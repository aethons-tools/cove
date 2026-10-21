package intercom

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func el(id string, version, nonce int64, deleted bool) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"id": id, "type": "rectangle", "version": version, "versionNonce": nonce, "isDeleted": deleted, "x": 1})
	return b
}

func TestElementOfAndWins(t *testing.T) {
	e, err := ElementOf(el("a", 3, 9, false))
	if err != nil || e.ID != "a" || e.Version != 3 || e.Nonce != 9 || e.Deleted {
		t.Fatalf("ElementOf = %+v, %v", e, err)
	}
	for name, doc := range map[string]json.RawMessage{
		"not json": json.RawMessage(`nope`),
		"no id":    json.RawMessage(`{"version":1}`),
		"too big":  json.RawMessage(`{"id":"a","text":"` + strings.Repeat("x", 64<<10) + `"}`),
	} {
		if _, err := ElementOf(doc); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	mk := func(v, n int64) Element { return Element{ID: "a", Version: v, Nonce: n} }
	if !Wins(mk(4, 9), mk(3, 1)) || Wins(mk(3, 9), mk(4, 1)) {
		t.Fatal("higher version wins")
	}
	if !Wins(mk(3, 1), mk(3, 9)) || Wins(mk(3, 9), mk(3, 1)) || Wins(mk(3, 5), mk(3, 5)) {
		t.Fatal("equal version: lower nonce wins; identical never replaces")
	}
}
