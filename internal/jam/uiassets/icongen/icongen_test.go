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
		if err != nil || got.Bounds() != want.Bounds() {
			t.Fatalf("%s drifted; run `just icons`", s.Name)
		}
		if !withinTolerance(got, want, 2) {
			t.Fatalf("%s drifted; run `just icons`", s.Name)
		}
	}
}

func withinTolerance(a, b image.Image, tol int) bool {
	bd := a.Bounds()
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			ar, ag, ab, aa := color.RGBAModel.Convert(a.At(x, y)).(color.RGBA).RGBA()
			br, bg, bb, ba := color.RGBAModel.Convert(b.At(x, y)).(color.RGBA).RGBA()
			for _, p := range [][2]uint32{{ar, br}, {ag, bg}, {ab, bb}, {aa, ba}} {
				d := int(p[0]>>8) - int(p[1]>>8)
				if d < -tol || d > tol {
					return false
				}
			}
		}
	}
	return true
}
