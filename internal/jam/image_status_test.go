package jam

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/studio"
)

// imageKit is a supervisor over a store with role default/dev raising studio kit
// "web", and a launcher that names images like the real one (kit digest + asm).
func imageKit(t *testing.T, fl *fakeLauncher) (*Supervisor, Store) {
	t.Helper()
	sup, store, _ := supTestKit(t, fl)
	if _, err := EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "dev", Kit: "web", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	return sup, store
}

func summaryImage(t *testing.T, store Store, img ImageResolver, id string) string {
	t.Helper()
	for _, c := range CoveSummaries(store, img) {
		if c.ID == id {
			return c.Image
		}
	}
	t.Fatalf("%s missing from CoveSummaries", id)
	return ""
}

// A raise records the tag the launcher ran the cove's kit under.
func TestRaiseRecordsImageTag(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	want := fl.ImageTag(fl.gotSpec.Kit)
	if inst.ImageTag != want || want == "" {
		t.Fatalf("instance image tag = %q, want %q", inst.ImageTag, want)
	}
	if got, _ := store.GetInstance("w1"); got.ImageTag != want {
		t.Fatalf("stored image tag = %q, want %q", got.ImageTag, want)
	}
}

// A launcher that cannot name its images (or a raise with no kit) records none.
func TestRaiseWithoutImageTaggerRecordsNoTag(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, struct{ Launcher }{fl})
	if _, err := EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "dev", Kit: "web", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if inst.ImageTag != "" {
		t.Fatalf("image tag = %q, want none", inst.ImageTag)
	}
	if s := summaryImage(t, store, sup, "w1"); s != "unknown" {
		t.Fatalf("status = %q, want unknown", s)
	}
}

func TestCoveSummariesImageOK(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "w1"); s != "ok" {
		t.Fatalf("fresh raise = %q, want ok", s)
	}
	// A prompt-only kit edit bumps the version but not the image: still ok.
	if _, err := EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}, Prompt: "new"}); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "w1"); s != "ok" {
		t.Fatalf("after prompt-only bump = %q, want ok", s)
	}
}

func TestCoveSummariesImageStaleOnKitBump(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com", "example.com"}}); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "w1"); s != "stale" {
		t.Fatalf("after kit bump = %q, want stale", s)
	}
}

func TestCoveSummariesImageStaleOnHarnessChange(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	pinned := modelspec.Default("anthropic")
	pinned.Name, pinned.Version = "pinned", "2.1.100"
	if err := store.PutModelSpec(pinned); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "dev", Kit: "web", ModelSpec: "pinned",
		Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "w1"); s != "stale" {
		t.Fatalf("after harness change = %q, want stale", s)
	}
}

// A Jam-side change (the launcher's assembly fingerprint) also stales the image.
func TestCoveSummariesImageStaleOnAsmChange(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	fl.asm = "asm2"
	if s := summaryImage(t, store, sup, "w1"); s != "stale" {
		t.Fatalf("after asm change = %q, want stale", s)
	}
}

func TestCoveSummariesImageUnknown(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	// Raised before tags were recorded.
	if err := store.PutInstance(Instance{ActorID: "old", Project: "default", Role: "dev", Phase: PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "old"); s != "unknown" {
		t.Fatalf("no recorded tag = %q, want unknown", s)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	// No resolver (no supervisor wired): unknown, never an error.
	if s := summaryImage(t, store, nil, "w1"); s != "unknown" {
		t.Fatalf("nil resolver = %q, want unknown", s)
	}
	var nilSup *Supervisor
	if s := summaryImage(t, store, nilSup, "w1"); s != "unknown" {
		t.Fatalf("nil supervisor = %q, want unknown", s)
	}
	// Resolution error: the role's model-spec binding points at a missing spec.
	if err := store.PutRole("default", Role{Name: "dev", Kit: "web", ModelSpec: "gone",
		Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sup.CurrentImageTag("default", "dev"); err == nil {
		t.Fatal("CurrentImageTag with a missing model-spec must error")
	}
	if s := summaryImage(t, store, sup, "w1"); s != "unknown" {
		t.Fatalf("resolution error = %q, want unknown", s)
	}
	// The role is gone entirely: unknown.
	if err := store.RemoveRole("default", "dev"); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "w1"); s != "unknown" {
		t.Fatalf("role gone = %q, want unknown", s)
	}
}
