package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"playradar/internal/steam"
	"playradar/internal/store"
)

// scriptedProvider returns one canned result per call.
type scriptedProvider struct {
	results []Result
	calls   int
}

func (p *scriptedProvider) Platform() string { return "steam" }
func (p *scriptedProvider) Fetch(context.Context, string) (Result, error) {
	r := p.results[min(p.calls, len(p.results)-1)]
	p.calls++
	return r, nil
}

func newHardeningSyncer(t *testing.T, p Provider) (*Syncer, *store.Store, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	st.LinkAccount(context.Background(), store.Account{DiscordID: "100", Platform: "steam", ExternalID: "S1", LinkedAt: now})
	return &Syncer{Store: st, Providers: map[string]Provider{"steam": p}, Interval: 12 * time.Hour,
		Now: func() time.Time { return now }}, st, &now
}

// One empty reply from Steam keeps the library; a second one in a row means
// the library really went private.
func TestEmptyLibraryNeedsTwoRepliesToWipe(t *testing.T) {
	full := Result{Profile: store.Profile{DisplayName: "Mara"},
		Games:   []store.Game{{GameID: "548430", Name: "Deep Rock Galactic"}},
		Entries: []store.LibraryEntry{{GameID: "548430", MinutesTotal: 600}}}
	empty := Result{Profile: store.Profile{DisplayName: "Mara", Private: true}}
	p := &scriptedProvider{results: []Result{full, empty, empty}}
	s, st, _ := newHardeningSyncer(t, p)
	ctx := context.Background()

	if _, _, err := s.Sync(ctx, "100", "steam"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Sync(ctx, "100", "steam"); err == nil {
		t.Fatal("first empty reply should be held back as an error")
	}
	if stats, _ := st.LibraryStats(ctx, "100", "steam"); stats.Games != 1 {
		t.Fatalf("library wiped by one empty reply: %+v", stats)
	}
	if _, _, err := s.Sync(ctx, "100", "steam"); err != nil {
		t.Fatal(err)
	}
	acct, _, _ := st.Account(ctx, "100", "steam")
	if stats, _ := st.LibraryStats(ctx, "100", "steam"); stats.Games != 0 || !acct.LibraryPrivate {
		t.Fatalf("second empty reply should mark it private: %+v %+v", stats, acct)
	}
}

// badDataChecker fails one app with unreadable data and knows the rest.
type badDataChecker struct{ bad string }

func (c badDataChecker) GameDetails(_ context.Context, id string) (steam.Details, bool, error) {
	if id == c.bad {
		return steam.Details{}, false, fmt.Errorf("%w: test", steam.ErrBadData)
	}
	return steam.Details{Genres: []string{"Action"}, Reviews: 10}, true, nil
}

// One app with a broken store page doesn't stop the details warmup.
func TestWarmupSkipsBrokenApps(t *testing.T) {
	p := &scriptedProvider{results: []Result{{Profile: store.Profile{DisplayName: "Mara"},
		Games:   []store.Game{{GameID: "1", Name: "Broken"}, {GameID: "2", Name: "Fine"}},
		Entries: []store.LibraryEntry{{GameID: "1", MinutesTotal: 900}, {GameID: "2", MinutesTotal: 10}}}}}
	s, st, _ := newHardeningSyncer(t, p)
	s.Checker = badDataChecker{bad: "1"}
	s.tick(context.Background())
	if n, _ := st.UndetailedCount(context.Background(), "100", "steam"); n != 0 {
		t.Fatalf("%d games still waiting for details; the broken one blocked the rest", n)
	}
}

// A panic in a tick is logged, not fatal.
func TestTickPanicIsRecovered(t *testing.T) {
	s := &Syncer{Providers: map[string]Provider{"steam": &scriptedProvider{}}} // nil Store panics
	s.safeTick(context.Background())
}
