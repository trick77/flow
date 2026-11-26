// Package store opens Flow's SQLite database (pure-Go ncruces driver, kit's DSN) and applies the embedded
// migrations. The row types and the store interfaces live with the areas that own them (profile, credentials,
// projects, sandboxes, sessions, each in its model.go and store.go); docs/schema.md describes them all.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	_ "github.com/ncruces/go-sqlite3/driver" // registers "sqlite3", with the SQLite build it bundles
	kitmigrate "github.com/trick77/ship/kit/migrate"
	"github.com/trick77/ship/kit/sqlitex"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations is the embedded migrations directory, files at its root as kit's runner reads them.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err) // embedded at build time; a programmer error
	}
	return sub
}

// Open opens the database at path, migrated to the current schema. A write transaction takes its lock at BEGIN
// (_txlock=immediate), never upgrading into SQLITE_BUSY halfway.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", sqlitex.DSN(path, "_txlock=immediate"))
	if err != nil {
		return nil, err
	}
	if _, err := kitmigrate.Up(ctx, db, Migrations(), kitmigrate.Options{Dialect: kitmigrate.SQLite}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return db, nil
}
