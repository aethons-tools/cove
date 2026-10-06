package jam

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
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
	// Raised before tags (and kits) were recorded.
	if err := store.PutInstance(Instance{ActorID: "old", Project: "default", Role: "dev", Phase: PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "old"); s != "unknown" {
		t.Fatalf("no recorded tag or kit = %q, want unknown", s)
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
	if _, err := sup.CurrentImage("default", "dev"); err == nil {
		t.Fatal("CurrentImage with a missing model-spec must error")
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

// A role that no longer raises any kit stales a studio raised from one.
func TestCoveSummariesImageStaleWhenRoleDropsKit(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if s := summaryImage(t, store, sup, "w1"); s != "stale" {
		t.Fatalf("role dropped its kit = %q, want stale", s)
	}
}

// Without a recorded tag — raised before tags were recorded, or a launcher that
// cannot name images — the recorded kit still detects a changed image: a
// different kit or build digest is stale; the same one is unknown (Jam's own
// build inputs can't be compared).
func TestCoveSummariesImageFallsBackToKit(t *testing.T) {
	t.Run("pre-tag instance", func(t *testing.T) {
		fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
		sup, store := imageKit(t, fl)
		cur, err := sup.CurrentImage("default", "dev")
		if err != nil || !cur.HasKit {
			t.Fatalf("current = %+v, %v", cur, err)
		}
		put := func(id string, k KitRef) {
			if err := store.PutInstance(Instance{ActorID: id, Project: "default", Role: "dev", Phase: PhaseLive, Kit: k}); err != nil {
				t.Fatal(err)
			}
		}
		put("same", cur.Kit)
		old := cur.Kit
		old.Digest = "olddigest"
		put("older", old)
		if s := summaryImage(t, store, sup, "same"); s != "unknown" {
			t.Fatalf("pre-tag, same digest = %q, want unknown", s)
		}
		if s := summaryImage(t, store, sup, "older"); s != "stale" {
			t.Fatalf("pre-tag, other digest = %q, want stale", s)
		}
	})
	t.Run("launcher without tags", func(t *testing.T) {
		fl := &fakeLauncher{liveness: LivenessAlive}
		sup, store, _ := supTestKit(t, struct{ Launcher }{fl})
		if _, err := EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRole("default", Role{Name: "dev", Kit: "web", Scope: Scope{TTL: time.Hour}}); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
			t.Fatal(err)
		}
		if s := summaryImage(t, store, sup, "w1"); s != "unknown" {
			t.Fatalf("same kit, no tags = %q, want unknown", s)
		}
		if _, err := EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind, Egress: []string{"example.com"}}); err != nil {
			t.Fatal(err)
		}
		if s := summaryImage(t, store, sup, "w1"); s != "stale" {
			t.Fatalf("kit bump, no tags = %q, want stale", s)
		}
	})
}

// Only a studio that is (or is becoming) a running cove has an image status;
// terminating and gone ones report "-" and are never resolved.
func TestCoveSummariesImageOnlyForRunningPhases(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Phase{PhaseLive, PhaseIdled, PhaseRaising} {
		inst.Phase = p
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		if s := summaryImage(t, store, sup, "w1"); s != "ok" {
			t.Fatalf("%s = %q, want ok", p, s)
		}
	}
	for _, p := range []Phase{PhaseTerminating, PhaseGone, PhaseLost} {
		inst.Phase = p
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		if s := summaryImage(t, store, sup, "w1"); s != "-" {
			t.Fatalf("%s = %q, want -", p, s)
		}
	}
}

// A resolution error is logged at debug level so "unknown" can be explained —
// at most once per role per minute.
func TestCurrentImageErrorLoggedOncePerRolePerMinute(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	_, store := imageKit(t, fl)
	var buf bytes.Buffer
	clk := time.Unix(1000, 0)
	sup := NewSupervisor(store, fl, "h", time.Minute, 30*time.Second, func() time.Time { return clk },
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err := store.PutRole("default", Role{Name: "dev", Kit: "web", ModelSpec: "gone", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	logged := func() int { return strings.Count(buf.String(), "image status unresolvable") }
	for range 3 {
		if _, err := sup.CurrentImage("default", "dev"); err == nil {
			t.Fatal("want an error")
		}
	}
	if n := logged(); n != 1 || !strings.Contains(buf.String(), "level=DEBUG") || !strings.Contains(buf.String(), "gone") {
		t.Fatalf("logged %d times:\n%s", n, buf.String())
	}
	clk = clk.Add(time.Minute)
	if _, err := sup.CurrentImage("default", "dev"); err == nil {
		t.Fatal("want an error")
	}
	if n := logged(); n != 2 {
		t.Fatalf("after a minute logged %d times, want 2", n)
	}
}

// The current tag is cached: repeated listings (the UI polls every 3s) parse
// and hash the kit once until the kit, the role's harness or the launcher's
// assembly changes.
func TestCurrentImageIsCached(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if s := summaryImage(t, store, sup, "w1"); s != "ok" {
			t.Fatalf("status = %q", s)
		}
	}
	if n := sup.kitRefs.misses; n != 1 {
		t.Fatalf("kit resolved %d times over 3 listings, want 1", n)
	}
	fl.asm = "asm2" // the assembly is not cached: picked up at once
	if s := summaryImage(t, store, sup, "w1"); s != "stale" {
		t.Fatalf("after asm change = %q, want stale", s)
	}
	if _, err := EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind, Egress: []string{"x.example"}}); err != nil {
		t.Fatal(err)
	}
	summaryImage(t, store, sup, "w1")
	if n := sup.kitRefs.misses; n != 2 {
		t.Fatalf("a kit bump must re-resolve: misses = %d, want 2", n)
	}
}

// standing list asks only for its role's studios.
func TestRoleCoveSummariesFilters(t *testing.T) {
	store := NewMemStore()
	for _, i := range []Instance{
		{ActorID: "a", Project: "default", Role: "dev", Phase: PhaseLive},
		{ActorID: "b", Project: "default", Role: "ops", Phase: PhaseLive},
		{ActorID: "c", Project: "acme", Role: "dev", Phase: PhaseLive},
	} {
		if err := store.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	got := RoleCoveSummaries(store, nil, "", "dev")
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("RoleCoveSummaries(default/dev) = %+v", got)
	}
}

// Raise and CurrentImage share one resolver: a role under a non-default
// model-spec harness raises exactly the image CurrentImage reports.
func TestRaiseAndCurrentImageAgreeUnderPinnedHarness(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	pinned := modelspec.Default("anthropic")
	pinned.Name, pinned.Version = "pinned", "2.1.100"
	if err := store.PutModelSpec(pinned); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "dev", Kit: "web", ModelSpec: "pinned",
		Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	cur, err := sup.CurrentImage("default", "dev")
	if err != nil || cur.Tag != inst.ImageTag || cur.Kit != inst.Kit {
		t.Fatalf("current = %+v (%v), raised tag %q kit %+v", cur, err, inst.ImageTag, inst.Kit)
	}
}

// GET /admin/coves?role= (and ?project=) lists one role's studios.
func TestAdminCovesRoleFilter(t *testing.T) {
	h, store := newTestAdmin(t)
	for _, i := range []Instance{
		{ActorID: "a", Project: "default", Role: "dev", Phase: PhaseLive},
		{ActorID: "b", Project: "default", Role: "ops", Phase: PhaseLive},
	} {
		if err := store.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	rec := doReq(t, h, "GET", "/admin/coves?project=default&role=dev", nil)
	var got []CoveSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("filtered = %+v", got)
	}
}
