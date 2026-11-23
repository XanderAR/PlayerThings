package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"playradar/internal/linktoken"
	"playradar/internal/store"
)

// fakeDiscordAPI plays Discord's OAuth2 and user endpoints.
type fakeDiscordAPI struct {
	mu      sync.Mutex
	userID  string
	conns   []connection
	revoked []string
	codes   []string
}

func (f *fakeDiscordAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/oauth2/token":
			id, secret, ok := r.BasicAuth()
			r.ParseForm()
			if !ok || id != "app" || secret != "shh" || r.Form.Get("redirect_uri") != "https://bot.example/link/discord/callback" {
				http.Error(w, "bad client", http.StatusUnauthorized)
				return
			}
			f.codes = append(f.codes, r.Form.Get("code"))
			json.NewEncoder(w).Encode(map[string]string{"access_token": "AT-" + r.Form.Get("code"), "token_type": "Bearer"})
		case "/oauth2/token/revoke":
			r.ParseForm()
			f.revoked = append(f.revoked, r.Form.Get("token"))
		case "/users/@me":
			json.NewEncoder(w).Encode(map[string]string{"id": f.userID})
		case "/users/@me/connections":
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "no token", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(f.conns)
		default:
			http.NotFound(w, r)
		}
	})
}

func discordServer(t *testing.T, api *fakeDiscordAPI) (*Server, *store.Store) {
	t.Helper()
	s, st := newServer(t, fakeSteam{}, fakeSyncer{profile: store.Profile{DisplayName: "Mara"}, games: 12})
	srv := httptest.NewServer(api.handler(t))
	t.Cleanup(srv.Close)
	s.Discord = &DiscordOAuth{ClientID: "app", ClientSecret: "shh", API: srv.URL, AuthorizeURL: "https://discord.example/oauth2/authorize", HTTP: srv.Client()}
	return s, st
}

func linkToken(s *Server, discordID, guildID string) string {
	return linktoken.Sign(s.LinkSecret, linktoken.Claims{DiscordID: discordID, GuildID: guildID, Expires: now.Add(time.Minute)})
}

func TestDiscordLinkStartRedirectsToConsent(t *testing.T) {
	s, _ := discordServer(t, &fakeDiscordAPI{})
	tok := linkToken(s, "100", "G")
	rec := get(t, s, "/link/discord?t="+url.QueryEscape(tok))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if rec.Code != http.StatusFound || loc.Host != "discord.example" || q.Get("client_id") != "app" ||
		q.Get("scope") != "identify connections" || q.Get("state") != tok || q.Get("redirect_uri") != "https://bot.example/link/discord/callback" {
		t.Fatalf("redirect = %d %s", rec.Code, loc)
	}
	// Without a client secret it falls back to the Steam page.
	s.Discord = nil
	rec = get(t, s, "/link/discord?t="+url.QueryEscape(tok))
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/link?t=") {
		t.Fatalf("fallback = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// The link page offers both ways when it's on.
	s2, _ := discordServer(t, &fakeDiscordAPI{})
	body := get(t, s2, "/link?t="+url.QueryEscape(tok)).Body.String()
	if !strings.Contains(body, "Use the Steam account on my Discord") || !strings.Contains(body, "Sign in through Steam") {
		t.Fatalf("link page = %s", body)
	}
}

func TestDiscordLinkUsesVerifiedSteamConnection(t *testing.T) {
	api := &fakeDiscordAPI{userID: "100", conns: []connection{
		{Type: "twitch", ID: "tw1", Verified: true},
		{Type: "steam", ID: "76561198000000001", Verified: false}, // not verified: ignored
		{Type: "steam", ID: "76561198000000009", Name: "Mara", Verified: true},
	}}
	s, st := discordServer(t, api)
	tok := linkToken(s, "100", "G")
	rec := get(t, s, "/link/discord/callback?code=abc&state="+url.QueryEscape(tok))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Linked as Mara") {
		t.Fatalf("callback = %d %s", rec.Code, rec.Body.String())
	}
	acct, ok, _ := st.Account(context.Background(), "100", "steam")
	if !ok || acct.ExternalID != "76561198000000009" {
		t.Fatalf("account = %+v %v", acct, ok)
	}
	if visible, _ := st.Visible(context.Background(), "G", "100"); !visible {
		t.Fatal("linking from a server should opt in there")
	}
	if len(api.revoked) != 1 || api.revoked[0] != "AT-abc" {
		t.Fatalf("access token wasn't revoked: %v", api.revoked)
	}
}

func TestDiscordLinkRefusesSomeoneElsesLink(t *testing.T) {
	api := &fakeDiscordAPI{userID: "999", conns: []connection{{Type: "steam", ID: "76561198000000009", Verified: true}}}
	s, st := discordServer(t, api)
	rec := get(t, s, "/link/discord/callback?code=abc&state="+url.QueryEscape(linkToken(s, "100", "")))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "different Discord account") {
		t.Fatalf("mismatch = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := st.Account(context.Background(), "100", "steam"); ok {
		t.Fatal("linked across accounts")
	}
	if _, ok, _ := st.Account(context.Background(), "999", "steam"); ok {
		t.Fatal("linked the wrong person")
	}
}

func TestDiscordLinkWithoutSteamOffersSteamSignIn(t *testing.T) {
	api := &fakeDiscordAPI{userID: "100", conns: []connection{{Type: "xbox", ID: "x", Verified: true}}}
	s, st := discordServer(t, api)
	tok := linkToken(s, "100", "")
	rec := get(t, s, "/link/discord/callback?code=abc&state="+url.QueryEscape(tok))
	body := rec.Body.String()
	if !strings.Contains(body, "No Steam account on your Discord") || !strings.Contains(body, "Sign in through Steam instead") ||
		!strings.Contains(body, "/link?t=") {
		t.Fatalf("no steam = %s", body)
	}
	if _, ok, _ := st.Account(context.Background(), "100", "steam"); ok {
		t.Fatal("nothing should be linked")
	}
	// Pressing Cancel on Discord's screen.
	rec = get(t, s, "/link/discord/callback?error=access_denied&state="+url.QueryEscape(tok))
	if !strings.Contains(rec.Body.String(), "Discord sign-in didn&#39;t finish") || len(api.codes) != 1 {
		t.Fatalf("cancelled = %s (codes %v)", rec.Body.String(), api.codes)
	}
	// A forged or expired state is refused.
	rec = get(t, s, "/link/discord/callback?code=abc&state=forged")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forged state = %d", rec.Code)
	}
}

func TestIsSteamID(t *testing.T) {
	for id, want := range map[string]bool{"76561198000000009": true, "7656119800000000": false, "12345678901234567": false, "7656119800000000x": false} {
		if isSteamID(id) != want {
			t.Errorf("%s: want %v", id, want)
		}
	}
}
