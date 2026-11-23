package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"playradar/internal/discord"
	"playradar/internal/linktoken"
	"playradar/internal/store"
)

// ----- helpers for reading Components V2 replies -----

// valid fails the test if d breaks Discord's component rules, and checks it
// uses the components v2 layout.
func valid(t *testing.T, d discord.ResponseData) discord.ResponseData {
	t.Helper()
	if d.Flags&discord.FlagComponentsV2 == 0 {
		t.Fatalf("expected a components v2 message, got %+v", d)
	}
	if err := discord.CheckMessage(d); err != nil {
		raw, _ := json.MarshalIndent(d, "", " ")
		t.Fatalf("invalid message: %v\n%s", err, raw)
	}
	return d
}

func walk(comps []discord.Component, fn func(discord.Component)) {
	for _, c := range comps {
		fn(c)
		walk(c.Components, fn)
		if c.Accessory != nil {
			fn(*c.Accessory)
		}
	}
}

// texts joins every text block in a message.
func texts(d discord.ResponseData) string {
	var out []string
	walk(d.Components, func(c discord.Component) {
		if c.Type == discord.ComponentTextDisplay {
			out = append(out, c.Content)
		}
	})
	return strings.Join(out, "\n")
}

// buttons lists every button, in rows and beside sections.
func buttons(d discord.ResponseData) []discord.Component {
	var out []discord.Component
	walk(d.Components, func(c discord.Component) {
		if c.Type == discord.ComponentButton {
			out = append(out, c)
		}
	})
	return out
}

func button(t *testing.T, d discord.ResponseData, match func(discord.Component) bool) discord.Component {
	t.Helper()
	for _, b := range buttons(d) {
		if match(b) {
			return b
		}
	}
	t.Fatalf("button not found in %s", texts(d))
	return discord.Component{}
}

func hasCustomID(d discord.ResponseData, id string) bool {
	for _, b := range buttons(d) {
		if b.CustomID == id {
			return true
		}
	}
	return false
}

func click(user, guild, customID string, values ...string) map[string]any {
	in := map[string]any{
		"type":  discord.InteractionMessageComponent,
		"token": "itok",
		"data":  map[string]any{"custom_id": customID, "component_type": 2, "values": values},
	}
	u := map[string]any{"id": user, "username": "user" + user}
	if guild != "" {
		in["guild_id"] = guild
		in["member"] = map[string]any{"user": u, "permissions": "0"}
	} else {
		in["user"] = u
	}
	return in
}

func postData(m discord.WebhookMessage) discord.ResponseData {
	return discord.ResponseData{Content: m.Content, Embeds: m.Embeds, Components: m.Components, Flags: m.Flags}
}

func (h *harness) details(t *testing.T, id string, reviews int, genres ...string) {
	t.Helper()
	yes := true
	if err := h.st.SetGameDetails(context.Background(), "steam", id,
		store.GameDetails{Multiplayer: &yes, Genres: genres, Reviews: reviews, ReleaseYear: 2020}, now); err != nil {
		t.Fatal(err)
	}
}

// ----- server views -----

func TestWhoPlays(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("whoplays", "G", "100", "0", opt("game", 3, "548430")))
	d := valid(t, messageData(t, r))
	text := texts(d)
	if !strings.Contains(text, "## Who plays Deep Rock Galactic\n-# 3 of 3 linked members own it") ||
		!strings.Contains(text, "👑 <@100> · **412 h** · 6 h lately") {
		t.Fatalf("whoplays = %s", text)
	}
	if strings.Contains(text, "<@400>") {
		t.Fatal("hidden member listed")
	}
	btn := button(t, d, func(b discord.Component) bool { return b.Style == discord.ButtonLink })
	if !strings.HasPrefix(btn.URL, "https://bot.example/go?") || !strings.Contains(btn.URL, url.QueryEscape("https://player.gg/explore/games/548430.steam")) {
		t.Fatalf("button url = %s", btn.URL)
	}
	if !hasCustomID(d, "game:548430") || d.Flags&discord.FlagEphemeral != 0 {
		t.Fatalf("whoplays buttons/flags = %+v", d)
	}
}

func TestWhoPlaysTypedName(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("whoplays", "G", "100", "0", opt("game", 3, "lethal")))
	if text := texts(valid(t, messageData(t, r))); !strings.Contains(text, "Who plays Lethal Company") {
		t.Fatalf("got %s", text)
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
	// /game in a DM searches your own library.
	in = cmd("game", "", "100", "", map[string]any{"name": "game", "type": 3, "value": "had", "focused": true})
	in["type"] = discord.InteractionAutocomplete
	r, _ = h.send(t, in)
	ac = discord.AutocompleteData{}
	json.Unmarshal(r.Data, &ac)
	if len(ac.Choices) != 1 || ac.Choices[0].Value != "1145360" || ac.Choices[0].Name != "Hades II · 96 h" {
		t.Fatalf("own-library choices = %+v", ac.Choices)
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
	text := texts(valid(t, h.dc.edits[0]))
	if !strings.Contains(text, "### 🎮 Multiplayer") || !strings.Contains(text, "Deep Rock Galactic") ||
		!strings.Contains(text, "Lethal Company") || strings.Contains(text, "Hades") {
		t.Fatalf("incommon = %s", text)
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
	text := texts(valid(t, messageData(t, r)))
	if !strings.Contains(text, "🥇 **Helldivers 2** · 20 h · 1 player") || !strings.Contains(text, "🥈 **Deep Rock Galactic** · 17 h · 2 players") {
		t.Fatalf("chart = %s", text)
	}
	if strings.Contains(text, "167 h") {
		t.Fatal("hidden member's playtime counted")
	}
	r, _ = h.send(t, cmd("chart", "G", "100", "0", opt("weekly_in", 7, "C9")))
	if d := messageData(t, r); !strings.Contains(d.Content, "Manage Server") {
		t.Fatalf("weekly without permission = %+v", d)
	}
}

func TestVisibilityHide(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("visibility", "G", "200", "0", opt("setting", 3, "hide")))
	if text := texts(valid(t, messageData(t, r))); !strings.Contains(text, "You're hidden here") {
		t.Fatalf("hide = %s", text)
	}
	r, _ = h.send(t, cmd("whoplays", "G", "100", "0", opt("game", 3, "548430")))
	if strings.Contains(texts(messageData(t, r)), "<@200>") {
		t.Fatal("hidden member still listed")
	}
}

// 100 has the most hours here in Deep Rock Galactic (400 is hidden), CS2
// and Hades II; 200 has more Lethal Company.
func TestCrowns(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("crowns", "G", "100", "0"))
	text := texts(valid(t, messageData(t, r)))
	if !strings.Contains(text, "3 crowns in this server") ||
		!strings.Contains(text, "👑 **Deep Rock Galactic** · 412 h · next: <@200> 288 h") || strings.Contains(text, "Lethal") {
		t.Fatalf("crowns = %s", text)
	}
	r, _ = h.send(t, cmd("crowns", "G", "100", "0", opt("player", 6, "400")))
	if d := messageData(t, r); !strings.Contains(d.Content, "hidden") {
		t.Fatalf("hidden player = %+v", d)
	}
}

func TestLeaderboardTabs(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("leaderboard", "G", "200", "0"))
	d := valid(t, messageData(t, r))
	text := texts(d)
	if !strings.Contains(text, "🏆 Most hours played") || !strings.Contains(text, "🥇 <@100> · **826 h**") ||
		!strings.Contains(text, "🥈 <@200> · **421 h**") || !strings.Contains(text, "You're **#2**") || strings.Contains(text, "<@400>") {
		t.Fatalf("leaderboard = %s", text)
	}
	r, _ = h.send(t, click("200", "G", "lb:set:crowns"))
	if r.Type != discord.ResponseUpdateMessage {
		t.Fatalf("tab click type = %d", r.Type)
	}
	if text := texts(valid(t, messageData(t, r))); !strings.Contains(text, "🥇 <@100> · **3 👑**") || !strings.Contains(text, "🥈 <@200> · **2 👑**") {
		t.Fatalf("crown board = %s", text)
	}
	r, _ = h.send(t, click("300", "G", "lb:open:recent"))
	if d := valid(t, messageData(t, r)); r.Type != discord.ResponseMessage || d.Flags&discord.FlagEphemeral == 0 {
		t.Fatalf("open board = %d %+v", r.Type, d)
	}
}

func TestTaste(t *testing.T) {
	h := newHarness(t)
	h.details(t, "548430", 300000, "Action")
	h.details(t, "1966720", 400000, "Action", "Indie")
	h.details(t, "730", 9000000, "Action")
	h.details(t, "1145360", 60000, "Action", "RPG")
	h.details(t, "553850", 200000, "Action")
	r, _ := h.send(t, cmd("taste", "G", "100", "0", opt("friend", 6, "200")))
	text := texts(valid(t, messageData(t, r)))
	if !strings.Contains(text, "**2** in common · **2** only you · **1** only name200") && !strings.Contains(text, "**2** in common · **2** only you · **1** only steam200") {
		t.Fatalf("taste counts = %s", text)
	}
	if !strings.Contains(text, "▸ **Deep Rock Galactic** · 412 h vs 288 h") || !strings.Contains(text, "### Genres") || !strings.Contains(text, "% · ") {
		t.Fatalf("taste = %s", text)
	}
	r, _ = h.send(t, cmd("taste", "G", "100", "0", opt("friend", 6, "100")))
	if d := messageData(t, r); !strings.Contains(d.Content, "someone other") {
		t.Fatalf("self taste = %+v", d)
	}
}

// ----- personal views -----

func TestProfile(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("profile", "G", "100", "0"))
	d := valid(t, messageData(t, r))
	text := texts(d)
	if !strings.Contains(text, "## name100") || !strings.Contains(text, "**826 h** played  ·  **4** games  ·  **16 h** in the last 2 weeks") ||
		!strings.Contains(text, "🥇 **Deep Rock Galactic** · 412 h") || !strings.Contains(text, "▸ **Hades II** · 10 h") {
		t.Fatalf("profile = %s", text)
	}
	if len(h.sy.soon) != 0 {
		t.Fatal("fresh profile should not trigger a sync")
	}
	r, _ = h.send(t, cmd("profile", "G", "100", "0", opt("player", 6, "400")))
	if d := messageData(t, r); d.Flags&discord.FlagEphemeral == 0 || !strings.Contains(d.Content, "hidden") {
		t.Fatalf("hidden lookup = %+v", d)
	}
	r, _ = h.send(t, cmd("profile", "", "999", ""))
	if d := messageData(t, r); !strings.Contains(d.Content, "/link") {
		t.Fatalf("unlinked = %+v", d)
	}
}

// A library whose first sync failed is loaded on the spot by /profile, which
// then shows the finished card instead of a card full of zeros.
func TestProfileLoadsLibraryThatNeverSynced(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.st.LinkAccount(ctx, store.Account{DiscordID: "500", Platform: "steam", ExternalID: "76561198000000500", LinkedAt: now.Add(-time.Hour)})
	h.st.MarkSyncError(ctx, "500", "steam", "steam: 403 Forbidden", now.Add(-time.Hour))
	h.sy.onSync = func(id string) error {
		return h.st.SaveSync(ctx, id, "steam", store.Profile{DisplayName: "Xan"},
			[]store.Game{{GameID: "548430", Name: "Deep Rock Galactic", ImageURL: "https://img/548430"}},
			[]store.LibraryEntry{{GameID: "548430", MinutesTotal: 600, MinutesRecent: 60}}, now)
	}
	r, _ := h.send(t, cmd("profile", "", "500", ""))
	if r.Type != discord.ResponseDeferredMessage {
		t.Fatalf("type = %d, want a deferred reply", r.Type)
	}
	h.b.Wait()
	if len(h.sy.synced) != 1 || len(h.dc.edits) != 1 {
		t.Fatalf("synced = %v, edits = %d", h.sy.synced, len(h.dc.edits))
	}
	if text := texts(valid(t, h.dc.edits[0])); !strings.Contains(text, "**10 h** played  ·  **1** game") || !strings.Contains(text, "Deep Rock Galactic") {
		t.Fatalf("card = %s", text)
	}
}

func TestProfileSyncFailureSaysSo(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.st.LinkAccount(ctx, store.Account{DiscordID: "500", Platform: "steam", ExternalID: "76561198000000500", LinkedAt: now})
	h.sy.onSync = func(string) error { return errors.New("steam: 403 Forbidden") }
	h.send(t, cmd("profile", "", "500", ""))
	h.b.Wait()
	if len(h.dc.edits) != 1 || !strings.Contains(h.dc.edits[0].Content, "didn't send your library") {
		t.Fatalf("edits = %+v", h.dc.edits)
	}
}

func TestGameCard(t *testing.T) {
	h := newHarness(t)
	// Not looked up yet: the card is built after a lookup, behind a deferral.
	r, _ := h.send(t, cmd("game", "G", "100", "0", opt("game", 3, "548430")))
	if r.Type != discord.ResponseDeferredMessage {
		t.Fatalf("type = %d", r.Type)
	}
	h.b.Wait()
	d := valid(t, h.dc.edits[0])
	text := texts(d)
	if !strings.Contains(text, "## Deep Rock Galactic") || !strings.Contains(text, "**You** · 412 h · 6 h in the last 2 weeks") ||
		!strings.Contains(text, "**This server** · 3 of 3 linked members own it") || !strings.Contains(text, "👑 <@100> with 412 h") {
		t.Fatalf("game card = %s", text)
	}
	if !hasCustomID(d, "wp:548430") {
		t.Fatal("missing who-plays button")
	}
	// Looked up: answered straight away, with store details.
	h.details(t, "548430", 304804, "Action", "Indie")
	r, _ = h.send(t, cmd("game", "", "300", "", opt("game", 3, "548430")))
	text = texts(valid(t, messageData(t, r)))
	if !strings.Contains(text, "-# 2020 · Action · Indie · 304,804 Steam reviews") || !strings.Contains(text, "🧊 Tip of the iceberg") ||
		!strings.Contains(text, "**Multiplayer** · Yes") || strings.Contains(text, "This server") {
		t.Fatalf("detailed game card = %s", text)
	}
	// The who-plays button answers privately.
	r, _ = h.send(t, click("200", "G", "wp:548430"))
	if d := valid(t, messageData(t, r)); d.Flags&discord.FlagEphemeral == 0 || !strings.Contains(texts(d), "Who plays Deep Rock Galactic") {
		t.Fatalf("who-plays button = %+v", d)
	}
}

func TestIceberg(t *testing.T) {
	h := newHarness(t)
	h.details(t, "548430", 300000)
	h.details(t, "730", 9000000)
	h.details(t, "1145360", 60000)
	h.details(t, "1966720", 400)
	r, _ := h.send(t, cmd("iceberg", "", "100", ""))
	if r.Type != discord.ResponseDeferredMessage {
		t.Fatalf("type = %d", r.Type)
	}
	h.b.Wait()
	text := texts(valid(t, h.dc.edits[0]))
	// Hours-weighted: 5,760 min at depth 1 and 1,000 min at depth 5 out of 49,540.
	if !strings.Contains(text, "### 🧊 Tip of the iceberg") || !strings.Contains(text, "Deep Rock Galactic · Counter-Strike 2") ||
		!strings.Contains(text, "### 🦑 The trench\n-# under 500 reviews\nLethal Company") || !strings.Contains(text, "Depth score **4**/100") {
		t.Fatalf("iceberg = %s", text)
	}
	if strings.Contains(text, "The abyss") {
		t.Fatal("empty tiers should be left out")
	}
}

func TestCollage(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("collage", "", "100", ""))
	d := valid(t, messageData(t, r))
	var img string
	walk(d.Components, func(c discord.Component) {
		if c.Type == discord.ComponentMediaGallery && len(c.Items) == 1 {
			img = c.Items[0].Media.URL
		}
	})
	// One drawn shelf of covers, ranked by hours.
	if !strings.HasPrefix(img, "https://bot.example/render/shelf.jpg?l=grid&g=548430.24720-730.18060-1145360.5760-1966720.1000&s=") ||
		!strings.Contains(texts(d), "**1** Deep Rock Galactic · **2** Counter-Strike 2") {
		t.Fatalf("collage = %s, %s", img, texts(d))
	}
}

func TestUnlinkFlow(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("unlink", "", "100", ""))
	d := valid(t, messageData(t, r))
	if d.Flags&discord.FlagEphemeral == 0 || !hasCustomID(d, "unlink:confirm") {
		t.Fatalf("unlink = %+v", d)
	}
	r, _ = h.send(t, click("100", "", "unlink:confirm"))
	if r.Type != discord.ResponseUpdateMessage || !strings.Contains(texts(valid(t, messageData(t, r))), "Deleted") {
		t.Fatalf("confirm = %d %s", r.Type, r.Data)
	}
	if has, _ := h.st.HasData(context.Background(), "100"); has {
		t.Fatal("data still stored after unlink")
	}
}

func TestLinkCommand(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("link", "G", "555", "0"))
	d := valid(t, messageData(t, r))
	if d.Flags&discord.FlagEphemeral == 0 {
		t.Fatal("/link must be ephemeral")
	}
	btn := button(t, d, func(b discord.Component) bool { return b.Label == "Link Steam" })
	u, err := url.Parse(btn.URL)
	if err != nil || u.Path != "/link" {
		t.Fatalf("link url = %v", btn.URL)
	}
	c, err := linktoken.Verify(h.secret, u.Query().Get("t"), now)
	if err != nil || c.DiscordID != "555" || c.GuildID != "G" {
		t.Fatalf("claims = %+v err = %v", c, err)
	}
	h.send(t, cmd("visibility", "G", "200", "0", opt("setting", 3, "hide")))
	r, _ = h.send(t, cmd("link", "G", "200", "0"))
	if d := valid(t, messageData(t, r)); !hasCustomID(d, "vis:show") || !strings.Contains(texts(d), "linked as **steam200**") {
		t.Fatalf("hidden user link = %s", texts(d))
	}
}

func TestHelp(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("help", "", "1", ""))
	if text := texts(valid(t, messageData(t, r))); !strings.Contains(text, "`/iceberg`") || !strings.Contains(text, "`/radar`") {
		t.Fatalf("help = %s", text)
	}
}

// ----- playtests -----

func TestPlaytestsPageAndDetails(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("playtests", "G", "100", "0"))
	d := valid(t, messageData(t, r))
	text := texts(d)
	// Live first (ending soonest), then upcoming; Old Test has ended.
	starfall, moss, neon := strings.Index(text, "### Starfall Outriders"), strings.Index(text, "### Mossbound"), strings.Index(text, "### Neon Courier")
	if !strings.Contains(text, "-# 2 live · 1 starting soon") || starfall < 0 || moss < starfall || neon < moss || strings.Contains(text, "Old Test") {
		t.Fatalf("playtests = %s", text)
	}
	access := button(t, d, func(b discord.Component) bool { return b.Label == "Request access" })
	if !strings.Contains(access.URL, url.QueryEscape("https://player.gg/explore/games/1-starfall")) {
		t.Fatalf("request access url = %s", access.URL)
	}
	// One page: no page buttons.
	if hasCustomID(d, "pt:p:1:") {
		t.Fatal("single page shows page buttons")
	}

	// The details menu swaps in one playtest, with a fit note and a way back.
	r, _ = h.send(t, click("100", "G", "pt:s:0:", "0"))
	d = valid(t, messageData(t, r))
	if r.Type != discord.ResponseUpdateMessage || !strings.Contains(texts(d), "## Starfall Outriders\n-# by Lumen Forge") ||
		!strings.Contains(texts(d), "🎯 **3 members here** play games like Deep Rock Galactic") || !hasCustomID(d, "pt:p:0:") {
		t.Fatalf("details = %s", texts(d))
	}
	r, _ = h.send(t, click("100", "G", "pt:p:0:"))
	if !strings.Contains(texts(valid(t, messageData(t, r))), "### Mossbound") {
		t.Fatal("back button didn't return to the list")
	}

	r, _ = h.send(t, cmd("playtests", "", "100", "", opt("genre", 3, "co-op")))
	if text := texts(valid(t, messageData(t, r))); !strings.Contains(text, "Starfall Outriders") || strings.Contains(text, "Mossbound") {
		t.Fatalf("genre filter = %s", text)
	}
}

func TestPlaytestsPaging(t *testing.T) {
	h := newHarness(t)
	var list []map[string]any
	json.Unmarshal([]byte(playtestsJSON), &list)
	for _, n := range []string{"A", "B", "C", "D"} {
		list = append(list, map[string]any{"title": "Extra " + n, "url": "https://player.gg/explore/games/x-" + n, "image": "https://img/" + n,
			"summary": "Summary " + n, "starts": "2026-12-20"})
	}
	raw, _ := json.Marshal(list)
	os.WriteFile(h.tests, raw, 0o644)

	r, _ := h.send(t, cmd("playtests", "", "100", ""))
	d := valid(t, messageData(t, r))
	if !hasCustomID(d, "pt:p:1:") || !strings.Contains(string(r.Data), `"label":"1 / 3"`) {
		t.Fatalf("first page nav = %s", r.Data)
	}
	r, _ = h.send(t, click("100", "", "pt:p:2:"))
	if text := texts(valid(t, messageData(t, r))); !strings.Contains(text, "Extra D") || strings.Contains(text, "Starfall") {
		t.Fatalf("last page = %s", text)
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
	if text := texts(valid(t, postData(h.dc.posts[0]))); !strings.Contains(text, "Playtest radar is on") || !strings.Contains(h.dc.edits[0].Content, "<#C1>") {
		t.Fatalf("welcome = %s edit = %+v", text, h.dc.edits[0])
	}
	h.b.postNewPlaytests(context.Background())
	if len(h.dc.posts) != 1 {
		t.Fatalf("reposted existing playtests: %d posts", len(h.dc.posts))
	}

	var list []map[string]any
	json.Unmarshal([]byte(playtestsJSON), &list)
	list = append(list, map[string]any{"title": "Drill Squad", "url": "https://player.gg/explore/games/5-drill", "starts": "2026-11-21", "similar_steam_apps": []string{"548430"}})
	raw, _ := json.Marshal(list)
	os.WriteFile(h.tests, raw, 0o644)
	h.b.postNewPlaytests(context.Background())
	if len(h.dc.posts) != 2 {
		t.Fatalf("posts = %d", len(h.dc.posts))
	}
	post := valid(t, postData(h.dc.posts[1]))
	if text := texts(post); !strings.Contains(text, "New playtest that fits this server") || !strings.Contains(text, "**3 members here** play games like Deep Rock Galactic") {
		t.Fatalf("radar post = %s", text)
	}
	// Radar status, then turning it off.
	r, _ = h.send(t, cmd("radar", "G", "100", "32"))
	if !hasCustomID(valid(t, messageData(t, r)), "radar:off") {
		t.Fatal("radar status has no off button")
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
	if len(h.dc.posts) != 1 {
		t.Fatalf("posts = %d", len(h.dc.posts))
	}
	post := valid(t, postData(h.dc.posts[0]))
	if !strings.Contains(texts(post), "posts every Monday") {
		t.Fatalf("weekly post = %s", texts(post))
	}
	for _, b := range buttons(post) {
		if b.Style != discord.ButtonLink {
			t.Fatalf("scheduled post kept an app button: %+v", b)
		}
	}
}

// ----- small helpers -----

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

func TestTasteScore(t *testing.T) {
	same := []store.GenreShare{{Genre: "Action", Minutes: 100}}
	if s := tasteScore(store.Comparison{Shared: 10, AOnly: 5, BOnly: 5}, same, same); s != 100 {
		t.Errorf("identical genres, big overlap = %d", s)
	}
	if s := tasteScore(store.Comparison{AOnly: 5, BOnly: 5}, same, []store.GenreShare{{Genre: "Puzzle", Minutes: 100}}); s != 0 {
		t.Errorf("nothing shared = %d", s)
	}
}
