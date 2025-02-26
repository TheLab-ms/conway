package keyfob

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFindTrustedIP(t *testing.T) {
	m := New(nil, nil, "google.com")
	assert.False(t, m.findTrustedIP(context.Background()))
	assert.NotNil(t, *m.trustedIP.Load())
}

func TestSignatureValidation(t *testing.T) {
	m := New(nil, &url.URL{}, "")

	// Happy path
	signed := m.signQR("123", time.Now().Add(time.Hour))
	actual, ok := m.verifyQR(signed)
	assert.True(t, ok)
	assert.Equal(t, int64(123), actual)

	// No signature
	_, ok = m.verifyQR("123")
	assert.False(t, ok)

	// Expired
	_, ok = m.verifyQR(m.signQR("123", time.Now().Add(-time.Hour)))
	assert.False(t, ok)

	// Bad key
	m.initSigningKey()
	_, ok = m.verifyQR(signed)
	assert.False(t, ok)
}
