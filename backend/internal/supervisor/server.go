package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/secret"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/ssewire"
)

// Config is /etc/flow/flowd.json (layout.Config), pushed by Flow through the Incus file API, root 0600.
type Config struct {
	Token   string `json:"token"`
	Sandbox string `json:"sandbox"`
	// Listen is flowd's address, ":7433" (flowdproto.Port) by default: the bridge address, reachable from the host
	// only (the network ACL blocks other sandboxes).
	Listen    string `json:"listen,omitempty"`
	CloneBase string `json:"cloneBase,omitempty"`
}

// Heartbeat is the keepalive interval of event streams.
const Heartbeat = 15 * time.Second

// Handler is the flowdproto HTTP server for s, every route behind the bearer token.
func (s *Supervisor) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(flowdproto.RouteHello, func(w http.ResponseWriter, r *http.Request) {
		h, err := s.Hello(r.Context())
		reply(w, h, err)
	})
	mux.HandleFunc(flowdproto.RouteEnv, func(w http.ResponseWriter, r *http.Request) {
		var e flowdproto.Env
		if decode(w, r, &e) {
			reply(w, struct{}{}, s.PutEnv(r.Context(), e))
		}
	})
	mux.HandleFunc(flowdproto.RouteRepos, func(w http.ResponseWriter, r *http.Request) {
		var spec flowdproto.RepoSpec
		if decode(w, r, &spec) {
			st, err := s.EnsureRepo(r.Context(), spec)
			reply(w, st, err)
		}
	})
	mux.HandleFunc(flowdproto.RouteSessionPut, func(w http.ResponseWriter, r *http.Request) {
		var spec flowdproto.SessionSpec
		if decode(w, r, &spec) {
			if spec.ID != r.PathValue("id") {
				reply(w, nil, invalid("session id in path and body differ"))
				return
			}
			st, err := s.EnsureSession(r.Context(), spec)
			reply(w, st, err)
		}
	})
	mux.HandleFunc(flowdproto.RouteSessionGet, func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Session(r.Context(), r.PathValue("id"))
		reply(w, st, err)
	})
	mux.HandleFunc(flowdproto.RouteSessionDelete, func(w http.ResponseWriter, r *http.Request) {
		reply(w, struct{}{}, s.StopSession(r.Context(), r.PathValue("id"), r.URL.Query().Get("remove") == "true"))
	})
	mux.HandleFunc(flowdproto.RouteWorktrees, func(w http.ResponseWriter, r *http.Request) {
		var spec flowdproto.WorktreeSpec
		if decode(w, r, &spec) {
			st, err := s.AddWorktree(r.Context(), r.PathValue("id"), spec)
			reply(w, st, err)
		}
	})
	mux.HandleFunc(flowdproto.RouteCommands, func(w http.ResponseWriter, r *http.Request) {
		var c event.Command
		if decode(w, r, &c) {
			if c.Session != r.PathValue("id") {
				reply(w, nil, invalid("session in path and command differ"))
				return
			}
			res, err := s.Command(r.Context(), c)
			reply(w, res, err)
		}
	})
	mux.HandleFunc(flowdproto.RouteDiff, func(w http.ResponseWriter, r *http.Request) {
		d, err := s.Diff(r.Context(), r.PathValue("id"), r.URL.Query().Get("repo"))
		reply(w, d, err)
	})
	mux.HandleFunc(flowdproto.RouteEvents, s.events)
	s.terminalRoutes(mux)
	s.jobRoutes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(flowdproto.HeaderProtocol, strconv.Itoa(flowdproto.Protocol))
		got, ok := httpx.Bearer(r)
		if !ok || !secret.Match(got, token) {
			writeErr(w, http.StatusUnauthorized, flowdproto.CodeUnauthorized, "")
			return
		}
		if v := r.Header.Get(flowdproto.HeaderProtocol); v != "" {
			if n, err := strconv.Atoi(v); err != nil || n < flowdproto.MinProtocol {
				writeErr(w, http.StatusBadRequest, flowdproto.CodeProtocol, v)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// events serves a session's log after a seq, then follows it; live frames from the host are interleaved without an
// id.
func (s *Supervisor) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.readSpec(id); err != nil {
		reply(w, nil, err)
		return
	}
	after, err := position(r)
	if err != nil {
		reply(w, nil, invalid("position: %v", err))
		return
	}
	sw, err := ssewire.Open(w)
	if err != nil {
		return
	}
	defer sw.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	wake := make(chan struct{}, 1)
	live := make(chan event.Event, 256)
	go s.subscribe(ctx, id, wake, live)
	go func() {
		t := time.NewTicker(Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if sw.Comment() != nil {
					cancel()
					return
				}
			case e := <-live:
				if send(sw, e) != nil {
					cancel()
					return
				}
			}
		}
	}()
	_ = s.log(id).r.Follow(ctx, after, wake, func(e event.Event) bool {
		return send(sw, e) == nil
	})
}

func send(sw *ssewire.Writer, e event.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f := ssewire.Frame{Event: "flow", Data: string(b)}
	if e.Seq > 0 {
		f.ID = strconv.FormatInt(e.Seq, 10)
	}
	return sw.Send(f)
}

// position is the stream's start: the larger of ?after and Last-Event-ID (a client that reconnects by itself keeps
// the ?after it opened with), 0 without either.
func position(r *http.Request) (int64, error) {
	var pos int64
	for _, v := range []string{r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID")} {
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n < 0 {
			err = errors.New("negative")
		}
		if err != nil {
			return 0, err
		}
		pos = max(pos, n)
	}
	return pos, nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, event.MaxLine))
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, flowdproto.CodeInvalid, err.Error())
		return false
	}
	return true
}

func reply(w http.ResponseWriter, v any, err error) {
	if err == nil {
		httpx.WriteJSON(w, http.StatusOK, v)
		return
	}
	var ce *codeError
	switch {
	case errors.As(err, &ce):
		writeErr(w, http.StatusBadRequest, ce.code, err.Error())
	case errors.Is(err, flowdproto.ErrNotFound):
		writeErr(w, http.StatusNotFound, flowdproto.CodeNotFound, err.Error())
	case errors.Is(err, flowdproto.ErrNotPending):
		writeErr(w, http.StatusConflict, flowdproto.CodeNotPending, err.Error())
	case errors.Is(err, errRepoBroken):
		writeErr(w, http.StatusConflict, flowdproto.CodeRepoBroken, err.Error())
	case errors.Is(err, errHostDown):
		writeErr(w, http.StatusConflict, flowdproto.CodeBusy, err.Error())
	default:
		slog.Error("flowd request", "err", err)
		writeErr(w, http.StatusInternalServerError, flowdproto.CodeInternal, err.Error())
	}
}

func writeErr(w http.ResponseWriter, status int, code, detail string) {
	httpx.WriteJSON(w, status, flowdproto.ErrorBody{Code: code, Detail: detail})
}

// Run is flowd: read the config, re-attach to the hosts that survived, serve until ctx ends. A stop is not a
// failure.
func Run(ctx context.Context, log *slog.Logger, version string) error {
	return run(ctx, log, version, layout.Config, nil)
}

func run(ctx context.Context, log *slog.Logger, version, configPath string, o *Options) error {
	log.InfoContext(ctx, "flowd started", "version", version)
	b, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("%s: %w", configPath, err)
	}
	if cfg.Token == "" {
		return fmt.Errorf("%s: no token", configPath)
	}
	if cfg.Listen == "" {
		cfg.Listen = ":" + strconv.Itoa(flowdproto.Port)
	}
	if o == nil {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return err
		}
		o = &Options{Units: Systemd{Run: ExecRunner{}}, Run: ExecRunner{}, Exe: exe}
	}
	o.Version, o.Sandbox, o.CloneBase, o.Log = version, cfg.Sandbox, cfg.CloneBase, log
	s := New(*o)
	if err := s.prepare(configPath); err != nil {
		return err
	}
	s.Reattach(ctx)
	s.ReattachTerminals(ctx)
	l, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(cfg.Token), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.InfoContext(ctx, "flowd listening", "addr", l.Addr().String(), "sandbox", cfg.Sandbox)
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.InfoContext(context.WithoutCancel(ctx), "flowd stopping", "cause", context.Cause(ctx).Error())
	return nil
}
