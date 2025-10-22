package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var BaseMigration string

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?cache=shared&mode=rwc&_journal_mode=WAL", path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, err
}

func OpenTest(t *testing.T) *sql.DB {
	path := filepath.Join(t.TempDir(), "db")
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?cache=shared&mode=rwc&_journal_mode=WAL", path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func MustMigrate(db *sql.DB, migration string) {
	_, err := db.Exec(migration)
	if err != nil {
		panic(fmt.Errorf("error while migrating database: %s", err))
	}
}

// deprecated
func New(path string) (*sql.DB, error) {
	return Open(path)
}

func NewTest(t *testing.T) *sql.DB {
	db := newTest(t, filepath.Join(t.TempDir(), "test.db"))
	MustMigrate(db, BaseMigration)
	return db
}

func newTest(t *testing.T, file string) *sql.DB {
	db, err := New(file)
	if err != nil {
		t.Fatalf("creating db: %s", err)
	}
	return db
}
