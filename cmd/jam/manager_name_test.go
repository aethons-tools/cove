package main

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/fleet"
)

// jam cannot import fleet, so its studio-name migration spells the local
// manager's name itself: the two must agree, or the migration would skip
// every local instance raised under a manager.
func TestLocalManagerNameIsFleets(t *testing.T) {
	if jam.LocalManagerName != fleet.LocalName {
		t.Fatalf("jam.LocalManagerName = %q, fleet.LocalName = %q", jam.LocalManagerName, fleet.LocalName)
	}
}
