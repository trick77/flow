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
