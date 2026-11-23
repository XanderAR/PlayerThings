package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// seedServer links four users. a, b and d are visible in server g; c is hidden.
func seedServer(t *testing.T) *Store {
	t.Helper()
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "a", "s1",
		owned{"10", "Deep Rock Galactic", 24000, 300},
		owned{"20", "Hades II", 600, 600},
		owned{"30", "Factorio", 0, 0}) // owned, never played
	seedUser(t, s, "b", "s2",
		owned{"10", "Deep Rock Galactic", 9000, 900},
		owned{"20", "Hades II", 6000, 0},
		owned{"40", "Balatro", 3000, 120})
	seedUser(t, s, "c", "s3", owned{"10", "Deep Rock Galactic", 99999, 5000}) // hidden
	seedUser(t, s, "d", "s4",
		owned{"20", "Hades II", 6000, 50}, // ties b, so nobody holds the Hades II crown
		owned{"40", "Balatro", 30, 0})
	for _, id := range []string{"a", "b", "d"} {
		s.SetVisibility(ctx, "g", id, true, t0)
	}
	s.SetVisibility(ctx, "g", "c", false, t0)
	return s
}

func TestGameDetailsRoundTrip(t *testing.T) {
	s := seedServer(t)
	ctx := context.Background()

	// Most-played first; Factorio has no playtime but is still in a library.
	ids, err := s.UndetailedGames(ctx, "steam", 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"10", "20", "40", "30"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("undetailed = %v, want %v", ids, want)
	}
	if n, _ := s.UndetailedCount(ctx, "a", "steam"); n != 2 {
		t.Fatalf("undetailed count for a = %d, want 2 (played games only)", n)
	}

	yes := true
	if err := s.SetGameDetails(ctx, "steam", "10", GameDetails{Multiplayer: &yes, Genres: []string{"Action", "Co,op"}, Reviews: 304804, ReleaseYear: 2020}, t0); err != nil {
		t.Fatal(err)
	}
	g, ok, err := s.Game(ctx, "steam", "10")
	if err != nil || !ok {
		t.Fatalf("game ok=%v err=%v", ok, err)
	}
	if g.Multiplayer == nil || !*g.Multiplayer || g.Reviews != 304804 || g.ReleaseYear != 2020 ||
		!reflect.DeepEqual(g.Genres, []string{"Action", "Co op"}) || !g.DetailsAt.Equal(t0) || !g.CheckedAt.Equal(t0) {
		t.Fatalf("game = %+v", g)
	}
	// Unknown store page: stored as looked up so it isn't retried.
	if err := s.SetGameDetails(ctx, "steam", "30", GameDetails{}, t0); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.UndetailedGames(ctx, "steam", 10)
	if want := []string{"20", "40"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("undetailed after = %v, want %v", ids, want)
	}
	if n, _ := s.UndetailedCount(ctx, "a", "steam"); n != 1 {
		t.Fatalf("undetailed count for a = %d, want 1", n)
	}

	// Details flow through queries that embed a Game.
	top, _ := s.TopGames(ctx, "a", "steam", false, 1)
	if len(top) != 1 || top[0].Reviews != 304804 {
		t.Fatalf("top = %+v", top)
	}
	pg, ok, err := s.Owned(ctx, "b", "steam", "10")
	if err != nil || !ok || pg.MinutesTotal != 9000 || pg.Name != "Deep Rock Galactic" || pg.ReleaseYear != 2020 {
		t.Fatalf("owned = %+v ok=%v err=%v", pg, ok, err)
	}
	if _, ok, _ := s.Owned(ctx, "a", "steam", "40"); ok {
		t.Fatal("a doesn't own Balatro")
	}
}

func TestLeaderboard(t *testing.T) {
	s := seedServer(t)
	ctx := context.Background()
	cases := map[string][]MemberValue{
		"hours":  {{DiscordID: "a", Value: 24600}, {DiscordID: "b", Value: 18000}, {DiscordID: "d", Value: 6030}},
		"recent": {{DiscordID: "b", Value: 1020}, {DiscordID: "a", Value: 900}, {DiscordID: "d", Value: 50}},
		"games":  {{DiscordID: "b", Value: 3}, {DiscordID: "a", Value: 2}, {DiscordID: "d", Value: 2}},
	}
	for kind, want := range cases {
		got, err := s.Leaderboard(ctx, "g", "steam", kind, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("%s = %+v", kind, got)
		}
		for i := range want {
			if got[i].DiscordID != want[i].DiscordID || got[i].Value != want[i].Value || got[i].DisplayName != "p"+want[i].DiscordID {
				t.Fatalf("%s[%d] = %+v, want %+v", kind, i, got[i], want[i])
			}
		}
	}
	if _, err := s.Leaderboard(ctx, "g", "steam", "nope", 10); err == nil {
		t.Fatal("expected an error for an unknown leaderboard")
	}
	// A private library drops out.
	s.SaveSync(ctx, "a", "steam", Profile{DisplayName: "pa", Private: true}, nil, nil, t0)
	got, _ := s.Leaderboard(ctx, "g", "steam", "hours", 10)
	if len(got) != 2 || got[0].DiscordID != "b" {
		t.Fatalf("after a went private = %+v", got)
	}
}

func TestCrowns(t *testing.T) {
	s := seedServer(t)
	ctx := context.Background()

	// a leads Deep Rock (hidden c doesn't count); b leads Balatro; Hades II is tied.
	crowns, total, err := s.Crowns(ctx, "g", "steam", "a", 60, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(crowns) != 1 || crowns[0].Game.GameID != "10" || crowns[0].Minutes != 24000 ||
		crowns[0].RunnerUpID != "b" || crowns[0].RunnerUpMinutes != 9000 {
		t.Fatalf("a crowns = %+v total=%d", crowns, total)
	}
	crowns, total, _ = s.Crowns(ctx, "g", "steam", "b", 60, 10)
	if total != 1 || crowns[0].Game.Name != "Balatro" || crowns[0].RunnerUpID != "d" {
		t.Fatalf("b crowns = %+v total=%d", crowns, total)
	}
	if crowns, total, _ := s.Crowns(ctx, "g", "steam", "d", 60, 10); total != 0 || len(crowns) != 0 {
		t.Fatalf("d should hold no crowns (tie on Hades II): %+v", crowns)
	}
	// The minimum filters out small leads; limit trims but total doesn't.
	if _, total, _ := s.Crowns(ctx, "g", "steam", "b", 5000, 10); total != 0 {
		t.Fatalf("Balatro at 50 h is under a 5000-minute minimum? total=%d", total)
	}
	if crowns, total, _ := s.Crowns(ctx, "g", "steam", "a", 60, 0); total != 1 || len(crowns) != 1 {
		t.Fatalf("limit 0 = %+v total=%d", crowns, total)
	}

	board, err := s.CrownBoard(ctx, "g", "steam", 60, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(board) != 2 || board[0].Value != 1 || board[1].Value != 1 {
		t.Fatalf("crown board = %+v", board)
	}

	// Breaking the tie hands d the Hades II crown.
	s.SaveSync(ctx, "d", "steam", Profile{DisplayName: "pd"},
		[]Game{{GameID: "20", Name: "Hades II"}, {GameID: "40", Name: "Balatro"}},
		[]LibraryEntry{{GameID: "20", MinutesTotal: 6001}, {GameID: "40", MinutesTotal: 30}}, t0)
	crowns, _, _ = s.Crowns(ctx, "g", "steam", "d", 60, 10)
	if len(crowns) != 1 || crowns[0].Game.GameID != "20" || crowns[0].RunnerUpMinutes != 6000 {
		t.Fatalf("d crowns after = %+v", crowns)
	}
}

func TestCompare(t *testing.T) {
	s := seedServer(t)
	ctx := context.Background()
	c, err := s.Compare(ctx, "steam", "a", "b", 10)
	if err != nil {
		t.Fatal(err)
	}
	// a owns 10, 20, 30; b owns 10, 20, 40.
	if c.Shared != 2 || c.AOnly != 1 || c.BOnly != 1 {
		t.Fatalf("comparison = %+v", c)
	}
	// Deep Rock: min(24000, 9000) = 9000; Hades II: min(600, 6000) = 600.
	if len(c.Top) != 2 || c.Top[0].Game.GameID != "10" || c.Top[0].AMinutes != 24000 || c.Top[0].BMinutes != 9000 ||
		c.Top[1].Game.GameID != "20" {
		t.Fatalf("top = %+v", c.Top)
	}
	if c, _ := s.Compare(ctx, "steam", "a", "nobody", 10); c.Shared != 0 || c.AOnly != 3 || c.BOnly != 0 || len(c.Top) != 0 {
		t.Fatalf("against nobody = %+v", c)
	}
}

func TestGenreMinutes(t *testing.T) {
	s := seedServer(t)
	ctx := context.Background()
	s.SetGameDetails(ctx, "steam", "10", GameDetails{Genres: []string{"Action", "Indie"}}, t0)
	s.SetGameDetails(ctx, "steam", "20", GameDetails{Genres: []string{"Action", "RPG"}}, t0)
	s.SetGameDetails(ctx, "steam", "30", GameDetails{Genres: []string{"Strategy"}}, t0) // unplayed

	got, err := s.GenreMinutes(ctx, "a", "steam", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []GenreShare{{"Action", 24600, 2}, {"Indie", 24000, 1}, {"RPG", 600, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("genres = %+v, want %+v", got, want)
	}
	if got, _ := s.GenreMinutes(ctx, "a", "steam", 1); len(got) != 1 || got[0].Genre != "Action" {
		t.Fatalf("limited = %+v", got)
	}
}

// A database created before game details existed gains the new columns.
func TestMigrateAddsGameDetailColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE games (
		platform TEXT NOT NULL, game_id TEXT NOT NULL, name TEXT NOT NULL,
		image_url TEXT NOT NULL DEFAULT '', store_url TEXT NOT NULL DEFAULT '',
		multiplayer INTEGER, checked_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (platform, game_id));
		INSERT INTO games (platform, game_id, name, checked_at) VALUES ('steam', '10', 'Deep Rock Galactic', 5);`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	g, ok, err := s.Game(context.Background(), "steam", "10")
	s.Close()
	if err != nil || !ok || g.Name != "Deep Rock Galactic" || g.Genres != nil || !g.DetailsAt.IsZero() || g.CheckedAt != time.Unix(5, 0) {
		t.Fatalf("game = %+v ok=%v err=%v", g, ok, err)
	}
	// Opening again is a no-op.
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}
