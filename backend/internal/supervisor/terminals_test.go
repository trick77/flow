package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdclient"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/host"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/terminals/term"
)

// echoShell is a shell that echoes its input; the line "exit N\r" ends it with N (one digit).
type echoShell struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	exit chan int
	once sync.Once
}

func newEchoShell() *echoShell {
	r, w := io.Pipe()
	return &echoShell{r: r, w: w, exit: make(chan int, 1)}
}

func (e *echoShell) Read(p []byte) (int, error) { return e.r.Read(p) }
func (e *echoShell) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if s := string(p); strings.HasPrefix(s, "exit ") && len(s) >= 6 {
		e.end(int(s[5] - '0'))
	}
	return n, err
}
func (e *echoShell) Close() error          { return e.r.Close() }
func (e *echoShell) Resize(int, int) error { return nil }
func (e *echoShell) Pid() int              { return 7 }
func (e *echoShell) Hangup()               { e.end(129) }
func (e *echoShell) Kill()                 { e.end(137) }
func (e *echoShell) Wait() int             { return <-e.exit }
func (e *echoShell) end(code int)          { e.once.Do(func() { _ = e.w.Close(); e.exit <- code }) }

// termUnits runs `flowd term` units in this process; like systemd units they outlive the Supervisor.
type termUnits struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
	done    map[string]chan struct{}
	props   map[string][]string
	argv    map[string][]string
	linger  time.Duration
}

func newTermUnits() *termUnits {
	return &termUnits{running: map[string]context.CancelFunc{}, done: map[string]chan struct{}{},
		props: map[string][]string{}, argv: map[string][]string{}}
}

func (u *termUnits) Start(_ context.Context, unit string, argv, props []string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if argv[1] != "term" {
		return errors.New("not a terminal")
	}
	if _, ok := u.running[unit]; ok {
		return errors.New("unit " + unit + " already loaded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	u.running[unit], u.done[unit], u.props[unit], u.argv[unit] = cancel, done, props, argv
	linger := u.linger
	go func() {
		defer close(done)
		_ = term.Run(ctx, argv[2:], slog.New(slog.DiscardHandler))
		select { // a unit winding down after its process ended (collected a moment later)
		case <-time.After(linger):
		case <-ctx.Done():
		}
		u.mu.Lock()
		delete(u.running, unit) // --collect
		u.mu.Unlock()
	}()
	return nil
}

func (u *termUnits) State(_ context.Context, unit string) (flowdproto.UnitState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, ok := u.running[unit]; ok {
		return flowdproto.UnitActive, nil
	}
	return flowdproto.UnitMissing, nil
}

func (u *termUnits) List(_ context.Context, glob string) ([]string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for unit := range u.running {
		if strings.HasPrefix(unit, strings.TrimSuffix(glob, "*")) {
			out = append(out, unit)
		}
	}
	return out, nil
}

func (u *termUnits) Stop(_ context.Context, unit string) error {
	u.mu.Lock()
	cancel, ok := u.running[unit]
	done := u.done[unit]
	u.mu.Unlock()
	if ok {
		cancel()
		<-done
	}
	return nil
}

func (u *termUnits) stopAll() {
	u.mu.Lock()
	var units []string
	for unit := range u.running {
		units = append(units, unit)
	}
	u.mu.Unlock()
	for _, unit := range units {
		_ = u.Stop(context.Background(), unit)
	}
}

type termRig struct {
	t     *testing.T
	root  string
	units *termUnits
	sv    *Supervisor
	srv   *httptest.Server
	api   flowdproto.API
}

func newTermRig(t *testing.T) *termRig {
	t.Helper()
	old := term.StartShell
	term.StartShell = func(term.ShellSpec) (term.Shell, error) { return newEchoShell(), nil }
	t.Cleanup(func() { term.StartShell = old })
	root, err := os.MkdirTemp("", "tm") // unix socket paths are short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, d := range []string{layout.SessionDir("demo"), layout.Home} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := &termRig{t: t, root: root, units: newTermUnits()}
	t.Cleanup(r.units.stopAll)
	r.flowd()
	// As Flow's bring-up does; a restart (flowd()) loses it.
	if err := r.api.PutEnv(context.Background(), flowdproto.Env{Revision: 1}); err != nil {
		t.Fatal(err)
	}
	return r
}

// flowd starts (or restarts) flowd over the same root and units.
func (r *termRig) flowd() {
	if r.srv != nil {
		r.srv.CloseClientConnections()
		r.srv.Close()
	}
	r.sv = New(Options{Version: "test", Sandbox: "sb", Root: r.root, Units: r.units, Run: ExecRunner{}, Exe: "flowd",
		Log: slog.New(slog.DiscardHandler), HostWait: 5 * time.Second})
	r.sv.ReattachTerminals(context.Background())
	r.srv = httptest.NewServer(r.sv.Handler("tok"))
	r.t.Cleanup(r.srv.Close)
	r.api = flowdclient.NewDialer().Dial(flowdproto.Endpoint{Address: strings.TrimPrefix(r.srv.URL, "http://"), Token: "tok"})
}

func readUntil(t *testing.T, c io.Reader, want string) string {
	t.Helper()
	var got []byte
	buf := make([]byte, 4096)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(string(got), want) {
		if time.Now().After(deadline) {
			t.Fatalf("output %q never held %q", got, want)
		}
		n, err := c.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Fatalf("read after %q: %v", got, err)
		}
	}
	return string(got)
}

func readExit(t *testing.T, c io.Reader) int {
	t.Helper()
	buf := make([]byte, 4096)
	for {
		_, err := c.Read(buf)
		var exit *flowdproto.TerminalExit
		if errors.As(err, &exit) {
			return exit.Code
		}
		if err != nil {
			t.Fatalf("read before exit: %v", err)
		}
	}
}

func TestTerminalLifecycle(t *testing.T) {
	r := newTermRig(t)
	ctx := context.Background()
	if err := r.api.PutEnv(ctx, flowdproto.Env{Revision: 2, GitName: "Jan", ClaudeOAuthToken: "oauth-secret",
		AnthropicAPIKey: "key-secret"}); err != nil {
		t.Fatal(err)
	}
	spec := flowdproto.TerminalSpec{ID: "t1", Session: "s1", Dir: layout.SessionDir("demo"), Cols: 80, Rows: 24,
		ClaudeAuth: event.ClaudeAuthSubscriptionToken}
	st, err := r.api.EnsureTerminal(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != "t1" || st.Session != "s1" || st.Unit != flowdproto.UnitActive || st.Dir != spec.Dir || st.Exited {
		t.Fatalf("state = %+v", st)
	}
	unit := layout.TerminalUnit("t1")
	envFile := filepath.Join(r.root, layout.SessionEnvFile("t1"))
	props := strings.Join(r.units.props[unit], "\n")
	for _, want := range []string{"MemoryMax=4294967296", "CPUQuota=200%", "TasksMax=2048", "EnvironmentFile=" + envFile} {
		if !strings.Contains(props, want) {
			t.Errorf("props lack %q:\n%s", want, props)
		}
	}
	argv := strings.Join(r.units.argv[unit], " ")
	for _, secret := range []string{"oauth-secret", "key-secret"} {
		if strings.Contains(props, secret) || strings.Contains(argv, secret) {
			t.Errorf("a credential on the command line:\n%s\n%s", props, argv)
		}
	}
	// The session's mode decides: its one credential, in the unit's EnvironmentFile (root 0600, tmpfs).
	ef, err := os.ReadFile(envFile)
	if err != nil || !strings.Contains(string(ef), `CLAUDE_CODE_OAUTH_TOKEN="oauth-secret"`) ||
		strings.Contains(string(ef), "key-secret") || !strings.Contains(string(ef), `GIT_AUTHOR_NAME="Jan"`) {
		t.Errorf("env file = %q %v", ef, err)
	}
	if !strings.Contains(argv, "--dir "+filepath.Join(r.root, spec.Dir)) || !strings.Contains(argv, "--session s1") ||
		!strings.Contains(argv, "--cols 80 --rows 24") || !strings.Contains(argv, "--claude-auth subscription_token") {
		t.Errorf("argv = %s", argv)
	}
	// The project's home terminal (no session, no Claude mode): no credential at all.
	home := flowdproto.TerminalSpec{ID: "p-x-1", Dir: layout.Home, Cols: 80, Rows: 24}
	if _, err := r.api.EnsureTerminal(ctx, home); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.root, layout.SessionEnvFile("p-x-1"))); strings.Contains(string(b), "secret") ||
		!strings.Contains(strings.Join(r.units.argv[layout.TerminalUnit("p-x-1")], " "), "--claude-auth subscription_login") {
		t.Errorf("home terminal env = %q", b)
	}
	if err := r.api.CloseTerminal(ctx, "p-x-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.SessionEnvFile("p-x-1"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("env file kept after close: %v", err)
	}
	bad := spec
	bad.ClaudeAuth = "bogus"
	if _, err := r.api.EnsureTerminal(ctx, bad); !errors.Is(err, flowdproto.ErrInvalid) {
		t.Errorf("bogus claudeAuth: %v", err)
	}
	// Idempotent while running: the same unit, not a second one.
	if again, err := r.api.EnsureTerminal(ctx, spec); err != nil || again.Unit != flowdproto.UnitActive {
		t.Fatalf("ensure again = %+v %v", again, err)
	}

	// The attach outlives the context it was dialled with.
	actx, cancel := context.WithCancel(ctx)
	a, err := r.api.AttachTerminal(actx, "t1", flowdproto.TerminalSize{Cols: 100, Rows: 30})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := a.Write([]byte("echo marker-1\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, a, "marker-1")
	if err := a.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	b, err := r.api.AttachTerminal(ctx, "t1", flowdproto.TerminalSize{})
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, b, "marker-1") // the ring
	_ = b.Close()

	// flowd restarts: the terminal runs on, is found again by unit and socket, and replays its ring.
	// (A hijacked connection outlives httptest's close; a real flowd's exit ends it. Flow detaches.)
	_ = a.Close()
	r.flowd()
	h, err := r.api.Hello(ctx)
	if err != nil || len(h.Terminals) != 1 || h.Terminals[0].ID != "t1" || h.Terminals[0].Session != "s1" {
		t.Fatalf("hello terminals = %+v %v", h.Terminals, err)
	}
	c, err := r.api.AttachTerminal(ctx, "t1", flowdproto.TerminalSize{Cols: 90, Rows: 20})
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, c, "marker-1")
	if info, err := term.Query(ctx, r.sv.termSocket("t1")); err != nil || info.Size != (term.Size{Cols: 90, Rows: 20}) {
		t.Fatalf("size after attach = %+v %v", info.Size, err)
	}

	// The shell exits: the client reads the status; later ones get it from the file the terminal left.
	if _, err := c.Write([]byte("exit 3\r")); err != nil {
		t.Fatal(err)
	}
	if code := readExit(t, c); code != 3 {
		t.Fatalf("exit = %d", code)
	}
	waitUnit(t, r.units, unit, false)
	st, err = r.sv.Terminal(ctx, "t1")
	if err != nil || !st.Exited || st.ExitCode != 3 || st.Unit != flowdproto.UnitMissing {
		t.Fatalf("state after exit = %+v %v", st, err)
	}
	d, err := r.api.AttachTerminal(ctx, "t1", flowdproto.TerminalSize{})
	if err != nil {
		t.Fatalf("attach after exit = %v", err)
	}
	if readExit(t, d) != 3 {
		t.Fatal("late attach got another exit")
	}
	_ = d.Close()

	// flowd restarted above and holds no set yet: no terminal starts without its environment until Flow pushes.
	if _, err := r.api.EnsureTerminal(ctx, spec); !errors.Is(err, flowdproto.ErrUnavailable) {
		t.Fatalf("terminal start without a set: %v", err)
	}
	if err := r.api.PutEnv(ctx, flowdproto.Env{Revision: 3}); err != nil {
		t.Fatal(err)
	}
	// Ensure starts it again (the restart); Close stops it and forgets the status.
	st, err = r.api.EnsureTerminal(ctx, spec)
	if err != nil || st.Exited || st.Unit != flowdproto.UnitActive {
		t.Fatalf("restart = %+v %v", st, err)
	}
	if err := r.api.CloseTerminal(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sv.Terminal(ctx, "t1"); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Fatalf("closed terminal = %v", err)
	}
	if _, err := r.api.AttachTerminal(ctx, "t1", flowdproto.TerminalSize{}); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Fatalf("attach closed = %v", err)
	}
}

func waitUnit(t *testing.T, u *termUnits, unit string, running bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := u.State(context.Background(), unit)
		if (st == flowdproto.UnitActive) == running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("unit %s running=%v never", unit, running)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Ensure on a terminal whose shell has exited while its unit still runs (writing out its clients, being
// collected) starts a new shell: the restart never answers with the ended one.
func TestEnsureRestartsAnEndingTerminal(t *testing.T) {
	r := newTermRig(t)
	r.units.mu.Lock()
	r.units.linger = 3 * time.Second
	r.units.mu.Unlock()
	ctx := context.Background()
	spec := flowdproto.TerminalSpec{ID: "t3", Dir: layout.Home, Cols: 80, Rows: 24}
	if _, err := r.api.EnsureTerminal(ctx, spec); err != nil {
		t.Fatal(err)
	}
	c, err := r.api.AttachTerminal(ctx, "t3", flowdproto.TerminalSize{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("exit 4"))
	if code := readExit(t, c); code != 4 {
		t.Fatalf("exit = %d", code)
	}
	if st, _ := r.sv.Terminal(ctx, "t3"); st.Unit != flowdproto.UnitActive {
		t.Fatalf("the unit should still be winding down: %+v", st)
	}
	st, err := r.api.EnsureTerminal(ctx, spec)
	if err != nil || st.Exited || st.Unit != flowdproto.UnitActive {
		t.Fatalf("restart = %+v %v", st, err)
	}
	d, err := r.api.AttachTerminal(ctx, "t3", flowdproto.TerminalSize{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = d.Write([]byte("echo fresh"))
	readUntil(t, d, "fresh")
	_ = d.Close()
}

// A session's terminal opens where the session's agent works: asked for the session directory, flowd starts it in
// the worktree the spec names as the agent's working directory. Without a spec it stays where it was asked.
func TestSessionTerminal_startsInTheAgentsDir(t *testing.T) {
	r := newTermRig(t)
	ctx := context.Background()
	wt := layout.Worktree("demo", "toolkit")
	if err := os.MkdirAll(filepath.Join(r.root, wt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(r.sv.p(layout.SessionSpecFile("s1"))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.sv.writeSpec(host.Spec{Session: flowdproto.SessionSpec{ID: "s1", Name: "demo"},
		Dir: layout.SessionDir("demo"), Cwd: wt}); err != nil {
		t.Fatal(err)
	}
	st, err := r.api.EnsureTerminal(ctx, flowdproto.TerminalSpec{ID: "t1", Session: "s1", Dir: layout.SessionDir("demo"),
		Cols: 80, Rows: 24})
	if err != nil || st.Dir != wt {
		t.Fatalf("session terminal = %+v, %v; want dir %s", st, err, wt)
	}
	st, err = r.api.EnsureTerminal(ctx, flowdproto.TerminalSpec{ID: "t2", Session: "s2", Dir: layout.SessionDir("demo"),
		Cols: 80, Rows: 24})
	if err != nil || st.Dir != layout.SessionDir("demo") {
		t.Fatalf("terminal of a session without a spec = %+v, %v", st, err)
	}
}

func TestTerminalRefusals(t *testing.T) {
	r := newTermRig(t)
	ctx := context.Background()
	ok := flowdproto.TerminalSpec{ID: "t2", Dir: layout.Home, Cols: 80, Rows: 24}
	for name, spec := range map[string]flowdproto.TerminalSpec{
		"id":       {ID: "../x", Dir: layout.Home, Cols: 80, Rows: 24},
		"session":  {ID: "t2", Session: "A/B", Dir: layout.Home, Cols: 80, Rows: 24},
		"dir":      {ID: "t2", Dir: "/etc", Cols: 80, Rows: 24},
		"dir dots": {ID: "t2", Dir: layout.Sessions + "/../..", Cols: 80, Rows: 24},
		"cols":     {ID: "t2", Dir: layout.Home, Cols: 0, Rows: 24},
		"rows":     {ID: "t2", Dir: layout.Home, Cols: 80, Rows: flowdproto.TerminalMaxRows + 1},
	} {
		if _, err := r.api.EnsureTerminal(ctx, spec); !errors.Is(err, flowdproto.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	missing := ok
	missing.Dir = layout.SessionDir("gone")
	if _, err := r.api.EnsureTerminal(ctx, missing); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Errorf("missing directory: %v", err)
	}
	if _, err := r.api.AttachTerminal(ctx, "nope", flowdproto.TerminalSize{}); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Errorf("attach unknown: %v", err)
	}
	if err := r.api.CloseTerminal(ctx, "BAD"); !errors.Is(err, flowdproto.ErrInvalid) {
		t.Errorf("close bad id: %v", err)
	}
	if _, err := r.sv.Terminal(ctx, "BAD"); err == nil {
		t.Error("state of a bad id")
	}
	if _, err := r.sv.AttachTerminal(ctx, "BAD", flowdproto.TerminalSize{}); err == nil {
		t.Error("attach of a bad id")
	}
	// The body's id must be the path's.
	req, _ := http.NewRequest(http.MethodPut, r.srv.URL+"/v1/terminals/other", strings.NewReader(`{"id":"t2"}`))
	req.Header.Set("Authorization", "Bearer tok")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("path/body mismatch: %v %v", resp, err)
	}
	// Without the token nothing is served, the WebSocket included.
	for _, path := range []string{"/v1/terminals/t2/attach", "/v1/terminals/BAD/attach"} {
		resp, err := http.Get(r.srv.URL + path)
		if err != nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without token: %v %v", path, resp, err)
		}
	}
	req, _ = http.NewRequest(http.MethodGet, r.srv.URL+"/v1/terminals/BAD/attach", nil)
	req.Header.Set("Authorization", "Bearer tok")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("attach bad id: %v %v", resp, err)
	}
	// A unit stopped behind flowd's back (state failed or inactive) is cleared before a new start.
	if _, err := r.api.EnsureTerminal(ctx, ok); err != nil {
		t.Fatal(err)
	}
	if list, err := r.sv.Terminals(ctx); err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if got := r.sv.ReattachTerminals(ctx); len(got) != 1 {
		t.Fatalf("reattach = %+v", got)
	}
}

func TestTermDirAndUnrooted(t *testing.T) {
	for dir, want := range map[string]bool{layout.Home: true, layout.SessionDir("a-b"): true, "/etc": false,
		layout.Sessions: false, layout.SessionDir(".x"): false, layout.SessionDir("a") + "/b": false} {
		if validTermDir(dir) != want {
			t.Errorf("validTermDir(%q) != %v", dir, want)
		}
	}
	s := New(Options{Root: "/tmp/r"})
	if s.unrooted("/tmp/r/home/flow") != "/home/flow" || s.unrooted("/other") != "/other" {
		t.Error("unrooted")
	}
	if New(Options{}).unrooted("/x") != "/x" {
		t.Error("unrooted without root")
	}
	if p := termProps(flowdproto.Limits{MemoryMaxBytes: 1 << 30, CPUQuotaPercent: 50}); p[0] != "MemoryMax=1073741824" ||
		p[1] != "CPUQuota=50%" {
		t.Errorf("props = %v", p[:2])
	}
}

// A terminal that accepts on its socket but never answers (stopped, or throttled in its unit) costs Hello at most
// its short budget, well inside Flow's 3 s, and is reported by its unit state: it must not make flowd unreachable.
func TestHello_stalledTerminal(t *testing.T) {
	r := newTermRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	unit := layout.TerminalUnit("stall")
	done := make(chan struct{})
	go func() { <-ctx.Done(); close(done) }()
	r.units.mu.Lock()
	r.units.running[unit], r.units.done[unit] = cancel, done
	r.units.mu.Unlock()
	sock := r.sv.termSocket("stall")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		var held []net.Conn // accepted, never answered
		for {
			c, err := ln.Accept()
			if err != nil {
				for _, c := range held {
					_ = c.Close()
				}
				return
			}
			held = append(held, c)
		}
	}()
	t0 := time.Now()
	hctx, hcancel := context.WithTimeout(context.Background(), 3*time.Second) // Flow's hello timeout
	defer hcancel()
	h, err := r.api.Hello(hctx)
	if err != nil || time.Since(t0) > 2*time.Second {
		t.Fatalf("hello with a stalled terminal: %v after %s", err, time.Since(t0))
	}
	if len(h.Terminals) != 1 || h.Terminals[0].ID != "stall" || h.Terminals[0].Unit != flowdproto.UnitActive {
		t.Errorf("stalled terminal reported as %+v", h.Terminals)
	}
}
