package supervisor

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
)

func sessionSpec(id, name string, repos ...string) flowdproto.SessionSpec {
	sp := flowdproto.SessionSpec{ID: id, Name: name, Provider: event.ProviderClaude, Model: "m", Mode: "ask",
		Replay: true}
	for _, r := range repos {
		sp.Worktrees = append(sp.Worktrees, flowdproto.WorktreeSpec{Repo: r, Branch: "flow/" + name, BaseRef: "origin/master"})
	}
	return sp
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// Finding 1: a creation that fails while its worktrees are being made can be undone by stop-with-remove, and the
// name is free again afterwards.
func TestFailedCreationCanBeUndone(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "same", "trick77/toolkit", "trick77/missing")); err == nil {
		t.Fatal("creation with a missing repository succeeded")
	}
	if err := r.api.StopSession(ctx, "s1", true); err != nil {
		t.Fatalf("undo of the failed creation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.Worktree("same", "toolkit"))); err == nil {
		t.Error("the worktree made before the failure is still there")
	}
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s2", "same", "trick77/toolkit")); err != nil {
		t.Errorf("the name stays blocked: %v", err)
	}
}

// Stopping a unit that never started (a creation that failed before its host) is not an error: the undo goes on.
func TestSystemdStop_unitNotLoaded(t *testing.T) {
	// systemctl says "Failed to stop u.service: Unit u.service not loaded."; the match is on "not loaded".
	f := &fakeRun{err: errors.New("systemctl stop u.service: exit status 5: unit u.service not loaded")}
	if err := (Systemd{Run: f}).Stop(context.Background(), "u.service"); err != nil {
		t.Errorf("stop of a unit that is not loaded: %v", err)
	}
	f.err = errors.New("systemctl stop u.service: exit status 1: Access denied")
	if err := (Systemd{Run: f}).Stop(context.Background(), "u.service"); err == nil {
		t.Error("a real stop failure was hidden")
	}
}

// Finding 4: stop-with-remove completes when an earlier attempt already removed a worktree.
func TestStopWithRemove_isRetryable(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit", "trick77/ship")); err != nil {
		t.Fatal(err)
	}
	// An earlier attempt got as far as the first worktree.
	checkout := filepath.Join(r.root, layout.Checkout("toolkit"))
	gitRun(t, checkout, "worktree", "remove", "--force", filepath.Join(r.root, layout.Worktree("demo", "toolkit")))
	if err := r.api.StopSession(ctx, "s1", true); err != nil {
		t.Fatalf("retried stop-with-remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.Worktree("demo", "ship"))); err == nil {
		t.Error("the second worktree was not removed")
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.SessionClaim("demo"))); err == nil {
		t.Error("the claim was not freed")
	}
}

// Finding 6: a new session never inherits a leftover branch: one without commits of its own is reset to the fresh
// base, one with commits of its own is refused and kept. Stop-with-remove deletes a session branch only when it holds
// no commits beyond its base.
func TestLeftoverBranches(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: "trick77/toolkit", DefaultBranch: "master"}); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(r.root, layout.Checkout("toolkit"))
	old := gitOut(t, checkout, "rev-parse", "HEAD")
	gitRun(t, checkout, "branch", "flow/empty", old) // a leftover with nothing of its own
	// The remote moves on.
	work := filepath.Join(t.TempDir(), "w")
	gitRun(t, "", "clone", "--quiet", r.base+"trick77/toolkit.git", work)
	_ = os.WriteFile(filepath.Join(work, "new.txt"), []byte("new"), 0o644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "--quiet", "-m", "second")
	gitRun(t, work, "push", "--quiet", "origin", "HEAD:master")
	fresh := gitOut(t, work, "rev-parse", "HEAD")

	st, err := r.api.EnsureSession(ctx, sessionSpec("s1", "empty", "trick77/toolkit"))
	if err != nil {
		t.Fatal(err)
	}
	if head := gitOut(t, filepath.Join(r.root, layout.Worktree("empty", "toolkit")), "rev-parse", "HEAD"); head != fresh || st.Worktrees[0].BaseSHA != fresh {
		t.Errorf("new session on %s (base recorded %s), want the fresh base %s", head, st.Worktrees[0].BaseSHA, fresh)
	}

	// A leftover with a commit of its own is never taken over or reset.
	gitRun(t, checkout, "checkout", "--quiet", "-b", "flow/kept", old)
	_ = os.WriteFile(filepath.Join(checkout, "mine.txt"), []byte("unpushed"), 0o644)
	gitRun(t, checkout, "add", ".")
	gitRun(t, checkout, "commit", "--quiet", "-m", "unpushed")
	mine := gitOut(t, checkout, "rev-parse", "HEAD")
	gitRun(t, checkout, "checkout", "--quiet", "master")
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s2", "kept", "trick77/toolkit")); err == nil {
		t.Error("a new session took over a branch with commits of its own")
	}
	if got := gitOut(t, checkout, "rev-parse", "flow/kept"); got != mine {
		t.Errorf("the branch with work moved: %s, want %s", got, mine)
	}

	// Stop-with-remove: the empty session branch goes, one with work stays.
	if err := r.api.StopSession(ctx, "s1", true); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command("git", "-C", checkout, "branch", "--list", "flow/empty").Output(); strings.TrimSpace(string(out)) != "" {
		t.Error("a session branch without commits of its own was kept")
	}
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s3", "work", "trick77/toolkit")); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(r.root, layout.Worktree("work", "toolkit"))
	_ = os.WriteFile(filepath.Join(wt, "w.txt"), []byte("w"), 0o644)
	gitRun(t, wt, "add", ".")
	gitRun(t, wt, "commit", "--quiet", "-m", "session work")
	if err := r.api.StopSession(ctx, "s3", true); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command("git", "-C", checkout, "branch", "--list", "flow/work").Output(); strings.TrimSpace(string(out)) == "" {
		t.Error("a session branch with commits of its own was deleted")
	}
}

// Finding 2: a worktree added while the host is down gets its worktree.added once the host runs.
func TestAddWorktree_whileTheHostIsDown(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit")); err != nil {
		t.Fatal(err)
	}
	if err := r.api.StopSession(ctx, "s1", false); err != nil {
		t.Fatal(err)
	}
	w := flowdproto.WorktreeSpec{Repo: "trick77/ship", Branch: "flow/demo", BaseRef: "origin/master"}
	if _, err := r.api.AddWorktree(ctx, "s1", w); err == nil {
		t.Error("AddWorktree with the host down reported success")
	}
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.api.AddWorktree(ctx, "s1", w); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, r.api, 0, func(e event.Event) bool {
		return e.Kind == event.KindWorktreeAdded && strings.Contains(string(e.Data), "trick77/ship")
	})
	if len(evs) == 0 {
		t.Error("no worktree.added for the worktree added while the host was down")
	}
}

// Finding 10, through the real host: the command result says whether the message was queued.
func TestCommand_reportsQueued(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit")); err != nil {
		t.Fatal(err)
	}
	msg := func(id string) event.Command {
		return command(t, id, event.CmdMessageSend, event.MessageSend{MessageID: id, Text: id, Origin: event.MessageOriginUser})
	}
	if res, err := r.api.Command(ctx, msg("m1")); err != nil || res.Queued {
		t.Fatalf("first message: %+v, %v", res, err)
	}
	collect(t, r.api, 0, kindIs(event.KindPermissionRequested)) // the turn waits for the owner
	if res, err := r.api.Command(ctx, msg("m2")); err != nil || !res.Queued {
		t.Errorf("message behind the turn: %+v, %v", res, err)
	}
	if res, err := r.api.Command(ctx, msg("m2")); err != nil || !res.Queued || !res.Duplicate {
		t.Errorf("retry of the queued message: %+v, %v", res, err)
	}
}

// Finding 9: an id that is not a layout name never reaches the filesystem, on any route.
func TestInvalidSessionIDs(t *testing.T) {
	r := newRig(t)
	for _, path := range []string{"/v1/sessions/%2e%2e", "/v1/sessions/..%2fetc", "/v1/sessions/Bad.Name",
		"/v1/sessions/%2e%2e/events", "/v1/sessions/Bad.Name?remove=true"} {
		method := http.MethodGet
		if strings.Contains(path, "remove") {
			method = http.MethodDelete
		}
		req, _ := http.NewRequest(method, r.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s: %d, want 400", method, path, resp.StatusCode)
		}
	}
	if _, err := r.api.Session(context.Background(), "Bad.Name"); errors.Is(err, flowdproto.ErrNotFound) {
		t.Error("an invalid id was looked up")
	}
}
