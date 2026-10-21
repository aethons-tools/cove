package intercom

import (
	"encoding/json"
	"fmt"
	"time"

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
	var f struct {
		ID           string `json:"id"`
		Version      int64  `json:"version"`
		VersionNonce int64  `json:"versionNonce"`
		IsDeleted    bool   `json:"isDeleted"`
	}
	if err := json.Unmarshal(doc, &f); err != nil {
		return Element{}, fmt.Errorf("%w: element: %v", ErrInvalid, err)
	}
	if f.ID == "" || len(f.ID) > 128 {
		return Element{}, fmt.Errorf("%w: element has no id", ErrInvalid)
	}
	return Element{ID: f.ID, Version: f.Version, Nonce: f.VersionNonce, Deleted: f.IsDeleted, Doc: doc}, nil
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
