package bot

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"playradar/internal/discord"
	"playradar/internal/store"
)

// Links builds every outbound URL. Links to player.gg go through this app's
// /go redirect, which counts the click and adds UTM tags.
type Links struct {
	Site     string // this app's public base URL
	PlayerGG string // https://player.gg
}

// Go wraps target in the click-counting redirect.
func (l Links) Go(target, command, guildID string) string {
	q := url.Values{}
	q.Set("u", target)
	q.Set("c", command)
	if guildID != "" {
		q.Set("g", guildID)
	}
	return l.Site + "/go?" + q.Encode()
}

// Game is a Steam game's page on player.gg. player.gg resolves
// /explore/games/<appid>.steam to its own game page.
func (l Links) Game(appID string) string { return l.PlayerGG + "/explore/games/" + appID + ".steam" }

// Playtests is player.gg's playtest listing.
func (l Links) Playtests() string { return l.PlayerGG + "/playtests?sort=new" }

// Join is player.gg's sign-up page.
func (l Links) Join() string { return l.PlayerGG + "/join" }

// gameButton links to a game's player.gg page.
func (b *Bot) gameButton(g store.Game, command, guildID string) discord.Component {
	return discord.LinkButton(discord.Truncate(g.Name, 50)+" on player.gg", b.Links.Go(b.Links.Game(g.GameID), command, guildID))
}

// viewButton is the short "View on player.gg" link for cards about one game.
func (b *Bot) viewButton(g store.Game, command, guildID string) discord.Component {
	return discord.LinkButton("View on player.gg", b.Links.Go(b.Links.Game(g.GameID), command, guildID))
}

// steamStore is a game's Steam store page.
func steamStore(appID string) string { return "https://store.steampowered.com/app/" + appID + "/" }

// coverURL is a Steam game's portrait library cover (600x900). Steam serves
// one for every app, generating it from the store art when needed.
func coverURL(appID string) string {
	return "https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/" + appID + "/library_600x900.jpg"
}

func mention(id string) string { return "<@" + id + ">" }

// hours formats minutes of playtime: "0.5 h", "6.5 h", "1,284 h".
func hours(minutes int) string {
	if minutes <= 0 {
		return "0 h"
	}
	h := float64(minutes) / 60
	if h < 10 {
		if h < 0.1 {
			h = 0.1
		}
		return strings.TrimSuffix(strconv.FormatFloat(h, 'f', 1, 64), ".0") + " h"
	}
	return thousands(int(h+0.5)) + " h"
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ago formats how long ago t was, for footers (which can't show Discord timestamps).
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "not yet"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%d %s ago", m, plural(m, "minute", "minutes"))
	case d < 48*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%d %s ago", h, plural(h, "hour", "hours"))
	default:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%d days ago", days)
	}
}

// rankLines formats ranked games: "🥇 **Deep Rock Galactic** · 412 h".
func rankLines(games []store.PlayedGame, recent bool) string {
	var lines []string
	for i, g := range games {
		m := g.MinutesTotal
		if recent {
			m = g.MinutesRecent
		}
		lines = append(lines, fmt.Sprintf("%s **%s** · %s", medal(i), g.Name, hours(m)))
	}
	return strings.Join(lines, "\n")
}

const privateLibraryHelp = "Steam is hiding this library, so there's nothing to show yet.\n" +
	"In Steam: **Profile → Edit Profile → Privacy Settings → Game details: Public**. " +
	"It syncs again within a few minutes."
