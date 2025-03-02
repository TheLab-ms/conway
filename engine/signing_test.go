package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestValueSigner(t *testing.T) {
	v := NewValueSigner[int]()

	t.Run("happy path", func(t *testing.T) {
		val, ok := v.Verify(v.Sign(42, time.Hour))
		assert.True(t, ok)
		assert.Equal(t, 42, val)
	})

	t.Run("expired", func(t *testing.T) {
		val, ok := v.Verify(v.Sign(42, -time.Second))
		assert.False(t, ok)
		assert.Equal(t, 0, val)
	})

	t.Run("invalid str", func(t *testing.T) {
		val, ok := v.Verify("invalid")
		assert.False(t, ok)
		assert.Equal(t, 0, val)
	})

	t.Run("wrong key", func(t *testing.T) {
		str := v.Sign(42, time.Hour)
		v.initSigningKey()
		val, ok := v.Verify(str)
		assert.False(t, ok)
		assert.Equal(t, 0, val)
	})
}
