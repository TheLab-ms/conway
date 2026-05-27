package discordbot

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
)

// verifySignature validates Discord's Ed25519 signature on an interaction
// request. The signed message is the concatenation of the X-Signature-Timestamp
// header and the raw request body.
//
// See https://discord.com/developers/docs/interactions/receiving-and-responding#security-and-authorization
func verifySignature(publicKeyHex, signatureHex, timestamp string, body []byte) error {
	if publicKeyHex == "" {
		return errors.New("application public key not configured")
	}
	pubKey, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return errInvalidPublicKey
	}
	if len(pubKey) != ed25519.PublicKeySize {
		return errInvalidPublicKey
	}

	sig, err := hex.DecodeString(signatureHex)
	if err != nil {
		return errInvalidSignature
	}
	if len(sig) != ed25519.SignatureSize {
		return errInvalidSignature
	}

	msg := make([]byte, 0, len(timestamp)+len(body))
	msg = append(msg, timestamp...)
	msg = append(msg, body...)

	if !ed25519.Verify(ed25519.PublicKey(pubKey), msg, sig) {
		return errBadSignature
	}
	return nil
}

var (
	errInvalidPublicKey = errors.New("invalid application public key")
	errInvalidSignature = errors.New("invalid signature header")
	errBadSignature     = errors.New("signature verification failed")
)
