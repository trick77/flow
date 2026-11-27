package sandboxes

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

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
