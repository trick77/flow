package config

import (
	"maps"
	"strings"
	"testing"
	"time"
)

func lookup(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// oidcVars is a complete oidc-mode environment plus extra.
func oidcVars(extra map[string]string) map[string]string {
	vars := map[string]string{
		"BACKEND_PUBLIC_FQDN":          "flow.example.com",
		"BACKEND_OIDC_ISSUER":          "https://auth.example.com",
		"BACKEND_OIDC_CLIENT_ID":       "flow",
		"BACKEND_OIDC_CLIENT_SECRET":   "client-secret",
		"BACKEND_OIDC_ALLOWED_SUBJECT": "owner-sub",
		"BACKEND_SESSION_SECRET":       strings.Repeat("s", 32),
	}
	maps.Copy(vars, extra)
	return vars
}

func TestLoad_defaults(t *testing.T) {
	cfg, err := Load(lookup(oidcVars(nil)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != DefaultAddr || cfg.AuthMode != AuthOIDC {
		t.Errorf("Addr = %q, mode %q", cfg.Addr, cfg.AuthMode)
	}
	want := OIDC{PublicURL: "https://flow.example.com", Issuer: "https://auth.example.com", ClientID: "flow",
		ClientSecret: "client-secret", Subject: "owner-sub", SessionSecret: strings.Repeat("s", 32),
		SessionTTL: DefaultSessionTTL}
	if cfg.OIDC != want {
		t.Errorf("OIDC = %+v, want %+v", cfg.OIDC, want)
	}
}

// The session keeps the groups of its sign-in, so its lifetime is how long a removal at the provider takes to reach
// Flow: at most a day by default.
func TestDefaultSessionTTL_boundsStaleAdmission(t *testing.T) {
	if DefaultSessionTTL > 24*time.Hour {
		t.Errorf("DefaultSessionTTL = %s", DefaultSessionTTL)
	}
}

// oidc is the default mode: an environment without its settings must not boot, and the error names every one.
func TestLoad_oidcNeedsItsSettings(t *testing.T) {
	_, err := Load(lookup(nil))
	if err == nil {
		t.Fatal("Load accepted oidc mode without settings")
	}
	for _, name := range []string{"BACKEND_PUBLIC_FQDN", "BACKEND_OIDC_ISSUER", "BACKEND_OIDC_CLIENT_ID",
		"BACKEND_OIDC_CLIENT_SECRET", "BACKEND_SESSION_SECRET", "BACKEND_OIDC_ALLOWED_SUBJECT"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func TestLoad_oidcSettings(t *testing.T) {
	cfg, err := Load(lookup(oidcVars(map[string]string{"BACKEND_OIDC_ALLOWED_SUBJECT": "",
		"BACKEND_OIDC_ALLOWED_GROUP": "flow-owner", "BACKEND_SESSION_TTL": "12h", "BACKEND_PUBLIC_FQDN": "Flow.Example.com"})))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC.Group != "flow-owner" || cfg.OIDC.Subject != "" || cfg.OIDC.SessionTTL != 12*time.Hour ||
		cfg.OIDC.PublicURL != "https://flow.example.com" {
		t.Errorf("OIDC = %+v", cfg.OIDC)
	}
	for name, extra := range map[string]map[string]string{
		"nobody admitted":   {"BACKEND_OIDC_ALLOWED_SUBJECT": ""},
		"short secret":      {"BACKEND_SESSION_SECRET": "short"},
		"zero ttl":          {"BACKEND_SESSION_TTL": "0s"},
		"bad ttl":           {"BACKEND_SESSION_TTL": "soon"},
		"fqdn with scheme":  {"BACKEND_PUBLIC_FQDN": "https://flow.example.com"},
		"fqdn with path":    {"BACKEND_PUBLIC_FQDN": "flow.example.com/x"},
		"fqdn with port":    {"BACKEND_PUBLIC_FQDN": "flow.example.com:8443"},
		"unreadable fqdn":   {"BACKEND_PUBLIC_FQDN": "flow%zz"},
		"trailing colon":    {"BACKEND_PUBLIC_FQDN": "flow.example.com:"},
		"trailing dot":      {"BACKEND_PUBLIC_FQDN": "flow.example.com."},
		"empty label":       {"BACKEND_PUBLIC_FQDN": "flow..example.com"},
		"bad label":         {"BACKEND_PUBLIC_FQDN": "-flow.example.com"},
		"underscore":        {"BACKEND_PUBLIC_FQDN": "flow_x.example.com"},
		"userinfo":          {"BACKEND_PUBLIC_FQDN": "me@flow.example.com"},
		"missing client id": {"BACKEND_OIDC_CLIENT_ID": " "},
	} {
		if _, err := Load(lookup(oidcVars(extra))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoad_addrFromEnv(t *testing.T) {
	cfg, err := Load(lookup(oidcVars(map[string]string{"BACKEND_ADDR": " 127.0.0.1:9090 "})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9090" {
		t.Errorf("Addr = %q, want the trimmed value", cfg.Addr)
	}
}

func TestLoad_blankAddrIsUnset(t *testing.T) {
	cfg, err := Load(lookup(oidcVars(map[string]string{"BACKEND_ADDR": "  "})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != DefaultAddr {
		t.Errorf("Addr = %q, want the default", cfg.Addr)
	}
}

func TestLoad_badAddrFails(t *testing.T) {
	_, err := Load(lookup(oidcVars(map[string]string{"BACKEND_ADDR": "8080"})))
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
	if cfg.OIDC != (OIDC{}) {
		t.Errorf("dev mode read oidc settings: %+v", cfg.OIDC)
	}
	if _, err := Load(lookup(map[string]string{"BACKEND_AUTH_MODE": "dev", "BACKEND_ADDR": "localhost:1"})); err != nil {
		t.Errorf("dev on localhost: %v", err)
	}
	for name, vars := range map[string]map[string]string{
		"dev on every interface": {"BACKEND_AUTH_MODE": "dev"},
		"bad seal key":           oidcVars(map[string]string{"BACKEND_SEAL_KEY": "short"}),
		"incus without certs":    oidcVars(map[string]string{"BACKEND_INCUS_URL": "https://x", "BACKEND_SEAL_KEY": key}),
		"incus without seal key": oidcVars(map[string]string{"BACKEND_INCUS_URL": "https://x", "BACKEND_INCUS_CERT": "c", "BACKEND_INCUS_KEY": "k", "BACKEND_INCUS_SERVER_CERT": "s"}),
	} {
		if _, err := Load(lookup(vars)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAddr_matchesLoad(t *testing.T) {
	vars := oidcVars(map[string]string{"BACKEND_ADDR": "127.0.0.1:9191"})
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
