// Package linktoken signs the short-lived URLs that tie a website visit to the
// Discord user who ran /link, so nobody needs to sign in with Discord twice.
package linktoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	Name      string // the Discord name shown on the link page, so people can spot a link meant for someone else
	Expires   time.Time
}

// Sign returns a URL-safe token for c.
func Sign(secret []byte, c Claims) string {
	payload := strings.Join([]string{c.DiscordID, c.GuildID, strconv.FormatInt(c.Expires.Unix(), 10),
		base64.RawURLEncoding.EncodeToString([]byte(c.Name))}, "|")
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
	if (len(parts) != 3 && len(parts) != 4) || parts[0] == "" {
		return Claims{}, ErrInvalid
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Claims{}, ErrInvalid
	}
	c := Claims{DiscordID: parts[0], GuildID: parts[1], Expires: time.Unix(exp, 0)}
	if len(parts) == 4 {
		name, err := base64.RawURLEncoding.DecodeString(parts[3])
		if err != nil {
			return Claims{}, ErrInvalid
		}
		c.Name = string(name)
	}
	if now.After(c.Expires) {
		return c, ErrExpired
	}
	return c, nil
}

// SignClick signs a /go click link, so only links the app made are counted.
func SignClick(secret []byte, target, command, guildID string) string {
	return hex.EncodeToString(mac(secret, "click|"+target+"|"+command+"|"+guildID)[:8])
}

// VerifyClick checks a signature from SignClick.
func VerifyClick(secret []byte, target, command, guildID, sig string) bool {
	return hmac.Equal([]byte(SignClick(secret, target, command, guildID)), []byte(sig))
}

func mac(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}
