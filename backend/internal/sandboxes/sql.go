package sandboxes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/trick77/flow/internal/store"
)

// SQL is the Store on Flow's database.
type SQL struct{ DB *sql.DB }

var _ Store = SQL{}

const cols = `id, project_id, instance, state, progress_code, error_code, arch, flowd_version, flowd_token, setup_hash,
	last_used_at, started_at, created_at, updated_at`

func scan(sc interface{ Scan(...any) error }) (Sandbox, error) {
	var s Sandbox
	var lastUsed, started sql.NullString
	var created, updated string
	err := sc.Scan(&s.ID, &s.ProjectID, &s.Instance, &s.State, &s.ProgressCode, &s.ErrorCode, &s.Arch, &s.FlowdVersion,
		&s.FlowdToken, &s.SetupHash, &lastUsed, &started, &created, &updated)
	s.LastUsedAt, s.StartedAt = store.ParseNull(lastUsed), store.ParseNull(started)
	s.CreatedAt, s.UpdatedAt = store.Parse(created), store.Parse(updated)
	return s, err
}

// ByProject implements Store.
func (s SQL) ByProject(ctx context.Context, projectID string) (Sandbox, error) {
	sb, err := scan(s.DB.QueryRowContext(ctx, `SELECT `+cols+` FROM sandboxes WHERE project_id = ?`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return sb, fmt.Errorf("%w: project %s", ErrNotFound, projectID)
	}
	return sb, err
}

// List implements Store.
func (s SQL) List(ctx context.Context) ([]Sandbox, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+cols+` FROM sandboxes ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Sandbox
	for rows.Next() {
		sb, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sb)
	}
	return out, rows.Err()
}

// Put implements Store: insert, or replace every column but the creation time.
func (s SQL) Put(ctx context.Context, sb Sandbox) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sandboxes (id, project_id, instance, state, progress_code, error_code,
		arch, flowd_version, flowd_token, setup_hash, last_used_at, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET instance = excluded.instance, state = excluded.state,
		progress_code = excluded.progress_code, error_code = excluded.error_code, arch = excluded.arch,
		flowd_version = excluded.flowd_version, flowd_token = excluded.flowd_token, setup_hash = excluded.setup_hash,
		last_used_at = excluded.last_used_at, started_at = excluded.started_at, updated_at = ?`,
		sb.ID, sb.ProjectID, sb.Instance, sb.State, sb.ProgressCode, sb.ErrorCode, sb.Arch, sb.FlowdVersion, sb.FlowdToken,
		sb.SetupHash, store.Null(sb.LastUsedAt), store.Null(sb.StartedAt), store.Format(time.Now()))
	return err
}

// SetState implements Store.
func (s SQL) SetState(ctx context.Context, id, state, progressCode, errorCode string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE sandboxes SET state = ?, progress_code = ?, error_code = ?, updated_at = ?
		WHERE id = ?`, state, progressCode, errorCode, store.Format(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: sandbox %s", ErrNotFound, id)
	}
	return nil
}

// Touch implements Store.
func (s SQL) Touch(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sandboxes SET last_used_at = ? WHERE id = ?`, store.Format(time.Now()), id)
	return err
}
