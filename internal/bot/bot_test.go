package bot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"playradar/internal/discord"
	"playradar/internal/linktoken"
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
	f.posts = append(f.posts, m)
	return nil
}

type fakeSyncer struct {
	mu   sync.Mutex
	soon []string
	mp   map[string]bool
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
	ts := "1763800000"
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
}

func TestWhoPlays(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("whoplays", "G", "100", "0", opt("game", 3, "548430")))
	d := messageData(t, r)
	if r.Type != discord.ResponseMessage || len(d.Embeds) != 1 {
		t.Fatalf("response = %+v", d)
	}
	e := d.Embeds[0]
	lines := strings.Split(e.Description, "\n")
	if e.Title != "Who plays Deep Rock Galactic" || !strings.HasPrefix(lines[0], "👑 <@100>") || !strings.Contains(lines[0], "412 h") {
		t.Fatalf("embed = %+v", e)
	}
	if strings.Contains(e.Description, "<@400>") {
		t.Fatal("hidden member listed")
	}
	if e.Footer.Text != "3 of 3 linked members own it · /link to get on the list" {
		t.Fatalf("footer = %q", e.Footer.Text)
	}
	btn := d.Components[0].Components[0]
	if !strings.HasPrefix(btn.URL, "https://bot.example/go?") || !strings.Contains(btn.URL, url.QueryEscape("https://player.gg/explore/games/548430.steam")) {
		t.Fatalf("button url = %s", btn.URL)
	}
	if d.Flags&discord.FlagEphemeral != 0 {
		t.Fatal("whoplays should be public")
	}
}

func TestWhoPlaysTypedName(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("whoplays", "G", "100", "0", opt("game", 3, "lethal")))
	d := messageData(t, r)
	if len(d.Embeds) == 0 || d.Embeds[0].Title != "Who plays Lethal Company" {
		t.Fatalf("got %+v", d)
	}
	r, _ = h.send(t, cmd("whoplays", "G", "100", "0", opt("game", 3, "no such game")))
	if d := messageData(t, r); d.Flags&discord.FlagEphemeral == 0 || !strings.Contains(d.Content, "Nobody here") {
		t.Fatalf("got %+v", d)
	}
}

func TestAutocomplete(t *testing.T) {
	h := newHarness(t)
	in := cmd("whoplays", "G", "100", "0", map[string]any{"name": "game", "type": 3, "value": "deep", "focused": true})
	in["type"] = discord.InteractionAutocomplete
	r, _ := h.send(t, in)
	var ac discord.AutocompleteData
	json.Unmarshal(r.Data, &ac)
	if r.Type != discord.ResponseAutocomplete || len(ac.Choices) != 1 || ac.Choices[0].Value != "548430" || !strings.Contains(ac.Choices[0].Name, "3 own it") {
		t.Fatalf("choices = %+v", ac.Choices)
	}
	in["guild_id"] = "elsewhere"
	r, _ = h.send(t, in)
	if !strings.Contains(string(r.Data), `"choices":[]`) {
		t.Fatalf("empty autocomplete must send an empty list: %s", r.Data)
	}
}

func TestProfile(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("profile", "G", "100", "0"))
	d := messageData(t, r)
	e := d.Embeds[0]
	if e.Title != "name100's games" || e.Fields[0].Value != "826 h" || e.Fields[1].Value != "4 games" || e.Fields[2].Value != "16 h" {
		t.Fatalf("embed = %+v", e)
	}
	if !strings.Contains(e.Fields[3].Value, "Deep Rock Galactic") || e.Image == nil {
		t.Fatalf("fields = %+v", e.Fields)
	}
	if len(h.sy.soon) != 0 {
		t.Fatal("fresh profile should not trigger a sync")
	}

	// Someone hidden in this server can't be looked up.
	r, _ = h.send(t, cmd("profile", "G", "100", "0", opt("player", 6, "400")))
	if d := messageData(t, r); d.Flags&discord.FlagEphemeral == 0 || !strings.Contains(d.Content, "hidden") {
		t.Fatalf("hidden lookup = %+v", d)
	}
	// Not linked at all.
	r, _ = h.send(t, cmd("profile", "", "999", ""))
	if d := messageData(t, r); !strings.Contains(d.Content, "/link") {
		t.Fatalf("unlinked = %+v", d)
	}
}

// A library whose first sync failed shows a "not loaded" note, not zeros,
// and asking again kicks off a retry.
func TestProfileBeforeFirstSuccessfulSync(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.st.LinkAccount(ctx, store.Account{DiscordID: "500", Platform: "steam", ExternalID: "76561198000000500", LinkedAt: now.Add(-time.Hour)})
	h.st.MarkSyncError(ctx, "500", "steam", "STEAM_API_KEY is not set", now.Add(-time.Hour))

	r, _ := h.send(t, cmd("profile", "", "500", ""))
	d := messageData(t, r)
	e := d.Embeds[0]
	if len(e.Fields) != 0 || !strings.Contains(e.Description, "hasn't loaded yet") || d.Flags&discord.FlagEphemeral == 0 {
		t.Fatalf("profile = %+v", d)
	}
	if len(h.sy.soon) != 1 {
		t.Fatalf("expected a retry, got %d syncs", len(h.sy.soon))
	}
}

func TestInCommonIsDeferredAndFindsMultiplayer(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("incommon", "G", "100", "0", opt("friend", 6, "200")))
	if r.Type != discord.ResponseDeferredMessage {
		t.Fatalf("type = %d", r.Type)
	}
	h.b.Wait()
	if len(h.dc.edits) != 1 {
		t.Fatalf("edits = %d", len(h.dc.edits))
	}
	e := h.dc.edits[0].Embeds[0]
	if e.Title != "Games you all own" || e.Fields[0].Name != "Multiplayer" {
		t.Fatalf("embed = %+v", e)
	}
	if !strings.Contains(e.Fields[0].Value, "Deep Rock Galactic") || !strings.Contains(e.Fields[0].Value, "Lethal Company") || strings.Contains(e.Fields[0].Value, "Hades") {
		t.Fatalf("multiplayer = %q", e.Fields[0].Value)
	}

	// A hidden friend is refused up front.
	r, _ = h.send(t, cmd("incommon", "G", "100", "0", opt("friend", 6, "400")))
	if d := messageData(t, r); !strings.Contains(d.Content, "<@400>") {
		t.Fatalf("hidden friend = %+v", d)
	}
}

func TestChart(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("chart", "G", "100", "0"))
	d := messageData(t, r)
	lines := strings.Split(d.Embeds[0].Description, "\n")
	if !strings.Contains(lines[0], "Helldivers 2") || !strings.Contains(lines[1], "Deep Rock Galactic") || !strings.Contains(lines[1], "2 players") {
		t.Fatalf("chart = %q", d.Embeds[0].Description)
	}
	if strings.Contains(d.Embeds[0].Description, "167 h") {
		t.Fatal("hidden member's playtime counted")
	}
	// Weekly posting needs Manage Server.
	r, _ = h.send(t, cmd("chart", "G", "100", "0", opt("weekly_in", 7, "C9")))
	if d := messageData(t, r); !strings.Contains(d.Content, "Manage Server") {
		t.Fatalf("weekly without permission = %+v", d)
	}
}

func TestVisibilityHide(t *testing.T) {
	h := newHarness(t)
	h.send(t, cmd("visibility", "G", "200", "0", opt("setting", 3, "hide")))
	r, _ := h.send(t, cmd("whoplays", "G", "100", "0", opt("game", 3, "548430")))
	if d := messageData(t, r); strings.Contains(d.Embeds[0].Description, "<@200>") {
		t.Fatal("hidden member still listed")
	}
}

func TestUnlinkFlow(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("unlink", "", "100", ""))
	d := messageData(t, r)
	if d.Flags&discord.FlagEphemeral == 0 || d.Components[0].Components[0].CustomID != "unlink:confirm" {
		t.Fatalf("unlink = %+v", d)
	}
	click := map[string]any{"type": discord.InteractionMessageComponent, "user": map[string]any{"id": "100"}, "data": map[string]any{"custom_id": "unlink:confirm", "component_type": 2}}
	r, _ = h.send(t, click)
	if r.Type != discord.ResponseUpdateMessage || !strings.Contains(string(r.Data), `"components":[]`) {
		t.Fatalf("confirm = %d %s", r.Type, r.Data)
	}
	if has, _ := h.st.HasData(context.Background(), "100"); has {
		t.Fatal("data still stored after unlink")
	}
}

func TestLinkCommand(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("link", "G", "555", "0"))
	d := messageData(t, r)
	if d.Flags&discord.FlagEphemeral == 0 {
		t.Fatal("/link must be ephemeral")
	}
	u, err := url.Parse(d.Components[0].Components[0].URL)
	if err != nil || u.Path != "/link" {
		t.Fatalf("link url = %v", d.Components[0].Components[0].URL)
	}
	c, err := linktoken.Verify(h.secret, u.Query().Get("t"), now)
	if err != nil || c.DiscordID != "555" || c.GuildID != "G" {
		t.Fatalf("claims = %+v err = %v", c, err)
	}
	// A linked user who hid themselves gets a "show me" button.
	h.send(t, cmd("visibility", "G", "200", "0", opt("setting", 3, "hide")))
	r, _ = h.send(t, cmd("link", "G", "200", "0"))
	if !strings.Contains(string(r.Data), "vis:show") {
		t.Fatalf("missing show button: %s", r.Data)
	}
}

func TestPlaytests(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("playtests", "", "100", ""))
	d := messageData(t, r)
	if d.Content != "2 playtests are live and 1 starts soon." || len(d.Embeds) != 3 {
		t.Fatalf("playtests = %q, %d embeds", d.Content, len(d.Embeds))
	}
	buttons := d.Components[0].Components
	// Live playtests come first, ending soonest first.
	if buttons[0].Label != "Request access: Starfall Outriders" || buttons[1].Label != "Request access: Mossbound" ||
		buttons[len(buttons)-1].Label != "All playtests on player.gg" {
		t.Fatalf("buttons = %+v", buttons)
	}
	r, _ = h.send(t, cmd("playtests", "", "100", "", opt("genre", 3, "co-op")))
	if d := messageData(t, r); len(d.Embeds) != 1 || d.Embeds[0].Title != "Starfall Outriders" {
		t.Fatalf("genre filter = %+v", d.Embeds)
	}
}

func TestRadarSetupAndNewPlaytest(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("radar", "G", "100", "32", opt("channel", 7, "C1")))
	if r.Type != discord.ResponseDeferredMessage {
		t.Fatalf("type = %d", r.Type)
	}
	h.b.Wait()
	if len(h.dc.created) != 1 || h.dc.created[0] != "C1" || len(h.dc.posts) != 1 {
		t.Fatalf("created = %v posts = %d", h.dc.created, len(h.dc.posts))
	}
	if !strings.Contains(h.dc.posts[0].Content, "Playtest radar is on") || !strings.Contains(h.dc.edits[0].Content, "<#C1>") {
		t.Fatalf("welcome = %+v edit = %+v", h.dc.posts[0], h.dc.edits[0])
	}

	// Nothing new: no posts.
	h.b.postNewPlaytests(context.Background())
	if len(h.dc.posts) != 1 {
		t.Fatalf("reposted existing playtests: %d posts", len(h.dc.posts))
	}

	// A new playtest similar to Deep Rock Galactic appears.
	var list []map[string]any
	json.Unmarshal([]byte(playtestsJSON), &list)
	list = append(list, map[string]any{"title": "Drill Squad", "url": "https://player.gg/explore/games/5-drill", "starts": "2026-11-21", "similar_steam_apps": []string{"548430"}})
	raw, _ := json.Marshal(list)
	os.WriteFile(h.tests, raw, 0o644)
	h.b.postNewPlaytests(context.Background())
	if len(h.dc.posts) != 2 {
		t.Fatalf("posts = %d", len(h.dc.posts))
	}
	e := h.dc.posts[1].Embeds[0]
	if e.Author.Name != "New playtest that fits this server" || !strings.Contains(e.Description, "**3 members here** play games like Deep Rock Galactic") {
		t.Fatalf("radar post = %+v", e)
	}
}

func TestRadarWithoutPermissionExplainsInstall(t *testing.T) {
	h := newHarness(t)
	h.dc.createErr = &discord.APIError{Status: 403, Code: discord.CodeMissingPermissions}
	h.send(t, cmd("radar", "G", "100", "32", opt("channel", 7, "C1")))
	h.b.Wait()
	if len(h.dc.edits) != 1 || !strings.Contains(h.dc.edits[0].Content, "Manage Webhooks") {
		t.Fatalf("edits = %+v", h.dc.edits)
	}
}

func TestWeeklyChartPostsOncePerWeek(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.st.SetFeed(ctx, store.Feed{GuildID: "G", Kind: store.FeedWeeklyChart, ChannelID: "C2", WebhookID: "w", WebhookToken: "t", CreatedBy: "100",
		CreatedAt: now.AddDate(0, 0, -10), LastPostedAt: now.AddDate(0, 0, -10)})
	h.b.postDueCharts(ctx)
	h.b.postDueCharts(ctx)
	if len(h.dc.posts) != 1 || !strings.Contains(h.dc.posts[0].Embeds[0].Footer.Text, "posts every Monday") {
		t.Fatalf("posts = %+v", h.dc.posts)
	}
}

func TestLastMondayTen(t *testing.T) {
	cases := map[string]string{
		"2026-11-22T12:00:00Z": "2026-11-16T10:00:00Z", // Sunday
		"2026-11-23T09:59:00Z": "2026-11-16T10:00:00Z", // Monday before ten
		"2026-11-23T10:00:00Z": "2026-11-23T10:00:00Z", // Monday at ten
		"2026-11-24T08:00:00Z": "2026-11-23T10:00:00Z", // Tuesday
	}
	for in, want := range cases {
		ti, _ := time.Parse(time.RFC3339, in)
		if got := lastMondayTen(ti).Format(time.RFC3339); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestHours(t *testing.T) {
	cases := map[int]string{0: "0 h", 3: "0.1 h", 30: "0.5 h", 360: "6 h", 390: "6.5 h", 24720: "412 h", 77040: "1,284 h"}
	for m, want := range cases {
		if got := hours(m); got != want {
			t.Errorf("hours(%d) = %q want %q", m, got, want)
		}
	}
}
