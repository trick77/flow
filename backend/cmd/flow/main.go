// Command flow is the server: the JSON API and the embedded UI.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/logging"
	"github.com/trick77/ship/kit/version"

	"github.com/trick77/flow/internal/config"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/web"
)

// shutdownTimeout lets in-flight requests finish on SIGTERM.
const shutdownTimeout = 15 * time.Second

func main() {
	// The container probe (compose healthcheck) runs before any setup: it must answer even when the rest of the
	// environment is broken.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(httpx.Healthcheck(httpx.LocalURL(config.Addr(os.Getenv), "/healthz")))
	}
	// A bad BACKEND_LOG_LEVEL fails boot instead of logging at info.
	if _, err := logging.Setup(logging.Options{Level: os.Getenv("BACKEND_LOG_LEVEL"), LevelVar: "BACKEND_LOG_LEVEL"}); err != nil {
		slog.Error("fatal", logging.Err(err))
		os.Exit(1)
	}
	if err := serve(); err != nil {
		slog.Error("fatal", logging.Err(err))
		os.Exit(1)
	}
}

func serve() error {
	// First, so the running build is identifiable even when startup fails later.
	slog.Info("starting flow", "version", version.String())
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	handler := httpapi.New(httpapi.Options{Version: version.String(), Static: web.SPAHandler()})
	slog.Info("listening", "addr", cfg.Addr)
	// Serve stops on SIGINT/SIGTERM (kit's run.Context) and drains.
	return httpx.Serve(context.Background(), httpx.NewServer(cfg.Addr, handler), shutdownTimeout)
}
