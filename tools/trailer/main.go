// Command trailer renders a 30-second product trailer from the bot's real
// card screenshots and cover images (the same assets as the top.gg page),
// and encodes it with ffmpeg.
//
//	go run ./tools/trailer -out trailer/playergg-trailer.mp4
//	go run ./tools/trailer -stills 1,4,6.5,10 -out trailer/   # PNG stills to review
//
// The structure follows short Discord-bot trailers like Bake.gg's: a hook, a
// command typed in Discord, one beat per feature with a short caption, and an
// end card with an Add to Discord button.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	_ "image/jpeg"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
)

const (
	W, H  = 1920, 1080
	fps   = 30
	total = 30.0
)

var (
	accent   = color.NRGBA{0x9d, 0x86, 0xff, 0xff}
	white    = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	soft     = color.NRGBA{0xd6, 0xcf, 0xff, 0xff}
	muted    = color.NRGBA{0x9a, 0x93, 0xbd, 0xff}
	blurple  = color.NRGBA{0x58, 0x65, 0xf2, 0xff}
	chipBG   = color.NRGBA{0x34, 0x2b, 0x5c, 0xff}
	barBG    = color.NRGBA{0x38, 0x3a, 0x40, 0xff}
	popupBG  = color.NRGBA{0x2b, 0x2d, 0x31, 0xff}
	rowBG    = color.NRGBA{0x40, 0x42, 0x49, 0xff}
	discText = color.NRGBA{0xdb, 0xde, 0xe1, 0xff}
	discDim  = color.NRGBA{0xb5, 0xba, 0xc1, 0xff}
)

type faces struct{ hook, caption, chip, title, tagline, small, bar, barBold, barSmall, button font.Face }

type assets struct {
	verdict, four, profile, collage, backlog, goty, playtests, icon image.Image
	covers                                                          []image.Image
}

type renderer struct {
	bg   *image.RGBA
	glow *image.NRGBA
	a    assets
	f    faces
}

func main() {
	out := flag.String("out", "trailer/playergg-trailer.mp4", "output video (or folder, with -stills)")
	media := flag.String("media", "internal/web/static/media", "folder with the card and shelf images")
	icon := flag.String("icon", "internal/web/static/icon.png", "app icon")
	ffmpeg := flag.String("ffmpeg", "ffmpeg", "ffmpeg binary")
	stills := flag.String("stills", "", "comma-separated seconds: write PNG stills instead of a video")
	flag.Parse()

	r := &renderer{bg: background(), glow: glowSprite(900)}
	var err error
	if r.a, err = loadAssets(*media, *icon); err != nil {
		log.Fatal(err)
	}
	if r.f, err = loadFaces(); err != nil {
		log.Fatal(err)
	}
	frame := image.NewRGBA(image.Rect(0, 0, W, H))

	if *stills != "" {
		os.MkdirAll(*out, 0o755)
		for _, s := range strings.Split(*stills, ",") {
			t, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
			r.draw(frame, t)
			f, _ := os.Create(filepath.Join(*out, fmt.Sprintf("still-%05.2f.png", t)))
			png.Encode(f, frame)
			f.Close()
		}
		return
	}

	os.MkdirAll(filepath.Dir(*out), 0o755)
	cmd := exec.Command(*ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", W, H), "-r", strconv.Itoa(fps), "-i", "-",
		"-c:v", "libx264", "-preset", "slow", "-crf", "16", "-pix_fmt", "yuv420p", "-movflags", "+faststart", *out)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	w := bufio.NewWriterSize(in, 1<<22)
	n := int(total * fps)
	for i := 0; i < n; i++ {
		r.draw(frame, float64(i)/fps)
		if _, err := w.Write(frame.Pix); err != nil {
			log.Fatal(err)
		}
		if i%60 == 0 {
			fmt.Fprintf(os.Stderr, "\rframe %d/%d", i, n)
		}
	}
	w.Flush()
	in.Close()
	if err := cmd.Wait(); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "\rwrote %s\n", *out)
}

// ----- timeline -----

type beat struct {
	start, end   float64
	img          func(a assets) image.Image
	command, cap string
}

var beats = []beat{
	{5.0, 8.6, func(a assets) image.Image { return a.verdict }, "/verdict", "Get judged by your own playtime."},
	{8.4, 12.0, func(a assets) image.Image { return a.four }, "/four", "Show off the four games that made you."},
	{11.8, 15.4, nil, "/profile", "Your whole Steam library in one card."},
	{15.2, 18.8, func(a assets) image.Image { return a.backlog }, "/backlog", "Face your pile of shame."},
	{18.6, 22.2, func(a assets) image.Image { return a.goty }, "/goty", "Run your Game of the Year race."},
	{22.0, 25.6, func(a assets) image.Image { return a.playtests }, "/playtests", "Play new games before they're out."},
}

func (r *renderer) draw(dst *image.RGBA, t float64) {
	copy(dst.Pix, r.bg.Pix)
	gx := 1250 + 260*math.Sin(t*0.35)
	gy := 420 + 140*math.Cos(t*0.27)
	draw.Draw(dst, r.glow.Bounds().Add(image.Pt(int(gx)-r.glow.Bounds().Dx()/2, int(gy)-r.glow.Bounds().Dy()/2)), r.glow, image.Point{}, draw.Over)

	if t < 2.9 {
		r.hook(dst, t)
	}
	if t >= 2.6 && t < 5.3 {
		r.composer(dst, t-2.6)
	}
	for _, b := range beats {
		if t >= b.start && t < b.end {
			r.beat(dst, b, t-b.start, b.end-b.start)
		}
	}
	if t >= 25.4 {
		r.endCard(dst, t-25.4)
	}
	// Fade in from black at the start and out at the end.
	if t < 0.35 {
		fade(dst, 1-t/0.35)
	}
	if t > total-0.45 {
		fade(dst, (t-(total-0.45))/0.45)
	}
}

// presence is a scene's opacity: in over `in` seconds, out over the last `out`.
func presence(local, dur, in, out float64) float64 {
	a := 1.0
	if local < in {
		a = ease(local / in)
	}
	if out > 0 && local > dur-out {
		a = math.Min(a, ease((dur-local)/out))
	}
	return clamp(a)
}

// ----- scenes -----

func (r *renderer) hook(dst *image.RGBA, t float64) {
	alpha := presence(t, 2.9, 0.3, 0.35)
	line1, line2 := "Who here actually plays", "Elden Ring?"
	full := line1 + "\n" + line2
	shown := int(clamp((t-0.25)/1.4) * float64(len([]rune(full))))
	typed := string([]rune(full)[:shown])
	a, b, _ := strings.Cut(typed, "\n")
	y1, y2 := 470, 590
	x1 := W/2 - font.MeasureString(r.f.hook, line1).Ceil()/2
	x2 := W/2 - font.MeasureString(r.f.hook, line2).Ceil()/2
	text(dst, r.f.hook, a, x1, y1, white, alpha)
	text(dst, r.f.hook, b, x2, y2, accent, alpha)
	// Caret while typing.
	if shown < len([]rune(full)) && int(t*4)%2 == 0 {
		x, y := x1+font.MeasureString(r.f.hook, a).Ceil()+6, y1
		if strings.Contains(typed, "\n") {
			x, y = x2+font.MeasureString(r.f.hook, b).Ceil()+6, y2
		}
		fillRound(dst, image.Rect(x, y-80, x+8, y+10), 3, white, alpha)
	}
}

// composer is Discord's message box with /verdict being typed and the
// command suggestion above it.
func (r *renderer) composer(dst *image.RGBA, t float64) {
	alpha := presence(t, 2.7, 0.35, 0.45)
	lift := (1 - ease(clamp(t/0.5))) * 40
	bar := image.Rect(W/2-560, 760, W/2+560, 840).Add(image.Pt(0, int(lift)))
	fillRound(dst, bar, 14, barBG, alpha)
	circleAt(dst, bar.Min.X+44, bar.Min.Y+40, 17, discDim, alpha)
	text(dst, r.f.barBold, "+", bar.Min.X+35, bar.Min.Y+52, barBG, alpha)

	cmd := "/verdict"
	shown := int(clamp((t-0.25)/0.7) * float64(len(cmd)))
	typed := cmd[:shown]
	text(dst, r.f.bar, typed, bar.Min.X+84, bar.Min.Y+52, discText, alpha)
	if int(t*3)%2 == 0 || shown < len(cmd) {
		x := bar.Min.X + 86 + font.MeasureString(r.f.bar, typed).Ceil()
		fillRound(dst, image.Rect(x, bar.Min.Y+20, x+3, bar.Min.Y+60), 1, discText, alpha)
	}
	if t > 0.45 {
		pa := alpha * ease(clamp((t-0.45)/0.25))
		pop := image.Rect(bar.Min.X, bar.Min.Y-178, bar.Max.X, bar.Min.Y-14)
		fillRound(dst, pop, 12, popupBG, pa)
		text(dst, r.f.barSmall, "Commands matching "+typed, pop.Min.X+24, pop.Min.Y+42, discDim, pa)
		row := image.Rect(pop.Min.X+12, pop.Min.Y+62, pop.Max.X-12, pop.Max.Y-12)
		rowColor := rowBG
		if t > 1.75 { // Enter: the row lights up
			rowColor = color.NRGBA{0x4e, 0x50, 0x58, 0xff}
		}
		fillRound(dst, row, 8, rowColor, pa)
		place(dst, r.a.icon, float64(row.Min.X+42), float64(row.Min.Y+44), 52.0/float64(r.a.icon.Bounds().Dx()), pa)
		text(dst, r.f.barBold, "/verdict", row.Min.X+86, row.Min.Y+40, white, pa)
		text(dst, r.f.barSmall, "Let the court judge your gaming habits", row.Min.X+86, row.Min.Y+74, discDim, pa)
		label := "Player.gg"
		text(dst, r.f.barSmall, label, row.Max.X-24-font.MeasureString(r.f.barSmall, label).Ceil(), row.Min.Y+56, discDim, pa)
	}
}

func (r *renderer) beat(dst *image.RGBA, b beat, local, dur float64) {
	alpha := presence(local, dur, 0.4, 0.3)
	enter := ease(clamp(local / 0.55))
	zoom := 1 + 0.035*local/dur
	dy := (1 - enter) * 50
	if b.img == nil { // profile card beside the collage
		pScale := fit(r.a.profile, 760, 840)
		cScale := fit(r.a.collage, 820, 840) * 0.95
		pw := float64(r.a.profile.Bounds().Dx()) * pScale
		cw := float64(r.a.collage.Bounds().Dx()) * cScale
		gap := 46.0
		left := (W - (pw + gap + cw)) / 2
		r.card(dst, r.a.profile, left+pw/2, 470+dy, pScale*zoom, alpha)
		r.card(dst, r.a.collage, left+pw+gap+cw/2, 470+dy+(1-ease(clamp((local-0.12)/0.55)))*30, cScale*zoom, alpha*ease(clamp((local-0.12)/0.4)))
	} else {
		img := b.img(r.a)
		bw, bh := float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
		if bh/bw > 1.05 {
			// Tall cards: show them bigger and pan slowly down them.
			scale := math.Min(1.5, 1100/bw)
			h := bh * scale
			p := smooth(clamp((local - 0.35) / (dur - 0.8)))
			from, to := 40+h/2, 925-h/2
			r.card(dst, img, W/2, from+(to-from)*p+dy, scale, alpha)
		} else {
			r.card(dst, img, W/2, 465+dy, fit(img, 1440, 850)*zoom, alpha)
		}
	}
	r.caption(dst, b.command, b.cap, alpha*ease(clamp((local-0.25)/0.35)))
}

func (r *renderer) endCard(dst *image.RGBA, t float64) {
	in := func(at float64) float64 { return ease(clamp((t - at) / 0.45)) }
	// Covers fanned across the top.
	for i, c := range r.a.covers {
		ang := []float64{-8, 0, 8}[i]
		cx := []float64{W/2 - 230, W / 2, W/2 + 230}[i]
		cy := []float64{300, 270, 300}[i]
		a := in(0.1 + 0.12*float64(i))
		scale := 250.0 / float64(c.Bounds().Dx())
		r.shadow(dst, c, cx, cy+20+(1-a)*40, scale, ang, a*0.8)
		placeRot(dst, c, cx, cy+(1-a)*40, scale, ang, a)
	}
	// Logo and name.
	ta := in(0.45)
	name := "Player.gg"
	nw := font.MeasureString(r.f.title, name).Ceil()
	iconSize := 112.0
	total := int(iconSize) + 28 + nw
	x0 := W/2 - total/2
	place(dst, r.a.icon, float64(x0)+iconSize/2, 606-(1-ta)*20, iconSize/float64(r.a.icon.Bounds().Dx()), ta)
	text(dst, r.f.title, name, x0+int(iconSize)+28, 650-int((1-ta)*20), white, ta)
	tg := "Your games, your server, your verdict."
	text(dst, r.f.tagline, tg, W/2-font.MeasureString(r.f.tagline, tg).Ceil()/2, 734, soft, in(0.7))
	// Add to Discord.
	ba := in(0.95)
	pop := 1 + 0.06*math.Sin(clamp((t-0.95)/0.4)*math.Pi)
	bw, bh := 520*pop, 96*pop
	btn := image.Rect(int(W/2-bw/2), int(830-bh/2), int(W/2+bw/2), int(830+bh/2))
	fillRound(dst, btn, 14, blurple, ba)
	label := "ADD TO DISCORD"
	text(dst, r.f.button, label, W/2-font.MeasureString(r.f.button, label).Ceil()/2, 843, white, ba)
	small := "Free on Discord  ·  Link Steam once, use it everywhere"
	text(dst, r.f.small, small, W/2-font.MeasureString(r.f.small, small).Ceil()/2, 938, muted, in(1.2))
}

// card draws an image with a soft shadow, centred on (cx, cy).
func (r *renderer) card(dst *image.RGBA, img image.Image, cx, cy, scale, alpha float64) {
	r.shadow(dst, img, cx, cy+18, scale, 0, alpha*0.9)
	placeRot(dst, img, cx, cy, scale, 0, alpha)
}

func (r *renderer) shadow(dst *image.RGBA, img image.Image, cx, cy, scale, angle, alpha float64) {
	b := img.Bounds()
	w, h := float64(b.Dx())*scale, float64(b.Dy())*scale
	for i := 3; i >= 1; i-- {
		grow := float64(i) * 14
		sh := image.NewUniform(color.NRGBA{0, 0, 0, 0})
		_ = sh
		rect := image.Rect(int(cx-w/2-grow), int(cy-h/2-grow+grow/2), int(cx+w/2+grow), int(cy+h/2+grow+grow/2))
		if angle == 0 {
			fillRound(dst, rect, int(18+grow), color.NRGBA{0, 0, 0, 255}, alpha*0.11)
		}
	}
}

func (r *renderer) caption(dst *image.RGBA, command, s string, alpha float64) {
	if alpha <= 0 {
		return
	}
	cw := font.MeasureString(r.f.chip, command).Ceil() + 36
	tw := font.MeasureString(r.f.caption, s).Ceil()
	pad := 22
	total := cw + 18 + tw + 2*pad
	x0 := W/2 - total/2
	box := image.Rect(x0, 948, x0+total, 1036)
	fillRound(dst, box, 18, color.NRGBA{0x0b, 0x0a, 0x12, 0xff}, alpha*0.88)
	chip := image.Rect(x0+pad, 966, x0+pad+cw, 1018)
	fillRound(dst, chip, 26, chipBG, alpha)
	text(dst, r.f.chip, command, chip.Min.X+18, 1002, color.NRGBA{0xee, 0xea, 0xff, 0xff}, alpha)
	text(dst, r.f.caption, s, chip.Max.X+18, 1006, white, alpha)
}

// ----- drawing helpers -----

func background() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			t := float64(x)/W*0.55 + float64(y)/H*0.45
			img.SetRGBA(x, y, lerp(color.RGBA{0x0d, 0x0b, 0x17, 0xff}, color.RGBA{0x22, 0x18, 0x45, 0xff}, t))
		}
	}
	return img
}

func glowSprite(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)-c, float64(y)-c) / c
			a := math.Max(0, 1-d)
			a = a * a * 0.42
			img.SetNRGBA(x, y, color.NRGBA{0x7c, 0x5c, 0xff, uint8(a * 255)})
		}
	}
	return img
}

func fit(img image.Image, maxW, maxH float64) float64 {
	b := img.Bounds()
	return math.Min(1.6, math.Min(maxW/float64(b.Dx()), maxH/float64(b.Dy())))
}

func place(dst *image.RGBA, src image.Image, cx, cy, scale, alpha float64) {
	placeRot(dst, src, cx, cy, scale, 0, alpha)
}

// placeRot draws src scaled and rotated (degrees) around (cx, cy) at an opacity.
func placeRot(dst *image.RGBA, src image.Image, cx, cy, scale, angle, alpha float64) {
	if alpha <= 0.003 {
		return
	}
	th := angle * math.Pi / 180
	sb := src.Bounds()
	w, h := float64(sb.Dx()), float64(sb.Dy())
	a, b := scale*math.Cos(th), -scale*math.Sin(th)
	d, e := scale*math.Sin(th), scale*math.Cos(th)
	m := f64.Aff3{a, b, cx - (a*w/2 + b*h/2), d, e, cy - (d*w/2 + e*h/2)}
	var opts *draw.Options
	if alpha < 0.997 {
		opts = &draw.Options{SrcMask: image.NewUniform(color.Alpha{uint8(alpha * 255)})}
	}
	draw.BiLinear.Transform(dst, m, src, sb, draw.Over, opts)
}

// fillRound fills a rounded rectangle at an opacity.
func fillRound(dst *image.RGBA, r image.Rectangle, rad int, c color.NRGBA, alpha float64) {
	if alpha <= 0.003 {
		return
	}
	rad = min(rad, r.Dx()/2, r.Dy()/2)
	mask := image.NewAlpha(r)
	full := uint8(clamp(alpha) * float64(c.A))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cx := min(max(x, r.Min.X+rad), r.Max.X-rad-1)
			cy := min(max(y, r.Min.Y+rad), r.Max.Y-rad-1)
			dx, dy := float64(x-cx), float64(y-cy)
			cov := clamp(float64(rad) + 0.5 - math.Hypot(dx, dy))
			if x >= r.Min.X+rad && x < r.Max.X-rad || y >= r.Min.Y+rad && y < r.Max.Y-rad {
				cov = 1
			}
			mask.SetAlpha(x, y, color.Alpha{uint8(cov * float64(full))})
		}
	}
	draw.DrawMask(dst, r, image.NewUniform(color.NRGBA{c.R, c.G, c.B, 255}), image.Point{}, mask, r.Min, draw.Over)
}

func circleAt(dst *image.RGBA, cx, cy, rad int, c color.NRGBA, alpha float64) {
	fillRound(dst, image.Rect(cx-rad, cy-rad, cx+rad, cy+rad), rad, c, alpha)
}

func text(dst *image.RGBA, f font.Face, s string, x, y int, c color.NRGBA, alpha float64) {
	if s == "" || alpha <= 0.003 {
		return
	}
	c.A = uint8(float64(c.A) * clamp(alpha))
	(&font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, y)}).DrawString(s)
}

func fade(dst *image.RGBA, amount float64) {
	k := 1 - clamp(amount)
	for i := 0; i < len(dst.Pix); i += 4 {
		dst.Pix[i] = uint8(float64(dst.Pix[i]) * k)
		dst.Pix[i+1] = uint8(float64(dst.Pix[i+1]) * k)
		dst.Pix[i+2] = uint8(float64(dst.Pix[i+2]) * k)
	}
}

// smooth eases in and out.
func smooth(t float64) float64 { t = clamp(t); return t * t * (3 - 2*t) }

func ease(t float64) float64 { t = clamp(t); return 1 - math.Pow(1-t, 3) }
func clamp(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

func lerp(a, b color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 0xff}
}

// ----- loading -----

func loadAssets(media, iconPath string) (assets, error) {
	var a assets
	var err error
	load := func(name string) image.Image {
		if err != nil {
			return nil
		}
		var f *os.File
		if f, err = os.Open(filepath.Join(media, name)); err != nil {
			return nil
		}
		defer f.Close()
		var img image.Image
		img, _, err = image.Decode(f)
		return img
	}
	a.verdict, a.four, a.profile = load("card-verdict.png"), load("shelf-four.jpg"), load("card-profile.png")
	a.collage, a.backlog, a.goty = load("shelf-collage.jpg"), load("card-backlog.png"), load("card-goty.png")
	a.playtests = load("card-playtests.png")
	if err != nil {
		return a, err
	}
	f, err := os.Open(iconPath)
	if err != nil {
		return a, err
	}
	icon, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return a, err
	}
	rounded := image.NewRGBA(image.Rect(0, 0, 256, 256))
	draw.CatmullRom.Scale(rounded, rounded.Bounds(), icon, icon.Bounds(), draw.Src, nil)
	roundCorners(rounded, 56)
	a.icon = rounded
	for _, id := range []string{"367520", "1145350", "1086940"} { // Hollow Knight, Hades II, Baldur's Gate 3
		resp, err := http.Get("https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/" + id + "/library_600x900.jpg")
		if err != nil {
			return a, err
		}
		img, _, err := image.Decode(resp.Body)
		resp.Body.Close()
		if err != nil {
			return a, fmt.Errorf("cover %s: %w", id, err)
		}
		a.covers = append(a.covers, img)
	}
	return a, nil
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

func loadFaces() (faces, error) {
	var f faces
	var err error
	face := func(file string, size float64) font.Face {
		if err != nil {
			return nil
		}
		var raw []byte
		if raw, err = os.ReadFile(filepath.Join(`C:\Windows\Fonts`, file)); err != nil {
			return nil
		}
		var ft *opentype.Font
		if ft, err = opentype.Parse(raw); err != nil {
			return nil
		}
		var fc font.Face
		fc, err = opentype.NewFace(ft, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		return fc
	}
	f.hook = face("seguibl.ttf", 104)
	f.caption = face("segoeuib.ttf", 44)
	f.chip = face("segoeuib.ttf", 32)
	f.title = face("seguibl.ttf", 124)
	f.tagline = face("segoeuib.ttf", 48)
	f.small = face("segoeui.ttf", 32)
	f.bar = face("segoeui.ttf", 36)
	f.barBold = face("segoeuib.ttf", 34)
	f.barSmall = face("segoeui.ttf", 26)
	f.button = face("segoeuib.ttf", 38)
	return f, err
}
