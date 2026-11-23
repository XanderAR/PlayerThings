// Package store keeps linked accounts, synced libraries and per-server
// visibility in SQLite. Tables are keyed by platform so more platforms
// (Xbox, PlayStation, RetroAchievements...) can be added without migrations.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
	discord_id      TEXT NOT NULL,
	platform        TEXT NOT NULL,
	external_id     TEXT NOT NULL,
	display_name    TEXT NOT NULL DEFAULT '',
	avatar_url      TEXT NOT NULL DEFAULT '',
	profile_url     TEXT NOT NULL DEFAULT '',
	library_private INTEGER NOT NULL DEFAULT 0,
	linked_at       INTEGER NOT NULL,
	synced_at       INTEGER NOT NULL DEFAULT 0,
	sync_error      TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (discord_id, platform),
	UNIQUE (platform, external_id)
);

CREATE TABLE IF NOT EXISTS games (
	platform    TEXT NOT NULL,
	game_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	image_url   TEXT NOT NULL DEFAULT '',
	store_url   TEXT NOT NULL DEFAULT '',
	multiplayer INTEGER,
	checked_at  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (platform, game_id)
);

CREATE TABLE IF NOT EXISTS library (
	discord_id     TEXT NOT NULL,
	platform       TEXT NOT NULL,
	game_id        TEXT NOT NULL,
	minutes_total  INTEGER NOT NULL DEFAULT 0,
	minutes_recent INTEGER NOT NULL DEFAULT 0,
	last_played    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (discord_id, platform, game_id),
	FOREIGN KEY (discord_id, platform) REFERENCES accounts (discord_id, platform) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS library_game ON library (platform, game_id);

CREATE TABLE IF NOT EXISTS playtime_snapshots (
	discord_id    TEXT NOT NULL,
	platform      TEXT NOT NULL,
	game_id       TEXT NOT NULL,
	day           TEXT NOT NULL,
	minutes_total INTEGER NOT NULL,
	PRIMARY KEY (discord_id, platform, game_id, day),
	FOREIGN KEY (discord_id, platform) REFERENCES accounts (discord_id, platform) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS guild_visibility (
	guild_id   TEXT NOT NULL,
	discord_id TEXT NOT NULL,
	visible    INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (guild_id, discord_id)
);
CREATE INDEX IF NOT EXISTS guild_visibility_user ON guild_visibility (discord_id);
`

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Account is one linked platform account.
type Account struct {
	DiscordID      string
	Platform       string
	ExternalID     string
	DisplayName    string
	AvatarURL      string
	ProfileURL     string
	LibraryPrivate bool
	LinkedAt       time.Time
	SyncedAt       time.Time
	SyncError      string
}

// Game is catalogue data for one game on one platform.
type Game struct {
	Platform    string
	GameID      string
	Name        string
	ImageURL    string
	StoreURL    string
	Multiplayer *bool // nil until checked
	CheckedAt   time.Time
}

// LibraryEntry is one game in a user's synced library.
type LibraryEntry struct {
	GameID        string
	MinutesTotal  int
	MinutesRecent int // last two weeks
	LastPlayed    time.Time
}

// Profile is the account details refreshed on each sync.
type Profile struct {
	DisplayName string
	AvatarURL   string
	ProfileURL  string
	Private     bool
}

// PlayedGame is a game with one user's playtime.
type PlayedGame struct {
	Game
	MinutesTotal  int
	MinutesRecent int
}

// Ranked is one member's playtime for a game.
type Ranked struct {
	DiscordID     string
	MinutesTotal  int
	MinutesRecent int
}

// GameMatch is a game owned by members of a server.
type GameMatch struct {
	Game
	Owners int
}

// CommonGame is a game owned by every member of a group.
type CommonGame struct {
	Game
	CombinedMinutes int
}

// ChartEntry is a game's recent playtime across a server.
type ChartEntry struct {
	Game
	Minutes int
	Players int
}

// Open opens (and creates if needed) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	for _, s := range []string{schema, feedSchema} {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("create schema: %w", err)
		}
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// LinkAccount stores a newly verified account. If another Discord user had
// linked the same platform account, that older link is removed: the platform
// sign-in just proved who owns it.
func (s *Store) LinkAccount(ctx context.Context, a Account) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM accounts WHERE platform = ? AND external_id = ? AND discord_id <> ?`,
			a.Platform, a.ExternalID, a.DiscordID); err != nil {
			return err
		}
		// Re-linking a different external account replaces the old library.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM accounts WHERE discord_id = ? AND platform = ? AND external_id <> ?`,
			a.DiscordID, a.Platform, a.ExternalID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO accounts (discord_id, platform, external_id, display_name, avatar_url, profile_url, linked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (discord_id, platform) DO UPDATE SET linked_at = excluded.linked_at`,
			a.DiscordID, a.Platform, a.ExternalID, a.DisplayName, a.AvatarURL, a.ProfileURL, a.LinkedAt.Unix())
		return err
	})
}

// Account returns a user's account on platform.
func (s *Store) Account(ctx context.Context, discordID, platform string) (Account, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT discord_id, platform, external_id, display_name, avatar_url, profile_url,
		       library_private, linked_at, synced_at, sync_error
		FROM accounts WHERE discord_id = ? AND platform = ?`, discordID, platform)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	return a, err == nil, err
}

// DueForSync returns accounts last synced before cutoff, plus accounts whose
// last sync failed before retryCutoff, oldest first.
func (s *Store) DueForSync(ctx context.Context, platform string, cutoff, retryCutoff time.Time, limit int) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT discord_id, platform, external_id, display_name, avatar_url, profile_url,
		       library_private, linked_at, synced_at, sync_error
		FROM accounts
		WHERE platform = ? AND (synced_at < ? OR (sync_error != '' AND synced_at < ?))
		ORDER BY synced_at LIMIT ?`, platform, cutoff.Unix(), retryCutoff.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveSync replaces an account's library with freshly synced data and records
// a playtime snapshot for every game whose total changed.
func (s *Store) SaveSync(ctx context.Context, discordID, platform string, p Profile, games []Game, entries []LibraryEntry, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		upsertGame, err := tx.PrepareContext(ctx, `
			INSERT INTO games (platform, game_id, name, image_url, store_url) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (platform, game_id) DO UPDATE SET
				name = excluded.name, image_url = excluded.image_url, store_url = excluded.store_url`)
		if err != nil {
			return err
		}
		defer upsertGame.Close()
		for _, g := range games {
			if _, err := upsertGame.ExecContext(ctx, platform, g.GameID, g.Name, g.ImageURL, g.StoreURL); err != nil {
				return err
			}
		}

		previous := map[string]int{}
		rows, err := tx.QueryContext(ctx,
			`SELECT game_id, minutes_total FROM library WHERE discord_id = ? AND platform = ?`, discordID, platform)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var minutes int
			if err := rows.Scan(&id, &minutes); err != nil {
				rows.Close()
				return err
			}
			previous[id] = minutes
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM library WHERE discord_id = ? AND platform = ?`, discordID, platform); err != nil {
			return err
		}
		insert, err := tx.PrepareContext(ctx, `
			INSERT INTO library (discord_id, platform, game_id, minutes_total, minutes_recent, last_played)
			VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer insert.Close()
		snapshot, err := tx.PrepareContext(ctx, `
			INSERT INTO playtime_snapshots (discord_id, platform, game_id, day, minutes_total)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (discord_id, platform, game_id, day) DO UPDATE SET minutes_total = excluded.minutes_total`)
		if err != nil {
			return err
		}
		defer snapshot.Close()

		day := now.UTC().Format("2006-01-02")
		for _, e := range entries {
			if _, err := insert.ExecContext(ctx, discordID, platform, e.GameID, e.MinutesTotal, e.MinutesRecent, unix(e.LastPlayed)); err != nil {
				return err
			}
			if old, seen := previous[e.GameID]; e.MinutesTotal > 0 && (!seen || old != e.MinutesTotal) {
				if _, err := snapshot.ExecContext(ctx, discordID, platform, e.GameID, day, e.MinutesTotal); err != nil {
					return err
				}
			}
		}

		_, err = tx.ExecContext(ctx, `
			UPDATE accounts SET display_name = ?, avatar_url = ?, profile_url = ?, library_private = ?,
			       synced_at = ?, sync_error = ''
			WHERE discord_id = ? AND platform = ?`,
			p.DisplayName, p.AvatarURL, p.ProfileURL, boolInt(p.Private), now.Unix(), discordID, platform)
		return err
	})
}

// MarkSyncError records a failed sync so the account is retried later.
func (s *Store) MarkSyncError(ctx context.Context, discordID, platform, msg string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET synced_at = ?, sync_error = ? WHERE discord_id = ? AND platform = ?`,
		now.Unix(), msg, discordID, platform)
	return err
}

// Stats summarizes one user's library.
type Stats struct {
	Games         int
	Minutes       int
	RecentMinutes int // last two weeks
}

// LibraryStats summarizes a user's library on a platform.
func (s *Store) LibraryStats(ctx context.Context, discordID, platform string) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(minutes_total), 0), COALESCE(SUM(minutes_recent), 0)
		FROM library WHERE discord_id = ? AND platform = ?`,
		discordID, platform).Scan(&st.Games, &st.Minutes, &st.RecentMinutes)
	return st, err
}

// TopGames returns a user's most-played games, all-time or in the last two weeks.
func (s *Store) TopGames(ctx context.Context, discordID, platform string, recent bool, limit int) ([]PlayedGame, error) {
	order := "l.minutes_total"
	if recent {
		order = "l.minutes_recent"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+gameColumns+`, l.minutes_total, l.minutes_recent
		FROM library l JOIN games g ON g.platform = l.platform AND g.game_id = l.game_id
		WHERE l.discord_id = ? AND l.platform = ? AND `+order+` > 0
		ORDER BY `+order+` DESC, g.name LIMIT ?`, discordID, platform, limit)
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

// SetVisibility shows or hides a user on one server's leaderboards.
func (s *Store) SetVisibility(ctx context.Context, guildID, discordID string, visible bool, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO guild_visibility (guild_id, discord_id, visible, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (guild_id, discord_id) DO UPDATE SET visible = excluded.visible, updated_at = excluded.updated_at`,
		guildID, discordID, boolInt(visible), now.Unix())
	return err
}

// Visible reports whether a user has opted in to a server's leaderboards.
func (s *Store) Visible(ctx context.Context, guildID, discordID string) (bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx,
		`SELECT visible FROM guild_visibility WHERE guild_id = ? AND discord_id = ?`, guildID, discordID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == 1, err
}

// VisibleMembers counts opted-in members of a server with an account on platform.
func (s *Store) VisibleMembers(ctx context.Context, guildID, platform string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM guild_visibility v
		JOIN accounts a ON a.discord_id = v.discord_id AND a.platform = ?
		WHERE v.guild_id = ? AND v.visible = 1`, platform, guildID).Scan(&n)
	return n, err
}

// Game returns catalogue data for one game.
func (s *Store) Game(ctx context.Context, platform, gameID string) (Game, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+gameColumns+` FROM games g WHERE g.platform = ? AND g.game_id = ?`, platform, gameID)
	var g Game
	err := scanGame(row, &g)
	if errors.Is(err, sql.ErrNoRows) {
		return Game{}, false, nil
	}
	return g, err == nil, err
}

// SearchServerGames finds games owned by a server's opted-in members whose
// name contains query, most widely owned first. Used for autocomplete.
func (s *Store) SearchServerGames(ctx context.Context, guildID, platform, query string, limit int) ([]GameMatch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+gameColumns+`, COUNT(*) AS owners
		FROM guild_visibility v
		JOIN library l ON l.discord_id = v.discord_id AND l.platform = ?
		JOIN games g ON g.platform = l.platform AND g.game_id = l.game_id
		WHERE v.guild_id = ? AND v.visible = 1 AND g.name LIKE ? ESCAPE '\'
		GROUP BY g.platform, g.game_id
		ORDER BY owners DESC, g.name LIMIT ?`, platform, guildID, likePattern(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GameMatch
	for rows.Next() {
		var m GameMatch
		if err := scanGame(rows, &m.Game, &m.Owners); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// FindServerGame resolves typed text to a game owned in the server: an exact
// (case-insensitive) name match if there is one, otherwise the most-owned
// partial match.
func (s *Store) FindServerGame(ctx context.Context, guildID, platform, text string) (Game, bool, error) {
	matches, err := s.SearchServerGames(ctx, guildID, platform, text, 25)
	if err != nil || len(matches) == 0 {
		return Game{}, false, err
	}
	for _, m := range matches {
		if strings.EqualFold(m.Name, strings.TrimSpace(text)) {
			return m.Game, true, nil
		}
	}
	return matches[0].Game, true, nil
}

// WhoPlays ranks a server's opted-in members who own a game by total playtime.
func (s *Store) WhoPlays(ctx context.Context, guildID, platform, gameID string, limit int) ([]Ranked, int, error) {
	var owners int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM guild_visibility v
		JOIN library l ON l.discord_id = v.discord_id AND l.platform = ? AND l.game_id = ?
		WHERE v.guild_id = ? AND v.visible = 1`, platform, gameID, guildID).Scan(&owners); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.discord_id, l.minutes_total, l.minutes_recent
		FROM guild_visibility v
		JOIN library l ON l.discord_id = v.discord_id AND l.platform = ? AND l.game_id = ?
		WHERE v.guild_id = ? AND v.visible = 1
		ORDER BY l.minutes_total DESC, l.minutes_recent DESC LIMIT ?`, platform, gameID, guildID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Ranked
	for rows.Next() {
		var r Ranked
		if err := rows.Scan(&r.DiscordID, &r.MinutesTotal, &r.MinutesRecent); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, owners, rows.Err()
}

// CommonGames returns games every listed user owns, by combined playtime.
func (s *Store) CommonGames(ctx context.Context, platform string, discordIDs []string, limit int) ([]CommonGame, error) {
	if len(discordIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(discordIDs)), ",")
	args := []any{platform}
	for _, id := range discordIDs {
		args = append(args, id)
	}
	args = append(args, len(discordIDs), limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+gameColumns+`, SUM(l.minutes_total) AS combined
		FROM library l JOIN games g ON g.platform = l.platform AND g.game_id = l.game_id
		WHERE l.platform = ? AND l.discord_id IN (`+placeholders+`)
		GROUP BY g.platform, g.game_id
		HAVING COUNT(DISTINCT l.discord_id) = ?
		ORDER BY combined DESC, g.name LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommonGame
	for rows.Next() {
		var c CommonGame
		if err := scanGame(rows, &c.Game, &c.CombinedMinutes); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ServerChart ranks games by playtime in the last two weeks across a server's
// opted-in members.
func (s *Store) ServerChart(ctx context.Context, guildID, platform string, limit int) ([]ChartEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+gameColumns+`, SUM(l.minutes_recent) AS minutes, COUNT(*) AS players
		FROM guild_visibility v
		JOIN library l ON l.discord_id = v.discord_id AND l.platform = ?
		JOIN games g ON g.platform = l.platform AND g.game_id = l.game_id
		WHERE v.guild_id = ? AND v.visible = 1 AND l.minutes_recent > 0
		GROUP BY g.platform, g.game_id
		ORDER BY minutes DESC, players DESC LIMIT ?`, platform, guildID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChartEntry
	for rows.Next() {
		var c ChartEntry
		if err := scanGame(rows, &c.Game, &c.Minutes, &c.Players); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UncheckedGames returns games whose multiplayer tag hasn't been looked up,
// most widely owned first.
func (s *Store) UncheckedGames(ctx context.Context, platform string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.game_id FROM games g JOIN library l ON l.platform = g.platform AND l.game_id = g.game_id
		WHERE g.platform = ? AND g.checked_at = 0
		GROUP BY g.game_id ORDER BY COUNT(*) DESC LIMIT ?`, platform, limit)
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

// SetMultiplayer records a game's multiplayer tag (nil means unknown).
func (s *Store) SetMultiplayer(ctx context.Context, platform, gameID string, multiplayer *bool, now time.Time) error {
	var v any
	if multiplayer != nil {
		v = boolInt(*multiplayer)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET multiplayer = ?, checked_at = ? WHERE platform = ? AND game_id = ?`,
		v, now.Unix(), platform, gameID)
	return err
}

// DeleteUser removes everything stored about a Discord user.
func (s *Store) DeleteUser(ctx context.Context, discordID string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE discord_id = ?`, discordID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM guild_visibility WHERE discord_id = ?`, discordID)
		return err
	})
}

// HasData reports whether anything is stored about a Discord user.
func (s *Store) HasData(ctx context.Context, discordID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM accounts WHERE discord_id = ?) +
		       (SELECT COUNT(*) FROM guild_visibility WHERE discord_id = ?)`, discordID, discordID).Scan(&n)
	return n > 0, err
}

const gameColumns = `g.platform, g.game_id, g.name, g.image_url, g.store_url, g.multiplayer, g.checked_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanGame(sc scanner, g *Game, extra ...any) error {
	var mp sql.NullInt64
	var checked int64
	dest := append([]any{&g.Platform, &g.GameID, &g.Name, &g.ImageURL, &g.StoreURL, &mp, &checked}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return err
	}
	if mp.Valid {
		b := mp.Int64 == 1
		g.Multiplayer = &b
	}
	g.CheckedAt = fromUnix(checked)
	return nil
}

func scanAccount(sc scanner) (Account, error) {
	var a Account
	var private int
	var linked, synced int64
	err := sc.Scan(&a.DiscordID, &a.Platform, &a.ExternalID, &a.DisplayName, &a.AvatarURL, &a.ProfileURL,
		&private, &linked, &synced, &a.SyncError)
	a.LibraryPrivate = private == 1
	a.LinkedAt = fromUnix(linked)
	a.SyncedAt = fromUnix(synced)
	return a, err
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.TrimSpace(q)) + "%"
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}
