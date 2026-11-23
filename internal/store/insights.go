package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// GameDetails is store data looked up for a game after it's first synced.
type GameDetails struct {
	Multiplayer *bool // nil when unknown
	Genres      []string
	Reviews     int
	ReleaseYear int
}

// SetGameDetails records a game's details, including its multiplayer tag.
func (s *Store) SetGameDetails(ctx context.Context, platform, gameID string, d GameDetails, now time.Time) error {
	var mp any
	if d.Multiplayer != nil {
		mp = boolInt(*d.Multiplayer)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE games SET multiplayer = ?, checked_at = ?, genres = ?, reviews = ?, release_year = ?, details_at = ?
		WHERE platform = ? AND game_id = ?`,
		mp, now.Unix(), joinGenres(d.Genres), d.Reviews, d.ReleaseYear, now.Unix(), platform, gameID)
	return err
}

// UndetailedGames returns games in someone's library whose details haven't
// been looked up, most total playtime first.
func (s *Store) UndetailedGames(ctx context.Context, platform string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.game_id FROM games g JOIN library l ON l.platform = g.platform AND l.game_id = g.game_id
		WHERE g.platform = ? AND g.details_at = 0
		GROUP BY g.game_id
		ORDER BY SUM(l.minutes_total) DESC, COUNT(*) DESC, g.game_id LIMIT ?`, platform, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UndetailedCount counts a user's played games whose details are still
// being looked up.
func (s *Store) UndetailedCount(ctx context.Context, discordID, platform string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM library l JOIN games g ON g.platform = l.platform AND g.game_id = l.game_id
		WHERE l.discord_id = ? AND l.platform = ? AND l.minutes_total > 0 AND g.details_at = 0`,
		discordID, platform).Scan(&n)
	return n, err
}

// Owned returns a user's library entry for one game.
func (s *Store) Owned(ctx context.Context, discordID, platform, gameID string) (PlayedGame, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+gameColumns+`, l.minutes_total, l.minutes_recent
		FROM library l JOIN games g ON g.platform = l.platform AND g.game_id = l.game_id
		WHERE l.discord_id = ? AND l.platform = ? AND l.game_id = ?`, discordID, platform, gameID)
	var pg PlayedGame
	err := scanGame(row, &pg.Game, &pg.MinutesTotal, &pg.MinutesRecent)
	if errors.Is(err, sql.ErrNoRows) {
		return PlayedGame{}, false, nil
	}
	return pg, err == nil, err
}

// MemberValue is one member's score on a server leaderboard.
type MemberValue struct {
	DiscordID   string
	DisplayName string // platform display name
	AvatarURL   string
	Value       int
}

// visibleLibraries is the library rows of a server's opted-in members whose
// libraries are public. Arguments: platform, guildID.
const visibleLibraries = `
	SELECT a.discord_id, a.display_name, a.avatar_url, l.game_id, l.minutes_total, l.minutes_recent
	FROM guild_visibility v
	JOIN accounts a ON a.discord_id = v.discord_id AND a.platform = ? AND a.library_private = 0
	JOIN library l ON l.discord_id = a.discord_id AND l.platform = a.platform
	WHERE v.guild_id = ? AND v.visible = 1`

// Leaderboard ranks a server's opted-in members. kind is "hours" (all-time
// playtime in minutes), "recent" (last two weeks, in minutes) or "games"
// (games played).
func (s *Store) Leaderboard(ctx context.Context, guildID, platform, kind string, limit int) ([]MemberValue, error) {
	var agg string
	switch kind {
	case "hours":
		agg = "SUM(minutes_total)"
	case "recent":
		agg = "SUM(minutes_recent)"
	case "games":
		agg = "SUM(CASE WHEN minutes_total > 0 THEN 1 ELSE 0 END)"
	default:
		return nil, fmt.Errorf("unknown leaderboard %q", kind)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT discord_id, display_name, avatar_url, `+agg+` AS value
		FROM (`+visibleLibraries+`)
		GROUP BY discord_id
		HAVING value > 0
		ORDER BY value DESC, discord_id LIMIT ?`, platform, guildID, limit)
	if err != nil {
		return nil, err
	}
	return scanMemberValues(rows)
}

// Crown is a game where one member has the most playtime in a server.
type Crown struct {
	Game            Game
	Minutes         int
	RunnerUpID      string // "" when nobody else in the server has played it
	RunnerUpMinutes int
}

// crownHolders ranks each game's players in a server and keeps the top one
// when they're strictly ahead and above a minimum. Arguments: platform,
// guildID, minMinutes.
const crownHolders = `
	WITH ranked AS (
		SELECT discord_id, display_name, avatar_url, game_id, minutes_total,
		       ROW_NUMBER() OVER w AS rn,
		       LEAD(discord_id) OVER w AS next_id,
		       LEAD(minutes_total) OVER w AS next_minutes
		FROM (` + visibleLibraries + `)
		WHERE minutes_total > 0
		WINDOW w AS (PARTITION BY game_id ORDER BY minutes_total DESC, discord_id)
	)
	SELECT * FROM ranked
	WHERE rn = 1 AND minutes_total >= ? AND (next_minutes IS NULL OR minutes_total > next_minutes)`

// Crowns returns the games where discordID has strictly the most playtime
// among a server's opted-in members (at least minMinutes), most playtime
// first, plus the total number of crowns before limit.
func (s *Store) Crowns(ctx context.Context, guildID, platform, discordID string, minMinutes, limit int) ([]Crown, int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+gameColumns+`, c.minutes_total, COALESCE(c.next_id, ''), COALESCE(c.next_minutes, 0)
		FROM (`+crownHolders+`) c
		JOIN games g ON g.platform = ? AND g.game_id = c.game_id
		WHERE c.discord_id = ?
		ORDER BY c.minutes_total DESC, g.name`, platform, guildID, minMinutes, platform, discordID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Crown
	for rows.Next() {
		var c Crown
		if err := scanGame(rows, &c.Game, &c.Minutes, &c.RunnerUpID, &c.RunnerUpMinutes); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	total := len(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

// CrownBoard ranks a server's members by how many crowns they hold.
func (s *Store) CrownBoard(ctx context.Context, guildID, platform string, minMinutes, limit int) ([]MemberValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT discord_id, display_name, avatar_url, COUNT(*) AS value
		FROM (`+crownHolders+`)
		GROUP BY discord_id
		ORDER BY value DESC, discord_id LIMIT ?`, platform, guildID, minMinutes, limit)
	if err != nil {
		return nil, err
	}
	return scanMemberValues(rows)
}

// SharedGame is a game two users both own, with each one's playtime.
type SharedGame struct {
	Game               Game
	AMinutes, BMinutes int
}

// Comparison sums up how two libraries overlap.
type Comparison struct {
	Shared, AOnly, BOnly int          // owned games
	Top                  []SharedGame // played by both, most evenly-played first
}

// Compare measures how two users' libraries overlap.
func (s *Store) Compare(ctx context.Context, platform, a, b string, limit int) (Comparison, error) {
	var c Comparison
	var aCount, bCount int
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM library WHERE discord_id = ? AND platform = ?),
			(SELECT COUNT(*) FROM library WHERE discord_id = ? AND platform = ?),
			(SELECT COUNT(*) FROM library la JOIN library lb
			    ON lb.platform = la.platform AND lb.game_id = la.game_id AND lb.discord_id = ?
			 WHERE la.discord_id = ? AND la.platform = ?)`,
		a, platform, b, platform, b, a, platform).Scan(&aCount, &bCount, &c.Shared)
	if err != nil {
		return c, err
	}
	c.AOnly, c.BOnly = aCount-c.Shared, bCount-c.Shared

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+gameColumns+`, la.minutes_total, lb.minutes_total
		FROM library la
		JOIN library lb ON lb.platform = la.platform AND lb.game_id = la.game_id AND lb.discord_id = ?
		JOIN games g ON g.platform = la.platform AND g.game_id = la.game_id
		WHERE la.discord_id = ? AND la.platform = ? AND la.minutes_total > 0 AND lb.minutes_total > 0
		ORDER BY MIN(la.minutes_total, lb.minutes_total) DESC, la.minutes_total + lb.minutes_total DESC, g.name
		LIMIT ?`, b, a, platform, limit)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var sg SharedGame
		if err := scanGame(rows, &sg.Game, &sg.AMinutes, &sg.BMinutes); err != nil {
			return c, err
		}
		c.Top = append(c.Top, sg)
	}
	return c, rows.Err()
}

// GenreShare is a user's playtime in one genre.
type GenreShare struct {
	Genre   string
	Minutes int
	Games   int // played games in the genre
}

// GenreMinutes totals a user's playtime per genre over played games whose
// details are known, most playtime first. A game counts toward each of its
// genres. limit <= 0 returns every genre.
func (s *Store) GenreMinutes(ctx context.Context, discordID, platform string, limit int) ([]GenreShare, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.genres, l.minutes_total
		FROM library l JOIN games g ON g.platform = l.platform AND g.game_id = l.game_id
		WHERE l.discord_id = ? AND l.platform = ? AND l.minutes_total > 0 AND g.genres != ''`,
		discordID, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := map[string]*GenreShare{}
	for rows.Next() {
		var genres string
		var minutes int
		if err := rows.Scan(&genres, &minutes); err != nil {
			return nil, err
		}
		for _, g := range splitGenres(genres) {
			gs := totals[g]
			if gs == nil {
				gs = &GenreShare{Genre: g}
				totals[g] = gs
			}
			gs.Minutes += minutes
			gs.Games++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]GenreShare, 0, len(totals))
	for _, gs := range totals {
		out = append(out, *gs)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Minutes != out[j].Minutes {
			return out[i].Minutes > out[j].Minutes
		}
		if out[i].Games != out[j].Games {
			return out[i].Games > out[j].Games
		}
		return out[i].Genre < out[j].Genre
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func scanMemberValues(rows *sql.Rows) ([]MemberValue, error) {
	defer rows.Close()
	var out []MemberValue
	for rows.Next() {
		var m MemberValue
		if err := rows.Scan(&m.DiscordID, &m.DisplayName, &m.AvatarURL, &m.Value); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
