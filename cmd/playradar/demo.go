package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"playradar/internal/bot"
	"playradar/internal/config"
	"playradar/internal/discord"
)

// demo posts a walkthrough of every command to a channel: a short header on
// what each one does, then the card it makes, with dividers between them. It
// uses the preview's sample server (Alex, Mara and Theo).

type demoStep struct {
	card    string // preview card name
	header  string // markdown above the card; empty for a follow-up card
	caption string // small line above a follow-up card
}

type demoChapter struct {
	title, sub string
	steps      []demoStep
}

var demoScript = []demoChapter{
	{"Getting started", "Two commands to get going.", []demoStep{
		{card: "/help", header: "## /help\nEvery command on one card, grouped by what it's about."},
		{card: "/link", header: "## /link\nConnect Steam once and your games follow you into every server. If Steam is already connected to your Discord account, it takes one tap."},
	}},
	{"Your games", "Cards about you and your library.", []demoStep{
		{card: "/profile", header: "## /profile\nYour gaming profile on one card: total hours, your most played games and what you've been playing lately."},
		{card: "/four (first time: picker)", header: "## /four\nFour Favorites, like Letterboxd: the four games that made you. The first time, you pick them from your most played games."},
		{card: "/four", caption: "Once they're picked, the card shows who here shares your favorites and your closest match."},
		{card: "/verdict", header: "## /verdict\nThe court judges your gaming habits. You get an archetype and charges pulled from your stats, and your sentence is an upcoming playtest."},
		{card: "/verdict → Appeal", caption: "Only the accused can **Appeal**. It's always denied, but the court reads out your defense."},
		{card: "/iceberg", header: "## /iceberg\nYour most played games, sorted from the hits everyone owns down to the deep cuts by Steam review count. It also works out your gaming age and a depth score."},
		{card: "/collage", header: "## /collage\nYour top games as one image, all time or just the last two weeks."},
		{card: "/backlog", header: "## /backlog\nYour pile of shame: games you own but never launched, and how long they'd take to clear. It also picks one for tonight, and **Pick another** rerolls it."},
		{card: "/goty", header: "## /goty\nYour Game of the Year race: this year's releases you've played most. It links to player.gg to vote for your real winner."},
		{card: "/goty scope:server", caption: "Add `scope:server` for the whole server's race."},
		{card: "/collection", header: "## /collection\nCurated lists like Indie hall of fame or Road to GTA VI, with how much of each you've played."},
		{card: "/collection Indie hall of fame", caption: "Open one to see what's left to play and who here is furthest along."},
		{card: "/game", header: "## /game\nOne game on one card: your hours, how many people here own it, who holds the crown and how popular it is."},
	}},
	{"Your server", "Everyone who links shows up here. Each server has its own rankings.", []demoStep{
		{card: "/whoplays", header: "## /whoplays\nEveryone here who owns a game, ranked by hours. Handy for finding people to play with."},
		{card: "/leaderboard", header: "## /leaderboard\nThe server's top players. Tabs switch between hours, the last two weeks, games played, crowns and the biggest pile of shame."},
		{card: "/leaderboard → Crowns tab", caption: "After tapping **Crowns**."},
		{card: "/crowns", header: "## /crowns\nWhoever has the most hours in a game here holds its crown. This shows yours and who's close behind."},
		{card: "/taste", header: "## /taste\nHow your library compares with a friend's: a match score, the games you both play and your genre split."},
		{card: "/incommon", header: "## /incommon\nTag up to four friends to see the multiplayer games you all own, ranked by hours played together."},
		{card: "/chart", header: "## /chart\nThe server's most played games over the last two weeks. Admins can have it posted every Monday."},
	}},
	{"Playtests", "Get into games before they launch.", []demoStep{
		{card: "/playtests", header: "## /playtests\nLive and upcoming playtests on player.gg. **Request access** signs you up there."},
		{card: "/playtests → details", caption: "Each one opens to its details, including how many people here play similar games."},
		{card: "Radar welcome", header: "## /radar\nAdmins pick a channel and new playtests get posted there, flagged when they fit what the server plays.", caption: "When the radar is switched on:"},
		{card: "Radar post", caption: "When a new playtest goes live:"},
		{card: "Moment post", header: "## Moments\nA few times a year, a big gaming moment gets a quick poll in the radar channel, like GTA VI's launch or The Game Awards."},
	}},
	{"Privacy", "People decide what each server sees.", []demoStep{
		{card: "/visibility hide", header: "## /visibility\nShow or hide yourself in one server's leaderboards. Your account stays linked everywhere else."},
		{card: "/unlink", header: "## /unlink\nDeletes everything the app stores about you, after you confirm.", caption: "Buttons are off in this demo."},
	}},
}

func demo(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	channel := fs.String("channel", "", "post the walkthrough in this channel")
	you := fs.String("you", "100000000000000000", "Discord user ID to stand in for Alex, so Alex's buttons work for you")
	dry := fs.Bool("dry", false, "build and check every message without posting")
	fs.Parse(args)
	if *channel == "" && !*dry {
		fail("demo", fmt.Errorf("-channel is required (or -dry)"))
	}

	dir, err := os.MkdirTemp("", "playradar-demo")
	if err != nil {
		fail("temp dir", err)
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()
	cards, err := bot.Preview(ctx, dir, bot.PreviewFiles{Playtests: cfg.PlaytestsFile, Collections: cfg.CollectionsFile, Moments: cfg.MomentsFile, LinkSecret: cfg.LinkSecret}, cfg.BaseURL, *you)
	if err != nil {
		fail("render cards", err)
	}
	byName := map[string]discord.ResponseData{}
	for _, c := range cards {
		byName[c.Name] = c.Data
	}
	names := bot.PreviewPlayers(*you)
	msgs, err := demoMessages(cfg, byName, names)
	if err != nil {
		fail("build demo", err)
	}

	var rest *discord.Client
	if !*dry {
		if err := cfg.ValidateRegister(); err != nil {
			fail("config", err)
		}
		rest = discord.NewClient(cfg.DiscordAppID, cfg.DiscordBotToken)
		rest.UserAgent = fmt.Sprintf("DiscordBot (%s, 0.1)", cfg.BaseURL)
	}
	failed := 0
	for i, m := range msgs {
		status := "ok"
		if err := discord.CheckMessage(m.data); err != nil {
			status, failed = "INVALID: "+err.Error(), failed+1
		} else if rest != nil {
			if err := rest.CreateMessage(ctx, *channel, m.data); err != nil {
				status, failed = "DISCORD REJECTED: "+err.Error(), failed+1
			}
			time.Sleep(1200 * time.Millisecond) // stay under the channel rate limit
		}
		fmt.Printf("%2d %-32s %s\n", i+1, m.name, status)
	}
	if failed > 0 {
		os.Exit(1)
	}
}

type demoMessage struct {
	name string
	data discord.ResponseData
}

func demoMessages(cfg config.Config, cards map[string]discord.ResponseData, names map[string]string) ([]demoMessage, error) {
	const purple = 0x7C5CFF
	v2 := func(cs ...discord.Component) discord.ResponseData {
		return discord.ResponseData{Components: cs, Flags: discord.FlagComponentsV2}
	}
	var out []demoMessage
	out = append(out, demoMessage{"intro", v2(discord.Container(purple,
		discord.Gallery(discord.MediaItem{Media: discord.Media{URL: cfg.BaseURL + "/media/banner.png"}}),
		discord.Text("# Player.gg for Discord\nYour game library, your friends' stats and player.gg playtests, right inside your server. Here's every command, one at a time, with a note on what it does."),
		discord.Text("-# A demo server with three linked players: Alex, Mara and Theo. Their libraries are sample data."),
	))})

	for _, ch := range demoScript {
		out = append(out, demoMessage{"# " + ch.title, v2(
			discord.Separator(false, 2),
			discord.Container(purple, discord.Text("# "+ch.title+"\n"+ch.sub)),
		)})
		for i, st := range ch.steps {
			card, ok := cards[st.card]
			if !ok {
				return nil, fmt.Errorf("no card named %q", st.card)
			}
			if card.Flags&discord.FlagComponentsV2 == 0 {
				return nil, fmt.Errorf("card %q isn't a components v2 message", st.card)
			}
			var top []discord.Component
			if st.header != "" && i > 0 {
				top = append(top, discord.Separator(false, 2))
			}
			if st.header != "" {
				top = append(top, discord.Text(st.header))
			}
			if st.caption != "" {
				arrow := ""
				if st.header == "" {
					arrow = "↳ "
				}
				top = append(top, discord.Text("-# "+arrow+st.caption))
			}
			body := demoClean(card.Components, cfg.BaseURL, names)
			out = append(out, demoMessage{st.card, v2(append(top, body...)...)})
		}
	}

	install := "https://discord.com/oauth2/authorize?client_id=" + cfg.DiscordAppID
	out = append(out, demoMessage{"outro", v2(
		discord.Separator(false, 2),
		discord.Container(purple,
			discord.Text("## Try it\nAdd Player.gg to your server, then run `/link`. A player.gg account keeps your shelf, reviews and playtest invites in one place."),
			discord.ActionRow(
				discord.LinkButton("Add to Discord", install),
				discord.LinkButton("Create your player.gg account", "https://player.gg/join"),
			),
		),
	)})
	return out, nil
}

// demoClean swaps the sample players' mentions for their names (Discord shows
// unknown IDs as "@unknown-user"), and switches off buttons that shouldn't sit
// in a shared channel: sign-in links carry a link token, and /unlink deletes
// whoever taps it.
func demoClean(cs []discord.Component, site string, names map[string]string) []discord.Component {
	out := make([]discord.Component, len(cs))
	for i, c := range cs {
		if c.Content != "" {
			s := strings.ReplaceAll(c.Content, "> <@", ">, <@")
			for id, name := range names {
				s = strings.ReplaceAll(s, "<@"+id+">", name)
			}
			c.Content = s
		}
		if c.Type == discord.ComponentButton &&
			(strings.HasPrefix(c.URL, site+"/link") || strings.HasPrefix(c.CustomID, "unlink:")) {
			c.Disabled = true
		}
		if c.Accessory != nil {
			acc := demoClean([]discord.Component{*c.Accessory}, site, names)[0]
			c.Accessory = &acc
		}
		if len(c.Components) > 0 {
			c.Components = demoClean(c.Components, site, names)
		}
		out[i] = c
	}
	return out
}
