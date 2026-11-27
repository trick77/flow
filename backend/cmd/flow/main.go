// Command flow is the server: the JSON API and the embedded UI.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
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
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/metering"
	"github.com/trick77/flow/internal/profile"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandbox"
	"github.com/trick77/flow/internal/sandbox/incus"
	"github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/sessions"
	"github.com/trick77/flow/internal/store"
	"github.com/trick77/flow/internal/terminals"
	"github.com/trick77/flow/internal/timeline"
	"github.com/trick77/flow/web"
)

// shutdownTimeout lets in-flight requests finish on SIGTERM.
const shutdownTimeout = 15 * time.Second

// discoveryTimeout bounds reading the OIDC provider's discovery document at start.
const discoveryTimeout = 15 * time.Second

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
	switch cfg.AuthMode {
	case config.AuthDev:
		slog.Warn("dev auth mode: every request is the owner's", "addr", cfg.Addr)
		opts.Guard, opts.Areas.Auth = auth.Dev{}, auth.Dev{}
	case config.AuthOIDC:
		// One discovery round trip at start, bounded: a provider that does not answer fails the boot.
		ctx, stop := context.WithTimeout(base, discoveryTimeout)
		oidc, err := auth.NewOIDC(ctx, cfg.OIDC)
		stop()
		if err != nil {
			return err
		}
		slog.Info("oidc sign-in", "issuer", cfg.OIDC.Issuer, "public_url", cfg.OIDC.PublicURL)
		opts.Guard, opts.Areas.Auth = oidc, oidc
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
	srv := httpx.NewServer(cfg.Addr, handler(cfg, opts))
	srv.BaseContext = func(net.Listener) context.Context { return base }
	srv.RegisterOnShutdown(cancel)
	srv.RegisterOnShutdown(hub.Close)
	slog.Info("listening", "addr", cfg.Addr)
	// Serve stops on SIGINT/SIGTERM (kit's run.Context) and drains.
	return httpx.Serve(context.Background(), srv, shutdownTimeout)
}

// sealKey is BACKEND_SEAL_KEY, or in dev mode without one a key made for this run only: every secret stored
// meanwhile is unreadable after a restart (flowd tokens are then made again; credentials must be stored again).
// The config refuses oidc mode without a key.
func sealKey(cfg config.Config) ([]byte, error) {
	if cfg.SealKey != nil {
		return cfg.SealKey, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	slog.Warn("BACKEND_SEAL_KEY unset: dev mode seals with a key made for this run only; stored credentials are " +
		"LOST at the next restart. Set BACKEND_SEAL_KEY (32 bytes, base64) to keep them.")
	return key, nil
}

// wire builds the areas of the walking skeleton: projects, sandboxes on Incus, sessions, timeline, credentials.
func wire(ctx context.Context, cfg config.Config, db *sql.DB, hub *sse.Hub[apitypes.AppEvent], areas *httpapi.Areas) error {
	key, err := sealKey(cfg)
	if err != nil {
		return err
	}
	sealer, err := credentials.NewAESSealer(key)
	if err != nil {
		return err
	}
	runtime, err := incus.New(incus.Config{URL: cfg.IncusURL, Project: cfg.IncusProject, Pool: cfg.IncusPool,
		CertFile: cfg.IncusCert, KeyFile: cfg.IncusKey, ServerCertFile: cfg.IncusServerCert})
	if err != nil {
		return err
	}
	projectStore := projects.SQL{DB: db}
	creds := &credentials.Service{Store: credentials.SQL{DB: db}, Sealer: sealer, Log: slog.Default(),
		Profile: profile.SQL{DB: db}}
	manager := sandboxManager(cfg, db, projectStore, runtime, sealer, hub)
	manager.Env = creds.Env
	// A changed credential reaches running sandboxes at once, new session hosts get it at their start.
	creds.Changed = func() { go manager.PushEnv(context.WithoutCancel(ctx)) }
	svc := sessionService(db, projectStore, manager, hub)
	svc.Limits = flowdproto.Limits{MemoryMaxBytes: cfg.Session.MemoryBytes, CPUQuotaPercent: cfg.Session.CPUPercent,
		TasksMax: cfg.Session.Tasks}
	svc.Replay = cfg.SessionReplay
	if cfg.SessionReplay {
		slog.Warn("BACKEND_SESSION_REPLAY: sessions play the recorded demo turn, no agent runs")
	}
	svc.CloseTerminals = func(ctx context.Context, api flowdproto.API, id string) error {
		return api.CloseTerminal(ctx, terminals.SessionTerminalID(id, 1))
	}
	// Archives made while a sandbox was stopped are finished when it is up again.
	manager.Warmed = func(projectID string, api flowdproto.API) {
		svc.Sweep(context.WithoutCancel(ctx), projectID, api)
	}
	// A project's sandbox and rows go only when no session of it is in use (archived ones go with them).
	active := func(ctx context.Context, projectID string) (int, error) {
		list, err := svc.Store.List(ctx, sessions.ListFilter{ProjectID: projectID})
		return len(list), err
	}
	manager.ActiveSessions = active
	// Before the followers start: nothing runs yet, so a setup, stop or delete still recorded was cut off.
	if err := manager.Reconcile(ctx); err != nil {
		return err
	}
	if err := svc.Start(ctx); err != nil {
		return err
	}
	publish := func(e apitypes.AppEvent) { hub.Publish(e) }
	areas.Profile = &profile.API{Store: profile.SQL{DB: db}, Changed: creds.Changed, Publish: publish}
	areas.Connections = &credentials.API{Service: creds}
	areas.Projects = &projects.API{Store: projectStore, Sandboxes: manager,
		DefaultBranch:  projects.RemoteDefaultBranch("https://github.com/", &http.Client{Timeout: 30 * time.Second}),
		ActiveSessions: active, Publish: publish}
	areas.Sandboxes = &sandboxes.API{Manager: manager, ActiveSessions: active}
	areas.Sessions = &sessions.API{Service: svc}
	areas.Timeline = &timeline.API{Sessions: svc, Hub: hub}
	areas.Terminals = &terminals.API{Sessions: svc.Store, Projects: projectStore, Sandboxes: manager, Lock: svc.Lock}
	return nil
}

// handler is the server's whole handler: the browser API and the UI, never served to a request that arrives over
// the sandbox network (sandboxes reach Flow's container there at L2), whatever areas are wired.
func handler(cfg config.Config, opts httpapi.Options) http.Handler {
	return sandboxes.OffSandboxNetwork(httpapi.New(opts), cfg.SandboxNetwork.Subnet)
}

// sandboxManager is the sandboxes area: addresses persisted in the sandbox rows (adopted from the instances' NICs
// on first use), and the memory ceiling for all running sandboxes from the configuration.
func sandboxManager(cfg config.Config, db *sql.DB, projectStore projects.SQL, runtime sandbox.Runtime,
	sealer credentials.Sealer, hub *sse.Hub[apitypes.AppEvent]) *sandboxes.Manager {
	// Packed once per Flow process: the same files give the same bytes, and so the same release.
	bundle := sync.OnceValues(func() ([]byte, error) {
		t0 := time.Now()
		b, err := sandboxes.Bundle(cfg.SidecarDir)
		switch {
		case err != nil:
			return nil, fmt.Errorf("sidecar bundle %s: %w", cfg.SidecarDir, err)
		case b == nil:
			slog.Warn("no sidecar bundle: Claude sessions cannot start in sandboxes (replay sessions can)",
				"dir", cfg.SidecarDir)
		default:
			slog.Info("sidecar bundle packed", "dir", cfg.SidecarDir, "bytes", len(b), "ms", time.Since(t0).Milliseconds())
		}
		return b, nil
	})
	m := &sandboxes.Manager{Store: sandboxes.SQL{DB: db}, Projects: projectStore, Runtime: runtime,
		Dialer: flowdclient.NewDialer(), Sealer: sealer, Image: cfg.SandboxImage, Network: cfg.SandboxNetwork,
		Memory: cfg.SandboxMemory, CPUs: cfg.SandboxCPUs, MemoryTotal: cfg.SandboxMemoryTotal,
		HomeVolume: cfg.HomeVolume, Sidecar: bundle,
		Addresses: sandboxes.SQLAddresses{DB: db, Network: cfg.SandboxNetwork},
		Version:   version.String(), Log: slog.Default(), Wait: time.Minute, Poll: 200 * time.Millisecond,
		LockWait: 2 * time.Second,
		Flowd: func(arch string) ([]byte, error) {
			return os.ReadFile(filepath.Join(cfg.FlowdDir, fmt.Sprintf("flowd-linux-%s", arch)))
		}}
	m.Changed = sandboxChanged(m, hub)
	return m
}

// sandboxChanged publishes a sandbox's change. The event carries the state, so the UI shows a setup turning warm
// without asking again; a row that cannot be read publishes nothing (a made-up none would show a warm sandbox as not
// set up), the next change or a reload brings the truth.
func sandboxChanged(m *sandboxes.Manager, hub *sse.Hub[apitypes.AppEvent]) func(projectID string) {
	return func(projectID string) {
		sb, err := m.Lookup(context.Background(), projectID)
		if err != nil {
			slog.Warn("sandbox change not published: its row could not be read", "project", projectID, logging.Err(err))
			return
		}
		hub.Publish(apitypes.AppEvent{Kind: apitypes.AppEventKindSandboxUpdated, ProjectID: projectID, Sandbox: &sb})
	}
}

// sessionService is the sessions area, its follower feeding the metering ledger on the same database.
func sessionService(db *sql.DB, projectStore projects.SQL, sb sessions.Sandboxes,
	hub *sse.Hub[apitypes.AppEvent]) *sessions.Service {
	return &sessions.Service{Store: sessions.SQL{DB: db}, Projects: projectStore, Sandboxes: sb, Hub: hub,
		Log: slog.Default(), Meter: &metering.Recorder{Store: metering.SQL{DB: db}}}
}
