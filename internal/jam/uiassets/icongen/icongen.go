// Package icongen renders the Jam app's PNG icons from the J mark SVG — pure
// Go, so `just icons` needs no system tools. The output is committed under
// uiassets/static/icons; a test regenerates and checks for drift.
package icongen

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// Spec describes one icon file.
type Spec struct {
	Name       string      // file name, e.g. "icon-192.png"
	Size       int         // square edge in px
	Background color.Color // nil = transparent
	Scale      float64     // fraction of the edge the mark's box occupies
}

var white = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}

// Specs are the five icons the app ships.
var Specs = []Spec{
	{Name: "favicon-32.png", Size: 32, Scale: 0.92},
	{Name: "icon-192.png", Size: 192, Scale: 0.92},
	{Name: "icon-512.png", Size: 512, Scale: 0.92},
	{Name: "icon-maskable-512.png", Size: 512, Background: white, Scale: 0.68}, // painted pixels leave the 0.4R safe circle above ~0.70
	{Name: "apple-touch-icon.png", Size: 180, Background: white, Scale: 0.92},
}

// Render draws the SVG centred in a Size×Size image per s.
func Render(svg []byte, s Spec) (*image.RGBA, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svg), oksvg.StrictErrorMode)
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, s.Size, s.Size))
	if s.Background != nil {
		draw.Draw(img, img.Bounds(), image.NewUniform(s.Background), image.Point{}, draw.Src)
	}
	box := float64(s.Size) * s.Scale
	off := (float64(s.Size) - box) / 2
	icon.SetTarget(off, off, box, box)
	icon.Draw(rasterx.NewDasher(s.Size, s.Size, rasterx.NewScannerGV(s.Size, s.Size, img, img.Bounds())), 1)
	return img, nil
}

// Encode writes img as a best-compression PNG.
func Encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
