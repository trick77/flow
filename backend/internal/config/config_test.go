package config

import (
	"strings"
	"testing"
)

func lookup(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestLoad_defaults(t *testing.T) {
	cfg, err := Load(lookup(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != DefaultAddr {
		t.Errorf("Addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
}

func TestLoad_addrFromEnv(t *testing.T) {
	cfg, err := Load(lookup(map[string]string{"BACKEND_ADDR": " 127.0.0.1:9090 "}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9090" {
		t.Errorf("Addr = %q, want the trimmed value", cfg.Addr)
	}
}

func TestLoad_blankAddrIsUnset(t *testing.T) {
	cfg, err := Load(lookup(map[string]string{"BACKEND_ADDR": "  "}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != DefaultAddr {
		t.Errorf("Addr = %q, want the default", cfg.Addr)
	}
}

func TestLoad_badAddrFails(t *testing.T) {
	_, err := Load(lookup(map[string]string{"BACKEND_ADDR": "8080"}))
	if err == nil || !strings.Contains(err.Error(), "BACKEND_ADDR") {
		t.Fatalf("Load err = %v, want one naming BACKEND_ADDR", err)
	}
}

func TestLoad_devAuthAndIncus(t *testing.T) {
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfg, err := Load(lookup(map[string]string{"BACKEND_AUTH_MODE": "dev", "BACKEND_ADDR": "127.0.0.1:8080",
		"BACKEND_SEAL_KEY": key, "BACKEND_INCUS_URL": "https://127.0.0.1:8443", "BACKEND_INCUS_CERT": "c",
		"BACKEND_INCUS_KEY": "k", "BACKEND_INCUS_SERVER_CERT": "s"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthMode != AuthDev || len(cfg.SealKey) != 32 || cfg.SandboxImage != "flow-base" || cfg.SandboxMemory != 4<<30 {
		t.Errorf("cfg %+v", cfg)
	}
	if _, err := Load(lookup(map[string]string{"BACKEND_AUTH_MODE": "dev", "BACKEND_ADDR": "localhost:1"})); err != nil {
		t.Errorf("dev on localhost: %v", err)
	}
	for name, vars := range map[string]map[string]string{
		"dev on every interface": {"BACKEND_AUTH_MODE": "dev"},
		"bad seal key":           {"BACKEND_SEAL_KEY": "short"},
		"incus without certs":    {"BACKEND_INCUS_URL": "https://x", "BACKEND_SEAL_KEY": key},
		"incus without seal key": {"BACKEND_INCUS_URL": "https://x", "BACKEND_INCUS_CERT": "c", "BACKEND_INCUS_KEY": "k", "BACKEND_INCUS_SERVER_CERT": "s"},
	} {
		if _, err := Load(lookup(vars)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAddr_matchesLoad(t *testing.T) {
	vars := map[string]string{"BACKEND_ADDR": "127.0.0.1:9191"}
	cfg, err := Load(lookup(vars))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := Addr(lookup(vars)); got != cfg.Addr {
		t.Errorf("Addr = %q, Load read %q", got, cfg.Addr)
	}
	if got := Addr(lookup(nil)); got != DefaultAddr {
		t.Errorf("Addr unset = %q, want %q", got, DefaultAddr)
	}
}
