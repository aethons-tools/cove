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

func TestElementOfStrict(t *testing.T) {
	bad := map[string]string{
		"version over 2^53":  `{"id":"a","version":9007199254740993}`,
		"negative version":   `{"id":"a","version":-1}`,
		"fractional version": `{"id":"a","version":1.5}`,
		"negative nonce":     `{"id":"a","version":1,"versionNonce":-5}`,
		"NUL in id":          `{"id":"a\u0000b","version":1}`,
		"invalid UTF-8":      "{\"id\":\"a\",\"t\":\"\xff\"}",
		"id over 128 bytes":  `{"id":"` + strings.Repeat("x", 129) + `","version":1}`,
	}
	for name, doc := range bad {
		_, err := ElementOf(json.RawMessage(doc))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
		if name == "id over 128 bytes" && err != nil && strings.Contains(err.Error(), "no id") {
			t.Errorf("a long id is reported as missing: %v", err)
		}
	}
	if _, err := ElementOf(json.RawMessage(`{"id":"ok","version":3,"versionNonce":12345}`)); err != nil {
		t.Fatal(err)
	}
	// Keys match exactly: a case-folded "Version" doesn't set the merge version.
	if e, err := ElementOf(json.RawMessage(`{"id":"a","version":1,"Version":9}`)); err != nil || e.Version != 1 {
		t.Fatalf("case-folded key: %+v %v", e, err)
	}
}

func TestValidatePartsOneBoard(t *testing.T) {
	p := Part{Type: BoardPartType, Data: json.RawMessage(`{"board_id":"brd_01j9q3aaaaaaaaaaaaaaaaaaaa"}`)}
	if err := validateParts([]Part{p, p}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("two board parts: %v", err)
	}
}
