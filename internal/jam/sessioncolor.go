package jam

import (
	"math"
	"regexp"
	"strconv"
)

// Session colors (docs/usage/jam/session-colors.md): each session gets one
// of SessionPalette when raised, the one farthest (OKLab ΔE) from the colors
// of the sessions live now.

// SessionPalette is 24 OKLCH hues 15° apart at lightness 0.52 and chroma up
// to 0.14, as sRGB; each reads at 4.5:1 or better under white text.
var SessionPalette = [...]string{
	"#aa3f4e", "#ab4235", "#a84811", "#9a5500", "#8d5e05", "#816501", "#736c01", "#607204",
	"#427a17", "#0a7e3a", "#057c59", "#027a6b", "#047879", "#007785", "#027493", "#0470a5",
	"#2769b7", "#4a61b9", "#6359b5", "#7652ac", "#874c9f", "#94468f", "#9e427b", "#a63f66",
}

var colorRE = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// oklabOf is hex (#rrggbb) in OKLab; ok=false for anything else.
func oklabOf(hex string) ([3]float64, bool) {
	if !colorRE.MatchString(hex) {
		return [3]float64{}, false
	}
	var c [3]float64
	for i := range c {
		v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		s := float64(v) / 255
		if s <= 0.04045 {
			c[i] = s / 12.92
		} else {
			c[i] = math.Pow((s+0.055)/1.055, 2.4)
		}
	}
	l := math.Cbrt(0.4122214708*c[0] + 0.5363325363*c[1] + 0.0514459929*c[2])
	m := math.Cbrt(0.2119034982*c[0] + 0.6806995451*c[1] + 0.1073969566*c[2])
	s := math.Cbrt(0.0883024619*c[0] + 0.2817188376*c[1] + 0.6299787005*c[2])
	return [3]float64{
		0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}, true
}

// PickSessionColor is the palette color whose nearest live color is
// farthest; a tie goes to the earlier palette color. Invalid live colors are
// ignored.
func PickSessionColor(live []string) string {
	var labs [][3]float64
	for _, c := range live {
		if l, ok := oklabOf(c); ok {
			labs = append(labs, l)
		}
	}
	best, bestD := SessionPalette[0], -1.0
	for _, c := range SessionPalette {
		p, _ := oklabOf(c)
		d := math.Inf(1)
		for _, l := range labs {
			d = math.Min(d, math.Sqrt((p[0]-l[0])*(p[0]-l[0])+(p[1]-l[1])*(p[1]-l[1])+(p[2]-l[2])*(p[2]-l[2])))
		}
		if d > bestD {
			best, bestD = c, d
		}
	}
	return best
}
