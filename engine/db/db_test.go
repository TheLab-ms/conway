package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDB(t *testing.T) {
	file := filepath.Join(t.TempDir(), "test.db")
	db1, err := Open(file)
	require.NoError(t, err)
	db1.Close()

	db2, err := Open(file)
	require.NoError(t, err)
	db2.Close()
}
