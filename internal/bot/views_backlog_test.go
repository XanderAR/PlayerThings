package bot

import (
	"context"
	"strings"
	"testing"

	"playradar/internal/discord"
	"playradar/internal/store"
)

// seedBacklog links 500 (four games never launched, one barely played) and
// 600 (one never launched), both visible in server G.
func (h *harness) seedBacklog(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	libs := map[string][]struct {
		id, name string
		minutes  int
	}{
		"500": {{"548430", "Deep Rock Galactic", 600}, {"1966720", "Lethal Company", 30},
			{"753640", "Outer Wilds", 0}, {"632470", "Disco Elysium", 0}, {"105600", "Terraria", 0}, {"999", "Test Tool", 0}},
		"600": {{"548430", "Deep Rock Galactic", 100}, {"105600", "Terraria", 0}},
	}
	for id, games := range libs {
		if err := h.st.LinkAccount(ctx, store.Account{DiscordID: id, Platform: "steam", ExternalID: "7656119800000" + id, LinkedAt: now}); err != nil {
			t.Fatal(err)
		}
		var gs []store.Game
		var es []store.LibraryEntry
		for _, g := range games {
			gs = append(gs, store.Game{GameID: g.id, Name: g.name})
			es = append(es, store.LibraryEntry{GameID: g.id, MinutesTotal: g.minutes})
		}
		if err := h.st.SaveSync(ctx, id, "steam", store.Profile{DisplayName: "steam" + id}, gs, es, now); err != nil {
			t.Fatal(err)
		}
		if err := h.st.SetVisibility(ctx, "G", id, true, now); err != nil {
			t.Fatal(err)
		}
	}
	h.details(t, "105600", 1000000, "Action", "Adventure")
	h.details(t, "753640", 100000, "Adventure")
	h.details(t, "632470", 80000, "RPG")
}

func TestBacklog(t *testing.T) {
	h := newHarness(t)
	h.seedBacklog(t)
	r, _ := h.send(t, cmd("backlog", "G", "500", "0"))
	d := valid(t, messageData(t, r))
	text := texts(d)
	for _, want := range []string{
		"## 📚 name500's pile of shame\n-# 6 games owned on Steam",
		"## 4 never launched\n▰▰▰▰▰▰▰▱▱▱ **67%** of the library · *this isn't a backlog, it's an archive*",
		"🫠 **1** more played for under an hour",
		"📅 Clear one a week and you're done by **December 2026**",
		"🏆 **Biggest pile** in this server, out of 2",
		"### 🎲 Tonight's pick",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	// The cover board shows the best-known unplayed games first.
	var img string
	walk(d.Components, func(c discord.Component) {
		if c.Type == discord.ComponentMediaGallery {
			img = c.Items[0].Media.URL
		}
	})
	if !strings.Contains(img, "l=set&g=105600.0-753640.0-632470.0-999.0&") {
		t.Fatalf("board = %s", img)
	}
	// Tool-like entries with no reviews aren't picked when real games exist.
	if strings.Contains(text, "**Test Tool**") {
		t.Fatalf("picked a tool: %s", text)
	}
	if !hasCustomID(d, "lb:open:backlog") {
		t.Fatal("no server piles button")
	}

	// Rerolling walks through every candidate before repeating.
	reroll := button(t, d, func(c discord.Component) bool { return strings.HasPrefix(c.CustomID, "bl:r:500:") })
	picks := map[string]bool{pickName(text): true}
	for i := 0; i < 2; i++ {
		r, _ = h.send(t, click("500", "G", reroll.CustomID))
		if r.Type != discord.ResponseUpdateMessage {
			t.Fatalf("reroll type = %d", r.Type)
		}
		d = valid(t, messageData(t, r))
		picks[pickName(texts(d))] = true
		reroll = button(t, d, func(c discord.Component) bool { return strings.HasPrefix(c.CustomID, "bl:r:500:") })
	}
	if len(picks) != 3 {
		t.Fatalf("three rolls should give three games: %v", picks)
	}

	// Someone else can't reroll it.
	r, _ = h.send(t, click("600", "G", reroll.CustomID))
	if !strings.Contains(texts(messageData(t, r)), "That's not your pile") {
		t.Fatalf("other user reroll = %s", r.Data)
	}

	// The leaderboard has a pile-of-shame tab.
	r, _ = h.send(t, cmd("leaderboard", "G", "600", "0", opt("type", 3, "backlog")))
	board := texts(valid(t, messageData(t, r)))
	if !strings.Contains(board, "🥇 <@500> · **4 never launched**") || !strings.Contains(board, "You're **#2** · 1 never launched") {
		t.Fatalf("board = %s", board)
	}
}

func TestBacklogEmpty(t *testing.T) {
	h := newHarness(t)
	// 300 has played everything for over an hour.
	r, _ := h.send(t, cmd("backlog", "", "300", ""))
	if text := texts(valid(t, messageData(t, r))); !strings.Contains(text, "No pile at all") {
		t.Fatalf("empty backlog = %s", text)
	}
}

func pickName(text string) string {
	_, after, _ := strings.Cut(text, "### 🎲 Tonight's pick\n**")
	name, _, _ := strings.Cut(after, "**")
	return name
}
