package bot

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"playradar/internal/discord"
	"playradar/internal/linktoken"
	"playradar/internal/render"
	"playradar/internal/store"
)

const steamPlatform = "steam"

// ----- /link -----

func (b *Bot) cmdLink(ctx context.Context, in *discord.Interaction) discord.Response {
	user := in.Invoker()
	token := linktoken.Sign(b.LinkSecret, linktoken.Claims{
		DiscordID: user.ID, GuildID: in.GuildID, Name: user.DisplayName(), Expires: b.now().Add(30 * time.Minute),
	})
	linkURL := b.Links.Site + "/link?t=" + url.QueryEscape(token)
	steamOnly := linkURL
	if b.DiscordLinking {
		// One click when Steam is connected to their Discord; the page it
		// lands on falls back to Steam sign-in when it isn't.
		linkURL = b.Links.Site + "/link/discord?t=" + url.QueryEscape(token)
	}

	acct, linked, err := b.Store.Account(ctx, user.ID, steamPlatform)
	if err != nil {
		return b.oops(err, "load account")
	}
	steam, label := "**Steam**\n-# Library and playtime, synced twice a day", "Link Steam"
	if b.DiscordLinking {
		steam = "**Steam**\n-# One click if Steam is connected to your Discord. Or [sign in on Steam](" + steamOnly + ")"
	}
	if linked {
		steam = fmt.Sprintf("**Steam** · linked as **%s**\n-# Link again to switch accounts or refresh your library", acct.DisplayName)
		label = "Re-link"
	}
	parts := []discord.Component{
		heading("Connect your accounts", "Link once and your games follow you into every server", ""),
		line(),
		discord.Section(discord.LinkButton(label, linkURL), steam),
		discord.Section(discord.LinkButton("Join player.gg", b.Links.Go(b.Links.Join(), "link", in.GuildID)),
			"**player.gg**\n-# Your shelf, reviews and playtest invites"),
		discord.Text("**PlayStation** · **Xbox**\n-# Coming soon"),
	}
	if in.InGuild() {
		parts = append(parts, line(), note("Linking here adds you to this server's leaderboards. Change it any time with `/visibility`."))
	}
	comps := []discord.Component{discord.Container(colorAccent, parts...)}
	if linked && in.InGuild() {
		visible, err := b.Store.Visible(ctx, in.GuildID, user.ID)
		if err != nil {
			return b.oops(err, "load visibility")
		}
		if !visible {
			comps = append(comps, discord.ActionRow(discord.Button(discord.ButtonPrimary, "Show me in this server", "vis:show")))
		}
	}
	return message(card(true, comps...))
}

// ----- whose data a command shows -----

// target works out whose data to show: the given user option if set,
// otherwise the person who ran the command. Someone else must be visible in
// this server. A non-nil Response means stop and send it.
func (b *Bot) target(ctx context.Context, in *discord.Interaction, option string) (discord.User, bool, *discord.Response) {
	invoker := in.Invoker()
	id := in.Data.StringOption(option)
	if id == "" || id == invoker.ID {
		return invoker, true, nil
	}
	if !in.InGuild() {
		r := reply("You can only look up other people inside a server where they've opted in.", true)
		return discord.User{}, false, &r
	}
	visible, err := b.Store.Visible(ctx, in.GuildID, id)
	if err != nil {
		r := b.oops(err, "load visibility")
		return discord.User{}, false, &r
	}
	if !visible {
		r := reply(mention(id)+" hasn't linked an account here, or is hidden in this server.", true)
		return discord.User{}, false, &r
	}
	u := discord.User{ID: id}
	if ru, ok := in.Data.ResolvedUser(id); ok {
		u = ru
	}
	return u, false, nil
}

// account loads a linked account, or the reply to send when there isn't one.
func (b *Bot) account(ctx context.Context, u discord.User, self bool) (store.Account, *discord.Response) {
	acct, linked, err := b.Store.Account(ctx, u.ID, steamPlatform)
	if err != nil {
		r := b.oops(err, "load account")
		return acct, &r
	}
	if !linked {
		r := reply(mention(u.ID)+" hasn't linked an account yet.", true)
		if self {
			r = reply("You haven't linked an account yet. Run `/link` to get started.", true)
		}
		return acct, &r
	}
	return acct, nil
}

func displayName(u discord.User, acct store.Account) string {
	if n := u.DisplayName(); n != "" {
		return n
	}
	return acct.DisplayName
}

// ----- /profile -----

func (b *Bot) cmdProfile(ctx context.Context, in *discord.Interaction) discord.Response {
	target, self, stop := b.target(ctx, in, "player")
	if stop != nil {
		return *stop
	}
	acct, stop := b.account(ctx, target, self)
	if stop != nil {
		return *stop
	}
	if self {
		loaded, err := b.libraryLoaded(ctx, acct)
		if err != nil {
			return b.oops(err, "library stats")
		}
		if !loaded {
			// The first sync failed or hasn't finished: load it now and show
			// the card when it's done, instead of asking to run it again.
			return b.later(in, false, func(ctx context.Context) discord.ResponseData {
				syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				if _, _, err := b.Syncer.Sync(syncCtx, target.ID, steamPlatform); err != nil {
					b.log().Warn("profile sync failed", "err", err)
					return discord.ResponseData{Content: "Steam didn't send your library just now. I'll keep trying every 10 minutes, so run `/profile` again later."}
				}
				fresh, _, err := b.Store.Account(ctx, target.ID, steamPlatform)
				if err == nil {
					var data discord.ResponseData
					if data, err = b.profileCard(ctx, in, target, fresh); err == nil {
						return data
					}
				}
				b.log().Error("profile after sync", "err", err)
				return discord.ResponseData{Content: "Something went wrong on my side. Try again in a minute."}
			})
		}
		stale := b.now().Sub(acct.SyncedAt) > 6*time.Hour
		retryPrivate := acct.LibraryPrivate && b.now().Sub(acct.SyncedAt) > 10*time.Minute
		retryFailed := acct.SyncError != "" && b.now().Sub(acct.SyncedAt) > time.Minute
		if stale || retryPrivate || retryFailed {
			b.Syncer.SyncSoon(target.ID, steamPlatform)
		}
	}
	data, err := b.profileCard(ctx, in, target, acct)
	if err != nil {
		return b.oops(err, "profile card")
	}
	return message(data)
}

// libraryLoaded reports whether an account's library has synced at least once.
func (b *Bot) libraryLoaded(ctx context.Context, acct store.Account) (bool, error) {
	if acct.LibraryPrivate || (acct.SyncError == "" && !acct.SyncedAt.IsZero()) {
		return true, nil
	}
	stats, err := b.Store.LibraryStats(ctx, acct.DiscordID, acct.Platform)
	return stats.Games > 0, err
}

// profileCard renders a linked account's profile card.
func (b *Bot) profileCard(ctx context.Context, in *discord.Interaction, target discord.User, acct store.Account) (discord.ResponseData, error) {
	name := displayName(target, acct)
	sub := "Steam · " + acct.DisplayName + " · synced " + ago(acct.SyncedAt, b.now())
	join := discord.LinkButton("Get your player.gg shelf", b.Links.Go(b.Links.Join(), "profile", in.GuildID))

	if acct.LibraryPrivate {
		return card(false,
			discord.Container(colorMuted, heading(name, sub, acct.AvatarURL), line(), discord.Text(privateLibraryHelp)),
			discord.ActionRow(join),
		), nil
	}
	stats, err := b.Store.LibraryStats(ctx, target.ID, steamPlatform)
	if err != nil {
		return discord.ResponseData{}, err
	}
	if stats.Games == 0 && (acct.SyncError != "" || acct.SyncedAt.IsZero()) {
		return notice(true, colorMuted, name+"'s games", "Their Steam library hasn't loaded yet."), nil
	}
	top, err := b.Store.TopGames(ctx, target.ID, steamPlatform, false, 5)
	if err != nil {
		return discord.ResponseData{}, err
	}
	recent, err := b.Store.TopGames(ctx, target.ID, steamPlatform, true, 3)
	if err != nil {
		return discord.ResponseData{}, err
	}

	parts := []discord.Component{
		heading(name, sub, acct.AvatarURL),
		line(),
		discord.Text(fmt.Sprintf("**%s** played  ·  **%s** %s  ·  **%s** in the last 2 weeks",
			hours(stats.Minutes), thousands(stats.Games), plural(stats.Games, "game", "games"), hours(stats.RecentMinutes))),
	}
	favs, err := b.Store.Favorites(ctx, target.ID, steamPlatform)
	if err != nil {
		return discord.ResponseData{}, err
	}
	if len(favs) > 0 {
		var names []string
		for _, g := range favs {
			names = append(names, g.Name)
		}
		parts = append(parts, discord.Text("❤️ **Four favorites** · "+strings.Join(names, " · ")))
	}
	if len(top) > 0 {
		parts = append(parts, gap(), discord.Text("### Most played\n"+rankLines(top, false)))
	}
	if len(recent) > 0 {
		parts = append(parts, discord.Text("### Playing lately\n"+recentLines(recent)))
	}
	if g, ok := b.shelf(render.Strip, top, false); ok {
		parts = append(parts, g)
	}
	if acct.SyncedAt.IsZero() {
		parts = append(parts, note("Your library is syncing. Check back in a minute."))
	}
	parts = append(parts, line(), note("More: `/four` · `/verdict` · `/iceberg` · `/collage` · `/taste`"))

	var buttons []discord.Component
	if len(recent) > 0 {
		buttons = append(buttons, b.gameButton(recent[0].Game, "profile", in.GuildID))
	} else if len(top) > 0 {
		buttons = append(buttons, b.gameButton(top[0].Game, "profile", in.GuildID))
	}
	buttons = append(buttons, join)
	return card(false, discord.Container(colorAccent, parts...), discord.ActionRow(buttons...)), nil
}

// recentLines lists recently played games: "▸ **Hades II** · 10 h".
func recentLines(games []store.PlayedGame) string {
	var lines []string
	for _, g := range games {
		lines = append(lines, fmt.Sprintf("▸ **%s** · %s", g.Name, hours(g.MinutesRecent)))
	}
	return strings.Join(lines, "\n")
}

// gameGallery shows up to n game images: wide Steam headers, or portrait
// covers, which crop better into a grid. ok is false when there are none.
func gameGallery(games []store.PlayedGame, n int, covers bool) (discord.Component, bool) {
	var items []discord.MediaItem
	for _, g := range games {
		url := g.ImageURL
		if covers && g.Platform == steamPlatform {
			url = coverURL(g.GameID)
		}
		if url == "" {
			continue
		}
		items = append(items, discord.MediaItem{Media: discord.Media{URL: url}})
		if len(items) == n {
			break
		}
	}
	if len(items) == 0 {
		return discord.Component{}, false
	}
	return discord.Gallery(items...), true
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
		return message(notice(true, colorLive, "You're visible here", "You now appear in this server's `/whoplays`, `/leaderboard`, `/crowns` and `/chart`."))
	}
	return message(notice(true, colorMuted, "You're hidden here", "Your account stays linked for other servers. `/unlink` deletes everything."))
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
	return message(notice(true, colorMuted, "Delete your data?",
		"This deletes your linked accounts, synced games and server settings. It can't be undone.",
		discord.ActionRow(
			discord.Button(discord.ButtonDanger, "Delete my data", "unlink:confirm"),
			discord.Button(discord.ButtonSecondary, "Cancel", "unlink:cancel"),
		)))
}

// ----- buttons and menus -----

func (b *Bot) component(ctx context.Context, in *discord.Interaction) discord.Response {
	user := in.Invoker().ID
	switch in.Data.CustomID {
	case "unlink:confirm":
		if err := b.Store.DeleteUser(ctx, user); err != nil {
			return b.oops(err, "delete user")
		}
		return update(notice(true, colorMuted, "Deleted", "Everything stored about you is gone."))
	case "unlink:cancel":
		return update(notice(true, colorMuted, "Cancelled", "Nothing was deleted."))
	case "vis:show":
		if !in.InGuild() {
			return reply("That only works in a server.", true)
		}
		if err := b.Store.SetVisibility(ctx, in.GuildID, user, true, b.now()); err != nil {
			return b.oops(err, "set visibility")
		}
		return update(notice(true, colorLive, "You're visible here", "You now show up in this server's leaderboards."))
	case "radar:off":
		return b.radarOff(ctx, in)
	}
	parts := strings.Split(in.Data.CustomID, ":")
	switch parts[0] {
	case "pt":
		return b.playtestsComponent(ctx, in, parts)
	case "lb":
		return b.leaderboardComponent(ctx, in, parts)
	case "game":
		if len(parts) == 2 {
			return b.gameReply(ctx, in, parts[1], true)
		}
	case "wp":
		if len(parts) == 2 {
			return b.whoPlaysReply(ctx, in, parts[1], true)
		}
	case "goty":
		if !in.InGuild() {
			return reply("The server race only works inside servers.", true)
		}
		data, err := b.serverGOTY(ctx, in.GuildID, b.now().Year())
		if err != nil {
			return b.oops(err, "server goty")
		}
		data = b.withNudge(ctx, in, data)
		data.Flags |= discord.FlagEphemeral
		return message(data)
	case "col":
		return b.collectionComponent(ctx, in, parts)
	case "mo":
		return b.momentComponent(ctx, in, parts)
	case "vd":
		return b.verdictComponent(ctx, in, parts)
	case "four":
		return b.fourComponent(ctx, in, parts)
	case "bl":
		return b.backlogComponent(ctx, in, parts)
	case "nudge":
		return b.nudgeComponent(in)
	}
	return reply("That button has expired. Run the command again.", true)
}

// ----- autocomplete -----

func (b *Bot) autocomplete(ctx context.Context, in *discord.Interaction) discord.Response {
	choices := []discord.Choice{}
	focused, ok := in.Data.Focused()
	if ok && focused.Name == "name" && in.Data.Name == "collection" {
		list, _ := b.collections()
		q := strings.ToLower(focused.StringValue())
		for _, c := range list {
			if q == "" || strings.Contains(strings.ToLower(c.Title), q) {
				choices = append(choices, discord.Choice{Name: discord.Truncate(c.Emoji+" "+c.Title, 100), Value: c.Slug})
			}
			if len(choices) == 25 {
				break
			}
		}
	}
	if ok && in.Data.Name == "four" {
		choices = append(choices, b.favoriteChoices(ctx, in, focused.StringValue())...)
	}
	if ok && focused.Name == "game" {
		query := focused.StringValue()
		switch {
		case in.InGuild():
			matches, err := b.Store.SearchServerGames(ctx, in.GuildID, steamPlatform, query, 25)
			if err != nil {
				b.log().Error("autocomplete", "err", err)
			}
			for _, m := range matches {
				choices = append(choices, discord.Choice{
					Name:  discord.Truncate(fmt.Sprintf("%s · %d %s", m.Name, m.Owners, plural(m.Owners, "owns it", "own it")), 100),
					Value: m.GameID,
				})
			}
		case in.Data.Name == "game":
			for _, g := range b.searchOwnGames(ctx, in.Invoker().ID, query, 25) {
				choices = append(choices, discord.Choice{Name: discord.Truncate(g.Name+" · "+hours(g.MinutesTotal), 100), Value: g.GameID})
			}
		}
	}
	return discord.Response{Type: discord.ResponseAutocomplete, Data: discord.AutocompleteData{Choices: choices}}
}

// searchOwnGames finds games in one user's library by name, most played first.
func (b *Bot) searchOwnGames(ctx context.Context, discordID, query string, limit int) []store.PlayedGame {
	games, err := b.Store.TopGames(ctx, discordID, steamPlatform, false, 2000)
	if err != nil {
		b.log().Error("search own games", "err", err)
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	// Names starting with the text first, then a word starting with it, then
	// any match; most played first within each.
	var tiers [3][]store.PlayedGame
	for _, g := range games {
		name := strings.ToLower(g.Name)
		switch {
		case query == "" || strings.HasPrefix(name, query):
			tiers[0] = append(tiers[0], g)
		case strings.Contains(name, " "+query):
			tiers[1] = append(tiers[1], g)
		case strings.Contains(name, query):
			tiers[2] = append(tiers[2], g)
		}
	}
	out := append(append(tiers[0], tiers[1]...), tiers[2]...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
