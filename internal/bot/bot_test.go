package bot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"playradar/internal/discord"
	"playradar/internal/store"
)

var now = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) // a Sunday

type fakeDiscord struct {
	mu        sync.Mutex
	edits     []discord.ResponseData
	posts     []discord.WebhookMessage
	created   []string
	deleted   []discord.Webhook
	createErr error
	postErr   error // returned by ExecuteWebhook when set
}

func (f *fakeDiscord) EditOriginal(_ context.Context, _ string, d discord.ResponseData) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, d)
	return nil
}
func (f *fakeDiscord) CreateWebhook(_ context.Context, channelID, _ string) (discord.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return discord.Webhook{}, f.createErr
	}
	f.created = append(f.created, channelID)
	return discord.Webhook{ID: "wh" + channelID, Token: "tok"}, nil
}
func (f *fakeDiscord) DeleteWebhook(_ context.Context, w discord.Webhook) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, w)
	return nil
}
func (f *fakeDiscord) ExecuteWebhook(_ context.Context, _ discord.Webhook, m discord.WebhookMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postErr != nil {
		return f.postErr
	}
	f.posts = append(f.posts, m)
	return nil
}

type fakeSyncer struct {
	mu     sync.Mutex
	soon   []string
	synced []string
	mp     map[string]bool
	onSync func(id string) error // what a synchronous Sync does; nil succeeds without changes
}

func (f *fakeSyncer) Sync(_ context.Context, id, _ string) (store.Profile, int, error) {
	f.mu.Lock()
	f.synced = append(f.synced, id)
	on := f.onSync
	f.mu.Unlock()
	if on != nil {
		if err := on(id); err != nil {
			return store.Profile{}, 0, err
		}
	}
	return store.Profile{}, 0, nil
}

func (f *fakeSyncer) SyncSoon(id, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.soon = append(f.soon, id)
}
func (f *fakeSyncer) CheckGame(_ context.Context, id string) (*bool, error) {
	v, ok := f.mp[id]
	if !ok {
		return nil, nil
	}
	return &v, nil
}

type harness struct {
	b      *Bot
	st     *store.Store
	priv   ed25519.PrivateKey
	dc     *fakeDiscord
	sy     *fakeSyncer
	tests  string
	secret []byte
}

const playtestsJSON = `[
 {"title":"Starfall Outriders","studio":"Lumen Forge","url":"https://player.gg/explore/games/1-starfall","summary":"Co-op shooter.","platforms":["PC"],"genres":["Shooter","Co-op"],"starts":"2026-11-10","ends":"2026-11-30","similar_steam_apps":["548430"]},
 {"title":"Mossbound","url":"https://player.gg/explore/games/2-mossbound","platforms":["PC","PS5"],"genres":["Cozy"],"starts":"2026-11-20","ends":"2026-12-02"},
 {"title":"Neon Courier","url":"https://player.gg/explore/games/3-neon","genres":["Racing"],"starts":"2026-12-05"},
 {"title":"Old Test","url":"https://player.gg/explore/games/4-old","starts":"2026-10-01","ends":"2026-10-10"}
]`

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tests := filepath.Join(dir, "playtests.json")
	os.WriteFile(tests, []byte(playtestsJSON), 0o644)

	pub, priv, _ := ed25519.GenerateKey(nil)
	h := &harness{st: st, priv: priv, dc: &fakeDiscord{}, sy: &fakeSyncer{mp: map[string]bool{"548430": true, "1966720": true, "1145360": false}}, tests: tests,
		secret: []byte("0123456789abcdef0123456789abcdef")}
	h.b = &Bot{
		AppName: "PlayRadar", InstallURL: "https://discord.com/oauth2/authorize?client_id=1",
		Store: st, Syncer: h.sy, Discord: h.dc, PublicKey: pub, LinkSecret: h.secret,
		Links:         Links{Site: "https://bot.example", PlayerGG: "https://player.gg"},
		PlaytestsFile: tests, PlaytestsSource: "curated list",
		Now: func() time.Time { return now },
	}
	h.seed(t)
	return h
}

// seed links four users. 100, 200 and 300 are visible in server G; 400 is hidden.
func (h *harness) seed(t *testing.T) {
	ctx := context.Background()
	type g struct {
		id, name      string
		total, recent int
	}
	users := map[string][]g{
		"100": {{"548430", "Deep Rock Galactic", 24720, 360}, {"730", "Counter-Strike 2", 18060, 0}, {"1145360", "Hades II", 5760, 600}, {"1966720", "Lethal Company", 1000, 0}},
		"200": {{"548430", "Deep Rock Galactic", 17280, 660}, {"553850", "Helldivers 2", 6000, 1200}, {"1966720", "Lethal Company", 2000, 0}},
		"300": {{"548430", "Deep Rock Galactic", 8400, 0}, {"2379780", "Balatro", 1200, 900}},
		"400": {{"548430", "Deep Rock Galactic", 99999, 9999}},
	}
	for id, games := range users {
		if err := h.st.LinkAccount(ctx, store.Account{DiscordID: id, Platform: "steam", ExternalID: "7656119800000" + id, LinkedAt: now}); err != nil {
			t.Fatal(err)
		}
		var gs []store.Game
		var es []store.LibraryEntry
		for _, x := range games {
			gs = append(gs, store.Game{GameID: x.id, Name: x.name, ImageURL: "https://img/" + x.id})
			es = append(es, store.LibraryEntry{GameID: x.id, MinutesTotal: x.total, MinutesRecent: x.recent})
		}
		if err := h.st.SaveSync(ctx, id, "steam", store.Profile{DisplayName: "steam" + id, AvatarURL: "https://avatar/" + id}, gs, es, now); err != nil {
			t.Fatal(err)
		}
		if err := h.st.SetVisibility(ctx, "G", id, id != "400", now); err != nil {
			t.Fatal(err)
		}
	}
}

type rawResponse struct {
	Type int             `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (h *harness) send(t *testing.T, in map[string]any) (rawResponse, int) {
	t.Helper()
	body, _ := json.Marshal(in)
	ts := strconv.FormatInt(now.Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/interactions", bytes.NewReader(body))
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(ed25519.Sign(h.priv, append([]byte(ts), body...))))
	req.Header.Set("X-Signature-Timestamp", ts)
	rec := httptest.NewRecorder()
	h.b.ServeHTTP(rec, req)
	var r rawResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return r, rec.Code
}

func messageData(t *testing.T, r rawResponse) discord.ResponseData {
	t.Helper()
	var d discord.ResponseData
	if err := json.Unmarshal(r.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func cmd(name, guild, user string, perms string, options ...map[string]any) map[string]any {
	in := map[string]any{
		"type":  discord.InteractionApplicationCommand,
		"token": "itok",
		"data":  map[string]any{"name": name, "options": options},
	}
	u := map[string]any{"id": user, "username": "user" + user, "global_name": "name" + user}
	if guild != "" {
		in["guild_id"] = guild
		in["member"] = map[string]any{"user": u, "permissions": perms}
	} else {
		in["user"] = u
	}
	return in
}

func opt(name string, typ int, value any) map[string]any {
	return map[string]any{"name": name, "type": typ, "value": value}
}

func TestPingAndSignature(t *testing.T) {
	h := newHarness(t)
	r, code := h.send(t, map[string]any{"type": 1})
	if code != 200 || r.Type != discord.ResponsePong {
		t.Fatalf("ping: code %d type %d", code, r.Type)
	}

	req := httptest.NewRequest(http.MethodPost, "/interactions", strings.NewReader(`{"type":1}`))
	req.Header.Set("X-Signature-Ed25519", strings.Repeat("00", 64))
	req.Header.Set("X-Signature-Timestamp", "1")
	rec := httptest.NewRecorder()
	h.b.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature: code %d", rec.Code)
	}

	// A correctly signed request from 10 minutes ago is a replay.
	body := []byte(`{"type":1}`)
	old := strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10)
	req = httptest.NewRequest(http.MethodPost, "/interactions", bytes.NewReader(body))
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(ed25519.Sign(h.priv, append([]byte(old), body...))))
	req.Header.Set("X-Signature-Timestamp", old)
	rec = httptest.NewRecorder()
	h.b.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed request: code %d", rec.Code)
	}
}
