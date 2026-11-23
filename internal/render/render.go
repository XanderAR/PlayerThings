// Package render draws the shareable images the bot links to: a shelf of
// game covers with each game's rank and hours, like a record collage.
//
// Image URLs carry everything needed to draw them (game IDs and minutes) and
// are signed, so the endpoint needs no database lookup, Discord can cache them
// forever, and a changed library simply produces a new URL.
package render

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // some store art is PNG
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Tile is one game on a shelf.
type Tile struct {
	AppID   string
	Minutes int
}

// Layouts: a 5x2 grid for /collage, a single row for profile cards, a
// collection board (5x2, no ranks, unplayed games dimmed) and four favorites
// (one row of four, a heart instead of a rank).
const (
	Grid  = "grid"
	Strip = "strip"
	Set   = "set"
	Four  = "four"
)

// ValidLayout reports whether l is a layout Shelf can draw.
func ValidLayout(l string) bool { return l == Grid || l == Strip || l == Set || l == Four }

const (
	tileW, tileH = 240, 360 // Steam library covers are 2:3
	captionH     = 44       // strip under each cover for rank and hours
	gap          = 6
	maxTiles     = 10

	maxArtPixels = 4096 * 4096 // larger store art is refused
	maxRendering = 3           // shelves drawn at once; the rest wait
)

func size(layout string) (cols, rows int) {
	switch layout {
	case Strip:
		return 5, 1
	case Four:
		return 4, 1
	}
	return 5, 2
}

// ----- URLs -----

// Encode packs tiles into a URL-safe string: "548430.24720-730.18060".
func Encode(tiles []Tile) string {
	parts := make([]string, len(tiles))
	for i, t := range tiles {
		parts[i] = t.AppID + "." + strconv.Itoa(t.Minutes)
	}
	return strings.Join(parts, "-")
}

// Decode reverses Encode, rejecting anything that isn't app IDs and minutes.
func Decode(s string) ([]Tile, error) {
	if s == "" {
		return nil, errors.New("no games")
	}
	parts := strings.Split(s, "-")
	if len(parts) > maxTiles {
		return nil, fmt.Errorf("at most %d games", maxTiles)
	}
	tiles := make([]Tile, 0, len(parts))
	for _, p := range parts {
		id, mins, ok := strings.Cut(p, ".")
		m, err := strconv.Atoi(mins)
		if !ok || err != nil || m < 0 || !digits(id) || len(id) > 12 {
			return nil, fmt.Errorf("bad game %q", p)
		}
		tiles = append(tiles, Tile{AppID: id, Minutes: m})
	}
	return tiles, nil
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Sign returns the signature for a layout and encoded tiles.
func Sign(secret []byte, layout, encoded string) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "shelf|v1|%s|%s", layout, encoded)
	return hex.EncodeToString(mac.Sum(nil)[:12])
}

// Verify checks a signature from Sign.
func Verify(secret []byte, layout, encoded, sig string) bool {
	return hmac.Equal([]byte(Sign(secret, layout, encoded)), []byte(sig))
}

// ShelfURL is the public URL of a shelf image.
func ShelfURL(site string, secret []byte, layout string, tiles []Tile) string {
	if len(tiles) > maxTiles {
		tiles = tiles[:maxTiles]
	}
	enc := Encode(tiles)
	return fmt.Sprintf("%s/render/shelf.jpg?l=%s&g=%s&s=%s", site, layout, enc, Sign(secret, layout, enc))
}

// ----- drawing -----

// Renderer draws shelves, caching cover art and finished images in memory.
type Renderer struct {
	HTTP *http.Client
	// Cover and Fallback give the art URLs for an app: the portrait cover,
	// then the wide store header if the cover is missing.
	Cover    func(appID string) string
	Fallback func(appID string) string
	// Assets finds art the fixed URLs miss (newer apps keep it under hashed
	// paths). Nil skips that step.
	Assets func(ctx context.Context, appID string) (cover, header string, err error)

	mu      sync.Mutex
	art     map[string]image.Image // decoded art by URL
	output  map[string][]byte      // finished JPEGs by layout and tiles
	raw     map[string][]byte      // art files for /render/art.jpg
	assets  map[string]storeAssets // where the store says each app's art is
	missing map[string]time.Time   // art URLs that failed recently

	big, small font.Face
	busy       chan struct{} // limits renders running at once
}

// New returns a Renderer that fetches art from Steam's CDN.
func New() *Renderer {
	r := &Renderer{
		HTTP: &http.Client{Timeout: 8 * time.Second},
		Cover: func(id string) string {
			return "https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/" + id + "/library_600x900.jpg"
		},
		Fallback: func(id string) string {
			return "https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/" + id + "/header.jpg"
		},
	}
	r.Assets = r.SteamAssets
	return r
}

func (r *Renderer) init() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.big != nil {
		return nil
	}
	f, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return err
	}
	if r.big, err = opentype.NewFace(f, &opentype.FaceOptions{Size: 26, DPI: 72, Hinting: font.HintingFull}); err != nil {
		return err
	}
	if r.small, err = opentype.NewFace(f, &opentype.FaceOptions{Size: 18, DPI: 72, Hinting: font.HintingFull}); err != nil {
		return err
	}
	r.art = map[string]image.Image{}
	r.output = map[string][]byte{}
	r.busy = make(chan struct{}, maxRendering)
	r.raw = map[string][]byte{}
	r.assets = map[string]storeAssets{}
	r.missing = map[string]time.Time{}
	return nil
}

var (
	background = color.RGBA{0x1e, 0x1f, 0x22, 0xff}
	emptyTile  = color.RGBA{0x2b, 0x2d, 0x31, 0xff}
	captionBG  = color.RGBA{0x26, 0x27, 0x2b, 0xff}
	rankBG     = color.RGBA{0x40, 0x42, 0x49, 0xff}
	medals     = []color.RGBA{{0xf5, 0xc5, 0x42, 0xff}, {0xc9, 0xd1, 0xd9, 0xff}, {0xd0, 0x8c, 0x5b, 0xff}}
)

// Shelf draws up to 10 games as a JPEG.
func (r *Renderer) Shelf(ctx context.Context, layout string, tiles []Tile) ([]byte, error) {
	if err := r.init(); err != nil {
		return nil, err
	}
	key := layout + "|" + Encode(tiles)
	r.mu.Lock()
	if out, ok := r.output[key]; ok {
		r.mu.Unlock()
		return out, nil
	}
	r.mu.Unlock()

	select {
	case r.busy <- struct{}{}:
		defer func() { <-r.busy }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	cols, rows := size(layout)
	if len(tiles) > cols*rows {
		tiles = tiles[:cols*rows]
	}
	// Shrink a grid that isn't full, so a short library has no empty row,
	// and a short strip so it has no empty tiles.
	if rows > 1 && len(tiles) <= cols {
		rows = 1
	}
	if rows == 1 && len(tiles) < cols && len(tiles) > 0 {
		cols = len(tiles)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, gap+cols*(tileW+gap), gap+rows*(tileH+captionH+gap)))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{background}, image.Point{}, draw.Src)

	art := r.fetchAll(ctx, tiles)
	for i := 0; i < cols*rows; i++ {
		x := gap + (i%cols)*(tileW+gap)
		y := gap + (i/cols)*(tileH+captionH+gap)
		cell := image.Rect(x, y, x+tileW, y+tileH)
		if i >= len(tiles) || art[i] == nil {
			draw.Draw(canvas, cell, &image.Uniform{emptyTile}, image.Point{}, draw.Src)
		} else {
			drawCover(canvas, cell, art[i])
		}
		if i >= len(tiles) {
			continue
		}
		strip := image.Rect(x, y+tileH, x+tileW, y+tileH+captionH)
		if layout == Set {
			if tiles[i].Minutes == 0 {
				draw.Draw(canvas, cell, &image.Uniform{color.NRGBA{0x12, 0x13, 0x15, 185}}, image.Point{}, draw.Over)
			}
			r.setCaption(canvas, strip, tiles[i].Minutes)
		} else if layout == Four {
			r.fourCaption(canvas, strip, tiles[i].Minutes)
		} else {
			r.caption(canvas, strip, i, tiles[i].Minutes)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	r.mu.Lock()
	if len(r.output) > 200 {
		r.output = map[string][]byte{}
	}
	r.output[key] = out
	r.mu.Unlock()
	return out, nil
}

// fetchAll gets art for every tile in parallel; missing art stays nil.
func (r *Renderer) fetchAll(ctx context.Context, tiles []Tile) []image.Image {
	out := make([]image.Image, len(tiles))
	var wg sync.WaitGroup
	for i, t := range tiles {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			r.artChain(ctx, id, ArtCover, func(u string) bool {
				img, err := r.fetch(ctx, u)
				if err == nil {
					out[i] = img
				}
				return err == nil
			})
		}(i, t.AppID)
	}
	wg.Wait()
	return out
}

func (r *Renderer) fetch(ctx context.Context, url string) (image.Image, error) {
	r.mu.Lock()
	if img, ok := r.art[url]; ok {
		r.mu.Unlock()
		return img, nil
	}
	r.mu.Unlock()
	raw, err := r.fetchRaw(ctx, url)
	if err != nil {
		return nil, err
	}
	// Check the size before decoding, so one huge image can't eat the memory.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxArtPixels {
		return nil, fmt.Errorf("%s: image is %dx%d", url, cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	// Keep a copy just big enough to fill a tile (about 350 KB for a cover),
	// so the cache stays small.
	small := image.NewRGBA(coverRect(img.Bounds(), tileW, tileH))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), img, img.Bounds(), draw.Src, nil)
	r.mu.Lock()
	if len(r.art) > 400 {
		r.art = map[string]image.Image{}
	}
	r.art[url] = small
	r.mu.Unlock()
	return small, nil
}

// coverRect scales b down until it just covers w x h, keeping its shape, so a
// tile can still be cropped from it at full sharpness.
func coverRect(b image.Rectangle, w, h int) image.Rectangle {
	bw, bh := b.Dx(), b.Dy()
	scale := max(float64(w)/float64(bw), float64(h)/float64(bh))
	if scale >= 1 {
		return image.Rect(0, 0, bw, bh)
	}
	return image.Rect(0, 0, max(1, int(float64(bw)*scale)), max(1, int(float64(bh)*scale)))
}

// drawCover fills cell with src, cropping the middle if the shapes differ.
func drawCover(dst *image.RGBA, cell image.Rectangle, src image.Image) {
	sb := src.Bounds()
	want := float64(cell.Dx()) / float64(cell.Dy())
	have := float64(sb.Dx()) / float64(sb.Dy())
	crop := sb
	if have > want { // too wide: trim the sides
		w := int(float64(sb.Dy()) * want)
		off := (sb.Dx() - w) / 2
		crop = image.Rect(sb.Min.X+off, sb.Min.Y, sb.Min.X+off+w, sb.Max.Y)
	} else if have < want { // too tall: trim top and bottom
		h := int(float64(sb.Dx()) / want)
		off := (sb.Dy() - h) / 2
		crop = image.Rect(sb.Min.X, sb.Min.Y+off, sb.Max.X, sb.Min.Y+off+h)
	}
	draw.CatmullRom.Scale(dst, cell, src, crop, draw.Src, nil)
}

// caption draws the rank badge and hours in the strip under a cover, so
// nothing is drawn over the art.
func (r *Renderer) caption(dst *image.RGBA, strip image.Rectangle, i, minutes int) {
	draw.Draw(dst, strip, &image.Uniform{captionBG}, image.Point{}, draw.Src)
	// Rank badge: medal colors for the top three.
	cx, cy, rad := strip.Min.X+22, strip.Min.Y+strip.Dy()/2, 14
	fill, ink := color.Color(rankBG), color.Color(color.White)
	if i < len(medals) {
		fill, ink = medals[i], background
	}
	circle(dst, cx, cy, rad, fill)
	rank := strconv.Itoa(i + 1)
	w := font.MeasureString(r.small, rank).Ceil()
	text(dst, r.small, rank, cx-w/2, cy+7, ink)
	text(dst, r.big, hoursLabel(minutes), strip.Min.X+46, cy+10, color.White)
}

// setCaption marks a collection game as played (green dot and hours) or not.
func (r *Renderer) setCaption(dst *image.RGBA, strip image.Rectangle, minutes int) {
	draw.Draw(dst, strip, &image.Uniform{captionBG}, image.Point{}, draw.Src)
	cy := strip.Min.Y + strip.Dy()/2
	if minutes == 0 {
		text(dst, r.small, "not played", strip.Min.X+16, cy+7, color.RGBA{0x8a, 0x8e, 0x96, 0xff})
		return
	}
	circle(dst, strip.Min.X+22, cy, 8, color.RGBA{0x23, 0xa5, 0x5a, 0xff})
	text(dst, r.big, hoursLabel(minutes), strip.Min.X+40, cy+10, color.White)
}

// fourCaption marks a favorite with a heart and the hours played on Steam.
func (r *Renderer) fourCaption(dst *image.RGBA, strip image.Rectangle, minutes int) {
	draw.Draw(dst, strip, &image.Uniform{captionBG}, image.Point{}, draw.Src)
	cy := strip.Min.Y + strip.Dy()/2
	heart(dst, strip.Min.X+24, cy, 11, color.RGBA{0xff, 0x5a, 0x7a, 0xff})
	if minutes == 0 {
		text(dst, r.small, "no Steam hours", strip.Min.X+44, cy+7, color.RGBA{0x8a, 0x8e, 0x96, 0xff})
		return
	}
	text(dst, r.big, hoursLabel(minutes), strip.Min.X+44, cy+10, color.White)
}

// heart fills a heart shape of half-width size centred on cx, cy, using the
// curve (x^2 + y^2 - 1)^3 - x^2 y^3 <= 0.
func heart(dst *image.RGBA, cx, cy, size int, c color.Color) {
	s := float64(size)
	for py := -size - 2; py <= size+2; py++ {
		for px := -size - 2; px <= size+2; px++ {
			x := float64(px) / s * 1.15
			y := -float64(py) / s * 1.15
			a := x*x + y*y - 1
			if a*a*a-x*x*y*y*y <= 0 {
				dst.Set(cx+px, cy+py, c)
			}
		}
	}
}

func circle(dst *image.RGBA, cx, cy, rad int, c color.Color) {
	for y := -rad; y <= rad; y++ {
		for x := -rad; x <= rad; x++ {
			if x*x+y*y <= rad*rad {
				dst.Set(cx+x, cy+y, blend(dst.At(cx+x, cy+y), c))
			}
		}
	}
}

func blend(under, over color.Color) color.Color {
	r1, g1, b1, _ := under.RGBA()
	r2, g2, b2, a2 := over.RGBA()
	a := float64(a2) / 0xffff
	mix := func(u, o uint32) uint8 { return uint8((float64(u)*(1-a) + float64(o)*a) / 0x101) }
	return color.RGBA{mix(r1, r2), mix(g1, g2), mix(b1, b2), 0xff}
}

func text(dst *image.RGBA, face font.Face, s string, x, y int, c color.Color) {
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

// hoursLabel matches the bot's hour format: "0.5 h", "6.5 h", "1,284 h".
func hoursLabel(minutes int) string {
	h := float64(minutes) / 60
	if h < 10 {
		if h < 0.1 {
			h = 0.1
		}
		return strings.TrimSuffix(strconv.FormatFloat(h, 'f', 1, 64), ".0") + " h"
	}
	n := strconv.Itoa(int(h + 0.5))
	for i := len(n) - 3; i > 0; i -= 3 {
		n = n[:i] + "," + n[i:]
	}
	return n + " h"
}
