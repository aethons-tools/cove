package launcher

import "testing"

// fakeImageChecker records the tag asked about and returns a scripted answer —
// standing in for the substrate backend's HasKitImage.
type fakeImageChecker struct {
	present  bool
	err      error
	askedTag string
}

func (f *fakeImageChecker) HasKitImage(tag string) (bool, error) {
	f.askedTag = tag
	return f.present, f.err
}

func TestBackendInventoryHasByImageTag(t *testing.T) {
	c := &fakeImageChecker{present: true}
	inv := backendInventory{ops: c}
	ok, err := inv.Has(KitRef{ID: "web", Version: 2, Digest: "deadbeef"})
	if err != nil || !ok {
		t.Fatalf("Has = %v,%v want true,nil", ok, err)
	}
	if c.askedTag != "cove-kit:deadbeef" {
		t.Fatalf("inventory queried tag %q, want cove-kit:deadbeef", c.askedTag)
	}
}

func TestBackendInventoryMissWhenAbsent(t *testing.T) {
	c := &fakeImageChecker{present: false}
	if ok, _ := (backendInventory{ops: c}).Has(KitRef{ID: "managed", Version: 9}); ok {
		t.Fatal("Has must be false when the image is absent")
	}
}
