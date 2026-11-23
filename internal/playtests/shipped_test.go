package playtests

import (
	"regexp"
	"strings"
	"testing"
)

// The playtest list is edited by hand and built into the image, so check it
// here rather than finding a bad entry in Discord.
func TestShippedPlaytestFiles(t *testing.T) {
	digits := regexp.MustCompile(`^[0-9]+$`)
	for _, path := range []string{"../../data/playtests.json", "../../data/playtests.example.json"} {
		list, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			// /go only forwards to player.gg, so any other link would break the button.
			if !strings.HasPrefix(p.URL, "https://player.gg/") {
				t.Errorf("%s: %q url %q is not on player.gg", path, p.Title, p.URL)
			}
			if p.Image != "" && !strings.HasPrefix(p.Image, "https://") {
				t.Errorf("%s: %q image must be https", path, p.Title)
			}
			if p.Starts == "" {
				t.Errorf("%s: %q needs a start date", path, p.Title)
			}
			if p.Ends != "" && p.Ends < p.Starts {
				t.Errorf("%s: %q ends before it starts", path, p.Title)
			}
			if len(p.Summary) > 300 {
				t.Errorf("%s: %q summary is %d characters; keep it short", path, p.Title, len(p.Summary))
			}
			for _, id := range p.SimilarSteamApps {
				if !digits.MatchString(id) {
					t.Errorf("%s: %q similar app %q is not a Steam app id", path, p.Title, id)
				}
			}
		}
	}
}
