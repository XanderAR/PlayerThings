package linktoken

import (
	"errors"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	tok := Sign(secret, Claims{DiscordID: "111", GuildID: "222", Expires: now.Add(time.Minute)})

	c, err := Verify(secret, tok, now)
	if err != nil {
		t.Fatal(err)
	}
	if c.DiscordID != "111" || c.GuildID != "222" {
		t.Fatalf("claims = %+v", c)
	}

	if _, err := Verify(secret, tok, now.Add(2*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired token: err = %v", err)
	}
	if _, err := Verify([]byte("another-secret-another-secret-xx"), tok, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong secret: err = %v", err)
	}
	tampered := "x" + tok[1:]
	if _, err := Verify(secret, tampered, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered token: err = %v", err)
	}
	if _, err := Verify(secret, "not-a-token", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("garbage: err = %v", err)
	}
}

func TestDMTokenHasNoGuild(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	c, err := Verify(secret, Sign(secret, Claims{DiscordID: "5", Expires: now.Add(time.Hour)}), now)
	if err != nil || c.GuildID != "" {
		t.Fatalf("c = %+v, err = %v", c, err)
	}
}
