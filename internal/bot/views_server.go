package bot

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"playradar/internal/discord"
	"playradar/internal/store"
)

// crownMinutes is the least playtime that can hold a crown.
const crownMinutes = 60

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
	data, err := b.whoPlaysCard(ctx, in.GuildID, game)
	if err != nil {
		return b.oops(err, "who plays")
	}
	return message(data)
}

// whoPlaysReply answers the "Who plays" button on a game card, privately.
func (b *Bot) whoPlaysReply(ctx context.Context, in *discord.Interaction, gameID string, ephemeral bool) discord.Response {
	if !in.InGuild() {
		return reply("Who plays what only works inside servers.", true)
	}
	game, ok, err := b.Store.Game(ctx, steamPlatform, gameID)
	if err != nil {
		return b.oops(err, "load game")
	}
	if !ok {
		return reply("I don't know that game any more.", true)
	}
	data, err := b.whoPlaysCard(ctx, in.GuildID, game)
	if err != nil {
		return b.oops(err, "who plays")
	}
	if ephemeral {
		data.Flags |= discord.FlagEphemeral
	}
	return message(data)
}

func (b *Bot) whoPlaysCard(ctx context.Context, guildID string, game store.Game) (discord.ResponseData, error) {
	ranked, owners, err := b.Store.WhoPlays(ctx, guildID, steamPlatform, game.GameID, 15)
	if err != nil {
		return discord.ResponseData{}, err
	}
	members, err := b.Store.VisibleMembers(ctx, guildID, steamPlatform)
	if err != nil {
		return discord.ResponseData{}, err
	}
	var lines []string
	for i, r := range ranked {
		prefix := medal(i)
		if i == 0 && r.MinutesTotal > 0 {
			prefix = "👑"
		}
		l := fmt.Sprintf("%s %s · **%s**", prefix, mention(r.DiscordID), hours(r.MinutesTotal))
		if r.MinutesRecent > 0 {
			l += " · " + hours(r.MinutesRecent) + " lately"
		}
		lines = append(lines, l)
	}
	if more := owners - len(ranked); more > 0 {
		lines = append(lines, fmt.Sprintf("-# …and %d more", more))
	}
	if len(lines) == 0 {
		lines = append(lines, "Nobody who's visible here owns it yet.")
	}
	var parts []discord.Component
	if game.ImageURL != "" {
		parts = append(parts, discord.Gallery(discord.MediaItem{Media: discord.Media{URL: game.ImageURL}}))
	}
	parts = append(parts,
		discord.Text(fmt.Sprintf("## Who plays %s\n-# %d of %d linked %s %s it", game.Name,
			owners, members, plural(members, "member", "members"), plural(owners, "owns", "own"))),
		line(),
		discord.Text(strings.Join(lines, "\n")),
		line(),
		note("👑 most hours here · Not listed? `/link`, or `/visibility show`"),
	)
	return card(false,
		discord.Container(colorSoon, parts...),
		discord.ActionRow(b.viewButton(game, "whoplays", guildID), discord.Button(discord.ButtonSecondary, "Game details", "game:"+game.GameID)),
	), nil
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
	if len(common) == 0 {
		return notice(false, colorGroup, "What can you play together?", strings.Join(people, " ")+"\nYou don't share any Steam games yet.")
	}
	var multi, rest []store.CommonGame
	for _, g := range common {
		if g.Multiplayer != nil && *g.Multiplayer {
			multi = append(multi, g)
		} else {
			rest = append(rest, g)
		}
	}
	best := common[0].Game
	if len(multi) > 0 {
		best = multi[0].Game
	}

	var parts []discord.Component
	if best.ImageURL != "" {
		parts = append(parts, discord.Gallery(discord.MediaItem{Media: discord.Media{URL: best.ImageURL}}))
	}
	parts = append(parts,
		discord.Text(fmt.Sprintf("## What can you play together?\n-# %s · %d %s everyone owns",
			strings.Join(people, " "), len(common), plural(len(common), "game", "games"))),
		line(),
	)
	if len(multi) > 0 {
		var lines []string
		for i, g := range multi {
			if i == 8 {
				break
			}
			lines = append(lines, fmt.Sprintf("%s **%s** · %s together", medal(i), g.Name, hours(g.CombinedMinutes)))
		}
		parts = append(parts, discord.Text("### 🎮 Multiplayer\n"+strings.Join(lines, "\n")))
	}
	if len(rest) > 0 {
		var names []string
		for _, g := range rest {
			names = append(names, g.Name)
		}
		parts = append(parts, discord.Text("### Also in everyone's library\n"+joinMore(names, 8)))
	}
	parts = append(parts, line(), note("Multiplayer tags come from the Steam store"))
	return card(false,
		discord.Container(colorGroup, parts...),
		discord.ActionRow(b.gameButton(best, "incommon", guildID), discord.Button(discord.ButtonSecondary, "Game details", "game:"+best.GameID)),
	)
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
	var items []discord.MediaItem
	for i, c := range entries {
		lines = append(lines, fmt.Sprintf("%s **%s** · %s · %d %s", medal(i), c.Name, hours(c.Minutes), c.Players, plural(c.Players, "player", "players")))
		if c.ImageURL != "" && len(items) < 3 {
			items = append(items, discord.MediaItem{Media: discord.Media{URL: c.ImageURL}})
		}
	}
	foot := fmt.Sprintf("From %d linked %s", members, plural(members, "member", "members"))
	if footerNote != "" {
		foot += " · " + footerNote
	}
	parts := []discord.Component{
		heading("📈 Most played here", "Last 2 weeks", ""),
		line(),
		discord.Text(strings.Join(lines, "\n")),
	}
	if len(items) > 0 {
		parts = append(parts, discord.Gallery(items...))
	}
	parts = append(parts, note(foot))
	return card(false,
		discord.Container(colorAccent, parts...),
		discord.ActionRow(b.gameButton(entries[0].Game, command, guildID), discord.Button(discord.ButtonSecondary, "Server leaderboard", "lb:open:recent")),
	), true, nil
}

// ----- /crowns -----

func (b *Bot) cmdCrowns(ctx context.Context, in *discord.Interaction) discord.Response {
	if !in.InGuild() {
		return reply("`/crowns` works inside servers.", true)
	}
	target, self, stop := b.target(ctx, in, "player")
	if stop != nil {
		return *stop
	}
	acct, stop := b.account(ctx, target, self)
	if stop != nil {
		return *stop
	}
	if self {
		visible, err := b.Store.Visible(ctx, in.GuildID, target.ID)
		if err != nil {
			return b.oops(err, "load visibility")
		}
		if !visible {
			return message(notice(true, colorMuted, "You're hidden here", "Hidden members can't hold crowns. Turn it on with `/visibility show`."))
		}
	}
	crowns, total, err := b.Store.Crowns(ctx, in.GuildID, steamPlatform, target.ID, crownMinutes, 10)
	if err != nil {
		return b.oops(err, "crowns")
	}
	name := displayName(target, acct)
	body := "No crowns yet. A crown goes to whoever has the most hours in a game among people here."
	if total > 0 {
		var lines []string
		for _, c := range crowns {
			l := fmt.Sprintf("👑 **%s** · %s", c.Game.Name, hours(c.Minutes))
			if c.RunnerUpID != "" {
				l += fmt.Sprintf(" · next: %s %s", mention(c.RunnerUpID), hours(c.RunnerUpMinutes))
			}
			lines = append(lines, l)
		}
		body = strings.Join(lines, "\n")
	}
	parts := []discord.Component{
		heading(name+"'s crowns", fmt.Sprintf("%d %s in this server", total, plural(total, "crown", "crowns")), acct.AvatarURL),
		line(),
		discord.Text(body),
	}
	if total > len(crowns) {
		parts = append(parts, note(fmt.Sprintf("Showing the top %d by hours", len(crowns))))
	}
	parts = append(parts, line(), note("Most hours in a game here wins its crown (1 h minimum). Pass the holder to take it."))
	return message(card(false,
		discord.Container(colorGold, parts...),
		discord.ActionRow(discord.Button(discord.ButtonSecondary, "Crown leaderboard", "lb:open:crowns")),
	))
}

// ----- /leaderboard -----

type boardKind struct{ key, label, title string }

var boardKinds = []boardKind{
	{"hours", "Hours", "Most hours played"},
	{"recent", "Last 2 weeks", "Most played lately"},
	{"games", "Games played", "Most games played"},
	{"crowns", "Crowns", "Most crowns"},
	{"backlog", "Pile of shame", "Most games never launched"},
}

func findBoard(key string) boardKind {
	for _, k := range boardKinds {
		if k.key == key {
			return k
		}
	}
	return boardKinds[0]
}

func (b *Bot) cmdLeaderboard(ctx context.Context, in *discord.Interaction) discord.Response {
	if !in.InGuild() {
		return reply("`/leaderboard` works inside servers.", true)
	}
	data, err := b.leaderboardCard(ctx, in.GuildID, in.Invoker().ID, in.Data.StringOption("type"))
	if err != nil {
		return b.oops(err, "leaderboard")
	}
	return message(data)
}

// leaderboardComponent handles lb:set:<kind> (switch tabs on this message)
// and lb:open:<kind> (show a board privately).
func (b *Bot) leaderboardComponent(ctx context.Context, in *discord.Interaction, parts []string) discord.Response {
	if len(parts) != 3 || !in.InGuild() {
		return reply("That button has expired. Run `/leaderboard` again.", true)
	}
	data, err := b.leaderboardCard(ctx, in.GuildID, in.Invoker().ID, parts[2])
	if err != nil {
		return b.oops(err, "leaderboard")
	}
	if parts[1] == "open" {
		data.Flags |= discord.FlagEphemeral
		return message(data)
	}
	return update(data)
}

func (b *Bot) leaderboardCard(ctx context.Context, guildID, viewer, key string) (discord.ResponseData, error) {
	kind := findBoard(key)
	var rows []store.MemberValue
	var err error
	if kind.key == "crowns" {
		rows, err = b.Store.CrownBoard(ctx, guildID, steamPlatform, crownMinutes, 100)
	} else {
		rows, err = b.Store.Leaderboard(ctx, guildID, steamPlatform, kind.key, 100)
	}
	if err != nil {
		return discord.ResponseData{}, err
	}
	value := func(v int) string {
		switch kind.key {
		case "games":
			return thousands(v) + " " + plural(v, "game", "games")
		case "crowns":
			return thousands(v) + " 👑"
		case "backlog":
			return thousands(v) + " never launched"
		}
		return hours(v)
	}
	var lines []string
	for i, r := range rows {
		if i == 10 {
			break
		}
		lines = append(lines, fmt.Sprintf("%s %s · **%s**", medal(i), mention(r.DiscordID), value(r.Value)))
	}
	body := strings.Join(lines, "\n")
	if body == "" {
		body = "Nobody's on this board yet. Join with `/link`."
	}
	you := "Not on the board? `/link`, or `/visibility show`"
	for i, r := range rows {
		if r.DiscordID == viewer {
			you = fmt.Sprintf("You're **#%d** · %s", i+1, value(r.Value))
			break
		}
	}
	var tabs []discord.Component
	for _, k := range boardKinds {
		style := discord.ButtonSecondary
		if k.key == kind.key {
			style = discord.ButtonPrimary
		}
		tabs = append(tabs, discord.Button(style, k.label, "lb:set:"+k.key))
	}
	return card(false,
		discord.Container(colorAccent,
			heading("🏆 "+kind.title, fmt.Sprintf("%d %s ranked in this server", len(rows), plural(len(rows), "member", "members")), ""),
			line(),
			discord.Text(body),
			line(),
			note(you),
		),
		discord.ActionRow(tabs...),
	), nil
}

// ----- /taste -----

func (b *Bot) cmdTaste(ctx context.Context, in *discord.Interaction) discord.Response {
	if !in.InGuild() {
		return reply("`/taste` works inside servers.", true)
	}
	me := in.Invoker()
	if in.Data.StringOption("friend") == me.ID {
		return reply("Pick someone other than yourself.", true)
	}
	myAcct, stop := b.account(ctx, me, true)
	if stop != nil {
		return *stop
	}
	them, _, stop := b.target(ctx, in, "friend")
	if stop != nil {
		return *stop
	}
	theirAcct, stop := b.account(ctx, them, false)
	if stop != nil {
		return *stop
	}
	cmp, err := b.Store.Compare(ctx, steamPlatform, me.ID, them.ID, 5)
	if err != nil {
		return b.oops(err, "compare")
	}
	ga, err := b.Store.GenreMinutes(ctx, me.ID, steamPlatform, 0)
	if err != nil {
		return b.oops(err, "genres")
	}
	gb, err := b.Store.GenreMinutes(ctx, them.ID, steamPlatform, 0)
	if err != nil {
		return b.oops(err, "genres")
	}
	myName, theirName := displayName(me, myAcct), displayName(them, theirAcct)
	score := tasteScore(cmp, ga, gb)

	parts := []discord.Component{
		discord.Text(fmt.Sprintf("## %s × %s\n-# Steam taste match", myName, theirName)),
		discord.Text(fmt.Sprintf("# %d%% · %s", score, tasteVerdict(score))),
		line(),
		discord.Text(fmt.Sprintf("**%s** in common · **%s** only you · **%s** only %s",
			thousands(cmp.Shared), thousands(cmp.AOnly), thousands(cmp.BOnly), theirName)),
	}
	if len(cmp.Top) > 0 {
		var lines []string
		for _, g := range cmp.Top {
			lines = append(lines, fmt.Sprintf("▸ **%s** · %s vs %s", g.Game.Name, hours(g.AMinutes), hours(g.BMinutes)))
		}
		parts = append(parts, discord.Text("### You both play\n"+strings.Join(lines, "\n")))
	}
	if g := genreLines(ga, gb, 5); g != "" {
		parts = append(parts, discord.Text(fmt.Sprintf("### Genres · you vs %s\n%s", theirName, g)))
	} else {
		parts = append(parts, note("Genres appear once your games have been looked up on Steam."))
	}
	parts = append(parts, line(), note("Find something to play together with `/incommon`."))

	comps := []discord.Component{discord.Container(colorGroup, parts...)}
	if len(cmp.Top) > 0 {
		comps = append(comps, discord.ActionRow(b.gameButton(cmp.Top[0].Game, "taste", in.GuildID)))
	}
	return message(card(false, comps...))
}

// tasteScore blends library overlap with how similarly two people split their
// hours across genres, as a 0-100 match.
func tasteScore(c store.Comparison, a, b []store.GenreShare) int {
	union := c.Shared + c.AOnly + c.BOnly
	overlap := 0.0
	if union > 0 {
		// Libraries rarely overlap much, so a third in common already counts as full marks.
		overlap = math.Min(1, 3*float64(c.Shared)/float64(union))
	}
	cos, ok := genreCosine(a, b)
	if !ok {
		return int(overlap*100 + 0.5)
	}
	return int((0.5*overlap+0.5*cos)*100 + 0.5)
}

func genreCosine(a, b []store.GenreShare) (float64, bool) {
	if len(a) == 0 || len(b) == 0 {
		return 0, false
	}
	bm := map[string]float64{}
	var na, nb, dot float64
	for _, g := range b {
		bm[g.Genre] = float64(g.Minutes)
		nb += float64(g.Minutes) * float64(g.Minutes)
	}
	for _, g := range a {
		v := float64(g.Minutes)
		na += v * v
		dot += v * bm[g.Genre]
	}
	if na == 0 || nb == 0 {
		return 0, false
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), true
}

func tasteVerdict(score int) string {
	switch {
	case score >= 80:
		return "practically twins"
	case score >= 60:
		return "strong match"
	case score >= 40:
		return "some overlap"
	case score >= 20:
		return "different worlds"
	}
	return "total opposites"
}

// genreLines compares each person's share of hours in the genres they
// play most between them: "**Action** · 62% vs 41%".
func genreLines(a, b []store.GenreShare, n int) string {
	share := func(list []store.GenreShare) map[string]float64 {
		total := 0
		for _, g := range list {
			total += g.Minutes
		}
		m := map[string]float64{}
		for _, g := range list {
			if total > 0 {
				m[g.Genre] = float64(g.Minutes) / float64(total)
			}
		}
		return m
	}
	sa, sb := share(a), share(b)
	type row struct {
		genre    string
		combined float64
	}
	var rows []row
	seen := map[string]bool{}
	for _, m := range []map[string]float64{sa, sb} {
		for g := range m {
			if !seen[g] {
				seen[g] = true
				rows = append(rows, row{g, sa[g] + sb[g]})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].combined != rows[j].combined {
			return rows[i].combined > rows[j].combined
		}
		return rows[i].genre < rows[j].genre
	})
	var lines []string
	for i, r := range rows {
		if i == n {
			break
		}
		lines = append(lines, fmt.Sprintf("**%s** · %s vs %s", r.genre, pct(sa[r.genre]), pct(sb[r.genre])))
	}
	return strings.Join(lines, "\n")
}
