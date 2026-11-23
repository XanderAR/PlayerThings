// Package syncer pulls linked accounts' libraries from their platforms on a
// schedule. Each platform is a Provider, so Xbox, PlayStation or
// RetroAchievements can be added next to Steam.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"playradar/internal/steam"
	"playradar/internal/store"
)

// ErrBusy means the account is already being synced.
var ErrBusy = errors.New("sync already running for this account")

// emptyLibrary is the sync error recorded the first time Steam sends no games
// for a library that had some.
const emptyLibrary = "steam sent an empty library; keeping the last one until it does so twice"

// Result is one fetch from a platform.
type Result struct {
	Profile store.Profile
	Games   []store.Game
	Entries []store.LibraryEntry
}

// Provider fetches one platform's library for an account.
type Provider interface {
	Platform() string
	Fetch(ctx context.Context, externalID string) (Result, error)
}

// DetailsChecker looks up a Steam game's store details: multiplayer support,
// genres, review count and release year.
type DetailsChecker interface {
	GameDetails(ctx context.Context, appID string) (steam.Details, bool, error)
}

// detailsPerTick caps store lookups per minute. The store client spaces calls
// about 1.6 s apart, so this fits in a tick and stays under the store's limits.
const detailsPerTick = 25

// Syncer refreshes libraries in the background.
type Syncer struct {
	Store     *store.Store
	Providers map[string]Provider
	Checker   DetailsChecker // optional
	Interval  time.Duration  // how stale an account may get
	Log       *slog.Logger
	Now       func() time.Time

	running sync.Map
}

// Sync refreshes one account now and returns its profile and game count.
func (s *Syncer) Sync(ctx context.Context, discordID, platform string) (store.Profile, int, error) {
	p, ok := s.Providers[platform]
	if !ok {
		return store.Profile{}, 0, fmt.Errorf("no provider for %s", platform)
	}
	key := discordID + "/" + platform
	if _, busy := s.running.LoadOrStore(key, true); busy {
		return store.Profile{}, 0, ErrBusy
	}
	defer s.running.Delete(key)

	acct, found, err := s.Store.Account(ctx, discordID, platform)
	if err != nil || !found {
		return store.Profile{}, 0, errors.Join(err, errIf(!found, "account not linked"))
	}
	res, err := p.Fetch(ctx, acct.ExternalID)
	if err != nil {
		_ = s.Store.MarkSyncError(ctx, discordID, platform, err.Error(), s.now())
		return store.Profile{}, 0, err
	}
	// Steam sometimes answers with an empty library for a moment. Only treat
	// it as private once it happens twice in a row, so a blip doesn't wipe a
	// library that loaded before.
	if res.Profile.Private && !acct.LibraryPrivate && acct.SyncError != emptyLibrary {
		if stats, err := s.Store.LibraryStats(ctx, discordID, platform); err == nil && stats.Games > 0 {
			_ = s.Store.MarkSyncError(ctx, discordID, platform, emptyLibrary, s.now())
			return store.Profile{}, 0, errors.New(emptyLibrary)
		}
	}
	res.Profile.ExternalID = acct.ExternalID
	if err := s.Store.SaveSync(ctx, discordID, platform, res.Profile, res.Games, res.Entries, s.now()); err != nil {
		return store.Profile{}, 0, err
	}
	s.log().Info("synced", "platform", platform, "games", len(res.Entries), "private", res.Profile.Private)
	return res.Profile, len(res.Entries), nil
}

// SyncSoon refreshes an account in the background.
func (s *Syncer) SyncSoon(discordID, platform string) {
	go func() {
		defer s.recoverPanic("background sync")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, _, err := s.Sync(ctx, discordID, platform); err != nil && !errors.Is(err, ErrBusy) {
			s.log().Warn("background sync failed", "platform", platform, "err", err)
		}
	}()
}

// CheckGame looks up and stores a Steam game's details, and returns its
// multiplayer tag (nil when the store has no page for it).
func (s *Syncer) CheckGame(ctx context.Context, appID string) (*bool, error) {
	if s.Checker == nil {
		return nil, nil
	}
	d, known, err := s.Checker.GameDetails(ctx, appID)
	if err != nil {
		return nil, err
	}
	var gd store.GameDetails
	if known {
		mp := d.Multiplayer
		gd = store.GameDetails{Multiplayer: &mp, Genres: d.Genres, Reviews: d.Reviews, ReleaseYear: d.ReleaseYear}
	}
	// Unknown games are stored with empty details so they aren't retried forever.
	return gd.Multiplayer, s.Store.SetGameDetails(ctx, "steam", appID, gd, s.now())
}

// retryFailedAfter is how long a failed sync waits before it's tried again,
// instead of waiting the full sync interval.
const retryFailedAfter = 10 * time.Minute

// Run syncs stale accounts and fills in game details until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.safeTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Syncer) tick(ctx context.Context) {
	cutoff := s.now().Add(-s.Interval)
	retryCutoff := s.now().Add(-retryFailedAfter)
	for platform := range s.Providers {
		due, err := s.Store.DueForSync(ctx, platform, cutoff, retryCutoff, 30)
		if err != nil {
			s.log().Error("list accounts to sync", "err", err)
			continue
		}
		for _, a := range due {
			if ctx.Err() != nil {
				return
			}
			_, _, err := s.Sync(ctx, a.DiscordID, platform)
			if errors.Is(err, steam.ErrBudget) {
				s.log().Warn("steam budget used up; pausing sync until tomorrow")
				return
			}
			if errors.Is(err, steam.ErrNoKey) {
				break // nothing can sync until the key is set; skip quietly
			}
			if err != nil && !errors.Is(err, ErrBusy) {
				s.log().Warn("sync failed", "platform", platform, "err", err)
			}
		}
	}
	if s.Checker == nil {
		return
	}
	ids, err := s.Store.UndetailedGames(ctx, "steam", detailsPerTick)
	if err != nil {
		s.log().Error("list games without details", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.CheckGame(ctx, id); err != nil {
			if errors.Is(err, steam.ErrBadData) {
				// This app's store page is broken: store empty details so the
				// warmup moves past it instead of stopping on it every tick.
				s.log().Warn("unreadable game details; skipping", "app", id, "err", err)
				_ = s.Store.SetGameDetails(ctx, "steam", id, store.GameDetails{}, s.now())
				continue
			}
			// Likely the store rate limit; try the rest next tick.
			s.log().Warn("game details lookup failed; pausing until next tick", "app", id, "err", err)
			return
		}
	}
}

// safeTick runs one tick, logging a panic instead of taking the process down.
func (s *Syncer) safeTick(ctx context.Context) {
	defer s.recoverPanic("sync tick")
	s.tick(ctx)
}

func (s *Syncer) recoverPanic(what string) {
	if p := recover(); p != nil {
		s.log().Error("panic", "in", what, "panic", p, "stack", string(debug.Stack()))
	}
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Syncer) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func errIf(cond bool, msg string) error {
	if cond {
		return errors.New(msg)
	}
	return nil
}

// SteamProvider syncs Steam libraries through the official Web API.
type SteamProvider struct {
	Client *steam.Client
}

// Platform names the provider.
func (SteamProvider) Platform() string { return "steam" }

// Fetch returns the profile and owned games for a SteamID64.
func (p SteamProvider) Fetch(ctx context.Context, steamID string) (Result, error) {
	player, err := p.Client.PlayerSummary(ctx, steamID)
	if err != nil {
		return Result{}, err
	}
	lib, err := p.Client.OwnedGames(ctx, steamID)
	if err != nil {
		return Result{}, err
	}
	res := Result{Profile: store.Profile{
		DisplayName: player.PersonaName,
		AvatarURL:   player.AvatarFull,
		ProfileURL:  player.ProfileURL,
		Private:     lib.Private,
	}}
	for _, g := range lib.Games {
		id := steam.AppIDString(g.AppID)
		res.Games = append(res.Games, store.Game{
			GameID: id, Name: g.Name, ImageURL: steam.HeaderImage(id), StoreURL: steam.StoreURL(id),
		})
		var last time.Time
		if g.LastPlayed > 0 {
			last = time.Unix(g.LastPlayed, 0)
		}
		res.Entries = append(res.Entries, store.LibraryEntry{
			GameID: id, MinutesTotal: g.PlaytimeForever, MinutesRecent: g.Playtime2Weeks, LastPlayed: last,
		})
	}
	return res, nil
}
