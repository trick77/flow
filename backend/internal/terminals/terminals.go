package terminals

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/coder/websocket"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/sessions"
	"github.com/trick77/flow/internal/terminals/wsterm"
)

// SessionStore is what terminals read of sessions.
type SessionStore interface {
	Get(ctx context.Context, id string) (sessions.Session, error)
}

// ProjectStore is what terminals read of projects.
type ProjectStore interface {
	Get(ctx context.Context, id string) (projects.Project, error)
}

// API implements httpapi.Terminals. A terminal's id names its session (or, for one in a project's home, its
// project) and a slot, so the id alone leads back to the sandbox after a Flow restart: Flow keeps no terminal
// state. One slot per session for now; the id format leaves room for more.
type API struct {
	Sessions  SessionStore
	Projects  ProjectStore
	Sandboxes sessions.Sandboxes
	// Lock is the sessions service's per-session lock (sessions.Service.Lock), held from the archived check to the
	// terminal's start so an archive cannot pass in between; nil locks nothing.
	Lock func(sessionID string) func()
}

var _ httpapi.Terminals = (*API)(nil)

// Id prefixes: a session's terminal, a project home's.
const (
	sessionPrefix = "s-"
	projectPrefix = "p-"
)

// SessionTerminalID is the id of a session's terminal in slot (from 1).
func SessionTerminalID(sessionID string, slot int) string {
	return sessionPrefix + sessionID + "-" + strconv.Itoa(slot)
}

// ProjectTerminalID is the id of a terminal in a project's sandbox home.
func ProjectTerminalID(projectID string, slot int) string {
	return projectPrefix + projectID + "-" + strconv.Itoa(slot)
}

// target is what a terminal id names.
type target struct {
	sessionID, projectID string
}

// parseID splits an id; ok is false for one this package never made (and for one flowd would refuse as a name).
func parseID(id string) (target, bool) {
	if !layout.ValidName(id) {
		return target{}, false
	}
	i := strings.LastIndexByte(id, '-')
	if i < 0 {
		return target{}, false
	}
	if n, err := strconv.Atoi(id[i+1:]); err != nil || n < 1 || strconv.Itoa(n) != id[i+1:] {
		return target{}, false
	}
	switch rest := id[:i]; {
	case strings.HasPrefix(rest, sessionPrefix) && len(rest) > len(sessionPrefix):
		return target{sessionID: rest[len(sessionPrefix):]}, true
	case strings.HasPrefix(rest, projectPrefix) && len(rest) > len(projectPrefix):
		return target{projectID: rest[len(projectPrefix):]}, true
	}
	return target{}, false
}

// project finds the project whose sandbox holds a terminal; not_found for an id that names nothing. archived: the
// terminal belongs to an archived session.
func (a *API) project(ctx context.Context, id string) (projectID string, archived bool, err error) {
	t, ok := parseID(id)
	if !ok {
		return "", false, apitypes.Fail(apitypes.ErrorCodeNotFound, "")
	}
	if t.sessionID != "" {
		se, err := a.Sessions.Get(ctx, t.sessionID)
		if errors.Is(err, sessions.ErrNotFound) {
			return "", false, apitypes.Fail(apitypes.ErrorCodeNotFound, "")
		}
		return se.ProjectID, se.ArchivedAt != nil, err
	}
	if _, err := a.Projects.Get(ctx, t.projectID); err != nil {
		if errors.Is(err, projects.ErrNotFound) {
			return "", false, apitypes.Fail(apitypes.ErrorCodeNotFound, "")
		}
		return "", false, err
	}
	return t.projectID, false, nil
}

// stopped reports whether a no-start lookup failed because the sandbox does not run (or does not exist), the only
// failures that prove its terminals are gone. Store, runtime and dial errors prove nothing, nor does
// sandboxes.ErrMayRun (not warm by the records, its instance maybe running).
func stopped(err error) bool {
	return errors.Is(err, sandboxes.ErrNotWarm) || errors.Is(err, sandboxes.ErrNotFound)
}

// isWebSocket reports whether r asks for a WebSocket upgrade.
func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// CreateTerminal implements httpapi.Terminals: create or attach. The sandbox is brought up as for a message
// (Sandboxes.Ensure starts a stopped one); the session's agent is not started: a shell needs the sandbox and the
// session directory, not the agent.
func (a *API) CreateTerminal(w http.ResponseWriter, r *http.Request) {
	var in apitypes.TerminalCreate
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if !flowdproto.ValidTerminalSize(in.Cols, in.Rows) {
		field := "rows"
		if !flowdproto.ValidTerminalSize(in.Cols, 1) {
			field = "cols"
		}
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: field})
		return
	}
	ctx := r.Context()
	out := apitypes.Terminal{ProjectID: in.ProjectID}
	spec := flowdproto.TerminalSpec{Cols: in.Cols, Rows: in.Rows}
	if in.SessionID != "" {
		// The session must exist before its lock is taken (a lock per made-up id would stay); the archived check is
		// read again under the lock.
		se, err := a.Sessions.Get(ctx, in.SessionID)
		if err == nil && a.Lock != nil {
			defer a.Lock(in.SessionID)()
			se, err = a.Sessions.Get(ctx, in.SessionID)
		}
		if errors.Is(err, sessions.ErrNotFound) || err == nil && se.ProjectID != in.ProjectID {
			httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "sessionId"})
			return
		}
		if err != nil {
			httpapi.WriteError(w, err)
			return
		}
		if se.ArchivedAt != nil {
			httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeSessionArchived, ""))
			return
		}
		spec.ID, spec.Session, spec.Dir = SessionTerminalID(se.ID, 1), se.ID, layout.SessionDir(se.Name)
		if se.Provider == string(event.ProviderClaude) {
			spec.ClaudeAuth = event.ClaudeAuth(se.ClaudeAuth) // the shell gets what the session's agent gets
		}
		out.SessionID = se.ID
	} else {
		if _, err := a.Projects.Get(ctx, in.ProjectID); err != nil {
			if errors.Is(err, projects.ErrNotFound) {
				err = &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "projectId"}
			}
			httpapi.WriteError(w, err)
			return
		}
		spec.ID, spec.Dir = ProjectTerminalID(in.ProjectID, 1), layout.Home
	}
	if !layout.ValidName(spec.ID) {
		field := "projectId"
		if spec.Session != "" {
			field = "sessionId"
		}
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: field})
		return
	}
	restarted := false
	for attempt := 0; ; attempt++ {
		api, err := a.Sandboxes.Ensure(ctx, in.ProjectID)
		if err != nil {
			httpapi.WriteError(w, unavailable(err))
			return
		}
		st, err := api.EnsureTerminal(ctx, spec)
		if errors.Is(err, flowdproto.ErrUnavailable) && attempt == 0 {
			a.Sandboxes.Forget(in.ProjectID) // a sandbox that stopped or restarted behind Flow's back
			continue
		}
		if err == nil && st.Exited && !restarted {
			// The old shell ended but its unit is still winding down (a flowd that reports it as it is): close it and
			// start the new one, rather than answer "created" for the ended terminal.
			restarted = true
			if err := api.CloseTerminal(ctx, spec.ID); err != nil {
				httpapi.WriteError(w, unavailable(err))
				return
			}
			continue
		}
		if errors.Is(err, flowdproto.ErrNotFound) {
			// The session directory is gone (the sandbox was rebuilt): nothing a retry fixes.
			httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeConflict, "session directory missing"))
			return
		}
		if err != nil {
			httpapi.WriteError(w, unavailable(err))
			return
		}
		// flowd opens a session's terminal where the session's agent works, which may be below the directory asked.
		if st.Dir != "" {
			out.Cwd = st.Dir
		}
		break
	}
	out.ID = spec.ID
	if out.Cwd == "" {
		out.Cwd = spec.Dir
	}
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

// unavailable passes API errors through and makes everything else sandbox_unavailable.
func unavailable(err error) error {
	var ae *apitypes.Error
	if errors.As(err, &ae) {
		return err
	}
	return apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
}

// AttachTerminal implements httpapi.Terminals: the WebSocket (wsterm's protocol), proxied to flowd. It never starts
// a sandbox (a reconnecting tab must not wake a stopped one): a sandbox that is not warm, and a terminal that is
// gone, are told with close codes after the upgrade, since a browser cannot read a refused handshake. The Guard has
// already admitted the request: the signed-in owner, and the auth package's same-origin rule, which refuses a
// cross-origin Origin on this GET.
func (a *API) AttachTerminal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("terminalId")
	projectID, archived, err := a.project(r.Context(), id)
	var ae *apitypes.Error
	switch {
	case errors.As(err, &ae) && ae.Code == apitypes.ErrorCodeNotFound && isWebSocket(r), err == nil && archived:
		// An id that names nothing, or an archived session's terminal: gone for good, told as a close code.
		wsterm.Refuse(w, r, wsterm.CloseGone, "terminal gone")
		return
	case err != nil:
		httpapi.WriteError(w, err)
		return
	}
	var size flowdproto.TerminalSize
	q := r.URL.Query()
	if c, err := strconv.Atoi(q.Get("cols")); err == nil {
		if n, err := strconv.Atoi(q.Get("rows")); err == nil && flowdproto.ValidTerminalSize(c, n) {
			size = flowdproto.TerminalSize{Cols: c, Rows: n}
		}
	}
	api, err := a.Sandboxes.Warm(r.Context(), projectID)
	var tc flowdproto.TerminalConn
	if err == nil {
		tc, err = api.AttachTerminal(r.Context(), id, size)
		if errors.Is(err, flowdproto.ErrUnavailable) {
			a.Sandboxes.Forget(projectID)
			if api, err = a.Sandboxes.Warm(r.Context(), projectID); err == nil {
				tc, err = api.AttachTerminal(r.Context(), id, size)
			}
		}
	}
	var exit *flowdproto.TerminalExit
	switch {
	case stopped(err), errors.Is(err, sandboxes.ErrMayRun):
		// Not usable until it is started again (a failed sandbox whose shell may run is not reachable either).
		wsterm.Refuse(w, r, wsterm.CloseNoSandbox, "sandbox not running")
		return
	case errors.As(err, &exit):
		if c, err := wsterm.Accept(w, r); err == nil {
			wsterm.Exit(r.Context(), c, exit.Code)
		}
		return
	case errors.Is(err, flowdproto.ErrNotFound):
		wsterm.Refuse(w, r, wsterm.CloseGone, "terminal not running")
		return
	case err != nil:
		// Store, runtime or dial trouble: no proof the terminal is gone; the browser tries again.
		wsterm.Refuse(w, r, websocket.StatusInternalError, "sandbox unreachable")
		return
	}
	c, err := wsterm.Accept(w, r)
	if err != nil {
		_ = tc.Close()
		return
	}
	wsterm.Serve(r.Context(), c, tc)
}

// CloseTerminal implements httpapi.Terminals: stops the terminal (an archived session's too). A sandbox that is not
// running has no terminals to stop, so it is not started for this; any other failure to reach it is reported, since
// the shell (with the session's credential) may still run.
func (a *API) CloseTerminal(w http.ResponseWriter, r *http.Request) {
	projectID, _, err := a.project(r.Context(), r.PathValue("terminalId"))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	api, err := a.Sandboxes.Warm(r.Context(), projectID)
	if stopped(err) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		httpapi.WriteError(w, unavailable(err))
		return
	}
	if err := api.CloseTerminal(r.Context(), r.PathValue("terminalId")); err != nil {
		httpapi.WriteError(w, unavailable(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
