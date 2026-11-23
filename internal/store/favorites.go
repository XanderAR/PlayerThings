package store

import (
	"context"
	"database/sql"
	"time"
)

const favoriteSchema = `
CREATE TABLE IF NOT EXISTS favorites (
	discord_id TEXT NOT NULL,
	slot       INTEGER NOT NULL,
	platform   TEXT NOT NULL,
	game_id    TEXT NOT NULL,
	set_at     INTEGER NOT NULL,
	PRIMARY KEY (discord_id, slot)
);
CREATE INDEX IF NOT EXISTS favorites_game ON favorites (platform, game_id);
`

// MaxFavorites is how many favorite games someone can pick.
const MaxFavorites = 4

// SetFavorites replaces a user's favorite games, in order.
func (s *Store) SetFavorites(ctx context.Context, discordID, platform string, gameIDs []string, at time.Time) error {
	if len(gameIDs) > MaxFavorites {
		gameIDs = gameIDs[:MaxFavorites]
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM favorites WHERE discord_id = ?`, discordID); err != nil {
			return err
		}
		for i, id := range gameIDs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO favorites (discord_id, slot, platform, game_id, set_at) VALUES (?, ?, ?, ?, ?)`,
				discordID, i, platform, id, at.Unix()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Favorites returns a user's favorite games in order, with their own playtime.
func (s *Store) Favorites(ctx context.Context, discordID, platform string) ([]PlayedGame, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+gameColumns+`, COALESCE(l.minutes_total, 0), COALESCE(l.minutes_recent, 0)
		FROM favorites f
		JOIN games g ON g.platform = f.platform AND g.game_id = f.game_id
		LEFT JOIN library l ON l.discord_id = f.discord_id AND l.platform = f.platform AND l.game_id = f.game_id
		WHERE f.discord_id = ? AND f.platform = ?
		ORDER BY f.slot`, discordID, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlayedGame
	for rows.Next() {
		var pg PlayedGame
		if err := scanGame(rows, &pg.Game, &pg.MinutesTotal, &pg.MinutesRecent); err != nil {
			return nil, err
		}
		out = append(out, pg)
	}
	return out, rows.Err()
}

// FavoriteMatches returns, for each of a server's opted-in members other than
// discordID, which of discordID's favorites are also in their four.
func (s *Store) FavoriteMatches(ctx context.Context, guildID, platform, discordID string) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f2.discord_id, f2.game_id FROM favorites f1
		JOIN favorites f2 ON f2.platform = f1.platform AND f2.game_id = f1.game_id AND f2.discord_id <> f1.discord_id
		JOIN guild_visibility v ON v.guild_id = ? AND v.discord_id = f2.discord_id AND v.visible = 1
		WHERE f1.discord_id = ? AND f1.platform = ?
		ORDER BY f2.discord_id`, guildID, discordID, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var who, id string
		if err := rows.Scan(&who, &id); err != nil {
			return nil, err
		}
		out[who] = append(out[who], id)
	}
	return out, rows.Err()
}
