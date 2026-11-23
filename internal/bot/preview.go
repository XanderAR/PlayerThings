package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"playradar/internal/discord"
	"playradar/internal/playtests"
	"playradar/internal/steam"
	"playradar/internal/store"
)

// Sample friends get numeric IDs so their mentions render like real ones.
const (
	previewMara = "100000000000000001"
	previewTheo = "100000000000000002"
)

// PreviewCard is one rendered reply, for checking layouts in Discord.
type PreviewCard struct {
	Name string
	Data discord.ResponseData
}

// Preview renders every card the bot makes from sample data, by running real
// interactions through the bot against a throwaway database in dir. youID is
// shown as the person running the commands; the other players are samples.
// PreviewFiles are the data files the preview reads.
type PreviewFiles struct {
	Playtests, Collections, Moments string
	// LinkSecret signs image URLs; use the live service's so its /render
	// endpoint draws the preview's images. Empty uses a throwaway key.
	LinkSecret string
}

func Preview(ctx context.Context, dir string, files PreviewFiles, siteURL, youID string) ([]PreviewCard, error) {
	playtestsFile := files.Playtests
	st, err := store.Open(filepath.Join(dir, "preview.db"))
	if err != nil {
		return nil, err
	}
	defer st.Close()
	const guild = "preview-guild"
	now := time.Now()
	if err := seedPreview(ctx, st, guild, youID, now); err != nil {
		return nil, err
	}
	capture := &captureDiscord{}
	b := &Bot{
		AppName: "PlayRadar", InstallURL: "https://discord.com/oauth2/authorize",
		Store: st, Syncer: previewSyncer{}, Discord: capture,
		LinkSecret:    previewSecret(files.LinkSecret),
		Links:         Links{Site: siteURL, PlayerGG: "https://player.gg"},
		PlaytestsFile: playtestsFile, PlaytestsSource: "player.gg", CollectionsFile: files.Collections, MomentsFile: files.Moments,
	}

	var cards []PreviewCard
	var problems []string
	run := func(name string, in *discord.Interaction) {
		resp := b.Handle(ctx, in)
		var data discord.ResponseData
		switch resp.Type {
		case discord.ResponseMessage, discord.ResponseUpdateMessage:
			d, ok := resp.Data.(*discord.ResponseData)
			if !ok {
				problems = append(problems, name+": no message data")
				return
			}
			data = *d
		case discord.ResponseDeferredMessage:
			b.Wait()
			data = capture.last()
		default:
			problems = append(problems, fmt.Sprintf("%s: response type %d", name, resp.Type))
			return
		}
		cards = append(cards, PreviewCard{name, data})
	}
	member := func(id, name string) *discord.Member {
		return &discord.Member{User: &discord.User{ID: id, Username: strings.ToLower(name), GlobalName: name}, Permissions: "32"}
	}
	you := member(youID, "Alex")
	command := func(name string, opts ...discord.Option) *discord.Interaction {
		return &discord.Interaction{Type: discord.InteractionApplicationCommand, Token: "preview", GuildID: guild, Member: you,
			Data: discord.InteractionData{Name: name, Options: opts}}
	}
	clickOn := func(customID string, values ...string) *discord.Interaction {
		return &discord.Interaction{Type: discord.InteractionMessageComponent, Token: "preview", GuildID: guild, Member: you,
			Data: discord.InteractionData{CustomID: customID, Values: values}}
	}
	opt := func(name string, value string) discord.Option {
		raw, _ := json.Marshal(value)
		return discord.Option{Name: name, Value: raw}
	}

	run("/help", command("help"))
	run("/link", command("link"))
	run("/profile", command("profile"))
	run("/iceberg", command("iceberg"))
	run("/collage", command("collage"))
	run("/game", command("game", opt("game", "548430")))
	run("/whoplays", command("whoplays", opt("game", "548430")))
	run("/leaderboard", command("leaderboard"))
	run("/leaderboard → Crowns tab", clickOn("lb:set:crowns"))
	run("/crowns", command("crowns"))
	run("/taste", command("taste", opt("friend", previewMara)))
	run("/incommon", command("incommon", opt("friend", previewMara), opt("friend2", previewTheo)))
	run("/chart", command("chart"))
	run("/playtests", command("playtests"))
	run("/playtests → details", clickOn("pt:s:0:", "0"))
	run("/goty", command("goty"))
	run("/goty scope:server", command("goty", opt("scope", "server")))
	run("/collection", command("collection"))
	run("/collection Indie hall of fame", command("collection", opt("name", "indie-hall-of-fame")))
	run("/four (first time: picker)", command("four"))
	run("/four → after picking", clickOn("four:set", "548430", "427520", "646570", "1092790"))
	run("/four", command("four"))
	run("/verdict", command("verdict"))
	run("/verdict → Appeal", clickOn("vd:a:"+youID))
	run("/backlog", command("backlog"))
	run("/backlog → Pick another", clickOn(fmt.Sprintf("bl:r:%s:%d:1", youID, backlogSeed(youID, now))))
	if ms, err := b.moments(); err == nil && len(ms) > 0 {
		if data, err := b.momentCard(ctx, ms[0], guild); err == nil {
			cards = append(cards, PreviewCard{"Moment post", data})
		}
		run("Moment → after a vote", clickOn("mo:"+ms[0].ID+":"+ms[0].Choices[0].ID))
	}

	// Last, since hiding changes what the server cards show.
	run("/visibility hide", command("visibility", opt("setting", "hide")))
	run("/unlink", command("unlink"))

	all, err := playtests.Load(playtestsFile)
	if err != nil {
		return nil, err
	}
	if current := playtests.Current(all, b.now()); len(current) > 0 {
		if data, err := b.radarMessage(ctx, guild, current[0]); err == nil {
			cards = append(cards, PreviewCard{"Radar post", data})
		}
		if err := b.radarWelcome(ctx, store.Feed{GuildID: guild}); err == nil {
			cards = append(cards, PreviewCard{"Radar welcome", capture.lastPost()})
		}
	}
	if data, ok, err := b.chartMessage(ctx, guild, "weekly", "posts every Monday"); err == nil && ok {
		data.Components = linkButtonsOnly(data.Components)
		cards = append(cards, PreviewCard{"Weekly chart post", data})
	}
	if len(problems) > 0 {
		return cards, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return cards, nil
}

type previewGame struct {
	id, name string
	reviews  int
	mp       bool
	genres   []string
	year     int // release year; 0 means 2020
}

var previewGames = []previewGame{
	{"730", "Counter-Strike 2", 9000000, true, []string{"Action", "Free To Play"}, 0},
	{"413150", "Stardew Valley", 650000, true, []string{"Indie", "RPG", "Simulation"}, 0},
	{"548430", "Deep Rock Galactic", 304804, true, []string{"Action"}, 0},
	{"427520", "Factorio", 180000, true, []string{"Strategy", "Simulation"}, 0},
	{"2379780", "Balatro", 150000, false, []string{"Strategy", "Indie"}, 0},
	{"646570", "Slay the Spire", 140000, false, []string{"Strategy", "Indie"}, 0},
	{"1092790", "Inscryption", 45000, false, []string{"Adventure", "Indie"}, 2026},
	{"1102190", "Monster Train", 25000, true, []string{"Strategy", "Indie"}, 0},
	{"1267910", "Melvor Idle", 20000, false, []string{"RPG", "Casual"}, 0},
	{"1434950", "HighFleet", 8000, false, []string{"Strategy", "Simulation"}, 2026},
	{"1127500", "Mini Motorways", 9500, false, []string{"Strategy", "Casual"}, 0},
	{"1782120", "ZERO Sievert", 1500, false, []string{"Action", "RPG"}, 2026},
	{"887570", "NEBULOUS: Fleet Command", 450, true, []string{"Strategy", "Simulation"}, 0},
	{"2221490", "Tom Clancy's The Division 2", 210000, true, []string{"Action", "RPG"}, 0},
	{"230410", "Warframe", 700000, true, []string{"Action", "Free To Play"}, 0},
	{"1085660", "Destiny 2", 650000, true, []string{"Action", "Free To Play"}, 0},
}

// seedPreview links you and two sample friends, all visible in the guild.
func seedPreview(ctx context.Context, st *store.Store, guild, youID string, now time.Time) error {
	type play struct {
		id            string
		hours, recent float64
	}
	libraries := []struct {
		id, name string
		games    []play
	}{
		{youID, "Alex", []play{{"548430", 412, 6}, {"730", 301, 0}, {"427520", 260, 0}, {"646570", 150, 0}, {"413150", 120, 0},
			{"2379780", 85, 9}, {"1267910", 70, 0}, {"1102190", 60, 0}, {"1434950", 40, 0}, {"1092790", 30, 5},
			{"887570", 25, 0}, {"1782120", 18, 0}, {"1127500", 12, 0},
			{"230410", 0, 0}, {"1085660", 0, 0}, {"2221490", 0, 0}}},
		{previewMara, "Mara", []play{{"230410", 600, 0}, {"427520", 400, 0}, {"2221490", 300, 20}, {"548430", 288, 11},
			{"413150", 50, 0}, {"2379780", 20, 14}}},
		{previewTheo, "Theo", []play{{"1085660", 250, 8}, {"413150", 200, 0}, {"548430", 140, 0}, {"2379780", 120, 15}, {"646570", 90, 0}}},
	}
	names := map[string]string{}
	for _, g := range previewGames {
		names[g.id] = g.name
	}
	for _, lib := range libraries {
		if err := st.LinkAccount(ctx, store.Account{DiscordID: lib.id, Platform: steamPlatform, ExternalID: "preview-" + lib.id, LinkedAt: now}); err != nil {
			return err
		}
		var games []store.Game
		var entries []store.LibraryEntry
		for _, p := range lib.games {
			games = append(games, store.Game{GameID: p.id, Name: names[p.id], ImageURL: steam.HeaderImage(p.id), StoreURL: steam.StoreURL(p.id)})
			entries = append(entries, store.LibraryEntry{GameID: p.id, MinutesTotal: int(p.hours * 60), MinutesRecent: int(p.recent * 60)})
		}
		profile := store.Profile{DisplayName: lib.name, AvatarURL: "https://cdn.discordapp.com/embed/avatars/0.png"}
		if err := st.SaveSync(ctx, lib.id, steamPlatform, profile, games, entries, now); err != nil {
			return err
		}
		if err := st.SetVisibility(ctx, guild, lib.id, true, now); err != nil {
			return err
		}
	}
	if err := st.SetFavorites(ctx, previewMara, steamPlatform, []string{"427520", "230410", "548430", "2379780"}, now); err != nil {
		return err
	}
	if err := st.SetFavorites(ctx, previewTheo, steamPlatform, []string{"413150", "646570", "2379780", "1085660"}, now); err != nil {
		return err
	}
	for _, g := range previewGames {
		mp := g.mp
		if err := st.SetGameDetails(ctx, steamPlatform, g.id, store.GameDetails{Multiplayer: &mp, Genres: g.genres, Reviews: g.reviews, ReleaseYear: max(g.year, 2020)}, now); err != nil {
			return err
		}
	}
	return nil
}

// previewSyncer never touches Steam; the preview data is already "synced".
type previewSyncer struct{}

func (previewSyncer) Sync(context.Context, string, string) (store.Profile, int, error) {
	return store.Profile{}, 0, nil
}
func (previewSyncer) SyncSoon(string, string)                          {}
func (previewSyncer) CheckGame(context.Context, string) (*bool, error) { return nil, nil }

// captureDiscord keeps deferred edits and webhook posts instead of sending them.
type captureDiscord struct {
	mu    sync.Mutex
	edits []discord.ResponseData
	posts []discord.WebhookMessage
}

func (c *captureDiscord) EditOriginal(_ context.Context, _ string, d discord.ResponseData) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.edits = append(c.edits, d)
	return nil
}
func (c *captureDiscord) CreateWebhook(context.Context, string, string) (discord.Webhook, error) {
	return discord.Webhook{ID: "preview", Token: "preview"}, nil
}
func (c *captureDiscord) DeleteWebhook(context.Context, discord.Webhook) error { return nil }
func (c *captureDiscord) ExecuteWebhook(_ context.Context, _ discord.Webhook, m discord.WebhookMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.posts = append(c.posts, m)
	return nil
}

func (c *captureDiscord) last() discord.ResponseData {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.edits) == 0 {
		return discord.ResponseData{Content: "(no reply)"}
	}
	return c.edits[len(c.edits)-1]
}

func (c *captureDiscord) lastPost() discord.ResponseData {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.posts) == 0 {
		return discord.ResponseData{Content: "(no post)"}
	}
	m := c.posts[len(c.posts)-1]
	return discord.ResponseData{Content: m.Content, Embeds: m.Embeds, Components: m.Components, Flags: m.Flags}
}

func previewSecret(s string) []byte {
	if s == "" {
		s = strings.Repeat("p", 32)
	}
	return []byte(s)
}
