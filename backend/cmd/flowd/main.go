// Command flowd is the supervisor daemon inside each sandbox's system container. The server pushes the binary
// matching the sandbox's architecture into it. Multi-call: "flowd host" is a session host, started by flowd as a
// transient systemd unit.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/trick77/ship/kit/logging"
	"github.com/trick77/ship/kit/run"
	"github.com/trick77/ship/kit/version"

	"github.com/trick77/flow/internal/host"
	"github.com/trick77/flow/internal/supervisor"
	"github.com/trick77/flow/internal/terminals/term"
)

func main() {
	if err := start(); err != nil {
		slog.Error("fatal", logging.Err(err))
		os.Exit(1)
	}
}

func start() error {
	if _, err := logging.Setup(logging.Options{Level: os.Getenv("BACKEND_LOG_LEVEL"), LevelVar: "BACKEND_LOG_LEVEL"}); err != nil {
		return err
	}
	ctx, stop := run.Context(context.Background())
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "host" {
		return host.Run(ctx, os.Args[2:], version.String(), slog.Default())
	}
	if len(os.Args) > 1 && os.Args[1] == "term" {
		return term.Run(ctx, os.Args[2:], slog.Default())
	}
	if len(os.Args) > 1 {
		// Never the supervisor by accident (flowd.service runs it without arguments): an agent starting "flowd mcp"
		// before it exists must fail as that, not try to become a second flowd.
		return fmt.Errorf("flowd: unknown subcommand %q", os.Args[1])
	}
	return supervisor.Run(ctx, slog.Default(), version.String())
}
