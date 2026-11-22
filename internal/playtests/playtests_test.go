package playtests

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCurrentOrdersLiveThenUpcoming(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	all := []Playtest{
		{Title: "Over", URL: "u1", Starts: "2026-11-01", Ends: "2026-11-10"},
		{Title: "Later", URL: "u2", Starts: "2026-12-10"},
		{Title: "Live long", URL: "u3", Starts: "2026-11-01", Ends: "2026-12-20"},
		{Title: "Soon", URL: "u4", Starts: "2026-12-01"},
		{Title: "Live short", URL: "u5", Starts: "2026-11-20", Ends: "2026-11-25"},
		{Title: "Ends today", URL: "u6", Starts: "2026-11-01", Ends: "2026-11-22"},
	}
	got := Current(all, now)
	want := []string{"Ends today", "Live short", "Live long", "Soon", "Later"}
	if len(got) != len(want) {
		t.Fatalf("got %d playtests", len(got))
	}
	for i, p := range got {
		if p.Title != want[i] {
			t.Fatalf("position %d: got %q want %q", i, p.Title, want[i])
		}
	}
	if s := got[1].Status(now); s != "Live until Nov 25" {
		t.Errorf("status = %q", s)
	}
	if s := got[3].Status(now); s != "Starts Dec 1" {
		t.Errorf("status = %q", s)
	}
}

func TestLoadValidates(t *testing.T) {
	dir := t.TempDir()
	if list, err := Load(filepath.Join(dir, "missing.json")); err != nil || list != nil {
		t.Fatalf("missing file: %v %v", list, err)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`[{"title":"x","url":"https://player.gg/x","starts":"Dec 1"}]`), 0o644)
	if _, err := Load(bad); err == nil {
		t.Fatal("bad date accepted")
	}
	good := filepath.Join(dir, "good.json")
	os.WriteFile(good, []byte(`[{"title":"x","url":"https://player.gg/x","starts":"2026-12-01"}]`), 0o644)
	if list, err := Load(good); err != nil || len(list) != 1 {
		t.Fatalf("good file: %v %v", list, err)
	}
}

func TestShippedExampleFileLoads(t *testing.T) {
	for _, name := range []string{"playtests.json", "playtests.example.json"} {
		path := filepath.Join("..", "..", "data", name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if _, err := Load(path); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
