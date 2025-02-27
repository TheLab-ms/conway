package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestValueSigner(t *testing.T) {
	v := &ValueSigner[int64]{}
	key := []byte("test")

	val, ok := v.Verify(v.Sign(123, key, time.Hour), key)
	assert.True(t, ok)
	assert.Equal(t, int64(123), val)

	_, ok = v.Verify(v.Sign(123, []byte("not it"), time.Hour), key)
	assert.False(t, ok)

	_, ok = v.Verify(v.Sign(123, key, -time.Second), key)
	assert.False(t, ok)

	_, ok = v.Verify("invalid", key)
	assert.False(t, ok)

	_, ok = v.Verify("inv@lid.sig", key)
	assert.False(t, ok)
}
