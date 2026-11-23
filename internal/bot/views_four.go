package bot

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"playradar/internal/discord"
	"playradar/internal/render"
	"playradar/internal/store"
)

// ----- /four -----
//
// Four favorites, after Letterboxd: the four games that made you, shown as
// covers on a card. In a server the card also shows who else here shares
// them, which is what gets people posting theirs.

const colorFour = 0xFF5A7A

var fourSlots = []string{"first", "second", "third", "fourth"}

func (b *Bot) cmdFour(ctx context.Context, in *discord.Interaction) discord.Response {
	var picks []string
	for _, slot := range fourSlots {
		if v := strings.TrimSpace(in.Data.StringOption(slot)); v != "" {
			picks = append(picks, v)
		}
	}
	if len(picks) > 0 {
		return b.setFour(ctx, in, picks)
	}
	target, self, stop := b.target(ctx, in, "player")
	if stop != nil {
		return *stop
	}
	acct, stop := b.account(ctx, target, self)
	if stop != nil {
		return *stop
	}
	favs, err := b.Store.Favorites(ctx, target.ID, steamPlatform)
	if err != nil {
		return b.oops(err, "load favorites")
	}
	if len(favs) == 0 {
		if self {
			return b.fourPicker(ctx, in)
		}
		return message(notice(true, colorMuted, displayName(target, acct)+" hasn't picked their four yet",
			"Show them how it's done.", discord.ActionRow(discord.Button(discord.ButtonPrimary, "Pick your four", "four:pick"))))
	}
	data, err := b.fourCard(ctx, in, target, acct, favs)
	if err != nil {
		return b.oops(err, "four card")
	}
	return message(data)
}

// setFour saves games typed into the first..fourth options and shows the card.
func (b *Bot) setFour(ctx context.Context, in *discord.Interaction, picks []string) discord.Response {
	me := in.Invoker()
	acct, stop := b.account(ctx, me, true)
	if stop != nil {
		return *stop
	}
	var ids []string
	seen := map[string]bool{}
	for _, text := range picks {
		g, found, err := b.findGame(ctx, in, text)
		if err != nil {
			return b.oops(err, "find favorite")
		}
		if !found {
			return reply(fmt.Sprintf("I couldn't find “%s”. Pick games from the list as you type: it covers your library and this server's.", text), true)
		}
		if !seen[g.GameID] {
			seen[g.GameID] = true
			ids = append(ids, g.GameID)
		}
	}
	return b.saveFour(ctx, in, me, acct, ids, false)
}

func (b *Bot) saveFour(ctx context.Context, in *discord.Interaction, me discord.User, acct store.Account, ids []string, edit bool) discord.Response {
	if err := b.Store.SetFavorites(ctx, me.ID, steamPlatform, ids, b.now()); err != nil {
		return b.oops(err, "save favorites")
	}
	favs, err := b.Store.Favorites(ctx, me.ID, steamPlatform)
	if err != nil {
		return b.oops(err, "load favorites")
	}
	data, err := b.fourCard(ctx, in, me, acct, favs)
	if err != nil {
		return b.oops(err, "four card")
	}
	if edit {
		// The picker is private, so show the card there with a way to post it.
		data.Components = append(data.Components[:1], discord.ActionRow(
			discord.Button(discord.ButtonPrimary, "Post it here", "four:post"),
			discord.Button(discord.ButtonSecondary, "Change them", "four:pick"),
		))
		return update(data)
	}
	return message(data)
}

// fourPicker is a private menu of someone's 25 most-played games.
func (b *Bot) fourPicker(ctx context.Context, in *discord.Interaction) discord.Response {
	me := in.Invoker().ID
	games, err := b.Store.TopGames(ctx, me, steamPlatform, false, 25)
	if err != nil {
		return b.oops(err, "picker games")
	}
	if len(games) == 0 {
		return message(notice(true, colorMuted, "Nothing to pick from yet",
			"Your Steam library has no playtime yet. You can still type any game: `/four first:` and pick from the list."))
	}
	current, err := b.Store.Favorites(ctx, me, steamPlatform)
	if err != nil {
		return b.oops(err, "load favorites")
	}
	chosen := map[string]bool{}
	for _, g := range current {
		chosen[g.GameID] = true
	}
	var opts []discord.SelectOption
	for _, g := range games {
		opts = append(opts, discord.SelectOption{
			Label: discord.Truncate(g.Name, 100), Value: g.GameID,
			Description: hours(g.MinutesTotal) + " played", Default: chosen[g.GameID],
		})
	}
	one := 1
	menu := discord.Select("four:set", "Choose up to four games", opts...)
	menu.MinValues, menu.MaxValues = &one, min(store.MaxFavorites, len(opts))
	return message(card(true, discord.Container(colorFour,
		discord.Text("### ❤️ Pick your four\nThe four games that made you. Here are your most played; pick up to four."),
		discord.ActionRow(menu),
		note("Favorite isn't in the list? Type it instead: `/four first:` and pick from the suggestions."),
	)))
}

// fourCard shows someone's four favorites.
func (b *Bot) fourCard(ctx context.Context, in *discord.Interaction, target discord.User, acct store.Account, favs []store.PlayedGame) (discord.ResponseData, error) {
	name := displayName(target, acct)
	var names []string
	total := 0
	for _, g := range favs {
		names = append(names, g.Name)
		total += g.MinutesTotal
	}
	sub := "The games that made them"
	if total > 0 {
		sub = hours(total) + " on Steam between them"
	}
	parts := []discord.Component{heading("❤️ "+name+"'s four favorites", sub, acct.AvatarURL)}
	if img, ok := b.shelf(render.Four, favs, false); ok {
		parts = append(parts, img)
	}
	parts = append(parts, note(strings.Join(names, " · ")))

	if in.InGuild() {
		matches, err := b.Store.FavoriteMatches(ctx, in.GuildID, steamPlatform, target.ID)
		if err != nil {
			return discord.ResponseData{}, err
		}
		if len(matches) > 0 {
			parts = append(parts, line(), discord.Text(sharedFavorites(favs, matches)))
		}
	}
	return card(false,
		discord.Container(colorFour, parts...),
		discord.ActionRow(
			discord.Button(discord.ButtonPrimary, "Pick your four", "four:pick"),
			discord.LinkButton("Build your shelf on player.gg", b.Links.Go(b.Links.Join(), "four", in.GuildID)),
		),
	), nil
}

// sharedFavorites says which favorites other members here share, and who
// shares the most.
func sharedFavorites(favs []store.PlayedGame, matches map[string][]string) string {
	var who []string
	for id := range matches {
		who = append(who, id)
	}
	// Closest matches first, so they're named first on each game too.
	sort.Slice(who, func(i, j int) bool {
		if len(matches[who[i]]) != len(matches[who[j]]) {
			return len(matches[who[i]]) > len(matches[who[j]])
		}
		return who[i] < who[j]
	})
	fans := map[string][]string{}
	for _, id := range who {
		for _, g := range matches[id] {
			fans[g] = append(fans[g], mention(id))
		}
	}
	lines := []string{"### Also in their four here"}
	for _, g := range favs {
		if names := fans[g.GameID]; len(names) > 0 {
			l := "▸ **" + g.Name + "** · " + strings.Join(names[:min(3, len(names))], ", ")
			if len(names) > 3 {
				l += fmt.Sprintf(" +%d", len(names)-3)
			}
			lines = append(lines, l)
		}
	}
	best := who[0]
	lines = append(lines, fmt.Sprintf("🤝 Closest match: %s, sharing %d of %d", mention(best), len(matches[best]), len(favs)))
	return strings.Join(lines, "\n")
}

// fourComponent handles the picker and its buttons.
func (b *Bot) fourComponent(ctx context.Context, in *discord.Interaction, parts []string) discord.Response {
	me := in.Invoker()
	if len(parts) != 2 {
		return reply("That button has expired. Run `/four` again.", true)
	}
	switch parts[1] {
	case "pick":
		if _, stop := b.account(ctx, me, true); stop != nil {
			return *stop
		}
		return b.fourPicker(ctx, in)
	case "set":
		acct, stop := b.account(ctx, me, true)
		if stop != nil {
			return *stop
		}
		var ids []string
		for _, v := range in.Data.Values {
			if _, ok, err := b.Store.Game(ctx, steamPlatform, v); err != nil {
				return b.oops(err, "check favorite")
			} else if ok && len(ids) < store.MaxFavorites {
				ids = append(ids, v)
			}
		}
		if len(ids) == 0 {
			return reply("Pick at least one game.", true)
		}
		return b.saveFour(ctx, in, me, acct, ids, true)
	case "post":
		acct, stop := b.account(ctx, me, true)
		if stop != nil {
			return *stop
		}
		favs, err := b.Store.Favorites(ctx, me.ID, steamPlatform)
		if err != nil {
			return b.oops(err, "load favorites")
		}
		if len(favs) == 0 {
			return b.fourPicker(ctx, in)
		}
		data, err := b.fourCard(ctx, in, me, acct, favs)
		if err != nil {
			return b.oops(err, "four card")
		}
		return message(data)
	}
	return reply("That button has expired. Run `/four` again.", true)
}

// favoriteChoices suggests games for /four: your own library first, then
// games people in this server own.
func (b *Bot) favoriteChoices(ctx context.Context, in *discord.Interaction, query string) []discord.Choice {
	var choices []discord.Choice
	seen := map[string]bool{}
	for _, g := range b.searchOwnGames(ctx, in.Invoker().ID, query, 25) {
		seen[g.GameID] = true
		choices = append(choices, discord.Choice{Name: discord.Truncate(g.Name+" · "+hours(g.MinutesTotal), 100), Value: g.GameID})
	}
	if in.InGuild() && len(choices) < 25 {
		matches, err := b.Store.SearchServerGames(ctx, in.GuildID, steamPlatform, query, 25)
		if err != nil {
			b.log().Error("favorite autocomplete", "err", err)
		}
		for _, m := range matches {
			if seen[m.GameID] || len(choices) == 25 {
				continue
			}
			choices = append(choices, discord.Choice{Name: discord.Truncate(m.Name, 100), Value: m.GameID})
		}
	}
	return choices
}
