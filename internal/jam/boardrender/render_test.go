package boardrender

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"testing"
	"time"
)

func doc(m map[string]any) json.RawMessage {
	if _, ok := m["strokeColor"]; !ok {
		m["strokeColor"] = "#1e1e1e"
	}
	if _, ok := m["strokeWidth"]; !ok {
		m["strokeWidth"] = 2
	}
	if _, ok := m["opacity"]; !ok {
		m["opacity"] = 100
	}
	b, _ := json.Marshal(m)
	return b
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// at maps a scene point to the image, given the render's bounds.
func near(c color.Color, r, g, b uint8) bool {
	cr, cg, cb, _ := c.RGBA()
	d := func(x uint32, y uint8) bool { v := int(x>>8) - int(y); return v < 40 && v > -40 }
	return d(cr, r) && d(cg, g) && d(cb, b)
}

func TestRenderShapes(t *testing.T) {
	els := []json.RawMessage{
		doc(map[string]any{"id": "r", "type": "rectangle", "x": 0, "y": 0, "width": 200, "height": 100, "backgroundColor": "#ff0000", "fillStyle": "solid"}),
		doc(map[string]any{"id": "e", "type": "ellipse", "x": 300, "y": 0, "width": 100, "height": 100, "backgroundColor": "#00ff00", "fillStyle": "solid"}),
		doc(map[string]any{"id": "d", "type": "diamond", "x": 0, "y": 200, "width": 100, "height": 100, "backgroundColor": "#0000ff", "fillStyle": "solid"}),
		doc(map[string]any{"id": "a", "type": "arrow", "x": 200, "y": 50, "width": 100, "height": 0, "points": [][]float64{{0, 0}, {100, 0}}, "endArrowhead": "arrow"}),
		doc(map[string]any{"id": "t", "type": "text", "x": 300, "y": 200, "width": 120, "height": 25, "text": "Hello", "fontSize": 20}),
		doc(map[string]any{"id": "f", "type": "freedraw", "x": 150, "y": 220, "points": [][]float64{{0, 0}, {20, 30}, {40, 0}}}),
		doc(map[string]any{"id": "gone", "type": "rectangle", "x": 1000, "y": 1000, "width": 50, "height": 50, "isDeleted": true}),
		doc(map[string]any{"id": "u", "type": "mystery", "x": 450, "y": 200, "width": 60, "height": 40}),
	}
	b, err := Render(els, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, b)
	// Bounds: x -20..530, y -20..320 (padding 20), scale 1 (< 1600).
	pt := func(x, y int) color.Color { return img.At(x+20, y+20) }
	if w, h := img.Bounds().Dx(), img.Bounds().Dy(); w != 550 || h != 340 {
		t.Fatalf("size %dx%d, want 550x340 (the deleted element excluded)", w, h)
	}
	if !near(pt(100, 50), 255, 0, 0) || !near(pt(350, 50), 0, 255, 0) || !near(pt(50, 250), 0, 0, 255) {
		t.Fatalf("fills: %v %v %v", pt(100, 50), pt(350, 50), pt(50, 250))
	}
	if !near(pt(0, 50), 0x1e, 0x1e, 0x1e) {
		t.Fatalf("rectangle stroke at its left edge: %v", pt(0, 50))
	}
	if !near(pt(250, 50), 0x1e, 0x1e, 0x1e) {
		t.Fatalf("arrow shaft: %v", pt(250, 50))
	}
	dark := 0
	for x := 300; x < 360; x++ {
		for y := 200; y < 225; y++ {
			if near(pt(x, y), 0x1e, 0x1e, 0x1e) {
				dark++
			}
		}
	}
	if dark < 30 {
		t.Fatalf("text drew %d dark pixels", dark)
	}
	if !near(pt(10, 10), 255, 0, 0) || near(pt(-10, -10), 255, 0, 0) {
		t.Fatal("background white outside shapes")
	}
}

func TestRenderEmptyAndScale(t *testing.T) {
	b, err := Render(nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if img := decode(t, b); img.Bounds().Dx() == 0 {
		t.Fatal("an empty board renders a small blank image")
	}
	big := []json.RawMessage{doc(map[string]any{"id": "r", "type": "rectangle", "x": 0, "y": 0, "width": 4000, "height": 1000})}
	b, _ = Render(big, nil, 480)
	if img := decode(t, b); img.Bounds().Dx() != 480 {
		t.Fatalf("scaled width %d, want 480", img.Bounds().Dx())
	}
}

func TestRenderBoundTextInContainer(t *testing.T) {
	els := []json.RawMessage{
		doc(map[string]any{"id": "r", "type": "rectangle", "x": 0, "y": 0, "width": 200, "height": 80, "boundElements": []map[string]string{{"id": "l", "type": "text"}}}),
		doc(map[string]any{"id": "l", "type": "text", "x": 10, "y": 28, "width": 180, "height": 25, "text": "API gateway", "fontSize": 20, "containerId": "r", "textAlign": "center"}),
	}
	if _, err := Render(els, nil, 0); err != nil {
		t.Fatal(err)
	}
}

func TestRenderHostileInputsStayCheap(t *testing.T) {
	start := time.Now()
	els := []json.RawMessage{doc(map[string]any{"id": "t", "type": "text", "x": 0, "y": 0, "width": 100, "height": 25, "text": "big", "fontSize": 1e6})}
	if _, err := Render(els, nil, 0); err != nil {
		t.Fatal(err)
	}
	var many []json.RawMessage
	for i := range 3000 {
		many = append(many, doc(map[string]any{"id": "r" + strconv.Itoa(i), "type": "rectangle", "x": float64(i % 50), "y": float64(i / 50), "width": 1500, "height": 1500, "backgroundColor": "#ff0000", "fillStyle": "solid"}))
	}
	if _, err := Render(many, nil, 0); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []json.RawMessage{
		json.RawMessage(`{"id":"a","type":"rectangle","x":1e308,"y":0,"width":10,"height":10}`),
		json.RawMessage(`{"id":"b","type":"rectangle","x":-1e308,"y":0,"width":10,"height":10}`),
		json.RawMessage(`{"id":"c","type":"rectangle","x":0,"y":0,"width":1e9,"height":1e9}`),
	} {
		if _, err := Render([]json.RawMessage{bad, els[0]}, nil, 0); err != nil {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("hostile renders took %v", d)
	}
}

// Draw order follows Excalidraw's fractional index, not id.
func TestRenderOrderByIndex(t *testing.T) {
	els := []json.RawMessage{
		doc(map[string]any{"id": "a-label", "type": "text", "x": 10, "y": 10, "width": 80, "height": 25, "text": "XXXX", "fontSize": 30, "index": "a1"}),
		doc(map[string]any{"id": "z-box", "type": "rectangle", "x": 0, "y": 0, "width": 200, "height": 60, "backgroundColor": "#ff0000", "fillStyle": "solid", "index": "a0"}),
	}
	b, _ := Render(els, nil, 0)
	img := decode(t, b)
	dark := 0
	for x := 30; x < 100; x++ {
		for y := 30; y < 60; y++ {
			if near(img.At(x, y), 0x1e, 0x1e, 0x1e) {
				dark++
			}
		}
	}
	if dark < 20 {
		t.Fatal("the label (higher index) must draw over the box")
	}
}
