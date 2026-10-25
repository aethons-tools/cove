package adminui

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// A malformed mode in a draft's status is a store fault, not mode 0.
func TestDraftReadMalformedMode(t *testing.T) {
	st := jam.KitDraftStatus{Entries: []jam.DraftEntry{{Path: "a", Mode: "07x5", Size: 1}}}
	if _, err := draftRead("app", st, nil); err == nil {
		t.Fatal("malformed mode accepted")
	}
}
