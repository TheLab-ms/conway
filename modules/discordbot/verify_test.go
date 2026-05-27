package discordbot

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// signed returns a (publicKeyHex, signatureHex) pair for the timestamp+body
// concatenation, mirroring exactly what Discord sends in its interaction
// request headers.
func signed(t *testing.T, timestamp string, body []byte) (pubHex, sigHex string, priv ed25519.PrivateKey, pub ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	msg := append([]byte(timestamp), body...)
	sig := ed25519.Sign(priv, msg)
	return hex.EncodeToString(pub), hex.EncodeToString(sig), priv, pub
}

func TestVerifySignature_RoundTrip(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":1}`)
	ts := "1234567890"
	pubHex, sigHex, _, _ := signed(t, ts, body)

	require.NoError(t, verifySignature(pubHex, sigHex, ts, body))
}

func TestVerifySignature_TamperedBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":1}`)
	ts := "1234567890"
	pubHex, sigHex, _, _ := signed(t, ts, body)

	tampered := []byte(`{"type":2}`)
	err := verifySignature(pubHex, sigHex, ts, tampered)
	require.ErrorIs(t, err, errBadSignature)
}

func TestVerifySignature_TamperedTimestamp(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":1}`)
	ts := "1234567890"
	pubHex, sigHex, _, _ := signed(t, ts, body)

	err := verifySignature(pubHex, sigHex, "9999999999", body)
	require.ErrorIs(t, err, errBadSignature)
}

func TestVerifySignature_WrongKey(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":1}`)
	ts := "1234567890"
	_, sigHex, _, _ := signed(t, ts, body)

	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	err = verifySignature(hex.EncodeToString(otherPub), sigHex, ts, body)
	require.ErrorIs(t, err, errBadSignature)
}

func TestVerifySignature_MissingPublicKey(t *testing.T) {
	t.Parallel()
	err := verifySignature("", "00", "0", []byte("x"))
	require.Error(t, err)
}

func TestVerifySignature_MalformedPublicKey(t *testing.T) {
	t.Parallel()
	// Not valid hex.
	err := verifySignature("zz", "00", "0", []byte("x"))
	require.ErrorIs(t, err, errInvalidPublicKey)

	// Valid hex, wrong length.
	err = verifySignature("aabb", "00", "0", []byte("x"))
	require.ErrorIs(t, err, errInvalidPublicKey)
}

func TestVerifySignature_MalformedSignature(t *testing.T) {
	t.Parallel()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)

	// Not valid hex.
	err = verifySignature(pubHex, "zz", "0", []byte("x"))
	require.ErrorIs(t, err, errInvalidSignature)

	// Valid hex, wrong length.
	err = verifySignature(pubHex, "aabb", "0", []byte("x"))
	require.ErrorIs(t, err, errInvalidSignature)
}
