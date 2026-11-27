package sandboxes

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/credentials"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/flowdproto/flowdfake"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandbox"
	"github.com/trick77/flow/internal/sandbox/sandboxfake"
	"github.com/trick77/flow/internal/store"
)

// dialer hands out the fake flowd, which answers only while the fake runtime says the sandbox is reachable over
// its NIC, as a real flowd behind a cut-off NIC does.
type dialer struct {
	fake      *flowdfake.Fake
	rt        *sandboxfake.Runtime
	endpoints []flowdproto.Endpoint
}

func (d *dialer) Dial(e flowdproto.Endpoint) flowdproto.API {
	d.endpoints = append(d.endpoints, e)
	return reachable{Fake: d.fake, rt: d.rt, name: e.Sandbox}
}

type reachable struct {
	*flowdfake.Fake
	rt   *sandboxfake.Runtime
	name string
}

func (r reachable) Hello(ctx context.Context) (flowdproto.Hello, error) {
	if r.rt != nil && !r.rt.Reachable(r.name) {
		return flowdproto.Hello{}, flowdproto.ErrUnavailable
	}
	return r.Fake.Hello(ctx)
}

func labNetwork() sandbox.Network {
	return sandbox.Network{Bridge: "flowbr0", Subnet: netip.MustParsePrefix("172.30.0.0/16"),
		Range: netip.MustParsePrefix("172.30.128.0/17"), Gateway: netip.MustParseAddr("172.30.0.1"),
		Resolvers: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
}

type rig struct {
	m      *Manager
	rt     *sandboxfake.Runtime
	d      *dialer
	ps     projects.SQL
	db     *sql.DB
	events []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ps := projects.SQL{DB: db}
	if err := ps.Create(t.Context(), projects.Project{ID: "01a21998-5a4f-7828-81be-b679dbb3324e", Name: "demo"},
		[]projects.Repo{{ID: "r1", FullName: "trick77/toolkit", DefaultBranch: "master"}}); err != nil {
		t.Fatal(err)
	}
	sealer, _ := credentials.NewAESSealer(bytes.Repeat([]byte{1}, 32))
	r := &rig{rt: sandboxfake.New(), ps: ps, db: db}
	r.d = &dialer{fake: flowdfake.New("sb"), rt: r.rt}
	r.m = &Manager{Store: SQL{DB: db}, Projects: ps, Runtime: r.rt, Dialer: r.d, Sealer: sealer, Image: "flow-base",
		Network: labNetwork(), Memory: 1 << 30, Version: "fake", Log: slog.New(slog.DiscardHandler), Wait: time.Second,
		Poll: time.Millisecond, Flowd: func(arch string) ([]byte, error) { return []byte("bin-" + arch), nil },
		Changed: func(id string) { r.events = append(r.events, id) }}
	// The fake flowd runs this build's release, as one delivered before does.
	rel, err := r.m.Release("amd64")
	if err != nil {
		t.Fatal(err)
	}
	r.d.fake.SetRelease(rel)
	return r
}

// release is the manager's release for the fake's architecture.
func (r *rig) release(t *testing.T) string {
	t.Helper()
	rel, err := r.m.Release("amd64")
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

const pid = "01a21998-5a4f-7828-81be-b679dbb3324e"

func TestEnsure(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Warm(ctx, pid); !errors.Is(err, ErrNotFound) {
		t.Errorf("Warm before setup: %v", err)
	}
	api, err := r.m.Ensure(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	in, ok := r.rt.Instance("flow-b679dbb3324e")
	if !ok || in.Status != sandbox.StatusRunning || !in.Spec.Nesting || in.Limits.MemoryBytes != 1<<30 {
		t.Fatalf("instance %+v", in)
	}
	if len(in.Files) != 0 {
		t.Errorf("flowd delivered although its version matched: %v", in.Files)
	}
	if again, _ := r.m.Ensure(ctx, pid); again != api || len(r.d.endpoints) != 1 {
		t.Error("second Ensure dialled again")
	}
	sb, _ := r.m.Store.ByProject(ctx, pid)
	if sb.State != StateWarm || sb.FlowdVersion != r.release(t) || len(sb.FlowdToken) == 0 || sb.Arch != "amd64" {
		t.Errorf("row %+v", sb)
	}
	if d := r.m.Describe(ctx, pid); d.State != "warm" || d.FlowdVersion != r.release(t) {
		t.Errorf("Describe = %+v", d)
	}
	token := r.d.endpoints[0].Token
	if r.d.endpoints[0].Address != "172.30.128.1:7433" || strings.Contains(string(sb.FlowdToken), token) {
		t.Errorf("endpoint %+v", r.d.endpoints[0])
	}
	want := labNetwork().NIC(netip.MustParseAddr("172.30.128.1"))
	if in.NIC.Bridge != want.Bridge || in.NIC.Address != want.Address || in.NIC.Gateway != want.Gateway ||
		len(in.Guest) != 1 || !bytes.Equal(in.Guest[0].Content, want.GuestFiles()[0].Content) {
		t.Errorf("NIC %+v, guest %+v", in.NIC, in.Guest)
	}

	// A new build delivers flowd: release, unit, config, one exec.
	r.m.Forget(pid)
	r.m.Version = "2.0.0"
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	in, _ = r.rt.Instance("flow-b679dbb3324e")
	if rel := r.release(t); !strings.HasPrefix(rel, "2.0.0-") {
		t.Errorf("release %s does not carry the version", rel)
	}
	for _, p := range []string{layout.Release(r.release(t)) + "/flowd.new", layout.Unit, layout.Config} {
		if _, ok := in.Files[p]; !ok {
			t.Errorf("not pushed: %s", p)
		}
	}
	if !strings.Contains(string(in.Files[layout.Config].Content), token) || in.Files[layout.Config].Mode != 0o600 {
		t.Error("config lacks the kept token or is not 0600")
	}
	if len(r.rt.Execs) != 1 || !strings.Contains(r.rt.Execs[0].Args[2], "systemctl restart flowd.service") {
		t.Errorf("execs %+v", r.rt.Execs)
	}

	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Warm(ctx, pid); !errors.Is(err, ErrNotWarm) {
		t.Errorf("Warm after stop: %v", err)
	}
	if in, _ := r.rt.Instance("flow-b679dbb3324e"); in.Status != sandbox.StatusStopped {
		t.Error("instance still running")
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Warm(ctx, pid); err != nil {
		t.Errorf("Warm after start: %v", err)
	}
	if len(r.events) == 0 {
		t.Error("no change announced")
	}
}

// Warm never boots a sandbox, even one Flow's records call warm (after a host reboot, say).
// Forget only drops a cache entry: it must not wait for a bring-up that holds the project's lock for minutes (the
// start endpoint calls it and is documented to answer at once).
func TestForget_doesNotWaitForABringUp(t *testing.T) {
	r := newRig(t)
	unlock := r.m.lock(pid) // a bring-up in progress
	defer unlock()
	done := make(chan struct{})
	go func() {
		r.m.Forget(pid)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Forget waited for the bring-up's lock")
	}
}

func TestWarm_neverStarts(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	_ = r.rt.Stop(ctx, "flow-b679dbb3324e", time.Second) // behind Flow's back
	r.m.Forget(pid)
	if _, err := r.m.Warm(ctx, pid); !errors.Is(err, ErrNotWarm) {
		t.Errorf("Warm of a stopped instance: %v", err)
	}
	if in, _ := r.rt.Instance("flow-b679dbb3324e"); in.Status != sandbox.StatusRunning && in.Status != sandbox.StatusStopped {
		t.Fatal(in.Status)
	} else if in.Status == sandbox.StatusRunning {
		t.Error("Warm started the sandbox")
	}
	if d := r.m.Describe(ctx, pid); d.State != "stopped" {
		t.Errorf("state after Warm found it stopped: %s", d.State)
	}
}

// A cached connection does not hide a sandbox stopped behind Flow's back: Warm checks the instance.
func TestWarm_seesAStopBehindTheCache(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	_ = r.rt.Stop(ctx, "flow-b679dbb3324e", time.Second) // no Forget: the cache still holds the connection
	if _, err := r.m.Warm(ctx, pid); !errors.Is(err, ErrNotWarm) {
		t.Errorf("Warm with a stale cache: %v", err)
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatalf("Ensure after: %v", err)
	}
	if in, _ := r.rt.Instance("flow-b679dbb3324e"); in.Status != sandbox.StatusRunning {
		t.Error("Ensure trusted the stale cache and did not start the sandbox")
	}
}

// cancelling runtime refuses work on a cancelled context, as the real client does.
type cancelling struct{ *sandboxfake.Runtime }

func (c cancelling) Start(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Runtime.Start(ctx, name)
}

// The caller leaving (a page reload) does not mark a healthy sandbox failed: bring-up runs to its end on its own
// context.
func TestEnsure_callerCancelDoesNotFailTheSandbox(t *testing.T) {
	r := newRig(t)
	r.m.Runtime = cancelling{r.rt}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = r.m.Ensure(ctx, pid)
	if d := r.m.Describe(t.Context(), pid); d.State != "warm" {
		t.Errorf("state after the caller left: %s %s", d.State, d.ErrorCode)
	}
}

// slowFlowd answers hello only after a few tries, as flowd does right after the sandbox boots.
type slowFlowd struct {
	*flowdfake.Fake
	fails int
}

func (s *slowFlowd) Hello(ctx context.Context) (flowdproto.Hello, error) {
	if s.fails > 0 {
		s.fails--
		return flowdproto.Hello{}, flowdproto.ErrUnavailable
	}
	return s.Fake.Hello(ctx)
}

type slowDialer struct{ api *slowFlowd }

func (d slowDialer) Dial(flowdproto.Endpoint) flowdproto.API { return d.api }

// After a start, an installed flowd is waited for, not delivered again (Incus refuses to overwrite a running
// executable: "text file busy"); a delivery writes a temporary file and never replaces an existing release.
func TestStart_waitsForTheInstalledFlowd(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	slow := &slowFlowd{Fake: r.d.fake, fails: 3}
	r.m.Dialer = slowDialer{api: slow}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if in, _ := r.rt.Instance("flow-b679dbb3324e"); len(in.Files) != 0 || slow.fails != 0 {
		t.Errorf("flowd delivered again after a start (files %d, hellos left %d)", len(in.Files), slow.fails)
	}
	r.m.Forget(pid)
	r.m.Version = "3.0.0"
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	in, _ := r.rt.Instance("flow-b679dbb3324e")
	rel := layout.Release(r.release(t))
	if _, ok := in.Files[rel+"/flowd.new"]; !ok {
		t.Errorf("flowd not pushed to a temporary name: %v", in.Files)
	}
	script := r.rt.Execs[len(r.rt.Execs)-1].Args[2]
	if !strings.Contains(script, "[ -e "+rel+"/flowd ]") || !strings.Contains(script, "mv -f") {
		t.Errorf("activation does not keep an existing release: %s", script)
	}
}

func TestEnsure_failures(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, "missing"); !errors.Is(err, projects.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	r.rt.ExecFunc = func(string, sandbox.Command) (sandbox.Result, error) {
		return sandbox.Result{ExitCode: 1, Stderr: []byte("no systemd")}, nil
	}
	r.m.Version = "other"
	if _, err := r.m.Ensure(ctx, pid); err == nil || !strings.Contains(err.Error(), "no systemd") {
		t.Errorf("failed activation: %v", err)
	}
	if d := r.m.Describe(ctx, pid); d.State != "failed" || d.ErrorCode != "sandbox_unavailable" {
		t.Errorf("Describe after failure = %+v", d)
	}
	r.m.Flowd = func(string) ([]byte, error) { return nil, errors.New("no binary") }
	if _, err := r.m.Ensure(ctx, pid); err == nil {
		t.Error("missing binary: no error")
	}
	if d := r.m.Describe(ctx, "missing"); d.State != "none" {
		t.Errorf("Describe of nothing = %+v", d)
	}
	if err := r.m.Stop(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stop without sandbox: %v", err)
	}
	ctx2, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.m.poll(ctx2, func() (sandbox.State, bool, error) { return sandbox.State{}, false, nil }); err == nil {
		t.Error("poll ignored its context")
	}
	r.m.Wait = 0
	if _, err := r.m.poll(ctx, func() (sandbox.State, bool, error) { return sandbox.State{}, false, nil }); err == nil {
		t.Error("poll never timed out")
	}
}

func TestSQLAndInstanceName(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	s := r.m.Store.(SQL)
	if err := s.Put(ctx, Sandbox{ID: "s1", ProjectID: pid, Instance: "i", State: StateSetup}); err != nil {
		t.Fatal(err)
	}
	if err := s.Touch(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil {
		t.Errorf("List = %+v, %v", list, err)
	}
	if err := s.SetState(ctx, "nope", "warm", "", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetState missing: %v", err)
	}
	if n := InstanceName("01a21998-5a4f-7828-81be-b679dbb3324e"); n != "flow-b679dbb3324e" {
		t.Errorf("InstanceName = %s", n)
	}
}

func TestAPI(t *testing.T) {
	r := newRig(t)
	a := &API{Manager: r.m}
	do := func(h http.HandlerFunc, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", nil)
		req.SetPathValue("projectId", id)
		w := httptest.NewRecorder()
		h(w, req)
		return w
	}
	if w := do(a.GetSandbox, "nope"); w.Code != 404 {
		t.Errorf("unknown project: %d", w.Code)
	}
	if w := do(a.GetSandbox, pid); w.Code != 200 || !strings.Contains(w.Body.String(), `"none"`) {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}
	if w := do(a.StartSandbox, pid); w.Code != 202 {
		t.Errorf("start: %d", w.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.m.Describe(t.Context(), pid).State != "warm" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if w := do(a.StopSandbox, pid); w.Code != 202 || !strings.Contains(w.Body.String(), `"stopped"`) {
		t.Errorf("stop: %d %s", w.Code, w.Body)
	}
	_ = r.ps.DB.Close()
	if w := do(a.StopSandbox, pid); w.Code != 500 {
		t.Errorf("stop on a closed database: %d", w.Code)
	}
}
