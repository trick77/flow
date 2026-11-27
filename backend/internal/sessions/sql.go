package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/trick77/flow/internal/store"
)

// SQL is the Store on Flow's database.
type SQL struct{ DB *sql.DB }

var _ Store = SQL{}

const cols = `id, project_id, parent_id, name, title, title_source, provider, claude_auth, provider_session_id, model,
	mode, mode_cap, branch, status, status_at, unseen, seen_seq, last_seq, turn_started_at, last_activity_at,
	archived_at, cleanup_pending, created_at, updated_at`

func scan(sc interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var parent, turn, archived sql.NullString
	var statusAt, activity, created, updated string
	err := sc.Scan(&s.ID, &s.ProjectID, &parent, &s.Name, &s.Title, &s.TitleSource, &s.Provider, &s.ClaudeAuth,
		&s.ProviderSessionID, &s.Model, &s.Mode, &s.ModeCap, &s.Branch, &s.Status, &statusAt, &s.Unseen, &s.SeenSeq,
		&s.LastSeq, &turn, &activity, &archived, &s.CleanupPending, &created, &updated)
	if parent.Valid {
		s.ParentID = &parent.String
	}
	s.TurnStartedAt, s.ArchivedAt = store.ParseNull(turn), store.ParseNull(archived)
	s.StatusAt, s.LastActivityAt = store.Parse(statusAt), store.Parse(activity)
	s.CreatedAt, s.UpdatedAt = store.Parse(created), store.Parse(updated)
	return s, err
}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return err
}

// List implements Store, most recently active first.
func (s SQL) List(ctx context.Context, f ListFilter) ([]Session, error) {
	var where []string
	var args []any
	if f.Archived {
		where = append(where, "archived_at IS NOT NULL")
	} else {
		where = append(where, "archived_at IS NULL")
	}
	if f.ProjectID != "" {
		where, args = append(where, "project_id = ?"), append(args, f.ProjectID)
	}
	if f.CleanupPending {
		where = append(where, "cleanup_pending = 1")
	}
	if f.ParentID != "" {
		where, args = append(where, "parent_id = ?"), append(args, f.ParentID)
	}
	// Only constant fragments are joined; every value is a parameter.
	rows, err := s.DB.QueryContext(ctx, `SELECT `+cols+` FROM sessions WHERE `+strings.Join(where, " AND ")+ //nolint:gosec // see above
		` ORDER BY last_activity_at DESC, id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Session
	for rows.Next() {
		se, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// Get implements Store.
func (s SQL) Get(ctx context.Context, id string) (Session, error) {
	se, err := scan(s.DB.QueryRowContext(ctx, `SELECT `+cols+` FROM sessions WHERE id = ?`, id))
	return se, notFound(err, "session "+id)
}

// Create implements Store: the session and its worktrees in one transaction.
func (s SQL) Create(ctx context.Context, se Session, worktrees []Worktree) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if se.TitleSource == "" {
		se.TitleSource = "derived"
	}
	if se.Status == "" {
		se.Status = "idle"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions (id, project_id, parent_id, name, title, title_source, provider,
		claude_auth, provider_session_id, model, mode, mode_cap, branch, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		se.ID, se.ProjectID, se.ParentID, se.Name, se.Title, se.TitleSource, se.Provider, se.ClaudeAuth,
		se.ProviderSessionID, se.Model, se.Mode, se.ModeCap, se.Branch, se.Status)
	if err != nil {
		return err
	}
	for _, w := range worktrees {
		if err := addWorktree(ctx, tx, w); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Update implements Store: the fields an owner or the follower changes.
func (s SQL) Update(ctx context.Context, se Session) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE sessions SET title = ?, title_source = ?, model = ?, mode = ?, mode_cap = ?,
		provider_session_id = ?, updated_at = ? WHERE id = ?`, se.Title, se.TitleSource, se.Model, se.Mode, se.ModeCap,
		se.ProviderSessionID, store.Format(time.Now()), se.ID)
	return affected(res, err, "session "+se.ID)
}

func affected(res sql.Result, err error, what string) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return nil
}

// ApplyMirror implements Store; LastSeq never moves backwards.
func (s SQL) ApplyMirror(ctx context.Context, id string, m Mirror) error {
	now := store.Format(time.Now())
	q := `UPDATE sessions SET last_seq = max(last_seq, ?), last_activity_at = ?, updated_at = ?`
	args := []any{m.LastSeq, now, now}
	if m.Status != "" {
		q += `, status_at = CASE WHEN status = ? THEN status_at ELSE ? END, status = ?`
		args = append(args, m.Status, now, m.Status)
	}
	if m.Unseen {
		q += `, unseen = 1`
	}
	if m.ProviderSessionID != "" {
		q += `, provider_session_id = ?`
		args = append(args, m.ProviderSessionID)
	}
	if m.TurnStarted {
		q += `, turn_started_at = ?`
		args = append(args, now)
	}
	if m.TurnEnded {
		q += `, turn_started_at = NULL`
	}
	res, err := s.DB.ExecContext(ctx, q+` WHERE id = ?`, append(args, id)...)
	return affected(res, err, "session "+id)
}

// MarkSeen implements Store.
func (s SQL) MarkSeen(ctx context.Context, id string, seq int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE sessions SET seen_seq = max(seen_seq, ?),
		unseen = CASE WHEN ? >= last_seq THEN 0 ELSE unseen END WHERE id = ?`, seq, seq, id)
	return affected(res, err, "session "+id)
}

// Archive implements Store.
func (s SQL) Archive(ctx context.Context, id string, cleanupPending bool) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE sessions SET archived_at = ?, cleanup_pending = ? WHERE id = ?
		AND archived_at IS NULL`, store.Format(time.Now()), cleanupPending, id)
	return affected(res, err, "session "+id)
}

// CleanedUp implements Store.
func (s SQL) CleanedUp(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE sessions SET cleanup_pending = 0 WHERE id = ?`, id)
	return affected(res, err, "session "+id)
}

// Delete implements Store; the worktree rows go with the session (ON DELETE CASCADE).
func (s SQL) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return affected(res, err, "session "+id)
}

const wtCols = `id, session_id, repo_id, branch, path, base_ref, base_sha, pr_number, pr_url, pr_title, pr_state,
	pr_order, pr_note, created_at, updated_at`

// Worktrees implements Store, in creation order.
func (s SQL) Worktrees(ctx context.Context, sessionID string) ([]Worktree, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+wtCols+` FROM session_worktrees WHERE session_id = ?
		ORDER BY created_at, rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Worktree
	for rows.Next() {
		var w Worktree
		var created, updated string
		if err := rows.Scan(&w.ID, &w.SessionID, &w.RepoID, &w.Branch, &w.Path, &w.BaseRef, &w.BaseSHA, &w.PRNumber,
			&w.PRURL, &w.PRTitle, &w.PRState, &w.PROrder, &w.PRNote, &created, &updated); err != nil {
			return nil, err
		}
		w.CreatedAt, w.UpdatedAt = store.Parse(created), store.Parse(updated)
		out = append(out, w)
	}
	return out, rows.Err()
}

func addWorktree(ctx context.Context, ex interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, w Worktree) error {
	if w.PRState == "" {
		w.PRState = "not_opened"
	}
	_, err := ex.ExecContext(ctx, `INSERT INTO session_worktrees (id, session_id, repo_id, branch, path, base_ref, base_sha,
		pr_number, pr_url, pr_title, pr_state, pr_order, pr_note) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.SessionID, w.RepoID, w.Branch, w.Path, w.BaseRef, w.BaseSHA, w.PRNumber, w.PRURL, w.PRTitle, w.PRState,
		w.PROrder, w.PRNote)
	return err
}

// AddWorktree implements Store.
func (s SQL) AddWorktree(ctx context.Context, w Worktree) error { return addWorktree(ctx, s.DB, w) }

// UpdateWorktree implements Store.
func (s SQL) UpdateWorktree(ctx context.Context, w Worktree) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE session_worktrees SET base_sha = ?, pr_number = ?, pr_url = ?, pr_title = ?,
		pr_state = ?, pr_order = ?, pr_note = ?, updated_at = ? WHERE id = ?`, w.BaseSHA, w.PRNumber, w.PRURL, w.PRTitle,
		w.PRState, w.PROrder, w.PRNote, store.Format(time.Now()), w.ID)
	return affected(res, err, "worktree "+w.ID)
}

// CountChildren implements Store.
func (s SQL) CountChildren(ctx context.Context, parentID string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE parent_id = ? AND archived_at IS NULL`,
		parentID).Scan(&n)
	return n, err
}
