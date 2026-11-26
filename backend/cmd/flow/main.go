// Command flow is the server: the JSON API and the embedded UI.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/logging"
	"github.com/trick77/ship/kit/sse"
	"github.com/trick77/ship/kit/version"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/auth"
	"github.com/trick77/flow/internal/config"
	"github.com/trick77/flow/internal/credentials"
	"github.com/trick77/flow/internal/flowdclient"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandbox/incus"
	"github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/sessions"
	"github.com/trick77/flow/internal/store"
	"github.com/trick77/flow/internal/timeline"
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
	// Streams (SSE) end with this context when the server shuts down, instead of holding the drain.
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := httpapi.Options{Version: version.String(), Static: web.SPAHandler()}
	if cfg.AuthMode == config.AuthDev {
		slog.Warn("dev auth mode: every request is the owner's", "addr", cfg.Addr)
		opts.Guard, opts.Areas.Auth = auth.Dev{}, auth.Dev{}
	}
	hub := sse.NewHub[apitypes.AppEvent](sse.HubOptions{})
	defer hub.Close()
	if cfg.IncusURL != "" {
		db, err := store.Open(base, cfg.DBPath)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		if err := wire(base, cfg, db, hub, &opts.Areas); err != nil {
			return err
		}
	} else {
		slog.Warn("BACKEND_INCUS_URL unset: projects, sandboxes, sessions and timeline answer not_implemented")
	}
	srv := httpx.NewServer(cfg.Addr, httpapi.New(opts))
	srv.BaseContext = func(net.Listener) context.Context { return base }
	srv.RegisterOnShutdown(cancel)
	srv.RegisterOnShutdown(hub.Close)
	slog.Info("listening", "addr", cfg.Addr)
	// Serve stops on SIGINT/SIGTERM (kit's run.Context) and drains.
	return httpx.Serve(context.Background(), srv, shutdownTimeout)
}

// wire builds the areas of the walking skeleton: projects, sandboxes on Incus, sessions, timeline.
func wire(ctx context.Context, cfg config.Config, db *sql.DB, hub *sse.Hub[apitypes.AppEvent], areas *httpapi.Areas) error {
	sealer, err := credentials.NewAESSealer(cfg.SealKey)
	if err != nil {
		return err
	}
	runtime, err := incus.New(incus.Config{URL: cfg.IncusURL, Project: cfg.IncusProject, CertFile: cfg.IncusCert,
		KeyFile: cfg.IncusKey, ServerCertFile: cfg.IncusServerCert})
	if err != nil {
		return err
	}
	projectStore := projects.SQL{DB: db}
	sessionStore := sessions.SQL{DB: db}
	manager := &sandboxes.Manager{Store: sandboxes.SQL{DB: db}, Projects: projectStore, Runtime: runtime,
		Dialer: flowdclient.NewDialer(), Sealer: sealer, Image: cfg.SandboxImage, Network: cfg.SandboxNetwork,
		Memory: cfg.SandboxMemory, Version: version.String(), Log: slog.Default(), Wait: time.Minute,
		Poll: 200 * time.Millisecond,
		Flowd: func(arch string) ([]byte, error) {
			return os.ReadFile(filepath.Join(cfg.FlowdDir, fmt.Sprintf("flowd-linux-%s", arch)))
		},
		Changed: func(projectID string) {
			hub.Publish(apitypes.AppEvent{Kind: apitypes.AppEventKindSandboxUpdated, ProjectID: projectID})
		}}
	svc := &sessions.Service{Store: sessionStore, Projects: projectStore, Sandboxes: manager, Hub: hub, Log: slog.Default()}
	if err := svc.Start(ctx); err != nil {
		return err
	}
	areas.Projects = &projects.API{Store: projectStore, Sandboxes: manager,
		DefaultBranch: projects.RemoteDefaultBranch("https://github.com/", &http.Client{Timeout: 30 * time.Second})}
	areas.Sandboxes = &sandboxes.API{Manager: manager}
	areas.Sessions = &sessions.API{Service: svc}
	areas.Timeline = &timeline.API{Sessions: svc, Hub: hub}
	return nil
}
