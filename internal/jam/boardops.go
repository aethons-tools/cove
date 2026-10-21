package jam

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
)

// The agent vocabulary for whiteboards (docs/usage/jam/whiteboards.md): a
// compact view of a board (CompactBoard) and a small set of ops
// (ApplyBoardOps) that Jam turns into full Excalidraw elements — bound
// labels, arrow bindings, seeds, versions — so agents never handle them.

// CompactElement is one element as an agent reads it.
type CompactElement struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Text  string  `json:"text,omitempty"`
	From  string  `json:"from,omitempty"`
	To    string  `json:"to,omitempty"`
	Color string  `json:"color,omitempty"`
	Frame string  `json:"frame,omitempty"`
}

// BoardOp is one change an agent asks for. Unset coordinates keep theirs.
type BoardOp struct {
	Op    string   `json:"op"` // add | update | delete
	Type  string   `json:"type,omitempty"`
	ID    string   `json:"id,omitempty"`
	X     *float64 `json:"x,omitempty"`
	Y     *float64 `json:"y,omitempty"`
	W     *float64 `json:"w,omitempty"`
	H     *float64 `json:"h,omitempty"`
	Text  string   `json:"text,omitempty"`
	From  string   `json:"from,omitempty"`
	To    string   `json:"to,omitempty"`
	Color string   `json:"color,omitempty"`
	Frame string   `json:"frame,omitempty"`
}

const defaultStroke = "#1e1e1e"

var addable = map[string]bool{"rectangle": true, "ellipse": true, "diamond": true, "text": true, "arrow": true, "line": true, "frame": true}

type doc = map[string]any

func str(m doc, k string) string { s, _ := m[k].(string); return s }
func num(m doc, k string) float64 {
	v, _ := m[k].(float64)
	return v
}

// CompactBoard is docs as an agent reads them: live elements, a shape's
// bound label folded into its text, an arrow's bound ends as from/to.
func CompactBoard(docs []json.RawMessage) []CompactElement {
	els := parseDocs(docs)
	labels := map[string]string{}
	for _, m := range els {
		if c := str(m, "containerId"); c != "" && str(m, "type") == "text" {
			labels[c] = str(m, "text")
		}
	}
	var out []CompactElement
	for _, id := range sortedIDs(els) {
		m := els[id]
		if str(m, "type") == "text" && str(m, "containerId") != "" {
			continue
		}
		c := CompactElement{ID: id, Type: str(m, "type"), X: num(m, "x"), Y: num(m, "y"), W: num(m, "width"), H: num(m, "height"), Frame: str(m, "frameId")}
		c.Text = labels[id]
		if c.Type == "text" {
			c.Text = str(m, "text")
		}
		if c.Type == "frame" {
			c.Text = str(m, "name")
		}
		if b, ok := m["startBinding"].(doc); ok {
			c.From = str(b, "elementId")
		}
		if b, ok := m["endBinding"].(doc); ok {
			c.To = str(b, "elementId")
		}
		if s := str(m, "strokeColor"); s != "" && s != defaultStroke {
			c.Color = s
		}
		out = append(out, c)
	}
	return out
}

// parseDocs maps the live (not deleted) docs by id.
func parseDocs(docs []json.RawMessage) map[string]doc {
	out := map[string]doc{}
	for _, d := range docs {
		var m doc
		if json.Unmarshal(d, &m) != nil || str(m, "id") == "" {
			continue
		}
		if del, _ := m["isDeleted"].(bool); del {
			continue
		}
		out[str(m, "id")] = m
	}
	return out
}

func sortedIDs(m map[string]doc) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// ApplyBoardOps applies ops to the board's docs, returning the changed
// element docs (to merge) and the ids of shapes added, in order. Atomic:
// any bad op fails the whole batch and changes nothing.
func ApplyBoardOps(existing []json.RawMessage, ops []BoardOp) ([]json.RawMessage, []string, error) {
	a := &applier{els: parseDocs(existing), changed: map[string]bool{}, now: time.Now().UnixMilli(), tombs: map[string]float64{}}
	// Tombstones count: a re-added id must out-version its tombstone, and
	// new elements go above every index already used.
	for _, d := range existing {
		var m doc
		if json.Unmarshal(d, &m) != nil {
			continue
		}
		if ix := str(m, "index"); ix > a.maxIndex {
			a.maxIndex = ix
		}
		if del, _ := m["isDeleted"].(bool); del {
			a.tombs[str(m, "id")] = num(m, "version")
		}
	}
	var added []string
	for i, op := range ops {
		id, err := a.apply(op)
		if err != nil {
			return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Op, err)
		}
		if op.Op == "add" {
			added = append(added, id)
		}
	}
	var out []json.RawMessage
	for _, id := range sortedIDs(a.els) {
		if a.changed[id] {
			b, err := json.Marshal(a.els[id])
			if err != nil {
				return nil, nil, err
			}
			out = append(out, b)
		}
	}
	return out, added, nil
}

type applier struct {
	els      map[string]doc // includes elements deleted in this batch
	changed  map[string]bool
	now      int64
	tombs    map[string]float64 // deleted ids → their version
	maxIndex string             // the highest fractional index in use
}

func (a *applier) live(id string) (doc, bool) {
	m, ok := a.els[id]
	if !ok {
		return nil, false
	}
	if del, _ := m["isDeleted"].(bool); del {
		return nil, false
	}
	return m, true
}

// touch bumps m's version (a new nonce) and marks it changed.
func (a *applier) touch(m doc) {
	m["version"] = num(m, "version") + 1
	m["versionNonce"] = float64(rand.Int32())
	m["updated"] = float64(a.now)
	a.changed[str(m, "id")] = true
}

func newID() string {
	const al = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 20)
	for i := range b {
		b[i] = al[rand.IntN(len(al))]
	}
	return string(b)
}

func (a *applier) base(id, typ string, x, y, w, h float64, color string) doc {
	if color == "" {
		color = defaultStroke
	}
	m := doc{"id": id, "type": typ, "x": x, "y": y, "width": w, "height": h, "angle": 0.0,
		"strokeColor": color, "backgroundColor": "transparent", "fillStyle": "solid", "strokeWidth": 2.0,
		"strokeStyle": "solid", "roughness": 1.0, "opacity": 100.0, "groupIds": []any{}, "frameId": nil,
		"roundness": nil, "seed": float64(rand.Int32()), "version": 0.0, "versionNonce": 0.0,
		"isDeleted": false, "boundElements": []any{}, "updated": float64(a.now), "link": nil, "locked": false}
	a.maxIndex = nextIndex(a.maxIndex)
	m["index"] = a.maxIndex
	a.els[id] = m
	if v, ok := a.tombs[id]; ok { // re-adding a deleted id: beat its tombstone
		m["version"] = v
		delete(a.tombs, id)
	}
	a.touch(m)
	return m
}

func get(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func (a *applier) apply(op BoardOp) (string, error) {
	switch op.Op {
	case "add":
		return a.add(op)
	case "update":
		return op.ID, a.update(op)
	case "delete":
		return op.ID, a.delete(op.ID)
	}
	return "", fmt.Errorf("unknown op %q (add, update, delete)", op.Op)
}

func (a *applier) add(op BoardOp) (string, error) {
	if !addable[op.Type] {
		return "", fmt.Errorf("unknown type %q", op.Type)
	}
	id := op.ID
	if id == "" {
		id = newID()
	}
	if _, ok := a.els[id]; ok {
		return "", fmt.Errorf("id %q exists", id)
	}
	if op.Frame != "" {
		if _, ok := a.live(op.Frame); !ok {
			return "", fmt.Errorf("no frame %q", op.Frame)
		}
	}
	switch op.Type {
	case "arrow", "line":
		return id, a.addConnector(id, op)
	case "text":
		w := get(op.W, textWidth(op.Text, 20))
		m := a.base(id, "text", get(op.X, 0), get(op.Y, 0), w, get(op.H, 25), op.Color)
		setText(m, op.Text, 20)
		a.frame(m, op.Frame)
		return id, nil
	}
	w := get(op.W, math.Max(120, textWidth(op.Text, 20)+40))
	h := get(op.H, 60)
	m := a.base(id, op.Type, get(op.X, 0), get(op.Y, 0), w, h, op.Color)
	if op.Type == "frame" {
		m["name"] = op.Text
	} else if op.Type == "rectangle" {
		m["roundness"] = doc{"type": 3.0}
	}
	a.frame(m, op.Frame)
	if op.Text != "" && op.Type != "frame" {
		a.label(m, op.Text)
	}
	return id, nil
}

func (a *applier) frame(m doc, frame string) {
	if frame != "" {
		m["frameId"] = frame
	}
}

func textWidth(s string, size float64) float64 { return math.Max(20, float64(len(s))*size*0.55) }

func setText(m doc, text string, size float64) {
	m["text"], m["originalText"] = text, text
	m["fontSize"], m["fontFamily"], m["lineHeight"] = size, 1.0, 1.25
	m["textAlign"], m["verticalAlign"], m["autoResize"] = "left", "top", true
	m["containerId"] = nil
}

// label sets container m's bound text, adding it if missing.
func (a *applier) label(m doc, text string) {
	cid := str(m, "id")
	for _, lb := range boundOf(m, "text") {
		if t, ok := a.live(lb); ok {
			setText(t, text, num(t, "fontSize"))
			t["containerId"], t["textAlign"], t["verticalAlign"] = cid, "center", "middle"
			a.placeLabel(m, t)
			a.touch(t)
			return
		}
	}
	t := a.base(newID(), "text", 0, 0, 0, 0, str(m, "strokeColor"))
	setText(t, text, 20)
	t["containerId"], t["textAlign"], t["verticalAlign"] = cid, "center", "middle"
	a.placeLabel(m, t)
	m["boundElements"] = append(listOf(m["boundElements"]), doc{"type": "text", "id": str(t, "id")})
	a.touch(m)
}

func (a *applier) placeLabel(container, t doc) {
	w := math.Max(10, num(container, "width")-20)
	t["width"], t["height"] = w, 25.0
	cx, cy := num(container, "x")+num(container, "width")/2, num(container, "y")+num(container, "height")/2
	if pts := pointsOf(container); len(pts) >= 2 { // a connector: its midpoint
		first, last := pts[0], pts[len(pts)-1]
		cx, cy = num(container, "x")+(first[0]+last[0])/2, num(container, "y")+(first[1]+last[1])/2
		w = textWidth(str(t, "text"), num(t, "fontSize"))
		t["width"] = w
	}
	t["x"], t["y"] = cx-w/2, cy-12.5
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

func boundOf(m doc, typ string) []string {
	var out []string
	for _, b := range listOf(m["boundElements"]) {
		if bm, ok := b.(doc); ok && str(bm, "type") == typ {
			out = append(out, str(bm, "id"))
		}
	}
	return out
}

func pointsOf(m doc) [][2]float64 {
	var out [][2]float64
	for _, p := range listOf(m["points"]) {
		if pl := listOf(p); len(pl) >= 2 {
			x, _ := pl[0].(float64)
			y, _ := pl[1].(float64)
			out = append(out, [2]float64{x, y})
		}
	}
	return out
}

func (a *applier) addConnector(id string, op BoardOp) error {
	var from, to doc
	var ok bool
	if op.From != "" {
		if from, ok = a.live(op.From); !ok {
			return fmt.Errorf("no shape %q to connect from", op.From)
		}
	}
	if op.To != "" {
		if to, ok = a.live(op.To); !ok {
			return fmt.Errorf("no shape %q to connect to", op.To)
		}
	}
	if (from == nil || to == nil) && (op.X == nil || op.Y == nil) {
		return fmt.Errorf("a %s needs from and to, or x, y (and w, h)", op.Type)
	}
	m := a.base(id, op.Type, get(op.X, 0), get(op.Y, 0), 0, 0, op.Color)
	if op.Type == "arrow" {
		m["endArrowhead"], m["startArrowhead"] = "arrow", nil
	}
	if from != nil {
		m["startBinding"] = doc{"elementId": op.From, "focus": 0.0, "gap": 4.0}
		from["boundElements"] = append(listOf(from["boundElements"]), doc{"type": "arrow", "id": id})
		a.touch(from)
	}
	if to != nil {
		m["endBinding"] = doc{"elementId": op.To, "focus": 0.0, "gap": 4.0}
		to["boundElements"] = append(listOf(to["boundElements"]), doc{"type": "arrow", "id": id})
		a.touch(to)
	}
	a.route(m, get(op.W, 100), get(op.H, 0))
	a.frame(m, op.Frame)
	if op.Text != "" {
		a.label(m, op.Text)
	}
	return nil
}

// route sets a connector's position and points: between its bound shapes'
// facing edges, else from (x, y) by (w, h).
func (a *applier) route(m doc, w, h float64) {
	x0, y0 := num(m, "x"), num(m, "y")
	x1, y1 := x0+w, y0+h
	from, fok := a.bound(m, "startBinding")
	to, tok := a.bound(m, "endBinding")
	switch {
	case fok && tok:
		fx, fy := center(from)
		tx, ty := center(to)
		x0, y0 = edge(from, tx, ty)
		x1, y1 = edge(to, fx, fy)
	case fok:
		x0, y0 = edge(from, x1, y1)
	case tok:
		x1, y1 = edge(to, x0, y0)
	}
	m["x"], m["y"] = x0, y0
	m["width"], m["height"] = math.Abs(x1-x0), math.Abs(y1-y0)
	m["points"] = []any{[]any{0.0, 0.0}, []any{x1 - x0, y1 - y0}}
}

func (a *applier) bound(m doc, key string) (doc, bool) {
	b, ok := m[key].(doc)
	if !ok {
		return nil, false
	}
	return a.live(str(b, "elementId"))
}

func center(m doc) (float64, float64) {
	return num(m, "x") + num(m, "width")/2, num(m, "y") + num(m, "height")/2
}

// edge is where the line from m's centre toward (tx, ty) leaves m's box,
// pushed out by a small gap.
func edge(m doc, tx, ty float64) (float64, float64) {
	cx, cy := center(m)
	dx, dy := tx-cx, ty-cy
	hw, hh := num(m, "width")/2+4, num(m, "height")/2+4
	if dx == 0 && dy == 0 {
		return cx, cy
	}
	s := math.Inf(1)
	if dx != 0 {
		s = math.Min(s, hw/math.Abs(dx))
	}
	if dy != 0 {
		s = math.Min(s, hh/math.Abs(dy))
	}
	return cx + dx*s, cy + dy*s
}

func (a *applier) update(op BoardOp) error {
	m, ok := a.live(op.ID)
	if !ok {
		return fmt.Errorf("no element %q", op.ID)
	}
	moved := op.X != nil || op.Y != nil || op.W != nil || op.H != nil
	if op.X != nil {
		m["x"] = *op.X
	}
	if op.Y != nil {
		m["y"] = *op.Y
	}
	if op.W != nil {
		m["width"] = *op.W
	}
	if op.H != nil {
		m["height"] = *op.H
	}
	if op.Color != "" {
		m["strokeColor"] = op.Color
	}
	if op.Frame != "" {
		if _, ok := a.live(op.Frame); !ok {
			return fmt.Errorf("no frame %q", op.Frame)
		}
		m["frameId"] = op.Frame
	}
	t := str(m, "type")
	switch {
	case op.Text != "" && t == "text":
		setText(m, op.Text, num(m, "fontSize"))
	case op.Text != "" && t == "frame":
		m["name"] = op.Text
	case op.Text != "":
		a.label(m, op.Text)
	}
	a.touch(m)
	if moved {
		for _, lb := range boundOf(m, "text") {
			if l, ok := a.live(lb); ok {
				a.placeLabel(m, l)
				a.touch(l)
			}
		}
		for _, arID := range boundOf(m, "arrow") {
			if ar, ok := a.live(arID); ok {
				a.route(ar, num(ar, "width"), num(ar, "height"))
				a.touch(ar)
				for _, lb := range boundOf(ar, "text") {
					if l, ok := a.live(lb); ok {
						a.placeLabel(ar, l)
						a.touch(l)
					}
				}
			}
		}
	}
	return nil
}

// delete tombstones an element, its label, and the connectors bound to it.
func (a *applier) delete(id string) error {
	m, ok := a.live(id)
	if !ok {
		return fmt.Errorf("no element %q", id)
	}
	gone := []string{id}
	gone = append(gone, boundOf(m, "text")...)
	for _, arID := range boundOf(m, "arrow") {
		if ar, ok := a.live(arID); ok {
			gone = append(gone, arID)
			gone = append(gone, boundOf(ar, "text")...)
		}
	}
	for _, g := range gone {
		if e, ok := a.live(g); ok {
			e["isDeleted"] = true
			a.touch(e)
		}
	}
	return nil
}

// indexDigits are fractional-indexing's base62 digits, in ASCII order.
const indexDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// nextIndex is a fractional index (Excalidraw's z-order key) above after:
// the next integer key ("a0" … "az", "b00" … — the head letter says how
// many digits follow). A key below "a0" (or none) gives "a0".
func nextIndex(after string) string {
	if after < "a0" || after[0] < 'a' || after[0] > 'z' {
		return "a0"
	}
	n := int(after[0]-'a') + 1
	if len(after) < 1+n {
		return "a0"
	}
	digits := []byte(after[1 : 1+n])
	for i := len(digits) - 1; i >= 0; i-- {
		d := strings.IndexByte(indexDigits, digits[i])
		if d < len(indexDigits)-1 {
			digits[i] = indexDigits[d+1]
			return string(after[0]) + string(digits)
		}
		digits[i] = '0'
	}
	if after[0] == 'z' {
		return after + "V" // out of integer keys: a fractional step above
	}
	return string(after[0]+1) + strings.Repeat("0", n+1)
}
