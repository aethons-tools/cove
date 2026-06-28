package awake

import "testing"

// New() must return a usable Inhibitor on every platform: Inhibit returns no
// error, a non-nil release, and release is safe to call more than once.
func TestNewInhibitContract(t *testing.T) {
	release, err := New().Inhibit()
	if err != nil {
		t.Fatalf("Inhibit: %v", err)
	}
	if release == nil {
		t.Fatal("release must not be nil")
	}
	release()
	release() // idempotent: a second release must not panic
}
