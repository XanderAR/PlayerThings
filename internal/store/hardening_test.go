package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Software in a Steam library (Wallpaper Engine, Blender) has playtime but
// isn't a game, so it stays out of rankings, totals, backlogs and boards.
func TestSoftwareIsLeftOutOfGameStats(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "100", "s100",
		owned{"431960", "Wallpaper Engine", 45000, 600},
		owned{"548430", "Deep Rock Galactic", 6000, 60},
		owned{"365670", "Blender", 0, 0},
		owned{"753640", "Outer Wilds", 0, 0})
	seedUser(t, s, "200", "s200", owned{"431960", "Wallpaper Engine", 100, 100}, owned{"548430", "Deep Rock Galactic", 30, 0})
	for _, id := range []string{"100", "200"} {
		s.SetVisibility(ctx, "G", id, true, t0)
	}
	s.SetGameDetails(ctx, "steam", "431960", GameDetails{Genres: []string{"Casual", "Indie", "Animation & Modeling", "Utilities"}}, t0)
	s.SetGameDetails(ctx, "steam", "365670", GameDetails{Genres: []string{"Animation & Modeling", "Video Production"}}, t0)
	s.SetGameDetails(ctx, "steam", "548430", GameDetails{Genres: []string{"Action"}}, t0)

	top, _ := s.TopGames(ctx, "100", "steam", false, 10)
	if len(top) != 1 || top[0].GameID != "548430" {
		t.Fatalf("top games = %+v", top)
	}
	stats, _ := s.LibraryStats(ctx, "100", "steam")
	if stats.Games != 2 || stats.Minutes != 6000 {
		t.Fatalf("stats = %+v, want 2 games (DRG, Outer Wilds) and 6000 minutes", stats)
	}
	unplayed, _ := s.Unplayed(ctx, "100", "steam")
	if len(unplayed) != 1 || unplayed[0].GameID != "753640" {
		t.Fatalf("unplayed = %+v", unplayed)
	}
	board, _ := s.Leaderboard(ctx, "G", "steam", "hours", 10)
	if len(board) != 2 || board[0].Value != 6000 || board[1].Value != 30 {
		t.Fatalf("hours board = %+v", board)
	}
	chart, _ := s.ServerChart(ctx, "G", "steam", 10)
	if len(chart) != 1 || chart[0].GameID != "548430" {
		t.Fatalf("chart = %+v", chart)
	}
	crowns, _, _ := s.Crowns(ctx, "G", "steam", "100", 1, 10)
	for _, c := range crowns {
		if c.Game.GameID == "431960" {
			t.Fatal("a crown for Wallpaper Engine")
		}
	}
}

// Databases from before the software column get it filled in on open.
func TestSoftwareBackfillOnOpen(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "100", "s100", owned{"431960", "Wallpaper Engine", 45000, 0})
	s.SetGameDetails(ctx, "steam", "431960", GameDetails{Genres: []string{"Casual", "Utilities"}}, t0)
	s.db.Exec(`UPDATE games SET software = 0`) // as an old database would have it
	if err := migrate(s.db); err != nil {
		t.Fatal(err)
	}
	if top, _ := s.TopGames(ctx, "100", "steam", false, 10); len(top) != 0 {
		t.Fatalf("software still ranked after backfill: %+v", top)
	}
}

// A sync that fetched the old Steam account must not overwrite the new link.
func TestSaveSyncRefusesAfterRelink(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "100", "OLD", owned{"548430", "Deep Rock Galactic", 600, 0})
	if err := s.LinkAccount(ctx, Account{DiscordID: "100", Platform: "steam", ExternalID: "NEW", LinkedAt: t0}); err != nil {
		t.Fatal(err)
	}
	err := s.SaveSync(ctx, "100", "steam", Profile{ExternalID: "OLD", DisplayName: "old name"},
		[]Game{{GameID: "730", Name: "Counter-Strike 2"}}, []LibraryEntry{{GameID: "730", MinutesTotal: 9999}}, t0)
	if !errors.Is(err, ErrRelinked) {
		t.Fatalf("err = %v, want ErrRelinked", err)
	}
	acct, _, _ := s.Account(ctx, "100", "steam")
	if acct.DisplayName == "old name" {
		t.Fatal("old account's profile was saved over the new link")
	}
}

// Someone who only voted on a moment can still delete their data.
func TestHasDataCountsVotesAndUnlinkClearsFeedCreator(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.Vote(ctx, "G", "launch", "300", "playing", t0); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasData(ctx, "300"); !has {
		t.Fatal("a vote is stored data")
	}
	s.SetFeed(ctx, Feed{GuildID: "G", Kind: FeedRadar, ChannelID: "C", WebhookID: "w", WebhookToken: "t", CreatedBy: "300", CreatedAt: t0})
	if err := s.DeleteUser(ctx, "300"); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasData(ctx, "300"); has {
		t.Fatal("data left after unlink")
	}
	if f, ok, _ := s.Feed(ctx, "G", FeedRadar); !ok || f.CreatedBy != "" {
		t.Fatalf("feed after unlink = %+v (should stay, without who made it)", f)
	}
}

func TestIsSoftware(t *testing.T) {
	for genres, want := range map[string]bool{
		"Action,Indie":                         false,
		"Casual,Indie,Utilities":               true,
		"Animation & Modeling":                 true,
		"Strategy,Simulation":                  false,
		"Design & Illustration,Web Publishing": true,
	} {
		if got := IsSoftware(splitGenres(genres)); got != want {
			t.Errorf("%s: got %v", genres, got)
		}
	}
}

// Typing "raft" offers Raft before Minecraft Dungeons, even when Minecraft is
// more widely owned, and "rock" finds Deep Rock by its second word.
func TestSearchRanksNameStartsFirst(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "100", "s100", owned{"1", "Minecraft Dungeons", 60, 0}, owned{"2", "Raft", 60, 0}, owned{"3", "Deep Rock Galactic", 60, 0}, owned{"4", "Rocket League", 60, 0})
	seedUser(t, s, "200", "s200", owned{"1", "Minecraft Dungeons", 60, 0})
	for _, id := range []string{"100", "200"} {
		s.SetVisibility(ctx, "G", id, true, t0)
	}
	got := func(q string) string {
		m, _ := s.SearchServerGames(ctx, "G", "steam", q, 10)
		var names []string
		for _, g := range m {
			names = append(names, g.Name)
		}
		return strings.Join(names, ", ")
	}
	if g := got("raft"); g != "Raft, Minecraft Dungeons" {
		t.Errorf("raft = %s", g)
	}
	if g := got("rock"); g != "Rocket League, Deep Rock Galactic" {
		t.Errorf("rock = %s", g)
	}
}
