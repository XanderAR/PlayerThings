package bot

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"playradar/internal/discord"
	"playradar/internal/linktoken"
	"playradar/internal/playtests"
	"playradar/internal/store"
)

const steamPlatform = "steam"

// ----- /link -----

func (b *Bot) cmdLink(ctx context.Context, in *discord.Interaction) discord.Response {
	user := in.Invoker()
	token := linktoken.Sign(b.LinkSecret, linktoken.Claims{
		DiscordID: user.ID, GuildID: in.GuildID, Expires: b.now().Add(30 * time.Minute),
	})
	linkURL := b.Links.Site + "/link?t=" + url.QueryEscape(token)

	acct, linked, err := b.Store.Account(ctx, user.ID, steamPlatform)
	if err != nil {
		return b.oops(err, "load account")
	}
	e := discord.Embed{Color: colorAccent, Title: "Connect your accounts"}
	label := "Link Steam"
	if linked {
		label = "Re-link Steam"
		e.Description = fmt.Sprintf("You're linked to Steam as **%s**. Link again to switch accounts or refresh your library.", acct.DisplayName)
	} else {
		e.Description = "Link once and your library and playtime follow you into every server."
	}
	e.Fields = []discord.EmbedField{
		{Name: "Steam", Value: "Library and playtime, synced twice a day", Inline: true},
		{Name: "player.gg", Value: "Shelf, reviews and XP (coming)", Inline: true},
		{Name: "PlayStation", Value: "Coming", Inline: true},
	}
	if in.InGuild() {
		e.Footer = &discord.EmbedFooter{Text: "Linking here adds you to this server's leaderboards. Change it any time with /visibility."}
	}

	buttons := []discord.Component{
		discord.LinkButton(label, linkURL),
		discord.LinkButton("Join player.gg", b.Links.Go(b.Links.Join(), "link", in.GuildID)),
	}
	if linked && in.InGuild() {
		visible, err := b.Store.Visible(ctx, in.GuildID, user.ID)
		if err != nil {
			return b.oops(err, "load visibility")
		}
		if !visible {
			buttons = append(buttons, discord.Button(discord.ButtonPrimary, "Show me in this server", "vis:show"))
		}
	}
	return message(discord.ResponseData{
		Embeds:     []discord.Embed{e},
		Components: []discord.Component{discord.ActionRow(buttons...)},
		Flags:      discord.FlagEphemeral,
	})
}

// ----- /profile -----

func (b *Bot) cmdProfile(ctx context.Context, in *discord.Interaction) discord.Response {
	invoker := in.Invoker()
	target := invoker
	if id := in.Data.StringOption("player"); id != "" && id != invoker.ID {
		if !in.InGuild() {
			return reply("You can only look up other people inside a server where they've opted in.", true)
		}
		visible, err := b.Store.Visible(ctx, in.GuildID, id)
		if err != nil {
			return b.oops(err, "load visibility")
		}
		if !visible {
			return reply(mention(id)+" hasn't linked an account here, or is hidden in this server.", true)
		}
		target = discord.User{ID: id}
		if u, ok := in.Data.ResolvedUser(id); ok {
			target = u
		}
	}
	self := target.ID == invoker.ID

	acct, linked, err := b.Store.Account(ctx, target.ID, steamPlatform)
	if err != nil {
		return b.oops(err, "load account")
	}
	if !linked {
		if self {
			return reply("You haven't linked an account yet. Run `/link` to get your profile card.", true)
		}
		return reply(mention(target.ID)+" hasn't linked an account yet.", true)
	}
	if self {
		stale := b.now().Sub(acct.SyncedAt) > 6*time.Hour
		retryPrivate := acct.LibraryPrivate && b.now().Sub(acct.SyncedAt) > 10*time.Minute
		if stale || retryPrivate {
			b.Syncer.SyncSoon(target.ID, steamPlatform)
		}
	}

	name := target.DisplayName()
	if name == "" {
		name = acct.DisplayName
	}
	e := discord.Embed{
		Color:     colorAccent,
		Title:     name + "'s games",
		Author:    &discord.EmbedAuthor{Name: "Steam · " + acct.DisplayName, URL: acct.ProfileURL},
		Thumbnail: &discord.EmbedImage{URL: acct.AvatarURL},
		Footer:    &discord.EmbedFooter{Text: "Steam synced " + ago(acct.SyncedAt, b.now())},
	}
	buttons := []discord.Component{}

	if acct.LibraryPrivate {
		e.Description = privateLibraryHelp
	} else {
		stats, err := b.Store.LibraryStats(ctx, target.ID, steamPlatform)
		if err != nil {
			return b.oops(err, "library stats")
		}
		top, err := b.Store.TopGames(ctx, target.ID, steamPlatform, false, 5)
		if err != nil {
			return b.oops(err, "top games")
		}
		recent, err := b.Store.TopGames(ctx, target.ID, steamPlatform, true, 3)
		if err != nil {
			return b.oops(err, "recent games")
		}
		e.Fields = []discord.EmbedField{
			{Name: "Played", Value: hours(stats.Minutes), Inline: true},
			{Name: "Library", Value: fmt.Sprintf("%s %s", thousands(stats.Games), plural(stats.Games, "game", "games")), Inline: true},
			{Name: "Last 2 weeks", Value: hours(stats.RecentMinutes), Inline: true},
		}
		if len(top) > 0 {
			e.Fields = append(e.Fields, discord.EmbedField{Name: "Most played", Value: rankLines(top, false)})
		}
		if len(recent) > 0 {
			e.Fields = append(e.Fields, discord.EmbedField{Name: "Playing lately", Value: rankLines(recent, true)})
		}
		var feature *store.PlayedGame
		if len(recent) > 0 {
			feature = &recent[0]
		} else if len(top) > 0 {
			feature = &top[0]
		}
		if feature != nil {
			e.Image = &discord.EmbedImage{URL: feature.ImageURL}
			buttons = append(buttons, b.gameButton(feature.Game, "profile", in.GuildID))
		}
		if acct.SyncedAt.IsZero() {
			e.Description = "Your library is syncing. Check back in a minute."
		}
	}
	buttons = append(buttons, discord.LinkButton("Get your player.gg shelf", b.Links.Go(b.Links.Join(), "profile", in.GuildID)))
	return message(discord.ResponseData{
		Embeds:     []discord.Embed{e},
		Components: []discord.Component{discord.ActionRow(buttons...)},
	})
}

// ----- /playtests -----

func (b *Bot) cmdPlaytests(ctx context.Context, in *discord.Interaction) discord.Response {
	all, err := playtests.Load(b.PlaytestsFile)
	if err != nil {
		return b.oops(err, "load playtests")
	}
	current := playtests.Current(all, b.now())
	if genre := strings.ToLower(strings.TrimSpace(in.Data.StringOption("genre"))); genre != "" {
		var filtered []playtests.Playtest
		for _, p := range current {
			for _, g := range p.Genres {
				if strings.Contains(strings.ToLower(g), genre) {
					filtered = append(filtered, p)
					break
				}
			}
		}
		current = filtered
	}
	browse := discord.LinkButton("All playtests on player.gg", b.Links.Go(b.Links.Playtests(), "playtests", in.GuildID))
	if len(current) == 0 {
		return message(discord.ResponseData{
			Content:    "No playtests are listed here right now. player.gg has the full list.",
			Components: []discord.Component{discord.ActionRow(browse)},
		})
	}

	shown := current
	if len(shown) > 4 {
		shown = shown[:4]
	}
	live := 0
	for _, p := range current {
		if p.Live(b.now()) {
			live++
		}
	}
	upcoming := len(current) - live

	var embeds []discord.Embed
	var buttons []discord.Component
	for _, p := range shown {
		embeds = append(embeds, b.playtestEmbed(p, "playtests", in.GuildID))
		buttons = append(buttons, discord.LinkButton("Request access: "+p.Title, b.Links.Go(p.URL, "playtests", in.GuildID)))
	}
	embeds[len(embeds)-1].Footer = &discord.EmbedFooter{Text: "From " + b.PlaytestsSource + " · sign up on player.gg"}
	buttons = append(buttons, browse)

	return message(discord.ResponseData{
		Content:    playtestSummary(live, upcoming),
		Embeds:     embeds,
		Components: []discord.Component{discord.ActionRow(buttons...)},
	})
}

func playtestSummary(live, upcoming int) string {
	switch {
	case live > 0 && upcoming > 0:
		return fmt.Sprintf("%d %s live and %d %s soon.", live, plural(live, "playtest is", "playtests are"), upcoming, plural(upcoming, "starts", "start"))
	case live > 0:
		return fmt.Sprintf("%d %s live.", live, plural(live, "playtest is", "playtests are"))
	default:
		return fmt.Sprintf("%d %s soon.", upcoming, plural(upcoming, "playtest starts", "playtests start"))
	}
}

func (b *Bot) playtestEmbed(p playtests.Playtest, command, guildID string) discord.Embed {
	e := discord.Embed{
		Color:       colorSoon,
		Title:       p.Title,
		URL:         b.Links.Go(p.URL, command, guildID),
		Description: p.Summary,
		Fields: []discord.EmbedField{
			{Name: "Status", Value: p.Status(b.now()), Inline: true},
		},
	}
	if p.Live(b.now()) {
		e.Color = colorLive
	}
	if len(p.Platforms) > 0 {
		e.Fields = append(e.Fields, discord.EmbedField{Name: "Platforms", Value: strings.Join(p.Platforms, ", "), Inline: true})
	}
	if p.Studio != "" {
		e.Fields = append(e.Fields, discord.EmbedField{Name: "Studio", Value: p.Studio, Inline: true})
	}
	if p.Image != "" {
		e.Thumbnail = &discord.EmbedImage{URL: p.Image}
	}
	return e
}

// ----- /whoplays -----

func (b *Bot) cmdWhoPlays(ctx context.Context, in *discord.Interaction) discord.Response {
	if !in.InGuild() {
		return reply("`/whoplays` works inside servers.", true)
	}
	text := strings.TrimSpace(in.Data.StringOption("game"))
	game, found, err := b.resolveGame(ctx, in.GuildID, text)
	if err != nil {
		return b.oops(err, "resolve game")
	}
	if !found {
		return reply(fmt.Sprintf("Nobody here who has linked owns a game matching “%s”. Members can join with `/link`.", text), true)
	}
	ranked, owners, err := b.Store.WhoPlays(ctx, in.GuildID, steamPlatform, game.GameID, 15)
	if err != nil {
		return b.oops(err, "who plays")
	}
	members, err := b.Store.VisibleMembers(ctx, in.GuildID, steamPlatform)
	if err != nil {
		return b.oops(err, "count members")
	}

	var lines []string
	for i, r := range ranked {
		prefix := fmt.Sprintf("`%d`", i+1)
		if i == 0 && r.MinutesTotal > 0 {
			prefix = "👑"
		}
		line := fmt.Sprintf("%s %s · **%s**", prefix, mention(r.DiscordID), hours(r.MinutesTotal))
		if r.MinutesRecent > 0 {
			line += fmt.Sprintf(" · %s last 2 weeks", hours(r.MinutesRecent))
		}
		lines = append(lines, line)
	}
	if more := owners - len(ranked); more > 0 {
		lines = append(lines, fmt.Sprintf("…and %d more", more))
	}
	e := discord.Embed{
		Color:       colorSoon,
		Title:       "Who plays " + game.Name,
		URL:         b.Links.Go(b.Links.Game(game.GameID), "whoplays", in.GuildID),
		Description: strings.Join(lines, "\n"),
		Thumbnail:   &discord.EmbedImage{URL: game.ImageURL},
		Footer: &discord.EmbedFooter{Text: fmt.Sprintf("%d of %d linked %s %s it · /link to get on the list",
			owners, members, plural(members, "member", "members"), plural(owners, "owns", "own"))},
	}
	return message(discord.ResponseData{
		Embeds:     []discord.Embed{e},
		Components: []discord.Component{discord.ActionRow(b.gameButton(game, "whoplays", in.GuildID))},
	})
}

// resolveGame turns the option value into a game: autocomplete sends the
// game ID, but people can also type a name and press enter.
func (b *Bot) resolveGame(ctx context.Context, guildID, text string) (store.Game, bool, error) {
	if text == "" {
		return store.Game{}, false, nil
	}
	if isDigits(text) {
		if g, ok, err := b.Store.Game(ctx, steamPlatform, text); err != nil || ok {
			return g, ok, err
		}
	}
	return b.Store.FindServerGame(ctx, guildID, steamPlatform, text)
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// ----- /incommon -----

func (b *Bot) cmdInCommon(ctx context.Context, in *discord.Interaction) discord.Response {
	if !in.InGuild() {
		return reply("`/incommon` works inside servers.", true)
	}
	invoker := in.Invoker().ID
	ids := []string{invoker}
	for _, opt := range []string{"friend", "friend2", "friend3", "friend4"} {
		if id := in.Data.StringOption(opt); id != "" && !contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) < 2 {
		return reply("Pick at least one other person.", true)
	}
	if _, linked, err := b.Store.Account(ctx, invoker, steamPlatform); err != nil {
		return b.oops(err, "load account")
	} else if !linked {
		return reply("Link your account first with `/link`.", true)
	}
	var missing []string
	for _, id := range ids[1:] {
		visible, err := b.Store.Visible(ctx, in.GuildID, id)
		if err != nil {
			return b.oops(err, "load visibility")
		}
		_, linked, err := b.Store.Account(ctx, id, steamPlatform)
		if err != nil {
			return b.oops(err, "load account")
		}
		if !visible || !linked {
			missing = append(missing, mention(id))
		}
	}
	if len(missing) > 0 {
		return reply(fmt.Sprintf("%s %s linked here yet, or %s hidden in this server. They can join with `/link`.",
			strings.Join(missing, ", "), plural(len(missing), "hasn't", "haven't"), plural(len(missing), "is", "are")), true)
	}

	guildID := in.GuildID
	return b.later(in, false, func(ctx context.Context) discord.ResponseData {
		common, err := b.Store.CommonGames(ctx, steamPlatform, ids, 40)
		if err != nil {
			b.log().Error("common games", "err", err)
			return discord.ResponseData{Content: "Something went wrong on my side. Try again in a minute."}
		}
		// Look up a few unknown multiplayer tags now; the syncer fills in the rest over time.
		lookups := 0
		for i := range common {
			if lookups >= 4 {
				break
			}
			if common[i].CheckedAt.IsZero() {
				lookups++
				if mp, err := b.Syncer.CheckGame(ctx, common[i].GameID); err == nil {
					common[i].Multiplayer = mp
				}
			}
		}
		return b.renderInCommon(ids, common, guildID)
	})
}

func (b *Bot) renderInCommon(ids []string, common []store.CommonGame, guildID string) discord.ResponseData {
	var people []string
	for _, id := range ids {
		people = append(people, mention(id))
	}
	e := discord.Embed{Color: colorGroup, Title: "Games you all own", Description: strings.Join(people, " ")}
	if len(common) == 0 {
		e.Description += "\n\nYou don't share any Steam games yet."
		return discord.ResponseData{Embeds: []discord.Embed{e}}
	}

	var multi, rest []store.CommonGame
	for _, g := range common {
		if g.Multiplayer != nil && *g.Multiplayer {
			multi = append(multi, g)
		} else {
			rest = append(rest, g)
		}
	}
	if len(multi) > 0 {
		var lines []string
		for i, g := range multi {
			if i == 8 {
				break
			}
			lines = append(lines, fmt.Sprintf("`%d` %s · **%s** combined", i+1, g.Name, hours(g.CombinedMinutes)))
		}
		e.Fields = append(e.Fields, discord.EmbedField{Name: "Multiplayer", Value: strings.Join(lines, "\n")})
	}
	if len(rest) > 0 {
		var names []string
		for i, g := range rest {
			if i == 6 {
				names = append(names, fmt.Sprintf("+%d more", len(rest)-6))
				break
			}
			names = append(names, g.Name)
		}
		e.Fields = append(e.Fields, discord.EmbedField{Name: "Also in everyone's library", Value: strings.Join(names, ", ")})
	}
	e.Footer = &discord.EmbedFooter{Text: "Multiplayer tags from the Steam store"}

	top := common[0].Game
	if len(multi) > 0 {
		top = multi[0].Game
	}
	e.Thumbnail = &discord.EmbedImage{URL: top.ImageURL}
	return discord.ResponseData{
		Embeds:     []discord.Embed{e},
		Components: []discord.Component{discord.ActionRow(b.gameButton(top, "incommon", guildID))},
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ----- /chart -----

func (b *Bot) cmdChart(ctx context.Context, in *discord.Interaction) discord.Response {
	if !in.InGuild() {
		return reply("`/chart` works inside servers.", true)
	}
	if channel := in.Data.StringOption("weekly_in"); channel != "" {
		if !in.Member.CanManageServer() {
			return reply("Setting up weekly posts needs the Manage Server permission. Plain `/chart` works for everyone.", true)
		}
		return b.setupFeed(in, store.FeedWeeklyChart, channel)
	}
	data, ok, err := b.chartMessage(ctx, in.GuildID, "chart", "")
	if err != nil {
		return b.oops(err, "server chart")
	}
	if !ok {
		return reply("Nothing to chart yet: nobody who linked here has played on Steam in the last two weeks. Members can join with `/link`.", true)
	}
	return message(data)
}

// chartMessage builds the server chart. ok is false when there's nothing to show.
func (b *Bot) chartMessage(ctx context.Context, guildID, command, footerNote string) (discord.ResponseData, bool, error) {
	entries, err := b.Store.ServerChart(ctx, guildID, steamPlatform, 10)
	if err != nil || len(entries) == 0 {
		return discord.ResponseData{}, false, err
	}
	members, err := b.Store.VisibleMembers(ctx, guildID, steamPlatform)
	if err != nil {
		return discord.ResponseData{}, false, err
	}
	var lines []string
	for i, c := range entries {
		lines = append(lines, fmt.Sprintf("`%d` %s · **%s** · %d %s", i+1, c.Name, hours(c.Minutes), c.Players, plural(c.Players, "player", "players")))
	}
	footer := fmt.Sprintf("From %d linked %s", members, plural(members, "member", "members"))
	if footerNote != "" {
		footer += " · " + footerNote
	}
	e := discord.Embed{
		Color:       colorAccent,
		Title:       "Most played here · last 2 weeks",
		Description: strings.Join(lines, "\n"),
		Thumbnail:   &discord.EmbedImage{URL: entries[0].ImageURL},
		Footer:      &discord.EmbedFooter{Text: footer},
	}
	return discord.ResponseData{
		Embeds:     []discord.Embed{e},
		Components: []discord.Component{discord.ActionRow(b.gameButton(entries[0].Game, command, guildID))},
	}, true, nil
}

// ----- /visibility -----

func (b *Bot) cmdVisibility(ctx context.Context, in *discord.Interaction) discord.Response {
	if !in.InGuild() {
		return reply("`/visibility` works inside servers.", true)
	}
	user := in.Invoker().ID
	show := in.Data.StringOption("setting") == "show"
	if show {
		if _, linked, err := b.Store.Account(ctx, user, steamPlatform); err != nil {
			return b.oops(err, "load account")
		} else if !linked {
			return reply("Link an account first with `/link`.", true)
		}
	}
	if err := b.Store.SetVisibility(ctx, in.GuildID, user, show, b.now()); err != nil {
		return b.oops(err, "set visibility")
	}
	if show {
		return reply("You now appear in this server's `/whoplays`, `/chart` and profile lookups.", true)
	}
	return reply("You're hidden in this server. Your account stays linked for other servers; `/unlink` deletes everything.", true)
}

// ----- /unlink -----

func (b *Bot) cmdUnlink(ctx context.Context, in *discord.Interaction) discord.Response {
	has, err := b.Store.HasData(ctx, in.Invoker().ID)
	if err != nil {
		return b.oops(err, "check data")
	}
	if !has {
		return reply("Nothing is stored about you.", true)
	}
	return message(discord.ResponseData{
		Content: "This deletes your linked accounts, synced games and server settings. It can't be undone.",
		Components: []discord.Component{discord.ActionRow(
			discord.Button(discord.ButtonDanger, "Delete my data", "unlink:confirm"),
			discord.Button(discord.ButtonSecondary, "Cancel", "unlink:cancel"),
		)},
		Flags: discord.FlagEphemeral,
	})
}

// ----- buttons -----

func (b *Bot) component(ctx context.Context, in *discord.Interaction) discord.Response {
	user := in.Invoker().ID
	switch in.Data.CustomID {
	case "unlink:confirm":
		if err := b.Store.DeleteUser(ctx, user); err != nil {
			return b.oops(err, "delete user")
		}
		return update(discord.ResponseData{Content: "Done. Everything stored about you is deleted.", ClearComponents: true})
	case "unlink:cancel":
		return update(discord.ResponseData{Content: "Cancelled. Nothing was deleted.", ClearComponents: true})
	case "vis:show":
		if !in.InGuild() {
			return reply("That only works in a server.", true)
		}
		if err := b.Store.SetVisibility(ctx, in.GuildID, user, true, b.now()); err != nil {
			return b.oops(err, "set visibility")
		}
		return update(discord.ResponseData{Content: "You now show up in this server's leaderboards.", ClearComponents: true})
	case "radar:off":
		return b.radarOff(ctx, in)
	}
	return reply("That button has expired.", true)
}

// ----- autocomplete -----

func (b *Bot) autocomplete(ctx context.Context, in *discord.Interaction) discord.Response {
	choices := []discord.Choice{}
	focused, ok := in.Data.Focused()
	if ok && in.Data.Name == "whoplays" && focused.Name == "game" && in.InGuild() {
		matches, err := b.Store.SearchServerGames(ctx, in.GuildID, steamPlatform, focused.StringValue(), 25)
		if err != nil {
			b.log().Error("autocomplete", "err", err)
		}
		for _, m := range matches {
			choices = append(choices, discord.Choice{
				Name:  discord.Truncate(fmt.Sprintf("%s · %d %s", m.Name, m.Owners, plural(m.Owners, "owns it", "own it")), 100),
				Value: m.GameID,
			})
		}
	}
	return discord.Response{Type: discord.ResponseAutocomplete, Data: discord.AutocompleteData{Choices: choices}}
}
