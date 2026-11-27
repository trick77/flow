//go:build incus

package incus

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/sandbox"
)

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// TestReal runs against a real Incus with the restricted certificate on a host prepared by deploy/host-setup.sh,
// configured like Flow: BACKEND_INCUS_URL, BACKEND_INCUS_PROJECT, BACKEND_INCUS_CERT, BACKEND_INCUS_KEY,
// BACKEND_INCUS_SERVER_CERT, BACKEND_SANDBOX_IMAGE (default flow-base), BACKEND_SANDBOX_BRIDGE (flowbr0), plus two
// free addresses of the sandbox range, BACKEND_ITEST_ADDRESSES (default 172.30.255.252,172.30.255.251). Build with
// -tags incus.
func TestReal(t *testing.T) {
	c, err := New(Config{URL: os.Getenv("BACKEND_INCUS_URL"), Project: os.Getenv("BACKEND_INCUS_PROJECT"),
		CertFile: os.Getenv("BACKEND_INCUS_CERT"), KeyFile: os.Getenv("BACKEND_INCUS_KEY"),
		ServerCertFile: os.Getenv("BACKEND_INCUS_SERVER_CERT")})
	if err != nil {
		t.Fatal(err)
	}
	image, bridge := envOr("BACKEND_SANDBOX_IMAGE", "flow-base"), envOr("BACKEND_SANDBOX_BRIDGE", "flowbr0")
	addrs := strings.Split(envOr("BACKEND_ITEST_ADDRESSES", "172.30.255.252,172.30.255.251"), ",")
	nic := func(a string) sandbox.NIC {
		return sandbox.NIC{Bridge: bridge, Address: netip.PrefixFrom(netip.MustParseAddr(a), 16),
			Gateway: netip.MustParseAddr("172.30.0.1"), Resolvers: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	name, clone := "flow-itest", "flow-itest-copy"
	for _, n := range []string{name, clone} {
		_ = c.Stop(ctx, n, 5*time.Second)
		_ = c.Delete(ctx, n)
	}
	defer func() {
		for _, n := range []string{name, clone} {
			_ = c.Stop(context.Background(), n, 5*time.Second)
			_ = c.Delete(context.Background(), n)
		}
	}()
	if _, err := c.BridgePorts(ctx, "nosuchbr0"); !errors.Is(err, sandbox.ErrNetworkMissing) {
		t.Errorf("BridgePorts of a missing bridge: %v", err)
	}
	t0 := time.Now()
	if err := c.Create(ctx, sandbox.Spec{Name: name, Image: image, NIC: nic(addrs[0]), Nesting: true,
		Limits: sandbox.Limits{MemoryBytes: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	t.Logf("create %s", time.Since(t0))
	up := func(want string) sandbox.State {
		t.Helper()
		t0 := time.Now()
		if err := c.Start(ctx, name); err != nil {
			t.Fatal(err)
		}
		var st sandbox.State
		for st.IPv4 != want && ctx.Err() == nil {
			st, _ = c.State(ctx, name)
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("address %s after %s, host side %s", st.IPv4, time.Since(t0), st.HostIface)
		ports, err := c.BridgePorts(ctx, bridge)
		if err != nil || !slices.Contains(ports, st.HostIface) || st.Address.String() != want {
			t.Errorf("NIC not on %s: ports %v (%v), state %+v", bridge, ports, err, st)
		}
		return st
	}
	up(addrs[0])
	payload := strings.Repeat("flow", 1<<18) // 1 MiB
	if err := c.Push(ctx, name, sandbox.File{Path: "/opt/flow/releases/itest/blob", Content: []byte(payload), Mode: 0o750}); err != nil {
		t.Fatal(err)
	}
	res, err := c.Exec(ctx, name, sandbox.Command{Args: []string{"sh", "-c",
		"stat -c '%s %a %u' /opt/flow/releases/itest/blob; echo oops >&2; exit 7"}, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "1048576 750 0" || res.ExitCode != 7 || strings.TrimSpace(string(res.Stderr)) != "oops" {
		t.Errorf("exec = %q, exit %d, stderr %q", got, res.ExitCode, res.Stderr)
	}
	// The guest has its address, no IPv6 at all, and resolves names.
	res, err = c.Exec(ctx, name, sandbox.Command{Args: []string{"sh", "-c",
		"ip -4 -o addr show dev eth0 | awk '{print $4}'; echo v6=$(ip -6 -o addr show dev eth0 | wc -l); " +
			"for i in $(seq 1 50); do getent hosts deb.debian.org >/dev/null && echo dns=ok && exit 0; sleep 0.2; done; echo dns=fail"},
		Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); !strings.Contains(got, addrs[0]+"/16\n") || !strings.Contains(got, "v6=0\n") ||
		!strings.Contains(got, "dns=ok") {
		t.Errorf("guest network: %q %q", got, res.Stderr)
	}
	if err := c.SetNIC(ctx, name, nic(addrs[1])); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("SetNIC while running: %v", err)
	}
	if err := c.Stop(ctx, name, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.SetNIC(ctx, name, nic(addrs[1])); err != nil {
		t.Fatal(err)
	}
	up(addrs[1])
	if err := c.Stop(ctx, name, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.Clone(ctx, name, clone); err != nil {
		t.Fatal(err)
	}
	if st, err := c.State(ctx, clone); err != nil || st.Address.IsValid() {
		t.Errorf("clone kept the address: %+v, %v", st, err)
	}
	if err := c.SetNIC(ctx, clone, nic(addrs[0])); err != nil {
		t.Fatal(err)
	}
	if st, err := c.State(ctx, clone); err != nil || st.Address.String() != addrs[0] {
		t.Errorf("clone after SetNIC: %+v, %v", st, err)
	}
}
