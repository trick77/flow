package profile

import (
	"path/filepath"
	"testing"

	"github.com/trick77/flow/internal/store"
)

// Get reads the one row the migration seeds, defaults first, then what was written to it.
func TestSQL_Get(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := SQL{DB: db}
	p, err := s.Get(t.Context())
	if err != nil || p.GitName != "" || p.FormatLocale != "en-CH" || p.ConfigSyncState != "never" ||
		p.ConfigSyncedAt != nil || p.UpdatedAt.IsZero() {
		t.Fatalf("seeded row %+v, %v", p, err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE owner_profile SET git_name = 'Jan', git_email = 'jan@example.com',
		config_synced_at = '2026-11-27T10:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	if p, err = s.Get(t.Context()); err != nil || p.GitName != "Jan" || p.GitEmail != "jan@example.com" ||
		p.ConfigSyncedAt == nil {
		t.Errorf("updated row %+v, %v", p, err)
	}
}
