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
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/store"
)

// Sandboxes is what sessions need of the sandboxes area.
type Sandboxes interface {
	// Ensure brings the project's sandbox to warm and returns its flowd.
	Ensure(ctx context.Context, projectID string) (flowdproto.API, error)
	// Warm returns the flowd of a sandbox that is warm by Flow's records, never starting one.
	Warm(ctx context.Context, projectID string) (flowdproto.API, error)
	// Forget drops a cached connection after it failed.
	Forget(projectID string)
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

	mu      sync.Mutex
	base    context.Context
	follows map[string]bool
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
	name, err := s.uniqueName(ctx, in.ProjectID, base)
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
	// take it first (the unique index on project and name says so). Then the next suffix, a few times.
	for attempt := 2; ; attempt++ {
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
		if !strings.Contains(err.Error(), "sessions.name") || attempt > 10 {
			return apitypes.Session{}, err
		}
		name = fmt.Sprintf("%s-%d", base, attempt)
	}
	// Until the session is usable a failure takes it back out: in the sandbox first (host unit, worktrees, the
	// directory's claim; EnsureSession may have got that far before failing), through the no-start path so a
	// stopped sandbox stays stopped, then the row. A retry then neither finds a half-made session nor its leftovers.
	undo := func(err error) (apitypes.Session, error) {
		ctx := context.WithoutCancel(ctx)
		if api, werr := s.Sandboxes.Warm(ctx, se.ProjectID); werr == nil {
			if serr := api.StopSession(ctx, se.ID, true); serr != nil && !errors.Is(serr, flowdproto.ErrNotFound) {
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

func (s *Service) uniqueName(ctx context.Context, projectID, base string) (string, error) {
	taken := map[string]bool{}
	for _, archived := range []bool{false, true} {
		list, err := s.Store.List(ctx, ListFilter{ProjectID: projectID, Archived: archived})
		if err != nil {
			return "", err
		}
		for _, se := range list {
			taken[se.Name] = true
		}
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name, nil
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
		Worktrees: []flowdproto.WorktreeSpec{}}
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

// Host makes sure the session's sandbox is warm and its host runs, and returns the sandbox's flowd.
func (s *Service) Host(ctx context.Context, id string) (flowdproto.API, error) {
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
		if err != nil {
			return nil, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
		}
		s.recordWorktrees(ctx, se.ID, st.Worktrees)
		return api, nil
	}
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
	if s.base == nil || s.follows[id] {
		return
	}
	if s.follows == nil {
		s.follows = map[string]bool{}
	}
	s.follows[id] = true
	go s.follow(s.base, id)
}

func (s *Service) follow(ctx context.Context, id string) {
	retry := s.Retry
	if retry == 0 {
		retry = 2 * time.Second
	}
	// Ending frees the place, so a later Follow (a session created again under its id cannot be, but an unarchived
	// one could) starts a new follower.
	defer func() {
		s.mu.Lock()
		delete(s.follows, id)
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
			if api, err := s.Sandboxes.Warm(ctx, se.ProjectID); err == nil {
				s.mirror(ctx, api, se)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

// mirror follows one stream from the row's last seq until it fails.
func (s *Service) mirror(ctx context.Context, api flowdproto.API, se Session) {
	last := se.LastSeq
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
		if err := s.Store.ApplyMirror(ctx, se.ID, m); err != nil {
			s.Log.Error("mirror", "session", se.ID, "err", err)
			return
		}
		if publish {
			s.Publish(ctx, se.ID)
		}
	}
}

// Publish sends a session's current summary on the app event stream.
func (s *Service) Publish(ctx context.Context, id string) {
	if s.Hub == nil {
		return
	}
	se, err := s.Store.Get(ctx, id)
	if err != nil {
		return
	}
	sum, err := s.Summary(ctx, se)
	if err != nil {
		return
	}
	s.Hub.Publish(apitypes.AppEvent{Kind: apitypes.AppEventKindSessionUpdated, Session: &sum, SessionID: id})
}
