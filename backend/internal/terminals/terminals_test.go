package terminals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/auth"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/flowdproto/flowdfake"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/projects"
	sbx "github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/sessions"
)

const (
	sid = "0193f1e2-aaaa-7bbb-8ccc-0123456789ab"
	pid = "0193f1e2-bbbb-7bbb-8ccc-0123456789ab"
)

type sessionStore map[string]sessions.Session

func (s sessionStore) Get(_ context.Context, id string) (sessions.Session, error) {
	if se, ok := s[id]; ok {
		return se, nil
	}
	return sessions.Session{}, sessions.ErrNotFound
}

type projectStore map[string]projects.Project

func (s projectStore) Get(_ context.Context, id string) (projects.Project, error) {
	if p, ok := s[id]; ok {
		return p, nil
	}
	return projects.Project{}, projects.ErrNotFound
}

// fakeSandboxes hands out one flowd; warm false makes Warm fail (a stopped sandbox).
type fakeSandboxes struct {
	mu        sync.Mutex
	api       flowdproto.API
	warm      bool
	ensureErr error
	warmErr   error // a failure of the no-start path other than "not warm" (store, runtime, dial)
	ensured   int
	forgot    int
}

func (s *fakeSandboxes) Ensure(context.Context, string) (flowdproto.API, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensured++
	if s.ensureErr != nil {
		return nil, s.ensureErr
	}
	s.warm = true
	return s.api, nil
}

func (s *fakeSandboxes) Warm(context.Context, string) (flowdproto.API, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.warmErr != nil {
		return nil, s.warmErr
	}
	if !s.warm {
		return nil, sbx.ErrNotWarm
	}
	return s.api, nil
}

func (s *fakeSandboxes) Forget(string) {
	s.mu.Lock()
	s.forgot++
	s.mu.Unlock()
}

// flaky fails its first EnsureTerminal and AttachTerminal as a restarted sandbox's stale connection does.
type flaky struct {
	*flowdfake.Fake
	mu      sync.Mutex
	ensures int
	attachs int
}

func (f *flaky) EnsureTerminal(ctx context.Context, t flowdproto.TerminalSpec) (flowdproto.TerminalState, error) {
	f.mu.Lock()
	f.ensures++
	n := f.ensures
	f.mu.Unlock()
	if n == 1 {
		return flowdproto.TerminalState{}, flowdproto.ErrUnavailable
	}
	return f.Fake.EnsureTerminal(ctx, t)
}

func (f *flaky) AttachTerminal(ctx context.Context, id string, size flowdproto.TerminalSize) (flowdproto.TerminalConn, error) {
	f.mu.Lock()
	f.attachs++
	n := f.attachs
	f.mu.Unlock()
	if n == 1 {
		return nil, flowdproto.ErrUnavailable
	}
	return f.Fake.AttachTerminal(ctx, id, size)
}

type rig struct {
	t    *testing.T
	fake *flowdfake.Fake
	sb   *fakeSandboxes
	srv  *httptest.Server
	stop context.CancelFunc
}

func newRig(t *testing.T) *rig {
	t.Helper()
	fake := flowdfake.New("sb")
	sb := &fakeSandboxes{api: fake}
	a := &API{
		Sessions: sessionStore{sid: {ID: sid, ProjectID: pid, Name: "flaky-test", Provider: "claude", ClaudeAuth: "api_key"},
			"oc":       {ID: "oc", ProjectID: pid, Name: "oc", Provider: "opencode", ClaudeAuth: "api_key"},
			"archived": {ID: "archived", ProjectID: pid, Name: "old", ArchivedAt: &time.Time{}}},
		Projects:  projectStore{pid: {ID: pid}, "other": {ID: "other"}, "Bad.Project": {ID: "Bad.Project"}},
		Sandboxes: sb,
	}
	// The real handler with the dev-mode guard: the same-origin rule the browser meets.
	srv := httptest.NewUnstartedServer(httpapi.New(httpapi.Options{Guard: auth.Dev{}, Static: http.NotFoundHandler(),
		Areas: httpapi.Areas{Terminals: a}}))
	base, stop := context.WithCancel(context.Background())
	srv.Config.BaseContext = func(net.Listener) context.Context { return base }
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(stop)
	return &rig{t: t, fake: fake, sb: sb, srv: srv, stop: stop}
}

func (r *rig) post(body string) (*http.Response, map[string]any) {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/api/terminals", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (r *rig) dial(id, query, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if origin == "" {
		origin = r.srv.URL
	}
	h.Set("Origin", origin)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.srv.URL, "http")+"/api/terminals/"+id+"/ws"+query,
		&websocket.DialOptions{HTTPHeader: h})
	if c != nil {
		c.SetReadLimit(1 << 20)
	}
	return c, resp, err
}

func readUntil(t *testing.T, c *websocket.Conn, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got string
	for !strings.Contains(got, want) {
		typ, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read after %q: %v", got, err)
		}
		if typ == websocket.MessageBinary {
			got += string(b)
		}
	}
	return got
}

// closeOf reads until the connection ends and returns its close code and the text messages seen.
func closeOf(t *testing.T, c *websocket.Conn) (websocket.StatusCode, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var texts []string
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err), texts
		}
		if typ == websocket.MessageText {
			texts = append(texts, string(b))
		}
	}
}

func TestCreate(t *testing.T) {
	r := newRig(t)
	resp, out := r.post(`{"projectId":"` + pid + `","sessionId":"` + sid + `","cols":100,"rows":30}`)
	if resp.StatusCode != http.StatusCreated || out["id"] != "s-"+sid+"-1" || out["cwd"] != layout.SessionDir("flaky-test") ||
		out["sessionId"] != sid || out["projectId"] != pid {
		t.Fatalf("create = %d %v", resp.StatusCode, out)
	}
	rec := &recorder{Fake: r.fake}
	r.sb.api = rec
	r.post(`{"projectId":"` + pid + `","sessionId":"` + sid + `","cols":80,"rows":24}`)
	r.post(`{"projectId":"` + pid + `","sessionId":"oc","cols":80,"rows":24}`)
	if len(rec.specs) != 2 || rec.specs[0].ClaudeAuth != "api_key" || rec.specs[1].ClaudeAuth != "" {
		t.Fatalf("the session's Claude mode reaches flowd only for Claude sessions: %+v", rec.specs)
	}
	r.sb.api = r.fake
	if r.sb.ensured != 3 {
		t.Fatalf("sandbox ensured %d times, want 1 (a stopped sandbox starts as for a message)", r.sb.ensured)
	}
	h, _ := r.fake.Hello(context.Background())
	if len(h.Terminals) != 2 || h.Terminals[0].Dir != layout.SessionDir("flaky-test") || h.Terminals[0].Session != sid {
		t.Fatalf("flowd terminals = %+v", h.Terminals)
	}
	if len(h.Sessions) != 0 {
		t.Fatalf("the session's agent was started: %+v", h.Sessions)
	}
	// Create again: the same terminal (attach), not a second one.
	if resp, out := r.post(`{"projectId":"` + pid + `","sessionId":"` + sid + `","cols":80,"rows":24}`); resp.StatusCode != 201 ||
		out["id"] != "s-"+sid+"-1" {
		t.Fatalf("create again = %d %v", resp.StatusCode, out)
	}
	resp, out = r.post(`{"projectId":"` + pid + `","cols":80,"rows":24}`)
	if resp.StatusCode != http.StatusCreated || out["id"] != "p-"+pid+"-1" || out["cwd"] != layout.Home {
		t.Fatalf("home terminal = %d %v", resp.StatusCode, out)
	}
	for body, want := range map[string]string{
		`{"projectId":"` + pid + `","cols":0,"rows":24}`:                         "invalid_request",
		`{"projectId":"` + pid + `","cols":80,"rows":201}`:                       "invalid_request",
		`{"projectId":"` + pid + `","sessionId":"nope","cols":80,"rows":24}`:     "invalid_request",
		`{"projectId":"other","sessionId":"` + sid + `","cols":80,"rows":24}`:    "invalid_request",
		`{"projectId":"` + pid + `","sessionId":"archived","cols":80,"rows":24}`: "session_archived",
		`{"projectId":"nope","cols":80,"rows":24}`:                               "invalid_request",
		`{"projectId":"` + pid + `","cols":80,"rows":24,"extra":1}`:              "invalid_request",
	} {
		if resp, out := r.post(body); out["code"] != want {
			t.Errorf("%s: %d %v, want %s", body, resp.StatusCode, out, want)
		}
	}
}

// The archived check runs under the session's lock: an archive that held it while this request came in (here:
// one that completes as the lock is handed over) is seen, and no shell starts for the archived session.
func TestCreate_underTheSessionLock(t *testing.T) {
	fake := flowdfake.New("sb")
	store := sessionStore{sid: {ID: sid, ProjectID: pid, Name: "flaky-test", Provider: "claude"}}
	locked := 0
	a := &API{Sessions: store, Projects: projectStore{pid: {ID: pid}}, Sandboxes: &fakeSandboxes{api: fake},
		Lock: func(id string) func() {
			locked++
			se := store[id]
			se.ArchivedAt = &time.Time{} // the archive that held the lock
			store[id] = se
			return func() {}
		}}
	req := httptest.NewRequest(http.MethodPost, "/api/terminals",
		strings.NewReader(`{"projectId":"`+pid+`","sessionId":"`+sid+`","cols":80,"rows":24}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.CreateTerminal(w, req)
	if locked != 1 || !strings.Contains(w.Body.String(), "session_archived") {
		t.Fatalf("create while archived: %d %s (locked %d)", w.Code, w.Body, locked)
	}
	if h, _ := fake.Hello(context.Background()); len(h.Terminals) != 0 {
		t.Errorf("a shell started for an archived session: %+v", h.Terminals)
	}
	// A session id that names nothing takes no lock (each would stay in the lock map).
	req = httptest.NewRequest(http.MethodPost, "/api/terminals",
		strings.NewReader(`{"projectId":"`+pid+`","sessionId":"made-up","cols":80,"rows":24}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	a.CreateTerminal(w, req)
	if locked != 1 || !strings.Contains(w.Body.String(), "invalid_request") {
		t.Errorf("unknown session: %d %s (locked %d)", w.Code, w.Body, locked)
	}
}

func TestCreateFailures(t *testing.T) {
	r := newRig(t)
	r.sb.ensureErr = apitypes.Fail(apitypes.ErrorCodeSandboxMemoryFull, "")
	if _, out := r.post(`{"projectId":"` + pid + `","cols":80,"rows":24}`); out["code"] != "sandbox_memory_full" {
		t.Fatalf("ensure failure = %v", out)
	}
	r.sb.ensureErr = errors.New("boom")
	if _, out := r.post(`{"projectId":"` + pid + `","cols":80,"rows":24}`); out["code"] != "sandbox_unavailable" {
		t.Fatalf("plain failure = %v", out)
	}
	r.sb.ensureErr = nil
	// A stale connection: forgotten and dialled again once.
	r.sb.api = &flaky{Fake: r.fake}
	if resp, out := r.post(`{"projectId":"` + pid + `","cols":80,"rows":24}`); resp.StatusCode != 201 || r.sb.forgot != 1 {
		t.Fatalf("retry = %d %v forgot %d", resp.StatusCode, out, r.sb.forgot)
	}
	// The session directory is gone (the fake has no directories: a missing one is flowd's not_found).
	r.sb.api = notFound{r.fake}
	if _, out := r.post(`{"projectId":"` + pid + `","sessionId":"` + sid + `","cols":80,"rows":24}`); out["code"] != "conflict" {
		t.Fatalf("missing directory = %v", out)
	}
}

type recorder struct {
	*flowdfake.Fake
	specs []flowdproto.TerminalSpec
}

func (r *recorder) EnsureTerminal(ctx context.Context, t flowdproto.TerminalSpec) (flowdproto.TerminalState, error) {
	r.specs = append(r.specs, t)
	return r.Fake.EnsureTerminal(ctx, t)
}

type notFound struct{ *flowdfake.Fake }

func (notFound) EnsureTerminal(context.Context, flowdproto.TerminalSpec) (flowdproto.TerminalState, error) {
	return flowdproto.TerminalState{}, flowdproto.ErrNotFound
}

func TestAttach(t *testing.T) {
	r := newRig(t)
	id := "s-" + sid + "-1"
	if resp, _ := r.post(`{"projectId":"` + pid + `","sessionId":"` + sid + `","cols":80,"rows":24}`); resp.StatusCode != 201 {
		t.Fatal(resp.Status)
	}
	ctx := context.Background()
	a, _, err := r.dial(id, "?cols=100&rows=30", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.fake.TerminalSize(id); got != (flowdproto.TerminalSize{Cols: 100, Rows: 30}) {
		t.Fatalf("size on attach = %+v", got)
	}
	if err := a.Write(ctx, websocket.MessageBinary, []byte("echo marker\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, a, "marker")
	for _, m := range []string{`{"resize":{"cols":120,"rows":40}}`, `{"resize":{"cols":9000,"rows":40}}`, `not json`} {
		if err := a.Write(ctx, websocket.MessageText, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	// A second tab gets the scrollback first.
	b, _, err := r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, b, "marker")
	if got := r.fake.TerminalSize(id); got != (flowdproto.TerminalSize{Cols: 120, Rows: 40}) {
		t.Fatalf("size after resizes = %+v", got)
	}
	// flowd restarts: the browser's socket closes with a code that is no exit, and attaching again replays.
	r.fake.DropTerminalConns(id)
	if code, texts := closeOf(t, a); code == websocket.StatusNormalClosure || code == wsGone || len(texts) != 0 {
		t.Fatalf("after a flowd restart: %d %v", code, texts)
	}
	_ = b.CloseNow()
	c, _, err := r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, c, "marker")
	// The shell exits: an exit message, then a normal close; a later attach is told the same.
	r.fake.ExitTerminal(id, 3)
	code, texts := closeOf(t, c)
	if code != websocket.StatusNormalClosure || len(texts) != 1 || texts[0] != `{"exit":{"code":3}}` {
		t.Fatalf("exit: %d %v", code, texts)
	}
	d, _, err := r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, texts := closeOf(t, d); code != websocket.StatusNormalClosure || len(texts) != 1 {
		t.Fatalf("attach after exit: %d %v", code, texts)
	}
	// Restart (create again) and close.
	if resp, _ := r.post(`{"projectId":"` + pid + `","sessionId":"` + sid + `","cols":80,"rows":24}`); resp.StatusCode != 201 {
		t.Fatal(resp.Status)
	}
	req, _ := http.NewRequest(http.MethodDelete, r.srv.URL+"/api/terminals/"+id, nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("close = %v %v", resp, err)
	}
	e, _, err := r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := closeOf(t, e); code != wsGone {
		t.Fatalf("attach closed terminal: %d", code)
	}
}

const wsGone = websocket.StatusCode(4404)

func TestAttachRefusals(t *testing.T) {
	r := newRig(t)
	id := "s-" + sid + "-1"
	// Not warm: the sandbox is not started by an attach; the browser learns it from the close code.
	c, _, err := r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := closeOf(t, c); code != 4409 || r.sb.ensured != 0 {
		t.Fatalf("not warm: %d, ensured %d", code, r.sb.ensured)
	}
	// Another page's origin is refused before the upgrade (auth's same-origin rule covers the handshake's GET).
	r.sb.warm = true
	if _, resp, err := r.dial(id, "", "http://evil.example"); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin: %v %v", resp, err)
	}
	// Ids this package never made, sessions that do not exist and archived sessions: "gone" after the upgrade (a
	// browser cannot read a refused handshake and would retry forever); a plain GET still gets 404.
	for _, bad := range []string{"x", "s-nope-1", "p-nope-1", "s--1", "s-" + sid + "-0", "s-" + sid + "-01",
		"q-" + sid + "-1", "s-archived-1"} {
		c, resp, err := r.dial(bad, "", "")
		if err != nil {
			t.Errorf("%s: handshake refused: %v %v", bad, resp, err)
			continue
		}
		if code, _ := closeOf(t, c); code != wsGone {
			t.Errorf("%s: close %d, want %d", bad, code, wsGone)
		}
	}
	if resp, err := http.Get(r.srv.URL + "/api/terminals/x/ws"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("plain GET of an unknown id: %v %v", resp, err)
	}
	// An archived session's terminal still running in flowd is not attached to.
	if _, err := r.fake.EnsureTerminal(context.Background(), flowdproto.TerminalSpec{ID: "s-archived-1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	if c, _, err := r.dial("s-archived-1", "", ""); err != nil {
		t.Fatal(err)
	} else if code, _ := closeOf(t, c); code != wsGone {
		t.Errorf("archived session: close %d", code)
	}
	// Warm, but flowd has no such terminal.
	c, _, err = r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := closeOf(t, c); code != wsGone {
		t.Fatalf("missing terminal: %d", code)
	}
	// A stale connection is forgotten and dialled again.
	if _, err := r.fake.EnsureTerminal(context.Background(), flowdproto.TerminalSpec{ID: id, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	r.sb.api = &flaky{Fake: r.fake, ensures: 1}
	c, _, err = r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Write(context.Background(), websocket.MessageBinary, []byte("again"))
	readUntil(t, c, "again")
	if r.sb.forgot != 1 {
		t.Fatalf("forgot %d", r.sb.forgot)
	}
	// Flow shuts down: 1012, the browser attaches again later.
	r.stop()
	if code, _ := closeOf(t, c); code != websocket.StatusServiceRestart {
		t.Fatalf("shutdown: %d", code)
	}
}

func TestCloseWithoutSandbox(t *testing.T) {
	r := newRig(t)
	for id, want := range map[string]int{"s-" + sid + "-1": http.StatusNoContent, "bogus": http.StatusNotFound} {
		req, _ := http.NewRequest(http.MethodDelete, r.srv.URL+"/api/terminals/"+id, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != want {
			t.Errorf("close %s = %v %v", id, resp, err)
		}
	}
	if r.sb.ensured != 0 {
		t.Fatal("close started the sandbox")
	}
}

// Only "not warm" means stopped: any other failure of the no-start path (store, runtime, dial) is no proof that the
// shell is gone. Attach says "try again", close reports the failure instead of claiming success.
func TestWarmFailuresAreNotStopped(t *testing.T) {
	r := newRig(t)
	id := "s-" + sid + "-1"
	r.sb.warmErr = errors.New("database is locked")
	c, _, err := r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := closeOf(t, c); code != websocket.StatusInternalError {
		t.Fatalf("attach on a transient failure: close %d, want 1011", code)
	}
	req, _ := http.NewRequest(http.MethodDelete, r.srv.URL+"/api/terminals/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode == http.StatusNoContent {
		t.Fatalf("close on a transient failure answered %v %v", resp, err)
	}
	// Not warm by the records (failed) while the instance may run: close reports it instead of claiming the shell gone;
	// attach tells the browser the sandbox is not usable.
	r.sb.warmErr = fmt.Errorf("%w (recorded failed)", sbx.ErrMayRun)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("close on a failed, running sandbox answered %v %v", resp, err)
	}
	c, _, err = r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := closeOf(t, c); code != 4409 {
		t.Fatalf("attach on a failed, running sandbox: close %d", code)
	}
	// The project has no sandbox at all: nothing can run, so that is "stopped" too.
	r.sb.warmErr = sbx.ErrNotFound
	c, _, err = r.dial(id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := closeOf(t, c); code != 4409 {
		t.Fatalf("attach without a sandbox: close %d", code)
	}
}

// exitedOnce reports the terminal's shell as exited with its unit still up, once, as an older flowd does while the
// unit winds down.
type exitedOnce struct {
	*flowdfake.Fake
	mu      sync.Mutex
	ensures int
	closes  int
}

func (e *exitedOnce) EnsureTerminal(ctx context.Context, t flowdproto.TerminalSpec) (flowdproto.TerminalState, error) {
	e.mu.Lock()
	e.ensures++
	n := e.ensures
	e.mu.Unlock()
	if n == 1 {
		return flowdproto.TerminalState{ID: t.ID, Unit: flowdproto.UnitActive, Exited: true, ExitCode: 3}, nil
	}
	return e.Fake.EnsureTerminal(ctx, t)
}

func (e *exitedOnce) CloseTerminal(ctx context.Context, id string) error {
	e.mu.Lock()
	e.closes++
	e.mu.Unlock()
	return e.Fake.CloseTerminal(ctx, id)
}

// A restart that finds the old shell exited but its unit not yet gone closes it and starts a new one, instead of
// answering "created" for a terminal that is still the ended one.
func TestRestartOfAnEndingTerminal(t *testing.T) {
	r := newRig(t)
	e := &exitedOnce{Fake: r.fake}
	r.sb.api = e
	resp, out := r.post(`{"projectId":"` + pid + `","sessionId":"` + sid + `","cols":80,"rows":24}`)
	if resp.StatusCode != http.StatusCreated || e.closes != 1 || e.ensures != 2 {
		t.Fatalf("restart = %d %v, closes %d, ensures %d", resp.StatusCode, out, e.closes, e.ensures)
	}
	h, _ := r.fake.Hello(context.Background())
	if len(h.Terminals) != 1 || h.Terminals[0].Exited {
		t.Fatalf("terminals after restart: %+v", h.Terminals)
	}
}

// Refusals name the field at fault.
func TestFieldNames(t *testing.T) {
	r := newRig(t)
	for body, field := range map[string]string{
		`{"projectId":"` + pid + `","cols":0,"rows":24}`:   "cols",
		`{"projectId":"` + pid + `","cols":80,"rows":0}`:   "rows",
		`{"projectId":"` + pid + `","cols":80,"rows":999}`: "rows",
		`{"projectId":"Bad.Project","cols":80,"rows":24}`:  "projectId",
	} {
		if _, out := r.post(body); out["code"] != "invalid_request" || out["field"] != field {
			t.Errorf("%s: %v, want field %s", body, out, field)
		}
	}
}

func TestIDs(t *testing.T) {
	if SessionTerminalID(sid, 2) != "s-"+sid+"-2" || ProjectTerminalID(pid, 1) != "p-"+pid+"-1" {
		t.Fatal("ids")
	}
	if !layout.ValidName(SessionTerminalID(sid, 1)) {
		t.Fatal("a session terminal id is no flowd name")
	}
	if tg, ok := parseID("p-" + pid + "-3"); !ok || tg.projectID != pid {
		t.Fatalf("parse = %+v %v", tg, ok)
	}
}
