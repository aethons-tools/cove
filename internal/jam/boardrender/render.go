// Package boardrender draws whiteboard elements (Excalidraw element JSON) to
// PNG in pure Go: clean shapes, arrows, text and freehand strokes, without
// Excalidraw's hand-drawn roughness. It is what agents see (board_snapshot)
// and what a board card's thumbnail shows. See
// docs/superpowers/specs/2026-10-21-intercom-whiteboard-design.md §8.
package boardrender

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

// DefaultMaxSide bounds a render's longer side.
const DefaultMaxSide = 1600

const pad = 20

// element is the part of an Excalidraw element the renderer reads.
type element struct {
	ID              string      `json:"id"`
	Type            string      `json:"type"`
	X               float64     `json:"x"`
	Y               float64     `json:"y"`
	Width           float64     `json:"width"`
	Height          float64     `json:"height"`
	StrokeColor     string      `json:"strokeColor"`
	BackgroundColor string      `json:"backgroundColor"`
	FillStyle       string      `json:"fillStyle"`
	StrokeWidth     float64     `json:"strokeWidth"`
	Opacity         *float64    `json:"opacity"`
	IsDeleted       bool        `json:"isDeleted"`
	Points          [][]float64 `json:"points"`
	EndArrowhead    *string     `json:"endArrowhead"`
	StartArrowhead  *string     `json:"startArrowhead"`
	Text            string      `json:"text"`
	FontSize        float64     `json:"fontSize"`
	TextAlign       string      `json:"textAlign"`
	ContainerID     *string     `json:"containerId"`
	Name            *string     `json:"name"`
	FileID          string      `json:"fileId"`
}

// Render draws els (deleted ones skipped) scaled so the longer side is at
// most maxSide (DefaultMaxSide when <= 0). images resolves an image
// element's fileId (nil: images draw as labelled boxes).
func Render(docs []json.RawMessage, images func(fileID string) (image.Image, bool), maxSide int) ([]byte, error) {
	if maxSide <= 0 {
		maxSide = DefaultMaxSide
	}
	var els []element
	for _, d := range docs {
		var e element
		if json.Unmarshal(d, &e) == nil && !e.IsDeleted {
			els = append(els, e)
		}
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, e := range els {
		x0, y0, x1, y1 := bounds(e)
		minX, minY, maxX, maxY = math.Min(minX, x0), math.Min(minY, y0), math.Max(maxX, x1), math.Max(maxY, y1)
	}
	if len(els) == 0 {
		minX, minY, maxX, maxY = 0, 0, 160, 90
	}
	minX, minY, maxX, maxY = minX-pad, minY-pad, maxX+pad, maxY+pad
	scale := 1.0
	if side := math.Max(maxX-minX, maxY-minY); side > float64(maxSide) {
		scale = float64(maxSide) / side
	}
	w, h := int(math.Ceil((maxX-minX)*scale)), int(math.Ceil((maxY-minY)*scale))
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, w, h)), ox: minX, oy: minY, s: scale}
	draw.Draw(c.img, c.img.Bounds(), image.White, image.Point{}, draw.Src)
	for _, e := range els {
		c.element(e, images)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, c.img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// bounds is an element's scene bounding box.
func bounds(e element) (x0, y0, x1, y1 float64) {
	if len(e.Points) > 0 && (e.Type == "arrow" || e.Type == "line" || e.Type == "freedraw") {
		x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, p := range e.Points {
			if len(p) < 2 {
				continue
			}
			x0, y0, x1, y1 = math.Min(x0, e.X+p[0]), math.Min(y0, e.Y+p[1]), math.Max(x1, e.X+p[0]), math.Max(y1, e.Y+p[1])
		}
		return
	}
	return e.X, e.Y, e.X + e.Width, e.Y + e.Height
}

type canvas struct {
	img    *image.RGBA
	ox, oy float64 // scene origin of the image
	s      float64 // scale
}

func (c *canvas) pt(x, y float64) (float32, float32) {
	return float32((x - c.ox) * c.s), float32((y - c.oy) * c.s)
}

// fill paints the path built by build in col.
func (c *canvas) fill(col color.Color, build func(r *vector.Rasterizer)) {
	b := c.img.Bounds()
	r := vector.NewRasterizer(b.Dx(), b.Dy())
	build(r)
	r.Draw(c.img, b, image.NewUniform(col), image.Point{})
}

// polyline strokes pts (scene coordinates) with width w.
func (c *canvas) polyline(pts [][2]float64, w float64, col color.Color, closed bool) {
	if len(pts) < 2 {
		return
	}
	hw := math.Max(w*c.s, 1) / 2
	n := len(pts)
	if closed {
		pts = append(pts, pts[0])
		n++
	}
	c.fill(col, func(r *vector.Rasterizer) {
		for i := 0; i+1 < n; i++ {
			ax, ay := c.pt(pts[i][0], pts[i][1])
			bx, by := c.pt(pts[i+1][0], pts[i+1][1])
			dx, dy := float64(bx-ax), float64(by-ay)
			l := math.Hypot(dx, dy)
			if l == 0 {
				continue
			}
			nx, ny := float32(-dy/l*hw), float32(dx/l*hw)
			// One winding for every piece (as circle's), or overlapping
			// pieces cancel out in the rasterizer's accumulation.
			q := [4][2]float32{{ax + nx, ay + ny}, {bx + nx, by + ny}, {bx - nx, by - ny}, {ax - nx, ay - ny}}
			if area(q[:]) < 0 {
				q[1], q[3] = q[3], q[1]
			}
			r.MoveTo(q[0][0], q[0][1])
			for _, v := range q[1:] {
				r.LineTo(v[0], v[1])
			}
			r.ClosePath()
		}
		for i := 0; i < n; i++ { // round joins
			x, y := c.pt(pts[i][0], pts[i][1])
			circle(r, x, y, float32(hw))
		}
	})
}

// area is a polygon's signed (shoelace) area.
func area(p [][2]float32) float32 {
	var a float32
	for i := range p {
		j := (i + 1) % len(p)
		a += p[i][0]*p[j][1] - p[j][0]*p[i][1]
	}
	return a / 2
}

// circle adds a circle subpath, wound with positive area (as polyline's
// pieces).
func circle(r *vector.Rasterizer, cx, cy, rad float32) {
	const k = 16
	for i := 0; i <= k; i++ {
		a := 2 * math.Pi * float64(i) / k
		x, y := cx+rad*float32(math.Cos(a)), cy+rad*float32(math.Sin(a))
		if i == 0 {
			r.MoveTo(x, y)
		} else {
			r.LineTo(x, y)
		}
	}
	r.ClosePath()
}

// polygon fills pts (scene coordinates).
func (c *canvas) polygon(pts [][2]float64, col color.Color) {
	c.fill(col, func(r *vector.Rasterizer) {
		for i, p := range pts {
			x, y := c.pt(p[0], p[1])
			if i == 0 {
				r.MoveTo(x, y)
			} else {
				r.LineTo(x, y)
			}
		}
		r.ClosePath()
	})
}

func ellipsePts(e element) [][2]float64 {
	const k = 48
	cx, cy, rx, ry := e.X+e.Width/2, e.Y+e.Height/2, e.Width/2, e.Height/2
	pts := make([][2]float64, k)
	for i := range pts {
		a := 2 * math.Pi * float64(i) / k
		pts[i] = [2]float64{cx + rx*math.Cos(a), cy + ry*math.Sin(a)}
	}
	return pts
}

func (c *canvas) element(e element, images func(string) (image.Image, bool)) {
	stroke := parseColor(e.StrokeColor, e.Opacity, color.RGBA{0x1e, 0x1e, 0x1e, 0xff})
	sw := e.StrokeWidth
	if sw <= 0 {
		sw = 2
	}
	shape := func(pts [][2]float64) {
		if e.BackgroundColor != "" && e.BackgroundColor != "transparent" {
			fillCol := parseColor(e.BackgroundColor, e.Opacity, color.RGBA{})
			if e.FillStyle != "" && e.FillStyle != "solid" {
				fillCol = lighten(fillCol) // hachure/cross-hatch: a lighter wash
			}
			c.polygon(pts, fillCol)
		}
		c.polyline(pts, sw, stroke, true)
	}
	switch e.Type {
	case "rectangle", "frame", "embeddable", "iframe":
		shape([][2]float64{{e.X, e.Y}, {e.X + e.Width, e.Y}, {e.X + e.Width, e.Y + e.Height}, {e.X, e.Y + e.Height}})
		if e.Type == "frame" && e.Name != nil {
			c.text(*e.Name, e.X, e.Y-18, 300, 14, "left", stroke)
		}
	case "ellipse":
		shape(ellipsePts(e))
	case "diamond":
		shape([][2]float64{{e.X + e.Width/2, e.Y}, {e.X + e.Width, e.Y + e.Height/2}, {e.X + e.Width/2, e.Y + e.Height}, {e.X, e.Y + e.Height/2}})
	case "line", "arrow", "freedraw":
		var pts [][2]float64
		for _, p := range e.Points {
			if len(p) >= 2 {
				pts = append(pts, [2]float64{e.X + p[0], e.Y + p[1]})
			}
		}
		if e.Type == "freedraw" {
			sw = math.Max(sw, 2)
		}
		c.polyline(pts, sw, stroke, false)
		if e.Type == "arrow" && len(pts) >= 2 {
			if e.EndArrowhead == nil || *e.EndArrowhead != "" {
				c.arrowhead(pts[len(pts)-2], pts[len(pts)-1], sw, stroke)
			}
			if e.StartArrowhead != nil && *e.StartArrowhead != "" {
				c.arrowhead(pts[1], pts[0], sw, stroke)
			}
		}
	case "text":
		size := e.FontSize
		if size <= 0 {
			size = 20
		}
		c.text(e.Text, e.X, e.Y, e.Width, size, e.TextAlign, stroke)
	case "image":
		if img, ok := lookup(images, e.FileID); ok {
			x0, y0 := c.pt(e.X, e.Y)
			x1, y1 := c.pt(e.X+e.Width, e.Y+e.Height)
			xdraw.ApproxBiLinear.Scale(c.img, image.Rect(int(x0), int(y0), int(x1), int(y1)), img, img.Bounds(), xdraw.Over, nil)
			return
		}
		fallthrough
	default:
		// Unknown (or unresolved image): a labelled box.
		shape([][2]float64{{e.X, e.Y}, {e.X + e.Width, e.Y}, {e.X + e.Width, e.Y + e.Height}, {e.X, e.Y + e.Height}})
		c.text(e.Type, e.X+4, e.Y+4, e.Width-8, 12, "left", stroke)
	}
}

func lookup(images func(string) (image.Image, bool), id string) (image.Image, bool) {
	if images == nil || id == "" {
		return nil, false
	}
	return images(id)
}

func (c *canvas) arrowhead(from, to [2]float64, sw float64, col color.Color) {
	a := math.Atan2(to[1]-from[1], to[0]-from[0])
	l := 10 + 2*sw
	for _, d := range []float64{math.Pi * 5 / 6, -math.Pi * 5 / 6} {
		c.polyline([][2]float64{to, {to[0] + l*math.Cos(a+d), to[1] + l*math.Sin(a+d)}}, sw, col, false)
	}
}

var face = func() *opentype.Font {
	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		panic(err)
	}
	return f
}()

// text draws s from (x, y) (its top-left), wrapping within width (scene
// units) when width > 0, aligned left/center/right.
func (c *canvas) text(s string, x, y, width, size float64, align string, col color.Color) {
	px := size * c.s
	if px < 4 {
		return
	}
	ff, err := opentype.NewFace(face, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return
	}
	defer ff.Close()
	d := &font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: ff}
	maxW := width * c.s
	lineH := px * 1.25
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		lines = append(lines, wrap(d, para, maxW)...)
	}
	x0, y0 := c.pt(x, y)
	for i, ln := range lines {
		adv := float64(d.MeasureString(ln)) / 64
		lx := float64(x0)
		switch align {
		case "center":
			lx += (maxW - adv) / 2
		case "right":
			lx += maxW - adv
		}
		d.Dot = fixed.P(int(lx), int(float64(y0)+px*0.95+float64(i)*lineH))
		d.DrawString(ln)
	}
}

func wrap(d *font.Drawer, s string, maxW float64) []string {
	if maxW <= 0 {
		return []string{s}
	}
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		try := strings.TrimSpace(line + " " + w)
		if line != "" && float64(d.MeasureString(try))/64 > maxW {
			out = append(out, line)
			line = w
			continue
		}
		line = try
	}
	return append(out, line)
}

// parseColor reads "#rgb", "#rrggbb" or "transparent"; opacity is 0–100.
func parseColor(s string, opacity *float64, def color.RGBA) color.RGBA {
	c := def
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) == 6 {
		if v, err := strconv.ParseUint(s, 16, 32); err == nil {
			c = color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}
		}
	}
	if opacity != nil && *opacity >= 0 && *opacity < 100 {
		a := *opacity / 100
		c = color.RGBA{uint8(float64(c.R) * a), uint8(float64(c.G) * a), uint8(float64(c.B) * a), uint8(float64(c.A) * a)}
	}
	return c
}

func lighten(c color.RGBA) color.RGBA {
	return color.RGBA{uint8(int(c.R)/2 + 128), uint8(int(c.G)/2 + 128), uint8(int(c.B)/2 + 128), c.A}
}
