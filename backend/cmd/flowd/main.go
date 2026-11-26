// Command flowd is the supervisor daemon inside each sandbox's system container. The server pushes the binary
// matching the sandbox's architecture into it.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/trick77/ship/kit/logging"
	"github.com/trick77/ship/kit/run"
	"github.com/trick77/ship/kit/version"

	"github.com/trick77/flow/internal/supervisor"
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
	return supervisor.Run(ctx, slog.Default(), version.String())
}
