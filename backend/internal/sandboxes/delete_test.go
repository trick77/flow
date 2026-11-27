package sandboxes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/sandbox"
)

const instance = "flow-b679dbb3324e"

// failingDelete is the fake runtime with a delete (or a state read) that fails.
type failingDelete struct {
	sandbox.Runtime
	deleteErr, stateErr error
}

func (f failingDelete) Delete(ctx context.Context, name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.Runtime.Delete(ctx, name)
}

func (f failingDelete) State(ctx context.Context, name string) (sandbox.State, error) {
	if f.stateErr != nil {
		return sandbox.State{}, f.stateErr
	}
	return f.Runtime.State(ctx, name)
}

func code(err error) apitypes.ErrorCode {
	var e *apitypes.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Delete stops and deletes a running instance, frees its address and leaves the row deleting: nothing brings the
// sandbox up again until the project's rows take the row with them.
func TestDelete(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Delete(ctx, pid, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.rt.Instance(instance); ok {
		t.Error("instance still there")
	}
	sb, err := r.m.Store.ByProject(ctx, pid)
	if err != nil || sb.State != StateDeleting || sb.IPv4 != "" {
		t.Errorf("row %+v, %v", sb, err)
	}
	if _, err := r.m.Ensure(ctx, pid); code(err) != apitypes.ErrorCodeSandboxUnavailable {
		t.Errorf("Ensure of a deleted sandbox: %v", err)
	}
	if _, err := r.m.Warm(ctx, pid); !errors.Is(err, ErrNotWarm) {
		t.Errorf("Warm of a deleted sandbox: %v", err)
	}
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Errorf("Stop of a deleted sandbox: %v", err)
	}
	if err := r.m.Rebuild(ctx, pid); code(err) != apitypes.ErrorCodeSandboxUnavailable {
		t.Errorf("Rebuild of a deleted sandbox: %v", err)
	}
	if _, ok := r.rt.Instance(instance); ok {
		t.Error("something made the instance again")
	}
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateDeleting {
		t.Errorf("Describe = %+v", d)
	}
	// Again (a retry after the rows failed to go): the instance is gone already.
	if err := r.m.Delete(ctx, pid, nil); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

// The project's rows go under the same hold of the lock as the instance. When they cannot (a session made after the
// count), the project stays usable without an instance: the row turns stopped and a bring-up makes the instance anew.
func TestDelete_rowsFail(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	r.m.LockWait = 20 * time.Millisecond
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("project has sessions")
	err := r.m.Delete(ctx, pid, func(context.Context) error {
		if unlock, err := r.m.tryLock(pid); err == nil {
			unlock()
			t.Error("rows ran without the project's lock")
		}
		if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateDeleting {
			t.Errorf("state while the rows go %s", d.State)
		}
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("Delete = %v, want the rows' error", err)
	}
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateStopped {
		t.Errorf("after the rows failed: %+v", d)
	}
	if _, err := r.m.Warm(ctx, pid); !errors.Is(err, ErrNotWarm) {
		t.Errorf("Warm: %v", err)
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatalf("Ensure after the rows failed: %v", err)
	}
	if in, ok := r.rt.Instance(instance); !ok || in.Status != sandbox.StatusRunning {
		t.Errorf("instance %+v", in)
	}
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateWarm {
		t.Errorf("after the bring-up: %+v", d)
	}
}

// A project never brought up gets a row in state deleting, so a setup queued before the delete cannot make an
// instance the project's rows would no longer know.
func TestDelete_neverBroughtUp(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if err := r.m.Delete(ctx, pid, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Ensure(ctx, pid); err == nil {
		t.Error("a bring-up after the delete went through")
	}
	if _, ok := r.rt.Instance(instance); ok {
		t.Error("instance made after the delete")
	}
}

// A stopped instance is deleted without a stop; one that cannot be deleted leaves the row failed and says why.
func TestDelete_stoppedAndFailing(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	fake := r.m.Runtime
	r.m.Runtime = failingDelete{Runtime: fake, deleteErr: errors.New("incus said no")}
	if err := r.m.Delete(ctx, pid, nil); code(err) != apitypes.ErrorCodeSandboxUnavailable {
		t.Errorf("failing delete: %v", err)
	}
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateFailed {
		t.Errorf("after a failed delete: %+v", d)
	}
	r.m.Runtime = failingDelete{Runtime: fake, stateErr: errors.New("incus away")}
	if err := r.m.Delete(ctx, pid, nil); code(err) != apitypes.ErrorCodeSandboxUnavailable {
		t.Errorf("unreadable state: %v", err)
	}
	r.m.Runtime = fake
	if err := r.m.Delete(ctx, pid, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.rt.Instance(instance); ok {
		t.Error("stopped instance not deleted")
	}
}

// Delete and Rebuild answer sandbox_busy while a bring-up holds the project, instead of waiting minutes for it.
func TestDeleteAndRebuild_busy(t *testing.T) {
	r := newRig(t)
	unlock := r.m.lock(pid)
	defer unlock()
	done := make(chan [2]error)
	go func() { done <- [2]error{r.m.Delete(t.Context(), pid, nil), r.m.Rebuild(t.Context(), pid)} }()
	select {
	case errs := <-done:
		for _, err := range errs {
			if code(err) != apitypes.ErrorCodeSandboxBusy {
				t.Errorf("during a bring-up: %v", err)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("waited for the bring-up's lock")
	}
}

func waitState(t *testing.T, r *rig, state apitypes.SandboxState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.m.Describe(t.Context(), pid).State != state {
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s", r.m.Describe(t.Context(), pid).State, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Rebuild makes a new instance from the base image on the same address, delivers flowd to it again and clones the
// repositories afresh.
func TestRebuild(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	before, _ := r.m.Store.ByProject(ctx, pid)
	// Something on the old instance's filesystem, which a new instance does not have.
	if err := r.rt.Push(ctx, instance, sandbox.File{Path: layout.Config + ".old", Content: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Rebuild(ctx, pid); err != nil {
		t.Fatal(err)
	}
	waitState(t, r, apitypes.SandboxStateWarm)
	in, ok := r.rt.Instance(instance)
	if !ok || in.Status != sandbox.StatusRunning {
		t.Fatalf("instance after the rebuild %+v", in)
	}
	if _, ok := in.Files[layout.Config+".old"]; ok {
		t.Error("the old instance's filesystem survived the rebuild")
	}
	after, _ := r.m.Store.ByProject(ctx, pid)
	eps := r.d.endpoints
	if after.ID != before.ID || len(eps) != 2 || eps[0].Address != eps[1].Address || eps[0].Token != eps[1].Token {
		t.Errorf("row before %+v, after %+v, endpoints %+v", before, after, eps)
	}
	// A project never brought up: the rebuild is its first bring-up.
	r2 := newRig(t)
	if err := r2.m.Rebuild(t.Context(), pid); err != nil {
		t.Fatal(err)
	}
	waitState(t, r2, apitypes.SandboxStateWarm)
}

func TestRebuild_failingDelete(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.m.Runtime = failingDelete{Runtime: r.m.Runtime, deleteErr: errors.New("incus said no")}
	if err := r.m.Rebuild(ctx, pid); code(err) != apitypes.ErrorCodeSandboxUnavailable {
		t.Errorf("failing delete: %v", err)
	}
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateFailed {
		t.Errorf("after a failed rebuild: %+v", d)
	}
	_ = r.db.Close()
	if err := r.m.Rebuild(ctx, pid); err == nil {
		t.Error("closed database: no error")
	}
	if err := r.m.Delete(ctx, pid, nil); err == nil {
		t.Error("closed database: no error")
	}
}

// A setup asked for shows at once: a new project's first row, a stopped sandbox. A bring-up running records its own
// state, and a sandbox being deleted stays so.
func TestMarkSetup(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	unlock := r.m.lock(pid)
	r.m.markSetup(pid)
	unlock()
	if _, err := r.m.Store.ByProject(ctx, pid); !errors.Is(err, ErrNotFound) {
		t.Errorf("marked during a bring-up: %v", err)
	}
	r.m.markSetup("no-such-project")
	if rows, _ := r.m.Store.List(ctx); len(rows) != 0 {
		t.Errorf("a row for a missing project: %+v", rows)
	}
	r.m.markSetup(pid)
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateSetup {
		t.Errorf("new project: %+v", d)
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.m.markSetup(pid)
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateWarm {
		t.Errorf("warm sandbox: %+v", d)
	}
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.m.markSetup(pid)
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateSetup {
		t.Errorf("stopped sandbox: %+v", d)
	}
	if err := r.m.Delete(ctx, pid, nil); err != nil {
		t.Fatal(err)
	}
	r.m.markSetup(pid)
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateDeleting {
		t.Errorf("deleted sandbox: %+v", d)
	}
	_ = r.db.Close()
	r.m.markSetup(pid) // logged, nothing else
}

func TestAPI_RebuildSandbox(t *testing.T) {
	r := newRig(t)
	active := 1
	a := &API{Manager: r.m, ActiveSessions: func(context.Context, string) (int, error) {
		if active < 0 {
			return 0, errors.New("down")
		}
		return active, nil
	}}
	do := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", nil)
		req.SetPathValue("projectId", id)
		w := httptest.NewRecorder()
		a.RebuildSandbox(w, req)
		return w
	}
	if w := do("nope"); w.Code != 404 {
		t.Errorf("unknown project: %d", w.Code)
	}
	if w := do(pid); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "project_has_sessions") {
		t.Errorf("with a session in use: %d %s", w.Code, w.Body)
	}
	active = -1
	if w := do(pid); w.Code != 500 {
		t.Errorf("counting failed: %d", w.Code)
	}
	active = 0
	if w := do(pid); w.Code != http.StatusAccepted {
		t.Errorf("rebuild: %d %s", w.Code, w.Body)
	}
	waitState(t, r, apitypes.SandboxStateWarm)
	unlock := r.m.lock(pid)
	if w := do(pid); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "sandbox_busy") {
		t.Errorf("during a bring-up: %d %s", w.Code, w.Body)
	}
	unlock()
}
