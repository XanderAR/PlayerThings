package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"playradar/internal/discord"
)

func TestVerdictAndAppeal(t *testing.T) {
	h := newHarness(t)
	r, _ := h.send(t, cmd("verdict", "G", "100", "0"))
	d := valid(t, messageData(t, r))
	text := texts(d)
	// Half of 100's hours are Deep Rock Galactic, which has its own archetype.
	if !strings.Contains(text, "### ⛏️ The Dwarf") ||
		!strings.Contains(text, "▸ **50%** of your playtime is **Deep Rock Galactic**. Rock and stone") ||
		!strings.Contains(text, "**826 h** on Steam. That's 34 full days.") ||
		!strings.Contains(text, "**Sentence:** play **Starfall Outriders** before it comes out.") {
		t.Fatalf("verdict = %s", text)
	}
	if strings.Count(text, "▸ ") != 3 {
		t.Fatalf("want three lines of evidence: %s", text)
	}
	if !hasCustomID(d, "vd:a:100") || !hasCustomID(d, "vd:me") {
		t.Fatalf("buttons = %+v", buttons(d))
	}
	sentence := button(t, d, func(c discord.Component) bool { return c.Label == "Serve your sentence" })
	if !strings.Contains(sentence.URL, "c=verdict") || !strings.Contains(sentence.URL, "1-starfall") {
		t.Fatalf("sentence link = %s", sentence.URL)
	}

	// Only the accused can appeal.
	r, _ = h.send(t, click("200", "G", "vd:a:100"))
	if d := messageData(t, r); d.Flags&discord.FlagEphemeral == 0 || !strings.Contains(texts(d), "Only the accused can appeal") {
		t.Fatalf("someone else appealing = %+v", d)
	}
	r, _ = h.send(t, click("100", "G", "vd:a:100"))
	if r.Type != discord.ResponseUpdateMessage {
		t.Fatalf("appeal should update the card, got type %d", r.Type)
	}
	appeal := texts(valid(t, messageData(t, r)))
	if !strings.Contains(appeal, "The Dwarf, with honors") || !strings.Contains(appeal, "You hold **3 crowns** in this server.") {
		t.Fatalf("appeal = %s", appeal)
	}
	if !hasCustomID(messageData(t, r), "vd:v:100") {
		t.Fatal("appeal needs a way back to the verdict")
	}

	// Judge me: a new public verdict for whoever pressed it.
	r, _ = h.send(t, click("300", "G", "vd:me"))
	d = valid(t, messageData(t, r))
	if r.Type != discord.ResponseMessage || d.Flags&discord.FlagEphemeral != 0 || !strings.Contains(texts(d), "The verdict on") {
		t.Fatalf("judge me = %d %s", r.Type, texts(d))
	}
	// Someone without an account is pointed at /link.
	r, _ = h.send(t, click("999", "G", "vd:me"))
	if !strings.Contains(string(r.Data), "/link") {
		t.Fatalf("unlinked judge me = %s", r.Data)
	}
}

func TestVerdictFacts(t *testing.T) {
	h := newHarness(t)
	h.details(t, "730", 9000000)
	ev, err := h.b.evidence(context.Background(), "100", "")
	if err != nil {
		t.Fatal(err)
	}
	f := ev.facts()
	if _, ok := f["crowns"]; ok {
		t.Fatal("no crowns outside a server")
	}
	if ev.ruling(f).arch.title != "The Dwarf" {
		t.Fatalf("ruling = %+v", ev.ruling(f))
	}
	// Someone who bought a famous game and dropped it.
	ev.played = append(ev.played, ev.played[1])
	ev.played[len(ev.played)-1].MinutesTotal = 45
	if got := ev.facts()["dropped"].roast; got != "You bought **Counter-Strike 2**, played **45 minutes**, and never went back." {
		t.Fatalf("dropped = %q", got)
	}
}

func TestFourFavorites(t *testing.T) {
	h := newHarness(t)
	// No favorites yet: a private picker of your most-played games.
	r, _ := h.send(t, cmd("four", "G", "100", "0"))
	d := valid(t, messageData(t, r))
	var menu discord.Component
	walk(d.Components, func(c discord.Component) {
		if c.Type == discord.ComponentStringSelect {
			menu = c
		}
	})
	if d.Flags&discord.FlagEphemeral == 0 || menu.CustomID != "four:set" || len(menu.Options) != 4 || menu.MaxValues != 4 {
		t.Fatalf("picker = %+v", d)
	}

	// Picking shows the card privately with a button to post it.
	r, _ = h.send(t, click("100", "G", "four:set", "1145360", "548430"))
	if r.Type != discord.ResponseUpdateMessage || !hasCustomID(messageData(t, r), "four:post") {
		t.Fatalf("after picking = %d %s", r.Type, r.Data)
	}
	r, _ = h.send(t, click("100", "G", "four:post"))
	d = valid(t, messageData(t, r))
	if d.Flags&discord.FlagEphemeral != 0 {
		t.Fatal("posting should be public")
	}
	var img string
	walk(d.Components, func(c discord.Component) {
		if c.Type == discord.ComponentMediaGallery {
			img = c.Items[0].Media.URL
		}
	})
	if !strings.HasPrefix(img, "https://bot.example/render/shelf.jpg?l=four&g=1145360.5760-548430.24720&s=") ||
		!strings.Contains(texts(d), "Hades II · Deep Rock Galactic") {
		t.Fatalf("card = %s %s", img, texts(d))
	}

	// 300 sets theirs by typing; it overlaps with 100's.
	r, _ = h.send(t, cmd("four", "G", "300", "0", opt("first", 3, "2379780"), opt("second", 3, "Deep Rock")))
	d = valid(t, messageData(t, r))
	text := texts(d)
	if !strings.Contains(text, "Balatro · Deep Rock Galactic") ||
		!strings.Contains(text, "▸ **Deep Rock Galactic** is also a favorite of 1 person here") ||
		!strings.Contains(text, "Closest match: <@100>, sharing 1 of 2") {
		t.Fatalf("typed four = %s", text)
	}

	// The picker remembers what's chosen, and /profile shows the four.
	r, _ = h.send(t, click("100", "G", "four:pick"))
	raw, _ := json.Marshal(messageData(t, r))
	if strings.Count(string(raw), `"default":true`) != 2 {
		t.Fatalf("picker should preselect the current four: %s", raw)
	}
	r, _ = h.send(t, cmd("profile", "G", "100", "0"))
	if !strings.Contains(texts(messageData(t, r)), "❤️ **Four favorites** · Hades II · Deep Rock Galactic") {
		t.Fatalf("profile = %s", texts(messageData(t, r)))
	}

	// Someone with none: a nudge to pick yours.
	r, _ = h.send(t, cmd("four", "G", "100", "0", opt("player", 6, "200")))
	if !strings.Contains(texts(messageData(t, r)), "hasn't picked their four yet") {
		t.Fatalf("empty four = %s", r.Data)
	}

	// Unlink deletes favorites too.
	if err := h.st.DeleteUser(context.Background(), "300"); err != nil {
		t.Fatal(err)
	}
	if favs, _ := h.st.Favorites(context.Background(), "300", "steam"); len(favs) != 0 {
		t.Fatalf("favorites survived unlink: %+v", favs)
	}
}

func TestFourAutocomplete(t *testing.T) {
	h := newHarness(t)
	in := cmd("four", "G", "300", "0", map[string]any{"name": "first", "type": 3, "value": "", "focused": true})
	in["type"] = discord.InteractionAutocomplete
	r, _ := h.send(t, in)
	var ac discord.AutocompleteData
	json.Unmarshal(r.Data, &ac)
	// Your own games first (with hours), then other games people here own.
	if len(ac.Choices) < 3 || ac.Choices[0].Value != "548430" || !strings.Contains(ac.Choices[0].Name, " · 140 h") {
		t.Fatalf("choices = %+v", ac.Choices)
	}
	seen := map[string]bool{}
	for _, c := range ac.Choices {
		if seen[c.Value] {
			t.Fatalf("duplicate choice %s", c.Value)
		}
		seen[c.Value] = true
	}
	if !seen["730"] {
		t.Fatalf("server games missing: %+v", ac.Choices)
	}
}
