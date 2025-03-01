package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenIssuerBasics(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "test.key")
	i := NewTokenIssuer(keyPath)

	tok, err := i.Sign(&jwt.RegisteredClaims{
		Subject: "test",
	})
	require.NoError(t, err)

	// Success
	claims, err := i.Verify(tok)
	require.NoError(t, err)
	assert.Equal(t, "test", claims.Subject)

	// Malformed token
	_, err = i.Verify("invalid token")
	require.Error(t, err)

	// Expired
	tok, err = i.Sign(&jwt.RegisteredClaims{
		Subject:   "test",
		ExpiresAt: jwt.NewNumericDate(time.Time{}),
	})
	require.NoError(t, err)

	_, err = i.Verify(tok)
	require.Error(t, err)
}
