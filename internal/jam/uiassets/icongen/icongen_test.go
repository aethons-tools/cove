package icongen_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/uiassets/icongen"
)

func loadSVG(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../static/brand/j-logo-plain.svg")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func spec(t *testing.T, name string) icongen.Spec {
	t.Helper()
	for _, s := range icongen.Specs {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no spec %s", name)
	return icongen.Spec{}
}

func TestSpecs(t *testing.T) {
	want := map[string]int{"favicon-32.png": 32, "icon-192.png": 192, "icon-512.png": 512, "icon-maskable-512.png": 512, "apple-touch-icon.png": 180}
	if len(icongen.Specs) != len(want) {
		t.Fatalf("got %d specs, want %d", len(icongen.Specs), len(want))
	}
	for _, s := range icongen.Specs {
		size, ok := want[s.Name]
		if !ok || s.Size != size {
			t.Errorf("unexpected spec %s size %d", s.Name, s.Size)
		}
		white := s.Name == "icon-maskable-512.png" || s.Name == "apple-touch-icon.png"
		if white {
			if s.Background == nil {
				t.Errorf("%s needs a white background", s.Name)
				continue
			}
			r, g, b, a := s.Background.RGBA()
			if r != 0xffff || g != 0xffff || b != 0xffff || a != 0xffff {
				t.Errorf("%s background not white", s.Name)
			}
		} else if s.Background != nil {
			t.Errorf("%s should be transparent", s.Name)
		}
	}
}

func near(c color.RGBA, r, g, b uint8) bool {
	d := func(x, y uint8) bool {
		if x > y {
			return x-y <= 8
		}
		return y-x <= 8
	}
	return c.A == 0xff && d(c.R, r) && d(c.G, g) && d(c.B, b)
}

func TestRenderTransparentHasMarkColours(t *testing.T) {
	img, err := icongen.Render(loadSVG(t), spec(t, "icon-512.png"))
	if err != nil {
		t.Fatal(err)
	}
	if img.RGBAAt(0, 0).A != 0 {
		t.Errorf("corner alpha = %d, want 0", img.RGBAAt(0, 0).A)
	}
	var orange, blue bool
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			c := img.RGBAAt(x, y)
			orange = orange || near(c, 0xF7, 0x94, 0x1D)
			blue = blue || near(c, 0x2F, 0x67, 0xB2)
		}
	}
	if !orange || !blue {
		t.Errorf("orange=%v blue=%v, want both", orange, blue)
	}
}

func TestRenderMaskableSafeZone(t *testing.T) {
	img, err := icongen.Render(loadSVG(t), spec(t, "icon-maskable-512.png"))
	if err != nil {
		t.Fatal(err)
	}
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	if img.RGBAAt(0, 0) != white || img.RGBAAt(511, 511) != white {
		t.Fatal("corners not opaque white")
	}
	const c, r = 256.0, 0.4 * 512
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			dx, dy := float64(x)+0.5-c, float64(y)+0.5-c
			if dx*dx+dy*dy > r*r && img.RGBAAt(x, y) != white {
				t.Fatalf("pixel (%d,%d) outside safe zone is %v", x, y, img.RGBAAt(x, y))
			}
		}
	}
}

func TestCommittedIconsMatchGenerator(t *testing.T) {
	svg := loadSVG(t)
	for _, s := range icongen.Specs {
		want, err := icongen.Render(svg, s)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join("..", "static", "icons", s.Name))
		if err != nil {
			t.Fatalf("%s drifted; run `just icons`: %v", s.Name, err)
		}
		got, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s drifted; run `just icons`: decode: %v", s.Name, err)
		}
		if got.Bounds() != want.Bounds() {
			t.Fatalf("%s drifted; run `just icons`: bounds %v, want %v", s.Name, got.Bounds(), want.Bounds())
		}
		n, maxDiff := diffStats(got, want, 2)
		if !driftOK(n, maxDiff, total(got)) {
			t.Fatalf("%s drifted: %d of %d pixels differ by >2 (%.3f%%), maxDiff %d; run `just icons`",
				s.Name, n, total(got), 100*float64(n)/float64(total(got)), maxDiff)
		}
	}
}

// Drift rule: oksvg/rasterx antialiasing differs slightly across CPU
// architectures (Go fuses multiply-add on arm64 but not amd64), so edge pixels
// may be off by more than 2/255 when CI (amd64) checks icons committed from
// arm64. Tolerate a small scattered fraction of such pixels with bounded
// error; real drift (SVG edit, scale, background) changes far more pixels or
// by far more.
const (
	maxDriftFraction = 0.005
	maxDriftChannel  = 48
)

func total(img image.Image) int {
	b := img.Bounds()
	return b.Dx() * b.Dy()
}

func driftOK(n, maxDiff, pixels int) bool {
	return float64(n) <= maxDriftFraction*float64(pixels) && maxDiff <= maxDriftChannel
}

// diffStats returns the number of pixels where any 8-bit channel differs by
// more than tol, and the largest channel difference overall.
func diffStats(a, b image.Image, tol int) (n, maxDiff int) {
	bd := a.Bounds()
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			over := false
			for _, p := range [][2]uint32{{ar, br}, {ag, bg}, {ab, bb}, {aa, ba}} {
				d := int(p[0]>>8) - int(p[1]>>8)
				if d < 0 {
					d = -d
				}
				if d > maxDiff {
					maxDiff = d
				}
				if d > tol {
					over = true
				}
			}
			if over {
				n++
			}
		}
	}
	return n, maxDiff
}

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestDriftComparator(t *testing.T) {
	base := color.RGBA{100, 100, 100, 255}
	check := func(mod func(*image.RGBA)) bool {
		a, b := solid(100, 100, base), solid(100, 100, base)
		mod(b)
		n, m := diffStats(a, b, 2)
		return driftOK(n, m, total(a))
	}
	bump := func(img *image.RGBA, x, y int, d uint8) {
		c := img.RGBAAt(x, y)
		c.R += d
		img.SetRGBA(x, y, c)
	}
	if !check(func(*image.RGBA) {}) {
		t.Error("identical images should pass")
	}
	if !check(func(i *image.RGBA) {
		for k := 0; k < 5; k++ {
			bump(i, k*17, k*13, 30)
		}
	}) {
		t.Error("few scattered pixels off by 30 should pass")
	}
	if check(func(i *image.RGBA) { bump(i, 50, 50, 200) }) {
		t.Error("one pixel off by 200 should fail")
	}
	if check(func(i *image.RGBA) {
		for y := 0; y < 100; y++ {
			for x := 0; x < 2; x++ {
				bump(i, x, y, 10)
			}
		}
	}) {
		t.Error("2% block off by 10 should fail")
	}
}

func TestDriftComparatorCatchesScaleChange(t *testing.T) {
	svg := loadSVG(t)
	s := spec(t, "icon-512.png")
	want, err := icongen.Render(svg, s)
	if err != nil {
		t.Fatal(err)
	}
	s.Scale = 0.90
	got, err := icongen.Render(svg, s)
	if err != nil {
		t.Fatal(err)
	}
	n, m := diffStats(got, want, 2)
	if driftOK(n, m, total(want)) {
		t.Errorf("scale 0.90 vs 0.92 should fail drift check (n=%d maxDiff=%d)", n, m)
	}
}
