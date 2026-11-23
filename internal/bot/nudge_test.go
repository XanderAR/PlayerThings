package bot

import (
	"context"
	"strings"
	"testing"

	"playradar/internal/discord"
)

// In a server where only you have Steam linked, server views carry a nudge
// with a button that posts an invite (once per 10 minutes).
func TestLonelyServerNudge(t *testing.T) {
	h := newHarness(t)
	if err := h.st.SetVisibility(context.Background(), "L", "100", true, now); err != nil {
		t.Fatal(err)
	}
	for _, in := range []map[string]any{
		cmd("leaderboard", "L", "100", "0"),
		cmd("whoplays", "L", "100", "0", opt("game", 3, "548430")),
		cmd("crowns", "L", "100", "0"),
		cmd("chart", "L", "100", "0"),
		cmd("goty", "L", "100", "0", opt("scope", 3, "server")),
	} {
		r, _ := h.send(t, in)
		d := valid(t, messageData(t, r))
		if !strings.Contains(texts(d), "You're the only one here with Steam linked so far") || !hasCustomID(d, "nudge:link") {
			t.Fatalf("%v: no nudge in %s", in["data"], texts(d))
		}
	}

	// A server with three linked members gets no nudge.
	r, _ := h.send(t, cmd("leaderboard", "G", "100", "0"))
	if d := valid(t, messageData(t, r)); hasCustomID(d, "nudge:link") {
		t.Fatal("busy server shouldn't be nudged")
	}

	// Nobody linked at all.
	r, _ = h.send(t, cmd("leaderboard", "E", "999", "0"))
	if text := texts(valid(t, messageData(t, r))); !strings.Contains(text, "Nobody here has Steam linked yet") {
		t.Fatalf("empty server = %s", text)
	}

	// The button posts a public invite, then waits before allowing another.
	r, _ = h.send(t, click("100", "L", "nudge:link"))
	d := valid(t, messageData(t, r))
	if r.Type != discord.ResponseMessage || d.Flags&discord.FlagEphemeral != 0 ||
		!strings.Contains(texts(d), "<@100> wants to compare games with everyone") {
		t.Fatalf("invite = %d %s", r.Type, texts(d))
	}
	r, _ = h.send(t, click("100", "L", "nudge:link"))
	if d := messageData(t, r); d.Flags&discord.FlagEphemeral == 0 || !strings.Contains(d.Content, "a few minutes ago") {
		t.Fatalf("second invite should be held back: %+v", d)
	}
}
