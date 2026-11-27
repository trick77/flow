package credentials

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/trick77/flow/internal/store"
)

// ErrNotFound is a credential that is not stored.
var ErrNotFound = errors.New("credentials: not found")

// SQL is the Store on Flow's database (table credentials).
type SQL struct{ DB *sql.DB }

var _ Store = SQL{}

const cols = `id, kind, provider, label, env_name, sealed, login, expires_at, refresh_expires_at, created_at, updated_at`

func scan(sc interface{ Scan(...any) error }) (Credential, error) {
	var c Credential
	var expires, refresh sql.NullString
	var created, updated string
	err := sc.Scan(&c.ID, &c.Kind, &c.Provider, &c.Label, &c.EnvName, &c.Sealed, &c.Login, &expires, &refresh,
		&created, &updated)
	c.ExpiresAt, c.RefreshExpiresAt = store.ParseNull(expires), store.ParseNull(refresh)
	c.CreatedAt, c.UpdatedAt = store.Parse(created), store.Parse(updated)
	return c, err
}

// List implements Store, oldest first.
func (s SQL) List(ctx context.Context) ([]Credential, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+cols+` FROM credentials ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Credential
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get implements Store.
func (s SQL) Get(ctx context.Context, kind, provider string) (Credential, error) {
	c, err := scan(s.DB.QueryRowContext(ctx, `SELECT `+cols+` FROM credentials WHERE kind = ? AND provider = ?`,
		kind, provider))
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("%w: %s %s", ErrNotFound, kind, provider)
	}
	return c, err
}

// Put implements Store: one row per kind and provider, so a new credential replaces the stored one (its id and
// creation time with it: the API reports when the current secret was stored).
func (s SQL) Put(ctx context.Context, c Credential) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE kind = ? AND provider = ? AND id != ?`,
		c.Kind, c.Provider, c.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credentials (id, kind, provider, label, env_name, sealed, login,
		expires_at, refresh_expires_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET label = excluded.label, env_name = excluded.env_name, sealed = excluded.sealed,
		login = excluded.login, expires_at = excluded.expires_at, refresh_expires_at = excluded.refresh_expires_at,
		updated_at = excluded.updated_at`,
		c.ID, c.Kind, c.Provider, c.Label, c.EnvName, c.Sealed, c.Login, store.Null(c.ExpiresAt),
		store.Null(c.RefreshExpiresAt), store.Format(c.CreatedAt), store.Format(c.UpdatedAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Delete implements Store.
func (s SQL) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}
