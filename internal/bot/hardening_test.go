package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"playradar/internal/discord"
)

// If the radar's welcome post fails, the playtests open now are still marked
// as seen, so the next round doesn't post each of them separately.
func TestFailedRadarWelcomeDoesNotFloodChannel(t *testing.T) {
	h := newHarness(t)
	h.dc.postErr = errors.New("discord is down")
	h.send(t, cmd("radar", "G", "100", "32", opt("channel", 7, "C1")))
	h.b.Wait()
	h.dc.postErr = nil
	h.b.postNewPlaytests(context.Background())
	if len(h.dc.posts) != 0 {
		t.Fatalf("%d playtests posted one by one after a failed welcome", len(h.dc.posts))
	}
}

// A panic while building a deferred reply answers with an error message
// instead of crashing the whole app.
func TestPanicInDeferredReplyIsAnswered(t *testing.T) {
	h := newHarness(t)
	in := &discord.Interaction{Token: "t"}
	h.b.later(in, true, func(context.Context) discord.ResponseData { panic("boom") })
	h.b.Wait()
	if len(h.dc.edits) != 1 || !strings.Contains(h.dc.edits[0].Content, "Something went wrong") {
		t.Fatalf("edits = %+v", h.dc.edits)
	}
}

// Mistakes in the collections file (no games, a game with no app IDs) are
// skipped rather than breaking the command.
func TestBadCollectionsFileIsTolerated(t *testing.T) {
	h := newHarness(t)
	h.b.CollectionsFile = h.dataFile(t, "collections.json", `[
	 {"slug":"empty","emoji":"🫙","title":"Empty","games":[]},
	 {"slug":"half","emoji":"🤝","title":"Half","games":[{"name":"No IDs","ids":[]},{"name":"Deep Rock Galactic","ids":["548430"]}]}]`)
	r, _ := h.send(t, cmd("collection", "G", "100", "0"))
	text := texts(valid(t, messageData(t, r)))
	if strings.Contains(text, "Empty") || !strings.Contains(text, "**Half**\n▰▰▰▰▰▰▰▰▰▰  1/1") {
		t.Fatalf("collections = %s", text)
	}
	r, _ = h.send(t, cmd("collection", "G", "100", "0", opt("name", 3, "empty")))
	if strings.Contains(string(r.Data), "Something went wrong") {
		t.Fatalf("empty collection = %s", r.Data)
	}
}

// With Discord linking on, /link's button goes to the one-click path and the
// text keeps a plain Steam sign-in link.
func TestLinkOffersDiscordConnection(t *testing.T) {
	h := newHarness(t)
	h.b.DiscordLinking = true
	r, _ := h.send(t, cmd("link", "G", "999", "0"))
	d := valid(t, messageData(t, r))
	steam := button(t, d, func(c discord.Component) bool { return c.Label == "Link Steam" })
	if !strings.HasPrefix(steam.URL, "https://bot.example/link/discord?t=") ||
		!strings.Contains(texts(d), "One click if Steam is connected to your Discord. Or [sign in on Steam](https://bot.example/link?t=") {
		t.Fatalf("link card = %s %s", steam.URL, texts(d))
	}
}
