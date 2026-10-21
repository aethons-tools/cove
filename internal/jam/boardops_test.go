package jam

import (
	"encoding/json"
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
