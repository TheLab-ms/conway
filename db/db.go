package db

import (
	"database/sql"
	"embed"
	_ "embed"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"
)

//go:embed *.sql
var migrations embed.FS

func New(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?cache=shared&mode=rwc&_journal_mode=WAL", path))
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}
	db.SetMaxOpenConns(1)

	// File all of the migration fileMeta
	fileMeta, err := migrations.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("listing migrations: %w", err)
	}
	sort.Slice(fileMeta, func(i, j int) bool {
		return fileMeta[i].Name() < fileMeta[j].Name()
	})
	files := make([]string, len(fileMeta))
	for i, file := range fileMeta {
		migration, err := migrations.ReadFile(file.Name())
		if err != nil {
			return nil, fmt.Errorf("reading migration: %w", err)
		}
		files[i] = string(migration)
	}

	// Migrate the database in a transaction
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("starting txn: %w", err)
	}
	defer tx.Rollback()

	for i, meta := range fileMeta {
		_, err = tx.Exec("INSERT INTO migrations (name) VALUES (?)", meta.Name())
		if err != nil && meta.Name() != "01-init.sql" {
			continue
		}
		_, err = tx.Exec(files[i])
		if err != nil {
			return nil, fmt.Errorf("migrating db: %w", err)
		}
	}

	return db, tx.Commit()
}

func NewTest(t *testing.T) *sql.DB {
	return newTest(t, filepath.Join(t.TempDir(), "test.db"))
}

func newTest(t *testing.T, file string) *sql.DB {
	db, err := New(file)
	if err != nil {
		t.Fatalf("creating db: %s", err)
	}
	return db
}
