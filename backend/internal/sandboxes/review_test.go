package sandboxes

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/flow/internal/sandbox"
	"github.com/trick77/flow/internal/sandbox/sandboxfake"
)

// 1. A cached connection is not handed out for a sandbox that is running but cut off from its bridge.
func TestEnsure_cacheSeesACutOff(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	first, err := r.m.Ensure(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	r.rt.RemoveBridge("flowbr0")
	r.rt.AddBridge("flowbr0") // no Forget: the cache still holds the connection
	if _, err := r.m.Warm(ctx, pid); err == nil {
		t.Error("Warm handed out the cached connection of a cut-off sandbox")
	}
	starts := r.rt.Starts
	again, err := r.m.Ensure(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if !r.rt.Reachable("flow-b679dbb3324e") || r.rt.Starts != starts+1 || again == nil || first == nil {
		t.Errorf("Ensure kept the dead connection (starts %d → %d)", starts, r.rt.Starts)
	}
}

// 2. An address an instance without a row still carries (its project deleted, the instance left running) is
// never handed to a new sandbox, with the SQL assignment too.
func TestSQLAddresses_rowlessInstanceKeepsItsAddress(t *testing.T) {
	r := newRig(t)
	addColumn(t, r)
	ctx := t.Context()
	r.m.Addresses = SQLAddresses{DB: r.db, Network: labNetwork()}
	_ = r.rt.Create(ctx, sandbox.Spec{Name: "orphan", NIC: labNetwork().NIC(a("172.30.128.1"))})
	_ = r.rt.Start(ctx, "orphan")
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if in, _ := r.rt.Instance("flow-b679dbb3324e"); in.NIC.Address.Addr() == a("172.30.128.1") {
		t.Error("the new sandbox got the address the orphaned instance carries")
	}
}

// 3. Neither adoption, a stored value nor the in-memory seed can pin a sandbox to an address that is not
// assignable.
func TestAddresses_onlyAssignable(t *testing.T) {
	r := newRig(t)
	addColumn(t, r)
	ctx := t.Context()
	s := SQLAddresses{DB: r.db, Network: labNetwork()}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	sb, _ := r.m.Store.ByProject(ctx, pid)
	for _, bad := range []string{"10.1.2.3", "172.30.255.254", "172.30.0.7"} { // outside, builder's, Docker's part
		if _, err := r.db.Exec(`UPDATE sandboxes SET ipv4 = NULL WHERE id = ?`, sb.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Adopt(ctx, sb.ID, a(bad)); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Assign(ctx, sb.ID, nil); err != nil || !labNetwork().Assignable(got) {
			t.Errorf("adopted %s: Assign = %s, %v", bad, got, err)
		}
		if _, err := r.db.Exec(`UPDATE sandboxes SET ipv4 = ? WHERE id = ?`, bad, sb.ID); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Assign(ctx, sb.ID, nil); err != nil || !labNetwork().Assignable(got) {
			t.Errorf("stored %s: Assign = %s, %v", bad, got, err)
		}
	}
	m := NewMemoryAddresses(labNetwork(), map[string]netip.Addr{"x": a("172.30.255.254"), "y": a("172.30.128.0")})
	for _, k := range []string{"x", "y"} {
		if got, _ := m.Assign(ctx, k, nil); !labNetwork().Assignable(got) {
			t.Errorf("seeded %s kept %s", k, got)
		}
	}
	if got, _ := m.Assign(ctx, "z", []netip.Addr{a("172.30.128.1")}); got == a("172.30.128.1") {
		t.Error("memory assignment handed out a busy address")
	}
}

// settling reports the instance still starting on its first look and lets it finish starting then, as an
// instance does that Incus is still bringing up when Flow comes by.
type settling struct {
	*sandboxfake.Runtime
	once sync.Once
	name string
}

func (s *settling) State(ctx context.Context, name string) (sandbox.State, error) {
	first := false
	s.once.Do(func() { first = true })
	if first && name == s.name {
		_ = s.Start(ctx, name)
		st, err := s.Runtime.State(ctx, name)
		st.Status = sandbox.StatusOther
		return st, err
	}
	return s.Runtime.State(ctx, name)
}

// 4. An instance that finishes starting during the bring-up is not taken for cut off and restarted.
func TestEnsure_settlingInstanceIsNotRestarted(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	_ = r.m.Stop(ctx, pid)
	r.m.Runtime = &settling{Runtime: r.rt, name: "flow-b679dbb3324e"}
	starts := r.rt.Starts
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if r.rt.Starts != starts+1 {
		t.Errorf("started %d times, want once (no restart of a settling instance)", r.rt.Starts-starts)
	}
}

// failingFlowd fails the first delivery.
func (r *rig) failFirstDelivery() {
	calls := 0
	ok := r.m.Flowd
	r.m.Flowd = func(arch string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("no binary yet")
		}
		return ok(arch)
	}
}

// 5. The cleared flowd version of a re-created instance outlives a failed bring-up: the next start does not wait
// for a flowd the instance never had.
func TestEnsure_recreatedVersionClearIsPersisted(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	_ = r.rt.Stop(ctx, "flow-b679dbb3324e", time.Second)
	_ = r.rt.Delete(ctx, "flow-b679dbb3324e")
	r.m.Forget(pid)
	r.m.Dialer, r.m.Wait = deliveredDialer{r.d, r.release(t)}, 3*time.Second
	r.failFirstDelivery()
	if _, err := r.m.Ensure(ctx, pid); err == nil {
		t.Fatal("first delivery did not fail")
	}
	_ = r.rt.Stop(ctx, "flow-b679dbb3324e", time.Second)
	t0 := time.Now()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Errorf("waited %s for a flowd that was never delivered", d)
	}
}

// stoppingBehind stops the instance right after the first look at it, as a stop behind Flow's back between
// Warm's check and the bring-up.
type stoppingBehind struct {
	*sandboxfake.Runtime
	calls int
}

func (s *stoppingBehind) State(ctx context.Context, name string) (sandbox.State, error) {
	s.calls++
	st, err := s.Runtime.State(ctx, name)
	if s.calls == 1 {
		_ = s.Stop(ctx, name, time.Second)
	}
	return st, err
}

// 6. Warm never starts a sandbox, not even one that stopped after Warm looked.
func TestWarm_neverStartsInBring(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.m.Forget(pid)
	r.m.Runtime = &stoppingBehind{Runtime: r.rt}
	starts := r.rt.Starts
	if _, err := r.m.Warm(ctx, pid); !errors.Is(err, ErrNotWarm) {
		t.Errorf("Warm: %v", err)
	}
	if r.rt.Starts != starts {
		t.Error("Warm started the sandbox")
	}
	if d := r.m.Describe(ctx, pid); d.State != "stopped" {
		t.Errorf("state %s", d.State)
	}
}

// 7. A sandbox on another address than its assigned one is not reported as a missing bridge.
func TestWarm_addressMismatchIsNotABridgeError(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	sb, _ := r.m.Store.ByProject(ctx, pid)
	r.m.Addresses = NewMemoryAddresses(labNetwork(), map[string]netip.Addr{sb.ID: a("172.30.128.9")})
	r.m.Forget(pid)
	_, err := r.m.Warm(ctx, pid)
	if err == nil || errors.Is(err, sandbox.ErrNetworkMissing) || strings.Contains(err.Error(), "bridge missing") ||
		!strings.Contains(err.Error(), "172.30.128.9") {
		t.Errorf("Warm on a mismatched address: %v", err)
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if in, _ := r.rt.Instance("flow-b679dbb3324e"); in.NIC.Address.Addr() != a("172.30.128.9") {
		t.Errorf("Ensure left it on %s", in.NIC.Address)
	}
}
