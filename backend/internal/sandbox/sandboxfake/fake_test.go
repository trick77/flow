package sandboxfake

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/sandbox"
)

func nic(addr string) sandbox.NIC {
	return sandbox.NIC{Bridge: "flowbr0", Address: netip.PrefixFrom(netip.MustParseAddr(addr), 16),
		Gateway: netip.MustParseAddr("172.30.0.1"), Resolvers: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
}

func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	r := New()
	if err := r.Create(ctx, sandbox.Spec{Name: "base", Image: "flow-base", Limits: sandbox.Limits{CPUs: 2},
		NIC: nic("172.30.128.1")}); err != nil {
		t.Fatal(err)
	}
	if in, _ := r.Instance("base"); len(in.Guest) != 1 || in.Guest[0].Path != layout.NetworkConfig {
		t.Errorf("guest network config %+v", in.Guest)
	}
	if err := r.Create(ctx, sandbox.Spec{Name: "base"}); !errors.Is(err, sandbox.ErrExists) {
		t.Errorf("second create: %v", err)
	}
	if err := r.Push(ctx, "base", sandbox.File{Path: "/x"}); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("push into a stopped instance: %v", err)
	}
	if err := r.EnsureVolume(ctx, sandbox.Volume{Name: "home"}); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureVolume(ctx, sandbox.Volume{Name: "home", Shifted: true}); err != nil {
		t.Fatal(err)
	}
	m := sandbox.Mount{Volume: "home", Path: "/srv/flow-home"}
	for range 2 {
		if err := r.Attach(ctx, "base", m); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Attach(ctx, "base", sandbox.Mount{Volume: "nope"}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("attach a missing volume: %v", err)
	}
	if err := r.Clone(ctx, "base", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Clone(ctx, "base", "p1"); !errors.Is(err, sandbox.ErrExists) {
		t.Errorf("clone onto an existing name: %v", err)
	}
	if err := r.Clone(ctx, "nope", "p2"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("clone of a missing instance: %v", err)
	}
	if in, _ := r.Instance("p1"); in.NIC.Bridge != "" || in.NIC.Address.IsValid() {
		t.Errorf("clone NIC %+v: want none", in.NIC)
	}
	_ = r.Create(ctx, sandbox.Spec{Name: "unaddressed", NIC: sandbox.NIC{Bridge: "flowbr0"}})
	if err := r.Start(ctx, "unaddressed"); err == nil {
		t.Error("a NIC without address started")
	}
	if err := r.SetNIC(ctx, "p1", nic("172.30.128.2")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.Start(ctx, "p1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetLimits(ctx, "p1", sandbox.Limits{CPUs: 6, MemoryBytes: 4 << 30}); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ctx, "p1", sandbox.File{Path: "/opt/flow/bin/flowd", Content: []byte("elf"), Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	in, ok := r.Instance("p1")
	if !ok || len(in.Mounts) != 1 || in.Limits.CPUs != 6 || in.Limits.MemoryBytes != 4<<30 || string(in.Files["/opt/flow/bin/flowd"].Content) != "elf" {
		t.Errorf("instance = %+v", in)
	}
	st, err := r.State(ctx, "p1")
	if err != nil || st.Status != sandbox.StatusRunning || st.IPv4 != "172.30.128.2" || st.Arch != "amd64" ||
		st.MemoryBytes == 0 || st.Address.String() != "172.30.128.2" || st.HostIface == "" {
		t.Errorf("state = %+v, %v", st, err)
	}
	if err := r.SetNIC(ctx, "p1", nic("172.30.128.3")); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("SetNIC on a running instance: %v", err)
	}
	if err := r.Delete(ctx, "p1"); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("delete running: %v", err)
	}
	if err := r.Stop(ctx, "p1", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	_ = r.Delete(ctx, "unaddressed")
	list, _ := r.List(ctx)
	if len(list) != 1 || list[0].Name != "base" || list[0].IPv4 != "" {
		t.Errorf("list = %+v", list)
	}
	if _, ok := r.Instance("p1"); ok {
		t.Error("deleted instance still there")
	}
}

func TestExec(t *testing.T) {
	ctx := context.Background()
	r := New()
	_ = r.Create(ctx, sandbox.Spec{Name: "a"})
	if _, err := r.Exec(ctx, "a", sandbox.Command{Args: []string{"true"}}); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("exec in a stopped instance: %v", err)
	}
	_ = r.Start(ctx, "a")
	if res, err := r.Exec(ctx, "a", sandbox.Command{Args: []string{"true"}}); err != nil || res.ExitCode != 0 {
		t.Errorf("default exec = %+v, %v", res, err)
	}
	r.ExecFunc = func(_ string, c sandbox.Command) (sandbox.Result, error) {
		return sandbox.Result{ExitCode: 3, Stdout: []byte(c.Args[0])}, nil
	}
	res, err := r.Exec(ctx, "a", sandbox.Command{Args: []string{"systemctl"}})
	if err != nil || res.ExitCode != 3 || string(res.Stdout) != "systemctl" || len(r.Execs) != 2 {
		t.Errorf("exec = %+v, %v, %d recorded", res, err, len(r.Execs))
	}
}

func TestMissing(t *testing.T) {
	ctx := context.Background()
	r := New()
	for name, err := range map[string]error{
		"start":  r.Start(ctx, "x"),
		"stop":   r.Stop(ctx, "x", 0),
		"delete": r.Delete(ctx, "x"),
		"limits": r.SetLimits(ctx, "x", sandbox.Limits{}),
		"attach": r.Attach(ctx, "x", sandbox.Mount{}),
		"push":   r.Push(ctx, "x"),
	} {
		if !errors.Is(err, sandbox.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.State(ctx, "x"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("state: %v", err)
	}
	if err := r.SetNIC(ctx, "x", sandbox.NIC{}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("SetNIC: %v", err)
	}
}

// The bridge's lifecycle as Docker and Incus play it: removing it cuts running instances off and stops new
// starts; making it again lets new starts through but leaves the running ones cut off until restarted.
func TestBridge(t *testing.T) {
	ctx := context.Background()
	r := New()
	_ = r.Create(ctx, sandbox.Spec{Name: "a", Limits: sandbox.Limits{MemoryBytes: 1 << 30}, NIC: nic("172.30.128.1")})
	_ = r.Create(ctx, sandbox.Spec{Name: "b", NIC: nic("172.30.128.2")})
	_ = r.Start(ctx, "a")
	if ports, err := r.BridgePorts(ctx, "flowbr0"); err != nil || len(ports) != 1 || !r.Reachable("a") {
		t.Fatalf("ports %v, %v", ports, err)
	}
	r.RemoveBridge("flowbr0")
	if _, err := r.BridgePorts(ctx, "flowbr0"); !errors.Is(err, sandbox.ErrNetworkMissing) {
		t.Errorf("ports of a missing bridge: %v", err)
	}
	if err := r.Start(ctx, "b"); !errors.Is(err, sandbox.ErrNetworkMissing) {
		t.Errorf("start without bridge: %v", err)
	}
	r.AddBridge("flowbr0")
	if ports, _ := r.BridgePorts(ctx, "flowbr0"); len(ports) != 0 || r.Reachable("a") {
		t.Errorf("a re-attached by itself: ports %v", ports)
	}
	if st, _ := r.State(ctx, "a"); st.Status != sandbox.StatusRunning {
		t.Errorf("a stopped with its bridge: %s", st.Status)
	}
	_ = r.Stop(ctx, "a", time.Second)
	_ = r.Start(ctx, "a")
	if !r.Reachable("a") || r.Starts != 2 {
		t.Errorf("restart did not re-attach (starts %d)", r.Starts)
	}
	if r.Reachable("nope") {
		t.Error("a missing instance is reachable")
	}
}
