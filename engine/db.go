package engine

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// OpenDB opens a SQLite database at the given path.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?cache=shared&mode=rwc&_journal_mode=WAL", path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, err
}

// OpenTestDB creates a test database in a temporary directory.
func OpenTestDB(t *testing.T) *sql.DB {
	path := filepath.Join(t.TempDir(), "db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// MustMigrate applies a migration to the database, panicking on error.
func MustMigrate(db *sql.DB, migration string) {
	_, err := db.Exec(migration)
	if err != nil {
		panic(fmt.Errorf("error while migrating database: %s", err))
	}
}
