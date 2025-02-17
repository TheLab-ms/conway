package keyfob

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFindTrustedIP(t *testing.T) {
	m := New(nil, "google.com")
	assert.False(t, m.findTrustedIP(context.Background()))
	assert.NotNil(t, *m.trustedIP.Load())
}
