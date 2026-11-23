package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"playradar/internal/discord"
	"playradar/internal/playtests"
)

// playtestsPerPage keeps a page well inside Discord's 40-component limit.
const playtestsPerPage = 3

// ----- /playtests -----

func (b *Bot) cmdPlaytests(ctx context.Context, in *discord.Interaction) discord.Response {
	genre := safeID(strings.TrimSpace(in.Data.StringOption("genre")), 40)
	list, err := b.currentPlaytests(genre)
	if err != nil {
		return b.oops(err, "load playtests")
	}
	if len(list) == 0 {
		msg := "No playtests are listed here right now."
		if genre != "" {
			msg = fmt.Sprintf("No %s playtests are listed right now.", genre)
		}
		return message(notice(false, colorMuted, "Playtests", msg+" player.gg has the full list.",
			discord.ActionRow(b.allPlaytestsButton("playtests", in.GuildID))))
	}
	return message(b.playtestPage(list, 0, genre, in.GuildID))
}

// currentPlaytests loads live and upcoming playtests, filtered by genre.
func (b *Bot) currentPlaytests(genre string) ([]playtests.Playtest, error) {
	all, err := playtests.Load(b.PlaytestsFile)
	if err != nil {
		return nil, err
	}
	current := playtests.Current(all, b.now())
	genre = strings.ToLower(genre)
	if genre == "" {
		return current, nil
	}
	var out []playtests.Playtest
	for _, p := range current {
		for _, g := range p.Genres {
			if strings.Contains(strings.ToLower(g), genre) {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

func (b *Bot) allPlaytestsButton(command, guildID string) discord.Component {
	return discord.LinkButton("All playtests on player.gg", b.Links.Go(b.Links.Playtests(), command, guildID))
}

// status is a playtest's dates with a colored dot: "🟢 Live until Nov 30".
func (b *Bot) status(p playtests.Playtest) string {
	if p.Live(b.now()) {
		return "🟢 " + p.Status(b.now())
	}
	return "🟡 " + p.Status(b.now())
}

// playtestPage lists one page of playtests: each has its title and a
// "Request access" button, then its summary beside the cover image.
func (b *Bot) playtestPage(list []playtests.Playtest, page int, genre, guildID string) discord.ResponseData {
	pages := (len(list) + playtestsPerPage - 1) / playtestsPerPage
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	live := 0
	for _, p := range list {
		if p.Live(b.now()) {
			live++
		}
	}
	sub := fmt.Sprintf("%d live", live)
	if soon := len(list) - live; soon > 0 {
		sub += fmt.Sprintf(" · %d starting soon", soon)
	}
	title := "🧪 Playtests on player.gg"
	if genre != "" {
		title = "🧪 " + genre + " playtests"
	}
	parts := []discord.Component{heading(title, sub, ""), line()}

	start := page * playtestsPerPage
	end := min(start+playtestsPerPage, len(list))
	var options []discord.SelectOption
	for i := start; i < end; i++ {
		p := list[i]
		top := fmt.Sprintf("### %s\n-# %s", p.Title, b.status(p))
		if p.Studio != "" {
			top += " · " + p.Studio
		}
		parts = append(parts, discord.Section(discord.LinkButton("Request access", b.Links.Go(p.URL, "playtests", guildID)), top))
		summary := p.Summary
		if summary == "" {
			summary = strings.Join(p.Genres, " · ")
		}
		if summary != "" {
			if p.Image != "" {
				parts = append(parts, discord.Section(discord.Thumbnail(p.Image, ""), summary))
			} else {
				parts = append(parts, discord.Text(summary))
			}
		}
		if i < end-1 {
			parts = append(parts, line())
		}
		options = append(options, discord.SelectOption{
			Label: discord.Truncate(p.Title, 100), Value: strconv.Itoa(i), Description: discord.Truncate(p.Status(b.now()), 100),
		})
	}
	parts = append(parts, line(), note("Hand-picked from player.gg · tap **Request access** to sign up there"))

	comps := []discord.Component{
		discord.Container(colorLive, parts...),
		discord.ActionRow(discord.Select(fmt.Sprintf("pt:s:%d:%s", page, genre), "More about a playtest…", options...)),
	}
	nav := []discord.Component{}
	if pages > 1 {
		prev := discord.Button(discord.ButtonSecondary, "◀", fmt.Sprintf("pt:p:%d:%s", page-1, genre))
		prev.Disabled = page == 0
		next := discord.Button(discord.ButtonPrimary, "▶", fmt.Sprintf("pt:p:%d:%s", page+1, genre))
		next.Disabled = page == pages-1
		counter := discord.Button(discord.ButtonSecondary, fmt.Sprintf("%d / %d", page+1, pages), "pt:n")
		counter.Disabled = true
		nav = append(nav, prev, counter, next)
	}
	nav = append(nav, b.allPlaytestsButton("playtests", guildID))
	comps = append(comps, discord.ActionRow(nav...))
	return card(false, comps...)
}

// playtestDetail shows one playtest in full, with a Back button to its page.
func (b *Bot) playtestDetail(ctx context.Context, p playtests.Playtest, page int, genre, guildID string) discord.ResponseData {
	accent := colorSoon
	if p.Live(b.now()) {
		accent = colorLive
	}
	var parts []discord.Component
	if p.Image != "" {
		parts = append(parts, discord.Gallery(discord.MediaItem{Media: discord.Media{URL: p.Image}}))
	}
	head := "## " + p.Title
	if p.Studio != "" {
		head += "\n-# by " + p.Studio
	}
	parts = append(parts, discord.Text(head))
	if p.Summary != "" {
		parts = append(parts, discord.Text(p.Summary))
	}
	facts := []string{"**" + b.status(p) + "**"}
	if len(p.Platforms) > 0 {
		facts = append(facts, "**Platforms** "+strings.Join(p.Platforms, ", "))
	}
	if len(p.Genres) > 0 {
		facts = append(facts, "**Genres** "+strings.Join(p.Genres, " · "))
	}
	parts = append(parts, line(), discord.Text(strings.Join(facts, "\n")))
	if guildID != "" {
		if fit := b.fitLine(ctx, guildID, p); fit != "" {
			parts = append(parts, discord.Text("🎯 "+fit))
		}
	}
	return card(false,
		discord.Container(accent, parts...),
		discord.ActionRow(
			discord.Button(discord.ButtonSecondary, "◀ Back", fmt.Sprintf("pt:p:%d:%s", page, genre)),
			discord.LinkButton("Request access on player.gg", b.Links.Go(p.URL, "playtests", guildID)),
		),
	)
}

// fitLine says how many members here play games like this playtest, or "".
func (b *Bot) fitLine(ctx context.Context, guildID string, p playtests.Playtest) string {
	if len(p.SimilarSteamApps) == 0 {
		return ""
	}
	matches, err := b.Store.MembersOwningAny(ctx, guildID, steamPlatform, p.SimilarSteamApps)
	if err != nil {
		b.log().Warn("members owning similar games", "err", err)
		return ""
	}
	if matches == 0 {
		return ""
	}
	var names []string
	for _, id := range p.SimilarSteamApps {
		if g, ok, _ := b.Store.Game(ctx, steamPlatform, id); ok {
			names = append(names, g.Name)
		}
		if len(names) == 2 {
			break
		}
	}
	who := fmt.Sprintf("**%d %s here** %s", matches, plural(matches, "member", "members"), plural(matches, "plays", "play"))
	if len(names) == 0 {
		return who + " similar games"
	}
	return who + " games like " + strings.Join(names, " and ")
}

// playtestsComponent handles page buttons (pt:p:<page>:<genre>) and the
// details menu (pt:s:<page>:<genre>, value = index).
func (b *Bot) playtestsComponent(ctx context.Context, in *discord.Interaction, parts []string) discord.Response {
	if len(parts) < 3 {
		return reply("That button has expired. Run `/playtests` again.", true)
	}
	page, _ := strconv.Atoi(parts[2])
	genre := ""
	if len(parts) > 3 {
		genre = strings.Join(parts[3:], ":")
	}
	list, err := b.currentPlaytests(genre)
	if err != nil {
		return b.oops(err, "load playtests")
	}
	if len(list) == 0 {
		return update(notice(false, colorMuted, "Playtests", "These playtests have ended. player.gg has the full list.",
			discord.ActionRow(b.allPlaytestsButton("playtests", in.GuildID))))
	}
	switch parts[1] {
	case "p":
		return update(b.playtestPage(list, page, genre, in.GuildID))
	case "s":
		if len(in.Data.Values) == 1 {
			if i, err := strconv.Atoi(in.Data.Values[0]); err == nil && i >= 0 && i < len(list) {
				return update(b.playtestDetail(ctx, list[i], page, genre, in.GuildID))
			}
		}
		return update(b.playtestPage(list, page, genre, in.GuildID))
	}
	return reply("That button has expired. Run `/playtests` again.", true)
}
