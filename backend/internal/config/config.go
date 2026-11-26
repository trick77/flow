// Package config reads the server's configuration from BACKEND_* environment variables.
package config

import (
	"net"

	"github.com/trick77/ship/kit/env"
)

// DefaultAddr is where the server listens when BACKEND_ADDR is unset.
const DefaultAddr = ":8080"

// Config holds the server's runtime settings.
type Config struct {
	// Addr is the HTTP listen address, host:port (":8080" for every interface).
	Addr string
}

// Addr is BACKEND_ADDR as Load reads it (trimmed, empty is unset). The healthcheck subcommand probes it before
// anything else is set up, so a broken rest of the environment cannot fail the probe.
func Addr(getenv func(string) string) string {
	return env.New(getenv).String("BACKEND_ADDR", DefaultAddr)
}

// Load reads every setting and reports all faults at once.
func Load(getenv func(string) string) (Config, error) {
	r := env.New(getenv)
	cfg := Config{Addr: r.String("BACKEND_ADDR", DefaultAddr)}
	// net.Listen would refuse it too, but only after the rest of startup ran.
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		r.Fail("BACKEND_ADDR %q: want host:port, e.g. %s", cfg.Addr, DefaultAddr)
	}
	if err := r.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
