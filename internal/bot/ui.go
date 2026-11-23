package bot

import (
	"fmt"
	"strings"

	"playradar/internal/discord"
)

// Card accent colors.
const (
	colorAccent = 0x7C5CFF // brand purple: profiles, charts, help
	colorLive   = 0x23A55A // playtest running now
	colorSoon   = 0xF0B232 // playtest starting soon, who-plays
	colorGroup  = 0x3FA9FF // friends: incommon, taste
	colorGold   = 0xF5C542 // crowns
	colorMuted  = 0x4E5058 // notices, empty states
)

// card is a Components V2 message: everything is laid out with components.
func card(ephemeral bool, comps ...discord.Component) discord.ResponseData {
	flags := discord.FlagComponentsV2
	if ephemeral {
		flags |= discord.FlagEphemeral
	}
	return discord.ResponseData{Components: comps, Flags: flags, AllowedMentions: discord.NoPings()}
}

// heading is a card title with a small grey subtitle, and a thumbnail on the
// right when thumb is set.
func heading(title, sub, thumb string) discord.Component {
	md := "## " + title
	if sub != "" {
		md += "\n-# " + sub
	}
	if thumb == "" {
		return discord.Text(md)
	}
	return discord.Section(discord.Thumbnail(thumb, ""), md)
}

// line is a thin divider; gap is space without a line.
func line() discord.Component { return discord.Separator(true, discord.SpacingSmall) }
func gap() discord.Component  { return discord.Separator(false, discord.SpacingSmall) }

// note is small grey text, for footers and hints.
func note(md string) discord.Component { return discord.Text("-# " + md) }

// notice is a one-card confirmation or explanation.
func notice(ephemeral bool, accent int, title, body string, rows ...discord.Component) discord.ResponseData {
	parts := []discord.Component{discord.Text("### " + title + "\n" + body)}
	box := discord.Container(accent, parts...)
	return card(ephemeral, append([]discord.Component{box}, rows...)...)
}

// medal marks a rank: medals for the top three, then a number.
func medal(i int) string {
	switch i {
	case 0:
		return "🥇"
	case 1:
		return "🥈"
	case 2:
		return "🥉"
	}
	return fmt.Sprintf("`%d.`", i+1)
}

// joinMore joins names with " · ", showing at most max and counting the rest.
func joinMore(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, " · ")
	}
	return strings.Join(names[:max], " · ") + fmt.Sprintf(" · *+%d more*", len(names)-max)
}

// bar draws a 10-step meter for a 0-1 fraction.
func bar(frac float64) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	full := int(frac*10 + 0.5)
	return strings.Repeat("▰", full) + strings.Repeat("▱", 10-full)
}

// pct formats a 0-1 fraction as a whole percentage.
func pct(frac float64) string { return fmt.Sprintf("%d%%", int(frac*100+0.5)) }

// safeID makes free text safe to embed in a custom id.
func safeID(s string, max int) string {
	s = strings.ReplaceAll(s, ":", " ")
	return discord.Truncate(s, max)
}
