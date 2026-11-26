// Package config reads the server's configuration from BACKEND_* environment variables.
package config

import (
	"encoding/base64"
	"net"
	"strings"

	"github.com/trick77/ship/kit/env"
)

// DefaultAddr is where the server listens when BACKEND_ADDR is unset.
const DefaultAddr = ":8080"

// Auth modes.
const (
	AuthOIDC = "oidc"
	AuthDev  = "dev"
)

// Config holds the server's runtime settings.
type Config struct {
	// Addr is the HTTP listen address, host:port (":8080" for every interface).
	Addr string
	// AuthMode is oidc or dev; dev admits every request and is refused unless Addr is a loopback address.
	AuthMode string
	// DBPath is the SQLite database file.
	DBPath string
	// SealKey is the AES-256 key sealing secrets at rest (BACKEND_SEAL_KEY, 32 bytes, base64).
	SealKey []byte
	// Incus is how Flow reaches the sandbox runtime; the sandbox, session and timeline areas stay unwired without
	// IncusURL.
	IncusURL, IncusProject, IncusCert, IncusKey, IncusServerCert string
	// SandboxImage is the base image alias, SandboxNetwork the managed bridge sandboxes attach to.
	SandboxImage, SandboxNetwork string
	// SandboxMemory is each sandbox's limits.memory in bytes (0: none). Setting it also keeps the sandbox from
	// swapping (spike finding 7).
	SandboxMemory int64
	// FlowdDir holds flowd-linux-amd64 and flowd-linux-arm64, the binaries Flow pushes into sandboxes.
	FlowdDir string
}

// Addr is BACKEND_ADDR as Load reads it (trimmed, empty is unset). The healthcheck subcommand probes it before
// anything else is set up, so a broken rest of the environment cannot fail the probe.
func Addr(getenv func(string) string) string {
	return env.New(getenv).String("BACKEND_ADDR", DefaultAddr)
}

// Load reads every setting and reports all faults at once.
func Load(getenv func(string) string) (Config, error) {
	r := env.New(getenv)
	cfg := Config{
		Addr:            r.String("BACKEND_ADDR", DefaultAddr),
		AuthMode:        r.Enum("BACKEND_AUTH_MODE", AuthOIDC, AuthOIDC, AuthDev),
		DBPath:          r.String("BACKEND_DB_PATH", "/data/flow.db"),
		IncusURL:        r.String("BACKEND_INCUS_URL", ""),
		IncusProject:    r.String("BACKEND_INCUS_PROJECT", "flow"),
		IncusCert:       r.String("BACKEND_INCUS_CERT", ""),
		IncusKey:        r.String("BACKEND_INCUS_KEY", ""),
		IncusServerCert: r.String("BACKEND_INCUS_SERVER_CERT", ""),
		SandboxImage:    r.String("BACKEND_SANDBOX_IMAGE", "flow-base"),
		SandboxNetwork:  r.String("BACKEND_SANDBOX_NETWORK", "incusbr0"),
		SandboxMemory:   int64(r.Int("BACKEND_SANDBOX_MEMORY_MIB", 4096)) << 20,
		FlowdDir:        r.String("BACKEND_FLOWD_DIR", "/flowd"),
	}
	// net.Listen would refuse it too, but only after the rest of startup ran.
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		r.Fail("BACKEND_ADDR %q: want host:port, e.g. %s", cfg.Addr, DefaultAddr)
	} else if cfg.AuthMode == AuthDev && !loopback(host) {
		r.Fail("BACKEND_AUTH_MODE=dev admits everyone: BACKEND_ADDR must be a loopback address, not %q", cfg.Addr)
	}
	if k := r.String("BACKEND_SEAL_KEY", ""); k != "" {
		key, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(key) != 32 {
			r.Fail("BACKEND_SEAL_KEY: want 32 bytes, base64")
		}
		cfg.SealKey = key
	}
	if cfg.IncusURL != "" {
		if cfg.IncusCert == "" || cfg.IncusKey == "" || cfg.IncusServerCert == "" {
			r.Fail("BACKEND_INCUS_URL needs BACKEND_INCUS_CERT, BACKEND_INCUS_KEY and BACKEND_INCUS_SERVER_CERT")
		}
		if cfg.SealKey == nil {
			r.Fail("BACKEND_INCUS_URL needs BACKEND_SEAL_KEY: flowd tokens are stored sealed")
		}
	}
	if err := r.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
