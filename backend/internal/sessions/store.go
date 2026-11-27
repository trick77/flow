// Package sessions is Flow's side of sessions: creation with worktrees, the child-session rules, following every
// session's event log through flowd and mirroring its status into the database (owner: sessions area,
// docs/ownership.md).
package sessions

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound means no such session.
var ErrNotFound = errors.New("sessions: not found")

// ErrClientIDTaken means Create found a session made with the same message clientId.
var ErrClientIDTaken = errors.New("sessions: client id taken")

// ListFilter narrows List; zero values match everything that is not archived.
type ListFilter struct {
	ProjectID string
	ParentID  string
	Archived  bool
	// CleanupPending: only sessions whose sandbox still holds their host and worktrees (with Archived).
	CleanupPending bool
}

// Mirror is what following a log changes on a session row, applied in one statement.
type Mirror struct {
	LastSeq int64
	// At is the event's time: the turn's start and the session's last activity. Zero is now.
	At     time.Time
	Status string
	// Unseen marks the session unseen: with a LastSeq only when the owner has not seen that seq (a mark from the
	// session stream may be ahead of the mirror); without one (a turn cut off by a stop, in no event) always.
	Unseen            bool
	ProviderSessionID string
	TurnStarted       bool
	TurnEnded         bool
	// Model, Effort, Mode and ModeCap: the settings the log changed (session.settings, an approved plan's mode); ""
	// keeps (the agent's own effort is "default", never "").
	Model   string
	Effort  string
	Mode    string
	ModeCap string
}

// Patch is what the owner changes on a session row directly; "" keeps a field. A title is the owner's (title_source
// owner). Model and mode are the log's, written by the mirror only.
type Patch struct {
	Title string
}

// Store keeps session and worktree rows.
type Store interface {
	List(ctx context.Context, f ListFilter) ([]Session, error)
	Get(ctx context.Context, id string) (Session, error)
	// ByClientID is the session created with this message clientId (ErrNotFound: none).
	ByClientID(ctx context.Context, clientID string) (Session, error)
	// Create inserts the session and its worktrees; ErrClientIDTaken when a session holds its ClientID.
	Create(ctx context.Context, s Session, worktrees []Worktree) error
	// Update writes only the patch's fields, so it cannot undo a mirror written meanwhile.
	Update(ctx context.Context, id string, p Patch) error
	// ApplyMirror records progress through the log; it never moves LastSeq backwards.
	ApplyMirror(ctx context.Context, id string, m Mirror) error
	MarkSeen(ctx context.Context, id string, seq int64) error
	// Archive marks a session archived; cleanupPending: its sandbox was stopped, so its host and worktrees are still
	// there, to be removed at the next bring-up (CleanedUp then).
	Archive(ctx context.Context, id string, cleanupPending bool) error
	CleanedUp(ctx context.Context, id string) error
	// Delete removes a session and its worktree rows: only for a creation that failed before the session was
	// usable.
	Delete(ctx context.Context, id string) error
	Worktrees(ctx context.Context, sessionID string) ([]Worktree, error)
	AddWorktree(ctx context.Context, w Worktree) error
	UpdateWorktree(ctx context.Context, w Worktree) error
	// CountChildren counts a session's non-archived started sessions, for the bound on child sessions.
	CountChildren(ctx context.Context, parentID string) (int, error)
}
