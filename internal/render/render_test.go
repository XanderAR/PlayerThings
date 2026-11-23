package render

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestEncodeDecodeAndSign(t *testing.T) {
	tiles := []Tile{{"548430", 24720}, {"730", 18060}}
	enc := Encode(tiles)
	if enc != "548430.24720-730.18060" {
		t.Fatalf("encode = %q", enc)
	}
	back, err := Decode(enc)
	if err != nil || len(back) != 2 || back[1] != (Tile{"730", 18060}) {
		t.Fatalf("decode = %+v, %v", back, err)
	}
	for _, bad := range []string{"", "x.1", "1.-5", "1", strings.Repeat("1.1-", 11) + "1.1"} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("Decode(%q) should fail", bad)
		}
	}
	secret := []byte("0123456789abcdef0123456789abcdef")
	sig := Sign(secret, Grid, enc)
	if !Verify(secret, Grid, enc, sig) || Verify(secret, Strip, enc, sig) || Verify(secret, Grid, enc+"-1.1", sig) {
		t.Fatal("signature check is wrong")
	}
	if u := ShelfURL("https://bot.example", secret, Grid, tiles); u != "https://bot.example/render/shelf.jpg?l=grid&g="+enc+"&s="+sig {
		t.Fatalf("url = %s", u)
	}
}

// art serves a solid-color cover for app 1, only a wide header for app 2,
// and nothing for app 3.
func artServer(t *testing.T) *httptest.Server {
	jpg := func(w, h int, c color.Color) []byte {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, c)
			}
		}
		var buf bytes.Buffer
		jpeg.Encode(&buf, img, nil)
		return buf.Bytes()
	}
	cover, header := jpg(600, 900, color.RGBA{200, 40, 40, 255}), jpg(460, 215, color.RGBA{40, 40, 200, 255})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cover/1":
			w.Write(cover)
		case "/header/2":
			w.Write(header)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestShelf(t *testing.T) {
	srv := artServer(t)
	r := New()
	r.Cover = func(id string) string { return srv.URL + "/cover/" + id }
	r.Fallback = func(id string) string { return srv.URL + "/header/" + id }

	out, err := r.Shelf(context.Background(), Grid, []Tile{{"1", 24720}, {"2", 300}, {"3", 5}})
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	// Three games fit one row, so the grid shrinks to a single row.
	if b := img.Bounds(); b.Dx() != gap+5*(tileW+gap) || b.Dy() != gap+tileH+captionH+gap {
		t.Fatalf("size = %v", b)
	}
	// Middle of tile 1 is the red cover; tile 2 uses the blue header.
	if r, _, _, _ := img.At(gap+tileW/2, gap+tileH/2).RGBA(); r>>8 < 150 {
		t.Errorf("tile 1 isn't the cover (red = %d)", r>>8)
	}
	x2 := gap + (tileW + gap) + tileW/2
	if _, _, b, _ := img.At(x2, gap+tileH/2).RGBA(); b>>8 < 150 {
		t.Errorf("tile 2 isn't the header fallback (blue = %d)", b>>8)
	}
	if os.Getenv("SHELF_OUT") != "" {
		os.WriteFile(os.Getenv("SHELF_OUT"), out, 0o644)
	}
	// Rendering again is served from the cache.
	again, _ := r.Shelf(context.Background(), Grid, []Tile{{"1", 24720}, {"2", 300}, {"3", 5}})
	if !bytes.Equal(out, again) {
		t.Fatal("second render differs")
	}
}

// TestLiveShelf renders real Steam covers to a file for a visual check:
// SHELF_LIVE=out.jpg go test -run TestLiveShelf ./internal/render/
func TestLiveShelf(t *testing.T) {
	out := os.Getenv("SHELF_LIVE")
	if out == "" {
		t.Skip("set SHELF_LIVE to a file path to render real covers")
	}
	tiles := []Tile{{"548430", 24720}, {"730", 18060}, {"427520", 15600}, {"646570", 9000}, {"413150", 7200},
		{"2379780", 5100}, {"1267910", 4200}, {"1102190", 3600}, {"1434950", 2400}, {"1092790", 1800}}
	layout := Grid
	if os.Getenv("SHELF_LAYOUT") == Strip {
		layout, tiles = Strip, tiles[:5]
	}
	img, err := New().Shelf(context.Background(), layout, tiles)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(out, img, 0o644)
}

func TestHoursLabel(t *testing.T) {
	for m, want := range map[int]string{3: "0.1 h", 390: "6.5 h", 24720: "412 h", 77040: "1,284 h"} {
		if got := hoursLabel(m); got != want {
			t.Errorf("hoursLabel(%d) = %q, want %q", m, got, want)
		}
	}
}
