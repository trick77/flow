package incus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/sandbox"
)

// fake is a small Incus: instances, files, exec, volumes, operations that finish at once.
type fake struct {
	mu        sync.Mutex
	instances map[string]*fakeInst
	volumes   map[string]bool
	ops       map[string]map[string]any // id -> metadata (status "Success" or err)
	logs      map[string]string
	n         int
	projects  []string
	// bridges are the host bridges and their ports; a NIC's parent not in it is missing.
	bridges map[string][]string
}

type fakeInst struct {
	status  string
	config  map[string]string
	devices map[string]map[string]string
	files   map[string]string // path -> content; "" + dir marker for directories
	dirs    map[string]bool
}

func newFake() *fake {
	return &fake{instances: map[string]*fakeInst{}, volumes: map[string]bool{}, ops: map[string]map[string]any{},
		logs: map[string]string{}, bridges: map[string][]string{"flowbr0": {"veth0"}}}
}

func stringMap(v any) map[string]string {
	m := map[string]string{}
	for k, vv := range v.(map[string]any) {
		m[k] = vv.(string)
	}
	return m
}

func write(w http.ResponseWriter, v map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func sync200(w http.ResponseWriter, md any) {
	write(w, map[string]any{"type": "sync", "status": "Success", "status_code": 200, "metadata": md})
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	write(w, map[string]any{"type": "error", "error_code": code, "error": msg})
}

// async records an operation with the given outcome and answers with it.
func (f *fake) async(w http.ResponseWriter, errMsg string, md map[string]any) {
	f.n++
	id := "op" + string(rune('a'+f.n))
	op := map[string]any{"id": id, "status": "Success", "status_code": 200, "metadata": md}
	if errMsg != "" {
		op["status"], op["status_code"], op["err"] = "Failure", 400, errMsg
	}
	f.ops[id] = op
	w.WriteHeader(http.StatusAccepted)
	write(w, map[string]any{"type": "async", "status": "Operation created", "operation": "/1.0/operations/" + id,
		"metadata": op})
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects = append(f.projects, r.URL.Query().Get("project"))
	p := r.URL.Path
	parts := strings.Split(strings.TrimPrefix(p, "/1.0/"), "/")
	var body map[string]any
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch {
	case parts[0] == "operations" && len(parts) == 3 && parts[2] == "wait":
		op, ok := f.ops[parts[1]]
		if !ok {
			fail(w, 404, "Operation not found")
			return
		}
		if parts[1] == "hang" {
			op = map[string]any{"id": "hang", "status": "Running"}
		}
		sync200(w, op)
	case parts[0] == "operations" && r.Method == http.MethodDelete:
		sync200(w, map[string]any{})
	case parts[0] == "instances" && len(parts) == 1 && r.Method == http.MethodPost:
		name := body["name"].(string)
		if _, ok := f.instances[name]; ok {
			fail(w, 409, "Instance \""+name+"\" already exists")
			return
		}
		src := body["source"].(map[string]any)
		in := &fakeInst{status: "Stopped", config: map[string]string{}, devices: map[string]map[string]string{},
			files: map[string]string{}, dirs: map[string]bool{"/": true, "/etc": true, "/opt": true,
				"/etc/systemd/network": true}}
		if src["type"] == "copy" {
			orig, ok := f.instances[src["source"].(string)]
			if !ok {
				f.async(w, "Instance not found", nil)
				return
			}
			*in = *orig
			in.status = "Stopped"
			in.devices = map[string]map[string]string{}
			for k, d := range orig.devices {
				in.devices[k] = maps.Clone(d)
			}
		}
		if c, ok := body["config"].(map[string]any); ok {
			for k, v := range c {
				in.config[k] = v.(string)
			}
		}
		if d, ok := body["devices"].(map[string]any); ok {
			for k, v := range d {
				in.devices[k] = stringMap(v)
			}
		}
		f.instances[name] = in
		f.async(w, "", nil)
	case parts[0] == "instances" && len(parts) == 1:
		var out []map[string]any
		for name, in := range f.instances {
			out = append(out, f.describe(name, in, true))
		}
		sync200(w, out)
	case parts[0] == "instances":
		in, ok := f.instances[parts[1]]
		if !ok {
			fail(w, 404, "Instance not found")
			return
		}
		f.instance(w, r, parts, in, body)
	case parts[0] == "networks" && len(parts) == 3 && parts[2] == "state":
		ports, ok := f.bridges[parts[1]]
		switch {
		case !ok:
			fail(w, 404, "Network not found")
		case parts[1] == "eth0":
			sync200(w, map[string]any{"type": "broadcast"})
		default:
			sync200(w, map[string]any{"type": "broadcast", "bridge": map[string]any{"upper_devices": ports}})
		}
	case parts[0] == "storage-pools" && len(parts) == 5 && r.Method == http.MethodGet:
		if !f.volumes[parts[4]] {
			fail(w, 404, "Storage pool volume not found")
			return
		}
		sync200(w, map[string]any{})
	case parts[0] == "storage-pools" && r.Method == http.MethodPost:
		f.volumes[body["name"].(string)] = true
		f.async(w, "", nil)
	default:
		fail(w, 404, "not found "+p)
	}
}

func (f *fake) describe(name string, in *fakeInst, state bool) map[string]any {
	md := map[string]any{"name": name, "status": in.status, "architecture": "aarch64", "devices": in.devices,
		"config": in.config, "created_at": "2026-11-26T12:00:00Z"}
	if state {
		md["state"] = f.state(in)
	}
	return md
}

func (f *fake) state(in *fakeInst) map[string]any {
	st := map[string]any{"status": in.status, "processes": 14, "memory": map[string]any{"usage": 1000},
		"disk": map[string]any{"root": map[string]any{"usage": 5}}}
	if in.status == "Running" {
		st["network"] = map[string]any{"eth0": map[string]any{"hwaddr": "00:16:3e:00:00:01", "host_name": "veth0",
			"addresses": []any{
				map[string]any{"family": "inet6", "address": "fe80::1", "scope": "link"},
				map[string]any{"family": "inet", "address": in.devices["eth0"]["ipv4.address"], "scope": "global"}}}}
	}
	return st
}

func (f *fake) instance(w http.ResponseWriter, r *http.Request, parts []string, in *fakeInst, body map[string]any) {
	name := parts[1]
	sub := ""
	if len(parts) > 2 {
		sub = parts[2]
	}
	switch {
	case sub == "" && r.Method == http.MethodGet:
		sync200(w, f.describe(name, in, r.URL.Query().Get("recursion") == "1"))
	case sub == "" && r.Method == http.MethodDelete:
		if in.status == "Running" {
			f.async(w, "Instance is running", nil)
			return
		}
		delete(f.instances, name)
		f.async(w, "", nil)
	case sub == "" && (r.Method == http.MethodPatch || r.Method == http.MethodPut):
		// PATCH replaces each device it is given whole and keeps the others; PUT replaces them all. Incus refuses
		// IPv4 filtering on an unmanaged parent without an address (measured).
		if d, ok := body["devices"].(map[string]any); ok {
			for _, v := range d {
				if m := stringMap(v); m["security.ipv4_filtering"] == "true" && m["ipv4.address"] == "" {
					fail(w, 500, `Invalid devices: Device validation failed for "eth0": IPv4 filtering requires a manually specified ipv4.address when using an unmanaged parent bridge`)
					return
				}
			}
			if r.Method == http.MethodPut {
				in.devices = map[string]map[string]string{}
			}
			for k, v := range d {
				in.devices[k] = stringMap(v)
			}
		}
		if c, ok := body["config"].(map[string]any); ok {
			for k, v := range c {
				in.config[k] = v.(string)
			}
		}
		f.async(w, "", nil)
	case sub == "state" && r.Method == http.MethodGet:
		sync200(w, f.state(in))
	case sub == "state":
		switch body["action"] {
		case "start":
			if in.status == "Running" {
				f.async(w, "The instance is already running", nil)
				return
			}
			if p := in.devices["eth0"]["parent"]; p != "" && f.bridges[p] == nil {
				f.async(w, `Failed to start device "eth0": Parent device "`+p+`" doesn't exist`, nil)
				return
			}
			in.status = "Running"
		case "stop":
			if body["force"] != true && in.config["stuck"] == "true" {
				f.async(w, "shutdown timed out", nil)
				return
			}
			in.status = "Stopped"
		}
		f.async(w, "", nil)
	case sub == "files":
		// Incus serves the file API on stopped containers too (measured).
		p := r.URL.Query().Get("path")
		if !in.dirs[path.Dir(p)] {
			fail(w, 404, "Not Found")
			return
		}
		if r.Header.Get("X-Incus-type") == "directory" {
			in.dirs[p] = true
		} else {
			b, _ := io.ReadAll(r.Body)
			in.files[p] = r.Header.Get("X-Incus-mode") + ":" + string(b)
		}
		sync200(w, map[string]any{})
	case sub == "exec":
		cmd := body["command"].([]any)
		if cmd[0] == "hang" {
			w.WriteHeader(http.StatusAccepted)
			write(w, map[string]any{"type": "async", "operation": "/1.0/operations/hang"})
			return
		}
		f.n++
		out := "/1.0/instances/" + name + "/logs/exec-output/exec_" + string(rune('a'+f.n))
		f.logs[out+".stdout"], f.logs[out+".stderr"] = "out:"+cmd[len(cmd)-1].(string), "err"
		f.async(w, "", map[string]any{"return": 3, "output": map[string]any{"1": out + ".stdout", "2": out + ".stderr"}})
	case sub == "logs":
		p := r.URL.Path
		if r.Method == http.MethodDelete {
			delete(f.logs, p)
			sync200(w, map[string]any{})
			return
		}
		_, _ = io.WriteString(w, f.logs[p])
	default:
		fail(w, 404, "not found")
	}
}

func selfSigned(t *testing.T, cn string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{cn}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, der
}

func serve(t *testing.T, f *fake) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewUnstartedServer(f)
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	cert, _ := selfSigned(t, "flow-backend")
	return srv, newClient(Config{URL: srv.URL, Project: "flow"}, cert, srv.Certificate().Raw)
}

func TestLifecycle(t *testing.T) {
	f := newFake()
	_, c := serve(t, f)
	ctx := context.Background()
	spec := sandbox.Spec{Name: "sb1", Image: "flow-base", NIC: testNIC("172.30.128.1"), Nesting: true,
		Limits: sandbox.Limits{CPUs: 2, MemoryBytes: 1 << 30, Processes: 500}}
	if err := c.Create(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, spec); !errors.Is(err, sandbox.ErrExists) {
		t.Errorf("second create: %v", err)
	}
	if got := f.instances["sb1"].config; got["security.nesting"] != "true" || got["limits.memory"] != "1073741824" {
		t.Errorf("config %v", got)
	}
	want := map[string]string{"type": "nic", "nictype": "bridged", "parent": "flowbr0", "name": "eth0",
		"security.port_isolation": "true", "security.mac_filtering": "true", "security.ipv4_filtering": "true",
		"ipv4.address": "172.30.128.1"}
	if got := f.instances["sb1"].devices["eth0"]; !maps.Equal(got, want) {
		t.Errorf("eth0 = %v", got)
	}
	if got := f.instances["sb1"].files[layout.NetworkConfig]; !strings.HasPrefix(got, "0644:") ||
		!strings.Contains(got, "Address=172.30.128.1/16\n") {
		t.Errorf("guest network config of the stopped instance: %q", got)
	}
	if err := c.Start(ctx, "sb1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, "sb1"); err != nil {
		t.Errorf("starting a running instance: %v", err)
	}
	st, err := c.State(ctx, "sb1")
	if err != nil || st.Status != sandbox.StatusRunning || st.IPv4 != "172.30.128.1" || st.Arch != "arm64" ||
		st.DiskBytes != 5 || st.Address.String() != "172.30.128.1" || st.HostIface != "veth0" || st.MemoryLimitBytes != 1<<30 {
		t.Fatalf("State = %+v, %v", st, err)
	}
	if err := c.SetNIC(ctx, "sb1", testNIC("172.30.128.2")); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("SetNIC on a running instance: %v", err)
	}
	err = c.Push(ctx, "sb1", sandbox.File{Path: "/opt/flow/releases/v1/flowd", Content: []byte("bin"), Mode: 0o755},
		sandbox.File{Path: "/opt/flow/releases/v1/sidecar.tar", Content: []byte("tar")},
		sandbox.File{Path: "/etc/flow/flowd.json", Content: []byte("{}"), Mode: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.instances["sb1"].files["/opt/flow/releases/v1/flowd"]; got != "0755:bin" {
		t.Errorf("pushed %q", got)
	}
	if err := c.Push(ctx, "sb1", sandbox.File{Path: "relative"}); err == nil {
		t.Error("relative path pushed")
	}
	res, err := c.Exec(ctx, "sb1", sandbox.Command{Args: []string{"sh", "-c", "true"}, Dir: "/", Timeout: time.Second})
	if err != nil || res.ExitCode != 3 || string(res.Stdout) != "out:true" || string(res.Stderr) != "err" {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	if len(f.logs) != 0 {
		t.Errorf("exec logs left: %v", f.logs)
	}
	if _, err := c.Exec(ctx, "sb1", sandbox.Command{Args: []string{"hang"}, Timeout: time.Millisecond}); err == nil {
		t.Error("a hanging exec returned no error")
	}
	if err := c.EnsureVolume(ctx, sandbox.Volume{Name: "home", Shifted: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureVolume(ctx, sandbox.Volume{Name: "home"}); err != nil {
		t.Fatal(err)
	}
	m := sandbox.Mount{Volume: "home", Path: "/srv/flow-home"}
	if err := c.Attach(ctx, "sb1", m); err != nil {
		t.Fatal(err)
	}
	n := f.n
	if err := c.Attach(ctx, "sb1", m); err != nil || f.n != n {
		t.Errorf("second attach changed something: %v", err)
	}
	if err := c.SetLimits(ctx, "sb1", sandbox.Limits{CPUs: 4}); err != nil {
		t.Error(err)
	}
	if err := c.Delete(ctx, "sb1"); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("delete of a running instance: %v", err)
	}
	f.instances["sb1"].config["stuck"] = "true"
	if err := c.Stop(ctx, "sb1", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "sb1", time.Second); err != nil {
		t.Errorf("stopping a stopped instance: %v", err)
	}
	if err := c.SetNIC(ctx, "sb1", testNIC("172.30.128.9")); err != nil {
		t.Fatal(err)
	}
	if d, g := f.instances["sb1"].devices["eth0"], f.instances["sb1"].files[layout.NetworkConfig]; d["ipv4.address"] != "172.30.128.9" ||
		!strings.Contains(g, "Address=172.30.128.9/16\n") {
		t.Errorf("SetNIC: device %v, guest %q", d, g)
	}
	if err := c.Clone(ctx, "sb1", "sb2"); err != nil {
		t.Fatal(err)
	}
	if d, ok := f.instances["sb2"].devices["eth0"]; ok || f.instances["sb1"].devices["eth0"]["ipv4.address"] != "172.30.128.9" {
		t.Errorf("clone kept the NIC: %v", d)
	}
	if err := c.SetNIC(ctx, "sb2", sandbox.NIC{Bridge: "flowbr0"}); err == nil {
		t.Error("a filtered NIC without address was accepted")
	}
	if err := c.Clone(ctx, "nope", "sb3"); err == nil {
		t.Error("clone of a missing instance")
	}
	list, err := c.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if err := c.Delete(ctx, "sb2"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.State(ctx, "sb2"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("state of a deleted instance: %v", err)
	}
	for _, p := range f.projects {
		if p != "flow" {
			t.Fatalf("a request outside the project: %q", p)
		}
	}
}

func testNIC(addr string) sandbox.NIC {
	return sandbox.NIC{Bridge: "flowbr0", Address: netip.PrefixFrom(netip.MustParseAddr(addr), 16),
		Gateway: netip.MustParseAddr("172.30.0.1"), Resolvers: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
}

// A missing bridge is ErrNetworkMissing both when asked for and when Incus refuses a start for it.
func TestBridge(t *testing.T) {
	f := newFake()
	_, c := serve(t, f)
	ctx := context.Background()
	if ports, err := c.BridgePorts(ctx, "flowbr0"); err != nil || !slices.Equal(ports, []string{"veth0"}) {
		t.Errorf("ports %v, %v", ports, err)
	}
	if _, err := c.BridgePorts(ctx, "gone"); !errors.Is(err, sandbox.ErrNetworkMissing) {
		t.Errorf("missing bridge: %v", err)
	}
	f.bridges["eth0"] = nil
	if _, err := c.BridgePorts(ctx, "eth0"); err == nil || errors.Is(err, sandbox.ErrNetworkMissing) {
		t.Errorf("not a bridge: %v", err)
	}
	if err := c.Create(ctx, sandbox.Spec{Name: "sb", Image: "flow-base", NIC: testNIC("172.30.128.1")}); err != nil {
		t.Fatal(err)
	}
	delete(f.bridges, "flowbr0")
	if err := c.Start(ctx, "sb"); !errors.Is(err, sandbox.ErrNetworkMissing) {
		t.Errorf("start without bridge: %v", err)
	}
	if err := c.SetNIC(ctx, "missing", testNIC("172.30.128.1")); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("SetNIC of a missing instance: %v", err)
	}
	if err := c.Clone(ctx, "sb", "sb-copy"); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, sandbox.Spec{Name: "plain", Image: "flow-base"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Clone(ctx, "plain", "plain-copy"); err != nil {
		t.Errorf("clone without address: %v", err)
	}
}

func TestPinRefusesAnotherServer(t *testing.T) {
	srv := httptest.NewTLSServer(newFake())
	defer srv.Close()
	cert, other := selfSigned(t, "other")
	c := newClient(Config{URL: srv.URL, Project: "flow"}, cert, other)
	if _, err := c.List(context.Background()); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Errorf("unpinned server accepted: %v", err)
	}
}

func TestNew_readsTheFiles(t *testing.T) {
	dir := t.TempDir()
	cert, der := selfSigned(t, "flow-backend")
	keyDER, _ := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	files := map[string][]byte{"client.crt": certPEM, "server.crt": certPEM,
		"client.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), "bad.crt": []byte("x")}
	for name, b := range files {
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o600)
	}
	cfg := Config{URL: "https://127.0.0.1:8443/", Project: "flow", CertFile: filepath.Join(dir, "client.crt"),
		KeyFile: filepath.Join(dir, "client.key"), ServerCertFile: filepath.Join(dir, "server.crt")}
	if c, err := New(cfg); err != nil || c.base != "https://127.0.0.1:8443" || c.pool != "default" {
		t.Fatalf("New = %+v, %v", c, err)
	}
	for _, mod := range []func(*Config){
		func(c *Config) { c.CertFile = "missing" },
		func(c *Config) { c.ServerCertFile = "missing" },
		func(c *Config) { c.ServerCertFile = filepath.Join(dir, "bad.crt") },
	} {
		bad := cfg
		mod(&bad)
		if _, err := New(bad); err == nil {
			t.Errorf("New(%+v) succeeded", bad)
		}
	}
}

func TestErrors(t *testing.T) {
	for msg, want := range map[string]error{"Instance is not running": sandbox.ErrState,
		`Failed to start device "eth0": Parent device "flowbr0" doesn't exist`: sandbox.ErrNetworkMissing} {
		if err := apiError(400, msg); !errors.Is(err, want) {
			t.Errorf("%q → %v", msg, err)
		}
	}
	if err := apiError(500, "boom"); errors.Is(err, sandbox.ErrState) || errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("500 mapped to %v", err)
	}
	if goArch("x86_64") != "amd64" || goArch("riscv64") != "riscv64" {
		t.Error("goArch")
	}
	if s := toState(instance{Status: "Frozen"}); s.Status != sandbox.StatusOther {
		t.Errorf("frozen → %s", s.Status)
	}
}
