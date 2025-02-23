package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithScrapeCursor(t *testing.T) {
	conf := &Config{StateDir: t.TempDir()}

	calls := []int{}
	for i := 0; i < 3; i++ {
		withScrapeCursor(conf, "test", func(last int) int {
			calls = append(calls, last)
			return last + 2
		})
	}
	require.Equal(t, []int{0, 2, 4}, calls)
}
