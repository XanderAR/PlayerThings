// Package linktoken signs the short-lived URLs that tie a website visit to the
// Discord user who ran /link, so nobody needs to sign in with Discord twice.
package linktoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalid = errors.New("invalid link token")
	ErrExpired = errors.New("link token expired")
)

// Claims is what a link token carries.
type Claims struct {
	DiscordID string
	GuildID   string // the server /link was run in; empty in DMs
	Expires   time.Time
}

// Sign returns a URL-safe token for c.
func Sign(secret []byte, c Claims) string {
	payload := strings.Join([]string{c.DiscordID, c.GuildID, strconv.FormatInt(c.Expires.Unix(), 10)}, "|")
	p := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return p + "." + base64.RawURLEncoding.EncodeToString(mac(secret, p))
}

// Verify checks the signature and expiry of token.
func Verify(secret []byte, token string, now time.Time) (Claims, error) {
	p, sig, ok := strings.Cut(token, ".")
	if !ok {
		return Claims{}, ErrInvalid
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(secret, p)) {
		return Claims{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return Claims{}, ErrInvalid
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 || parts[0] == "" {
		return Claims{}, ErrInvalid
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Claims{}, ErrInvalid
	}
	c := Claims{DiscordID: parts[0], GuildID: parts[1], Expires: time.Unix(exp, 0)}
	if now.After(c.Expires) {
		return c, ErrExpired
	}
	return c, nil
}

func mac(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}
