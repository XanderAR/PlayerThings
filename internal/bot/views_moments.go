package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"playradar/internal/discord"
	"playradar/internal/render"
	"playradar/internal/store"
)

// ----- /goty -----

// tgaDate is The Game Awards 2026, mentioned on GOTY cards until it's over.
var tgaDate = time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC)

func (b *Bot) gotyButton(guildID string) discord.Component {
	return discord.LinkButton("🏆 Vote your GOTY on player.gg", b.Links.Go(b.Links.PlayerGG+"/goty", "goty", guildID))
}

func (b *Bot) gotyNote() string {
	if b.now().Before(tgaDate.AddDate(0, 0, 1)) {
		return "The Game Awards are on December 10. Hours aren't everything: **pick your winner on player.gg**, plus Biggest Obsession, Late to the Party and more."
	}
	return "Hours aren't everything: **pick your winner on player.gg**, plus Biggest Obsession, Late to the Party and more."
}

func (b *Bot) cmdGOTY(ctx context.Context, in *discord.Interaction) discord.Response {
	year := b.now().Year()
	if in.Data.StringOption("scope") == "server" {
		if !in.InGuild() {
			return reply("The server race only works inside servers.", true)
		}
		data, err := b.serverGOTY(ctx, in.GuildID, year)
		if err != nil {
			return b.oops(err, "server goty")
		}
		return message(data)
	}
	target, self, stop := b.target(ctx, in, "player")
	if stop != nil {
		return *stop
	}
	acct, stop := b.account(ctx, target, self)
	if stop != nil {
		return *stop
	}
	return b.later(in, false, func(ctx context.Context) discord.ResponseData {
		b.lookUpTopGames(ctx, target.ID, 6)
		games, err := b.Store.YearGames(ctx, target.ID, steamPlatform, year, 5)
		if err != nil {
			b.log().Error("year games", "err", err)
			return discord.ResponseData{Content: "Something went wrong on my side. Try again in a minute."}
		}
		return b.gotyCard(displayName(target, acct), games, year, in.GuildID)
	})
}

// lookUpTopGames fills in store details (release year, genres) for a few of
// someone's most-played games that haven't been looked up yet.
func (b *Bot) lookUpTopGames(ctx context.Context, discordID string, max int) {
	top, err := b.Store.TopGames(ctx, discordID, steamPlatform, false, 40)
	if err != nil {
		return
	}
	looked := 0
	for _, g := range top {
		if g.DetailsAt.IsZero() && looked < max {
			looked++
			if _, err := b.Syncer.CheckGame(ctx, g.GameID); err != nil {
				b.log().Warn("look up game", "err", err)
				return
			}
		}
	}
}

func (b *Bot) gotyCard(name string, games []store.PlayedGame, year int, guildID string) discord.ResponseData {
	parts := []discord.Component{
		discord.Text(fmt.Sprintf("## 🏆 %s's %d GOTY race\n-# %d releases they've played most", name, year, year)),
	}
	if len(games) == 0 {
		parts = append(parts, discord.Text(fmt.Sprintf("No %d releases in this Steam library yet, or they're still being looked up.", year)))
	} else {
		if g, ok := b.shelf(render.Strip, games, false); ok {
			parts = append(parts, g)
		}
		var lines []string
		for i, g := range games {
			lines = append(lines, fmt.Sprintf("%s **%s** · %s", medal(i), g.Name, hours(g.MinutesTotal)))
		}
		parts = append(parts, discord.Text(strings.Join(lines, "\n")))
	}
	parts = append(parts, line(), discord.Text(b.gotyNote()))
	rows := []discord.Component{b.gotyButton(guildID)}
	if guildID != "" {
		rows = append(rows, discord.Button(discord.ButtonSecondary, "Server race", "goty:server"))
	}
	return card(false, discord.Container(colorGold, parts...), discord.ActionRow(rows...))
}

func (b *Bot) serverGOTY(ctx context.Context, guildID string, year int) (discord.ResponseData, error) {
	entries, err := b.Store.ServerYearGames(ctx, guildID, steamPlatform, year, 10)
	if err != nil {
		return discord.ResponseData{}, err
	}
	members, err := b.Store.VisibleMembers(ctx, guildID, steamPlatform)
	if err != nil {
		return discord.ResponseData{}, err
	}
	parts := []discord.Component{
		discord.Text(fmt.Sprintf("## 🏆 This server's %d GOTY race\n-# Most played %d releases among %d linked %s", year, year, members, plural(members, "member", "members"))),
	}
	if len(entries) == 0 {
		parts = append(parts, discord.Text(fmt.Sprintf("Nobody here has played a %d release on Steam yet, or they're still being looked up.", year)))
	} else {
		var games []store.PlayedGame
		var lines []string
		for i, e := range entries {
			if i < 5 {
				games = append(games, store.PlayedGame{Game: e.Game, MinutesTotal: e.Minutes})
			}
			lines = append(lines, fmt.Sprintf("%s **%s** · %s · %d %s", medal(i), e.Name, hours(e.Minutes), e.Players, plural(e.Players, "player", "players")))
		}
		if g, ok := b.shelf(render.Strip, games, false); ok {
			parts = append(parts, g)
		}
		parts = append(parts, discord.Text(strings.Join(lines, "\n")))
	}
	parts = append(parts, line(), discord.Text(b.gotyNote()))
	return card(false, discord.Container(colorGold, parts...), discord.ActionRow(b.gotyButton(guildID))), nil
}

// ----- collections -----

// Collection is a hand-picked set of games, like a Letterboxd list.
type Collection struct {
	Slug  string           `json:"slug"`
	Emoji string           `json:"emoji"`
	Title string           `json:"title"`
	Blurb string           `json:"blurb"`
	URL   string           `json:"url"` // its page on player.gg, if there is one
	Games []CollectionGame `json:"games"`
}

// CollectionGame is one game; extra IDs are other Steam editions that count.
type CollectionGame struct {
	Name string   `json:"name"`
	IDs  []string `json:"ids"`
}

func (c Collection) ids() []string {
	var out []string
	for _, g := range c.Games {
		out = append(out, g.IDs...)
	}
	return out
}

// played returns minutes per game (0 = not played) from minutes per app ID.
func (c Collection) played(byID map[string]int) []int {
	out := make([]int, len(c.Games))
	for i, g := range c.Games {
		for _, id := range g.IDs {
			out[i] += byID[id]
		}
	}
	return out
}

func loadJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (b *Bot) collections() ([]Collection, error) {
	var list []Collection
	err := loadJSON(b.CollectionsFile, &list)
	return list, err
}

func (b *Bot) collectionsButton(guildID string) discord.Component {
	return discord.LinkButton("Browse collections on player.gg", b.Links.Go(b.Links.PlayerGG+"/collections", "collection", guildID))
}

func (b *Bot) cmdCollection(ctx context.Context, in *discord.Interaction) discord.Response {
	target, self, stop := b.target(ctx, in, "player")
	if stop != nil {
		return *stop
	}
	if _, stop := b.account(ctx, target, self); stop != nil {
		return *stop
	}
	list, err := b.collections()
	if err != nil {
		return b.oops(err, "load collections")
	}
	slug := in.Data.StringOption("name")
	for _, c := range list {
		if c.Slug == slug || strings.EqualFold(c.Title, slug) {
			data, err := b.collectionCard(ctx, c, target, in.GuildID)
			if err != nil {
				return b.oops(err, "collection")
			}
			return message(data)
		}
	}
	data, err := b.collectionsOverview(ctx, list, target, in.GuildID)
	if err != nil {
		return b.oops(err, "collections")
	}
	return message(data)
}

// collectionsOverview lists every collection with the person's progress.
func (b *Bot) collectionsOverview(ctx context.Context, list []Collection, target discord.User, guildID string) (discord.ResponseData, error) {
	var lines []string
	var options []discord.SelectOption
	for _, c := range list {
		byID, err := b.Store.PlayedMinutes(ctx, target.ID, steamPlatform, c.ids())
		if err != nil {
			return discord.ResponseData{}, err
		}
		done := 0
		for _, m := range c.played(byID) {
			if m > 0 {
				done++
			}
		}
		frac := float64(done) / float64(len(c.Games))
		lines = append(lines, fmt.Sprintf("%s **%s**\n%s  %d/%d", c.Emoji, c.Title, bar(frac), done, len(c.Games)))
		options = append(options, discord.SelectOption{Label: discord.Truncate(c.Title, 100), Value: c.Slug,
			Description: discord.Truncate(fmt.Sprintf("%d of %d played", done, len(c.Games)), 100)})
	}
	if len(lines) == 0 {
		return notice(true, colorMuted, "Collections", "No collections yet."), nil
	}
	if len(options) > 25 {
		options = options[:25]
	}
	return card(false,
		discord.Container(colorAccent,
			heading("📚 Collections", "How much of each one "+mention(target.ID)+" has played on Steam", ""),
			line(),
			discord.Text(strings.Join(lines, "\n")),
			line(),
			note("Pick one below to see every game and who here is furthest along."),
		),
		discord.ActionRow(discord.Select("col:"+target.ID, "Open a collection…", options...)),
		discord.ActionRow(b.collectionsButton(guildID)),
	), nil
}

// collectionCard shows one collection as a board of covers, played ones lit.
func (b *Bot) collectionCard(ctx context.Context, c Collection, target discord.User, guildID string) (discord.ResponseData, error) {
	byID, err := b.Store.PlayedMinutes(ctx, target.ID, steamPlatform, c.ids())
	if err != nil {
		return discord.ResponseData{}, err
	}
	mins := c.played(byID)
	done := 0
	var tiles []render.Tile
	var todo []string
	for i, g := range c.Games {
		if mins[i] > 0 {
			done++
		} else {
			todo = append(todo, g.Name)
		}
		if len(tiles) < 10 {
			tiles = append(tiles, render.Tile{AppID: g.IDs[0], Minutes: mins[i]})
		}
	}
	frac := float64(done) / float64(len(c.Games))
	status := fmt.Sprintf("**%s has played %d of %d** · %s %s", mention(target.ID), done, len(c.Games), bar(frac), pct(frac))
	if done == len(c.Games) {
		status = fmt.Sprintf("🎉 **%s has played every game in this collection.**", mention(target.ID))
	}
	parts := []discord.Component{
		discord.Text(fmt.Sprintf("## %s %s\n-# %s", c.Emoji, c.Title, c.Blurb)),
		discord.Text(status),
		discord.Gallery(discord.MediaItem{Media: discord.Media{URL: render.ShelfURL(b.Links.Site, b.LinkSecret, render.Set, tiles)}}),
	}
	if len(todo) > 0 && done < len(c.Games) {
		parts = append(parts, discord.Text("**Still to play:** "+joinMore(todo, 6)))
	}
	if guildID != "" {
		if leaders := b.collectionLeaders(ctx, c, guildID); leaders != "" {
			parts = append(parts, line(), discord.Text("### Furthest along here\n"+leaders))
		}
	}
	link := b.collectionsButton(guildID)
	if c.URL != "" {
		link = discord.LinkButton("This collection on player.gg", b.Links.Go(c.URL, "collection", guildID))
	}
	return card(false, discord.Container(colorAccent, parts...), discord.ActionRow(link)), nil
}

// collectionLeaders ranks this server's members by how much of c they've played.
func (b *Bot) collectionLeaders(ctx context.Context, c Collection, guildID string) string {
	byMember, err := b.Store.PlayedBy(ctx, guildID, steamPlatform, c.ids())
	if err != nil {
		b.log().Warn("collection leaders", "err", err)
		return ""
	}
	type row struct {
		id   string
		done int
	}
	var rows []row
	for who, ids := range byMember {
		have := map[string]bool{}
		for _, id := range ids {
			have[id] = true
		}
		done := 0
		for _, g := range c.Games {
			for _, id := range g.IDs {
				if have[id] {
					done++
					break
				}
			}
		}
		rows = append(rows, row{who, done})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].done != rows[j].done {
			return rows[i].done > rows[j].done
		}
		return rows[i].id < rows[j].id
	})
	var parts []string
	for i, r := range rows {
		if i == 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s %s %d/%d", medal(i), mention(r.id), r.done, len(c.Games)))
	}
	return strings.Join(parts, "  ·  ")
}

// collectionComponent opens a collection from the overview menu (col:<user>).
func (b *Bot) collectionComponent(ctx context.Context, in *discord.Interaction, parts []string) discord.Response {
	if len(parts) != 2 || len(in.Data.Values) != 1 {
		return reply("That menu has expired. Run `/collection` again.", true)
	}
	list, err := b.collections()
	if err != nil {
		return b.oops(err, "load collections")
	}
	for _, c := range list {
		if c.Slug == in.Data.Values[0] {
			data, err := b.collectionCard(ctx, c, discord.User{ID: parts[1]}, in.GuildID)
			if err != nil {
				return b.oops(err, "collection")
			}
			data.Flags |= discord.FlagEphemeral
			return message(data)
		}
	}
	return reply("That collection is gone.", true)
}

// ----- moments -----

// Moment is a rare, timely post for a big gaming moment (a launch, an awards
// show). It goes only to channels an admin chose with /radar, once per
// server, and asks members a one-tap question instead of advertising.
type Moment struct {
	ID         string         `json:"id"`
	Kicker     string         `json:"kicker"`
	Title      string         `json:"title"`
	Body       string         `json:"body"`
	Image      string         `json:"image"`
	Starts     string         `json:"starts"`
	Ends       string         `json:"ends"`
	Related    []string       `json:"related_steam_apps"`
	RelatedFor string         `json:"related_label"`
	Choices    []MomentChoice `json:"choices"`
	LinkLabel  string         `json:"link_label"`
	LinkURL    string         `json:"link_url"`
	Collection string         `json:"collection"`
}

// MomentChoice is one answer button.
type MomentChoice struct {
	ID    string `json:"id"`
	Emoji string `json:"emoji"`
	Label string `json:"label"`
}

func (m Moment) active(now time.Time) bool {
	day := now.UTC().Format("2006-01-02")
	return m.Starts <= day && (m.Ends == "" || day <= m.Ends)
}

func (b *Bot) moments() ([]Moment, error) {
	var list []Moment
	err := loadJSON(b.MomentsFile, &list)
	return list, err
}

// momentCard renders a moment for one server, with its live answer tally.
func (b *Bot) momentCard(ctx context.Context, m Moment, guildID string) (discord.ResponseData, error) {
	var parts []discord.Component
	if m.Image != "" {
		parts = append(parts, discord.Gallery(discord.MediaItem{Media: discord.Media{URL: m.Image}}))
	}
	parts = append(parts, discord.Text(fmt.Sprintf("-# %s\n## %s", m.Kicker, m.Title)), discord.Text(m.Body))
	if players, minutes, err := b.Store.ServerTotal(ctx, guildID, steamPlatform, m.Related); err != nil {
		return discord.ResponseData{}, err
	} else if players > 0 && m.RelatedFor != "" {
		parts = append(parts, discord.Text(fmt.Sprintf("🎯 **%d %s here** %s **%s** in %s between them.",
			players, plural(players, "member", "members"), plural(players, "has", "have"), hours(minutes), m.RelatedFor)))
	}
	counts, err := b.Store.VoteCounts(ctx, guildID, m.ID)
	if err != nil {
		return discord.ResponseData{}, err
	}
	var tally []string
	total := 0
	for _, c := range m.Choices {
		total += counts[c.ID]
		tally = append(tally, fmt.Sprintf("%s **%d** %s", c.Emoji, counts[c.ID], c.Label))
	}
	if total > 0 {
		parts = append(parts, line(), discord.Text(strings.Join(tally, "  ·  ")))
	} else {
		parts = append(parts, line(), note("Tap a button. Answers are counted for this server only."))
	}
	var buttons []discord.Component
	for _, c := range m.Choices {
		buttons = append(buttons, discord.Button(discord.ButtonSecondary, c.Emoji+" "+c.Label, "mo:"+m.ID+":"+c.ID))
	}
	if m.LinkURL != "" && len(buttons) < 5 {
		buttons = append(buttons, discord.LinkButton(m.LinkLabel, b.Links.Go(m.LinkURL, "moment", guildID)))
	}
	return card(false, discord.Container(0xF28C28, parts...), discord.ActionRow(buttons...)), nil
}

// momentComponent records an answer (mo:<moment>:<choice>) and refreshes the tally.
func (b *Bot) momentComponent(ctx context.Context, in *discord.Interaction, parts []string) discord.Response {
	if len(parts) != 3 || !in.InGuild() {
		return reply("That button has expired.", true)
	}
	list, err := b.moments()
	if err != nil {
		return b.oops(err, "load moments")
	}
	for _, m := range list {
		if m.ID != parts[1] {
			continue
		}
		valid := false
		for _, c := range m.Choices {
			valid = valid || c.ID == parts[2]
		}
		if !valid {
			break
		}
		if err := b.Store.Vote(ctx, in.GuildID, m.ID, in.Invoker().ID, parts[2], b.now()); err != nil {
			return b.oops(err, "vote")
		}
		data, err := b.momentCard(ctx, m, in.GuildID)
		if err != nil {
			return b.oops(err, "moment card")
		}
		return update(data)
	}
	return reply("That moment has passed.", true)
}

// postMoments posts each active moment once to every radar channel.
func (b *Bot) postMoments(ctx context.Context) {
	list, err := b.moments()
	if err != nil {
		b.log().Error("load moments", "err", err)
		return
	}
	var active []Moment
	for _, m := range list {
		if m.active(b.now()) {
			active = append(active, m)
		}
	}
	if len(active) == 0 {
		return
	}
	feeds, err := b.Store.Feeds(ctx, store.FeedRadar)
	if err != nil {
		b.log().Error("list radar feeds", "err", err)
		return
	}
	for _, f := range feeds {
		for _, m := range active {
			key := "moment:" + m.ID
			if done, err := b.Store.RadarPosted(ctx, f.GuildID, key); err != nil || done {
				continue
			}
			data, err := b.momentCard(ctx, m, f.GuildID)
			if err != nil {
				b.log().Error("build moment", "err", err)
				continue
			}
			if err := b.postWith(ctx, f, data, true); err != nil {
				b.log().Warn("moment post", "guild", f.GuildID, "err", err)
				break
			}
			_ = b.Store.MarkRadarPosted(ctx, f.GuildID, key, b.now())
		}
	}
}
