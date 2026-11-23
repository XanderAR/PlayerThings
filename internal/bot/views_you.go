package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"playradar/internal/discord"
	"playradar/internal/render"
	"playradar/internal/store"
)

// ----- /game -----

func (b *Bot) cmdGame(ctx context.Context, in *discord.Interaction) discord.Response {
	text := strings.TrimSpace(in.Data.StringOption("game"))
	game, found, err := b.findGame(ctx, in, text)
	if err != nil {
		return b.oops(err, "find game")
	}
	if !found {
		return reply(fmt.Sprintf("I couldn't find “%s”. Pick a game from the list as you type: it covers your library and this server's.", text), true)
	}
	return b.gameReply(ctx, in, game.GameID, false)
}

// findGame resolves a typed name or an autocomplete ID: this server's games
// first, then the invoker's own library.
func (b *Bot) findGame(ctx context.Context, in *discord.Interaction, text string) (store.Game, bool, error) {
	if text == "" {
		return store.Game{}, false, nil
	}
	if isDigits(text) {
		if g, ok, err := b.Store.Game(ctx, steamPlatform, text); err != nil || ok {
			return g, ok, err
		}
	}
	if in.InGuild() {
		if g, ok, err := b.Store.FindServerGame(ctx, in.GuildID, steamPlatform, text); err != nil || ok {
			return g, ok, err
		}
	}
	if own := b.searchOwnGames(ctx, in.Invoker().ID, text, 1); len(own) > 0 {
		return own[0].Game, true, nil
	}
	return store.Game{}, false, nil
}

// gameReply shows a game card. Games not yet looked up on the Steam store are
// looked up first, behind a deferred reply.
func (b *Bot) gameReply(ctx context.Context, in *discord.Interaction, gameID string, ephemeral bool) discord.Response {
	game, ok, err := b.Store.Game(ctx, steamPlatform, gameID)
	if err != nil {
		return b.oops(err, "load game")
	}
	if !ok {
		return reply("I don't know that game yet.", true)
	}
	if game.DetailsAt.IsZero() {
		return b.later(in, ephemeral, func(ctx context.Context) discord.ResponseData {
			if _, err := b.Syncer.CheckGame(ctx, gameID); err != nil {
				b.log().Warn("look up game", "err", err)
			}
			if fresh, ok, err := b.Store.Game(ctx, steamPlatform, gameID); err == nil && ok {
				game = fresh
			}
			data, err := b.gameCard(ctx, in, game)
			if err != nil {
				b.log().Error("game card", "err", err)
				return discord.ResponseData{Content: "Something went wrong on my side. Try again in a minute."}
			}
			return data
		})
	}
	data, err := b.gameCard(ctx, in, game)
	if err != nil {
		return b.oops(err, "game card")
	}
	if ephemeral {
		data.Flags |= discord.FlagEphemeral
	}
	return message(data)
}

func (b *Bot) gameCard(ctx context.Context, in *discord.Interaction, game store.Game) (discord.ResponseData, error) {
	var meta []string
	if game.ReleaseYear > 0 {
		meta = append(meta, strconv.Itoa(game.ReleaseYear))
	}
	if len(game.Genres) > 0 {
		g := game.Genres
		if len(g) > 3 {
			g = g[:3]
		}
		meta = append(meta, strings.Join(g, " · "))
	}
	if game.Reviews > 0 {
		meta = append(meta, thousands(game.Reviews)+" Steam reviews")
	}
	head := "## " + game.Name
	if len(meta) > 0 {
		head += "\n-# " + strings.Join(meta, " · ")
	}

	var facts []string
	me := in.Invoker().ID
	if pg, owned, err := b.Store.Owned(ctx, me, steamPlatform, game.GameID); err != nil {
		return discord.ResponseData{}, err
	} else if owned {
		l := "**You** · " + hours(pg.MinutesTotal)
		if pg.MinutesRecent > 0 {
			l += " · " + hours(pg.MinutesRecent) + " in the last 2 weeks"
		}
		facts = append(facts, l)
	} else if _, linked, _ := b.Store.Account(ctx, me, steamPlatform); linked {
		facts = append(facts, "**You** · not in your library")
	}
	if in.InGuild() {
		ranked, owners, err := b.Store.WhoPlays(ctx, in.GuildID, steamPlatform, game.GameID, 1)
		if err != nil {
			return discord.ResponseData{}, err
		}
		members, err := b.Store.VisibleMembers(ctx, in.GuildID, steamPlatform)
		if err != nil {
			return discord.ResponseData{}, err
		}
		facts = append(facts, fmt.Sprintf("**This server** · %d of %d linked %s %s it",
			owners, members, plural(members, "member", "members"), plural(owners, "owns", "own")))
		if len(ranked) > 0 && ranked[0].MinutesTotal >= crownMinutes {
			facts = append(facts, fmt.Sprintf("**Most hours here** · 👑 %s with %s", mention(ranked[0].DiscordID), hours(ranked[0].MinutesTotal)))
		}
	}
	mp := "Not known yet"
	if game.Multiplayer != nil {
		mp = "No"
		if *game.Multiplayer {
			mp = "Yes"
		}
	}
	facts = append(facts, "**Multiplayer** · "+mp)
	if !game.DetailsAt.IsZero() {
		t := icebergTiers[tierOf(game.Reviews)]
		facts = append(facts, fmt.Sprintf("**Iceberg tier** · %s %s", t.emoji, t.name))
	}

	var parts []discord.Component
	if game.ImageURL != "" {
		parts = append(parts, discord.Gallery(discord.MediaItem{Media: discord.Media{URL: game.ImageURL}}))
	}
	parts = append(parts, discord.Text(head), line(), discord.Text(strings.Join(facts, "\n")))

	buttons := []discord.Component{
		b.viewButton(game, "game", in.GuildID),
		discord.LinkButton("Steam store", steamStore(game.GameID)),
	}
	if in.InGuild() {
		buttons = append(buttons, discord.Button(discord.ButtonSecondary, "Who plays it here", "wp:"+game.GameID))
	}
	return card(false, discord.Container(colorAccent, parts...), discord.ActionRow(buttons...)), nil
}

// ----- /iceberg -----

type icebergTier struct {
	name, emoji, band string
	min, color        int
}

// icebergTiers sort games by Steam review count, a rough stand-in for how
// many people have played them. Deeper means more obscure.
var icebergTiers = []icebergTier{
	// Colors run from ice white through blue to violet, bright enough to
	// read on Discord's dark theme.
	{"Tip of the iceberg", "🧊", "200k+ Steam reviews", 200000, 0xE3F6FF},
	{"Just below the surface", "🌊", "50k-200k reviews", 50000, 0x8FD3F5},
	{"Deeper waters", "🐠", "10k-50k reviews", 10000, 0x4DA8F0},
	{"The deep", "🦈", "2k-10k reviews", 2000, 0x3B7BF0},
	{"The abyss", "🐙", "500-2k reviews", 500, 0x6A5CF5},
	{"The trench", "🦑", "under 500 reviews", 0, 0x9B4DF0},
}

func tierOf(reviews int) int {
	for i, t := range icebergTiers {
		if reviews >= t.min {
			return i
		}
	}
	return len(icebergTiers) - 1
}

// icebergGames is how many of someone's most-played games go on the iceberg.
const icebergGames = 40

func (b *Bot) cmdIceberg(ctx context.Context, in *discord.Interaction) discord.Response {
	target, self, stop := b.target(ctx, in, "player")
	if stop != nil {
		return *stop
	}
	acct, stop := b.account(ctx, target, self)
	if stop != nil {
		return *stop
	}
	if acct.LibraryPrivate {
		return message(notice(true, colorMuted, "Nothing to show yet", privateLibraryHelp))
	}
	return b.later(in, false, func(ctx context.Context) discord.ResponseData {
		games, err := b.Store.TopGames(ctx, target.ID, steamPlatform, false, icebergGames)
		if err != nil {
			b.log().Error("iceberg games", "err", err)
			return discord.ResponseData{Content: "Something went wrong on my side. Try again in a minute."}
		}
		// Look a few unknown games up now; the background sync covers the rest.
		looked := 0
		for _, g := range games {
			if g.DetailsAt.IsZero() && looked < 6 {
				looked++
				if _, err := b.Syncer.CheckGame(ctx, g.GameID); err != nil {
					b.log().Warn("look up game", "err", err)
					break
				}
			}
		}
		if looked > 0 {
			if fresh, err := b.Store.TopGames(ctx, target.ID, steamPlatform, false, icebergGames); err == nil {
				games = fresh
			}
		}
		return b.icebergCard(displayName(target, acct), self, games, in.GuildID)
	})
}

func (b *Bot) icebergCard(name string, self bool, games []store.PlayedGame, guildID string) discord.ResponseData {
	buckets := make([][]string, len(icebergTiers))
	pending := 0
	var depth, minutes float64
	for _, g := range games {
		if g.DetailsAt.IsZero() {
			pending++
			continue
		}
		t := tierOf(g.Reviews)
		buckets[t] = append(buckets[t], g.Name)
		depth += float64(t) * float64(g.MinutesTotal)
		minutes += float64(g.MinutesTotal)
	}
	if minutes == 0 {
		return notice(false, colorMuted, "🧊 "+name+"'s gaming iceberg",
			"These games are still being looked up on Steam. Try again in a few minutes.")
	}
	comps := []discord.Component{
		discord.Text(fmt.Sprintf("## 🧊 %s's gaming iceberg\n-# %d most-played games, from the ones everyone knows to the deep cuts", name, len(games))),
	}
	for i, t := range icebergTiers {
		if len(buckets[i]) == 0 {
			continue
		}
		comps = append(comps, discord.Container(t.color,
			discord.Text(fmt.Sprintf("### %s %s\n-# %s\n%s", t.emoji, t.name, t.band, joinMore(buckets[i], 8)))))
	}
	score := int(depth/minutes/float64(len(icebergTiers)-1)*100 + 0.5)
	foot := fmt.Sprintf("-# Depth score **%d**/100, weighted by hours. Higher means deeper cuts.", score)
	if pending > 0 {
		foot += fmt.Sprintf(" %d more %s still being looked up.", pending, plural(pending, "game is", "games are"))
	}
	comps = append(comps, discord.Text(foot),
		discord.ActionRow(discord.LinkButton("Build your shelf on player.gg", b.Links.Go(b.Links.Join(), "iceberg", guildID))))
	return card(false, comps...)
}

// ----- /collage -----

func (b *Bot) cmdCollage(ctx context.Context, in *discord.Interaction) discord.Response {
	target, self, stop := b.target(ctx, in, "player")
	if stop != nil {
		return *stop
	}
	acct, stop := b.account(ctx, target, self)
	if stop != nil {
		return *stop
	}
	recent := in.Data.StringOption("period") == "recent"
	games, err := b.Store.TopGames(ctx, target.ID, steamPlatform, recent, 10)
	if err != nil {
		return b.oops(err, "collage games")
	}
	name := displayName(target, acct)
	sub := "All time, by hours played"
	if recent {
		sub = "Last 2 weeks, by hours played"
	}
	gallery, ok := b.shelf(render.Grid, games, recent)
	if !ok {
		return message(notice(true, colorMuted, "🖼️ "+name+"'s top games", "Nothing to show for this period yet."))
	}
	var names []string
	for i, g := range games {
		names = append(names, fmt.Sprintf("**%d** %s", i+1, g.Name))
	}
	return message(card(false,
		discord.Container(colorAccent,
			discord.Text("## 🖼️ "+name+"'s top games\n-# "+sub),
			gallery,
			note(strings.Join(names, " · ")),
		),
		discord.ActionRow(b.gameButton(games[0].Game, "collage", in.GuildID),
			discord.LinkButton("Get your player.gg shelf", b.Links.Go(b.Links.Join(), "collage", in.GuildID))),
	))
}

// ----- /help -----

func (b *Bot) cmdHelp(ctx context.Context, in *discord.Interaction) discord.Response {
	return message(card(true,
		discord.Container(colorAccent,
			heading(b.AppName, "Your games and player.gg playtests, inside Discord", ""),
			line(),
			discord.Text("### You\n"+
				"`/link` connect Steam\n"+
				"`/profile` your card: hours, top games, what you're playing\n"+
				"`/iceberg` your games from mainstream to deep cuts\n"+
				"`/collage` your top games in one grid\n"+
				"`/goty` your Game of the Year race, then vote on player.gg\n"+
				"`/collection` how much of a collection you've played\n"+
				"`/game` one game: your hours and who here plays it"),
			discord.Text("### This server\n"+
				"`/whoplays` everyone here who owns a game\n"+
				"`/leaderboard` hours, games and crowns\n"+
				"`/crowns` games where you have the most hours here\n"+
				"`/taste` how your library compares with a friend's\n"+
				"`/incommon` multiplayer games your group all owns\n"+
				"`/chart` the server's most played this fortnight"),
			discord.Text("### Playtests\n"+
				"`/playtests` live and upcoming playtests on player.gg\n"+
				"`/radar` admins: post new ones in a channel"),
			line(),
			note("Privacy: `/visibility` shows or hides you per server · `/unlink` deletes everything"),
		),
		discord.ActionRow(discord.LinkButton("player.gg", b.Links.Go(b.Links.PlayerGG, "help", in.GuildID))),
	))
}
