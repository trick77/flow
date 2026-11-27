package sandboxes

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandbox"
	"github.com/trick77/flow/internal/sandbox/sandboxfake"
	"github.com/trick77/flow/internal/store"
)

const pid2 = "01a21998-5a4f-7828-81be-00000000beef"

func (r *rig) secondProject(t *testing.T) {
	t.Helper()
	if err := r.ps.Create(t.Context(), projects.Project{ID: pid2, Name: "other"},
		[]projects.Repo{{ID: "r2", FullName: "trick77/ship", DefaultBranch: "master"}}); err != nil {
		t.Fatal(err)
	}
}

func a(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestMemoryAddresses(t *testing.T) {
	ctx := t.Context()
	m := NewMemoryAddresses(labNetwork(), map[string]netip.Addr{"held": a("172.30.128.1"), "dup": a("172.30.128.1"),
		"outside": a("10.0.0.1")})
	x, _ := m.Assign(ctx, "x", nil)
	y, _ := m.Assign(ctx, "y", nil)
	if x != a("172.30.128.2") || y != a("172.30.128.3") {
		t.Errorf("x %s y %s: want the lowest free after the held one", x, y)
	}
	if again, _ := m.Assign(ctx, "x", nil); again != x {
		t.Errorf("x moved to %s", again)
	}
	_ = m.Release(ctx, "x")
	_ = m.Release(ctx, "never")
	if z, _ := m.Assign(ctx, "z", nil); z != x {
		t.Errorf("released %s not reused, got %s", x, z)
	}
	if dup, _ := m.Assign(ctx, "dup", nil); dup != a("172.30.128.1") {
		t.Errorf("the lower key does not keep the seeded address: %s", dup)
	}
	n := labNetwork()
	n.Range = netip.MustParsePrefix("172.30.1.0/30") // .1 and .2 assignable, .3 the builder's
	tiny := NewMemoryAddresses(n, nil)
	_, _ = tiny.Assign(ctx, "1", nil)
	_, _ = tiny.Assign(ctx, "2", nil)
	if _, err := tiny.Assign(ctx, "3", nil); !errors.Is(err, ErrNoAddress) {
		t.Errorf("full range: %v", err)
	}
}

// addColumn adds the column SQLAddresses is written against, as the coordinator's migration will.
func addColumn(t *testing.T, r *rig) {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM pragma_table_info('sandboxes') WHERE name = 'ipv4'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		if _, err := r.db.Exec(`ALTER TABLE sandboxes ADD COLUMN ipv4 TEXT`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS sandboxes_ipv4 ON sandboxes (ipv4)`); err != nil {
		t.Fatal(err)
	}
}

func TestSQLAddresses(t *testing.T) {
	r := newRig(t)
	addColumn(t, r)
	ctx := t.Context()
	s := SQLAddresses{DB: r.db, Network: labNetwork()}
	if _, err := s.Assign(ctx, "nope", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown sandbox: %v", err)
	}
	ids := make([]string, 6)
	for i := range ids {
		project := store.NewID()
		if err := r.ps.Create(ctx, projects.Project{ID: project, Name: "p" + project},
			[]projects.Repo{{ID: "repo" + project, FullName: "trick77/r" + project[len(project)-6:], DefaultBranch: "master"}}); err != nil {
			t.Fatal(err)
		}
		ids[i] = store.NewID()
		if _, err := r.db.Exec(`INSERT INTO sandboxes (id, project_id, instance, state) VALUES (?, ?, ?, 'setup')`,
			ids[i], project, "i"+ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	got := make([]netip.Addr, len(ids))
	for i, id := range ids {
		wg.Go(func() {
			got[i], _ = s.Assign(ctx, id, nil)
		})
	}
	wg.Wait()
	seen := map[netip.Addr]bool{}
	for i, ad := range got {
		if !labNetwork().Assignable(ad) || seen[ad] {
			t.Fatalf("assignment %d = %s (all %v)", i, ad, got)
		}
		seen[ad] = true
	}
	if again, _ := s.Assign(ctx, ids[0], nil); again != got[0] {
		t.Errorf("address moved: %s → %s", got[0], again)
	}
	if err := s.Release(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`DELETE FROM sandboxes WHERE id = ?`, ids[1]); err != nil {
		t.Fatal(err)
	}
	fresh := store.NewID()
	if _, err := r.db.Exec(`INSERT INTO sandboxes (id, project_id, instance, state) VALUES (?, ?, 'ifresh', 'setup')`,
		fresh, pid); err != nil {
		t.Fatal(err)
	}
	low := min(got[0].As4()[3], got[1].As4()[3])
	if f, _ := s.Assign(ctx, fresh, nil); f.As4()[3] != low {
		t.Errorf("freed addresses not reused: got %s, lowest free .%d", f, low)
	}
	if _, err := r.db.Exec(`UPDATE sandboxes SET ipv4 = ? WHERE id = ?`, got[2].String(), ids[3]); err == nil {
		t.Error("the unique index let two sandboxes hold one address")
	}
}

// Sequential use against one sandbox per project, the shape the schema has today.
func TestSQLAddresses_oneProject(t *testing.T) {
	r := newRig(t)
	addColumn(t, r)
	ctx := t.Context()
	s := SQLAddresses{DB: r.db, Network: labNetwork()}
	r.m.Addresses = s
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	sb, _ := r.m.Store.ByProject(ctx, pid)
	if got, _ := s.Assign(ctx, sb.ID, nil); got != a("172.30.128.1") {
		t.Errorf("row holds %s", got)
	}
	if err := r.m.Store.Put(ctx, sb); err != nil { // Put must not clear the column
		t.Fatal(err)
	}
	if got, _ := s.Assign(ctx, sb.ID, nil); got != a("172.30.128.1") {
		t.Errorf("after Put the row holds %s", got)
	}
	if again, _ := r.m.Store.ByProject(ctx, pid); again.IPv4 != "172.30.128.1" {
		t.Errorf("row IPv4 = %q", again.IPv4)
	}
	sb.IPv4 = "172.30.128.77" // Put must not write it either
	_ = r.m.Store.Put(ctx, sb)
	if again, _ := r.m.Store.ByProject(ctx, pid); again.IPv4 != "172.30.128.1" {
		t.Errorf("Put wrote IPv4: %q", again.IPv4)
	}
}

// Switching from the in-memory assignment to the column moves no sandbox: the rows adopt what the NICs carry
// before the first Assign, so B (made second, on .2) does not get .1 while A still runs there.
func TestSQLAddresses_switchOver(t *testing.T) {
	r := newRig(t)
	r.secondProject(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Ensure(ctx, pid2); err != nil {
		t.Fatal(err)
	}
	addColumn(t, r)
	r.m.Addresses, r.m.addrReady = SQLAddresses{DB: r.db, Network: labNetwork()}, false // the coordinator wired it
	r.m.Forget(pid2)
	starts := r.rt.Starts
	if _, err := r.m.Ensure(ctx, pid2); err != nil {
		t.Fatal(err)
	}
	a1, _ := r.rt.Instance("flow-b679dbb3324e")
	b, _ := r.rt.Instance(InstanceName(pid2))
	if a1.NIC.Address.Addr() != a("172.30.128.1") || b.NIC.Address.Addr() != a("172.30.128.2") || r.rt.Starts != starts {
		t.Errorf("A %s B %s, starts %d → %d", a1.NIC.Address, b.NIC.Address, starts, r.rt.Starts)
	}
	sb, _ := r.m.Store.ByProject(ctx, pid)
	if got, _ := r.m.Addresses.Assign(ctx, sb.ID, nil); got != a("172.30.128.1") {
		t.Errorf("A's row adopted %s", got)
	}
	// An address another row already holds stays there.
	s := r.m.Addresses.(SQLAddresses)
	if err := s.Adopt(ctx, "nope", a("172.30.128.1")); err != nil {
		t.Fatal(err)
	}
}

// After a Flow restart the default assignment comes from the instances' NICs: every sandbox keeps its address
// and a new one never gets an address an instance still carries, even one that is not Flow's.
func TestAddresses_seededFromInstances(t *testing.T) {
	r := newRig(t)
	r.secondProject(t)
	ctx := t.Context()
	n := labNetwork()
	_ = r.rt.Create(ctx, sandbox.Spec{Name: "stray", NIC: n.NIC(a("172.30.128.2"))})
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	first, _ := r.rt.Instance("flow-b679dbb3324e")
	r.m.Addresses, r.m.addrReady = nil, false // a restarted Flow
	r.m.Forget(pid)
	if _, err := r.m.Ensure(ctx, pid2); err != nil {
		t.Fatal(err)
	}
	second, _ := r.rt.Instance(InstanceName(pid2))
	if first.NIC.Address.Addr() != a("172.30.128.1") || second.NIC.Address.Addr() != a("172.30.128.3") {
		t.Errorf("first %s second %s", first.NIC.Address, second.NIC.Address)
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if in, _ := r.rt.Instance("flow-b679dbb3324e"); in.NIC.Address != first.NIC.Address || r.rt.Starts != 2 {
		t.Errorf("first sandbox moved to %s (starts %d)", in.NIC.Address, r.rt.Starts)
	}
}

// A bridge missing at bring-up fails it at once with a recorded code: nothing created, started or delivered, no
// retry. Once the bridge is back the next bring-up works.
func TestEnsure_bridgeMissing(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	r.rt.RemoveBridge("flowbr0")
	_, err := r.m.Ensure(ctx, pid)
	if err == nil || !strings.Contains(err.Error(), "host bridge missing") {
		t.Fatalf("Ensure: %v", err)
	}
	if _, ok := r.rt.Instance("flow-b679dbb3324e"); ok || r.rt.Starts != 0 || len(r.rt.Execs) != 0 {
		t.Error("created, started or delivered without a bridge")
	}
	if d := r.m.Describe(ctx, pid); d.State != "failed" || d.ErrorCode != "sandbox_network_missing" {
		t.Errorf("Describe = %+v", d)
	}
	r.rt.AddBridge("flowbr0")
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatalf("after the bridge came back: %v", err)
	}
	// Gone under a running sandbox: reported, the sandbox left alone.
	r.rt.RemoveBridge("flowbr0")
	r.m.Forget(pid)
	starts := r.rt.Starts
	if _, err := r.m.Ensure(ctx, pid); err == nil || r.rt.Starts != starts {
		t.Errorf("Ensure with the bridge gone: %v, starts %d → %d", err, starts, r.rt.Starts)
	}
	// Start refused by the runtime itself (the bridge vanished between the check and the start).
	r.rt.AddBridge("flowbr0")
	_ = r.m.Stop(ctx, pid)
	r.m.Runtime = vanishing{r.rt}
	if _, err := r.m.Ensure(ctx, pid); err == nil || !strings.Contains(err.Error(), "Parent device") {
		t.Errorf("start without bridge: %v", err)
	}
}

// vanishing deletes the bridge right before each start.
type vanishing struct{ *sandboxfake.Runtime }

func (v vanishing) Start(ctx context.Context, name string) error {
	v.RemoveBridge("flowbr0")
	return v.Runtime.Start(ctx, name)
}

// The bridge made again under running sandboxes leaves them cut off until restarted (Incus never re-attaches):
// Warm reports it without touching the sandbox, Ensure restarts it onto the bridge.
func TestEnsure_bridgeRecreated(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.rt.RemoveBridge("flowbr0")
	r.rt.AddBridge("flowbr0")
	r.m.Forget(pid)
	starts := r.rt.Starts
	if _, err := r.m.Warm(ctx, pid); err == nil || !strings.Contains(err.Error(), "cut off") || r.rt.Starts != starts {
		t.Errorf("Warm on a cut-off sandbox: %v (starts %d → %d)", err, starts, r.rt.Starts)
	}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if !r.rt.Reachable("flow-b679dbb3324e") || r.rt.Starts != starts+1 {
		t.Errorf("not restarted onto the bridge (starts %d → %d)", starts, r.rt.Starts)
	}
	if d := r.m.Describe(ctx, pid); d.State != "warm" {
		t.Errorf("state %s", d.State)
	}
}

// deliveredFlowd answers only once this build's flowd was pushed into the instance.
type deliveredFlowd struct {
	reachable
	version string
}

func (d deliveredFlowd) Hello(ctx context.Context) (flowdproto.Hello, error) {
	if in, _ := d.rt.Instance(d.name); in.Files[layout.Release(d.version)+"/flowd.new"].Path == "" {
		return flowdproto.Hello{}, flowdproto.ErrUnavailable
	}
	return d.reachable.Hello(ctx)
}

type deliveredDialer struct {
	d   *dialer
	rel string
}

func (dd deliveredDialer) Dial(e flowdproto.Endpoint) flowdproto.API {
	return deliveredFlowd{reachable: dd.d.Dial(e).(reachable), version: dd.rel}
}

// An instance deleted behind Flow's back (the host's teardown) is made again; its row still names the flowd of
// the old one, but the new instance has none: Flow delivers at once instead of waiting Wait for it.
func TestEnsure_recreatedInstanceGetsFlowdAtOnce(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	_ = r.rt.Stop(ctx, "flow-b679dbb3324e", time.Second)
	_ = r.rt.Delete(ctx, "flow-b679dbb3324e")
	r.m.Forget(pid)
	r.m.Dialer, r.m.Wait = deliveredDialer{r.d, r.release(t)}, 5*time.Second
	t0 := time.Now()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Errorf("bring-up waited %s for a flowd the new instance never had", d)
	}
}

// Admission counts what running sandboxes use, not their limits: three sandboxes of a 6 GiB limit run under a
// 14 GiB total while they are idle.
func TestEnsure_memoryMeasuredNotLimits(t *testing.T) {
	r := newRig(t)
	r.secondProject(t)
	const pid3 = "01a21998-5a4f-7828-81be-00000000cafe"
	if err := r.ps.Create(t.Context(), projects.Project{ID: pid3, Name: "third"},
		[]projects.Repo{{ID: "r3", FullName: "trick77/loom", DefaultBranch: "master"}}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	r.m.Memory, r.m.MemoryTotal, r.m.MemoryHeadroom = 6<<30, 14<<30, 6<<30
	for _, p := range []string{pid, pid2, pid3} {
		if _, err := r.m.Ensure(ctx, p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if in, _ := r.rt.Instance(InstanceName(pid3)); in.Status != sandbox.StatusRunning || in.Limits.MemoryBytes != 6<<30 {
		t.Errorf("third sandbox %s, limits %+v: the per-sandbox limit stays", in.Status, in.Limits)
	}
}

// A start is refused while the measured use plus the headroom passes the total; a refused new sandbox is refused
// before its instance is made. Stopping a sandbox, or its use dropping, makes room.
func TestEnsure_memoryFull(t *testing.T) {
	r := newRig(t)
	r.secondProject(t)
	ctx := t.Context()
	r.m.MemoryTotal, r.m.MemoryHeadroom = 4<<30, 1<<30
	r.rt.InUse = map[string]int64{InstanceName(pid): 3<<30 + 1}
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	_, err := r.m.Ensure(ctx, pid2)
	if err == nil || !strings.Contains(err.Error(), "memory ceiling") {
		t.Fatalf("second sandbox past the total: %v", err)
	}
	if d := r.m.Describe(ctx, pid2); d.State != "failed" || d.ErrorCode != "sandbox_memory_full" {
		t.Errorf("Describe past the total = %+v", d)
	}
	if _, ok := r.rt.Instance(InstanceName(pid2)); ok {
		t.Error("the refused sandbox's instance was made")
	}
	r.rt.InUse[InstanceName(pid)] = 3 << 30 // exactly fits: 3 + 1 of 4 GiB
	if _, err := r.m.Ensure(ctx, pid2); err != nil {
		t.Fatalf("use dropped: %v", err)
	}
	// An existing instance is refused at its start: stopped, it stays stopped.
	if err := r.m.Stop(ctx, pid2); err != nil {
		t.Fatal(err)
	}
	r.rt.InUse[InstanceName(pid)] = 4 << 30
	if _, err := r.m.Ensure(ctx, pid2); err == nil {
		t.Fatal("stopped sandbox started past the total")
	}
	if in, _ := r.rt.Instance(InstanceName(pid2)); in.Status != sandbox.StatusStopped {
		t.Errorf("refused start left %s", in.Status)
	}
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Ensure(ctx, pid2); err != nil {
		t.Errorf("after the first stopped: %v", err)
	}
	r.m.MemoryTotal = 0
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Errorf("no check: %v", err)
	}
}

// A failed measurement neither admits everything nor blocks: an unmeasured running instance counts with its
// usage page cache included (the fake reports 114 MiB).
func TestEnsure_memoryUnmeasured(t *testing.T) {
	r := newRig(t)
	r.secondProject(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.rt.InUseErr = errors.New("metrics: 403")
	r.m.MemoryHeadroom = 1 << 30
	r.m.MemoryTotal = 1<<30 + 100<<20 // the 70 MiB working set would fit, the 114 MiB usage does not
	if _, err := r.m.Ensure(ctx, pid2); err == nil || !strings.Contains(err.Error(), "memory ceiling") {
		t.Fatalf("unmeasured use not counted: %v", err)
	}
	r.m.MemoryTotal = 1<<30 + 120<<20
	if _, err := r.m.Ensure(ctx, pid2); err != nil {
		t.Errorf("unmeasured use blocks for good: %v", err)
	}
}

func TestEnsure_networkNotConfigured(t *testing.T) {
	r := newRig(t)
	r.m.Network = sandbox.Network{}
	if _, err := r.m.Ensure(t.Context(), pid); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("Ensure: %v", err)
	}
}

// A Flow whose only network is the sandbox network would refuse every proxied request: the guard says so at
// start.
func TestOffSandboxNetwork_warnsWhenItWouldRefuseEverything(t *testing.T) {
	subnet := netip.MustParsePrefix("172.30.0.0/16")
	cidr := func(s string) net.Addr {
		_, n, _ := net.ParseCIDR(s)
		n.IP = net.ParseIP(strings.Split(s, "/")[0])
		return n
	}
	for name, c := range map[string]struct {
		addrs []net.Addr
		warn  bool
	}{
		"only the sandbox network": {[]net.Addr{cidr("127.0.0.1/8"), cidr("172.30.0.2/16"), cidr("::1/128")}, true},
		"also the proxy network":   {[]net.Addr{cidr("127.0.0.1/8"), cidr("172.30.0.2/16"), cidr("10.9.0.2/24")}, false},
		"not on it at all":         {[]net.Addr{cidr("10.9.0.2/24")}, false},
	} {
		var logged strings.Builder
		log := slog.New(slog.NewTextHandler(&logged, nil))
		_ = offSandboxNetwork(http.NotFoundHandler(), subnet, func() ([]net.Addr, error) { return c.addrs, nil }, log)
		if got := strings.Contains(logged.String(), "every request"); got != c.warn {
			t.Errorf("%s: warned %v: %s", name, got, logged.String())
		}
	}
}

func TestOffSandboxNetwork(t *testing.T) {
	h := OffSandboxNetwork(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }),
		netip.MustParsePrefix("172.30.0.0/16"))
	for name, c := range map[string]struct {
		remote, local string
		want          int
	}{
		"from a sandbox":       {"172.30.128.5:4000", "10.9.0.2:8080", 403},
		"onto the flow leg":    {"10.9.0.7:4000", "172.30.0.2:8080", 403},
		"mapped v4 on the leg": {"[::ffff:10.9.0.7]:4000", "[::ffff:172.30.0.2]:8080", 403},
		"through the proxy":    {"10.9.0.7:4000", "10.9.0.2:8080", 204},
		"loopback":             {"127.0.0.1:4000", "127.0.0.1:8080", 204},
	} {
		req := httptest.NewRequest("GET", "/api/projects", nil)
		req.RemoteAddr = c.remote
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey,
			net.TCPAddrFromAddrPort(netip.MustParseAddrPort(c.local))))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", name, w.Code, c.want, w.Body)
		}
	}
}
