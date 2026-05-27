package engine

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"os"
)

// Ed25519Signer signs byte slices using an Ed25519 private key kept on disk.
//
// Unlike TokenIssuer (which uses RSA + PEM for compatibility with the JWT
// library), Ed25519Signer stores the raw 32-byte seed in a binary file. This
// keeps the on-disk format trivially parseable by other languages/devices and
// matches the size constraints of the access-controller firmware.
type Ed25519Signer struct {
	priv ed25519.PrivateKey
}

// NewEd25519Signer returns a signer whose key is loaded from keyFile. If the
// file does not exist, a fresh key is generated and written with mode 0600.
// Any I/O or parse error panics.
func NewEd25519Signer(keyFile string) *Ed25519Signer {
	s := &Ed25519Signer{}
	s.loadOrGenerate(keyFile)
	return s
}

func (s *Ed25519Signer) loadOrGenerate(file string) {
	seed, err := os.ReadFile(file)
	if err == nil {
		if len(seed) != ed25519.SeedSize {
			panic("ed25519 key file has wrong size")
		}
		s.priv = ed25519.NewKeyFromSeed(seed)
		return
	}
	if !os.IsNotExist(err) {
		panic(err)
	}

	slog.Info("generating Ed25519 key", "file", file)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(file, priv.Seed(), 0600); err != nil {
		panic(err)
	}
	s.priv = priv
}

// Sign returns the raw 64-byte Ed25519 signature over msg.
func (s *Ed25519Signer) Sign(msg []byte) []byte {
	return ed25519.Sign(s.priv, msg)
}

// PublicKey returns the raw 32-byte public key.
func (s *Ed25519Signer) PublicKey() []byte {
	pub := s.priv.Public().(ed25519.PublicKey)
	return []byte(pub)
}

// PublicKeyBase64 returns the public key encoded as standard base64 (no
// padding stripped). Suitable for displaying in an admin UI and copy-pasting
// into device configuration forms.
func (s *Ed25519Signer) PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(s.PublicKey())
}
