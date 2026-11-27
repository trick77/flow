package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
)

// procUnits runs hosts in this process: a unit is a goroutine running host.Run, which outlives the Supervisor
// that started it, as a systemd unit outlives flowd.
type procUnits struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
	done    map[string]chan struct{}
	started []string
}

func newUnits() *procUnits {
	return &procUnits{running: map[string]context.CancelFunc{}, done: map[string]chan struct{}{}}
}

func (u *procUnits) Start(_ context.Context, unit string, argv, _ []string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if argv[1] != "host" {
		return errors.New("not a host")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	u.running[unit], u.done[unit] = cancel, done
	u.started = append(u.started, unit)
	go func() {
		defer close(done)
		_ = host.Run(ctx, argv[2:], "test", slog.New(slog.DiscardHandler))
		u.mu.Lock()
		delete(u.running, unit)
		u.mu.Unlock()
	}()
	return nil
}

func (u *procUnits) State(_ context.Context, unit string) (flowdproto.UnitState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, ok := u.running[unit]; ok {
		return flowdproto.UnitActive, nil
	}
	return flowdproto.UnitMissing, nil
}

func (u *procUnits) List(_ context.Context, glob string) ([]string, error) {
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

func (u *procUnits) Stop(_ context.Context, unit string) error {
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

func (u *procUnits) stopAll() {
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

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// remote makes a bare repository base/owner/name.git with one commit on master.
func remote(t *testing.T, base, full string) {
	t.Helper()
	bare := filepath.Join(base, full+".git")
	_ = os.MkdirAll(bare, 0o755)
	gitRun(t, bare, "init", "--quiet", "--bare", "--initial-branch=master")
	work := filepath.Join(t.TempDir(), "w")
	gitRun(t, "", "clone", "--quiet", bare, work)
	_ = os.WriteFile(filepath.Join(work, "README"), []byte(full), 0o644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "--quiet", "-m", "first")
	gitRun(t, work, "push", "--quiet", "origin", "HEAD:master")
}

type rig struct {
	t     *testing.T
	root  string
	base  string
	units *procUnits
	srv   *httptest.Server
	api   flowdproto.API
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root, err := os.MkdirTemp("", "sv") // unix socket paths are short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	base := filepath.Join(t.TempDir(), "remote") + "/"
	remote(t, base, "trick77/toolkit")
	remote(t, base, "trick77/ship")
	r := &rig{t: t, root: root, base: base, units: newUnits()}
	t.Cleanup(r.units.stopAll)
	r.flowd()
	// Flow's bring-up pushes the set before anything else; flowd starts no unit without one. A restart (flowd())
	// loses it, as a real one does.
	if err := r.api.PutEnv(context.Background(), flowdproto.Env{Revision: 1}); err != nil {
		t.Fatal(err)
	}
	return r
}

// flowd starts (or restarts) flowd: a new Supervisor over the same root and units.
func (r *rig) flowd() {
	if r.srv != nil {
		r.srv.CloseClientConnections()
		r.srv.Close()
	}
	s := New(Options{Version: "test", Sandbox: "sb", Root: r.root, Units: r.units, Run: ExecRunner{}, Exe: "flowd",
		HostArgs: []string{"--pace", "0"}, CloneBase: "file://" + r.base, Log: slog.New(slog.DiscardHandler)})
	s.Reattach(context.Background())
	r.srv = httptest.NewServer(s.Handler("tok"))
	r.t.Cleanup(r.srv.Close)
	r.api = flowdclient.NewDialer().Dial(flowdproto.Endpoint{Address: strings.TrimPrefix(r.srv.URL, "http://"), Token: "tok"})
}

func command(t *testing.T, id string, kind event.CommandKind, payload any) event.Command {
	t.Helper()
	c, err := event.NewCommand(id, "s1", kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// collect follows the stream from after until an event matches stop, returning everything seen.
func collect(t *testing.T, api flowdproto.API, after int64, stop func(event.Event) bool) []event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out []event.Event
	for e, err := range api.Events(ctx, "s1", after) {
		if err != nil {
			t.Fatalf("stream: %v (after %d events)", err, len(out))
		}
		out = append(out, e)
		if stop(e) {
			return out
		}
	}
	t.Fatal("stream ended")
	return nil
}

func durable(evs []event.Event) []event.Event {
	var out []event.Event
	for _, e := range evs {
		if e.Seq > 0 {
			out = append(out, e)
		}
	}
	return out
}

func kindIs(k event.EventKind) func(event.Event) bool {
	return func(e event.Event) bool { return e.Kind == k }
}

func TestSessionLifecycle(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	st, err := r.api.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: "trick77/toolkit", DefaultBranch: "master"})
	if err != nil || st.Path != "/home/flow/work/toolkit" || len(st.HeadSHA) != 40 {
		t.Fatalf("EnsureRepo = %+v, %v", st, err)
	}
	if _, err := r.api.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: "trick77/toolkit"}); err != nil {
		t.Fatalf("fetch of an existing checkout: %v", err)
	}
	spec := flowdproto.SessionSpec{ID: "s1", Name: "demo", Provider: event.ProviderClaude, Model: "m",
		Mode: event.PermissionModeAsk, Limits: flowdproto.Limits{MemoryMaxBytes: 1 << 30, CPUQuotaPercent: 200}, Replay: true,
		Worktrees: []flowdproto.WorktreeSpec{
			{Repo: "trick77/toolkit", Branch: "flow/demo", BaseRef: "origin/master"},
			{Repo: "trick77/ship", Branch: "flow/demo", BaseRef: "origin/master"}}}
	ss, err := r.api.EnsureSession(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if ss.Unit != flowdproto.UnitActive || len(ss.Worktrees) != 2 || ss.Dir != "/home/flow/work/.sessions/demo" {
		t.Fatalf("session %+v", ss)
	}
	for _, repo := range []string{"toolkit", "ship"} {
		if _, err := os.Stat(filepath.Join(r.root, layout.Worktree("demo", repo), "README")); err != nil {
			t.Errorf("worktree %s: %v", repo, err)
		}
	}
	if again, err := r.api.EnsureSession(ctx, spec); err != nil || len(r.units.started) != 1 || again.LastSeq != ss.LastSeq {
		t.Errorf("second EnsureSession started another host: %v %v", r.units.started, err)
	}

	// The turn blocks on the permission request until the reply.
	if res, err := r.api.Command(ctx, command(t, "m1", event.CmdMessageSend, event.MessageSend{MessageID: "m1",
		Text: "go", Origin: event.MessageOriginUser})); err != nil || res.Duplicate {
		t.Fatalf("Command = %+v, %v", res, err)
	}
	evs := collect(t, r.api, 0, kindIs(event.KindPermissionRequested))
	var req event.PermissionRequested
	_ = json.Unmarshal(evs[len(evs)-1].Data, &req)
	if _, err := r.api.Command(ctx, command(t, "bad", event.CmdPermissionReply, event.PermissionReply{RequestID: "x",
		Decision: "allow_once"})); !errors.Is(err, flowdproto.ErrNotPending) {
		t.Errorf("reply to an unknown request: %v", err)
	}

	// flowd restarts while the turn waits; the host keeps it.
	r.flowd()
	hello, err := r.api.Hello(ctx)
	if err != nil || len(hello.Sessions) != 1 || hello.Sessions[0].Status != event.SessionStatusApproval {
		t.Fatalf("Hello after restart = %+v, %v", hello, err)
	}
	if res, _ := r.api.Command(ctx, command(t, "m1", event.CmdMessageSend, event.MessageSend{MessageID: "m1",
		Text: "go", Origin: event.MessageOriginUser})); !res.Duplicate {
		t.Error("a retried message was not a duplicate")
	}
	if _, err := r.api.Command(ctx, command(t, "r1", event.CmdPermissionReply, event.PermissionReply{
		RequestID: req.RequestID, Decision: "allow_once"})); err != nil {
		t.Fatal(err)
	}
	last := durable(evs)[len(durable(evs))-1].Seq
	rest := collect(t, r.api, last, func(e event.Event) bool {
		return e.Kind == event.KindSessionStatus && string(e.Data) == `{"status":"done"}`
	})
	all := append(durable(evs), durable(rest)...)
	for i, e := range all {
		if e.Seq != int64(i+1) {
			t.Fatalf("seq %d at %d: lost or duplicated", e.Seq, i)
		}
	}
	// Live deltas are not asserted: at pace 0 they can all pass before the subscription opens (host tests cover them).

	st2, err := r.api.Session(ctx, "s1")
	if err != nil || st2.Status != event.SessionStatusDone || st2.LastSeq != all[len(all)-1].Seq {
		t.Errorf("Session = %+v, %v", st2, err)
	}
	if _, err := r.api.AddWorktree(ctx, "s1", flowdproto.WorktreeSpec{Repo: "trick77/ship", Branch: "flow/demo",
		BaseRef: "origin/master"}); err != nil {
		t.Errorf("AddWorktree of an existing worktree: %v", err)
	}
	if err := r.api.StopSession(ctx, "s1", false); err != nil {
		t.Fatal(err)
	}
	// With the host gone the log is still served, the state read from it.
	st3, err := r.api.Session(ctx, "s1")
	if err != nil || st3.Unit != flowdproto.UnitMissing || st3.Status != event.SessionStatusDone || st3.LastSeq != st2.LastSeq+0 {
		t.Errorf("stopped session = %+v, %v", st3, err)
	}
	again := collect(t, r.api, 0, func(e event.Event) bool { return e.Seq == st2.LastSeq })
	if len(durable(again)) != int(st2.LastSeq) {
		t.Errorf("read back %d of %d events", len(again), st2.LastSeq)
	}
	if _, err := r.api.Command(ctx, command(t, "m2", event.CmdTurnInterrupt, event.TurnInterrupt{})); !errors.Is(err, flowdproto.ErrUnavailable) {
		t.Errorf("command to a stopped host: %v", err)
	}
	// The restarted flowd holds no set: it starts no host (one would run without its credential) until Flow
	// pushes again, which Flow does on the busy answer.
	if h, _ := r.api.Hello(ctx); h.EnvRevision != 0 {
		t.Errorf("env revision after restart %d", h.EnvRevision)
	}
	if _, err := r.api.EnsureSession(ctx, spec); !errors.Is(err, flowdproto.ErrUnavailable) ||
		len(r.units.started) != 1 {
		t.Fatalf("host start without a set: %v (starts %v)", err, r.units.started)
	}
	if err := r.api.PutEnv(ctx, flowdproto.Env{Revision: 3}); err != nil {
		t.Error(err)
	}
	if h, _ := r.api.Hello(ctx); h.EnvRevision != 3 {
		t.Errorf("env revision %d", h.EnvRevision)
	}
	// The next start resumes.
	if _, err := r.api.EnsureSession(ctx, spec); err != nil {
		t.Fatal(err)
	}
	resumed := collect(t, r.api, st2.LastSeq, kindIs(event.KindSessionResumed))
	if len(durable(resumed)) != 1 {
		t.Errorf("after restart: %v", resumed)
	}
	if err := r.api.StopSession(ctx, "s1", true); err != nil {
		t.Errorf("stop and remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.Worktree("demo", "ship"))); err == nil {
		t.Error("worktree left after remove")
	}
}

func TestRefusals(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for name, err := range map[string]error{
		"bad repo":    func() error { _, err := r.api.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: "../x"}); return err }(),
		"bad session": func() error { _, err := r.api.EnsureSession(ctx, flowdproto.SessionSpec{ID: "A/B"}); return err }(),
		"unknown":     func() error { _, err := r.api.Session(ctx, "nope"); return err }(),
		"stop":        r.api.StopSession(ctx, "nope", false),
		"worktree": func() error {
			_, err := r.api.AddWorktree(ctx, "nope", flowdproto.WorktreeSpec{})
			return err
		}(),
		"missing repo": func() error {
			_, err := r.api.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: "trick77/missing"})
			return err
		}(),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for e, err := range r.api.Events(ctx, "nope", 0) {
		if !errors.Is(err, flowdproto.ErrNotFound) {
			t.Errorf("events of an unknown session: %v %v", e, err)
		}
	}
	bad := flowdclient.NewDialer().Dial(flowdproto.Endpoint{Address: strings.TrimPrefix(r.srv.URL, "http://"), Token: "x"})
	if _, err := bad.Hello(ctx); !errors.Is(err, flowdproto.ErrUnauthorized) {
		t.Errorf("wrong token: %v", err)
	}
	for _, h := range []string{"0", "x"} {
		req, _ := http.NewRequest(http.MethodGet, r.srv.URL+"/v1/hello", nil)
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set(flowdproto.HeaderProtocol, h)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("protocol %s: %d", h, resp.StatusCode)
		}
	}
	for _, path := range []string{"/v1/sessions/s1/events?after=-1", "/v1/sessions/s1/events?after=x"} {
		_ = os.MkdirAll(filepath.Join(r.root, "var/lib/flow/sessions/s1"), 0o755)
		_ = os.WriteFile(filepath.Join(r.root, layout.SessionSpecFile("s1")), []byte(`{}`), 0o644)
		req, _ := http.NewRequest(http.MethodGet, r.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		resp, _ := http.DefaultClient.Do(req)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	for _, body := range []struct{ path, method, body string }{
		{"/v1/sessions/s1", http.MethodPut, `{"id":"s2"}`},
		{"/v1/sessions/s1/commands", http.MethodPost, `{"session":"s2"}`},
		{"/v1/env", http.MethodPut, `nope`},
	} {
		req, _ := http.NewRequest(body.method, r.srv.URL+body.path, strings.NewReader(body.body))
		req.Header.Set("Authorization", "Bearer tok")
		resp, _ := http.DefaultClient.Do(req)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s: %d", body.method, body.path, resp.StatusCode)
		}
	}
}

// Parents flowd creates for an agent directory (/run/flow/sessions after a boot cleared the tmpfs) stay traversable
// for the agent user, or its host cannot bind its socket.
func TestMkdirAgent_parentsTraversable(t *testing.T) {
	root := t.TempDir()
	s := New(Options{Root: root})
	dir := filepath.Join(root, "run", "flow", "sessions", "s1")
	if err := s.mkdirAgent(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(root, "run", "flow"), filepath.Join(root, "run", "flow", "sessions")} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm()&0o001 == 0 {
			t.Errorf("%s: mode %v, %v: others cannot traverse it", p, fi.Mode().Perm(), err)
		}
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o750 {
		t.Errorf("agent dir mode %v", fi.Mode().Perm())
	}
}

// An interrupted clone leaves .git without a HEAD; the next EnsureRepo must clone again, not fetch forever.
func TestEnsureRepo_recoversFromAnInterruptedClone(t *testing.T) {
	r := newRig(t)
	broken := filepath.Join(r.root, layout.Checkout("toolkit"))
	_ = os.MkdirAll(broken, 0o755)
	gitRun(t, broken, "init", "--quiet")
	st, err := r.api.EnsureRepo(context.Background(), flowdproto.RepoSpec{Repo: "trick77/toolkit", DefaultBranch: "master"})
	if err != nil || len(st.HeadSHA) != 40 {
		t.Fatalf("EnsureRepo over a broken checkout = %+v, %v", st, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(broken)); len(entries) != 1 {
		t.Errorf("work holds %d entries, want only the checkout (no temporary clone left)", len(entries))
	}
}

// A session directory belongs to one session id: another session with the same name never adopts its worktrees;
// the same session after a crash (worktrees made, spec not written) does; stop-and-remove frees the name.
func TestEnsureSession_adoptsOnlyItsOwnWorktrees(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	spec := func(id string) flowdproto.SessionSpec {
		return flowdproto.SessionSpec{ID: id, Name: "same", Provider: event.ProviderClaude, Model: "m", Mode: "ask",
			Replay: true, Worktrees: []flowdproto.WorktreeSpec{{Repo: "trick77/toolkit", Branch: "flow/same", BaseRef: "origin/master"}}}
	}
	if _, err := r.api.EnsureSession(ctx, spec("s1")); err != nil {
		t.Fatal(err)
	}
	if err := r.api.StopSession(ctx, "s1", false); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(r.root, layout.SessionSpecFile("s1"))) // as if flowd died before writing it
	if _, err := r.api.EnsureSession(ctx, spec("s1")); err != nil {
		t.Fatalf("the same session after a crash: %v", err)
	}
	if _, err := r.api.EnsureSession(ctx, spec("s2")); err == nil {
		t.Fatal("another session adopted the directory and worktree of s1")
	}
	if err := r.api.StopSession(ctx, "s1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.api.EnsureSession(ctx, spec("s2")); err != nil {
		t.Errorf("name not freed by stop-and-remove: %v", err)
	}
}

// The larger of ?after and Last-Event-ID wins: a client reconnecting by itself keeps its old ?after.
func TestPosition(t *testing.T) {
	r := httptest.NewRequest("GET", "/?after=3", nil)
	r.Header.Set("Last-Event-ID", "12")
	if n, err := position(r); err != nil || n != 12 {
		t.Errorf("position = %d, %v, want 12", n, err)
	}
	r.Header.Set("Last-Event-ID", "x")
	if _, err := position(r); err == nil {
		t.Error("a bad Last-Event-ID accepted")
	}
}

// flakyGit fails every git call whose arguments contain fail, as an OOM-killed or interrupted git does.
type flakyGit struct {
	fail string
}

func (f flakyGit) Run(ctx context.Context, c Cmd) ([]byte, error) {
	if strings.Contains(strings.Join(c.Args, " "), f.fail) {
		return nil, errors.New("signal: killed")
	}
	return ExecRunner{}.Run(ctx, c)
}

// checkoutWithWork clones toolkit as the main checkout and adds a local branch with an unpushed commit: what a
// session's work looks like.
func checkoutWithWork(t *testing.T, r *rig) string {
	t.Helper()
	dir := filepath.Join(r.root, layout.Checkout("toolkit"))
	gitRun(t, "", "clone", "--quiet", r.base+"trick77/toolkit.git", dir)
	gitRun(t, dir, "checkout", "--quiet", "-b", "flow/work")
	_ = os.WriteFile(filepath.Join(dir, "work.txt"), []byte("unpushed"), 0o644)
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "unpushed work")
	return dir
}

func survives(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "work.txt")); err != nil {
		t.Fatalf("the checkout's unpushed work is gone: %v", err)
	}
	out, err := exec.Command("git", "-C", dir, "branch", "--list", "flow/work").Output()
	if err != nil || !strings.Contains(string(out), "flow/work") {
		t.Fatalf("local branch gone: %q %v", out, err)
	}
}

func TestEnsureRepo_neverDeletesWork(t *testing.T) {
	r := newRig(t)
	spec := flowdproto.RepoSpec{Repo: "trick77/toolkit", DefaultBranch: "master"}
	sv := func(run Runner) *Supervisor {
		return New(Options{Root: r.root, Run: run, Units: r.units, CloneBase: "file://" + r.base,
			Log: slog.New(slog.DiscardHandler)})
	}
	dir := checkoutWithWork(t, r)

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := sv(ExecRunner{}).EnsureRepo(ctx, spec); err == nil {
			t.Error("a cancelled EnsureRepo reported success")
		}
		survives(t, dir)
	})
	t.Run("transient rev-parse failure", func(t *testing.T) {
		if _, err := sv(flakyGit{fail: "rev-parse"}).EnsureRepo(context.Background(), spec); err == nil {
			t.Error("a failing check reported success")
		}
		survives(t, dir)
	})
	t.Run("transient ref listing failure", func(t *testing.T) {
		gitRun(t, dir, "symbolic-ref", "HEAD", "refs/heads/missing") // HEAD names no commit
		_, err := sv(flakyGit{fail: "for-each-ref"}).EnsureRepo(context.Background(), spec)
		if err == nil {
			t.Error("success although the refs could not be listed")
		}
		survives(t, dir)
	})
	t.Run("broken HEAD with local branches", func(t *testing.T) {
		_, err := sv(ExecRunner{}).EnsureRepo(context.Background(), spec)
		if !errors.Is(err, errRepoBroken) {
			t.Errorf("a broken checkout holding refs: %v, want errRepoBroken", err)
		}
		survives(t, dir)
		gitRun(t, dir, "checkout", "--quiet", "flow/work")
	})
	t.Run("a directory that is not a repository", func(t *testing.T) {
		other := filepath.Join(r.root, layout.Checkout("ship"))
		_ = os.MkdirAll(other, 0o755)
		_ = os.WriteFile(filepath.Join(other, "keep.txt"), []byte("x"), 0o644)
		if _, err := sv(ExecRunner{}).EnsureRepo(context.Background(), flowdproto.RepoSpec{Repo: "trick77/ship"}); !errors.Is(err, errRepoBroken) {
			t.Errorf("non-empty directory without .git: %v", err)
		}
		// Over the wire it is the same error.
		if _, err := r.api.EnsureRepo(context.Background(), flowdproto.RepoSpec{Repo: "trick77/ship"}); !errors.Is(err, flowdproto.ErrRepoBroken) {
			t.Errorf("over HTTP: %v", err)
		}
		if _, err := os.Stat(filepath.Join(other, "keep.txt")); err != nil {
			t.Error("unknown files deleted")
		}
	})
	if _, err := sv(ExecRunner{}).EnsureRepo(context.Background(), spec); err != nil {
		t.Errorf("healthy checkout: %v", err)
	}
	survives(t, dir)
}

func TestRun_servesUntilStopped(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "flowd.json")
	_ = os.WriteFile(cfg, []byte(`{"token":"tok","sandbox":"sb","listen":"127.0.0.1:0"}`), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, slog.New(slog.DiscardHandler), "1.2.3", cfg, &Options{Root: dir, Units: newUnits(),
			Run: ExecRunner{}})
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`nope`, `{}`} {
		_ = os.WriteFile(cfg, []byte(body), 0o600)
		if err := run(context.Background(), slog.New(slog.DiscardHandler), "1", cfg, &Options{}); err == nil {
			t.Errorf("config %s accepted", body)
		}
	}
	if err := Run(context.Background(), slog.New(slog.DiscardHandler), "1"); err == nil {
		t.Error("Run without /etc/flow/flowd.json succeeded")
	}
}

// fakeRun records commands and answers with canned output.
type fakeRun struct {
	cmds []Cmd
	out  string
	err  error
}

func (f *fakeRun) Run(_ context.Context, c Cmd) ([]byte, error) {
	f.cmds = append(f.cmds, c)
	return []byte(f.out), f.err
}

func TestSystemd(t *testing.T) {
	ctx := context.Background()
	f := &fakeRun{}
	s := Systemd{Run: f}
	if err := s.Start(ctx, "flow-session-s1.service", []string{"/opt/flow/releases/1/flowd", "host"}, []string{"MemoryMax=1"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.cmds[0].Args, " ")
	for _, want := range []string{"--unit=flow-session-s1.service", "--uid=65532", "--collect", "MemorySwapMax=0",
		"-p OOMPolicy=continue", "-p MemoryMax=1", "-- /opt/flow/releases/1/flowd host"} {
		if !strings.Contains(got, want) {
			t.Errorf("systemd-run %s lacks %q", got, want)
		}
	}
	for out, want := range map[string]flowdproto.UnitState{
		"ActiveState=active\nLoadState=loaded\n":      flowdproto.UnitActive,
		"ActiveState=inactive\nLoadState=not-found\n": flowdproto.UnitMissing,
		"ActiveState=activating\nLoadState=loaded\n":  flowdproto.UnitActivating,
		"ActiveState=failed\nLoadState=loaded\n":      flowdproto.UnitFailed,
		"ActiveState=inactive\nLoadState=loaded\n":    flowdproto.UnitInactive,
	} {
		f.out = out
		if st, err := s.State(ctx, "u"); err != nil || st != want {
			t.Errorf("%q → %s, %v", out, st, err)
		}
	}
	f.out = "flow-session-a.service loaded active running x\nflow-session-b.service loaded failed failed y\n"
	if units, err := s.List(ctx, "flow-session-*"); err != nil || len(units) != 2 || units[1] != "flow-session-b.service" {
		t.Errorf("List = %v, %v", units, err)
	}
	if err := s.Stop(ctx, "u"); err != nil {
		t.Error(err)
	}
	f.err = errors.New("boom")
	if _, err := s.State(ctx, "u"); err == nil {
		t.Error("State hid an error")
	}
	if _, err := s.List(ctx, "x"); err == nil {
		t.Error("List hid an error")
	}
	if _, err := (ExecRunner{}).Run(ctx, Cmd{Name: "false"}); err == nil {
		t.Error("ExecRunner hid an exit code")
	}
}
