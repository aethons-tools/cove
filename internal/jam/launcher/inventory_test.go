package launcher

import (
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestInventoryHasByImageTag(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "sha256:abc\n"}}}
	inv := newColimaInventory(f)
	ok, err := inv.Has(KitRef{ID: "managed", Version: 2})
	if err != nil || !ok {
		t.Fatalf("Has = %v,%v want true,nil", ok, err)
	}
	// the inspect targeted the (id,version) tag
	if last := f.Calls[len(f.Calls)-1]; !containsArg(last.Args, "cove-kit:managed-v2") {
		t.Fatalf("inspect did not target the kit tag: %v", last.Args)
	}
}

func TestInventoryMissOnInspectError(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Err: &runner.ExitError{Code: 1}}}}
	if ok, _ := newColimaInventory(f).Has(KitRef{ID: "managed", Version: 9}); ok {
		t.Fatal("Has must be false when the image is absent")
	}
}

func containsArg(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}
