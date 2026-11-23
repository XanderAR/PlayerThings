package syncer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"playradar/internal/steam"
	"playradar/internal/store"
)

// fakeChecker knows the listed games; anything else has no store page.
type fakeChecker map[string]bool

func (f fakeChecker) GameDetails(_ context.Context, id string) (steam.Details, bool, error) {
	v, ok := f[id]
	if !ok {
		return steam.Details{}, false, nil
	}
	return steam.Details{Multiplayer: v, Genres: []string{"Action"}, Reviews: 1000, ReleaseYear: 2020}, true, nil
}

// failingChecker fails every lookup and counts the calls.
type failingChecker struct{ calls *int }

func (f failingChecker) GameDetails(context.Context, string) (steam.Details, bool, error) {
	*f.calls++
	return steam.Details{}, false, errors.New("429 Too Many Requests")
}

func TestSteamSyncAndMultiplayerWarmup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "GetPlayerSummaries"):
			w.Write([]byte(`{"response":{"players":[{"steamid":"76561198000000001","personaname":"Mara","avatarfull":"https://a/1.jpg","profileurl":"https://steamcommunity.com/id/mara/"}]}}`))
		case strings.Contains(r.URL.Path, "GetOwnedGames"):
			w.Write([]byte(`{"response":{"game_count":2,"games":[
				{"appid":548430,"name":"Deep Rock Galactic","playtime_forever":24720,"playtime_2weeks":360},
				{"appid":1145360,"name":"Hades II","playtime_forever":5760}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	st.LinkAccount(ctx, store.Account{DiscordID: "100", Platform: "steam", ExternalID: "76561198000000001", LinkedAt: now})

	client := steam.New("k", 0)
	client.APIBase = srv.URL
	s := &Syncer{
		Store:     st,
		Providers: map[string]Provider{"steam": SteamProvider{Client: client}},
		Checker:   fakeChecker{"548430": true, "1145360": false},
		Interval:  12 * time.Hour,
		Now:       func() time.Time { return now },
	}

	// Never synced, so it's due: one tick syncs it and warms multiplayer tags.
	s.tick(ctx)

	acct, _, _ := st.Account(ctx, "100", "steam")
	if acct.DisplayName != "Mara" || !acct.SyncedAt.Equal(now) || acct.LibraryPrivate {
		t.Fatalf("account = %+v", acct)
	}
	stats, _ := st.LibraryStats(ctx, "100", "steam")
	if stats.Games != 2 || stats.Minutes != 30480 {
		t.Fatalf("stats = %+v", stats)
	}
	g, _, _ := st.Game(ctx, "steam", "548430")
	if g.Multiplayer == nil || !*g.Multiplayer || !strings.HasSuffix(g.ImageURL, "/548430/header.jpg") ||
		g.Reviews != 1000 || g.ReleaseYear != 2020 || len(g.Genres) != 1 || g.DetailsAt.IsZero() {
		t.Fatalf("game = %+v", g)
	}
	if ids, _ := st.UndetailedGames(ctx, "steam", 10); len(ids) != 0 {
		t.Fatalf("undetailed after warmup = %v", ids)
	}

	// Just synced, so the next tick leaves it alone.
	due, _ := st.DueForSync(ctx, "steam", now.Add(-s.Interval), now.Add(-retryFailedAfter), 10)
	if len(due) != 0 {
		t.Fatalf("due right after sync = %d", len(due))
	}
}

// A failed sync is retried after retryFailedAfter, not after the full interval.
func TestFailedSyncRetriesSoon(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "sync.db"))
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	st.LinkAccount(ctx, store.Account{DiscordID: "100", Platform: "steam", ExternalID: "76561198000000001", LinkedAt: now})

	s := &Syncer{
		Store:     st,
		Providers: map[string]Provider{"steam": SteamProvider{Client: steam.New("", 0)}}, // no key: every sync fails
		Interval:  12 * time.Hour,
		Now:       func() time.Time { return now },
	}
	s.tick(ctx)
	if acct, _, _ := st.Account(ctx, "100", "steam"); acct.SyncError == "" {
		t.Fatalf("expected a sync error, got %+v", acct)
	}

	due := func(at time.Time) int {
		d, _ := st.DueForSync(ctx, "steam", at.Add(-s.Interval), at.Add(-retryFailedAfter), 10)
		return len(d)
	}
	if n := due(now.Add(time.Minute)); n != 0 {
		t.Fatalf("due a minute after failing = %d", n)
	}
	if n := due(now.Add(retryFailedAfter + time.Second)); n != 1 {
		t.Fatalf("due after the retry delay = %d", n)
	}
}

// Games without a store page are stored as looked up; a failing store stops
// the batch after one call instead of hammering it.
func TestDetailsWarmup(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "sync.db"))
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	st.LinkAccount(ctx, store.Account{DiscordID: "100", Platform: "steam", ExternalID: "76561198000000001", LinkedAt: now})
	st.SaveSync(ctx, "100", "steam", store.Profile{DisplayName: "Mara"},
		[]store.Game{{GameID: "1", Name: "Known"}, {GameID: "2", Name: "Delisted"}, {GameID: "3", Name: "Other"}},
		[]store.LibraryEntry{{GameID: "1", MinutesTotal: 300}, {GameID: "2", MinutesTotal: 200}, {GameID: "3", MinutesTotal: 100}}, now)

	calls := 0
	s := &Syncer{Store: st, Checker: failingChecker{&calls}, Interval: 12 * time.Hour, Now: func() time.Time { return now }}
	s.tick(ctx)
	if calls != 1 {
		t.Fatalf("lookups after a failure = %d, want 1", calls)
	}
	if ids, _ := st.UndetailedGames(ctx, "steam", 10); len(ids) != 3 {
		t.Fatalf("undetailed after failing = %v", ids)
	}

	s.Checker = fakeChecker{"1": false, "3": true}
	s.tick(ctx)
	if ids, _ := st.UndetailedGames(ctx, "steam", 10); len(ids) != 0 {
		t.Fatalf("undetailed after warmup = %v", ids)
	}
	if g, _, _ := st.Game(ctx, "steam", "2"); g.Multiplayer != nil || g.DetailsAt.IsZero() || g.Genres != nil {
		t.Fatalf("delisted game = %+v", g)
	}
	if mp, err := s.CheckGame(ctx, "3"); err != nil || mp == nil || !*mp {
		t.Fatalf("CheckGame = %v, %v", mp, err)
	}
}

func TestSyncUnlinkedAccount(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "sync.db"))
	defer st.Close()
	s := &Syncer{Store: st, Providers: map[string]Provider{"steam": SteamProvider{}}}
	if _, _, err := s.Sync(context.Background(), "nobody", "steam"); err == nil {
		t.Fatal("expected an error for an unlinked account")
	}
}
