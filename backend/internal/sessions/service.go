package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/trick77/ship/kit/sse"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/metering"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/store"
)

// Sandboxes is what sessions need of the sandboxes area.
type Sandboxes interface {
	// Ensure brings the project's sandbox to warm and returns its flowd.
	Ensure(ctx context.Context, projectID string) (flowdproto.API, error)
	// Warm returns the flowd of a sandbox that is warm by Flow's records, never starting one: sandboxes.ErrNotWarm
	// (or sandboxes.ErrNotFound, no sandbox yet) when it does not run, sandboxes.ErrMayRun when it is not warm but
	// its instance may run.
	Warm(ctx context.Context, projectID string) (flowdproto.API, error)
	// Forget drops a cached connection after it failed.
	Forget(projectID string)
}

// Meter takes each event of a session's log before the follower records its progress: one handed on just before
// a crash is handed on again after it, so a Meter is idempotent by session and seq. An error stops this pass of
// the follower, which retries from the same event. Forget ends a session's follow. *metering.Recorder is one.
type Meter interface {
	Observe(ctx context.Context, se metering.Session, e event.Event) error
	Forget(sessionID string)
}

// Service is the sessions area's core: creation with worktrees, the host of a session, and following every
// session's log to mirror its status into the database and onto the app event stream.
type Service struct {
	Store     Store
	Projects  projects.Store
	Sandboxes Sandboxes
	Hub       *sse.Hub[apitypes.AppEvent]
	Log       *slog.Logger
	// Retry is the wait before a follower dials again.
	Retry time.Duration
	// Meter, when set, takes every event the follower reads (internal/metering).
	Meter Meter
	// Limits bound every session host unit (BACKEND_SESSION_*), sent with each spec.
	Limits flowdproto.Limits
	// Replay has every host start play the recorded demo turn instead of the agent (BACKEND_SESSION_REPLAY):
	// read at each host start, so a session that ran replayed turns runs its agent once the setting is off.
	Replay bool
	// CloseTerminals closes a session's terminals in its sandbox (the terminals area's ids) when the session is
	// stopped or archived: a shell left running would keep the session's credential. nil closes none.
	CloseTerminals func(ctx context.Context, api flowdproto.API, sessionID string) error

	mu      sync.Mutex
	base    context.Context
	follows map[string]*follower
	locks   map[string]*sessionLock
}

// follower is one session's running follow; cancel ends it (Archive).
type follower struct{ cancel context.CancelFunc }

// Lock serializes, per session, whatever starts something of the session in its sandbox (Host, the creation of its
// terminal) with Archive: a start that read the row before an archive would otherwise recreate the host, worktrees
// or shell after it. Held across a sandbox bring-up. Returns the unlock.
// An entry lives while someone holds or waits for it: the last unlock drops it, so the map holds no lock of a
// session nobody works on (an archived one, an id that named nothing).
func (s *Service) Lock(id string) func() {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*sessionLock{}
	}
	l, ok := s.locks[id]
	if !ok {
		l = &sessionLock{}
		s.locks[id] = l
	}
	l.users++
	s.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.mu.Lock()
		if l.users--; l.users == 0 {
			delete(s.locks, id)
		}
		s.mu.Unlock()
	}
}

// sessionLock is one session's lock and how many hold or wait for it (guarded by Service.mu).
type sessionLock struct {
	mu    sync.Mutex
	users int
}

// Start follows every session that is not archived, until ctx ends.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
	list, err := s.Store.List(ctx, ListFilter{})
	if err != nil {
		return err
	}
	for _, se := range list {
		s.Follow(se.ID)
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug is a session directory name from free text: lower case words joined by dashes, at most 40 bytes.
func Slug(text string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(text), "-"), "-")
	for len(s) > 40 {
		i := strings.LastIndexByte(s[:40], '-')
		if i <= 0 {
			s = s[:40]
			break
		}
		s = s[:i]
	}
	if s == "" {
		return "session"
	}
	return s
}

// repoDir is the repository's directory; project creation refused names without one (layout.RepoDir).
func repoDir(full string) string {
	dir, _ := layout.RepoDir(full)
	return dir
}

// cut shortens s to at most n bytes on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n])
}

func invalid(field string) error {
	return &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: field}
}

// Create makes a session: its row and worktrees, the host in the sandbox, and its first turn when a message is
// given.
func (s *Service) Create(ctx context.Context, in apitypes.SessionCreate) (apitypes.Session, error) {
	if !slices.Contains(event.ProviderValues, event.Provider(in.Provider)) {
		return apitypes.Session{}, invalid("provider")
	}
	if !slices.Contains(event.PermissionModeValues, event.PermissionMode(in.Mode)) {
		return apitypes.Session{}, invalid("mode")
	}
	if in.ClaudeAuth != "" && !slices.Contains(event.ClaudeAuthValues, event.ClaudeAuth(in.ClaudeAuth)) {
		return apitypes.Session{}, invalid("claudeAuth")
	}
	if strings.TrimSpace(in.Model) == "" {
		return apitypes.Session{}, invalid("model")
	}
	if in.Message != nil && (in.Message.ClientID == "" || strings.TrimSpace(in.Message.Text) == "" ||
		!event.TextFits(in.Message.Text)) {
		return apitypes.Session{}, invalid("message")
	}
	if _, err := s.Projects.Get(ctx, in.ProjectID); err != nil {
		if errors.Is(err, projects.ErrNotFound) {
			return apitypes.Session{}, invalid("projectId")
		}
		return apitypes.Session{}, err
	}
	repos, err := s.Projects.Repos(ctx, in.ProjectID)
	if err != nil {
		return apitypes.Session{}, err
	}
	var chosen []projects.Repo
	for _, id := range in.RepoIds {
		i := slices.IndexFunc(repos, func(r projects.Repo) bool { return r.ID == id })
		if i < 0 || slices.ContainsFunc(chosen, func(r projects.Repo) bool { return r.ID == id }) {
			return apitypes.Session{}, invalid("repoIds")
		}
		chosen = append(chosen, repos[i])
	}
	if len(chosen) == 0 {
		return apitypes.Session{}, invalid("repoIds")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" && in.Message != nil {
		title, _, _ = strings.Cut(strings.TrimSpace(in.Message.Text), "\n")
	}
	title = cut(title, 80)
	base := Slug(title)
	name, suffix, err := s.uniqueName(ctx, in.ProjectID, base)
	if err != nil {
		return apitypes.Session{}, err
	}
	se := Session{ID: store.NewID(), ProjectID: in.ProjectID, Title: title, Provider: string(in.Provider),
		Model: in.Model, Mode: string(in.Mode), Status: string(event.SessionStatusIdle)}
	if in.Provider == apitypes.ProviderClaude {
		se.ClaudeAuth = string(apitypes.ClaudeAuthSubscriptionToken)
		if in.ClaudeAuth != "" {
			se.ClaudeAuth = string(in.ClaudeAuth)
		}
	}
	// The name was chosen outside the insert's transaction: a creation with the same title at the same moment may
	// take it first (the unique index on project and name says so). Then the next suffix after the chosen one (those
	// before it are known to be taken), a few times.
	for attempt := 1; ; attempt++ {
		se.Name, se.Branch = name, "flow/"+name
		var wts []Worktree
		for _, r := range chosen {
			wts = append(wts, Worktree{ID: store.NewID(), SessionID: se.ID, RepoID: r.ID, Branch: se.Branch,
				Path: layout.Worktree(name, repoDir(r.FullName)), BaseRef: "origin/" + r.DefaultBranch})
		}
		err := s.Store.Create(ctx, se, wts)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "sessions.name") || attempt >= 10 {
			return apitypes.Session{}, err
		}
		suffix++
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
	// Until the session is usable a failure takes it back out: in the sandbox first (host unit, worktrees, the
	// directory's claim; EnsureSession may have got that far before failing), through the no-start path so a
	// stopped sandbox stays stopped, then the row. A retry then neither finds a half-made session nor its leftovers.
	undo := func(err error) (apitypes.Session, error) {
		ctx := context.WithoutCancel(ctx)
		if api, werr := s.Sandboxes.Warm(ctx, se.ProjectID); werr == nil {
			if serr := s.stop(ctx, api, se.ID, true); serr != nil {
				s.Log.Warn("removing a failed session in its sandbox", "session", se.ID, "err", serr)
			}
		}
		if derr := s.Store.Delete(ctx, se.ID); derr != nil {
			s.Log.Error("removing a failed session", "session", se.ID, "err", derr)
		}
		return apitypes.Session{}, err
	}
	if _, err := s.Host(ctx, se.ID); err != nil {
		return undo(err)
	}
	if in.Message != nil {
		if _, err := s.Command(ctx, se.ID, in.Message.ClientID, event.CmdMessageSend, event.MessageSend{
			MessageID: in.Message.ClientID, Text: in.Message.Text, Origin: event.MessageOriginUser}); err != nil {
			return undo(err)
		}
	}
	s.Follow(se.ID)
	se, err = s.Store.Get(ctx, se.ID)
	if err != nil {
		return apitypes.Session{}, err
	}
	return s.Describe(ctx, se)
}

// uniqueName is the first free name of base, base-2, base-3, …, and its suffix (1 for base itself).
func (s *Service) uniqueName(ctx context.Context, projectID, base string) (string, int, error) {
	taken := map[string]bool{}
	for _, archived := range []bool{false, true} {
		list, err := s.Store.List(ctx, ListFilter{ProjectID: projectID, Archived: archived})
		if err != nil {
			return "", 0, err
		}
		for _, se := range list {
			taken[se.Name] = true
		}
	}
	name, suffix := base, 1
	for taken[name] {
		suffix++
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
	return name, suffix, nil
}

// Spec is what flowd needs to run a session's host.
func (s *Service) Spec(ctx context.Context, se Session) (flowdproto.SessionSpec, error) {
	wts, err := s.Store.Worktrees(ctx, se.ID)
	if err != nil {
		return flowdproto.SessionSpec{}, err
	}
	spec := flowdproto.SessionSpec{ID: se.ID, Name: se.Name, Provider: event.Provider(se.Provider),
		ClaudeAuth: event.ClaudeAuth(se.ClaudeAuth), Model: se.Model, Mode: event.PermissionMode(se.Mode),
		ModeCap: event.PermissionMode(se.ModeCap), ProviderSessionID: se.ProviderSessionID,
		Worktrees: []flowdproto.WorktreeSpec{}, Limits: s.Limits, Replay: s.Replay}
	if se.ParentID != nil {
		spec.ParentID = *se.ParentID
	}
	for _, w := range wts {
		r, err := s.Projects.Repo(ctx, w.RepoID)
		if err != nil {
			return spec, err
		}
		spec.Worktrees = append(spec.Worktrees, flowdproto.WorktreeSpec{Repo: r.FullName, Branch: w.Branch, BaseRef: w.BaseRef})
	}
	return spec, nil
}

// Host makes sure the session's sandbox is warm and its host runs, and returns the sandbox's flowd. Under the
// session's Lock: an Archive waits for it, or it finds the session archived.
func (s *Service) Host(ctx context.Context, id string) (flowdproto.API, error) {
	defer s.Lock(id)()
	se, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if se.ArchivedAt != nil {
		return nil, apitypes.Fail(apitypes.ErrorCodeSessionArchived, "")
	}
	spec, err := s.Spec(ctx, se)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		api, err := s.Sandboxes.Ensure(ctx, se.ProjectID)
		if err != nil {
			return nil, err
		}
		st, err := api.EnsureSession(ctx, spec)
		if errors.Is(err, flowdproto.ErrUnavailable) && attempt == 0 {
			s.Sandboxes.Forget(se.ProjectID) // a sandbox that stopped or restarted behind Flow's back
			continue
		}
		if errors.Is(err, flowdproto.ErrNotImplemented) {
			return nil, apitypes.Fail(apitypes.ErrorCodeNotImplemented, err.Error())
		}
		if err != nil {
			return nil, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
		}
		s.recordWorktrees(ctx, se.ID, st.Worktrees)
		return api, nil
	}
}

// stop closes the session's terminals and stops its host (remove: also its worktrees and directory claim). A
// session flowd does not know has nothing to stop. The host is stopped even when closing a terminal failed: both
// failures are returned.
func (s *Service) stop(ctx context.Context, api flowdproto.API, id string, remove bool) error {
	var errs []error
	if s.CloseTerminals != nil {
		if err := s.CloseTerminals(ctx, api, id); err != nil && !errors.Is(err, flowdproto.ErrNotFound) {
			errs = append(errs, fmt.Errorf("closing the terminal: %w", err))
		}
	}
	if err := api.StopSession(ctx, id, remove); err != nil && !errors.Is(err, flowdproto.ErrNotFound) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Archive closes a session's terminals, stops its host and removes its worktrees (branches with commits of their own
// stay, and flowd keeps a worktree with uncommitted or untracked changes), then marks it archived and ends its
// follower; its log stays readable. A stopped sandbox is not started for it: nothing of the session runs there, so
// the session is marked archived with its cleanup pending, which the sandbox's next bring-up does (Sweep). One that
// is not warm while its instance may run fails the archive. Archiving an archived session again changes nothing.
func (s *Service) Archive(ctx context.Context, id string) error {
	defer s.Lock(id)()
	se, err := s.Store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return apitypes.Fail(apitypes.ErrorCodeNotFound, "")
	}
	if err != nil || se.ArchivedAt != nil {
		return err
	}
	api, err := s.Sandboxes.Warm(ctx, se.ProjectID)
	// Only these prove nothing runs; any other failure (sandboxes.ErrMayRun among them: not warm, the instance not
	// proven stopped) may leave a host with the session's credential running.
	pending := errors.Is(err, sandboxes.ErrNotWarm) || errors.Is(err, sandboxes.ErrNotFound)
	if errors.Is(err, sandboxes.ErrMayRun) {
		return apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
	}
	if err != nil && !pending {
		return err
	}
	if !pending {
		if err := s.stop(ctx, api, id, true); err != nil {
			return apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
		}
	}
	if err := s.Store.Archive(ctx, id, pending); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	s.unfollow(id)
	s.Publish(ctx, id)
	return nil
}

// Sweep finishes the archives made while the project's sandbox was stopped (terminals, host, worktrees), now that
// api reaches its flowd: called after every bring-up. A failure is logged and stays pending for the next one.
func (s *Service) Sweep(ctx context.Context, projectID string, api flowdproto.API) {
	list, err := s.Store.List(ctx, ListFilter{ProjectID: projectID, Archived: true, CleanupPending: true})
	if err != nil {
		s.Log.Warn("archived sessions to clean up", "project", projectID, "err", err)
		return
	}
	for _, se := range list {
		if err := s.stop(ctx, api, se.ID, true); err != nil {
			s.Log.Warn("cleaning up an archived session", "session", se.ID, "err", err)
			continue
		}
		if err := s.Store.CleanedUp(ctx, se.ID); err != nil {
			s.Log.Warn("cleaning up an archived session", "session", se.ID, "err", err)
			continue
		}
		s.Log.Info("archived session cleaned up", "session", se.ID)
	}
}

// lostTurnHosts bounds the hosts CloseLostTurns starts at once, and lostTurnWait each start: a sandbox with many
// cut-off sessions must not have all their agents booting together.
const (
	lostTurnHosts = 2
	lostTurnWait  = time.Minute
)

// CloseLostTurns starts the hosts of the project's sessions whose log ends in an open turn (the row's
// turn_started_at: a sandbox stop cut it off, turnCutOff), so each host logs that turn lost now (requests expired,
// turn.ended lost, session.resumed sandbox_start) and an open timeline shows it closed without a message first.
// Such a host leaves queued messages queued (nobody asked for a turn) and is stopped again right after: no idle host
// stays, and the owner's next message starts one that drains the queue. Every other host starts on demand. Called
// after every bring-up, in the Warmed goroutine, so the sandbox turns warm without waiting for it. A host that runs
// already (a reconnect) is left as it is. Failures are logged; the next message closes the turn then.
func (s *Service) CloseLostTurns(ctx context.Context, projectID string, api flowdproto.API) {
	list, err := s.Store.List(ctx, ListFilter{ProjectID: projectID})
	if err != nil {
		s.Log.Warn("sessions with a lost turn", "project", projectID, "err", err)
		return
	}
	sem := make(chan struct{}, lostTurnHosts)
	var wg sync.WaitGroup
	for _, se := range list {
		if se.TurnStartedAt == nil {
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			s.closeLostTurn(ctx, api, se.ID)
		})
	}
	wg.Wait()
}

// closeLostTurn starts one session's host under the session's lock, asking the row again: an archive or a message
// may have come first. Only a host this call started is stopped again: one that ran already belongs to a message.
func (s *Service) closeLostTurn(ctx context.Context, api flowdproto.API, id string) {
	defer s.Lock(id)()
	ctx, cancel := context.WithTimeout(ctx, lostTurnWait)
	defer cancel()
	se, err := s.Store.Get(ctx, id)
	if err != nil || se.ArchivedAt != nil || se.TurnStartedAt == nil {
		return
	}
	spec, err := s.Spec(ctx, se)
	if err != nil {
		s.Log.Warn("starting a host to close a lost turn", "session", id, "err", err)
		return
	}
	spec.ResumeReason = event.ResumeReasonSandboxStart
	st, err := api.EnsureSession(ctx, spec)
	if err != nil {
		s.Log.Warn("starting a host to close a lost turn", "session", id, "err", err)
		return
	}
	s.recordWorktrees(ctx, id, st.Worktrees)
	if !st.Started {
		return
	}
	if err := api.StopSession(ctx, id, false); err != nil && !errors.Is(err, flowdproto.ErrNotFound) {
		s.Log.Warn("stopping the host that closed a lost turn", "session", id, "err", err)
		return
	}
	s.Log.Info("host started and stopped to close a lost turn", "session", id)
}

func (s *Service) recordWorktrees(ctx context.Context, id string, states []flowdproto.WorktreeState) {
	wts, err := s.Store.Worktrees(ctx, id)
	if err != nil {
		return
	}
	for _, w := range wts {
		for _, st := range states {
			if st.Path == w.Path && st.BaseSHA != "" && w.BaseSHA != st.BaseSHA {
				w.BaseSHA = st.BaseSHA
				_ = s.Store.UpdateWorktree(ctx, w)
			}
		}
	}
}

// Command sends one command to the session's host; a host that is not running is started first (with resume).
func (s *Service) Command(ctx context.Context, id, cmdID string, kind event.CommandKind, payload any) (flowdproto.CommandResult, error) {
	c, err := event.NewCommand(cmdID, id, kind, payload)
	if err != nil {
		return flowdproto.CommandResult{}, err
	}
	api, err := s.Host(ctx, id)
	if err != nil {
		return flowdproto.CommandResult{}, err
	}
	res, err := api.Command(ctx, c)
	if errors.Is(err, flowdproto.ErrUnavailable) {
		// The host ended between EnsureSession and the command, or flowd was restarting: once more.
		if api, err = s.Host(ctx, id); err == nil {
			res, err = api.Command(ctx, c)
		}
	}
	switch {
	case errors.Is(err, flowdproto.ErrNotPending):
		return res, apitypes.Fail(apitypes.ErrorCodeRequestNotPending, "")
	case errors.Is(err, flowdproto.ErrInvalid):
		return res, apitypes.Fail(apitypes.ErrorCodeInvalidRequest, err.Error())
	case err != nil:
		var ae *apitypes.Error
		if errors.As(err, &ae) {
			return res, err
		}
		return res, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
	}
	return res, nil
}

// CommandIfRunning sends a command that only matters to a running session (an interrupt, a reply): a stopped
// sandbox is not started for it. ran false: the sandbox is stopped or has none, so nothing of the session runs and
// the command had nothing to act on. In a warm sandbox it is Command: a host that is down starts, logs the cut-off
// turn lost and so closes what the owner still sees open.
func (s *Service) CommandIfRunning(ctx context.Context, id, cmdID string, kind event.CommandKind, payload any) (flowdproto.CommandResult, bool, error) {
	se, err := s.Store.Get(ctx, id)
	if err != nil {
		return flowdproto.CommandResult{}, false, err
	}
	if se.ArchivedAt != nil {
		return flowdproto.CommandResult{}, false, apitypes.Fail(apitypes.ErrorCodeSessionArchived, "")
	}
	_, err = s.Sandboxes.Warm(ctx, se.ProjectID)
	if errors.Is(err, sandboxes.ErrNotWarm) || errors.Is(err, sandboxes.ErrNotFound) {
		return flowdproto.CommandResult{}, false, nil
	}
	if err != nil {
		return flowdproto.CommandResult{}, false, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
	}
	res, err := s.Command(ctx, id, cmdID, kind, payload)
	return res, true, err
}

// Summary is the sidebar's view of a session.
func (s *Service) Summary(ctx context.Context, se Session) (apitypes.SessionSummary, error) {
	wts, err := s.Store.Worktrees(ctx, se.ID)
	if err != nil {
		return apitypes.SessionSummary{}, err
	}
	sum := apitypes.SessionSummary{ID: se.ID, ProjectID: se.ProjectID, Title: se.Title,
		Provider: apitypes.Provider(se.Provider), ClaudeAuth: apitypes.ClaudeAuth(se.ClaudeAuth), Model: se.Model,
		Mode: apitypes.PermissionMode(se.Mode), ModeCap: apitypes.PermissionMode(se.ModeCap),
		Status: apitypes.SessionStatus(se.Status), Unseen: se.Unseen, Repos: []string{}, Branch: se.Branch,
		TurnStartedAt: se.TurnStartedAt, LastActivityAt: se.LastActivityAt, CreatedAt: se.CreatedAt}
	if se.ParentID != nil {
		sum.ParentID = *se.ParentID
	}
	if event.SessionStatus(se.Status) == event.SessionStatusFailed {
		sum.TurnStartedAt = nil // a turn cut off by a sandbox stop: open in the log, running nowhere (turnCutOff)
	}
	for _, w := range wts {
		sum.Repos = append(sum.Repos, path.Base(w.Path))
	}
	return sum, nil
}

// Describe is the API's view of one session.
func (s *Service) Describe(ctx context.Context, se Session) (apitypes.Session, error) {
	sum, err := s.Summary(ctx, se)
	if err != nil {
		return apitypes.Session{}, err
	}
	wts, err := s.Store.Worktrees(ctx, se.ID)
	if err != nil {
		return apitypes.Session{}, err
	}
	out := apitypes.Session{Summary: sum, Dir: layout.SessionDir(se.Name), LastSeq: se.LastSeq, Worktrees: []apitypes.Worktree{}}
	for _, w := range wts {
		r, err := s.Projects.Repo(ctx, w.RepoID)
		if err != nil {
			return apitypes.Session{}, err
		}
		out.Worktrees = append(out.Worktrees, apitypes.Worktree{RepoID: w.RepoID, Repo: r.FullName, Branch: w.Branch,
			Path: w.Path, BaseRef: w.BaseRef})
	}
	return out, nil
}

// Follow starts following a session's log (once per session) for as long as Start's context lives.
func (s *Service) Follow(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.base == nil || s.follows[id] != nil {
		return
	}
	if s.follows == nil {
		s.follows = map[string]*follower{}
	}
	ctx, cancel := context.WithCancel(s.base)
	f := &follower{cancel: cancel}
	s.follows[id] = f
	go s.follow(ctx, id, f)
}

// unfollow ends a session's follower, also one blocked in a stream (flowd keeps following an archived session's
// log); it runs its own cleanup as it ends.
func (s *Service) unfollow(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.follows[id]; f != nil {
		f.cancel()
	}
}

func (s *Service) follow(ctx context.Context, id string, f *follower) {
	retry := s.Retry
	if retry == 0 {
		retry = 2 * time.Second
	}
	// Ending frees the place, so a later Follow (a session created again under its id cannot be, but an unarchived
	// one could) starts a new follower.
	defer func() {
		f.cancel()
		if s.Meter != nil {
			s.Meter.Forget(id)
		}
		s.mu.Lock()
		if s.follows[id] == f {
			delete(s.follows, id)
		}
		s.mu.Unlock()
	}()
	for ctx.Err() == nil {
		se, err := s.Store.Get(ctx, id)
		switch {
		case errors.Is(err, ErrNotFound) || err == nil && se.ArchivedAt != nil:
			return
		case err != nil:
			// A transient store error (locked, I/O): try again after the wait rather than freeze the sidebar status.
			s.Log.Warn("follow: reading the session", "session", id, "err", err)
		default:
			api, err := s.Sandboxes.Warm(ctx, se.ProjectID)
			switch {
			case err == nil:
				s.mirror(ctx, api, se)
			case errors.Is(err, sandboxes.ErrNotWarm):
				s.turnCutOff(ctx, se)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

// turnCutOff ends the mirrored turn of a session whose sandbox is proven stopped: a stop ends every unit without a
// turn.ended in the log, so the row would show the session working (or waiting for the owner) until a message
// restarts the host. The host logs that turn lost when it starts again (docs/events.md: failed); the row says so
// now. The row keeps turn_started_at: the log still ends in that open turn until a host closes it, and the
// sandbox's next bring-up starts the hosts of exactly those sessions (CloseLostTurns). Only ErrNotWarm proves the
// stop (never ErrMayRun).
func (s *Service) turnCutOff(ctx context.Context, se Session) {
	switch event.SessionStatus(se.Status) {
	case event.SessionStatusFailed:
		return // cut off already, or the turn failed
	case event.SessionStatusIdle, event.SessionStatusDone:
		if se.TurnStartedAt == nil {
			return
		}
	}
	if err := s.Store.ApplyMirror(ctx, se.ID, Mirror{Status: string(event.SessionStatusFailed),
		Unseen: true}); err != nil {
		s.Log.Warn("follow: ending the turn of a stopped sandbox", "session", se.ID, "err", err)
		return
	}
	s.Publish(ctx, se.ID)
}

// mirror follows one stream from the row's last seq until it fails.
func (s *Service) mirror(ctx context.Context, api flowdproto.API, se Session) {
	last := se.LastSeq
	ms := metering.Session{ID: se.ID, ProjectID: se.ProjectID, ParentID: se.ParentID, Provider: se.Provider,
		ClaudeAuth: se.ClaudeAuth, Model: se.Model}
	for e, err := range api.Events(ctx, se.ID, last) {
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Debug("follow", "session", se.ID, "err", err)
			}
			return
		}
		if e.Seq <= last {
			continue
		}
		last = e.Seq
		m := Mirror{LastSeq: e.Seq}
		publish := false
		switch e.Kind {
		case event.KindSessionStatus:
			if p, err := e.Payload(); err == nil {
				m.Status = string(p.(*event.SessionStatusChanged).Status)
				publish = true
			}
		case event.KindTurnStarted:
			m.TurnStarted, publish = true, true
		case event.KindTurnEnded:
			m.TurnEnded, m.Unseen, publish = true, true, true
		case event.KindSessionStarted:
			if p, err := e.Payload(); err == nil {
				m.ProviderSessionID = p.(*event.SessionStarted).ProviderSessionID
			}
		}
		if s.Meter != nil {
			if err := s.Meter.Observe(ctx, ms, e); err != nil {
				if ctx.Err() == nil { // a shutdown cutting a write off is no error
					s.Log.Error("meter", "session", se.ID, "seq", e.Seq, "err", err)
				}
				return
			}
		}
		if err := s.Store.ApplyMirror(ctx, se.ID, m); err != nil {
			s.Log.Error("mirror", "session", se.ID, "err", err)
			return
		}
		if publish {
			s.Publish(ctx, se.ID)
		}
	}
}

// Publish sends a session's current summary on the app event stream; an archived session's is session.removed
// (the sidebar drops it), also when a mirror still in flight publishes after the archive.
func (s *Service) Publish(ctx context.Context, id string) {
	if s.Hub == nil {
		return
	}
	se, err := s.Store.Get(ctx, id)
	if err != nil {
		return
	}
	if se.ArchivedAt != nil {
		s.Hub.Publish(apitypes.AppEvent{Kind: apitypes.AppEventKindSessionRemoved, SessionID: id})
		return
	}
	sum, err := s.Summary(ctx, se)
	if err != nil {
		return
	}
	s.Hub.Publish(apitypes.AppEvent{Kind: apitypes.AppEventKindSessionUpdated, Session: &sum, SessionID: id})
}
