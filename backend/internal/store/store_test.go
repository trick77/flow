package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trick77/ship/kit/schemadoc"
)

func open(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "flow.db")
}

// TestSchemaDoc keeps docs/schema.md in step with the migrations and the model.go row types. A file database, not
// :memory:: the pool would hand each connection its own empty database.
func TestSchemaDoc(t *testing.T) {
	db, err := Open(t.Context(), open(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := schemadoc.Describe(t.Context(), db, schemadoc.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	es, err := schemadoc.Entities(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := schemadoc.Render(s, es)
	if err != nil {
		t.Fatal(err)
	}
	schemadoc.Golden(t, doc, filepath.Join("..", "..", "..", "docs", "schema.md"))
}

func TestOpen_migratesOnceAndSeedsTheProfile(t *testing.T) {
	path := open(t)
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM owner_profile`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("owner_profile rows = %d, %v; want the one seeded row", n, err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO owner_profile (id) VALUES (2)`); err == nil {
		t.Error("a second profile row was accepted")
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, name, provider, model, mode, branch)
		VALUES ('s', 'missing', 'n', 'claude', 'm', 'ask', 'b')`); err == nil {
		t.Error("a session of a missing project was accepted: foreign keys are off")
	}
	_ = db.Close()
	// A second open finds nothing pending.
	db, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}

func TestValues(t *testing.T) {
	a, b := NewID(), NewID()
	if len(a) != 36 || a == b || a[14] != '7' || !strings.ContainsAny(a[19:20], "89ab") {
		t.Errorf("ids %s %s", a, b)
	}
	now := time.Date(2026, 11, 26, 12, 0, 0, 5e6, time.UTC)
	if s := Format(now); s != "2026-11-26T12:00:00.005Z" || !Parse(s).Equal(now) {
		t.Errorf("Format = %s", s)
	}
	if !Parse("garbage").IsZero() || ParseNull(sql.NullString{}) != nil || Null(nil).Valid {
		t.Error("null handling")
	}
	if p := ParseNull(Null(&now)); p == nil || !p.Equal(now) {
		t.Error("nullable round trip")
	}
}

func TestOpen_failsOnABadPath(t *testing.T) {
	if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "missing", "dir", "flow.db")); err == nil {
		t.Error("opened a database in a directory that does not exist")
	}
}
