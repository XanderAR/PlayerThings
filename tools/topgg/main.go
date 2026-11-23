// Command topgg builds the images for the bot's top.gg page: real card
// screenshots (trimmed, padded and stitched), cover images drawn by the bot's
// renderer from sample data, and a banner. Output goes to the folder the web
// server embeds and serves at /media/.
//
//	go run ./tools/topgg -out internal/web/static/media \
//	  -shot verdict=a.png -shot backlog=b1.png@0,b2.png@294 ...
//
// A shot is one or more screenshot slices of a card, each with its row offset
// within the card, so tall cards can be captured in parts.
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"

	"playradar/internal/render"
)

type shots map[string]string

func (s shots) String() string { return fmt.Sprint(map[string]string(s)) }
func (s shots) Set(v string) error {
	name, spec, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("want name=path[@offset],...")
	}
	s[name] = spec
	return nil
}

func main() {
	out := flag.String("out", "internal/web/static/media", "output folder")
	icon := flag.String("icon", "internal/web/static/icon.png", "app icon for the banner")
	bannerName := flag.String("banner", "banner.png", "banner file name")
	footer := flag.String("footer", "Steam stats, playtests and server leaderboards  ·  Unofficial, built for player.gg", "banner footer line")
	bannerOnly := flag.Bool("banner-only", false, "only draw the banner")
	list := shots{}
	flag.Var(list, "shot", "card screenshot: name=path[@offset][,path@offset...] (repeatable)")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	for name, spec := range list {
		img, err := card(spec)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		save(filepath.Join(*out, "card-"+name+".png"), img)
	}
	if !*bannerOnly {
		shelves(*out)
	}
	if err := banner(filepath.Join(*out, *bannerName), *icon, *footer); err != nil {
		log.Fatal(err)
	}
}

// ----- card screenshots -----

// card loads a card's screenshot slices, stitches them by row offset, trims
// the label above the card and the empty chat around it, and rounds the
// corners.
func card(spec string) (image.Image, error) {
	type slice struct {
		img image.Image
		off int
	}
	var parts []slice
	width, height := 0, 0
	for _, p := range strings.Split(spec, ",") {
		path, offStr, _ := strings.Cut(p, "@")
		off, _ := strconv.Atoi(offStr)
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		parts = append(parts, slice{img, off})
		width = max(width, img.Bounds().Dx())
		height = max(height, off+img.Bounds().Dy())
	}
	full := image.NewRGBA(image.Rect(0, 0, width, height))
	for _, s := range parts {
		draw.Draw(full, image.Rect(0, s.off, width, s.off+s.img.Bounds().Dy()), s.img, s.img.Bounds().Min, draw.Src)
	}

	bg := full.At(width-3, 3) // empty chat to the right of the label
	differs := func(c color.Color, by int) bool {
		r1, g1, b1, _ := c.RGBA()
		r2, g2, b2, _ := bg.RGBA()
		d := abs(int(r1>>8)-int(r2>>8)) + abs(int(g1>>8)-int(g2>>8)) + abs(int(b1>>8)-int(b2>>8))
		return d > by
	}
	saturated := func(c color.Color) bool {
		r, g, b, _ := c.RGBA()
		r, g, b = r>>8, g>>8, b>>8
		return abs(int(r)-int(g))+abs(int(g)-int(b))+abs(int(r)-int(b)) > 70
	}
	// The card starts where its colored left border does.
	top := -1
	for y := 0; y < height && top < 0; y++ {
		for x := 0; x < 8; x++ {
			if saturated(full.At(x, y)) {
				top = y
				break
			}
		}
	}
	if top < 0 {
		return nil, fmt.Errorf("couldn't find the card's colored border")
	}
	right, bottom := 0, top
	for y := top; y < height; y++ {
		for x := 0; x < width; x++ {
			if differs(full.At(x, y), 18) {
				right = max(right, x)
				bottom = max(bottom, y)
			}
		}
	}
	const pad = 14
	crop := image.Rect(0, top, right+1, bottom+1)
	outImg := image.NewRGBA(image.Rect(0, 0, crop.Dx()+2*pad, crop.Dy()+2*pad))
	draw.Draw(outImg, outImg.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(outImg, crop.Sub(crop.Min).Add(image.Pt(pad, pad)), full, crop.Min, draw.Src)
	roundCorners(outImg, 16)
	return outImg, nil
}

func roundCorners(img *image.RGBA, r int) {
	b := img.Bounds()
	for y := 0; y < r; y++ {
		for x := 0; x < r; x++ {
			dx, dy := float64(r-x)-0.5, float64(r-y)-0.5
			if dx*dx+dy*dy > float64(r*r) {
				for _, p := range []image.Point{{x, y}, {b.Max.X - 1 - x, y}, {x, b.Max.Y - 1 - y}, {b.Max.X - 1 - x, b.Max.Y - 1 - y}} {
					img.Set(p.X, p.Y, color.Transparent)
				}
			}
		}
	}
}

// ----- cover images from sample data -----

func shelves(out string) {
	r := render.New()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	h := func(hours float64) int { return int(hours * 60) }
	jobs := []struct {
		name, layout string
		tiles        []render.Tile
	}{
		{"collage", render.Grid, []render.Tile{
			{AppID: "1086940", Minutes: h(412)}, {AppID: "1245620", Minutes: h(301)}, {AppID: "1145350", Minutes: h(260)},
			{AppID: "413150", Minutes: h(188)}, {AppID: "548430", Minutes: h(150)}, {AppID: "1091500", Minutes: h(120)},
			{AppID: "2379780", Minutes: h(96)}, {AppID: "367520", Minutes: h(85)}, {AppID: "427520", Minutes: h(70)},
			{AppID: "753640", Minutes: h(42)}}},
		{"four", render.Four, []render.Tile{
			{AppID: "753640", Minutes: h(42)}, {AppID: "367520", Minutes: h(85)}, {AppID: "632470", Minutes: h(61)}, {AppID: "620", Minutes: h(23)}}},
		{"collection", render.Set, []render.Tile{
			{AppID: "367520", Minutes: h(85)}, {AppID: "504230", Minutes: h(31)}, {AppID: "1145360", Minutes: h(54)},
			{AppID: "413150", Minutes: h(188)}, {AppID: "391540", Minutes: 0}, {AppID: "2379780", Minutes: h(96)},
			{AppID: "753640", Minutes: h(42)}, {AppID: "105600", Minutes: 0}, {AppID: "646570", Minutes: h(150)},
			{AppID: "632470", Minutes: 0}}},
	}
	for _, j := range jobs {
		img, complete, err := r.Shelf(ctx, j.layout, j.tiles)
		if err != nil || !complete {
			log.Fatalf("%s: complete=%v err=%v", j.name, complete, err)
		}
		if err := os.WriteFile(filepath.Join(out, "shelf-"+j.name+".jpg"), img, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}

// ----- banner -----

func banner(path, iconPath, footer string) error {
	const W, H = 1600, 600
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	// Deep purple gradient with a soft glow behind the covers.
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			t := (float64(x)/W*0.6 + float64(y)/H*0.4)
			c := mix(color.RGBA{0x10, 0x0e, 0x1c, 0xff}, color.RGBA{0x2a, 0x1c, 0x52, 0xff}, t)
			dx, dy := float64(x-1230), float64(y-300)
			glow := math.Max(0, 1-math.Sqrt(dx*dx+dy*dy)/520)
			img.Set(x, y, mix(c, color.RGBA{0x7c, 0x5c, 0xff, 0xff}, glow*0.35))
		}
	}

	// Covers, fanned out.
	covers := []struct {
		id    string
		angle float64
		cx    float64
		cy    float64
	}{
		{"367520", -6, 1085, 318}, {"1086940", 6, 1465, 318}, {"1145350", 0, 1275, 296},
	}
	for _, c := range covers {
		cover, err := fetch("https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/" + c.id + "/library_600x900.jpg")
		if err != nil {
			return fmt.Errorf("cover %s: %w", c.id, err)
		}
		scale := 205.0 / float64(cover.Bounds().Dx())
		shadow := image.NewNRGBA(cover.Bounds())
		draw.Draw(shadow, shadow.Bounds(), image.NewUniform(color.NRGBA{0, 0, 0, 70}), image.Point{}, draw.Src)
		for i := 1; i <= 3; i++ {
			place(img, shadow, c.angle, scale, c.cx+float64(4*i), c.cy+float64(7*i))
		}
		place(img, cover, c.angle, scale, c.cx, c.cy)
	}

	black, err := face(`C:\Windows\Fonts\seguibl.ttf`, 104)
	if err != nil {
		return err
	}
	bold, err := face(`C:\Windows\Fonts\segoeuib.ttf`, 38)
	if err != nil {
		return err
	}
	chipFace, err := face(`C:\Windows\Fonts\segoeuib.ttf`, 26)
	if err != nil {
		return err
	}
	small, err := face(`C:\Windows\Fonts\segoeui.ttf`, 23)
	if err != nil {
		return err
	}

	if f, err := os.Open(iconPath); err == nil {
		ic, _, err := image.Decode(f)
		f.Close()
		if err == nil {
			small := image.NewRGBA(image.Rect(0, 0, 84, 84))
			draw.CatmullRom.Scale(small, small.Bounds(), ic, ic.Bounds(), draw.Src, nil)
			roundCorners(small, 18)
			draw.Draw(img, image.Rect(90, 92, 90+84, 92+84), small, image.Point{}, draw.Over)
		}
	}
	text(img, black, "Player.gg", 86, 290, color.White)
	text(img, bold, "Your games, your server, your verdict.", 92, 352, color.RGBA{0xd6, 0xcf, 0xff, 0xff})

	x := 92
	for _, chip := range []string{"/profile", "/four", "/verdict", "/backlog", "/goty", "/playtests"} {
		w := font.MeasureString(chipFace, chip).Ceil() + 36
		pill(img, image.Rect(x, 400, x+w, 450), color.RGBA{0x34, 0x2b, 0x5c, 0xff})
		text(img, chipFace, chip, x+18, 435, color.RGBA{0xee, 0xea, 0xff, 0xff})
		x += w + 12
	}
	text(img, small, footer, 92, 532, color.RGBA{0x9a, 0x93, 0xbd, 0xff})

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// place draws src rotated by angle degrees and scaled, centred on (cx, cy).
func place(dst *image.RGBA, src image.Image, angle, scale, cx, cy float64) {
	th := angle * math.Pi / 180
	sb := src.Bounds()
	w, h := float64(sb.Dx()), float64(sb.Dy())
	a, b := scale*math.Cos(th), -scale*math.Sin(th)
	d, e := scale*math.Sin(th), scale*math.Cos(th)
	m := f64.Aff3{a, b, cx - (a*w/2 + b*h/2), d, e, cy - (d*w/2 + e*h/2)}
	draw.CatmullRom.Transform(dst, m, src, sb, draw.Over, nil)
}

func pill(dst *image.RGBA, r image.Rectangle, c color.Color) {
	rad := r.Dy() / 2
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cx := min(max(x, r.Min.X+rad), r.Max.X-rad-1)
			cy := r.Min.Y + rad
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= rad*rad {
				dst.Set(x, y, c)
			}
		}
	}
}

func face(path string, size float64) (font.Face, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := opentype.Parse(raw)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

func text(dst *image.RGBA, f font.Face, s string, x, y int, c color.Color) {
	(&font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, y)}).DrawString(s)
}

func fetch(url string) (image.Image, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	img, _, err := image.Decode(resp.Body)
	return img, err
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	l := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 0xff}
}

func save(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

var _ = jpeg.Decode // registers JPEG decoding for screenshots and covers
