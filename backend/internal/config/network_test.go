package config

import (
	"maps"
	"net/netip"
	"strings"
	"testing"
)

// devVars is a minimal environment that loads, plus extra: the sandbox network tests need no sign-in settings.
func devVars(extra map[string]string) map[string]string {
	vars := map[string]string{"BACKEND_AUTH_MODE": "dev", "BACKEND_ADDR": "127.0.0.1:8080"}
	maps.Copy(vars, extra)
	return vars
}

func TestSandboxNetwork_defaults(t *testing.T) {
	cfg, err := Load(lookup(devVars(nil)))
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.SandboxNetwork
	if n.Bridge != "flowbr0" || n.Subnet.String() != "172.30.0.0/16" || n.Range.String() != "172.30.128.0/17" ||
		n.Gateway.String() != "172.30.0.1" || len(n.Resolvers) != 2 || n.Resolvers[1].String() != "9.9.9.9" {
		t.Errorf("network %+v", n)
	}
	if !n.Configured() || cfg.SandboxMemoryTotal != 14<<30 || cfg.SandboxMemoryHeadroom != 6<<30 {
		t.Errorf("configured %v, total %d, headroom %d", n.Configured(), cfg.SandboxMemoryTotal,
			cfg.SandboxMemoryHeadroom)
	}
}

// The headroom follows the sandbox's limit unless set; a total of 0 turns the admission check off.
func TestSandboxMemoryHeadroom(t *testing.T) {
	for name, c := range map[string]struct {
		vars            map[string]string
		total, headroom int64
	}{
		"follows the limit":   {map[string]string{"BACKEND_SANDBOX_MEMORY_MIB": "2048"}, 14 << 30, 2 << 30},
		"no limit":            {map[string]string{"BACKEND_SANDBOX_MEMORY_MIB": "0", "BACKEND_SESSION_MEMORY_MIB": "0"}, 14 << 30, 6 << 30},
		"set":                 {map[string]string{"BACKEND_SANDBOX_MEMORY_HEADROOM_MIB": "1024"}, 14 << 30, 1 << 30},
		"no check":            {map[string]string{"BACKEND_SANDBOX_MEMORY_TOTAL_MIB": "0"}, 0, 6 << 30},
		"total under a limit": {map[string]string{"BACKEND_SANDBOX_MEMORY_TOTAL_MIB": "4096", "BACKEND_SANDBOX_MEMORY_HEADROOM_MIB": "2048"}, 4 << 30, 2 << 30},
	} {
		cfg, err := Load(lookup(devVars(c.vars)))
		if err != nil || cfg.SandboxMemoryTotal != c.total || cfg.SandboxMemoryHeadroom != c.headroom {
			t.Errorf("%s: total %d, headroom %d, %v", name, cfg.SandboxMemoryTotal, cfg.SandboxMemoryHeadroom, err)
		}
	}
	for _, vars := range []map[string]string{{"BACKEND_SANDBOX_MEMORY_TOTAL_MIB": "-1"},
		{"BACKEND_SANDBOX_MEMORY_HEADROOM_MIB": "-1"}} {
		if _, err := Load(lookup(devVars(vars))); err == nil {
			t.Errorf("%v: no error", vars)
		}
	}
}

func TestSandboxNetwork_fromEnv(t *testing.T) {
	cfg, err := Load(lookup(devVars(map[string]string{"BACKEND_SANDBOX_BRIDGE": "sbx0",
		"BACKEND_SANDBOX_SUBNET": "10.88.0.0/20", "BACKEND_SANDBOX_RANGE": "10.88.8.0/21",
		"BACKEND_SANDBOX_RESOLVERS": " 10.88.0.53 , 9.9.9.9", "BACKEND_SANDBOX_MEMORY_TOTAL_MIB": "8192"})))
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.SandboxNetwork
	if n.Bridge != "sbx0" || n.Gateway != netip.MustParseAddr("10.88.0.1") || n.Resolvers[0].String() != "10.88.0.53" ||
		cfg.SandboxMemoryTotal != 8<<30 {
		t.Errorf("network %+v, total %d", n, cfg.SandboxMemoryTotal)
	}
}

// Every fault names its variable and says what is wrong.
func TestSandboxNetwork_faults(t *testing.T) {
	for name, c := range map[string]struct {
		vars map[string]string
		want string
	}{
		"subnet not a network":   {map[string]string{"BACKEND_SANDBOX_SUBNET": "172.30.0.1/16"}, "BACKEND_SANDBOX_SUBNET"},
		"subnet ipv6":            {map[string]string{"BACKEND_SANDBOX_SUBNET": "fd00::/64"}, "no IPv6"},
		"range garbage":          {map[string]string{"BACKEND_SANDBOX_RANGE": "upper half"}, "BACKEND_SANDBOX_RANGE"},
		"range is the subnet":    {map[string]string{"BACKEND_SANDBOX_RANGE": "172.30.0.0/16"}, "Docker allocates"},
		"range outside":          {map[string]string{"BACKEND_SANDBOX_RANGE": "10.0.0.0/24"}, "smaller part"},
		"gateway outside":        {map[string]string{"BACKEND_SANDBOX_GATEWAY": "10.0.0.1"}, "BACKEND_SANDBOX_GATEWAY"},
		"gateway in range":       {map[string]string{"BACKEND_SANDBOX_GATEWAY": "172.30.200.1"}, "lies in BACKEND_SANDBOX_RANGE"},
		"gateway garbage":        {map[string]string{"BACKEND_SANDBOX_GATEWAY": "gw"}, "not an IPv4 address"},
		"resolver is the host":   {map[string]string{"BACKEND_SANDBOX_RESOLVERS": "172.30.0.1"}, "is the host"},
		"resolver in range":      {map[string]string{"BACKEND_SANDBOX_RESOLVERS": "172.30.128.53"}, "among the sandboxes"},
		"resolver ipv6":          {map[string]string{"BACKEND_SANDBOX_RESOLVERS": "2606:4700::1111"}, "BACKEND_SANDBOX_RESOLVERS"},
		"resolver on the LAN":    {map[string]string{"BACKEND_SANDBOX_RESOLVERS": "192.168.1.1"}, "flow-sb-lan"},
		"resolver link-local":    {map[string]string{"BACKEND_SANDBOX_RESOLVERS": "169.254.169.253"}, "flow-sb-lan"},
		"resolver shared space":  {map[string]string{"BACKEND_SANDBOX_RESOLVERS": "100.100.100.100"}, "flow-sb-lan"},
		"no resolver":            {map[string]string{"BACKEND_SANDBOX_RESOLVERS": " , "}, "at least one"},
		"bridge name too long":   {map[string]string{"BACKEND_SANDBOX_BRIDGE": "a-very-long-bridge"}, "BACKEND_SANDBOX_BRIDGE"},
		"range of one address":   {map[string]string{"BACKEND_SANDBOX_RANGE": "172.30.255.255/32"}, "BACKEND_SANDBOX_RANGE"},
		"ceiling below one":      {map[string]string{"BACKEND_SANDBOX_MEMORY_TOTAL_MIB": "1024"}, "not even one sandbox"},
		"listening on the flows": {map[string]string{"BACKEND_ADDR": "172.30.0.2:8080", "BACKEND_AUTH_MODE": ""}, "sandbox network"},
	} {
		_, err := Load(lookup(devVars(c.vars)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.want)
		}
	}
}
