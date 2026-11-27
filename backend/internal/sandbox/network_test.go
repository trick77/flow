package sandbox

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/trick77/flow/internal/layout"
)

func labNetwork() Network {
	return Network{Bridge: "flowbr0", Subnet: netip.MustParsePrefix("172.30.0.0/16"),
		Range: netip.MustParsePrefix("172.30.128.0/17"), Gateway: netip.MustParseAddr("172.30.0.1"),
		Resolvers: []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("9.9.9.9")}}
}

func TestAssignable(t *testing.T) {
	n := labNetwork()
	if !n.Configured() || (Network{Bridge: "x"}).Configured() {
		t.Error("Configured")
	}
	for addr, want := range map[string]bool{
		"172.30.128.1":   true,
		"172.30.200.17":  true,
		"172.30.128.0":   false, // .0
		"172.30.129.255": false, // .255
		"172.30.255.254": false, // the image builder's
		"172.30.255.255": false, // the subnet's broadcast
		"172.30.0.5":     false, // Docker's part
		"172.30.0.1":     false, // gateway
		"10.0.0.1":       false,
		"::1":            false,
	} {
		if got := n.Assignable(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Assignable(%s) = %v", addr, got)
		}
	}
	if b := n.BuilderAddress(); b != netip.MustParseAddr("172.30.255.254") {
		t.Errorf("BuilderAddress = %s", b)
	}
	small := n
	small.Range = netip.MustParsePrefix("172.30.1.0/30") // .0 .1 .2 .3: the builder takes .3, .1 and .2 stay
	if b := small.BuilderAddress(); b != netip.MustParseAddr("172.30.1.3") || !small.Assignable(netip.MustParseAddr("172.30.1.2")) {
		t.Errorf("small range: builder %s", b)
	}
	if lastAddr(netip.Prefix{}).IsValid() {
		t.Error("lastAddr of nothing")
	}
}

// builderCases are shared with deploy/host-lib.sh's builder_address (checked in the lab against the same table).
var builderCases = []struct{ subnet, rng, gateway, want string }{
	{"172.30.0.0/16", "172.30.128.0/17", "172.30.0.1", "172.30.255.254"},
	{"172.30.0.0/16", "172.30.64.0/18", "172.30.0.1", "172.30.127.254"},
	{"10.88.0.0/20", "10.88.8.0/21", "10.88.0.1", "10.88.15.254"},
	{"192.168.50.0/24", "192.168.50.128/25", "192.168.50.1", "192.168.50.254"},
}

func TestBuilderAddress_table(t *testing.T) {
	for _, c := range builderCases {
		n := Network{Subnet: netip.MustParsePrefix(c.subnet), Range: netip.MustParsePrefix(c.rng),
			Gateway: netip.MustParseAddr(c.gateway)}
		if got := n.BuilderAddress().String(); got != c.want {
			t.Errorf("%s in %s: %s, want %s", c.rng, c.subnet, got, c.want)
		}
	}
}

func TestNICGuestFiles(t *testing.T) {
	nic := labNetwork().NIC(netip.MustParseAddr("172.30.128.7"))
	if nic.Bridge != "flowbr0" || nic.Address.String() != "172.30.128.7/16" || len(nic.Resolvers) != 2 {
		t.Fatalf("NIC = %+v", nic)
	}
	files := nic.GuestFiles()
	if len(files) != 1 || files[0].Path != layout.NetworkConfig || files[0].Mode != 0o644 {
		t.Fatalf("files %+v", files)
	}
	got := string(files[0].Content)
	for _, line := range []string{"Name=eth0", "Address=172.30.128.7/16", "Gateway=172.30.0.1", "DNS=1.1.1.1",
		"DNS=9.9.9.9", "LinkLocalAddressing=no", "IPv6AcceptRA=no"} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("unit lacks %q:\n%s", line, got)
		}
	}
	if strings.Contains(got, "DHCP=") {
		t.Errorf("unit asks for DHCP:\n%s", got)
	}
}
