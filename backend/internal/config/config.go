// Package config reads the server's configuration from BACKEND_* environment variables.
package config

import (
	"encoding/base64"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/trick77/ship/kit/env"

	"github.com/trick77/flow/internal/sandbox"
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
	// SandboxImage is the base image alias.
	SandboxImage string
	// SandboxNetwork is how sandboxes are networked: the host's Docker bridge, its subnet and gateway, the
	// sandboxes' address range and resolvers (network.go).
	SandboxNetwork sandbox.Network
	// SandboxMemory is each sandbox's limits.memory in bytes (0: none). Setting it also keeps the sandbox from
	// swapping (spike finding 7). A sandbox starts while the running ones' measured memory plus
	// SandboxMemoryHeadroom fits under SandboxMemoryTotal (0: no check).
	SandboxMemory, SandboxMemoryTotal, SandboxMemoryHeadroom int64
	// SandboxCPUs is each sandbox's limits.cpu (0: all of the host's).
	SandboxCPUs int
	// Session bounds each session host unit and everything its agent runs (systemd MemoryMax, CPUQuota, TasksMax;
	// MemorySwapMax=0 always).
	Session SessionLimits
	// SetupTimeout is the time limit of each repository's .flow/setup.sh (its unit gets Session's limits).
	SetupTimeout time.Duration
	// SessionReplay makes session hosts play the recorded demo turn instead of starting the agent: demos, and
	// machines without a Claude credential. Read at each host start.
	SessionReplay bool
	// HomeVolume is the custom volume mounted into every sandbox (layout.HomeVolume); IncusPool holds it.
	HomeVolume, IncusPool string
	// FlowdDir holds flowd-linux-amd64 and flowd-linux-arm64, the binaries Flow pushes into sandboxes; SidecarDir
	// the sidecar bundle delivered with them (package.json, dist/, production node_modules).
	FlowdDir, SidecarDir string
	// OIDC is the oidc mode's sign-in; empty in dev mode.
	OIDC OIDC
	// GitHubClientID is the owner's GitHub App's client id (not its app id): the device flow and the user-token
	// renewal need only it, no client secret (docs/deploy.md, "GitHub App"). Empty: the GitHub connection is
	// reported unavailable and repositories are reached anonymously.
	GitHubClientID string
}

// OIDC configures sign-in through the owner's identity provider (docs/auth.md).
type OIDC struct {
	// PublicURL is https://<BACKEND_PUBLIC_FQDN>: the redirect URI and the origin state changes must come from.
	PublicURL                      string
	Issuer, ClientID, ClientSecret string
	// Subject and Group admit the owner: a login whose sub is Subject, or whose groups claim holds Group. At least
	// one is set.
	Subject, Group string
	// SessionSecret signs the session cookie (at least 32 characters); rotating it signs the owner out.
	SessionSecret string
	// SessionTTL is how long a sign-in lasts; nothing renews it, and nothing but its end (or a new SessionSecret)
	// notices a change at the provider: the cookie carries the groups of the sign-in.
	SessionTTL time.Duration
}

// minSessionSecret: a shorter secret can be guessed offline from one cookie, and then a session forged.
const minSessionSecret = 32

// DefaultSessionTTL is the sign-in lifetime when BACKEND_SESSION_TTL is unset: one working day. It is also how long
// a group removal or a disabled account at the provider takes to reach Flow (the cookie keeps the groups of its
// sign-in), so short; one sign-in a day costs the owner a redirect, usually without a password prompt while the
// provider's own session lasts.
const DefaultSessionTTL = 12 * time.Hour

// DefaultSetupTimeout is a setup script's time limit when BACKEND_SETUP_TIMEOUT is unset: room for installing a
// project's toolchain and dependencies on a cold cache, short enough that a hung script fails the bring-up the same
// working session.
const DefaultSetupTimeout = 20 * time.Minute

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
		IncusPool:       r.String("BACKEND_INCUS_POOL", "default"),
		SandboxImage:    r.String("BACKEND_SANDBOX_IMAGE", "flow-base"),
		SandboxNetwork:  loadSandboxNetwork(r),
		SandboxMemory:   int64(r.Int("BACKEND_SANDBOX_MEMORY_MIB", DefaultSandboxMemoryMiB)) << 20,
		SandboxCPUs:     r.Int("BACKEND_SANDBOX_CPUS", DefaultSandboxCPUs),
		SessionReplay:   r.Bool("BACKEND_SESSION_REPLAY", false),
		GitHubClientID:  r.String("BACKEND_GITHUB_CLIENT_ID", ""),
		SetupTimeout:    r.Duration("BACKEND_SETUP_TIMEOUT", DefaultSetupTimeout),
		HomeVolume:      r.String("BACKEND_HOME_VOLUME", "flow-home"),
		// Where backend/Containerfile puts them.
		FlowdDir:   r.String("BACKEND_FLOWD_DIR", "/opt/flow"),
		SidecarDir: r.String("BACKEND_SIDECAR_DIR", "/opt/flow/sidecar"),
	}
	cfg.SandboxMemoryTotal, cfg.SandboxMemoryHeadroom = loadSandboxMemoryTotal(r, cfg.SandboxMemory)
	cfg.Session = loadSessionLimits(r, cfg.SandboxMemory, cfg.SandboxCPUs)
	if cfg.SetupTimeout <= 0 {
		r.Fail("BACKEND_SETUP_TIMEOUT: want a duration above zero")
	}
	checkAddrOffSandboxNetwork(r, cfg.Addr, cfg.SandboxNetwork)
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
	if cfg.AuthMode == AuthOIDC {
		cfg.OIDC = loadOIDC(r)
		// Dev mode makes a key per start (cmd/flow); a real installation must keep its secrets across restarts.
		// Blank counts as unset (as the reader above takes it); a key that is set but bad has its own fault.
		if cfg.SealKey == nil && strings.TrimSpace(getenv("BACKEND_SEAL_KEY")) == "" {
			r.Fail("BACKEND_AUTH_MODE=oidc needs BACKEND_SEAL_KEY (32 bytes, base64): credentials are stored sealed")
		}
	}
	if cfg.IncusURL != "" {
		if cfg.IncusCert == "" || cfg.IncusKey == "" || cfg.IncusServerCert == "" {
			r.Fail("BACKEND_INCUS_URL needs BACKEND_INCUS_CERT, BACKEND_INCUS_KEY and BACKEND_INCUS_SERVER_CERT")
		}
	}
	if err := r.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadOIDC reads the oidc mode's settings; every one but the TTL and one of subject and group is required.
func loadOIDC(r *env.Reader) OIDC {
	o := OIDC{
		Issuer:        r.Required("BACKEND_OIDC_ISSUER"),
		ClientID:      r.Required("BACKEND_OIDC_CLIENT_ID"),
		ClientSecret:  r.Required("BACKEND_OIDC_CLIENT_SECRET"),
		Subject:       r.String("BACKEND_OIDC_ALLOWED_SUBJECT", ""),
		Group:         r.String("BACKEND_OIDC_ALLOWED_GROUP", ""),
		SessionSecret: r.Required("BACKEND_SESSION_SECRET"),
		SessionTTL:    r.Duration("BACKEND_SESSION_TTL", DefaultSessionTTL),
	}
	// The public URL is the only one Flow answers state changes from and the provider's redirect target: a bare
	// host name, served over TLS (the session cookie is Secure).
	if fqdn := r.Required("BACKEND_PUBLIC_FQDN"); fqdn != "" {
		// Judged by the parsed host name, so a stray port separator or trailing dot (which would boot and then
		// refuse every state change and break the redirect URI) is a startup error.
		u, err := url.Parse("https://" + fqdn)
		if err != nil || u.Hostname() != fqdn || !hostName(fqdn) {
			r.Fail("BACKEND_PUBLIC_FQDN: want a bare host name such as flow.example.com (no scheme, port, path or "+
				"trailing dot), got %q", fqdn)
		} else {
			o.PublicURL = "https://" + strings.ToLower(u.Hostname())
		}
	}
	if o.Subject == "" && o.Group == "" {
		r.Fail("BACKEND_AUTH_MODE=oidc admits only the owner: set BACKEND_OIDC_ALLOWED_SUBJECT or BACKEND_OIDC_ALLOWED_GROUP")
	}
	if o.SessionSecret != "" && len(o.SessionSecret) < minSessionSecret {
		r.Fail("BACKEND_SESSION_SECRET: want at least %d characters, it signs the session cookie", minSessionSecret)
	}
	if o.SessionTTL <= 0 {
		r.Fail("BACKEND_SESSION_TTL: want a duration above zero")
	}
	return o
}

// hostName reports whether h is a DNS host name: dot-separated labels of letters, digits and inner hyphens.
func hostName(h string) bool {
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !hostNameRune(c) {
				return false
			}
		}
	}
	return true
}

// hostNameRune reports whether c may appear in a host name label: a letter, a digit or a hyphen.
func hostNameRune(c rune) bool {
	return c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
