package store

import (
	"context"
	"strings"
	"time"
)

const momentSchema = `
CREATE TABLE IF NOT EXISTS moment_votes (
	guild_id   TEXT NOT NULL,
	moment_id  TEXT NOT NULL,
	discord_id TEXT NOT NULL,
	choice     TEXT NOT NULL,
	voted_at   INTEGER NOT NULL,
	PRIMARY KEY (guild_id, moment_id, discord_id)
);
`

// Vote records (or changes) one member's answer to a moment post.
func (s *Store) Vote(ctx context.Context, guildID, momentID, discordID, choice string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO moment_votes (guild_id, moment_id, discord_id, choice, voted_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (guild_id, moment_id, discord_id) DO UPDATE SET choice = excluded.choice, voted_at = excluded.voted_at`,
		guildID, momentID, discordID, choice, at.Unix())
	return err
}

// PlayedMinutes returns a user's total minutes for each of ids they've played.
func (s *Store) PlayedMinutes(ctx context.Context, discordID, platform string, ids []string) (map[string]int, error) {
	out := map[string]int{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{discordID, platform}
	rows, err := s.db.QueryContext(ctx, `
		SELECT game_id, minutes_total FROM library
		WHERE discord_id = ? AND platform = ? AND minutes_total > 0 AND game_id IN (`+placeholders(ids, &args)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var m int
		if err := rows.Scan(&id, &m); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

// PlayedBy returns, for each of a server's opted-in members, which of ids
// they've played.
func (s *Store) PlayedBy(ctx context.Context, guildID, platform string, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{platform, guildID}
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.discord_id, l.game_id FROM guild_visibility v
		JOIN library l ON l.discord_id = v.discord_id AND l.platform = ?
		WHERE v.guild_id = ? AND v.visible = 1 AND l.minutes_total > 0 AND l.game_id IN (`+placeholders(ids, &args)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var who, id string
		if err := rows.Scan(&who, &id); err != nil {
			return nil, err
		}
		out[who] = append(out[who], id)
	}
	return out, rows.Err()
}

// ServerTotal counts a server's opted-in members who've played any of ids,
// and their combined minutes in them.
func (s *Store) ServerTotal(ctx context.Context, guildID, platform string, ids []string) (players, minutes int, err error) {
	if len(ids) == 0 {
		return 0, 0, nil
	}
	args := []any{platform, guildID}
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT l.discord_id), COALESCE(SUM(l.minutes_total), 0) FROM guild_visibility v
		JOIN library l ON l.discord_id = v.discord_id AND l.platform = ?
		WHERE v.guild_id = ? AND v.visible = 1 AND l.minutes_total > 0 AND l.game_id IN (`+placeholders(ids, &args)+`)`, args...).Scan(&players, &minutes)
	return players, minutes, err
}

// placeholders returns "?, ?, ..." for ids and appends them to args.
func placeholders(ids []string, args *[]any) string {
	marks := make([]string, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		*args = append(*args, id)
	}
	return strings.Join(marks, ", ")
}

// VoteCounts tallies a moment's answers in one server.
func (s *Store) VoteCounts(ctx context.Context, guildID, momentID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT choice, COUNT(*) FROM moment_votes WHERE guild_id = ? AND moment_id = ? GROUP BY choice`, guildID, momentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var choice string
		var n int
		if err := rows.Scan(&choice, &n); err != nil {
			return nil, err
		}
		out[choice] = n
	}
	return out, rows.Err()
}
