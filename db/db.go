package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

//go:embed migration.sql
var migration string

func New(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?cache=shared&mode=rwc&_journal_mode=WAL", path))
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}
	db.SetMaxOpenConns(1)

	_, err = db.Exec(migration)
	if err != nil {
		return nil, fmt.Errorf("migrating db: %w", err)
	}

	return db, nil
}

func NewTest(t *testing.T) *sql.DB {
	db, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("creating db: %s", err)
	}
	return db
}
