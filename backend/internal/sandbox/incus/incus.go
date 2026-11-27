// Package incus implements sandbox.Runtime against the Incus REST API (/1.0, ?project=, async operations waited on
// with /1.0/operations/{id}/wait) over HTTPS with the project-restricted client certificate and a pinned server
// certificate: its SAN never names the address Flow dials, so hostname verification is replaced by comparing the
// presented certificate with the one host-setup copied out. Standard library only: exec uses record-output (exit code
// and log files in the operation), not the websocket protocol. Tests that need a real Incus carry the incus build
// tag. Owner: sandboxes and Incus area.
package incus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/flow/internal/sandbox"
)

// Config is where and as whom Flow reaches Incus.
type Config struct {
	// URL is the API, https://127.0.0.1:8443.
	URL     string
	Project string
	// Pool is the storage pool of custom volumes.
	Pool string
	// CertFile and KeyFile are Flow's client certificate, restricted to Project.
	CertFile, KeyFile string
	// ServerCertFile is the Incus server certificate (PEM) to pin.
	ServerCertFile string
}

// Client is sandbox.Runtime over the REST API.
type Client struct {
	base    string
	project string
	pool    string
	http    *http.Client
}

var _ sandbox.Runtime = (*Client)(nil)

// New loads the certificates and returns a client.
func New(c Config) (*Client, error) {
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("incus client certificate: %w", err)
	}
	pemBytes, err := os.ReadFile(c.ServerCertFile)
	if err != nil {
		return nil, fmt.Errorf("incus server certificate: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("incus server certificate: no PEM certificate")
	}
	return newClient(c, cert, block.Bytes), nil
}

func newClient(c Config, cert tls.Certificate, pinned []byte) *Client {
	pin := sha256.Sum256(pinned)
	tc := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		// Verification is VerifyConnection's pin, not the system roots and the host name.
		InsecureSkipVerify: true, //nolint:gosec // replaced by the certificate pin below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || sha256.Sum256(cs.PeerCertificates[0].Raw) != pin {
				return errors.New("incus: server certificate does not match the pinned one")
			}
			return nil
		},
	}
	if c.Pool == "" {
		c.Pool = "default"
	}
	return &Client{base: strings.TrimRight(c.URL, "/"), project: c.Project, pool: c.Pool,
		http: &http.Client{Transport: &http.Transport{TLSClientConfig: tc}, Timeout: 10 * time.Minute}}
}

// response is Incus' envelope.
type response struct {
	Type      string          `json:"type"`
	Status    string          `json:"status"`
	Operation string          `json:"operation"`
	ErrorCode int             `json:"error_code"`
	Error     string          `json:"error"`
	Metadata  json.RawMessage `json:"metadata"`
}

type operation struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	StatusCode int             `json:"status_code"`
	Err        string          `json:"err"`
	Metadata   json.RawMessage `json:"metadata"`
}

// apiError maps Incus' status codes to sandbox's errors.
func apiError(code int, msg string) error {
	switch {
	case code == http.StatusNotFound:
		return fmt.Errorf("%w: %s", sandbox.ErrNotFound, msg)
	case code == http.StatusConflict || strings.Contains(msg, "already exists"):
		return fmt.Errorf("%w: %s", sandbox.ErrExists, msg)
	case strings.Contains(msg, "is not running") || strings.Contains(msg, "already running") ||
		strings.Contains(msg, "is running") || strings.Contains(msg, "already stopped"):
		return fmt.Errorf("%w: %s", sandbox.ErrState, msg)
	case strings.Contains(msg, "Parent device") && strings.Contains(msg, "doesn't exist"):
		// Start of an instance whose bridge is gone (measured, Incus 6.0.4: `Failed to start device "eth0": Parent
		// device "flowbr0" doesn't exist`).
		return fmt.Errorf("%w: %s", sandbox.ErrNetworkMissing, msg)
	}
	return fmt.Errorf("incus: %d %s", code, msg)
}

func (c *Client) url(p string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	q.Set("project", c.project)
	return c.base + p + "?" + q.Encode()
}

// do sends one request and returns the envelope; an async one is waited for unless wait is false.
func (c *Client) do(ctx context.Context, method, p string, q url.Values, body any, hdr http.Header) (response, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return response{}, err
		}
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(p, q), rd)
	if err != nil {
		return response{}, err
	}
	if _, raw := body.([]byte); body != nil && !raw {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("incus: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return response{}, fmt.Errorf("incus %s %s: status %d: %w", method, p, resp.StatusCode, err)
	}
	if r.Type == "error" {
		return r, apiError(r.ErrorCode, r.Error)
	}
	return r, nil
}

// sync runs a request and, when Incus answers with an operation, waits for it.
func (c *Client) sync(ctx context.Context, method, p string, body any) error {
	r, err := c.do(ctx, method, p, nil, body, nil)
	if err != nil || r.Type != "async" {
		return err
	}
	_, err = c.wait(ctx, r.Operation, 0)
	return err
}

// wait blocks on an operation; timeout 0 waits as long as ctx allows.
func (c *Client) wait(ctx context.Context, opPath string, timeout time.Duration) (operation, error) {
	q := url.Values{}
	secs := -1
	if timeout > 0 {
		secs = int(timeout.Seconds()) + 1
	}
	q.Set("timeout", strconv.Itoa(secs))
	opPath = strings.SplitN(opPath, "?", 2)[0]
	r, err := c.do(ctx, http.MethodGet, opPath+"/wait", q, nil, nil)
	if err != nil {
		return operation{}, err
	}
	var op operation
	if err := json.Unmarshal(r.Metadata, &op); err != nil {
		return op, err
	}
	switch op.Status {
	case "Success":
		return op, nil
	case "Running", "Pending":
		return op, fmt.Errorf("incus: operation %s still running after %s", op.ID, timeout)
	}
	return op, apiError(0, op.Err)
}

func inst(name string) string { return "/1.0/instances/" + url.PathEscape(name) }

// Create implements sandbox.Runtime: the instance with its NIC, then the guest network configuration pushed into
// the stopped instance (the file API works on stopped containers).
func (c *Client) Create(ctx context.Context, s sandbox.Spec) error {
	config := map[string]string{}
	if s.Nesting {
		config["security.nesting"] = "true"
	}
	limits(config, s.Limits)
	body := map[string]any{"name": s.Name, "type": "container",
		"source":  map[string]string{"type": "image", "alias": s.Image},
		"config":  config,
		"devices": map[string]any{"eth0": nicDevice(s.NIC)}}
	if err := c.sync(ctx, http.MethodPost, "/1.0/instances", body); err != nil {
		return err
	}
	return c.pushGuest(ctx, s.Name, s.NIC)
}

// nicDevice is the eth0 device: bridged on the host bridge (an unmanaged parent, so the project needs
// restricted.devices.nic=allow), isolated, filtered. Incus accepts ipv4.address on an unmanaged parent only together
// with ipv4_filtering, and then adds its `bridge incus` nftables chains for the port (measured).
func nicDevice(n sandbox.NIC) map[string]string {
	d := map[string]string{"type": "nic", "nictype": "bridged", "parent": n.Bridge, "name": "eth0",
		"security.port_isolation": "true", "security.mac_filtering": "true", "security.ipv4_filtering": "true"}
	if n.Address.IsValid() {
		d["ipv4.address"] = n.Address.Addr().String()
	}
	return d
}

func (c *Client) pushGuest(ctx context.Context, name string, n sandbox.NIC) error {
	if !n.Address.IsValid() {
		return nil
	}
	for _, f := range n.GuestFiles() {
		if err := c.pushOne(ctx, name, f.Path, f.Content, "file", f.Mode, f.UID, f.GID); err != nil {
			return err
		}
	}
	return nil
}

// SetNIC implements sandbox.Runtime.
func (c *Client) SetNIC(ctx context.Context, name string, n sandbox.NIC) error {
	st, err := c.State(ctx, name)
	if err != nil {
		return err
	}
	if st.Status != sandbox.StatusStopped {
		return fmt.Errorf("%w: set the NIC of %s: instance is %s", sandbox.ErrState, name, st.Status)
	}
	if err := c.sync(ctx, http.MethodPatch, inst(name), map[string]any{"devices": map[string]any{
		"eth0": nicDevice(n)}}); err != nil {
		return err
	}
	return c.pushGuest(ctx, name, n)
}

// BridgePorts implements sandbox.Runtime with the network state Incus reports for host interfaces it does not
// manage; the restricted certificate may read it.
func (c *Client) BridgePorts(ctx context.Context, bridge string) ([]string, error) {
	r, err := c.do(ctx, http.MethodGet, "/1.0/networks/"+url.PathEscape(bridge)+"/state", nil, nil, nil)
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", sandbox.ErrNetworkMissing, bridge)
	}
	if err != nil {
		return nil, err
	}
	var st struct {
		Bridge *struct {
			UpperDevices []string `json:"upper_devices"`
		} `json:"bridge"`
	}
	if err := json.Unmarshal(r.Metadata, &st); err != nil {
		return nil, err
	}
	if st.Bridge == nil {
		return nil, fmt.Errorf("incus: %s is not a bridge", bridge)
	}
	return st.Bridge.UpperDevices, nil
}

func limits(config map[string]string, l sandbox.Limits) {
	if l.CPUs > 0 {
		config["limits.cpu"] = strconv.Itoa(l.CPUs)
	}
	if l.MemoryBytes > 0 {
		config["limits.memory"] = strconv.FormatInt(l.MemoryBytes, 10)
	}
	if l.Processes > 0 {
		config["limits.processes"] = strconv.Itoa(l.Processes)
	}
}

// Clone implements sandbox.Runtime: the copy loses the source's eth0, so it has no network until SetNIC. Keeping
// the device without its address is not possible: Incus refuses IPv4 filtering on an unmanaged parent without a
// manually specified ipv4.address (measured), and a NIC without filtering could spoof.
func (c *Client) Clone(ctx context.Context, from, to string) error {
	if err := c.sync(ctx, http.MethodPost, "/1.0/instances", map[string]any{"name": to,
		"source": map[string]string{"type": "copy", "source": from}}); err != nil {
		return err
	}
	r, err := c.do(ctx, http.MethodGet, inst(to), nil, nil, nil)
	if err != nil {
		return err
	}
	var in instance
	if err := json.Unmarshal(r.Metadata, &in); err != nil {
		return err
	}
	if _, ok := in.Devices["eth0"]; !ok {
		return nil
	}
	delete(in.Devices, "eth0")
	// PUT, not PATCH: PATCH keeps every device it is not given.
	return c.sync(ctx, http.MethodPut, inst(to), map[string]any{"architecture": in.Architecture, "config": in.Config,
		"devices": in.Devices, "profiles": in.Profiles, "ephemeral": in.Ephemeral, "description": in.Description})
}

func (c *Client) state(ctx context.Context, name, action string, timeout time.Duration, force bool) error {
	err := c.sync(ctx, http.MethodPut, inst(name)+"/state", map[string]any{"action": action,
		"timeout": int(timeout.Seconds()), "force": force})
	return err
}

// Start implements sandbox.Runtime; starting a running instance is not an error.
func (c *Client) Start(ctx context.Context, name string) error {
	err := c.state(ctx, name, "start", 30*time.Second, false)
	if errors.Is(err, sandbox.ErrState) {
		if st, serr := c.State(ctx, name); serr == nil && st.Status == sandbox.StatusRunning {
			return nil
		}
	}
	return err
}

// Stop implements sandbox.Runtime: a clean shutdown within timeout, then a forced one.
func (c *Client) Stop(ctx context.Context, name string, timeout time.Duration) error {
	if st, err := c.State(ctx, name); err != nil {
		return err
	} else if st.Status == sandbox.StatusStopped {
		return nil
	}
	if err := c.state(ctx, name, "stop", timeout, false); err == nil {
		return nil
	}
	return c.state(ctx, name, "stop", timeout, true)
}

// Delete implements sandbox.Runtime.
func (c *Client) Delete(ctx context.Context, name string) error {
	err := c.sync(ctx, http.MethodDelete, inst(name), nil)
	return err
}

// SetLimits implements sandbox.Runtime.
func (c *Client) SetLimits(ctx context.Context, name string, l sandbox.Limits) error {
	config := map[string]string{}
	limits(config, l)
	err := c.sync(ctx, http.MethodPatch, inst(name), map[string]any{"config": config})
	return err
}

// EnsureVolume implements sandbox.Runtime.
func (c *Client) EnsureVolume(ctx context.Context, v sandbox.Volume) error {
	vols := "/1.0/storage-pools/" + url.PathEscape(c.pool) + "/volumes"
	_, err := c.do(ctx, http.MethodGet, vols+"/custom/"+url.PathEscape(v.Name), nil, nil, nil)
	if !errors.Is(err, sandbox.ErrNotFound) {
		return err
	}
	config := map[string]string{}
	if v.Shifted {
		config["security.shifted"] = "true"
	}
	err = c.sync(ctx, http.MethodPost, vols, map[string]any{"name": v.Name, "type": "custom", "config": config})
	if errors.Is(err, sandbox.ErrExists) {
		return nil
	}
	return err
}

type instance struct {
	Name         string                       `json:"name"`
	Status       string                       `json:"status"`
	Architecture string                       `json:"architecture"`
	CreatedAt    time.Time                    `json:"created_at"`
	LastUsedAt   time.Time                    `json:"last_used_at"`
	Config       map[string]string            `json:"config"`
	Devices      map[string]map[string]string `json:"devices"`
	Profiles     []string                     `json:"profiles"`
	Ephemeral    bool                         `json:"ephemeral"`
	Description  string                       `json:"description"`
	State        *instanceState               `json:"state"`
}

type instanceState struct {
	Status string `json:"status"`
	Memory struct {
		Usage     int64 `json:"usage"`
		UsagePeak int64 `json:"usage_peak"`
	} `json:"memory"`
	CPU struct {
		Usage int64 `json:"usage"`
	} `json:"cpu"`
	Disk    map[string]struct{ Usage int64 } `json:"disk"`
	Network map[string]struct {
		Hwaddr    string `json:"hwaddr"`
		HostName  string `json:"host_name"`
		Addresses []struct {
			Family  string `json:"family"`
			Address string `json:"address"`
			Scope   string `json:"scope"`
		} `json:"addresses"`
	} `json:"network"`
	Processes int `json:"processes"`
}

// Attach implements sandbox.Runtime; the device is named after the volume.
func (c *Client) Attach(ctx context.Context, name string, m sandbox.Mount) error {
	r, err := c.do(ctx, http.MethodGet, inst(name), nil, nil, nil)
	if err != nil {
		return err
	}
	var in instance
	if err := json.Unmarshal(r.Metadata, &in); err != nil {
		return err
	}
	if d, ok := in.Devices[m.Volume]; ok && d["path"] == m.Path && d["source"] == m.Volume {
		return nil
	}
	err = c.sync(ctx, http.MethodPatch, inst(name), map[string]any{"devices": map[string]any{m.Volume: map[string]string{
		"type": "disk", "pool": c.pool, "source": m.Volume, "path": m.Path}}})
	return err
}

// Push implements sandbox.Runtime: parents are created first (Incus refuses a file in a missing directory), then
// each file replaces what was there.
func (c *Client) Push(ctx context.Context, name string, files ...sandbox.File) error {
	made := map[string]bool{}
	for _, f := range files {
		if !path.IsAbs(f.Path) {
			return fmt.Errorf("incus push: %q is not absolute", f.Path)
		}
		var dirs []string
		for d := path.Dir(f.Path); d != "/" && !made[d]; d = path.Dir(d) {
			dirs = append(dirs, d)
		}
		for i := len(dirs) - 1; i >= 0; i-- {
			if err := c.pushOne(ctx, name, dirs[i], nil, "directory", 0o755, 0, 0); err != nil {
				return err
			}
			made[dirs[i]] = true
		}
		if err := c.pushOne(ctx, name, f.Path, f.Content, "file", f.Mode, f.UID, f.GID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) pushOne(ctx context.Context, name, p string, content []byte, typ string, mode os.FileMode, uid, gid int) error {
	if mode == 0 {
		mode = 0o644
	}
	h := http.Header{}
	h.Set("X-Incus-type", typ)
	h.Set("X-Incus-mode", fmt.Sprintf("%04o", mode.Perm()))
	h.Set("X-Incus-uid", strconv.Itoa(uid))
	h.Set("X-Incus-gid", strconv.Itoa(gid))
	if typ == "file" {
		h.Set("X-Incus-write", "overwrite")
		h.Set("Content-Type", "application/octet-stream")
	}
	if content == nil {
		content = []byte{}
	}
	_, err := c.do(ctx, http.MethodPost, inst(name)+"/files", url.Values{"path": {p}}, content, h)
	if err != nil {
		return fmt.Errorf("push %s: %w", p, err)
	}
	return nil
}

// Exec implements sandbox.Runtime: a non-interactive exec with recorded output, waited for at most c.Timeout;
// the output logs are read and deleted.
func (c *Client) Exec(ctx context.Context, name string, cmd sandbox.Command) (sandbox.Result, error) {
	timeout := cmd.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	env := cmd.Env
	if env == nil {
		env = map[string]string{}
	}
	body := map[string]any{"command": cmd.Args, "environment": env, "record-output": true,
		"wait-for-websocket": false, "interactive": false, "user": cmd.UID, "group": cmd.UID}
	if cmd.Dir != "" {
		body["cwd"] = cmd.Dir
	}
	r, err := c.do(ctx, http.MethodPost, inst(name)+"/exec", nil, body, nil)
	if err != nil {
		return sandbox.Result{}, err
	}
	op, err := c.wait(ctx, r.Operation, timeout)
	if err != nil {
		// Never leave the command running past its bound.
		_, _ = c.do(context.WithoutCancel(ctx), http.MethodDelete, strings.SplitN(r.Operation, "?", 2)[0], nil, nil, nil)
		return sandbox.Result{}, err
	}
	var md struct {
		Return int               `json:"return"`
		Output map[string]string `json:"output"`
	}
	if err := json.Unmarshal(op.Metadata, &md); err != nil {
		return sandbox.Result{}, err
	}
	res := sandbox.Result{ExitCode: md.Return}
	for fd, dst := range map[string]*[]byte{"1": &res.Stdout, "2": &res.Stderr} {
		p, ok := md.Output[fd]
		if !ok {
			continue
		}
		p = strings.SplitN(p, "?", 2)[0]
		*dst, err = c.raw(ctx, p)
		if err != nil {
			return res, err
		}
		_, _ = c.do(ctx, http.MethodDelete, p, nil, nil, nil)
	}
	return res, nil
}

// raw fetches a file the API serves as is (exec output logs).
func (c *Client) raw(ctx context.Context, p string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(p, nil), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp.StatusCode, p)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// State implements sandbox.Runtime.
func (c *Client) State(ctx context.Context, name string) (sandbox.State, error) {
	r, err := c.do(ctx, http.MethodGet, inst(name), url.Values{"recursion": {"1"}}, nil, nil)
	if err != nil {
		return sandbox.State{}, err
	}
	var in instance
	if err := json.Unmarshal(r.Metadata, &in); err != nil {
		return sandbox.State{}, err
	}
	if in.State == nil {
		r, err := c.do(ctx, http.MethodGet, inst(name)+"/state", nil, nil, nil)
		if err != nil {
			return sandbox.State{}, err
		}
		in.State = &instanceState{}
		if err := json.Unmarshal(r.Metadata, in.State); err != nil {
			return sandbox.State{}, err
		}
	}
	return toState(in), nil
}

func toState(in instance) sandbox.State {
	s := sandbox.State{Name: in.Name, CreatedAt: in.CreatedAt, LastUsedAt: in.LastUsedAt, Arch: goArch(in.Architecture)}
	switch strings.ToLower(in.Status) {
	case "running":
		s.Status = sandbox.StatusRunning
	case "stopped":
		s.Status = sandbox.StatusStopped
	default:
		s.Status = sandbox.StatusOther
	}
	if a, err := netip.ParseAddr(in.Devices["eth0"]["ipv4.address"]); err == nil {
		s.Address = a
	}
	s.MemoryLimitBytes, _ = strconv.ParseInt(in.Config["limits.memory"], 10, 64)
	if st := in.State; st != nil {
		s.MemoryBytes, s.MemoryPeakBytes, s.CPUNanos, s.Processes = st.Memory.Usage, st.Memory.UsagePeak, st.CPU.Usage, st.Processes
		if d, ok := st.Disk["root"]; ok {
			s.DiskBytes = d.Usage
		}
		if n, ok := st.Network["eth0"]; ok {
			s.MAC, s.HostIface = n.Hwaddr, n.HostName
			for _, a := range n.Addresses {
				if a.Family == "inet" && a.Scope == "global" {
					s.IPv4 = a.Address
					break
				}
			}
		}
	}
	return s
}

func goArch(a string) string {
	switch a {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	}
	return a
}

// List implements sandbox.Runtime.
func (c *Client) List(ctx context.Context) ([]sandbox.State, error) {
	r, err := c.do(ctx, http.MethodGet, "/1.0/instances", url.Values{"recursion": {"2"}}, nil, nil)
	if err != nil {
		return nil, err
	}
	var ins []instance
	if err := json.Unmarshal(r.Metadata, &ins); err != nil {
		return nil, err
	}
	out := make([]sandbox.State, 0, len(ins))
	for _, in := range ins {
		out = append(out, toState(in))
	}
	return out, nil
}
