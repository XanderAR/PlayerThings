package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

// ParsePublicKey decodes the hex public key from the Developer Portal.
func ParsePublicKey(hexKey string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, errors.New("discord public key must be 32 bytes")
	}
	return ed25519.PublicKey(b), nil
}

// Verify checks Discord's Ed25519 signature on an interaction request.
// Discord sends deliberately bad signatures to test endpoints, so every
// request must pass this before it is handled.
func Verify(key ed25519.PublicKey, r *http.Request, body []byte) bool {
	sig, err := hex.DecodeString(r.Header.Get("X-Signature-Ed25519"))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	ts := r.Header.Get("X-Signature-Timestamp")
	if ts == "" {
		return false
	}
	msg := make([]byte, 0, len(ts)+len(body))
	msg = append(msg, ts...)
	msg = append(msg, body...)
	return ed25519.Verify(key, msg, sig)
}
