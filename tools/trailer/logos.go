package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Studio logos come from captures of FirstLook's "Trusted by" wall, drawn on
// black in a 2 x 5 grid (400 x 169 cells) per page. They stay out of git:
// FirstLook shows them with the studios' permission, so check with FirstLook
// before using the trailer publicly.

const (
	logoCellW, logoCellH = 400, 169
	logoPerPage          = 10
)

// extractLogos cuts each logo out of the capture pages, keys out the black
// backdrop and writes NN.png files plus names.txt.
func extractLogos(pages, names []string, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	var lines []string
	for i, name := range names {
		page := i / logoPerPage
		if page >= len(pages) {
			return fmt.Errorf("no capture page for %s", name)
		}
		f, err := os.Open(pages[page])
		if err != nil {
			return err
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			return err
		}
		k := i % logoPerPage
		cell := image.Rect((k%2)*logoCellW, (k/2)*logoCellH, (k%2)*logoCellW+logoCellW, (k/2)*logoCellH+logoCellH)
		if cell.Max.X > 784 { // the page's scrollbar
			cell.Max.X = 784
		}
		logo := keyBlack(img, cell)
		out, err := os.Create(filepath.Join(outDir, fmt.Sprintf("%02d.png", i)))
		if err != nil {
			return err
		}
		png.Encode(out, logo)
		out.Close()
		lines = append(lines, fmt.Sprintf("%02d %s", i, name))
	}
	return os.WriteFile(filepath.Join(outDir, "names.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// keyBlack turns the black backdrop transparent and trims to the logo.
func keyBlack(src image.Image, cell image.Rectangle) *image.NRGBA {
	minX, minY, maxX, maxY := cell.Max.X, cell.Max.Y, cell.Min.X, cell.Min.Y
	alpha := func(c color.Color) (uint8, uint8, uint8, float64) {
		r, g, b, _ := c.RGBA()
		r8, g8, b8 := uint8(r>>8), uint8(g>>8), uint8(b>>8)
		m := max(r8, g8, b8)
		return r8, g8, b8, clamp((float64(m) - 16) / 48)
	}
	for y := cell.Min.Y; y < cell.Max.Y; y++ {
		for x := cell.Min.X; x < cell.Max.X; x++ {
			if _, _, _, a := alpha(src.At(x, y)); a > 0.05 {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < minX {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	out := image.NewNRGBA(image.Rect(0, 0, maxX-minX+1, maxY-minY+1))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			r, g, b, a := alpha(src.At(x, y))
			if a <= 0 {
				continue
			}
			// The captured colour was blended with black; undo that.
			un := func(v uint8) uint8 { return uint8(min(255, float64(v)/a)) }
			out.SetNRGBA(x-minX, y-minY, color.NRGBA{un(r), un(g), un(b), uint8(a * 255)})
		}
	}
	return out
}

// loadLogos reads the extracted logos in order.
func loadLogos(dir string) ([]image.Image, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	sort.Strings(files)
	var out []image.Image
	for _, f := range files {
		r, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(r)
		r.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, nil
}
