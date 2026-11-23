package bot

import (
	"context"
	"fmt"
	"time"

	"playradar/internal/discord"
	"playradar/internal/playtests"
	"playradar/internal/store"
)

// ----- /radar -----

func (b *Bot) cmdRadar(ctx context.Context, in *discord.Interaction) discord.Response {
	if !in.InGuild() {
		return reply("`/radar` works inside servers.", true)
	}
	if channel := in.Data.StringOption("channel"); channel != "" {
		return b.setupFeed(in, store.FeedRadar, channel)
	}
	feed, ok, err := b.Store.Feed(ctx, in.GuildID, store.FeedRadar)
	if err != nil {
		return b.oops(err, "load radar")
	}
	if !ok {
		return reply("The playtest radar is off. Turn it on with `/radar channel:#some-channel`.", true)
	}
	return message(notice(true, colorLive, "📡 Playtest radar is on",
		fmt.Sprintf("New playtests are posted in <#%s>, with a note when they fit what people here play.", feed.ChannelID),
		discord.ActionRow(discord.Button(discord.ButtonDanger, "Turn off the radar", "radar:off"))))
}

func (b *Bot) radarOff(ctx context.Context, in *discord.Interaction) discord.Response {
	if !in.InGuild() || !in.Member.CanManageServer() {
		return reply("Turning off the radar needs the Manage Server permission.", true)
	}
	feed, ok, err := b.Store.Feed(ctx, in.GuildID, store.FeedRadar)
	if err != nil {
		return b.oops(err, "load radar")
	}
	if ok {
		if err := b.Discord.DeleteWebhook(ctx, discord.Webhook{ID: feed.WebhookID, Token: feed.WebhookToken}); err != nil {
			b.log().Warn("delete radar webhook", "err", err)
		}
		if err := b.Store.DeleteFeed(ctx, in.GuildID, store.FeedRadar); err != nil {
			return b.oops(err, "delete radar")
		}
	}
	return update(notice(true, colorMuted, "📡 Playtest radar is off", "Turn it back on any time with `/radar channel:#some-channel`."))
}

// setupFeed creates a webhook in the channel, saves the feed and makes its
// first post, all after a deferred reply.
func (b *Bot) setupFeed(in *discord.Interaction, kind, channelID string) discord.Response {
	guildID, userID := in.GuildID, in.Invoker().ID
	return b.later(in, true, func(ctx context.Context) discord.ResponseData {
		hook, err := b.Discord.CreateWebhook(ctx, channelID, b.AppName)
		if err != nil {
			if discord.HasCode(err, discord.CodeMissingPermissions) || discord.HasCode(err, discord.CodeMissingAccess) {
				return discord.ResponseData{Content: fmt.Sprintf(
					"I can't post in <#%s>. Add me to this server with the **Manage Webhooks** permission, and make sure I can see that channel: %s",
					channelID, b.InstallURL)}
			}
			b.log().Error("create webhook", "err", err)
			return discord.ResponseData{Content: "I couldn't set that up. Try again in a minute."}
		}

		if old, ok, _ := b.Store.Feed(ctx, guildID, kind); ok {
			_ = b.Discord.DeleteWebhook(ctx, discord.Webhook{ID: old.WebhookID, Token: old.WebhookToken})
		}
		feed := store.Feed{
			GuildID: guildID, Kind: kind, ChannelID: channelID,
			WebhookID: hook.ID, WebhookToken: hook.Token, CreatedBy: userID,
			CreatedAt: b.now(), LastPostedAt: b.now(),
		}
		if err := b.Store.SetFeed(ctx, feed); err != nil {
			b.log().Error("save feed", "err", err)
			return discord.ResponseData{Content: "I couldn't save that. Try again in a minute."}
		}

		switch kind {
		case store.FeedRadar:
			if err := b.radarWelcome(ctx, feed); err != nil {
				b.log().Warn("radar welcome post", "err", err)
			}
			return discord.ResponseData{Content: fmt.Sprintf("Done. New playtests will be posted in <#%s>, with a note when they fit what people here play.", channelID)}
		default:
			if data, ok, err := b.chartMessage(ctx, guildID, "weekly", "posts every Monday"); err == nil && ok {
				_ = b.post(ctx, feed, data)
			}
			return discord.ResponseData{Content: fmt.Sprintf("Done. The chart will be posted in <#%s> every Monday.", channelID)}
		}
	})
}

// RunFeeds posts new playtests and weekly charts until ctx ends.
func (b *Bot) RunFeeds(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		b.postNewPlaytests(ctx)
		b.postMoments(ctx)
		b.postDueCharts(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// radarWelcome posts what's live right now and marks every current playtest
// as seen, so only later additions get their own posts.
func (b *Bot) radarWelcome(ctx context.Context, feed store.Feed) error {
	all, err := playtests.Load(b.PlaytestsFile)
	if err != nil {
		return err
	}
	current := playtests.Current(all, b.now())
	parts := []discord.Component{
		heading("📡 Playtest radar is on", "New playtests on player.gg land here, with a note when they fit what people here play", ""),
	}
	for i, p := range current {
		if i == 3 {
			break
		}
		parts = append(parts, line(),
			discord.Section(discord.LinkButton("Request access", b.Links.Go(p.URL, "radar", feed.GuildID)),
				fmt.Sprintf("**%s**\n-# %s", p.Title, b.status(p))))
	}
	if len(current) > 0 {
		parts = append(parts, line(), note("Open right now · `/playtests` shows the rest"))
	}
	data := card(false,
		discord.Container(colorLive, parts...),
		discord.ActionRow(b.allPlaytestsButton("radar", feed.GuildID)),
	)
	if err := b.post(ctx, feed, data); err != nil {
		return err
	}
	for _, p := range current {
		if err := b.Store.MarkRadarPosted(ctx, feed.GuildID, p.URL, b.now()); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) postNewPlaytests(ctx context.Context) {
	all, err := playtests.Load(b.PlaytestsFile)
	if err != nil {
		b.log().Error("load playtests", "err", err)
		return
	}
	current := playtests.Current(all, b.now())
	if len(current) == 0 {
		return
	}
	feeds, err := b.Store.Feeds(ctx, store.FeedRadar)
	if err != nil {
		b.log().Error("list radar feeds", "err", err)
		return
	}
	for _, f := range feeds {
		for _, p := range current {
			if ctx.Err() != nil {
				return
			}
			done, err := b.Store.RadarPosted(ctx, f.GuildID, p.URL)
			if err != nil || done {
				continue
			}
			data, err := b.radarMessage(ctx, f.GuildID, p)
			if err != nil {
				b.log().Error("build radar post", "err", err)
				continue
			}
			if err := b.post(ctx, f, data); err != nil {
				b.log().Warn("radar post", "guild", f.GuildID, "err", err)
				break
			}
			_ = b.Store.MarkRadarPosted(ctx, f.GuildID, p.URL, b.now())
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// radarMessage announces one playtest, noting how many members play similar games.
func (b *Bot) radarMessage(ctx context.Context, guildID string, p playtests.Playtest) (discord.ResponseData, error) {
	fit := b.fitLine(ctx, guildID, p)
	kicker := "📡 New playtest"
	if fit != "" {
		kicker = "📡 New playtest that fits this server"
	}
	accent := colorSoon
	if p.Live(b.now()) {
		accent = colorLive
	}
	var parts []discord.Component
	if p.Image != "" {
		parts = append(parts, discord.Gallery(discord.MediaItem{Media: discord.Media{URL: p.Image}}))
	}
	head := fmt.Sprintf("-# %s\n## %s\n-# %s", kicker, p.Title, b.status(p))
	if p.Studio != "" {
		head += " · " + p.Studio
	}
	parts = append(parts, discord.Text(head))
	if fit != "" {
		parts = append(parts, discord.Text("🎯 "+fit))
	}
	if p.Summary != "" {
		parts = append(parts, discord.Text(p.Summary))
	}
	return card(false,
		discord.Container(accent, parts...),
		discord.ActionRow(
			discord.LinkButton("Request access", b.Links.Go(p.URL, "radar", guildID)),
			b.allPlaytestsButton("radar", guildID),
		),
	), nil
}

func (b *Bot) postDueCharts(ctx context.Context) {
	due := lastMondayTen(b.now())
	feeds, err := b.Store.Feeds(ctx, store.FeedWeeklyChart)
	if err != nil {
		b.log().Error("list chart feeds", "err", err)
		return
	}
	for _, f := range feeds {
		if ctx.Err() != nil {
			return
		}
		if !f.LastPostedAt.Before(due) {
			continue
		}
		data, ok, err := b.chartMessage(ctx, f.GuildID, "weekly", "posts every Monday")
		if err != nil {
			b.log().Error("weekly chart", "err", err)
			continue
		}
		if ok {
			if err := b.post(ctx, f, data); err != nil {
				b.log().Warn("weekly chart post", "guild", f.GuildID, "err", err)
				continue
			}
		}
		_ = b.Store.MarkFeedPosted(ctx, f.GuildID, f.Kind, b.now())
	}
}

// linkButtonsOnly drops buttons that call back into the app from top-level
// action rows, and rows left empty.
func linkButtonsOnly(comps []discord.Component) []discord.Component {
	var out []discord.Component
	for _, c := range comps {
		if c.Type != discord.ComponentActionRow {
			out = append(out, c)
			continue
		}
		var kept []discord.Component
		for _, btn := range c.Components {
			if btn.Type == discord.ComponentButton && btn.Style == discord.ButtonLink {
				kept = append(kept, btn)
			}
		}
		if len(kept) > 0 {
			out = append(out, discord.ActionRow(kept...))
		}
	}
	return out
}

// lastMondayTen is the most recent Monday 10:00 UTC at or before now.
func lastMondayTen(now time.Time) time.Time {
	now = now.UTC()
	daysSince := (int(now.Weekday()) + 6) % 7 // Monday = 0
	t := time.Date(now.Year(), now.Month(), now.Day()-daysSince, 10, 0, 0, 0, time.UTC)
	if t.After(now) {
		t = t.AddDate(0, 0, -7)
	}
	return t
}

// post sends a message through a feed's webhook, dropping the feed if the
// webhook was deleted in Discord. Only link buttons are kept: scheduled posts
// don't need buttons that call back into the app.
func (b *Bot) post(ctx context.Context, f store.Feed, data discord.ResponseData) error {
	return b.postWith(ctx, f, data, false)
}

// postWith posts through a feed's webhook. interactive keeps buttons that call
// back into the app, which works because the app created (owns) the webhook.
func (b *Bot) postWith(ctx context.Context, f store.Feed, data discord.ResponseData, interactive bool) error {
	comps := data.Components
	if !interactive {
		comps = linkButtonsOnly(comps)
	}
	msg := discord.WebhookMessage{
		Username:        b.AppName,
		AvatarURL:       b.AvatarURL,
		Content:         data.Content,
		Embeds:          data.Embeds,
		Components:      comps,
		Flags:           data.Flags & discord.FlagComponentsV2,
		AllowedMentions: discord.NoPings(),
	}
	err := b.Discord.ExecuteWebhook(ctx, discord.Webhook{ID: f.WebhookID, Token: f.WebhookToken}, msg)
	if discord.HasCode(err, discord.CodeUnknownWebhook) {
		b.log().Info("webhook deleted in Discord; removing feed", "guild", f.GuildID, "kind", f.Kind)
		_ = b.Store.DeleteFeed(ctx, f.GuildID, f.Kind)
	}
	return err
}
