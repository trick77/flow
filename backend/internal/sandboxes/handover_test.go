package sandboxes

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/flow/internal/credentials"
	"github.com/trick77/flow/internal/flowdclient"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/sandbox"
	"github.com/trick77/flow/internal/supervisor"
)

var testTime = time.Unix(1_000_000, 0)

// The release names the build, not only its version: a rebuild with the same version string but other bytes (an
// unstamped dev-<commit>-dirty) gets its own release directory and is delivered.
func TestRelease_namesTheBuild(t *testing.T) {
	r := newRig(t)
	bin, bundle := "bin-a", "bundle-a"
	r.m.Flowd = func(string) ([]byte, error) { return []byte(bin), nil }
	r.m.Sidecar = func() ([]byte, error) { return []byte(bundle), nil }
	first := r.release(t)
	if again := r.release(t); again != first || !strings.HasPrefix(first, "fake-") {
		t.Errorf("release %s then %s", first, again)
	}
	bin = "bin-b"
	second := r.release(t)
	bin, bundle = "bin-a", "bundle-b"
	third := r.release(t)
	if second == first || third == first || second == third {
		t.Errorf("releases %s %s %s: a different build kept a release", first, second, third)
	}
	if _, err := r.m.Ensure(t.Context(), pid); err != nil {
		t.Fatal(err)
	}
	in, _ := r.rt.Instance("flow-b679dbb3324e")
	archive := in.Files[layout.Release(third)+"/"+layout.SidecarArchive]
	if string(archive.Content) != "bundle-b" || archive.Mode != 0o600 {
		t.Errorf("sidecar bundle not delivered into the release: %+v", archive)
	}
}

// One home volume for the installation, mounted at the layout's path in every sandbox, also in one made before.
func TestEnsure_homeVolume(t *testing.T) {
	r := newRig(t)
	r.secondProject(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil { // made before the volume existed
		t.Fatal(err)
	}
	if in, _ := r.rt.Instance("flow-b679dbb3324e"); len(in.Mounts) != 0 {
		t.Fatalf("mounted without a volume configured: %v", in.Mounts)
	}
	r.m.Forget(pid)
	r.m.HomeVolume = "flow-home"
	for _, p := range []string{pid, pid2} {
		if _, err := r.m.Ensure(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if v, ok := r.rt.Volume("flow-home"); !ok || v.Shifted {
		t.Errorf("volume %+v %v", v, ok)
	}
	want := sandbox.Mount{Volume: "flow-home", Path: layout.HomeVolume}
	for _, name := range []string{"flow-b679dbb3324e", InstanceName(pid2)} {
		if in, _ := r.rt.Instance(name); !slices.Equal(in.Mounts, []sandbox.Mount{want}) {
			t.Errorf("%s mounts %v", name, in.Mounts)
		}
	}
}

// Limits are the settings', set before every start, so a changed setting reaches a stopped sandbox.
func TestStart_setsLimits(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.m.Memory, r.m.CPUs = 6<<30, 4
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if in, _ := r.rt.Instance("flow-b679dbb3324e"); in.Limits != (sandbox.Limits{MemoryBytes: 6 << 30, CPUs: 4}) {
		t.Errorf("limits %+v", in.Limits)
	}
}

// Every bring-up pushes the current set; PushEnv reaches warm sandboxes and starts no stopped one.
func TestPushEnv(t *testing.T) {
	r := newRig(t)
	r.secondProject(t)
	ctx := t.Context()
	rev := int64(0)
	r.m.Env = func(context.Context) (flowdproto.Env, error) {
		rev++
		return flowdproto.Env{Revision: rev, ClaudeOAuthToken: "fake-token"}, nil
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if e := r.d.fake.Env(); e.Revision != 1 || e.ClaudeOAuthToken != "fake-token" {
		t.Errorf("env after bring-up %+v", e)
	}
	if _, err := r.m.Ensure(ctx, pid2); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Stop(ctx, pid2); err != nil {
		t.Fatal(err)
	}
	starts := r.rt.Starts
	pushes := r.d.fake.EnvPushes()
	r.m.PushEnv(ctx)
	if r.d.fake.EnvPushes() != pushes+1 || r.rt.Starts != starts {
		t.Errorf("pushes %d → %d, starts %d → %d", pushes, r.d.fake.EnvPushes(), starts, r.rt.Starts)
	}
	r.m.Env = func(context.Context) (flowdproto.Env, error) { return flowdproto.Env{}, errors.New("db gone") }
	r.m.Forget(pid)
	if _, err := r.m.Ensure(ctx, pid); err == nil || !strings.Contains(err.Error(), "environment") {
		t.Errorf("bring-up with no set: %v", err)
	}
}

// repoGate holds the bring-up in EnsureRepo (after the set was pushed, before the sandbox is warm) until released.
type repoGate struct {
	flowdproto.API
	entered chan struct{}
	once    *sync.Once
	release chan struct{}
}

func (g repoGate) EnsureRepo(ctx context.Context, r flowdproto.RepoSpec) (flowdproto.RepoState, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return g.API.EnsureRepo(ctx, r)
}

type gateDialer struct {
	d    flowdproto.Dialer
	gate repoGate
}

func (g gateDialer) Dial(e flowdproto.Endpoint) flowdproto.API {
	gate := g.gate
	gate.API = g.d.Dial(e)
	return gate
}

// Warmed (which finishes archives made while the sandbox was stopped) hears of every bring-up that greeted flowd,
// with that flowd, and not of a call served from the cached connection.
func TestWarmed_afterEveryBringUp(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	warmed := make(chan flowdproto.API, 4)
	r.m.Warmed = func(projectID string, api flowdproto.API) {
		if projectID == pid {
			warmed <- api
		}
	}
	next := func(what string) flowdproto.API {
		t.Helper()
		select {
		case api := <-warmed:
			return api
		case <-time.After(5 * time.Second):
			t.Fatalf("no Warmed after %s", what)
			return nil
		}
	}
	api, err := r.m.Ensure(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if got := next("the first start"); got != api {
		t.Error("Warmed got another flowd than Ensure returned")
	}
	if _, err := r.m.Warm(ctx, pid); err != nil { // cached
		t.Fatal(err)
	}
	r.m.Forget(pid) // as after Flow's restart: the next call greets again
	if _, err := r.m.Warm(ctx, pid); err != nil {
		t.Fatal(err)
	}
	next("the reconnect")
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	next("the start after a stop")
	select {
	case <-warmed:
		t.Error("Warmed for a call served from the cached connection")
	case <-time.After(50 * time.Millisecond):
	}
}

// A credential stored while the sandbox is still being set up (its set already pushed, the repositories cloning)
// reaches it once the bring-up ends, instead of being skipped for the sandbox not being warm yet.
func TestPushEnv_reachesASandboxInSetup(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	token := "fake-old"
	var mu sync.Mutex
	rev := int64(0)
	r.m.Env = func(context.Context) (flowdproto.Env, error) {
		mu.Lock()
		defer mu.Unlock()
		rev++
		return flowdproto.Env{Revision: rev, ClaudeOAuthToken: token}, nil
	}
	gate := repoGate{entered: make(chan struct{}), once: &sync.Once{}, release: make(chan struct{})}
	r.m.Dialer = gateDialer{d: r.d, gate: gate}
	up := make(chan error, 1)
	go func() { _, err := r.m.Ensure(ctx, pid); up <- err }()
	<-gate.entered
	if sb, _ := r.m.Store.ByProject(ctx, pid); sb.State != StateSetup || r.d.fake.Env().ClaudeOAuthToken != "fake-old" {
		t.Fatalf("not held in setup with the old set: %s %+v", sb.State, r.d.fake.Env())
	}
	mu.Lock()
	token = "fake-new"
	mu.Unlock()
	pushed := make(chan struct{})
	go func() { r.m.PushEnv(ctx); close(pushed) }()
	time.Sleep(20 * time.Millisecond) // PushEnv now waits for the bring-up
	close(gate.release)
	if err := <-up; err != nil {
		t.Fatal(err)
	}
	<-pushed
	if e := r.d.fake.Env(); e.ClaudeOAuthToken != "fake-new" {
		t.Errorf("sandbox warm with the set from before the change: %+v", e)
	}
}

// A sealed flowd token that no longer opens (another seal key, as dev mode makes per start) is made again and
// delivered, instead of failing every bring-up.
func TestToken_rotatesWhenUnreadable(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	old := r.d.endpoints[0].Token
	r.m.Sealer, _ = credentials.NewAESSealer(bytes.Repeat([]byte{2}, 32))
	r.m.Forget(pid)
	r.d.fake.SetRelease("") // flowd refuses the new token: it answers nothing Flow can use
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	last := r.d.endpoints[len(r.d.endpoints)-1].Token
	in, _ := r.rt.Instance("flow-b679dbb3324e")
	if last == old || !strings.Contains(string(in.Files[layout.Config].Content), last) {
		t.Errorf("token not rotated and delivered (old %s, new %s)", old, last)
	}

	// After a start, a flowd that refuses the new token is not waited for (Wait is long), it gets the delivery.
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.m.Sealer, _ = credentials.NewAESSealer(bytes.Repeat([]byte{3}, 32))
	r.d.fake.SetRelease(r.release(t))
	r.m.Dialer, r.m.Wait = tokenDialer{d: r.d}, 5*time.Second
	t0 := time.Now()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Errorf("waited %s on a flowd refusing the rotated token", d)
	}
}

// tokenFlowd answers only the token in the config last pushed into its instance, as flowd does.
type tokenFlowd struct {
	reachable
	token string
}

func (f tokenFlowd) Hello(ctx context.Context) (flowdproto.Hello, error) {
	if in, _ := f.rt.Instance(f.name); !strings.Contains(string(in.Files[layout.Config].Content), f.token) {
		return flowdproto.Hello{}, flowdproto.ErrUnauthorized
	}
	return f.reachable.Hello(ctx)
}

type tokenDialer struct{ d *dialer }

func (td tokenDialer) Dial(e flowdproto.Endpoint) flowdproto.API {
	return tokenFlowd{reachable: td.d.Dial(e).(reachable), token: e.Token}
}

// Bundle packs a directory the same way every time: the same files give the same release.
func TestBundle(t *testing.T) {
	dir := t.TempDir()
	for p, body := range map[string]string{"package.json": `{"type":"module"}`, "dist/main.js": "x",
		"node_modules/a/cli.js": "y"} {
		_ = os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644) //nolint:gosec // test files
	}
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", ".bin"), 0o755)
	_ = os.Symlink("../a/cli.js", filepath.Join(dir, "node_modules", ".bin", "cli"))
	a, err := Bundle(dir)
	if err != nil || len(a) == 0 {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(dir, "dist/main.js"), testTime, testTime)
	b, _ := Bundle(dir)
	if !bytes.Equal(a, b) {
		t.Error("the same files packed differently")
	}
	if none, err := Bundle(filepath.Join(dir, "missing")); none != nil || err != nil {
		t.Errorf("missing directory: %d bytes, %v", len(none), err)
	}
	// BACKEND_SIDECAR_DIR pointing at a symlink packs what the link points to, not an empty archive.
	link := filepath.Join(t.TempDir(), "sidecar")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if c, err := Bundle(link); err != nil || !bytes.Equal(a, c) {
		t.Errorf("symlinked directory: %d bytes (want %d), %v", len(c), len(a), err)
	}
}

// fakeUnits is a sandbox without session or terminal units, for flowd's Hello.
type fakeUnits struct{}

func (fakeUnits) Start(context.Context, string, []string, []string) error {
	return errors.New("no units")
}
func (fakeUnits) StartRoot(context.Context, string, []string, []string) error {
	return errors.New("no units")
}
func (fakeUnits) State(context.Context, string) (flowdproto.UnitState, error) {
	return flowdproto.UnitMissing, nil
}
func (fakeUnits) List(context.Context, string) ([]string, error) { return nil, nil }
func (fakeUnits) Stop(context.Context, string) error             { return nil }

// realDialer reaches one real flowd (the supervisor's HTTP server) whatever the endpoint's address.
type realDialer struct {
	url string
	d   *flowdclient.Dialer
}

func (r realDialer) Dial(e flowdproto.Endpoint) flowdproto.API {
	e.Address = strings.TrimPrefix(r.url, "http://")
	return r.d.Dial(e)
}

// lockedBuffer is a log destination shared by goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A secret stored through the API travels sealed, then over flowd's API into flowd, and appears in no log on the
// way: Flow's, the credentials service's, flowd's, the default logger's (where unhandled API errors go), neither
// when the push works nor when flowd refuses it.
func TestCredentialPush_logsNoSecret(t *testing.T) {
	const secret = "fake-subscription-token-FAKE-0000"
	logged := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := newRig(t)
	ctx := t.Context()
	flowd := supervisor.New(supervisor.Options{Version: "test", Sandbox: "flow-b679dbb3324e", Root: t.TempDir(),
		Units: fakeUnits{}, Log: log})
	var refuseEnv atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// flowd's real handler, under whichever token Flow made for the sandbox.
		tok := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		if refuseEnv.Load() && req.URL.Path == "/v1/env" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"internal","detail":"cannot apply the set"}`))
			return
		}
		if req.URL.Path == "/v1/repos" { // no clone from GitHub in a unit test
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"repo":"trick77/toolkit","path":"/home/flow/work/toolkit","headSha":"abc"}`))
			return
		}
		flowd.Handler(tok).ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	r.m.Dialer, r.m.Log = realDialer{url: srv.URL, d: flowdclient.NewDialer()}, log
	creds := &credentials.Service{Store: credentials.SQL{DB: r.db}, Sealer: r.m.Sealer, Log: log}
	r.m.Env = creds.Env
	creds.Changed = func() { r.m.PushEnv(ctx) }
	api := &credentials.API{Service: creds}
	// flowd reports no release, so the bring-up delivers into the fake runtime, then finds flowd answering.
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/connections/api-keys",
		strings.NewReader(`{"kind":"claude_token","value":"  `+secret+`\n"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.CreateAPIKey(w, req)
	if w.Code != 201 || strings.Contains(w.Body.String(), secret) {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	h, err := flowd.Hello(ctx)
	if err != nil || h.EnvRevision == 0 {
		t.Fatalf("flowd has no set: %+v %v", h, err)
	}
	rows, _ := creds.Store.List(ctx)
	if len(rows) != 1 || bytes.Contains(rows[0].Sealed, []byte(secret)) || rows[0].EnvName != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("stored %+v", rows)
	}
	w = httptest.NewRecorder()
	api.GetConnections(w, httptest.NewRequest("GET", "/api/connections", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), `"claude_token"`) {
		t.Errorf("status: %d %s", w.Code, w.Body)
	}
	// A push flowd refuses is logged without the set.
	refuseEnv.Store(true)
	r.m.PushEnv(ctx)
	if out := logged.String(); strings.Contains(out, secret) || strings.Contains(out, "FAKE-0000") {
		t.Errorf("a log holds the secret:\n%s", out)
	} else if !strings.Contains(out, "credential stored") || !strings.Contains(out, "cannot apply the set") {
		t.Errorf("the refused push was not logged: %s", out)
	}
}

// flowd restarting on its own (systemd's Restart=always) loses the set it held in memory, while Flow keeps its
// connection. The next host start is refused as busy, so Flow does what it does for an unreachable flowd: forget the
// connection and bring the sandbox up again, which pushes the set before the start is tried again.
func TestFlowdRestart_setPushedBeforeTheNextHost(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	newFlowd := func() *supervisor.Supervisor {
		return supervisor.New(supervisor.Options{Version: "test", Sandbox: "flow-b679dbb3324e", Root: t.TempDir(),
			Units: fakeUnits{}, Log: slog.New(slog.DiscardHandler)})
	}
	var flowd atomic.Pointer[supervisor.Supervisor]
	flowd.Store(newFlowd())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/repos" { // no clone from GitHub in a unit test
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"repo":"trick77/toolkit","path":"/home/flow/work/toolkit","headSha":"abc"}`))
			return
		}
		tok := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		flowd.Load().Handler(tok).ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	r.m.Dialer = realDialer{url: srv.URL, d: flowdclient.NewDialer()}
	r.m.Env = func(context.Context) (flowdproto.Env, error) {
		return flowdproto.Env{Revision: time.Now().UnixMicro(), ClaudeOAuthToken: "fake-token"}, nil
	}
	spec := flowdproto.SessionSpec{ID: "s1", Name: "demo", Provider: "claude", ClaudeAuth: "subscription_token"}
	api, err := r.m.Ensure(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	flowd.Store(newFlowd()) // the restart: no set
	if _, err := api.EnsureSession(ctx, spec); !errors.Is(err, flowdproto.ErrUnavailable) {
		t.Fatalf("host start on a flowd without a set: %v", err)
	}
	r.m.Forget(pid) // sessions.Service.Host on ErrUnavailable
	if api, err = r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if h, err := api.Hello(ctx); err != nil || h.EnvRevision == 0 {
		t.Fatalf("set not pushed again: %+v %v", h, err)
	}
	// Past the refusal now: this flowd has no units to start, which is the next error.
	if _, err := api.EnsureSession(ctx, spec); errors.Is(err, flowdproto.ErrUnavailable) {
		t.Errorf("still refused after the push: %v", err)
	}
}
