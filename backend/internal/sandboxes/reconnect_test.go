package sandboxes

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/sandbox"
	"github.com/trick77/flow/internal/sandbox/sandboxfake"
)

// failingRepo fails EnsureRepo while fails is above zero, as a fetch does while GitHub is unreachable.
type failingRepo struct {
	flowdproto.API
	fails *atomic.Int32
}

func (f failingRepo) EnsureRepo(ctx context.Context, r flowdproto.RepoSpec) (flowdproto.RepoState, error) {
	if f.fails.Add(-1) >= 0 {
		return flowdproto.RepoState{}, errors.New("fetch: could not resolve host")
	}
	return f.API.EnsureRepo(ctx, r)
}

type failingRepoDialer struct {
	d     flowdproto.Dialer
	fails *atomic.Int32
}

func (d failingRepoDialer) Dial(e flowdproto.Endpoint) flowdproto.API {
	return failingRepo{API: d.d.Dial(e), fails: d.fails}
}

// A reconnect through Warm that fails (a fetch while GitHub is away) leaves the running sandbox warm: the next Warm
// tries again and succeeds, instead of every later Warm answering not warm.
func TestWarm_failedReconnectKeepsTheSandboxWarm(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	fails := &atomic.Int32{}
	fails.Store(1)
	r.m.Dialer = failingRepoDialer{d: r.d, fails: fails}
	r.m.Forget(pid) // as after Flow's restart
	if _, err := r.m.Warm(ctx, pid); err == nil || errors.Is(err, ErrNotWarm) {
		t.Fatalf("Warm with a failing fetch: %v", err)
	}
	if d := r.m.Describe(ctx, pid); d.State != "warm" {
		t.Errorf("state after a failed reconnect: %s", d.State)
	}
	if _, err := r.m.Warm(ctx, pid); err != nil {
		t.Errorf("Warm after the fetch recovered: %v", err)
	}
}

// The same through Ensure, which may start the sandbox: a reconnect to a running, warm sandbox that fails for a
// passing reason started nothing and leaves it warm.
func TestEnsure_failedReconnectKeepsTheSandboxWarm(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	fails := &atomic.Int32{}
	fails.Store(1)
	r.m.Dialer = failingRepoDialer{d: r.d, fails: fails}
	r.m.Forget(pid) // as after Flow's restart
	if _, err := r.m.Ensure(ctx, pid); err == nil {
		t.Fatal("Ensure with a failing fetch: no error")
	}
	if d := r.m.Describe(ctx, pid); d.State != "warm" {
		t.Errorf("state after a failed reconnect: %s", d.State)
	}
	if _, err := r.m.Warm(ctx, pid); err != nil {
		t.Errorf("Warm after the fetch recovered: %v", err)
	}
}

// failingState fails every State look while on is set, as Incus does when it does not answer just now.
type failingState struct {
	*sandboxfake.Runtime
	on atomic.Bool
}

func (f *failingState) State(ctx context.Context, name string) (sandbox.State, error) {
	if f.on.Load() {
		return sandbox.State{}, errors.New("incus: timeout")
	}
	return f.Runtime.State(ctx, name)
}

// A State look that fails proves nothing about a cached connection: it stays and is answered.
func TestWarm_failedStateLookKeepsTheConnection(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	api, err := r.m.Ensure(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	rt := &failingState{Runtime: r.rt}
	rt.on.Store(true)
	r.m.Runtime = rt
	if again, err := r.m.Warm(ctx, pid); err != nil || again != api {
		t.Errorf("Warm on a failed look: %v", err)
	}
	if r.m.cached(pid).api != api {
		t.Error("the connection was dropped")
	}
}

// leavingStop is a runtime whose Stop sees the caller leave (a page reload) at its start.
type leavingStop struct {
	*sandboxfake.Runtime
	leave context.CancelFunc
}

func (l leavingStop) Stop(ctx context.Context, name string, wait time.Duration) error {
	l.leave()
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.Runtime.Stop(ctx, name, wait)
}

// A stop runs to its end when its caller leaves: the sandbox is recorded stopped, not failed or stopping.
func TestStop_callerLeaves(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Ensure(t.Context(), pid); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	r.m.Runtime = leavingStop{Runtime: r.rt, leave: cancel}
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Errorf("stop: %v", err)
	}
	if d := r.m.Describe(t.Context(), pid); d.State != "stopped" {
		t.Errorf("state after the caller left: %+v", d)
	}
}

// While a bring-up holds the project, Warm answers at once (not warm, may run) and an Ensure whose caller leaves
// returns instead of waiting minutes for the lock.
func TestLock_bringUpHoldsNoWaiter(t *testing.T) {
	r := newRig(t)
	unlock := r.m.lock(pid)
	r.m.setBringing(pid, true)
	defer unlock()
	warm, ensure := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := r.m.Warm(t.Context(), pid)
		warm <- err
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	go func() {
		_, err := r.m.Ensure(ctx, pid)
		ensure <- err
	}()
	for _, w := range []struct {
		c    chan error
		want error
	}{{warm, ErrMayRun}, {ensure, context.DeadlineExceeded}} {
		select {
		case err := <-w.c:
			if !errors.Is(err, w.want) {
				t.Errorf("waiter during a bring-up: %v, want %v", err, w.want)
			}
		case <-time.After(time.Second):
			t.Fatal("a waiter stayed parked behind the bring-up")
		}
	}
}

// The same with a real bring-up held midway (cloning): Warm answers at once, and the bring-up ends warm.
func TestWarm_answersDuringABringUp(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	gate := repoGate{entered: make(chan struct{}), once: &sync.Once{}, release: make(chan struct{})}
	r.m.Dialer = gateDialer{d: r.d, gate: gate}
	up := make(chan error, 1)
	go func() { _, err := r.m.Ensure(ctx, pid); up <- err }()
	<-gate.entered
	warm := make(chan error, 1)
	go func() { _, err := r.m.Warm(ctx, pid); warm <- err }()
	select {
	case err := <-warm:
		if !errors.Is(err, ErrMayRun) {
			t.Errorf("Warm during a bring-up: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("Warm waited for the bring-up")
	}
	close(gate.release)
	if err := <-up; err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Warm(ctx, pid); err != nil {
		t.Errorf("Warm after the bring-up: %v", err)
	}
}

// A reconnect that finds the sandbox cut off from its bridge does record it failed: only a start helps it, and the
// owner is told so.
func TestWarm_cutOffSandboxIsRecordedFailed(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.rt.RemoveBridge("flowbr0")
	r.rt.AddBridge("flowbr0")
	r.m.Forget(pid)
	if _, err := r.m.Warm(ctx, pid); err == nil {
		t.Fatal("Warm on a cut-off sandbox: no error")
	}
	if d := r.m.Describe(ctx, pid); d.State != "failed" || d.ErrorCode != "sandbox_network_missing" {
		t.Errorf("cut-off sandbox after Warm: %+v", d)
	}
}

// flakyPorts fails BridgePorts on the calls listed in fail (counted from 1).
type flakyPorts struct {
	*sandboxfake.Runtime
	calls atomic.Int32
	fail  map[int32]bool
}

func (f *flakyPorts) BridgePorts(ctx context.Context, bridge string) ([]string, error) {
	if f.fail[f.calls.Add(1)] {
		return nil, errors.New("incus: timeout")
	}
	return f.Runtime.BridgePorts(ctx, bridge)
}

// A failed read of the bridge's ports never stops a healthy running sandbox, in a bring-up or on the cached path.
func TestEnsure_bridgePortsHiccupStopsNothing(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	api, err := r.m.Ensure(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	const name = "flow-b679dbb3324e"
	// Cached path: the look fails, the connection stays.
	r.m.Runtime = &flakyPorts{Runtime: r.rt, fail: map[int32]bool{1: true}}
	dials := len(r.d.endpoints)
	if again, err := r.m.Ensure(ctx, pid); err != nil || again != api || len(r.d.endpoints) != dials {
		t.Errorf("cached Ensure on a failed look: %v (dials %d → %d)", err, dials, len(r.d.endpoints))
	}
	// Bring-up: the first read (the bridge exists) succeeds, the one in misplaced fails.
	r.m.Forget(pid)
	r.m.Runtime = &flakyPorts{Runtime: r.rt, fail: map[int32]bool{2: true}}
	starts := r.rt.Starts
	if _, err := r.m.Ensure(ctx, pid); err == nil {
		t.Error("Ensure with a failed bridge look: no error")
	}
	if in, _ := r.rt.Instance(name); in.Status != sandbox.StatusRunning || r.rt.Starts != starts {
		t.Errorf("a failed look restarted the sandbox: %s, starts %d → %d", in.Status, starts, r.rt.Starts)
	}
}

// A sandbox that is not warm by the records while its instance runs is not reported as stopped: its hosts and
// shells may run. Only a stopped or missing instance is ErrNotWarm.
func TestWarm_notWarmButRunningIsNotStopped(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	sb, _ := r.m.Store.ByProject(ctx, pid)
	if err := r.m.Store.SetState(ctx, sb.ID, StateFailed, "", "sandbox_unavailable"); err != nil {
		t.Fatal(err)
	}
	r.m.Forget(pid)
	_, err := r.m.Warm(ctx, pid)
	if !errors.Is(err, ErrMayRun) || errors.Is(err, ErrNotWarm) {
		t.Errorf("Warm on a failed, running sandbox: %v", err)
	}
	if err := r.rt.Stop(ctx, sb.Instance, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Warm(ctx, pid); !errors.Is(err, ErrNotWarm) {
		t.Errorf("Warm on a failed, stopped sandbox: %v", err)
	}
}
