package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
)

// seen records what each Changed would publish: the state and whether busy.
type seen struct {
	mu   sync.Mutex
	list []string
}

func (s *seen) watch(m *Manager) {
	m.Changed = func(id string) {
		sb, _ := m.Lookup(context.Background(), id)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.list = append(s.list, fmt.Sprintf("%s/%v", sb.State, sb.Busy))
	}
}

func (s *seen) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list
	s.list = nil
	return out
}

// idle waits until no operation runs on the project any more and the event of the last one's end is in (it is
// told just after the count drops).
func idle(t *testing.T, m *Manager, s *seen) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.list)
		ended := n > 0 && strings.HasSuffix(s.list[n-1], "/false")
		s.mu.Unlock()
		if !m.Busy(pid) && ended {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("still busy")
		}
		time.Sleep(time.Millisecond)
	}
}

// busyThroughout: every event but the last says busy, the last one not, and the last state is want.
func busyThroughout(t *testing.T, what string, got []string, want string) {
	t.Helper()
	if len(got) < 2 {
		t.Fatalf("%s: events %v", what, got)
	}
	for _, e := range got[:len(got)-1] {
		if e[len(e)-4:] != "true" {
			t.Errorf("%s: %v: an event before the end not busy", what, got)
		}
	}
	if last := got[len(got)-1]; last != want+"/false" {
		t.Errorf("%s: last event %s, want %s/false (%v)", what, last, want, got)
	}
}

func TestBusy_spansEachOperation(t *testing.T) {
	r := newRig(t)
	var s seen
	s.watch(r.m)
	ctx := t.Context()

	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	got := s.take()
	// The first event of the bring-up says busy already: the card never shows a setup left behind.
	if got[0] != "setup/true" {
		t.Errorf("first event %v", got)
	}
	busyThroughout(t, "bring-up", got, "warm")

	// A reconnect to a cached warm sandbox is no operation.
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if got := s.take(); len(got) != 0 {
		t.Errorf("cached Ensure published %v", got)
	}

	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	busyThroughout(t, "stop", s.take(), "stopped")

	r.m.Setup(pid)
	idle(t, r.m, &s)
	busyThroughout(t, "setup", s.take(), "warm")

	if err := r.m.Rebuild(ctx, pid); err != nil {
		t.Fatal(err)
	}
	idle(t, r.m, &s)
	busyThroughout(t, "rebuild", s.take(), "warm")

	if err := r.m.Delete(ctx, pid, nil); err != nil {
		t.Fatal(err)
	}
	busyThroughout(t, "delete", s.take(), "deleting")
}

func TestBusy_aSetupLeftBehindIsNot(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if err := r.m.Store.Put(ctx, Sandbox{ID: "s1", ProjectID: pid, Instance: InstanceName(pid), State: StateSetup}); err != nil {
		t.Fatal(err)
	}
	sb := r.m.Describe(ctx, pid)
	if sb.State != apitypes.SandboxStateSetup || sb.Busy {
		t.Errorf("describe %+v", sb)
	}
}

func TestBusy_whileASetupWaitsTheDeleteIsRefused(t *testing.T) {
	r := newRig(t)
	var s seen
	s.watch(r.m)
	ctx := t.Context()
	// A holder of the project's lock: a setup started now waits for it, busy all along.
	unlock := r.m.lock(pid)
	r.m.Setup(pid)
	if !r.m.Busy(pid) || !r.m.Describe(ctx, pid).Busy {
		t.Fatal("a waiting setup is not busy")
	}
	err := r.m.Delete(ctx, pid, nil)
	var ae *apitypes.Error
	if !errors.As(err, &ae) || ae.Code != apitypes.ErrorCodeSandboxBusy {
		t.Errorf("delete while busy: %v", err)
	}
	unlock()
	idle(t, r.m, &s)
	if sb := r.m.Describe(ctx, pid); sb.State != apitypes.SandboxStateWarm || sb.Busy {
		t.Errorf("after: %+v", sb)
	}
}

func TestBusy_endsOnce(t *testing.T) {
	r := newRig(t)
	outer := r.m.beginOp(pid)
	inner := r.m.beginOp(pid)
	inner.end()
	inner.end()
	if !r.m.Busy(pid) {
		t.Fatal("the outer operation ended with the inner one")
	}
	outer.end()
	if r.m.Busy(pid) {
		t.Fatal("still busy")
	}
	// Changed: once at the outer begin, once at its end.
	if len(r.events) != 2 {
		t.Errorf("events %v", r.events)
	}
	r.m.beginOp(pid).endQuietly()
	if r.m.Busy(pid) || len(r.events) != 3 {
		t.Errorf("a quiet end: busy %v, events %v", r.m.Busy(pid), r.events)
	}
}

func TestBusy_aReconnectCounts(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	var s seen
	s.watch(r.m)
	// Nothing cached (a Flow restart, a dropped connection): Warm reconnects under the lock.
	r.m.Forget(pid)
	if _, err := r.m.Warm(ctx, pid); err != nil {
		t.Fatal(err)
	}
	busyThroughout(t, "reconnect", s.take(), "warm")
}

func TestBusy_aProjectDeleteEndsWithoutAnEvent(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	var s seen
	s.watch(r.m)
	if err := r.m.Delete(ctx, pid, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// The project's removal follows; a sandbox.updated of state none before it would flash a card.
	for _, e := range s.take() {
		if e[len(e)-5:] == "false" {
			t.Errorf("an event after the delete's end: %s", e)
		}
	}
	if r.m.Busy(pid) {
		t.Error("still busy")
	}
}
