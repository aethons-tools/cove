package jam

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/boardrender"
)

func TestBoardOpsAddReadUpdateDelete(t *testing.T) {
	ops := []BoardOp{
		{Op: "add", Type: "rectangle", ID: "api", X: fptr(0), Y: fptr(0), Text: "API gateway"},
		{Op: "add", Type: "ellipse", ID: "db", X: fptr(400), Y: fptr(0), W: fptr(160), H: fptr(80), Text: "Postgres", Color: "#1971c2"},
		{Op: "add", Type: "arrow", ID: "a1", From: "api", To: "db", Text: "SQL"},
	}
	docs, added, err := ApplyBoardOps(nil, ops)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(added, ",") != "api,db,a1" {
		t.Fatalf("added = %v", added)
	}
	compact := CompactBoard(docs)
	byID := map[string]CompactElement{}
	for _, c := range compact {
		byID[c.ID] = c
	}
	api, db, arrow := byID["api"], byID["db"], byID["a1"]
	if api.Type != "rectangle" || api.Text != "API gateway" || api.W <= 0 || api.H <= 0 {
		t.Fatalf("api = %+v", api)
	}
	if db.Text != "Postgres" || db.Color != "#1971c2" || db.W != 160 {
		t.Fatalf("db = %+v", db)
	}
	if arrow.From != "api" || arrow.To != "db" || arrow.Text != "SQL" {
		t.Fatalf("arrow = %+v", arrow)
	}
	// Bound labels are folded in, not listed on their own.
	for _, c := range compact {
		if c.Type == "text" {
			t.Fatalf("a bound label surfaced as its own element: %+v", c)
		}
	}
	// It renders.
	if _, err := boardrender.Render(docs, nil, 0); err != nil {
		t.Fatal(err)
	}

	// Update moves and relabels; versions go up.
	upd, _, err := ApplyBoardOps(docs, []BoardOp{{Op: "update", ID: "api", X: fptr(50), Text: "Edge"}})
	if err != nil {
		t.Fatal(err)
	}
	var moved map[string]any
	for _, d := range upd {
		var m map[string]any
		_ = json.Unmarshal(d, &m)
		if m["id"] == "api" {
			moved = m
		}
	}
	if moved["x"].(float64) != 50 || moved["version"].(float64) < 2 {
		t.Fatalf("moved = %v", moved)
	}
	if c := CompactBoard(merge(docs, upd)); find(c, "api").Text != "Edge" {
		t.Fatalf("relabel lost: %+v", find(c, "api"))
	}

	// Delete removes the shape and its label.
	del, _, err := ApplyBoardOps(docs, []BoardOp{{Op: "delete", ID: "db"}})
	if err != nil {
		t.Fatal(err)
	}
	if c := CompactBoard(merge(docs, del)); find(c, "db").ID != "" {
		t.Fatal("deleted shape still listed")
	}
	if len(del) < 2 {
		t.Fatalf("delete should tombstone the shape and its label: %d docs", len(del))
	}
}

func TestBoardOpsAtomic(t *testing.T) {
	_, _, err := ApplyBoardOps(nil, []BoardOp{
		{Op: "add", Type: "rectangle", ID: "a", X: fptr(0), Y: fptr(0)},
		{Op: "add", Type: "arrow", From: "a", To: "missing"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("an arrow to a missing shape: %v", err)
	}
	for _, bad := range []BoardOp{{Op: "add", Type: "hexagon"}, {Op: "update", ID: "nope"}, {Op: "fly"}} {
		if _, _, err := ApplyBoardOps(nil, []BoardOp{bad}); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func find(cs []CompactElement, id string) CompactElement {
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	return CompactElement{}
}

// merge overlays changed docs onto docs by id (what the store does).
func merge(docs, changed []json.RawMessage) []json.RawMessage {
	idx := map[string]int{}
	out := append([]json.RawMessage(nil), docs...)
	for i, d := range out {
		var m struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(d, &m)
		idx[m.ID] = i
	}
	for _, d := range changed {
		var m struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(d, &m)
		if i, ok := idx[m.ID]; ok {
			out[i] = d
		} else {
			out = append(out, d)
		}
	}
	return out
}

func fptr(v float64) *float64 { return &v }

func TestBoardOpsReaddDeletedAndIndex(t *testing.T) {
	docs, _, _ := ApplyBoardOps(nil, []BoardOp{{Op: "add", Type: "rectangle", ID: "box", X: fptr(0), Y: fptr(0)}, {Op: "add", Type: "rectangle", ID: "two", X: fptr(200), Y: fptr(0)}})
	idx := map[string]string{}
	for _, d := range docs {
		var m map[string]any
		_ = json.Unmarshal(d, &m)
		idx[m["id"].(string)], _ = m["index"].(string)
	}
	if idx["box"] == "" || idx["two"] == "" || !(idx["box"] < idx["two"]) {
		t.Fatalf("indexes = %v, want increasing in add order", idx)
	}
	del, _, _ := ApplyBoardOps(docs, []BoardOp{{Op: "delete", ID: "box"}})
	all := merge(docs, del)
	re, _, err := ApplyBoardOps(all, []BoardOp{{Op: "add", Type: "ellipse", ID: "box", X: fptr(0), Y: fptr(0)}})
	if err != nil {
		t.Fatal(err)
	}
	var tomb, readd float64
	for _, d := range del {
		var m map[string]any
		_ = json.Unmarshal(d, &m)
		if m["id"] == "box" {
			tomb = m["version"].(float64)
		}
	}
	for _, d := range re {
		var m map[string]any
		_ = json.Unmarshal(d, &m)
		if m["id"] == "box" {
			readd = m["version"].(float64)
		}
	}
	if readd <= tomb {
		t.Fatalf("re-added version %v must beat the tombstone's %v", readd, tomb)
	}
}

func TestNextIndex(t *testing.T) {
	for in, want := range map[string]string{"": "a0", "a0": "a1", "a9": "aA", "az": "b00", "b0z": "b10", "bzz": "c000", "a5V": "a6", "Zz": "a0"} {
		if got := nextIndex(in); got != want || got <= in {
			t.Errorf("nextIndex(%q) = %q, want %q", in, got, want)
		}
	}
}

func elDoc(docs []json.RawMessage, id string) map[string]any {
	for _, d := range docs {
		var m map[string]any
		_ = json.Unmarshal(d, &m)
		if m["id"] == id {
			return m
		}
	}
	return nil
}

func lastPoint(m map[string]any) (float64, float64) {
	pts := m["points"].([]any)
	p := pts[len(pts)-1].([]any)
	return p[0].(float64), p[1].(float64)
}

// A one-ended arrow pointing up-left keeps its free end when its shape moves;
// moving an arrow itself moves its points; deleting a shape unbinds its arrows
// from the other end, and a deleted frame frees its children.
func TestBoardOpsGeometryAndDeletes(t *testing.T) {
	docs, _, err := ApplyBoardOps(nil, []BoardOp{
		{Op: "add", Type: "rectangle", ID: "s", X: fptr(300), Y: fptr(300), W: fptr(100), H: fptr(60)},
		{Op: "add", Type: "arrow", ID: "a", From: "s", X: fptr(100), Y: fptr(100), W: fptr(0), H: fptr(0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := elDoc(docs, "a")
	ex, ey := num(a, "x")+mustLast(a)[0], num(a, "y")+mustLast(a)[1]
	moved, _, _ := ApplyBoardOps(docs, []BoardOp{{Op: "update", ID: "s", X: fptr(320)}})
	a2 := elDoc(merge(docs, moved), "a")
	if fx, fy := num(a2, "x")+mustLast(a2)[0], num(a2, "y")+mustLast(a2)[1]; math.Abs(fx-ex) > 0.01 || math.Abs(fy-ey) > 0.01 {
		t.Fatalf("free end moved from (%v,%v) to (%v,%v)", ex, ey, fx, fy)
	}

	all, _, _ := ApplyBoardOps(nil, []BoardOp{
		{Op: "add", Type: "rectangle", ID: "p", X: fptr(0), Y: fptr(0)},
		{Op: "add", Type: "rectangle", ID: "q", X: fptr(300), Y: fptr(0)},
		{Op: "add", Type: "arrow", ID: "pq", From: "p", To: "q"},
		{Op: "add", Type: "line", ID: "free", X: fptr(0), Y: fptr(200), W: fptr(50), H: fptr(0)},
		{Op: "add", Type: "frame", ID: "f", X: fptr(0), Y: fptr(300), W: fptr(400), H: fptr(200), Text: "F"},
		{Op: "add", Type: "rectangle", ID: "child", X: fptr(10), Y: fptr(310), Frame: "f"},
	})
	up, _, _ := ApplyBoardOps(all, []BoardOp{{Op: "update", ID: "free", W: fptr(80), H: fptr(20)}})
	if x, y := lastPoint(elDoc(merge(all, up), "free")); x != 80 || y != 20 {
		t.Fatalf("resized line's points end at (%v,%v), want (80,20)", x, y)
	}
	del, _, _ := ApplyBoardOps(all, []BoardOp{{Op: "delete", ID: "p"}, {Op: "delete", ID: "f"}})
	after := merge(all, del)
	for _, b := range elDoc(after, "q")["boundElements"].([]any) {
		if b.(map[string]any)["id"] == "pq" {
			t.Fatal("q still lists the deleted arrow")
		}
	}
	if fid := elDoc(after, "child")["frameId"]; fid != nil {
		t.Fatalf("child keeps a deleted frame: %v", fid)
	}
}

func mustLast(m map[string]any) [2]float64 { x, y := lastPoint(m); return [2]float64{x, y} }
