package bot

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"playradar/internal/discord"
	"playradar/internal/playtests"
	"playradar/internal/store"
)

// ----- /verdict -----
//
// A roast in the style of fmbot's .judge, built from real stats rather than
// AI: an archetype, three lines of evidence and a "sentence" that points at a
// player.gg playtest. The accused can appeal, which flips the card to the
// case for the defense.

const (
	colorVerdict = 0xE5484D
	verdictGames = 5000 // every played game, for backlog and variety counts
)

// archetype is a label plus the punchline used when it leads the verdict.
type archetype struct {
	emoji, title, roast, defense string
}

// gameArchetypes are for people whose playtime is dominated by one game.
var gameArchetypes = map[string]archetype{
	"427520":  {"🏭", "The Factory Foreman", "The factory must grow. You, less so.", "You built a working economy. Most people can't build a shelf."},
	"730":     {"🔫", "The Eco-Round Philosopher", "And you still blame the tickrate.", "Crosshair placement most people only dream of."},
	"570":     {"🛡️", "The Ancient Defender", "Somewhere a support player is still waiting for you to buy wards.", "You learned a game most people quit in a week."},
	"440":     {"🎩", "The Hat Collector", "The hats are worth more than the hours. Barely.", "You stayed loyal to a classic."},
	"413150":  {"🌱", "The Retired Farmer", "Pierre has never had a more loyal customer.", "The valley has never been better looked after."},
	"105600":  {"🌳", "The Underground Architect", "You've dug further than most mining companies.", "You've beaten bosses most people never find."},
	"252490":  {"🪨", "The Rock Enthusiast", "Every one of those hours started with a rock.", "You survived wipe after wipe."},
	"1172470": {"🪂", "The Hot-Drop Regular", "Hot drop, early death, queue again. A lifestyle.", "You keep dropping in. That's grit."},
	"1086940": {"🎲", "The Save-Scummer", "That's a lot of reloading for one dice roll.", "You saw every ending. Probably."},
	"1245620": {"🗡️", "The Tarnished", "That's a lot of hours dying to the same boss.", "You didn't give up. The bosses did."},
	"289070":  {"🏛️", "The One-More-Turn Diplomat", "It was only ever going to be one more turn.", "Empires rise when you're around."},
	"8930":    {"🏛️", "The One-More-Turn Diplomat", "It was only ever going to be one more turn.", "Empires rise when you're around."},
	"227300":  {"🚚", "The Long-Haul Trucker", "You've driven further across Europe than most Europeans.", "Deliveries on time, every time."},
	"578080":  {"🍳", "The Pan Carrier", "The pan has saved you more often than your aim has.", "You've survived a lot of circles."},
	"271590":  {"🚗", "The Los Santos Local", "Still waiting on GTA VI for PC, then.", "You know every shortcut in Los Santos."},
	"230410":  {"🥷", "The Space Ninja", "The grind is the game. You knew that going in.", "You've earned every frame."},
	"238960":  {"🌳", "The Passive Tree Surgeon", "You've read more patch notes than books.", "You understand a skill tree nobody else can."},
	"292030":  {"🐺", "The Gwent Addict", "Most of those hours were Gwent. We both know it.", "You did every contract on the board."},
	"359550":  {"🧱", "The Wall Breacher", "You've spent more time droning than shooting.", "Map knowledge most people never get."},
	"548430":  {"⛏️", "The Dwarf", "Rock and stone, but mostly the rock.", "You never leave a dwarf behind."},
	"1145360": {"🔥", "The Escape Artist", "You've died in the underworld more often than anyone in Greek myth.", "Run after run, you got better."},
	"1145350": {"🔥", "The Escape Artist", "You've died in the underworld more often than anyone in Greek myth.", "Run after run, you got better."},
	"294100":  {"🧪", "The Colony Warden", "Your colonists have been through things. You did those things.", "Your colonies survive things they shouldn't."},
	"255710":  {"🏙️", "The Traffic Engineer", "And the traffic is still bad.", "Your cities run better than most real ones."},
	"252950":  {"⚽", "The Ceiling Shot Hopeful", "What a save! What a save! What a save!", "Mechanics most people can't even see."},
	"4000":    {"🔧", "The Sandbox Physicist", "You've spent more time in servers than in school.", "You've seen every game mode there is."},
	"945360":  {"🔪", "The Sus One", "Statistically, it was you.", "You read a room better than anyone."},
}

// genreArchetypes are for people whose playtime leans hard into one genre.
var genreArchetypes = map[string]archetype{
	"strategy":              {"♟️", "The Armchair General", "You've conquered more countries than you've visited.", "You think three moves ahead. In games, at least."},
	"rpg":                   {"📜", "The Side-Quest Completionist", "The main quest can wait. It has been waiting for years.", "You finish the side quests. The villagers thank you."},
	"simulation":            {"📊", "The Spreadsheet Enjoyer", "Your idea of fun has a supply chain.", "You have patience most people don't."},
	"action":                {"💥", "The Adrenaline Junkie", "Tutorials are for other people.", "Reflexes. Actual reflexes."},
	"adventure":             {"🧭", "The Explorer", "You've walked further in games than outside.", "You came for the world, not the leaderboard."},
	"indie":                 {"🎨", "The Indie Darling", "You've said \"it's like X meets Y\" at least once this week.", "You back small studios. Good."},
	"casual":                {"☕", "The Cozy Gamer", "Your most stressful moment this year was a fishing minigame.", "Gaming as self-care. Correct."},
	"sports":                {"🏟️", "The Couch Coach", "You've managed more teams than you've played on.", "Competitive spirit, zero knee injuries."},
	"racing":                {"🏎️", "The Petrolhead", "You brake for corners. In games, at least.", "Fast hands, faster laps."},
	"massively multiplayer": {"🐉", "The Raid Leader", "Your guild has seen more of you than your family has.", "People count on you every reset."},
	"early access":          {"🧪", "The Beta Tester", "You play games before they're finished, then complain they aren't finished.", "You play games before they're done. Studios need people like you."},
	"free to play":          {"🆓", "The Free-to-Play Purist", "Free to play. The skins weren't.", "Maximum fun, minimum spend. Smart."},
}

// commonGenres are on so many games that leaning into them says less.
var commonGenres = map[string]bool{"action": true, "indie": true, "free to play": true}

// evidence is everything the verdict is built from.
type evidence struct {
	owned, minutes, recent int
	played                 []store.PlayedGame // minutes > 0, most played first
	genres                 []store.GenreShare
	year                   int // the current year
	crowns                 int // -1 when not in a server, or too few members to count
	crownGame              string
}

func (b *Bot) evidence(ctx context.Context, userID, guildID string) (evidence, error) {
	ev := evidence{year: b.now().Year(), crowns: -1}
	stats, err := b.Store.LibraryStats(ctx, userID, steamPlatform)
	if err != nil {
		return ev, err
	}
	ev.owned, ev.minutes, ev.recent = stats.Games, stats.Minutes, stats.RecentMinutes
	if ev.played, err = b.Store.TopGames(ctx, userID, steamPlatform, false, verdictGames); err != nil {
		return ev, err
	}
	if ev.genres, err = b.Store.GenreMinutes(ctx, userID, steamPlatform, 0); err != nil {
		return ev, err
	}
	if guildID == "" {
		return ev, nil
	}
	visible, err := b.Store.Visible(ctx, guildID, userID)
	if err != nil || !visible {
		return ev, err
	}
	members, err := b.Store.VisibleMembers(ctx, guildID, steamPlatform)
	if err != nil || members < 3 {
		return ev, err
	}
	crowns, total, err := b.Store.Crowns(ctx, guildID, steamPlatform, userID, crownMinutes, 1)
	if err != nil {
		return ev, err
	}
	ev.crowns = total
	if len(crowns) > 0 {
		ev.crownGame = crowns[0].Game.Name
	}
	return ev, nil
}

// fact is one line of evidence, phrased for the prosecution and the defense.
// Either phrasing may be empty. Higher weight means funnier or more telling.
type fact struct {
	key, roast, defense string
	weight              float64
}

// The archetype and its lead fact are chosen together.
type ruling struct {
	arch archetype
	lead string // key of the fact that leads
}

func (ev evidence) facts() map[string]fact {
	out := map[string]fact{}
	add := func(f fact) { out[f.key] = f }
	h := ev.minutes / 60

	// The top game's share of all playtime.
	if len(ev.played) > 0 && ev.minutes > 0 {
		top := ev.played[0]
		share := float64(top.MinutesTotal) / float64(ev.minutes)
		f := fact{key: "top", weight: share * 100}
		if a, ok := gameArchetypes[top.GameID]; ok && share >= 0.35 {
			f.roast = fmt.Sprintf("**%s** of your playtime is **%s**. %s", pct(share), top.Name, a.roast)
			f.defense = fmt.Sprintf("**%s** in **%s**. %s", hours(top.MinutesTotal), top.Name, a.defense)
		} else if share >= 0.3 {
			f.roast = fmt.Sprintf("**%s** of your hours are **%s**. Other games exist, we checked.", pct(share), top.Name)
			f.defense = fmt.Sprintf("**%s** of your hours in **%s**. You know what you love.", pct(share), top.Name)
		} else if len(ev.played) >= 5 {
			f.roast = fmt.Sprintf("No game has more than **%s** of your hours. Commitment issues.", pct(share))
			f.defense = fmt.Sprintf("No game has more than **%s** of your hours. Range.", pct(share))
			f.weight = 25
		}
		add(f)
	}

	// Total hours.
	switch {
	case h >= 2000:
		add(fact{key: "hours", weight: 70,
			roast:   fmt.Sprintf("**%s** on Steam. That's %s days, or %.1f years of a full-time job.", hours(ev.minutes), thousands(h/24), float64(h)/1800),
			defense: fmt.Sprintf("**%s** on Steam. Others call it a hobby; this court calls it a career.", hours(ev.minutes))})
	case h >= 500:
		add(fact{key: "hours", weight: 45,
			roast:   fmt.Sprintf("**%s** on Steam. That's %d full days. We're not judging. We are, actually.", hours(ev.minutes), h/24),
			defense: fmt.Sprintf("**%s** on Steam. Others call it a hobby; this court calls it a career.", hours(ev.minutes))})
	case h < 50 && ev.minutes > 0:
		add(fact{key: "hours", weight: 60,
			roast:   fmt.Sprintf("**%s** on Steam, in total. Are you sure you're in the right server?", hours(ev.minutes)),
			defense: fmt.Sprintf("Only **%s** on Steam. A life outside games. Enviable.", hours(ev.minutes))})
	}

	// The backlog: games owned but never launched.
	if ev.owned >= 10 {
		unplayed := ev.owned - len(ev.played)
		share := float64(unplayed) / float64(ev.owned)
		switch {
		case ev.owned >= 20 && share >= 0.4:
			add(fact{key: "backlog", weight: 50 + share*40,
				roast:   fmt.Sprintf("You own **%s games** and have never launched **%s** of them. That's a museum, not a library.", thousands(ev.owned), thousands(unplayed)),
				defense: fmt.Sprintf("**%s games** still waiting for you. Optimism isn't a crime.", thousands(unplayed))})
		case share <= 0.15:
			add(fact{key: "backlog", weight: 45,
				roast:   fmt.Sprintf("You've launched **%s** of your library. Steam sales must hate you.", pct(1-share)),
				defense: fmt.Sprintf("You've launched **%s** of what you own. You actually play what you buy.", pct(1-share))})
		}
	} else if ev.owned > 0 {
		add(fact{key: "small", weight: 55,
			roast:   fmt.Sprintf("**%d %s** in your library. Minimalist, or very loyal to free weekends.", ev.owned, plural(ev.owned, "game", "games")),
			defense: fmt.Sprintf("**%d %s**, chosen carefully. Quality over quantity.", ev.owned, plural(ev.owned, "game", "games"))})
	}

	// The famous game bought and dropped within an hour and a half.
	var dropped *store.PlayedGame
	for i, g := range ev.played {
		if !g.DetailsAt.IsZero() && g.Reviews >= 20000 && g.MinutesTotal < 90 && (dropped == nil || g.Reviews > dropped.Reviews) {
			dropped = &ev.played[i]
		}
	}
	if dropped != nil {
		add(fact{key: "dropped", weight: 80,
			roast: fmt.Sprintf("You bought **%s**, played **%d %s**, and never went back.", dropped.Name, dropped.MinutesTotal, plural(dropped.MinutesTotal, "minute", "minutes"))})
	}

	// The last two weeks.
	switch {
	case ev.recent >= 50*60:
		add(fact{key: "recent", weight: 65,
			roast:   fmt.Sprintf("**%s** in the last two weeks. That's %.1f hours a day. Your chair knows your shape.", hours(ev.recent), float64(ev.recent)/60/14),
			defense: fmt.Sprintf("**%s** in the last two weeks. Still very much in the game.", hours(ev.recent))})
	case ev.recent >= 10*60:
		add(fact{key: "recent", weight: 35,
			defense: fmt.Sprintf("**%s** this fortnight. Still in the game.", hours(ev.recent))})
	case ev.recent == 0 && ev.minutes >= 100*60:
		add(fact{key: "recent", weight: 55,
			roast:   "Nothing played in the last two weeks. Touching grass, or waiting for a sale?",
			defense: "Nothing played in the last two weeks. Balance. The court is impressed."})
	}

	// How deep the cuts go (the /iceberg score) and the gaming age.
	top := ev.played
	if len(top) > icebergGames {
		top = top[:icebergGames]
	}
	if depth, ok := depthScore(top); ok {
		if depth >= 50 {
			add(fact{key: "depth", weight: 30 + float64(depth)/2,
				roast:   fmt.Sprintf("Depth score **%d**/100. You'd recommend your favorites, but nobody's heard of them.", depth),
				defense: fmt.Sprintf("Depth score **%d**/100. You find the good stuff before everyone else.", depth)})
		} else if depth <= 10 {
			if g, ok := mostObscure(top); ok && g.Reviews >= 20000 {
				add(fact{key: "depth", weight: 55,
					roast:   fmt.Sprintf("Your most obscure game is **%s**, with %s Steam reviews. Bold of you to call anything a hidden gem.", g.Name, thousands(g.Reviews)),
					defense: "You play what everyone plays, so there's always someone to play with."})
			}
		}
	}
	if age, ok := gamingAge(top); ok {
		diff := ev.year - age
		switch {
		case diff >= 10:
			old := "older than some people in this server"
			switch {
			case diff >= 18:
				old = "old enough to vote"
			case diff >= 16:
				old = "old enough to drive"
			case diff >= 13:
				old = "old enough for their own social media account"
			}
			add(fact{key: "age", weight: 40 + float64(diff),
				roast:   fmt.Sprintf("Your gaming age is **%d**. Your games are %s.", age, old),
				defense: fmt.Sprintf("Your gaming age is **%d**. You know a classic when you see one.", age)})
		case diff <= 1:
			add(fact{key: "age", weight: 50,
				roast:   fmt.Sprintf("Your gaming age is **%d**. If it isn't on the front page, you haven't heard of it.", age),
				defense: fmt.Sprintf("Your gaming age is **%d**. Always on what's next.", age)})
		}
	}

	// Online with others, or alone.
	if share, ok := multiplayerShare(ev.played); ok {
		switch {
		case share >= 0.75:
			add(fact{key: "mp", weight: 30 + share*20,
				roast:   fmt.Sprintf("**%s** of your hours are in multiplayer games. Your mic has heard things.", pct(share)),
				defense: fmt.Sprintf("**%s** of your hours are in multiplayer games. Good teammate energy.", pct(share))})
		case share <= 0.1:
			add(fact{key: "mp", weight: 40,
				roast:   fmt.Sprintf("**%s** of your hours are in single-player games. Co-op invites come here to die.", pct(1-share)),
				defense: fmt.Sprintf("**%s** of your hours are in single-player games. You came for the story.", pct(1-share))})
		}
	}

	// The genre the hours lean towards.
	if g, share, ok := ev.leaningGenre(); ok {
		a := genreArchetypes[strings.ToLower(g)]
		add(fact{key: "genre", weight: 35 + share*40,
			roast:   fmt.Sprintf("**%s** of your hours are in %s games. %s", pct(share), g, a.roast),
			defense: fmt.Sprintf("**%s** of your hours are in %s games. %s", pct(share), g, a.defense)})
	}

	// Server crowns.
	switch {
	case ev.crowns == 0:
		add(fact{key: "crowns", weight: 60,
			roast: "No crowns in this server. Whatever you play, someone here plays it more."})
	case ev.crowns >= 10:
		add(fact{key: "crowns", weight: 60,
			roast:   fmt.Sprintf("You hold **%d crowns** in this server. Nobody asked you to.", ev.crowns),
			defense: fmt.Sprintf("You hold **%d crowns** in this server. Nobody here plays **%s** more than you.", ev.crowns, ev.crownGame)})
	case ev.crowns > 0:
		add(fact{key: "crowns", weight: 50,
			defense: fmt.Sprintf("You hold **%d %s** in this server. Nobody here plays **%s** more than you.", ev.crowns, plural(ev.crowns, "crown", "crowns"), ev.crownGame)})
	}

	// Variety.
	tried := 0
	for _, g := range ev.played {
		if g.MinutesTotal >= 60 {
			tried++
		}
	}
	if tried >= 100 {
		add(fact{key: "variety", weight: 45,
			roast:   fmt.Sprintf("You've put over an hour into **%d games**. Commitment issues, but make it gaming.", tried),
			defense: fmt.Sprintf("Over an hour in **%d different games**. Range.", tried)})
	}
	return out
}

// leaningGenre is the genre with the biggest share of playtime, discounting
// genres nearly every game has. ok is false without a clear lean.
func (ev evidence) leaningGenre() (string, float64, bool) {
	known := 0
	for _, g := range ev.played {
		if len(g.Genres) > 0 {
			known += g.MinutesTotal
		}
	}
	if known == 0 || known < ev.minutes/2 {
		return "", 0, false
	}
	best, bestShare, bestScore := "", 0.0, 0.0
	for _, g := range ev.genres {
		key := strings.ToLower(g.Genre)
		if _, ok := genreArchetypes[key]; !ok {
			continue
		}
		share := float64(g.Minutes) / float64(known)
		score := share
		if commonGenres[key] {
			score -= 0.25
		}
		if score > bestScore {
			best, bestShare, bestScore = g.Genre, share, score
		}
	}
	return best, bestShare, bestScore >= 0.35
}

func (ev evidence) ruling(facts map[string]fact) ruling {
	if len(ev.played) > 0 && ev.minutes > 0 {
		top := ev.played[0]
		share := float64(top.MinutesTotal) / float64(ev.minutes)
		if a, ok := gameArchetypes[top.GameID]; ok && share >= 0.35 {
			return ruling{a, "top"}
		}
		if share >= 0.5 {
			return ruling{archetype{"💍", "The Monogamist", "", ""}, "top"}
		}
	}
	if f, ok := facts["backlog"]; ok && f.weight >= 72 && ev.owned >= 40 {
		return ruling{archetype{"🛒", "The Sale Hoarder", "", ""}, "backlog"}
	}
	if f, ok := facts["depth"]; ok && f.weight >= 57.5 && !strings.Contains(f.defense, "everyone plays") {
		return ruling{archetype{"🔦", "The Crate Digger", "", ""}, "depth"}
	}
	if f, ok := facts["age"]; ok {
		if strings.Contains(f.defense, "classic") && f.weight >= 52 {
			return ruling{archetype{"📼", "The Time Traveller", "", ""}, "age"}
		}
		if strings.Contains(f.defense, "next") {
			return ruling{archetype{"🆕", "The Day-One Player", "", ""}, "age"}
		}
	}
	if g, _, ok := ev.leaningGenre(); ok {
		return ruling{genreArchetypes[strings.ToLower(g)], "genre"}
	}
	if f, ok := facts["mp"]; ok {
		if strings.Contains(f.roast, "mic") {
			return ruling{archetype{"🎧", "The Lobby Regular", "", ""}, "mp"}
		}
		return ruling{archetype{"🐺", "The Lone Wolf", "", ""}, "mp"}
	}
	return ruling{archetype{"🎮", "The Generalist", "", ""}, "top"}
}

// lines picks the lead fact and the two strongest others, for one side.
func lines(facts map[string]fact, lead string, defense bool) []string {
	text := func(f fact) string {
		if defense {
			return f.defense
		}
		return f.roast
	}
	var out []string
	if f, ok := facts[lead]; ok && text(f) != "" {
		out = append(out, text(f))
	}
	var rest []fact
	for k, f := range facts {
		if k != lead && text(f) != "" {
			rest = append(rest, f)
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].weight != rest[j].weight {
			return rest[i].weight > rest[j].weight
		}
		return rest[i].key < rest[j].key
	})
	for _, f := range rest {
		if len(out) == 3 {
			break
		}
		out = append(out, text(f))
	}
	return out
}

// depthScore is the /iceberg depth score (0-100, hours-weighted).
func depthScore(games []store.PlayedGame) (int, bool) {
	var depth, minutes float64
	for _, g := range games {
		if g.DetailsAt.IsZero() {
			continue
		}
		depth += float64(tierOf(g.Reviews)) * float64(g.MinutesTotal)
		minutes += float64(g.MinutesTotal)
	}
	if minutes == 0 {
		return 0, false
	}
	return int(depth/minutes/float64(len(icebergTiers)-1)*100 + 0.5), true
}

// mostObscure is the least-reviewed game someone has put two hours into.
func mostObscure(games []store.PlayedGame) (store.PlayedGame, bool) {
	var best store.PlayedGame
	found := false
	for _, g := range games {
		if g.DetailsAt.IsZero() || g.MinutesTotal < 120 {
			continue
		}
		if !found || g.Reviews < best.Reviews {
			best, found = g, true
		}
	}
	return best, found
}

// multiplayerShare is the share of known playtime in multiplayer games.
func multiplayerShare(games []store.PlayedGame) (float64, bool) {
	var mp, known, all int
	for _, g := range games {
		all += g.MinutesTotal
		if g.Multiplayer == nil {
			continue
		}
		known += g.MinutesTotal
		if *g.Multiplayer {
			mp += g.MinutesTotal
		}
	}
	if known == 0 || known < all/2 {
		return 0, false
	}
	return float64(mp) / float64(known), true
}

// pick chooses one of options, the same way for the same seed.
func pick(seed string, options ...string) string {
	h := fnv.New32a()
	h.Write([]byte(seed))
	return options[int(h.Sum32()%uint32(len(options)))]
}

func (b *Bot) cmdVerdict(ctx context.Context, in *discord.Interaction) discord.Response {
	target, self, stop := b.target(ctx, in, "player")
	if stop != nil {
		return *stop
	}
	return b.verdictReply(ctx, in, target, self, false, false)
}

// verdictReply answers with a verdict (or the appeal), as a new message or by
// updating the one a button was on.
func (b *Bot) verdictReply(ctx context.Context, in *discord.Interaction, target discord.User, self, defense, edit bool) discord.Response {
	acct, stop := b.account(ctx, target, self)
	if stop != nil {
		return *stop
	}
	name := displayName(target, acct)
	if acct.LibraryPrivate {
		return message(notice(true, colorMuted, "Nothing to judge yet", privateLibraryHelp))
	}
	ev, err := b.evidence(ctx, target.ID, in.GuildID)
	if err != nil {
		return b.oops(err, "verdict evidence")
	}
	if len(ev.played) == 0 {
		return message(notice(true, colorMuted, "Nothing to judge yet",
			"There's no playtime on this Steam library yet. If you just linked, give it a minute to sync."))
	}
	data := b.verdictCard(name, acct.AvatarURL, target.ID, in.GuildID, ev, defense)
	if edit {
		return update(data)
	}
	return message(data)
}

func (b *Bot) verdictCard(name, avatar, subjectID, guildID string, ev evidence, defense bool) discord.ResponseData {
	facts := ev.facts()
	r := ev.ruling(facts)
	evidenceLine := fmt.Sprintf("Judged on %s across %s %s", hours(ev.minutes), thousands(len(ev.played)), plural(len(ev.played), "game", "games"))

	var title, sub, rulingText string
	accent := colorVerdict
	if defense {
		accent = colorLive
		title = "🧑‍⚖️ " + name + " appeals"
		sub = pick(subjectID+b.now().Format("2006-01-02"),
			"Appeal granted. In your defense:", "Appeal partly granted. In your defense:", "Appeal denied. Still, in your defense:")
		rulingText = "### " + r.arch.emoji + " " + r.arch.title + ", with honors"
	} else {
		title = "⚖️ The verdict on " + name
		sub = evidenceLine
		rulingText = "### " + r.arch.emoji + " " + r.arch.title
	}
	var body []string
	for _, l := range lines(facts, r.lead, defense) {
		body = append(body, "▸ "+l)
	}

	sentence, sentenceURL := b.sentence(ev, guildID)
	parts := []discord.Component{
		heading(title, sub, avatar),
		line(),
		discord.Text(rulingText),
		discord.Text(strings.Join(body, "\n")),
		line(),
		discord.Text(sentence),
	}
	if !defense {
		parts = append(parts, note("Only the accused can appeal."))
	}
	row := []discord.Component{discord.LinkButton("Serve your sentence", sentenceURL)}
	if defense {
		row = append(row, discord.Button(discord.ButtonSecondary, "Back to the verdict", "vd:v:"+subjectID))
	} else {
		row = append(row, discord.Button(discord.ButtonSecondary, "Appeal", "vd:a:"+subjectID))
	}
	row = append(row, discord.Button(discord.ButtonPrimary, "Judge me", "vd:me"))
	return card(false, discord.Container(accent, parts...), discord.ActionRow(row...))
}

// sentence points at a playtest that fits the accused's genres, or at
// player.gg itself when none are running.
func (b *Bot) sentence(ev evidence, guildID string) (string, string) {
	list, err := b.currentPlaytests("")
	if err != nil {
		b.log().Warn("verdict playtests", "err", err)
	}
	if p, ok := b.bestPlaytest(list, ev.genres); ok {
		return fmt.Sprintf("**Sentence:** play **%s** before it comes out. Request access on player.gg.", p.Title),
			b.Links.Go(p.URL, "verdict", guildID)
	}
	return "**Sentence:** put your shelf on player.gg and let everyone see the evidence.",
		b.Links.Go(b.Links.Join(), "verdict", guildID)
}

// bestPlaytest prefers live playtests, then ones sharing the most playtime
// genres.
func (b *Bot) bestPlaytest(list []playtests.Playtest, genres []store.GenreShare) (playtests.Playtest, bool) {
	weight := map[string]int{}
	for _, g := range genres {
		weight[strings.ToLower(g.Genre)] = g.Minutes
	}
	best, bestScore, found := playtests.Playtest{}, -1, false
	for _, p := range list {
		score := 0
		if p.Live(b.now()) {
			score += 1 << 30
		}
		for _, g := range p.Genres {
			score += min(weight[strings.ToLower(g)], 1<<20)
		}
		if score > bestScore {
			best, bestScore, found = p, score, true
		}
	}
	return best, found
}

// verdictComponent handles Appeal, Back to the verdict and Judge me.
func (b *Bot) verdictComponent(ctx context.Context, in *discord.Interaction, parts []string) discord.Response {
	clicker := in.Invoker()
	if len(parts) == 2 && parts[1] == "me" {
		return b.verdictReply(ctx, in, clicker, true, false, false)
	}
	if len(parts) != 3 {
		return reply("That button has expired. Run `/verdict` again.", true)
	}
	if parts[2] != clicker.ID {
		return message(notice(true, colorMuted, "Only the accused can appeal",
			"This is "+mention(parts[2])+"'s verdict. Want your own? Press **Judge me**."))
	}
	return b.verdictReply(ctx, in, clicker, true, parts[1] == "a", true)
}
