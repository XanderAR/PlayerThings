package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type owned struct {
	id, name        string
	total, recent   int
}

func seedUser(t *testing.T, s *Store, discordID, steamID string, games ...owned) {
	t.Helper()
	ctx := context.Background()
	if err := s.LinkAccount(ctx, Account{DiscordID: discordID, Platform: "steam", ExternalID: steamID, LinkedAt: t0}); err != nil {
		t.Fatal(err)
	}
	var gs []Game
	var es []LibraryEntry
	for _, g := range games {
		gs = append(gs, Game{GameID: g.id, Name: g.name})
		es = append(es, LibraryEntry{GameID: g.id, MinutesTotal: g.total, MinutesRecent: g.recent})
	}
	if err := s.SaveSync(ctx, discordID, "steam", Profile{DisplayName: "p" + discordID}, gs, es, t0); err != nil {
		t.Fatal(err)
	}
}

func TestServerQueriesRespectVisibility(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "a", "s1", owned{"10", "Deep Rock Galactic", 24000, 300}, owned{"20", "Hades II", 600, 600})
	seedUser(t, s, "b", "s2", owned{"10", "Deep Rock Galactic", 9000, 900})
	seedUser(t, s, "c", "s3", owned{"10", "Deep Rock Galactic", 99999, 5000})
	s.SetVisibility(ctx, "g", "a", true, t0)
	s.SetVisibility(ctx, "g", "b", true, t0)
	s.SetVisibility(ctx, "g", "c", false, t0) // hidden

	ranked, owners, err := s.WhoPlays(ctx, "g", "steam", "10", 10)
	if err != nil {
		t.Fatal(err)
	}
	if owners != 2 || len(ranked) != 2 || ranked[0].DiscordID != "a" || ranked[1].DiscordID != "b" {
		t.Fatalf("ranked = %+v owners = %d", ranked, owners)
	}
	if n, _ := s.VisibleMembers(ctx, "g", "steam"); n != 2 {
		t.Fatalf("visible members = %d", n)
	}

	chart, err := s.ServerChart(ctx, "g", "steam", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chart) != 2 || chart[0].Name != "Deep Rock Galactic" || chart[0].Minutes != 1200 || chart[0].Players != 2 {
		t.Fatalf("chart = %+v", chart)
	}

	matches, err := s.SearchServerGames(ctx, "g", "steam", "deep", 5)
	if err != nil || len(matches) != 1 || matches[0].Owners != 2 {
		t.Fatalf("matches = %+v err = %v", matches, err)
	}
	if g, ok, _ := s.FindServerGame(ctx, "g", "steam", "hades ii"); !ok || g.GameID != "20" {
		t.Fatalf("find = %+v %v", g, ok)
	}
	if _, ok, _ := s.FindServerGame(ctx, "other-server", "steam", "deep"); ok {
		t.Fatal("found a game in a server nobody opted into")
	}
	if n, _ := s.MembersOwningAny(ctx, "g", "steam", []string{"20", "999"}); n != 1 {
		t.Fatalf("owning any = %d", n)
	}
}

func TestLikeWildcardsAreLiteral(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "a", "s1", owned{"1", "100% Orange Juice", 10, 0}, owned{"2", "Portal", 10, 0})
	s.SetVisibility(ctx, "g", "a", true, t0)
	m, _ := s.SearchServerGames(ctx, "g", "steam", "%", 5)
	if len(m) != 1 || m[0].GameID != "1" {
		t.Fatalf("matches = %+v", m)
	}
}

func TestCommonGames(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "a", "s1", owned{"10", "DRG", 100, 0}, owned{"20", "Hades", 50, 0}, owned{"30", "Lethal", 10, 0})
	seedUser(t, s, "b", "s2", owned{"10", "DRG", 300, 0}, owned{"30", "Lethal", 5, 0})
	common, err := s.CommonGames(ctx, "steam", []string{"a", "b"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(common) != 2 || common[0].GameID != "10" || common[0].CombinedMinutes != 400 || common[1].GameID != "30" {
		t.Fatalf("common = %+v", common)
	}
	yes := true
	s.SetMultiplayer(ctx, "steam", "10", &yes, t0)
	common, _ = s.CommonGames(ctx, "steam", []string{"a", "b"}, 10)
	if common[0].Multiplayer == nil || !*common[0].Multiplayer || common[1].Multiplayer != nil {
		t.Fatalf("multiplayer flags = %v %v", common[0].Multiplayer, common[1].Multiplayer)
	}
	if ids, _ := s.UncheckedGames(ctx, "steam", 10); len(ids) != 2 {
		t.Fatalf("unchecked = %v", ids)
	}
}

func TestResyncSnapshotsOnlyChanges(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "a", "s1", owned{"10", "DRG", 100, 0}, owned{"20", "Hades", 50, 0})
	next := t0.Add(24 * time.Hour)
	err := s.SaveSync(ctx, "a", "steam", Profile{DisplayName: "renamed"},
		[]Game{{GameID: "10", Name: "DRG"}, {GameID: "20", Name: "Hades"}},
		[]LibraryEntry{{GameID: "10", MinutesTotal: 160, MinutesRecent: 60}, {GameID: "20", MinutesTotal: 50}}, next)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM playtime_snapshots WHERE day = ?`, next.Format("2006-01-02")).Scan(&n)
	if n != 1 {
		t.Fatalf("snapshots on day 2 = %d, want 1 (only the changed game)", n)
	}
	acct, _, _ := s.Account(ctx, "a", "steam")
	if acct.DisplayName != "renamed" || !acct.SyncedAt.Equal(next) {
		t.Fatalf("account = %+v", acct)
	}
	st, _ := s.LibraryStats(ctx, "a", "steam")
	if st.Games != 2 || st.Minutes != 210 || st.RecentMinutes != 60 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestRelinkMovesSteamAccount(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "a", "s1", owned{"10", "DRG", 100, 0})
	// A second Discord account signs in to the same Steam account.
	if err := s.LinkAccount(ctx, Account{DiscordID: "b", Platform: "steam", ExternalID: "s1", LinkedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Account(ctx, "a", "steam"); ok {
		t.Fatal("old link still present")
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM library WHERE discord_id = 'a'`).Scan(&n)
	if n != 0 {
		t.Fatalf("old library rows = %d", n)
	}
}

func TestDeleteUserRemovesEverything(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedUser(t, s, "a", "s1", owned{"10", "DRG", 100, 5})
	s.SetVisibility(ctx, "g", "a", true, t0)
	if has, _ := s.HasData(ctx, "a"); !has {
		t.Fatal("expected data before delete")
	}
	if err := s.DeleteUser(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"accounts", "library", "playtime_snapshots", "guild_visibility"} {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE discord_id = 'a'`).Scan(&n)
		if n != 0 {
			t.Errorf("%s still has %d rows", table, n)
		}
	}
	if has, _ := s.HasData(ctx, "a"); has {
		t.Fatal("HasData after delete")
	}
}

func TestFeedsAndUsage(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	f := Feed{GuildID: "g", Kind: FeedRadar, ChannelID: "c", WebhookID: "w", WebhookToken: "tok", CreatedBy: "a", CreatedAt: t0}
	if err := s.SetFeed(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Feed(ctx, "g", FeedRadar)
	if err != nil || !ok || got.WebhookToken != "tok" {
		t.Fatalf("feed = %+v ok=%v err=%v", got, ok, err)
	}
	s.MarkRadarPosted(ctx, "g", "https://player.gg/x", t0)
	s.MarkRadarPosted(ctx, "g", "https://player.gg/x", t0) // idempotent
	if done, _ := s.RadarPosted(ctx, "g", "https://player.gg/x"); !done {
		t.Fatal("radar post not recorded")
	}
	s.CountUsage(ctx, UsageClick, "whoplays", "g", t0)
	s.CountUsage(ctx, UsageClick, "whoplays", "g", t0)
	s.CountUsage(ctx, UsageCommand, "profile", "", t0)
	rows, _ := s.UsageSince(ctx, "2026-11-01")
	if len(rows) != 2 {
		t.Fatalf("usage rows = %+v", rows)
	}
	for _, r := range rows {
		if r.Kind == UsageClick && (r.Count != 2 || r.Servers != 1) {
			t.Fatalf("click row = %+v", r)
		}
		if r.Kind == UsageCommand && r.Servers != 0 {
			t.Fatalf("DM command counted as a server: %+v", r)
		}
	}
}
