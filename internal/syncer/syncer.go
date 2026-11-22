// Package syncer pulls linked accounts' libraries from their platforms on a
// schedule. Each platform is a Provider, so Xbox, PlayStation or
// RetroAchievements can be added next to Steam.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"playradar/internal/steam"
	"playradar/internal/store"
)

// ErrBusy means the account is already being synced.
var ErrBusy = errors.New("sync already running for this account")

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

// MultiplayerChecker looks up whether a Steam game supports online play.
type MultiplayerChecker interface {
	IsMultiplayer(ctx context.Context, appID string) (multiplayer, known bool, err error)
}

// Syncer refreshes libraries in the background.
type Syncer struct {
	Store     *store.Store
	Providers map[string]Provider
	Checker   MultiplayerChecker // optional
	Interval  time.Duration      // how stale an account may get
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
	if err := s.Store.SaveSync(ctx, discordID, platform, res.Profile, res.Games, res.Entries, s.now()); err != nil {
		return store.Profile{}, 0, err
	}
	return res.Profile, len(res.Entries), nil
}

// SyncSoon refreshes an account in the background.
func (s *Syncer) SyncSoon(discordID, platform string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, _, err := s.Sync(ctx, discordID, platform); err != nil && !errors.Is(err, ErrBusy) {
			s.log().Warn("background sync failed", "platform", platform, "err", err)
		}
	}()
}

// CheckGame looks up and stores a Steam game's multiplayer tag.
func (s *Syncer) CheckGame(ctx context.Context, appID string) (*bool, error) {
	if s.Checker == nil {
		return nil, nil
	}
	mp, known, err := s.Checker.IsMultiplayer(ctx, appID)
	if err != nil {
		return nil, err
	}
	var v *bool
	if known {
		v = &mp
	}
	return v, s.Store.SetMultiplayer(ctx, "steam", appID, v, s.now())
}

// Run syncs stale accounts and fills in multiplayer tags until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Syncer) tick(ctx context.Context) {
	cutoff := s.now().Add(-s.Interval)
	for platform := range s.Providers {
		due, err := s.Store.DueForSync(ctx, platform, cutoff, 30)
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
	ids, err := s.Store.UncheckedGames(ctx, "steam", 5)
	if err != nil {
		s.log().Error("list unchecked games", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.CheckGame(ctx, id); err != nil {
			s.log().Warn("multiplayer lookup failed", "app", id, "err", err)
			return
		}
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
