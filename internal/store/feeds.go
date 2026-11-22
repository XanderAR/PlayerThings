package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const feedSchema = `
CREATE TABLE IF NOT EXISTS channel_feeds (
	guild_id       TEXT NOT NULL,
	kind           TEXT NOT NULL,
	channel_id     TEXT NOT NULL,
	webhook_id     TEXT NOT NULL,
	webhook_token  TEXT NOT NULL,
	created_by     TEXT NOT NULL,
	created_at     INTEGER NOT NULL,
	last_posted_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (guild_id, kind)
);

CREATE TABLE IF NOT EXISTS radar_posted (
	guild_id     TEXT NOT NULL,
	playtest_url TEXT NOT NULL,
	posted_at    INTEGER NOT NULL,
	PRIMARY KEY (guild_id, playtest_url)
);

CREATE TABLE IF NOT EXISTS usage_counts (
	day      TEXT NOT NULL,
	kind     TEXT NOT NULL,
	name     TEXT NOT NULL,
	guild_id TEXT NOT NULL,
	count    INTEGER NOT NULL,
	PRIMARY KEY (day, kind, name, guild_id)
);
`

// Feed kinds.
const (
	FeedRadar       = "radar"
	FeedWeeklyChart = "weekly_chart"
)

// Feed is a channel the bot posts to on its own, through a webhook it created.
type Feed struct {
	GuildID      string
	Kind         string
	ChannelID    string
	WebhookID    string
	WebhookToken string
	CreatedBy    string
	CreatedAt    time.Time
	LastPostedAt time.Time
}

// SetFeed creates or replaces a server's feed of one kind.
func (s *Store) SetFeed(ctx context.Context, f Feed) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_feeds (guild_id, kind, channel_id, webhook_id, webhook_token, created_by, created_at, last_posted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (guild_id, kind) DO UPDATE SET
			channel_id = excluded.channel_id, webhook_id = excluded.webhook_id,
			webhook_token = excluded.webhook_token, created_by = excluded.created_by,
			created_at = excluded.created_at, last_posted_at = excluded.last_posted_at`,
		f.GuildID, f.Kind, f.ChannelID, f.WebhookID, f.WebhookToken, f.CreatedBy, f.CreatedAt.Unix(), unix(f.LastPostedAt))
	return err
}

// Feeds lists every feed of a kind.
func (s *Store) Feeds(ctx context.Context, kind string) ([]Feed, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT guild_id, kind, channel_id, webhook_id, webhook_token, created_by, created_at, last_posted_at
		FROM channel_feeds WHERE kind = ? ORDER BY guild_id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Feed
	for rows.Next() {
		var f Feed
		var created, posted int64
		if err := rows.Scan(&f.GuildID, &f.Kind, &f.ChannelID, &f.WebhookID, &f.WebhookToken, &f.CreatedBy, &created, &posted); err != nil {
			return nil, err
		}
		f.CreatedAt, f.LastPostedAt = fromUnix(created), fromUnix(posted)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Feed returns one server's feed of a kind.
func (s *Store) Feed(ctx context.Context, guildID, kind string) (Feed, bool, error) {
	var f Feed
	var created, posted int64
	err := s.db.QueryRowContext(ctx, `
		SELECT guild_id, kind, channel_id, webhook_id, webhook_token, created_by, created_at, last_posted_at
		FROM channel_feeds WHERE guild_id = ? AND kind = ?`, guildID, kind).
		Scan(&f.GuildID, &f.Kind, &f.ChannelID, &f.WebhookID, &f.WebhookToken, &f.CreatedBy, &created, &posted)
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, false, nil
	}
	f.CreatedAt, f.LastPostedAt = fromUnix(created), fromUnix(posted)
	return f, err == nil, err
}

// DeleteFeed stops a feed.
func (s *Store) DeleteFeed(ctx context.Context, guildID, kind string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM channel_feeds WHERE guild_id = ? AND kind = ?`, guildID, kind)
	return err
}

// MarkFeedPosted records when a feed last posted.
func (s *Store) MarkFeedPosted(ctx context.Context, guildID, kind string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE channel_feeds SET last_posted_at = ? WHERE guild_id = ? AND kind = ?`, at.Unix(), guildID, kind)
	return err
}

// RadarPosted reports whether a playtest was already posted to a server.
func (s *Store) RadarPosted(ctx context.Context, guildID, playtestURL string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM radar_posted WHERE guild_id = ? AND playtest_url = ?`, guildID, playtestURL).Scan(&n)
	return n > 0, err
}

// MarkRadarPosted records that a playtest was posted to a server.
func (s *Store) MarkRadarPosted(ctx context.Context, guildID, playtestURL string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO radar_posted (guild_id, playtest_url, posted_at) VALUES (?, ?, ?)
		ON CONFLICT (guild_id, playtest_url) DO NOTHING`, guildID, playtestURL, at.Unix())
	return err
}

// MembersOwningAny counts a server's opted-in members who own at least one of
// the given games.
func (s *Store) MembersOwningAny(ctx context.Context, guildID, platform string, gameIDs []string) (int, error) {
	if len(gameIDs) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(gameIDs)), ",")
	args := []any{platform, guildID}
	for _, id := range gameIDs {
		args = append(args, id)
	}
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT v.discord_id) FROM guild_visibility v
		JOIN library l ON l.discord_id = v.discord_id AND l.platform = ?
		WHERE v.guild_id = ? AND v.visible = 1 AND l.game_id IN (`+placeholders+`)`, args...).Scan(&n)
	return n, err
}

// Usage kinds.
const (
	UsageCommand = "command"
	UsageClick   = "click"
)

// CountUsage adds one to a daily counter (commands run, links clicked).
func (s *Store) CountUsage(ctx context.Context, kind, name, guildID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_counts (day, kind, name, guild_id, count) VALUES (?, ?, ?, ?, 1)
		ON CONFLICT (day, kind, name, guild_id) DO UPDATE SET count = count + 1`,
		at.UTC().Format("2006-01-02"), kind, name, guildID)
	return err
}

// UsageRow is one line of the usage report.
type UsageRow struct {
	Kind    string
	Name    string
	Count   int
	Servers int
}

// UsageSince totals commands and clicks since a day (YYYY-MM-DD).
func (s *Store) UsageSince(ctx context.Context, day string) ([]UsageRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind, name, SUM(count), COUNT(DISTINCT NULLIF(guild_id, ''))
		FROM usage_counts WHERE day >= ?
		GROUP BY kind, name ORDER BY kind, SUM(count) DESC`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Kind, &r.Name, &r.Count, &r.Servers); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Totals is a headline count for the usage report.
type Totals struct {
	LinkedUsers    int
	VisibleServers int
	Feeds          int
}

// CountTotals returns headline numbers.
func (s *Store) CountTotals(ctx context.Context) (Totals, error) {
	var t Totals
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(DISTINCT discord_id) FROM accounts),
		       (SELECT COUNT(DISTINCT guild_id) FROM guild_visibility WHERE visible = 1),
		       (SELECT COUNT(*) FROM channel_feeds)`).Scan(&t.LinkedUsers, &t.VisibleServers, &t.Feeds)
	return t, err
}
