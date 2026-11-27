package projects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/trick77/flow/internal/store"
)

// SQL is the Store on Flow's database.
type SQL struct{ DB *sql.DB }

var _ Store = SQL{}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return err
}

func scanProject(sc interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var created, updated string
	err := sc.Scan(&p.ID, &p.Name, &created, &updated)
	p.CreatedAt, p.UpdatedAt = store.Parse(created), store.Parse(updated)
	return p, err
}

// List implements Store, oldest first.
func (s SQL) List(ctx context.Context) ([]Project, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, created_at, updated_at FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get implements Store.
func (s SQL) Get(ctx context.Context, id string) (Project, error) {
	p, err := scanProject(s.DB.QueryRowContext(ctx, `SELECT id, name, created_at, updated_at FROM projects WHERE id = ?`, id))
	return p, notFound(err, "project "+id)
}

// Create implements Store: the project and its repositories in one transaction.
func (s SQL) Create(ctx context.Context, p Project, repos []Repo) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id, name) VALUES (?, ?)`, p.ID, p.Name); err != nil {
		return err
	}
	for i, r := range repos {
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_repos (id, project_id, full_name, default_branch, position)
			VALUES (?, ?, ?, ?, ?)`, r.ID, p.ID, r.FullName, r.DefaultBranch, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Rename implements Store.
func (s SQL) Rename(ctx context.Context, id, name string) error {
	return s.exec(ctx, "project "+id, `UPDATE projects SET name = ?, updated_at = ? WHERE id = ?`, name,
		store.Format(nowFunc()), id)
}

// Delete implements Store: the project's archived sessions first, then the project with its repositories and sandbox
// row, in one transaction. Sessions go first, so a session worktree's ON DELETE RESTRICT on its repository never
// depends on the order in which the cascade visits the child tables. A session not archived left over (made after
// the caller counted) rolls everything back: it would otherwise vanish without its session.removed.
func (s SQL) Delete(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE project_id = ? AND archived_at IS NOT NULL`, id); err != nil {
		return err
	}
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE project_id = ?`, id).Scan(&open); err != nil {
		return err
	}
	if open > 0 {
		return fmt.Errorf("%w: project %s has %d", ErrHasSessions, id, open)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: project %s", ErrNotFound, id)
	}
	return tx.Commit()
}

func (s SQL) exec(ctx context.Context, what, q string, args ...any) error {
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return nil
}

const repoCols = `id, project_id, full_name, default_branch, position, created_at`

func scanRepo(sc interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	var created string
	err := sc.Scan(&r.ID, &r.ProjectID, &r.FullName, &r.DefaultBranch, &r.Position, &created)
	r.CreatedAt = store.Parse(created)
	return r, err
}

// Repos implements Store, in their order.
func (s SQL) Repos(ctx context.Context, projectID string) ([]Repo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+repoCols+` FROM project_repos WHERE project_id = ? ORDER BY position, id`,
		projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Repo implements Store.
func (s SQL) Repo(ctx context.Context, id string) (Repo, error) {
	r, err := scanRepo(s.DB.QueryRowContext(ctx, `SELECT `+repoCols+` FROM project_repos WHERE id = ?`, id))
	return r, notFound(err, "repo "+id)
}

// AddRepo implements Store, last in order.
func (s SQL) AddRepo(ctx context.Context, r Repo) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO project_repos (id, project_id, full_name, default_branch, position)
		VALUES (?, ?, ?, ?, (SELECT coalesce(max(position), -1) + 1 FROM project_repos WHERE project_id = ?))`,
		r.ID, r.ProjectID, r.FullName, r.DefaultBranch, r.ProjectID)
	return err
}

// RemoveRepo implements Store.
func (s SQL) RemoveRepo(ctx context.Context, id string) error {
	return s.exec(ctx, "repo "+id, `DELETE FROM project_repos WHERE id = ?`, id)
}
