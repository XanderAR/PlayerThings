package bot

import (
	"context"
	"fmt"
	"time"

	"playradar/internal/discord"
)

// ----- bringing friends in -----
//
// Server views (who plays what, leaderboards, crowns, charts) only mean
// something once a few people have linked. Until then they carry a nudge with
// a button that posts an invite in the channel.

// nudgeEvery limits invite posts to one per server in this window.
const nudgeEvery = 10 * time.Minute

// lonelyNudge is the nudge to show in a server where fewer than two members
// have Steam linked, or nothing when there are enough.
func (b *Bot) lonelyNudge(ctx context.Context, in *discord.Interaction) ([]discord.Component, error) {
	if !in.InGuild() {
		return nil, nil
	}
	n, err := b.Store.VisibleMembers(ctx, in.GuildID, steamPlatform)
	if err != nil || n > 1 {
		return nil, err
	}
	lead := "Nobody here has Steam linked yet."
	if n == 1 {
		lead = "Only one person here has Steam linked so far."
		visible, err := b.Store.Visible(ctx, in.GuildID, in.Invoker().ID)
		if err != nil {
			return nil, err
		}
		if visible {
			lead = "You're the only one here with Steam linked so far."
		}
	}
	return []discord.Component{
		discord.Container(colorGroup, discord.Text("### 👋 Better with friends\n"+lead+
			" Crowns, leaderboards and `/whoplays` come alive once a few people join: they run `/link` once and they're in.")),
		discord.ActionRow(discord.Button(discord.ButtonPrimary, "Invite friends to link", "nudge:link")),
	}, nil
}

// withNudge adds the nudge to a server card when it applies.
func (b *Bot) withNudge(ctx context.Context, in *discord.Interaction, data discord.ResponseData) discord.ResponseData {
	extra, err := b.lonelyNudge(ctx, in)
	if err != nil {
		b.log().Warn("lonely nudge", "err", err)
		return data
	}
	data.Components = append(data.Components, extra...)
	return data
}

// nudgeComponent posts the invite in the channel, at most once per server
// every nudgeEvery.
func (b *Bot) nudgeComponent(in *discord.Interaction) discord.Response {
	if !in.InGuild() {
		return reply("Invites only work inside servers.", true)
	}
	now := b.now()
	b.nudgeMu.Lock()
	if b.nudgedAt == nil {
		b.nudgedAt = map[string]time.Time{}
	}
	if last, ok := b.nudgedAt[in.GuildID]; ok && now.Sub(last) < nudgeEvery {
		b.nudgeMu.Unlock()
		return reply("An invite went out here a few minutes ago. Give people a moment to link.", true)
	}
	b.nudgedAt[in.GuildID] = now
	b.nudgeMu.Unlock()

	return message(card(false,
		discord.Container(colorGroup,
			discord.Text(fmt.Sprintf("## 🎮 Who plays what in here?\n%s wants to compare games with everyone. "+
				"Link your Steam with `/link`: it takes 30 seconds, and sign-in happens on Steam's own site.", mention(in.Invoker().ID))),
			discord.Text("Then try `/whoplays`, `/four` and `/verdict`, and see who holds the most crowns with `/leaderboard`."),
		),
		discord.ActionRow(discord.LinkButton("Get Player.gg", b.InstallURL)),
	))
}
