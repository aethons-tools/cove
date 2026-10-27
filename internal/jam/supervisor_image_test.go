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

// A pin to a manager no longer configured is a reset (place fresh), as a raise
// treats it: CurrentImage and PrepareImage use the default manager instead of
// failing as unknown, so an upgrade does not stick in "preparing".
func TestImageOnUnregisteredManagerUsesDefault(t *testing.T) {
	p := newPlacingFake("m0", "m1")
	p.by["m0"].prepareState = KitReady
	sup, store, _ := supTestKit(t, p)
	withDefaultKit(t, sup, store)
	def, err := sup.CurrentImage("default", "guest", "")
	if err != nil {
		t.Fatal(err)
	}
	cur, err := sup.CurrentImage("default", "guest", "gone")
	if err != nil || cur.Tag != def.Tag || cur.Tag == "" {
		t.Fatalf("CurrentImage on gone = %+v, %v; want the default's %+v", cur, err, def)
	}
	cur, st, err := sup.PrepareImage(context.Background(), "default", "guest", "gone")
	if err != nil || st.State != KitReady || cur.Tag != def.Tag {
		t.Fatalf("PrepareImage on gone = %+v, %+v, %v; want ready on the default", cur, st, err)
	}
	if p.by["m0"].prepareCalls != 1 || p.by["m1"].prepareCalls != 0 {
		t.Fatalf("prepare calls m0=%d m1=%d; want 1 and 0", p.by["m0"].prepareCalls, p.by["m1"].prepareCalls)
	}
}
