package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/eventlog"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
)

func logOf(t *testing.T, r *rig, id string) []event.Event {
	t.Helper()
	evs, err := eventlog.NewReader(filepath.Join(r.root, layout.EventLog(id))).Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// Stopping a session mid-turn sends the host its shutdown first: the log closes the open request and the turn as
// interrupted, instead of ending in an open turn that the next start would call lost.
func TestStopSession_midTurnClosesTheTurn(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.api.Command(ctx, command(t, "m1", event.CmdMessageSend, event.MessageSend{MessageID: "m1",
		Text: "go", Origin: event.MessageOriginUser})); err != nil {
		t.Fatal(err)
	}
	collect(t, r.api, 0, kindIs(event.KindPermissionRequested))
	if err := r.api.StopSession(ctx, "s1", false); err != nil {
		t.Fatal(err)
	}
	var resolved, ended bool
	for _, e := range logOf(t, r, "s1") {
		switch e.Kind {
		case event.KindPermissionResolved:
			resolved = strings.Contains(string(e.Data), `"expired"`)
		case event.KindTurnEnded:
			ended = strings.Contains(string(e.Data), `"interrupted"`)
		}
	}
	if !resolved || !ended {
		t.Errorf("log after a stop mid-turn: request expired %v, turn interrupted %v", resolved, ended)
	}
}

// A session whose spec is gone is still stopped (unit and credential file), not left running.
func TestStopSession_withoutASpec(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit")); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(r.root, layout.SessionSpecFile("s1")))
	if err := r.api.StopSession(ctx, "s1", false); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Errorf("stop without a spec: %v", err)
	}
	if st, _ := r.units.State(ctx, layout.HostUnit("s1")); st != flowdproto.UnitMissing {
		t.Errorf("host unit %s after the stop", st)
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.SessionEnvFile("s1"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("environment file left: %v", err)
	}
}

// killedAdd runs `git worktree add` and then fails, as a git killed midway leaves its directory behind.
type killedAdd struct{ on bool }

func (k *killedAdd) Run(ctx context.Context, c Cmd) ([]byte, error) {
	out, err := ExecRunner{}.Run(ctx, c)
	if k.on && len(c.Args) > 1 && c.Args[0] == "worktree" && c.Args[1] == "add" {
		return out, errors.New("signal: killed")
	}
	return out, err
}

// A worktree add that fails midway leaves no directory a later call would adopt as a finished worktree.
func TestAddWorktree_failedAddLeavesNothing(t *testing.T) {
	r := newRig(t)
	run := &killedAdd{on: true}
	s := New(Options{Root: r.root, Run: run, Units: r.units, CloneBase: "file://" + r.base,
		Log: slog.New(slog.DiscardHandler)})
	ctx := context.Background()
	w := flowdproto.WorktreeSpec{Repo: "trick77/toolkit", Branch: "flow/demo", BaseRef: "origin/master"}
	if _, err := s.addWorktree(ctx, "demo", w); err == nil {
		t.Fatal("the killed add succeeded")
	}
	dir := filepath.Join(r.root, layout.Worktree("demo", "toolkit"))
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("half-made worktree left: %v", err)
	}
	if list := gitOut(t, filepath.Join(r.root, layout.Checkout("toolkit")), "worktree", "list"); strings.Contains(list, "demo") {
		t.Errorf("git still lists it: %s", list)
	}
	run.on = false
	if _, err := s.addWorktree(ctx, "demo", w); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// movingBase moves the base ref to a new commit right after `git worktree add`, as another session's fetch does once
// the checkout's lock is free.
type movingBase struct {
	t        *testing.T
	checkout string
}

func (m *movingBase) Run(ctx context.Context, c Cmd) ([]byte, error) {
	out, err := ExecRunner{}.Run(ctx, c)
	if err == nil && len(c.Args) > 1 && c.Args[0] == "worktree" && c.Args[1] == "add" {
		next := gitOut(m.t, m.checkout, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit-tree", "origin/master^{tree}", "-p", "origin/master", "-m", "upstream")
		gitRun(m.t, m.checkout, "update-ref", "refs/remotes/origin/master", next)
	}
	return out, err
}

// The recorded base is the commit the branch was cut from, also when the ref moves right after the add and when an
// existing worktree is adopted later.
func TestAddWorktree_baseIsTheCutCommit(t *testing.T) {
	r := newRig(t)
	checkout := filepath.Join(r.root, layout.Checkout("toolkit"))
	s := New(Options{Root: r.root, Run: &movingBase{t: t, checkout: checkout}, Units: r.units,
		CloneBase: "file://" + r.base, Log: slog.New(slog.DiscardHandler)})
	ctx := context.Background()
	w := flowdproto.WorktreeSpec{Repo: "trick77/toolkit", Branch: "flow/demo", BaseRef: "origin/master"}
	st, err := s.addWorktree(ctx, "demo", w)
	if err != nil {
		t.Fatal(err)
	}
	cut := gitOut(t, filepath.Join(r.root, layout.Worktree("demo", "toolkit")), "rev-parse", "HEAD")
	if st.BaseSHA != cut {
		t.Errorf("base %s, the branch was cut from %s", st.BaseSHA, cut)
	}
	if st, err = s.addWorktree(ctx, "demo", w); err != nil || st.BaseSHA != cut {
		t.Errorf("adopted: base %s (%v), the branch was cut from %s", st.BaseSHA, err, cut)
	}
}

// mkdirAgent never follows a link planted at its name (flowd chowns as root).
func TestMkdirAgent_refusesALink(t *testing.T) {
	root := t.TempDir()
	s := New(Options{Root: root})
	target := filepath.Join(root, "secret")
	_ = os.Mkdir(target, 0o700)
	link := filepath.Join(root, "work", "planted")
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := s.mkdirAgent(link); err == nil {
		t.Error("a symlink was accepted as the agent's directory")
	}
}

// A worktree the spec recorded while the host was down (a start that failed afterwards) is announced by the next
// start, not only by the call that made it.
func TestEnsureSession_announcesWorktreesTheLogLacks(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit")); err != nil {
		t.Fatal(err)
	}
	if err := r.api.StopSession(ctx, "s1", false); err != nil {
		t.Fatal(err)
	}
	// A start that made ship's worktree and wrote the spec, then failed before its host answered.
	s := New(Options{Root: r.root, Run: ExecRunner{}, CloneBase: "file://" + r.base, Log: slog.New(slog.DiscardHandler)})
	sp, err := s.readSpec("s1")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.addWorktree(ctx, "demo", flowdproto.WorktreeSpec{Repo: "trick77/ship", Branch: "flow/demo",
		BaseRef: "origin/master"})
	if err != nil {
		t.Fatal(err)
	}
	sp.Worktrees = append(sp.Worktrees, st)
	if err := s.writeSpec(sp); err != nil {
		t.Fatal(err)
	}
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit", "trick77/ship")); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range logOf(t, r, "s1") {
		var w event.WorktreeAdded
		if e.Kind == event.KindWorktreeAdded && json.Unmarshal(e.Data, &w) == nil && w.Repo == "trick77/ship" {
			found = true
		}
	}
	if !found {
		t.Error("ship's worktree never reached the log")
	}
}

// Two AddWorktree calls at once both end up in the spec.
func TestAddWorktree_concurrentCallsKeepBoth(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, repo := range []string{"trick77/toolkit", "trick77/ship"} {
		wg.Go(func() {
			if _, err := r.api.AddWorktree(ctx, "s1", flowdproto.WorktreeSpec{Repo: repo, Branch: "flow/demo",
				BaseRef: "origin/master"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	st, err := r.api.Session(ctx, "s1")
	if err != nil || len(st.Worktrees) != 2 {
		t.Errorf("spec after two concurrent adds: %+v, %v", st.Worktrees, err)
	}
}
