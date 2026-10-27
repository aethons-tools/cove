package jam

import (
	"context"
	"testing"
)

// PrepareImage on a named manager prepares there (PrepareKitOn), never on the
// launcher's default, and reports that manager's tag.
func TestPrepareImageOnNamedManager(t *testing.T) {
	p := newPlacingFake("m0", "m1")
	sup, store, _ := supTestKit(t, p)
	withDefaultKit(t, sup, store)
	cur, st, err := sup.PrepareImage(context.Background(), "default", "guest", "m1")
	if err != nil || !cur.HasKit {
		t.Fatalf("PrepareImage = %+v, %+v, %v", cur, st, err)
	}
	if p.by["m1"].prepareCalls != 1 || p.by["m0"].prepareCalls != 0 {
		t.Fatalf("prepare calls m1=%d m0=%d; want 1 and 0", p.by["m1"].prepareCalls, p.by["m0"].prepareCalls)
	}
	want, _ := sup.CurrentImage("default", "guest", "m1")
	if cur.Tag != want.Tag || cur.Key() != want.Key() {
		t.Errorf("prepared %+v, CurrentImage on m1 %+v", cur, want)
	}
	// An empty manager is the launcher's default.
	if _, _, err := sup.PrepareImage(context.Background(), "default", "guest", ""); err != nil {
		t.Fatal(err)
	}
	if p.by["m0"].prepareCalls != 1 {
		t.Errorf("unpinned prepare calls on default = %d, want 1", p.by["m0"].prepareCalls)
	}
}
