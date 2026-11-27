package sandboxes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/sandbox"
)

// hangingDelete is the fake runtime with a delete that waits until its context ends (Incus not answering).
type hangingDelete struct{ sandbox.Runtime }

func (h hangingDelete) Delete(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

// A removal that runs out of its time still records failed: the write must not use the expired context.
func TestDeleteAndRebuild_timeoutRecordsFailed(t *testing.T) {
	old := removeTimeout
	removeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { removeTimeout = old })
	for name, op := range map[string]func(m *Manager) error{
		"delete":  func(m *Manager) error { return m.Delete(t.Context(), pid) },
		"rebuild": func(m *Manager) error { return m.Rebuild(t.Context(), pid) },
	} {
		r := newRig(t)
		if _, err := r.m.Ensure(t.Context(), pid); err != nil {
			t.Fatal(err)
		}
		r.m.Runtime = hangingDelete{Runtime: r.m.Runtime}
		if err := op(r.m); code(err) != apitypes.ErrorCodeSandboxUnavailable {
			t.Errorf("%s: %v", name, err)
		}
		if d := r.m.Describe(t.Context(), pid); d.State != apitypes.SandboxStateFailed {
			t.Errorf("%s: state %s after the removal timed out, want failed", name, d.State)
		}
	}
}

// A short holder of the project's lock (a Warm, an environment push) makes neither a delete nor a setup mark give
// up; a long one (a bring-up) does, after LockWait.
func TestTryLock_waitsForShortHolders(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	r.m.LockWait = time.Second
	hold := func(d time.Duration) {
		unlock := r.m.lock(pid)
		go func() {
			time.Sleep(d)
			unlock()
		}()
	}
	hold(50 * time.Millisecond)
	r.m.markSetup(pid)
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateSetup {
		t.Errorf("setup mark behind a short holder: %s", d.State)
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	hold(50 * time.Millisecond)
	if err := r.m.Delete(ctx, pid); err != nil {
		t.Errorf("delete behind a short holder: %v", err)
	}
	r2 := newRig(t)
	r2.m.LockWait = 50 * time.Millisecond
	unlock := r2.m.lock(pid)
	defer unlock()
	t0 := time.Now()
	if err := r2.m.Delete(ctx, pid); code(err) != apitypes.ErrorCodeSandboxBusy {
		t.Errorf("delete behind a bring-up: %v", err)
	}
	if time.Since(t0) < 50*time.Millisecond {
		t.Error("answered busy without waiting")
	}
}

// The count of open sessions is asked again under the project's lock: a session made after the API counted keeps
// its sandbox.
func TestDeleteAndRebuild_sessionMadeMeanwhile(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	open := 1
	r.m.ActiveSessions = func(context.Context, string) (int, error) {
		if open < 0 {
			return 0, errors.New("down")
		}
		return open, nil
	}
	for name, op := range map[string]func() error{
		"delete":  func() error { return r.m.Delete(ctx, pid) },
		"rebuild": func() error { return r.m.Rebuild(ctx, pid) },
	} {
		if err := op(); code(err) != apitypes.ErrorCodeProjectHasSessions {
			t.Errorf("%s: %v", name, err)
		}
	}
	open = -1
	if err := r.m.Delete(ctx, pid); err == nil {
		t.Error("count failed: no error")
	}
	if in, ok := r.rt.Instance(instance); !ok || in.Status != sandbox.StatusRunning {
		t.Errorf("instance touched: %+v", in)
	}
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateWarm {
		t.Errorf("state %s", d.State)
	}
	open = 0
	if err := r.m.Delete(ctx, pid); err != nil {
		t.Error(err)
	}
}

// At Flow's start a row left in setup, stopping or deleting turns failed, from which the sandbox starts again; other
// states stay.
func TestReconcile(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	sb, _ := r.m.Store.ByProject(ctx, pid)
	for _, state := range []string{StateSetup, StateStopping, StateDeleting, StateWarm, StateStopped} {
		if err := r.m.Store.SetState(ctx, sb.ID, state, "", ""); err != nil {
			t.Fatal(err)
		}
		if err := r.m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		want := apitypes.SandboxState(state)
		if state == StateSetup || state == StateStopping || state == StateDeleting {
			want = apitypes.SandboxStateFailed
		}
		if d := r.m.Describe(ctx, pid); d.State != want {
			t.Errorf("%s: %s, want %s", state, d.State, want)
		}
	}
	_ = r.m.Store.SetState(ctx, sb.ID, StateSetup, "", "")
	if err := r.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	r.m.Forget(pid)
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Errorf("start after a reconciled setup: %v", err)
	}
	_ = r.db.Close()
	if err := r.m.Reconcile(ctx); err == nil {
		t.Error("closed database: no error")
	}
}

// Lookup tells a project without a row (none) from a row that cannot be read.
func TestLookup(t *testing.T) {
	r := newRig(t)
	if sb, err := r.m.Lookup(t.Context(), pid); err != nil || sb.State != apitypes.SandboxStateNone {
		t.Errorf("no row: %+v, %v", sb, err)
	}
	_ = r.db.Close()
	if _, err := r.m.Lookup(t.Context(), pid); err == nil {
		t.Error("closed database: no error")
	}
}
