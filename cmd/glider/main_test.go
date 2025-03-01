package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithScrapeCursor(t *testing.T) {
	calls := []int{}
	dir := t.TempDir() + "/test.cursor"
	for range 3 {
		withScrapeCursor(dir, func(last int) int {
			calls = append(calls, last)
			return last + 2
		})
	}
	require.Equal(t, []int{0, 2, 4}, calls)
}
