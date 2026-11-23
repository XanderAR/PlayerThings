package syncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"playradar/internal/steam"
	"playradar/internal/store"
)

type fakeChecker map[string]bool

func (f fakeChecker) IsMultiplayer(_ context.Context, id string) (bool, bool, error) {
	v, ok := f[id]
	return v, ok, nil
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
	if g.Multiplayer == nil || !*g.Multiplayer || !strings.HasSuffix(g.ImageURL, "/548430/header.jpg") {
		t.Fatalf("game = %+v", g)
	}
	if ids, _ := st.UncheckedGames(ctx, "steam", 10); len(ids) != 0 {
		t.Fatalf("unchecked after warmup = %v", ids)
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

func TestSyncUnlinkedAccount(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "sync.db"))
	defer st.Close()
	s := &Syncer{Store: st, Providers: map[string]Provider{"steam": SteamProvider{}}}
	if _, _, err := s.Sync(context.Background(), "nobody", "steam"); err == nil {
		t.Fatal("expected an error for an unlinked account")
	}
}
