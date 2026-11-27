package profile

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/trick77/flow/internal/store"
)

// SQL is the Store on Flow's database: the one owner_profile row the migration seeds.
type SQL struct{ DB *sql.DB }

var _ Store = SQL{}

// nowFunc is the clock of updated_at and config_synced_at.
var nowFunc = time.Now

// Get implements Store.
func (s SQL) Get(ctx context.Context) (Profile, error) {
	var p Profile
	var synced sql.NullString
	var updated string
	err := s.DB.QueryRowContext(ctx, `SELECT git_name, git_email, language, format_locale, time_zone,
		sidebar_grouping, config_repo, config_sync_state, config_synced_at, config_commit, config_files,
		config_error_code, updated_at FROM owner_profile WHERE id = 1`).Scan(&p.GitName, &p.GitEmail, &p.Language,
		&p.FormatLocale, &p.TimeZone, &p.SidebarGrouping, &p.ConfigRepo, &p.ConfigSyncState, &synced, &p.ConfigCommit,
		&p.ConfigFiles, &p.ConfigErrorCode, &updated)
	p.ConfigSyncedAt, p.UpdatedAt = store.ParseNull(synced), store.Parse(updated)
	return p, err
}

// Update implements Store: the set fields in one statement, then the row as it is now. The caller validates.
func (s SQL) Update(ctx context.Context, p Patch) (Profile, error) {
	sets := []string{"updated_at = ?"}
	args := []any{store.Format(nowFunc())}
	for _, f := range []struct {
		col string
		v   *string
	}{{"git_name", p.GitName}, {"git_email", p.GitEmail}, {"language", p.Language},
		{"format_locale", p.FormatLocale}, {"time_zone", p.TimeZone}, {"sidebar_grouping", p.SidebarGrouping},
		{"config_repo", p.ConfigRepo}} {
		if f.v != nil {
			sets, args = append(sets, f.col+" = ?"), append(args, *f.v)
		}
	}
	// Only constant column names are joined; every value is a parameter.
	if _, err := s.DB.ExecContext(ctx, `UPDATE owner_profile SET `+strings.Join(sets, ", ")+` WHERE id = 1`, //nolint:gosec // see above
		args...); err != nil {
		return Profile{}, err
	}
	return s.Get(ctx)
}

// SetConfigSync implements Store: the state of the last config sync; a synced one records when.
func (s SQL) SetConfigSync(ctx context.Context, c ConfigSync) error {
	var synced any
	if c.State == "synced" {
		synced = store.Format(nowFunc())
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE owner_profile SET config_sync_state = ?, config_commit = ?,
		config_files = ?, config_error_code = ?, config_synced_at = coalesce(?, config_synced_at), updated_at = ?
		WHERE id = 1`, c.State, c.Commit, c.Files, c.ErrorCode, synced, store.Format(nowFunc()))
	return err
}
