// Package playtests reads the hand-curated playtest list (data/playtests.json)
// used by /playtests and /radar until the bot can read player.gg's live feed.
package playtests

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// Playtest is one listing.
type Playtest struct {
	Title     string   `json:"title"`
	Studio    string   `json:"studio"`
	URL       string   `json:"url"` // player.gg page where people request access
	Image     string   `json:"image"`
	Summary   string   `json:"summary"`
	Platforms []string `json:"platforms"`
	Genres    []string `json:"genres"`
	Starts    string   `json:"starts"` // YYYY-MM-DD
	Ends      string   `json:"ends"`   // YYYY-MM-DD, optional
	// SimilarSteamApps lists Steam app IDs of comparable games. /radar uses
	// them to say how many members of a server play something similar.
	SimilarSteamApps []string `json:"similar_steam_apps"`
}

// Load reads the list. A missing file means no playtests.
func Load(path string) ([]Playtest, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Playtest
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, p := range list {
		if p.Title == "" || p.URL == "" {
			return nil, fmt.Errorf("%s: entry %d needs a title and url", path, i+1)
		}
		for _, d := range []string{p.Starts, p.Ends} {
			if d == "" {
				continue
			}
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return nil, fmt.Errorf("%s: %q: dates must be YYYY-MM-DD", path, p.Title)
			}
		}
	}
	return list, nil
}

// Live reports whether the playtest is running on the given day.
func (p Playtest) Live(now time.Time) bool {
	today := now.UTC().Format("2006-01-02")
	return (p.Starts == "" || p.Starts <= today) && (p.Ends == "" || p.Ends >= today)
}

// Current returns live playtests (ending soonest first), then upcoming ones
// (starting soonest first). Finished ones are dropped.
func Current(all []Playtest, now time.Time) []Playtest {
	today := now.UTC().Format("2006-01-02")
	var live, upcoming []Playtest
	for _, p := range all {
		switch {
		case p.Ends != "" && p.Ends < today:
			continue
		case p.Live(now):
			live = append(live, p)
		default:
			upcoming = append(upcoming, p)
		}
	}
	sort.SliceStable(live, func(i, j int) bool { return endKey(live[i]) < endKey(live[j]) })
	sort.SliceStable(upcoming, func(i, j int) bool { return upcoming[i].Starts < upcoming[j].Starts })
	return append(live, upcoming...)
}

func endKey(p Playtest) string {
	if p.Ends == "" {
		return "9999-12-31"
	}
	return p.Ends
}

// Status describes the dates in a few words, e.g. "Live until Nov 30".
func (p Playtest) Status(now time.Time) string {
	if p.Live(now) {
		if p.Ends == "" {
			return "Live now"
		}
		return "Live until " + shortDate(p.Ends)
	}
	return "Starts " + shortDate(p.Starts)
}

func shortDate(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("Jan 2")
}
