package supervisor

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/eventlog"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/host"
	"github.com/trick77/flow/internal/layout"
)

// Stop-with-remove (archive) keeps a worktree with uncommitted or untracked work, with its branch, the session
// directory and its claim; a clean worktree beside it is removed as before.
func TestStopWithRemove_keepsADirtyWorktree(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit", "trick77/ship")); err != nil {
		t.Fatal(err)
	}
	dirty := filepath.Join(r.root, layout.Worktree("demo", "toolkit"))
	if err := os.WriteFile(filepath.Join(dirty, "notes.txt"), []byte("unsaved work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.api.StopSession(ctx, "s1", true); err != nil {
		t.Fatalf("archive with a dirty worktree: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dirty, "notes.txt")); err != nil || string(b) != "unsaved work" {
		t.Errorf("the untracked work is gone: %q %v", b, err)
	}
	if got := gitOut(t, filepath.Join(r.root, layout.Checkout("toolkit")), "branch", "--list", "flow/demo"); got == "" {
		t.Error("the dirty worktree's branch was deleted")
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.Worktree("demo", "ship"))); err == nil {
		t.Error("the clean worktree was kept")
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.SessionClaim("demo"))); err != nil {
		t.Error("the claim was freed while a worktree of the session is kept")
	}
	// Once the owner has dealt with the work, a second stop-with-remove removes it.
	if err := os.Remove(filepath.Join(dirty, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if err := r.api.StopSession(ctx, "s1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dirty); err == nil {
		t.Error("the clean worktree stays after the second stop")
	}
}

// Archiving works after the base ref is gone (fetch --prune after an upstream rename): the branch is judged against
// the recorded base commit and, holding nothing of its own, deleted.
func TestStopWithRemove_baseRefGone(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit")); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(r.root, layout.Checkout("toolkit"))
	gitRun(t, checkout, "update-ref", "-d", "refs/remotes/origin/master")
	if err := r.api.StopSession(ctx, "s1", true); err != nil {
		t.Fatalf("archive without the base ref: %v", err)
	}
	if got := gitOut(t, checkout, "branch", "--list", "flow/demo"); got != "" {
		t.Errorf("branch without commits of its own kept: %q", got)
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.Worktree("demo", "toolkit"))); err == nil {
		t.Error("the clean worktree was kept")
	}
}

// A host that accepts on its socket but never answers costs Hello at most its short budget, well inside Flow's 3 s,
// and is reported by its unit state alone.
func TestHello_stalledHost(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	const id = "stall"
	unit := layout.HostUnit(id)
	done := make(chan struct{})
	go func() { <-ctx.Done(); close(done) }()
	r.units.mu.Lock()
	r.units.running[unit], r.units.done[unit] = cancel, done
	r.units.mu.Unlock()
	writeSpecFile(t, r.root, id)
	sock := filepath.Join(r.root, layout.HostSocket(id))
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
		t.Fatalf("hello with a stalled host: %v after %s", err, time.Since(t0))
	}
	if len(h.Sessions) != 1 || h.Sessions[0].ID != id || h.Sessions[0].Unit != flowdproto.UnitActive ||
		h.Sessions[0].Status != event.SessionStatusIdle {
		t.Errorf("stalled host reported as %+v", h.Sessions)
	}
}

// writeSpecFile writes a session's spec as EnsureSession does.
func writeSpecFile(t *testing.T, root, id string) {
	t.Helper()
	path := filepath.Join(root, layout.SessionSpecFile(id))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Root: root, Log: slog.New(slog.DiscardHandler)})
	if err := s.writeSpec(host.Spec{Session: flowdproto.SessionSpec{ID: id, Name: id}, Dir: layout.SessionDir(id)}); err != nil {
		t.Fatal(err)
	}
}

// Session of a session without a running host reads its log on from where the last read ended, with one reader kept
// per session: a line before that point is never parsed again. Stop-with-remove drops the reader.
func TestSession_readsTheLogOnFromTheLastRead(t *testing.T) {
	root := t.TempDir()
	const id = "s1"
	writeSpecFile(t, root, id)
	s := New(Options{Root: root, Units: newUnits(), Log: slog.New(slog.DiscardHandler)})
	path := filepath.Join(root, layout.EventLog(id))
	w, _, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	appendStatus := func(st event.SessionStatus) {
		t.Helper()
		e, err := event.New(id, event.KindSessionStatus, time.Unix(0, 0), event.SessionStatusChanged{Status: st})
		if err == nil {
			_, err = w.Append(e)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for range 300 { // past the reader's index stride
		appendStatus(event.SessionStatusWorking)
	}
	appendStatus(event.SessionStatusDone)
	w.Sync()
	ctx := context.Background()
	st, err := s.Session(ctx, id)
	if err != nil || st.LastSeq != 301 || st.Status != event.SessionStatusDone {
		t.Fatalf("first read: %+v %v", st, err)
	}
	// Spoil an early line in place: a read from the log's start would fail on it.
	b, err := os.ReadFile(path) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	b[1] = '!'
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	appendStatus(event.SessionStatusIdle)
	w.Sync()
	if st, err := s.Session(ctx, id); err != nil || st.LastSeq != 302 || st.Status != event.SessionStatusIdle {
		t.Errorf("second read: %+v %v", st, err)
	}
	if err := s.StopSession(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, kept := s.logs[id]
	s.mu.Unlock()
	if kept {
		t.Error("the reader of a removed session stays")
	}
}
