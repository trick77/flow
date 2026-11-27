package profile

import (
	"context"
	"database/sql"

	"github.com/trick77/flow/internal/store"
)

// SQL reads the profile row on Flow's database. Only Get so far: the credentials area needs the git identity for
// the sandboxes; Update and SetConfigSync (the rest of Store) come with the profile API.
type SQL struct{ DB *sql.DB }

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
