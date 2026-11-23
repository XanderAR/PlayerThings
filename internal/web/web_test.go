package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"playradar/internal/linktoken"
	"playradar/internal/render"
	"playradar/internal/store"
)

var now = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

type fakeSteam struct {
	id  string
	err error
}

func (f fakeSteam) LoginURL(returnTo, realm string) string {
	return "https://steamcommunity.com/openid/login?return_to=" + url.QueryEscape(returnTo)
}
func (f fakeSteam) VerifyLogin(_ context.Context, _ url.Values, _ string) (string, error) {
	return f.id, f.err
}

type fakeSyncer struct {
	profile store.Profile
	games   int
	err     error
}

func (f fakeSyncer) Sync(context.Context, string, string) (store.Profile, int, error) {
	return f.profile, f.games, f.err
}

func newServer(t *testing.T, steam SteamAuth, sync Syncer) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{
		AppName: "Player.gg", BaseURL: "https://bot.example", PlayerGGURL: "https://player.gg",
		Store: st, Steam: steam, Syncer: sync, LinkSecret: []byte("0123456789abcdef0123456789abcdef"),
		Interactions: http.NotFoundHandler(), Now: func() time.Time { return now },
	}, st
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestShelfImageNeedsAValidSignature(t *testing.T) {
	s, _ := newServer(t, fakeSteam{}, fakeSyncer{})
	art := httptest.NewServer(http.NotFoundHandler()) // no art: tiles render empty
	defer art.Close()
	s.Render = render.New()
	s.Render.Cover = func(id string) string { return art.URL + "/c/" + id }
	s.Render.Fallback = func(id string) string { return art.URL + "/h/" + id }

	url := render.ShelfURL("", s.LinkSecret, render.Strip, []render.Tile{{AppID: "548430", Minutes: 600}})
	rec := get(t, s, url)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.Len() < 1000 {
		t.Fatalf("shelf = %d %s (%d bytes)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec := get(t, s, strings.Replace(url, "548430.600", "548430.601", 1)); rec.Code != http.StatusForbidden {
		t.Fatalf("tampered shelf = %d", rec.Code)
	}
}

func TestGoRedirectOnlyAllowsPlayerGG(t *testing.T) {
	s, st := newServer(t, fakeSteam{}, fakeSyncer{})
	raw := "https://player.gg/explore/games/548430.steam"
	target := url.QueryEscape(raw)
	sig := linktoken.SignClick(s.LinkSecret, raw, "whoplays", "123")
	rec := get(t, s, "/go?u="+target+"&c=whoplays&g=123&s="+sig)
	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if loc.Host != "player.gg" || q.Get("utm_source") != "discord" || q.Get("utm_medium") != "app" || q.Get("utm_campaign") != "whoplays" || q.Get("utm_content") != "123" {
		t.Fatalf("location = %s", loc)
	}
	rows, _ := st.UsageSince(context.Background(), "2026-11-01")
	if len(rows) != 1 || rows[0].Kind != store.UsageClick || rows[0].Count != 1 {
		t.Fatalf("usage = %+v", rows)
	}

	// Made-up links still redirect, but aren't counted (so nobody can fill the
	// database with random ones).
	for i := 0; i < 5; i++ {
		rec := get(t, s, fmt.Sprintf("/go?u=%s&c=whoplays&g=%d", target, 900+i))
		if rec.Code != http.StatusFound {
			t.Fatalf("unsigned link: code %d", rec.Code)
		}
	}
	if rows, _ := st.UsageSince(context.Background(), "2026-11-01"); len(rows) != 1 || rows[0].Count != 1 {
		t.Fatalf("unsigned clicks were counted: %+v", rows)
	}

	for _, bad := range []string{"https://evil.example/x", "http://player.gg/x", "https://notplayer.gg/x", "javascript:alert(1)", ""} {
		if rec := get(t, s, "/go?u="+url.QueryEscape(bad)); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: code = %d", bad, rec.Code)
		}
	}
}

func TestLinkPage(t *testing.T) {
	s, _ := newServer(t, fakeSteam{}, fakeSyncer{})
	tok := linktoken.Sign(s.LinkSecret, linktoken.Claims{DiscordID: "100", GuildID: "G", Name: "Mara <3", Expires: now.Add(time.Minute)})
	rec := get(t, s, "/link?t="+url.QueryEscape(tok))
	body := rec.Body.String()
	// The page names the Discord account, so a link sent by someone else stands out.
	if rec.Code != 200 || !strings.Contains(body, "Sign in through Steam") || !strings.Contains(body, "leaderboards") ||
		!strings.Contains(body, "Discord account <b>Mara &lt;3</b>") {
		t.Fatalf("code %d body %s", rec.Code, body)
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("link page must not leak its URL as a referrer")
	}

	expired := linktoken.Sign(s.LinkSecret, linktoken.Claims{DiscordID: "100", Expires: now.Add(-time.Minute)})
	if rec := get(t, s, "/link?t="+url.QueryEscape(expired)); rec.Code != 400 || !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("expired: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSteamCallbackLinksAndOptsIntoServer(t *testing.T) {
	s, st := newServer(t, fakeSteam{id: "76561198000000009"}, fakeSyncer{profile: store.Profile{DisplayName: "Mara"}, games: 1284})
	tok := linktoken.Sign(s.LinkSecret, linktoken.Claims{DiscordID: "100", GuildID: "G", Expires: now.Add(time.Minute)})
	rec := get(t, s, "/link/steam/callback?t="+url.QueryEscape(tok)+"&openid.mode=id_res")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Linked as Mara") || !strings.Contains(rec.Body.String(), "1,284 games") {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	acct, ok, _ := st.Account(context.Background(), "100", "steam")
	if !ok || acct.ExternalID != "76561198000000009" {
		t.Fatalf("account = %+v", acct)
	}
	if v, _ := st.Visible(context.Background(), "G", "100"); !v {
		t.Fatal("not opted into the server /link was run in")
	}
}

func TestSteamCallbackPrivateLibrary(t *testing.T) {
	s, _ := newServer(t, fakeSteam{id: "76561198000000009"}, fakeSyncer{profile: store.Profile{DisplayName: "Mara", Private: true}})
	tok := linktoken.Sign(s.LinkSecret, linktoken.Claims{DiscordID: "100", Expires: now.Add(time.Minute)})
	rec := get(t, s, "/link/steam/callback?t="+url.QueryEscape(tok))
	if !strings.Contains(rec.Body.String(), "Game details") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestSteamCallbackFailure(t *testing.T) {
	s, st := newServer(t, fakeSteam{err: errors.New("is_valid:false")}, fakeSyncer{})
	tok := linktoken.Sign(s.LinkSecret, linktoken.Claims{DiscordID: "100", Expires: now.Add(time.Minute)})
	rec := get(t, s, "/link/steam/callback?t="+url.QueryEscape(tok))
	if rec.Code != 400 {
		t.Fatalf("code = %d", rec.Code)
	}
	if _, ok, _ := st.Account(context.Background(), "100", "steam"); ok {
		t.Fatal("linked despite failed sign-in")
	}
}

func TestStaticPages(t *testing.T) {
	s, _ := newServer(t, fakeSteam{}, fakeSyncer{})
	for _, p := range []string{"/", "/privacy", "/terms", "/healthz"} {
		if rec := get(t, s, p); rec.Code != 200 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if !strings.Contains(get(t, s, "/privacy").Body.String(), "/unlink") {
		t.Error("privacy page must explain deletion")
	}
}

func TestMediaFiles(t *testing.T) {
	s, _ := newServer(t, fakeSteam{}, fakeSyncer{})
	rec := get(t, s, "/media/banner.png")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() < 10000 {
		t.Fatalf("banner: code %d type %s size %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec := get(t, s, "/media/"); rec.Code != 404 {
		t.Fatalf("folder listing: code %d", rec.Code)
	}
	if rec := get(t, s, "/media/nope.png"); rec.Code != 404 {
		t.Fatalf("missing file: code %d", rec.Code)
	}
}
