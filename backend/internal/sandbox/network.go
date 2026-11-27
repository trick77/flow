package sandbox

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/trick77/flow/internal/layout"
)

// Network is how every sandbox is networked (docs/architecture.md, "Sandbox network"): one NIC bridged on a host
// bridge that Docker created and owns, so Docker provides forwarding, masquerading, the firewalld zone and the
// separation from other Docker networks. Each NIC has a static IPv4 address from Range that Flow assigns; Docker
// allocates its own containers, Flow's among them, from the rest of Subnet. No IPv6.
type Network struct {
	// Bridge is the host bridge (Docker's com.docker.network.bridge.name), flowbr0.
	Bridge string
	// Subnet is the Docker network's subnet; Gateway its gateway, the host's address on the bridge.
	Subnet  netip.Prefix
	Gateway netip.Addr
	// Range is the sandboxes' part of Subnet, outside Docker's --ip-range. The host's firewalld policies match it.
	Range netip.Prefix
	// Resolvers are the DNS servers sandboxes use; never the host, which sandboxes cannot reach.
	Resolvers []netip.Addr
}

// Configured reports whether the network has what assigning addresses needs.
func (n Network) Configured() bool {
	return n.Bridge != "" && n.Subnet.IsValid() && n.Range.IsValid() && n.Gateway.IsValid() && len(n.Resolvers) > 0
}

// NIC is the NIC of a sandbox with address a.
func (n Network) NIC(a netip.Addr) NIC {
	return NIC{Bridge: n.Bridge, Address: netip.PrefixFrom(a, n.Subnet.Bits()), Gateway: n.Gateway,
		Resolvers: slices.Clone(n.Resolvers)}
}

// Assignable reports whether a may be a sandbox's address: inside Range and none of the subnet's network and
// broadcast addresses, the gateway, a resolver, BuilderAddress, or an address ending in .0 or .255 (legal inside a
// /16, but read as network or broadcast addresses by people and some software).
func (n Network) Assignable(a netip.Addr) bool {
	if !a.Is4() || !n.Range.Contains(a) || !n.Subnet.Contains(a) {
		return false
	}
	if last := a.As4()[3]; last == 0 || last == 255 {
		return false
	}
	return a != n.Subnet.Masked().Addr() && a != lastAddr(n.Subnet) && a != n.Gateway &&
		!slices.Contains(n.Resolvers, a) && a != n.BuilderAddress()
}

// BuilderAddress is the address deploy/sandbox-image.sh gives its build instance: the highest usable address of
// Range, never assigned to a sandbox, so building an image while sandboxes run cannot collide with one.
func (n Network) BuilderAddress() netip.Addr {
	for a := lastAddr(n.Range); a.IsValid() && n.Range.Contains(a); a = a.Prev() {
		if last := a.As4()[3]; last != 0 && last != 255 && a != lastAddr(n.Subnet) && a != n.Gateway {
			return a
		}
	}
	return netip.Addr{}
}

// lastAddr is the highest address of an IPv4 prefix.
func lastAddr(p netip.Prefix) netip.Addr {
	if !p.IsValid() || !p.Addr().Is4() {
		return netip.Addr{}
	}
	b := p.Masked().Addr().As4()
	binary.BigEndian.PutUint32(b[:], binary.BigEndian.Uint32(b[:])|(uint32(1)<<(32-p.Bits())-1))
	return netip.AddrFrom4(b)
}

// NIC is one instance's network interface eth0: a bridged port on Bridge with port isolation (no traffic to other
// isolated ports, so sandboxes cannot see each other at L2) and MAC and IPv4 filtering (frames only with its own MAC
// and Address: no ARP spoofing of the gateway, Flow or another sandbox, no foreign source addresses, which the
// host's source-based firewalld policies rely on).
type NIC struct {
	Bridge string
	// Address is the instance's address with the subnet's length (172.30.128.5/16): the gateway and Flow's
	// container are on-link.
	Address   netip.Prefix
	Gateway   netip.Addr
	Resolvers []netip.Addr
}

// GuestFiles is the guest's network configuration, a systemd-networkd unit written from outside before the
// instance starts (Docker bridges have no DHCP). It sorts before the image's own units, so it wins for eth0.
func (n NIC) GuestFiles() []File {
	var b strings.Builder
	b.WriteString("# Written by Flow before each start: the address Flow assigned, no DHCP, no IPv6.\n")
	b.WriteString("[Match]\nName=eth0\n\n[Network]\n")
	fmt.Fprintf(&b, "Address=%s\nGateway=%s\n", n.Address, n.Gateway)
	for _, r := range n.Resolvers {
		fmt.Fprintf(&b, "DNS=%s\n", r)
	}
	b.WriteString("LinkLocalAddressing=no\nIPv6AcceptRA=no\n")
	return []File{{Path: layout.NetworkConfig, Content: []byte(b.String()), Mode: 0o644}}
}
