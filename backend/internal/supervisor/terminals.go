package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/terminals/term"
	"github.com/trick77/flow/internal/terminals/wsterm"
)

// Terminals: each is a unit flow-term-<id> running `flowd term` (internal/terminals/term) as the agent user, with a
// PTY, the scrollback ring and layout.TerminalSocket. flowd keeps no state of its own about them: after a restart it
// finds them by unit name and asks each socket; an ended one left its exit status beside its socket.

// Default limits of a terminal unit when the spec names none: the owner's builds and installs run here.
const (
	termMemoryMax = 4 << 30
	termCPUQuota  = 200
	termTasksMax  = 2048
)

// A terminal's environment is the session's, built by the same code: the unit gets an EnvironmentFile from
// writeSessionEnv (git identity, the one credential of the session's Claude mode, root 0600 on tmpfs, never on the
// command line), and `flowd term` hands its shell what claude.Env passes a session's agent (the allow-list, the
// home-volume locations, that credential; never the MCP token). A terminal without a Claude mode (another
// provider, or the project's home terminal for signing in) is built as subscription_login: no credential at all,
// so a login made there lands on the home volume, where sessions find it.

// termAuth is the Claude mode a terminal's environment is built for.
func termAuth(t flowdproto.TerminalSpec) event.ClaudeAuth {
	if t.ClaudeAuth == "" {
		return event.ClaudeAuthSubscriptionLogin
	}
	return t.ClaudeAuth
}

// validTermDir: a terminal is asked for in the agent's home or in a session directory, nowhere else (flowd moves a
// session's terminal into the agent's working directory).
func validTermDir(dir string) bool {
	return dir == layout.Home || path.Dir(dir) == layout.Sessions && layout.ValidName(path.Base(dir))
}

func (s *Supervisor) termSocket(id string) string { return s.p(layout.TerminalSocket(id)) }
func (s *Supervisor) termExit(id string) string   { return s.p(layout.TerminalExitFile(id)) }

// EnsureTerminal implements flowdproto.API: a running terminal is reported as it is; otherwise one is started (also
// after its shell exited: that is the restart).
func (s *Supervisor) EnsureTerminal(ctx context.Context, t flowdproto.TerminalSpec) (flowdproto.TerminalState, error) {
	if !layout.ValidName(t.ID) || t.Session != "" && !layout.ValidName(t.Session) {
		return flowdproto.TerminalState{}, invalid("terminal id %q or session %q", t.ID, t.Session)
	}
	if !validTermDir(t.Dir) || !flowdproto.ValidTerminalSize(t.Cols, t.Rows) {
		return flowdproto.TerminalState{}, invalid("terminal dir %q or size %dx%d", t.Dir, t.Cols, t.Rows)
	}
	if t.ClaudeAuth != "" && !slices.Contains(event.ClaudeAuthValues, t.ClaudeAuth) {
		return flowdproto.TerminalState{}, invalid("terminal claudeAuth %q", t.ClaudeAuth)
	}
	if t.Session != "" {
		// A session's terminal opens where its agent works: the session's one worktree, or the session directory.
		// Flow knows only the session directory; the spec holds the choice.
		if sp, err := s.readSpec(t.Session); err == nil && t.Dir == sp.Dir {
			t.Dir = sp.AgentDir()
		}
	}
	unit := layout.TerminalUnit(t.ID)
	defer s.lock(unit)()
	st, err := s.o.Units.State(ctx, unit)
	if err != nil {
		return flowdproto.TerminalState{}, err
	}
	if st == flowdproto.UnitActive || st == flowdproto.UnitActivating {
		cur, err := s.terminal(ctx, t.ID)
		if err != nil || !cur.Exited {
			return cur, err
		}
		// The shell has ended and the unit is only writing out its clients: stop it and start a new one below.
		if err := s.o.Units.Stop(ctx, unit); err != nil {
			return flowdproto.TerminalState{}, err
		}
		st = flowdproto.UnitMissing
	}
	if err := s.envReady(); err != nil { // its environment is built from the set, like a session's
		return flowdproto.TerminalState{}, err
	}
	if fi, err := os.Stat(s.p(t.Dir)); err != nil || !fi.IsDir() {
		return flowdproto.TerminalState{}, fmt.Errorf("%w: directory %s", flowdproto.ErrNotFound, t.Dir)
	}
	if st != flowdproto.UnitMissing {
		// An ended unit is collected right after it ends; one still being collected (or failed) is cleared, or
		// systemd-run refuses the name.
		if err := s.o.Units.Stop(ctx, unit); err != nil {
			return flowdproto.TerminalState{}, err
		}
	}
	sock := s.termSocket(t.ID)
	if err := s.mkdirAgent(filepath.Dir(sock)); err != nil {
		return flowdproto.TerminalState{}, err
	}
	exitFile := s.termExit(t.ID)
	for _, f := range []string{exitFile, sock} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return flowdproto.TerminalState{}, err
		}
	}
	auth := termAuth(t)
	// The terminal's own file (its id is no session's): the session's agent keeps its own and its MCP token.
	envFile, err := s.writeSessionEnv(flowdproto.SessionSpec{ID: t.ID, Provider: event.ProviderClaude, ClaudeAuth: auth})
	if err != nil {
		return flowdproto.TerminalState{}, err
	}
	argv := []string{s.o.Exe, "term", "--id", t.ID, "--dir", s.p(t.Dir), "--socket", sock, "--exit-file", exitFile,
		"--cols", strconv.Itoa(t.Cols), "--rows", strconv.Itoa(t.Rows), "--claude-auth", string(auth)}
	if t.Session != "" {
		argv = append(argv, "--session", t.Session)
	}
	if err := s.o.Units.Start(ctx, unit, argv, append(termProps(t.Limits), "EnvironmentFile="+envFile)); err != nil {
		return flowdproto.TerminalState{}, err
	}
	deadline := time.Now().Add(s.o.HostWait)
	for {
		if _, err := term.Query(ctx, sock); err == nil {
			break
		} else if time.Now().After(deadline) {
			return flowdproto.TerminalState{}, fmt.Errorf("terminal %s did not come up: %w", t.ID, err)
		}
		select {
		case <-ctx.Done():
			return flowdproto.TerminalState{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	s.o.Log.Info("terminal started", "terminal", t.ID, "session", t.Session, "unit", unit)
	return s.terminal(ctx, t.ID)
}

// termProps are a terminal unit's limits and environment.
func termProps(l flowdproto.Limits) []string {
	mem, cpu, tasks := l.MemoryMaxBytes, l.CPUQuotaPercent, l.TasksMax
	if mem <= 0 {
		mem = termMemoryMax
	}
	if cpu <= 0 {
		cpu = termCPUQuota
	}
	if tasks <= 0 {
		tasks = termTasksMax
	}
	return []string{fmt.Sprintf("MemoryMax=%d", mem), fmt.Sprintf("CPUQuota=%d%%", cpu),
		"TasksMax=" + strconv.Itoa(tasks), "Environment=LANG=C.UTF-8"}
}

// terminal is a terminal's state: its own answer while it runs, else the exit status it left.
func (s *Supervisor) terminal(ctx context.Context, id string) (flowdproto.TerminalState, error) {
	return s.terminalState(ctx, ctx, id)
}

// terminalState is terminal with qctx bounding the question to its socket only: one that does not answer within it
// is reported by its unit state.
func (s *Supervisor) terminalState(ctx, qctx context.Context, id string) (flowdproto.TerminalState, error) {
	st, err := s.o.Units.State(ctx, layout.TerminalUnit(id))
	if err != nil {
		return flowdproto.TerminalState{}, err
	}
	sock := s.termSocket(id)
	if info, err := term.Query(qctx, sock); err == nil {
		return flowdproto.TerminalState{ID: id, Session: info.Session, Unit: st, Dir: s.unrooted(info.Dir),
			Exited: info.Exited, ExitCode: info.Code}, nil
	}
	if e, ok := term.ReadExit(s.termExit(id)); ok {
		return flowdproto.TerminalState{ID: id, Unit: st, Exited: true, ExitCode: e.Code}, nil
	}
	if st == flowdproto.UnitMissing {
		return flowdproto.TerminalState{}, fmt.Errorf("%w: terminal %s", flowdproto.ErrNotFound, id)
	}
	return flowdproto.TerminalState{ID: id, Unit: st}, nil
}

// unrooted takes Options.Root off a path (tests run under a temporary root).
func (s *Supervisor) unrooted(p string) string {
	if s.o.Root == "" {
		return p
	}
	if rel, ok := strings.CutPrefix(p, filepath.Clean(s.o.Root)); ok {
		return rel
	}
	return p
}

// Terminal is one terminal's state (GET has no route: Flow reads it from EnsureTerminal and Hello).
func (s *Supervisor) Terminal(ctx context.Context, id string) (flowdproto.TerminalState, error) {
	if !layout.ValidName(id) {
		return flowdproto.TerminalState{}, invalid("terminal id %q", id)
	}
	return s.terminal(ctx, id)
}

// Terminals lists the terminal units and what each says about itself.
func (s *Supervisor) Terminals(ctx context.Context) ([]flowdproto.TerminalState, error) {
	return s.terminals(ctx, ctx)
}

// terminals is Terminals with qctx bounding the questions to the sockets (terminalState).
func (s *Supervisor) terminals(ctx, qctx context.Context) ([]flowdproto.TerminalState, error) {
	units, err := s.o.Units.List(ctx, "flow-term-*")
	if err != nil {
		return nil, err
	}
	out := []flowdproto.TerminalState{}
	for _, u := range units {
		id := strings.TrimSuffix(strings.TrimPrefix(u, "flow-term-"), ".service")
		if !layout.ValidName(id) {
			continue
		}
		if st, err := s.terminalState(ctx, qctx, id); err == nil {
			out = append(out, st)
		}
	}
	return out, nil
}

// ReattachTerminals finds the terminals that survived flowd's restart; nothing needs re-establishing, since every
// attach dials the terminal's socket afresh.
func (s *Supervisor) ReattachTerminals(ctx context.Context) []flowdproto.TerminalState {
	list, err := s.Terminals(ctx)
	if err != nil {
		s.o.Log.Warn("listing terminal units", "err", err)
		return nil
	}
	for _, t := range list {
		s.o.Log.Info("reattached terminal", "terminal", t.ID, "session", t.Session, "unit", t.Unit, "exited", t.Exited)
	}
	return list
}

// CloseTerminal implements flowdproto.API: the unit is stopped (its shell hung up) and the exit status dropped.
func (s *Supervisor) CloseTerminal(ctx context.Context, id string) error {
	if !layout.ValidName(id) {
		return invalid("terminal id %q", id)
	}
	unit := layout.TerminalUnit(id)
	defer s.lock(unit)()
	if err := s.o.Units.Stop(ctx, unit); err != nil {
		return err
	}
	for _, f := range []string{s.termExit(id), s.termSocket(id), s.p(layout.SessionEnvFile(id))} {
		// Paths from layout and a terminal id checked with layout.ValidName above.
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // see above
			return err
		}
	}
	return nil
}

// AttachTerminal implements flowdproto.API: a connection to the terminal's socket.
func (s *Supervisor) AttachTerminal(ctx context.Context, id string, size flowdproto.TerminalSize) (flowdproto.TerminalConn, error) {
	if !layout.ValidName(id) {
		return nil, invalid("terminal id %q", id)
	}
	var sz *term.Size
	if flowdproto.ValidTerminalSize(size.Cols, size.Rows) {
		sz = &term.Size{Cols: size.Cols, Rows: size.Rows}
	}
	return s.attach(ctx, id, sz)
}

func (s *Supervisor) attach(ctx context.Context, id string, size *term.Size) (flowdproto.TerminalConn, error) {
	sock := s.termSocket(id)
	c, err := term.Attach(ctx, sock, size)
	if err == nil {
		return c, nil
	}
	if e, ok := term.ReadExit(s.termExit(id)); ok {
		return nil, &flowdproto.TerminalExit{Code: e.Code}
	}
	return nil, fmt.Errorf("%w: terminal %s", flowdproto.ErrNotFound, id)
}

// terminalRoutes mounts the terminal routes on mux.
func (s *Supervisor) terminalRoutes(mux *http.ServeMux) {
	mux.HandleFunc(flowdproto.RouteTerminalPut, func(w http.ResponseWriter, r *http.Request) {
		var spec flowdproto.TerminalSpec
		if decode(w, r, &spec) {
			if spec.ID != r.PathValue("id") {
				reply(w, nil, invalid("terminal id in path and body differ"))
				return
			}
			st, err := s.EnsureTerminal(r.Context(), spec)
			reply(w, st, err)
		}
	})
	mux.HandleFunc(flowdproto.RouteTerminalDelete, func(w http.ResponseWriter, r *http.Request) {
		reply(w, struct{}{}, s.CloseTerminal(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc(flowdproto.RouteTerminalAttach, s.terminalAttach)
}

// terminalAttach serves the WebSocket Flow attaches with (wsterm's protocol). ?cols=&rows= resize the terminal
// before its scrollback is sent, so the replay is laid out for the window it lands in.
func (s *Supervisor) terminalAttach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !layout.ValidName(id) {
		reply(w, nil, invalid("terminal id %q", id))
		return
	}
	var size *term.Size
	if c, err := strconv.Atoi(r.URL.Query().Get("cols")); err == nil {
		if rows, err := strconv.Atoi(r.URL.Query().Get("rows")); err == nil && flowdproto.ValidTerminalSize(c, rows) {
			size = &term.Size{Cols: c, Rows: rows}
		}
	}
	tc, err := s.attach(r.Context(), id, size)
	var exit *flowdproto.TerminalExit
	switch {
	case errors.As(err, &exit):
		if c, err := wsterm.Accept(w, r); err == nil {
			wsterm.Exit(r.Context(), c, exit.Code)
		}
		return
	case err != nil:
		reply(w, nil, err)
		return
	}
	c, err := wsterm.Accept(w, r)
	if err != nil {
		_ = tc.Close()
		return
	}
	wsterm.Serve(r.Context(), c, tc)
}
