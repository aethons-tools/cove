package jam

import (
	"math"
	"regexp"
	"strconv"
	"testing"
)

// luminance is hex's WCAG relative luminance.
func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	var c [3]float64
	for i := range c {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		s := float64(v) / 255
		if s <= 0.04045 {
			c[i] = s / 12.92
		} else {
			c[i] = math.Pow((s+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

func TestSessionPaletteContrast(t *testing.T) {
	if len(SessionPalette) != 24 {
		t.Fatalf("palette has %d colors, want 24", len(SessionPalette))
	}
	hexRE := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	seen := map[string]bool{}
	for _, c := range SessionPalette {
		if !hexRE.MatchString(c) {
			t.Fatalf("%q is not #rrggbb", c)
		}
		if seen[c] {
			t.Fatalf("%s twice", c)
		}
		seen[c] = true
		if cr := 1.05 / (luminance(t, c) + 0.05); cr < 4.5 {
			t.Errorf("%s: contrast %.2f with white, want >= 4.5", c, cr)
		}
	}
}

func dE(a, b [3]float64) float64 {
	return math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
}

func TestPickSessionColor(t *testing.T) {
	first := SessionPalette[0]
	// farthest from [0], by brute force (first wins a tie)
	l0, _ := oklabOf(first)
	want, best := "", -1.0
	for _, c := range SessionPalette {
		l, _ := oklabOf(c)
		if d := dE(l, l0); d > best {
			want, best = c, d
		}
	}
	cases := []struct {
		name string
		live []string
		want string
	}{
		{"no live sessions", nil, first},
		{"farthest from one", []string{first}, want},
		{"ignores invalid live colors", []string{"", "nope", "#12"}, first},
		{"tie broken by palette order", []string{first, first}, want},
	}
	for _, tc := range cases {
		if got := PickSessionColor(tc.live); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
	if want == first {
		t.Fatal("farthest from [0] is [0]")
	}
	t.Run("full palette repeats farthest", func(t *testing.T) {
		live := SessionPalette[:]
		got := PickSessionColor(live)
		in := false
		for _, c := range SessionPalette {
			in = in || c == got
		}
		if !in || got != PickSessionColor(live) {
			t.Fatalf("got %q", got)
		}
	})
}
