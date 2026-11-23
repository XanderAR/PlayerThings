package bot

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"playradar/internal/discord"
	"playradar/internal/render"
	"playradar/internal/store"
)

// ----- /backlog -----
//
// The pile of shame: how much of a Steam library has never been launched,
// the most acclaimed games sitting in it, and a "pick one for me" button
// that rerolls tonight's game.

const colorDust = 0xB08D57

// backlog is what the card is built from.
type backlog struct {
	owned    int
	unplayed []store.Game       // never launched, most reviewed first
	barely   []store.PlayedGame // played for under an hour
}

func (b *Bot) cmdBacklog(ctx context.Context, in *discord.Interaction) discord.Response {
	target, self, stop := b.target(ctx, in, "player")
	if stop != nil {
		return *stop
	}
	return b.backlogReply(ctx, in, target, self, backlogSeed(target.ID, b.now()), 0, false)
}

// backlogSeed starts each person on a different order of picks each day.
func backlogSeed(userID string, now time.Time) uint32 {
	h := fnv.New32a()
	h.Write([]byte(userID + now.Format("2006-01-02")))
	return h.Sum32()
}

func (b *Bot) backlogReply(ctx context.Context, in *discord.Interaction, target discord.User, self bool, seed uint32, roll int, edit bool) discord.Response {
	acct, stop := b.account(ctx, target, self)
	if stop != nil {
		return *stop
	}
	name := displayName(target, acct)
	if acct.LibraryPrivate {
		return message(notice(true, colorMuted, "Nothing to show yet", privateLibraryHelp))
	}
	var bl backlog
	var err error
	if bl.unplayed, err = b.Store.Unplayed(ctx, target.ID, steamPlatform); err != nil {
		return b.oops(err, "unplayed games")
	}
	played, err := b.Store.TopGames(ctx, target.ID, steamPlatform, false, verdictGames)
	if err != nil {
		return b.oops(err, "played games")
	}
	for _, g := range played {
		if g.MinutesTotal < 60 {
			bl.barely = append(bl.barely, g)
		}
	}
	bl.owned = len(bl.unplayed) + len(played)
	if bl.owned == 0 {
		return message(notice(true, colorMuted, "📚 "+name+"'s pile of shame", "This Steam library hasn't loaded yet. Give it a minute after linking."))
	}
	data, err := b.backlogCard(ctx, in, target.ID, name, acct.AvatarURL, bl, seed, roll)
	if err != nil {
		return b.oops(err, "backlog card")
	}
	if edit {
		return update(data)
	}
	return message(data)
}

func (b *Bot) backlogCard(ctx context.Context, in *discord.Interaction, userID, name, avatar string, bl backlog, seed uint32, roll int) (discord.ResponseData, error) {
	n := len(bl.unplayed)
	share := float64(n) / float64(bl.owned)
	head := heading("📚 "+name+"'s pile of shame", fmt.Sprintf("%s %s owned on Steam", thousands(bl.owned), plural(bl.owned, "game", "games")), avatar)

	if n == 0 && len(bl.barely) == 0 {
		return card(false, discord.Container(colorDust, head, line(),
			discord.Text("### No pile at all\nEvery game in this library has at least an hour on it. Suspicious."))), nil
	}

	stat := fmt.Sprintf("## %s never launched\n%s **%s** of the library · *%s*", thousands(n), bar(share), pct(share), shameVerdict(share))
	var extra []string
	if len(bl.barely) > 0 {
		extra = append(extra, fmt.Sprintf("🫠 **%d** more played for under an hour", len(bl.barely)))
	}
	if n > 0 {
		done := b.now().AddDate(0, 0, 7*n)
		extra = append(extra, fmt.Sprintf("📅 Clear one a week and you're done by **%s**", done.Format("January 2006")))
	}
	if rank, ok, err := b.backlogRank(ctx, in.GuildID, userID); err != nil {
		return discord.ResponseData{}, err
	} else if ok {
		extra = append(extra, rank)
	}
	if len(extra) > 0 {
		stat += "\n\n" + strings.Join(extra, "\n")
	}

	parts := []discord.Component{head, line(), discord.Text(stat)}
	if n > 0 {
		var top []store.PlayedGame
		for _, g := range bl.unplayed {
			if g.Platform == steamPlatform {
				top = append(top, store.PlayedGame{Game: g})
			}
			if len(top) == 10 {
				break
			}
		}
		if img, ok := b.shelf(render.Set, top, false); ok {
			parts = append(parts, img, note("The best-known games here that have never been launched"))
		}
	}
	parts = append(parts, note("From Steam playtime. Games played before Steam counted hours, or only offline, can show as never launched."))

	comps := []discord.Component{discord.Container(colorDust, parts...)}
	if pick, never, ok := backlogPick(bl, seed, roll); ok {
		meta := []string{}
		if pick.ReleaseYear > 0 {
			meta = append(meta, strconv.Itoa(pick.ReleaseYear))
		}
		if len(pick.Genres) > 0 {
			meta = append(meta, strings.Join(pick.Genres[:min(2, len(pick.Genres))], ", "))
		}
		if pick.Reviews > 0 {
			meta = append(meta, thousands(pick.Reviews)+" Steam reviews")
		}
		status := "never launched"
		if !never {
			status = "barely touched"
		}
		meta = append(meta, status)
		comps = append(comps, discord.Container(colorAccent,
			discord.Section(discord.Thumbnail(b.artURL(pick.Game, render.ArtCover), ""),
				"### 🎲 Tonight's pick\n**"+pick.Name+"**\n-# "+strings.Join(meta, " · ")),
		))
		row := []discord.Component{
			discord.Button(discord.ButtonPrimary, "🎲 Pick another", fmt.Sprintf("bl:r:%s:%d:%d", userID, seed, roll+1)),
			b.viewButton(pick.Game, "backlog", in.GuildID),
			discord.LinkButton("Steam store", steamStore(pick.GameID)),
		}
		if in.InGuild() {
			row = append(row, discord.Button(discord.ButtonSecondary, "Server piles", "lb:open:backlog"))
		}
		comps = append(comps, discord.ActionRow(row...))
	}
	return card(false, comps...), nil
}

// shameVerdict is a one-line judgement on the share never launched.
func shameVerdict(share float64) string {
	switch {
	case share >= 0.6:
		return "this isn't a backlog, it's an archive"
	case share >= 0.4:
		return "Steam sales have been very good to you"
	case share >= 0.2:
		return "a healthy pile, mostly"
	default:
		return "barely a pile, honestly"
	}
}

// backlogPick chooses tonight's game: the roll-th game in a shuffled order
// of the never-launched ones (preferring games with some reviews, to skip
// tools and test builds), or of the barely played ones if there are none.
// never reports which list it came from.
func backlogPick(bl backlog, seed uint32, roll int) (store.PlayedGame, bool, bool) {
	var pool []store.PlayedGame
	never := true
	var known []store.PlayedGame
	for _, g := range bl.unplayed {
		pool = append(pool, store.PlayedGame{Game: g})
		if g.Reviews >= 500 {
			known = append(known, store.PlayedGame{Game: g})
		}
	}
	if len(known) >= 3 {
		pool = known
	}
	if len(pool) == 0 {
		pool, never = bl.barely, false
	}
	if len(pool) == 0 {
		return store.PlayedGame{}, false, false
	}
	order := rand.New(rand.NewPCG(uint64(seed), 0x9e3779b97f4a7c15)).Perm(len(pool))
	return pool[order[roll%len(pool)]], never, true
}

// backlogRank places someone among this server's piles, if they're visible here.
func (b *Bot) backlogRank(ctx context.Context, guildID, userID string) (string, bool, error) {
	if guildID == "" {
		return "", false, nil
	}
	visible, err := b.Store.Visible(ctx, guildID, userID)
	if err != nil || !visible {
		return "", false, err
	}
	rows, err := b.Store.Leaderboard(ctx, guildID, steamPlatform, "backlog", 100)
	if err != nil || len(rows) < 2 {
		return "", false, err
	}
	for i, r := range rows {
		if r.DiscordID != userID {
			continue
		}
		if i == 0 {
			return fmt.Sprintf("🏆 **Biggest pile** in this server, out of %d", len(rows)), true, nil
		}
		return fmt.Sprintf("🏆 **#%d of %d** piles here · biggest: %s with %s", i+1, len(rows), mention(rows[0].DiscordID), thousands(rows[0].Value)), true, nil
	}
	return "", false, nil
}

// backlogComponent rerolls tonight's pick: bl:r:<user>:<seed>:<roll>. Only
// the pile's owner can reroll; anyone else gets their own pile.
func (b *Bot) backlogComponent(ctx context.Context, in *discord.Interaction, parts []string) discord.Response {
	if len(parts) != 5 || parts[1] != "r" {
		return reply("That button has expired. Run `/backlog` again.", true)
	}
	seed, err1 := strconv.ParseUint(parts[3], 10, 32)
	roll, err2 := strconv.Atoi(parts[4])
	if err1 != nil || err2 != nil || roll < 0 {
		return reply("That button has expired. Run `/backlog` again.", true)
	}
	clicker := in.Invoker()
	if parts[2] != clicker.ID {
		return message(notice(true, colorMuted, "That's not your pile",
			"This is "+mention(parts[2])+"'s pile of shame. Run `/backlog` to see yours and get your own pick."))
	}
	return b.backlogReply(ctx, in, clicker, true, uint32(seed), roll, true)
}
