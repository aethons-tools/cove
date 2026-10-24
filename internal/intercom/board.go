package intercom

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/ident"
)

// Whiteboards (docs/superpowers/specs/2026-10-21-intercom-whiteboard-design.md):
// a board belongs to one channel; its elements are Excalidraw element
// documents, one row each, merged by Excalidraw's own rule (Wins). A merge
// stamps every accepted element with the board's next seq, so a reader
// asking for changes since a seq never misses one.

// BoardPartType is the squawk part announcing a board: {"board_id": …}.
const BoardPartType = "application/vnd.jam.board+json"

// Board is a whiteboard in a channel.
type Board struct {
	ID        ident.ID  `json:"id"`
	Channel   ident.ID  `json:"channel"`
	CreatedBy ident.ID  `json:"created_by"`
	Title     string    `json:"title"`
	Created   time.Time `json:"created"`
	Seq       int64     `json:"seq"`
}

// Element is one board element: its merge fields and its document.
type Element struct {
	ID      string
	Version int64
	Nonce   int64
	Deleted bool
	Seq     int64
	Doc     json.RawMessage
}

// Limits on a merge.
const (
	MaxElementBytes = 64 << 10
	MaxMergeBatch   = 500
)

// ElementOf parses an Excalidraw element document's merge fields.
func ElementOf(doc json.RawMessage) (Element, error) {
	if len(doc) > MaxElementBytes {
		return Element{}, fmt.Errorf("%w: element over %d bytes", ErrInvalid, MaxElementBytes)
	}
	if !utf8.Valid(doc) {
		return Element{}, fmt.Errorf("%w: element is not valid UTF-8", ErrInvalid)
	}
	// Decode the top level by exact key (encoding/json folds case, so
	// "Version" would otherwise set the merge version the client never sees).
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return Element{}, fmt.Errorf("%w: element: %v", ErrInvalid, err)
	}
	var e Element
	if err := json.Unmarshal(top["id"], &e.ID); err != nil || e.ID == "" {
		return Element{}, fmt.Errorf("%w: element has no id", ErrInvalid)
	}
	if len(e.ID) > 128 || strings.ContainsRune(e.ID, 0) {
		return Element{}, fmt.Errorf("%w: element id must be 1–128 bytes, no NUL", ErrInvalid)
	}
	var err error
	if e.Version, err = safeInt(top["version"]); err != nil {
		return Element{}, fmt.Errorf("%w: version: %v", ErrInvalid, err)
	}
	if e.Nonce, err = safeInt(top["versionNonce"]); err != nil {
		return Element{}, fmt.Errorf("%w: versionNonce: %v", ErrInvalid, err)
	}
	if v, ok := top["isDeleted"]; ok {
		_ = json.Unmarshal(v, &e.Deleted)
	}
	e.Doc = doc
	return e, nil
}

// maxSafeInt is the largest integer a JavaScript number holds exactly.
const maxSafeInt = 1<<53 - 1

// safeInt reads a non-negative integer a browser can round-trip (absent: 0).
func safeInt(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, err
	}
	if f < 0 || f > maxSafeInt || f != float64(int64(f)) {
		return 0, fmt.Errorf("want an integer 0–2^53")
	}
	return int64(f), nil
}

// Wins reports whether incoming replaces stored: a higher version, or the
// same version with a lower nonce (Excalidraw's reconcile rule).
func Wins(incoming, stored Element) bool {
	if incoming.Version != stored.Version {
		return incoming.Version > stored.Version
	}
	return incoming.Nonce < stored.Nonce
}

// checkBatch validates a merge batch's size.
func checkBatch(els []Element) error {
	if len(els) > MaxMergeBatch {
		return fmt.Errorf("%w: over %d elements in one merge", ErrInvalid, MaxMergeBatch)
	}
	return nil
}

// ErrNoBoard is a merge into a board that doesn't exist.
var ErrNoBoard = fmt.Errorf("intercom: no such board")
