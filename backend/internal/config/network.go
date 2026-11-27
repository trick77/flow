package config

import (
	"net"
	"net/netip"
	"strings"

	"github.com/trick77/ship/kit/env"

	"github.com/trick77/flow/internal/sandbox"
)

// Sandbox network defaults, the layout measured in the lab (docs/architecture.md, "Sandbox network"): the Docker
// network flow on bridge flowbr0, Docker's own allocations in 172.30.0.0/24, sandboxes in the upper half. They must
// match what deploy/host-setup.sh created, which reads the same variables.
const (
	DefaultSandboxBridge    = "flowbr0"
	DefaultSandboxSubnet    = "172.30.0.0/16"
	DefaultSandboxRange     = "172.30.128.0/17"
	DefaultSandboxResolvers = "1.1.1.1,9.9.9.9"
	// DefaultSandboxMemoryTotalMiB: the owner plans 12 to 14 GiB of the host for Flow.
	DefaultSandboxMemoryTotalMiB = 14 << 10
)

// loadSandboxNetwork reads BACKEND_SANDBOX_BRIDGE, _SUBNET, _RANGE, _GATEWAY (default: the subnet's first address,
// Docker's choice) and _RESOLVERS, and checks they fit together.
func loadSandboxNetwork(r *env.Reader) sandbox.Network {
	n := sandbox.Network{Bridge: r.String("BACKEND_SANDBOX_BRIDGE", DefaultSandboxBridge)}
	n.Subnet = ipv4Prefix(r, "BACKEND_SANDBOX_SUBNET", DefaultSandboxSubnet)
	n.Range = ipv4Prefix(r, "BACKEND_SANDBOX_RANGE", DefaultSandboxRange)
	gateway := ""
	if n.Subnet.IsValid() {
		gateway = n.Subnet.Addr().Next().String()
	}
	n.Gateway = ipv4Addr(r, "BACKEND_SANDBOX_GATEWAY", gateway)
	for _, s := range r.List("BACKEND_SANDBOX_RESOLVERS", strings.Split(DefaultSandboxResolvers, ",")) {
		if a, err := netip.ParseAddr(s); err != nil || !a.Is4() {
			r.Fail("BACKEND_SANDBOX_RESOLVERS: %q is not an IPv4 address (sandboxes have no IPv6)", s)
		} else {
			n.Resolvers = append(n.Resolvers, a)
		}
	}
	if len(n.Resolvers) == 0 {
		r.Fail("BACKEND_SANDBOX_RESOLVERS: want at least one IPv4 address")
	}
	if n.Bridge == "" || len(n.Bridge) > 15 || strings.ContainsAny(n.Bridge, "/ \t:") {
		r.Fail("BACKEND_SANDBOX_BRIDGE: %q is not a Linux interface name (at most 15 characters, no '/', ':' or spaces)",
			n.Bridge)
	}
	if !n.Subnet.IsValid() || !n.Range.IsValid() {
		return n
	}
	if n.Range.Bits() <= n.Subnet.Bits() || !n.Subnet.Contains(n.Range.Addr()) {
		r.Fail("BACKEND_SANDBOX_RANGE: %s must be a smaller part of BACKEND_SANDBOX_SUBNET %s: Docker allocates its own "+
			"containers (Flow's among them) from the rest", n.Range, n.Subnet)
	}
	if n.Gateway.IsValid() {
		switch {
		case !n.Subnet.Contains(n.Gateway) || n.Gateway == n.Subnet.Addr():
			r.Fail("BACKEND_SANDBOX_GATEWAY: %s is not a host address of BACKEND_SANDBOX_SUBNET %s", n.Gateway, n.Subnet)
		case n.Range.Contains(n.Gateway):
			r.Fail("BACKEND_SANDBOX_GATEWAY: %s lies in BACKEND_SANDBOX_RANGE %s", n.Gateway, n.Range)
		}
	}
	for _, a := range n.Resolvers {
		switch {
		case a == n.Gateway:
			r.Fail("BACKEND_SANDBOX_RESOLVERS: %s is the host, which sandboxes may not reach", a)
		case n.Range.Contains(a):
			r.Fail("BACKEND_SANDBOX_RESOLVERS: %s lies in BACKEND_SANDBOX_RANGE %s, among the sandboxes", a, n.Range)
		case behindHost(a) && !n.Subnet.Contains(a):
			r.Fail("BACKEND_SANDBOX_RESOLVERS: %s is on a private, link-local or shared network, which host-setup's "+
				"policy flow-sb-lan keeps sandboxes from (a public resolver, or one on BACKEND_SANDBOX_SUBNET)", a)
		}
	}
	return n
}

// lanNets are deploy/host-lib.sh's LAN_DESTINATIONS: what sandboxes may not reach behind the host.
var lanNets = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10")}

func behindHost(a netip.Addr) bool {
	for _, p := range lanNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// loadSandboxMemoryTotal reads BACKEND_SANDBOX_MEMORY_TOTAL_MIB: at least one sandbox must fit.
func loadSandboxMemoryTotal(r *env.Reader, perSandbox int64) int64 {
	total := int64(r.Int("BACKEND_SANDBOX_MEMORY_TOTAL_MIB", DefaultSandboxMemoryTotalMiB)) << 20
	if total <= 0 || total < perSandbox {
		r.Fail("BACKEND_SANDBOX_MEMORY_TOTAL_MIB: %d MiB holds not even one sandbox of BACKEND_SANDBOX_MEMORY_MIB %d MiB",
			total>>20, perSandbox>>20)
	}
	return total
}

// checkAddrOffSandboxNetwork refuses a listen address on the sandbox subnet: sandboxes reach everything on it.
func checkAddrOffSandboxNetwork(r *env.Reader, addr string, n sandbox.Network) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return // reported with BACKEND_ADDR itself
	}
	if a, err := netip.ParseAddr(host); err == nil && n.Subnet.IsValid() && n.Subnet.Contains(a.Unmap()) {
		r.Fail("BACKEND_ADDR %q is on the sandbox network %s: sandboxes would reach the browser API", addr, n.Subnet)
	}
}

func ipv4Prefix(r *env.Reader, name, def string) netip.Prefix {
	v := r.String(name, def)
	p, err := netip.ParsePrefix(v)
	if err != nil || !p.Addr().Is4() || p != p.Masked() || p.Bits() > 30 {
		r.Fail("%s: %q is not an IPv4 network such as %s (sandboxes have no IPv6)", name, v, def)
		return netip.Prefix{}
	}
	return p
}

// ipv4Addr reads name; an empty default (no valid subnet to derive it from) stays the zero address, already
// reported.
func ipv4Addr(r *env.Reader, name, def string) netip.Addr {
	v := r.String(name, def)
	if v == "" {
		return netip.Addr{}
	}
	a, err := netip.ParseAddr(v)
	if err != nil || !a.Is4() {
		r.Fail("%s: %q is not an IPv4 address", name, v)
		return netip.Addr{}
	}
	return a
}
