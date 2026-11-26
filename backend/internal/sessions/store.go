// Package sessions is Flow's side of sessions: creation with worktrees, the child-session rules, following every
// session's event log through flowd and mirroring its status into the database (owner: sessions area,
// docs/ownership.md).
package sessions

import (
	"context"
	"errors"
)

// ErrNotFound means no such session.
var ErrNotFound = errors.New("sessions: not found")

// ListFilter narrows List; zero values match everything that is not archived.
type ListFilter struct {
	ProjectID string
	ParentID  string
	Archived  bool
}

// Mirror is what following a log changes on a session row, applied in one statement.
type Mirror struct {
	LastSeq           int64
	Status            string
	Unseen            bool
	ProviderSessionID string
	TurnStarted       bool
	TurnEnded         bool
}

// Store keeps session and worktree rows.
type Store interface {
	List(ctx context.Context, f ListFilter) ([]Session, error)
	Get(ctx context.Context, id string) (Session, error)
	Create(ctx context.Context, s Session, worktrees []Worktree) error
	Update(ctx context.Context, s Session) error
	// ApplyMirror records progress through the log; it never moves LastSeq backwards.
	ApplyMirror(ctx context.Context, id string, m Mirror) error
	MarkSeen(ctx context.Context, id string, seq int64) error
	Archive(ctx context.Context, id string) error
	// Delete removes a session and its worktree rows: only for a creation that failed before the session was
	// usable.
	Delete(ctx context.Context, id string) error
	Worktrees(ctx context.Context, sessionID string) ([]Worktree, error)
	AddWorktree(ctx context.Context, w Worktree) error
	UpdateWorktree(ctx context.Context, w Worktree) error
	// CountChildren counts a session's non-archived started sessions, for the bound on child sessions.
	CountChildren(ctx context.Context, parentID string) (int, error)
}
