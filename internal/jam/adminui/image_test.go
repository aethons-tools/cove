package adminui_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/studio"
)

// taggingLauncher names images like the real launcher (kit digest + a Jam-side
// fingerprint), so a test can stale every raised image by changing asm.
type taggingLauncher struct {
	fakeLauncher
	asm *string
}

func (l taggingLauncher) ImageTag(ref jam.KitRef) string {
	return "cove-kit:" + ref.Digest + "-" + *l.asm
}

// A cove raised on an image its role would no longer run is flagged in the
// studios table's Image column and on the role page's standing row.
func TestImageStaleIsFlagged(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if _, err := jam.EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "review", Kit: "web", Allocation: jam.RoleAllocation{
		Standing: []jam.StandingSession{{Name: "nightly", Prompt: "sweep"}}}}); err != nil {
		t.Fatal(err)
	}
	asm := "a1"
	sup := jam.NewSupervisor(store, taggingLauncher{asm: &asm}, "test-holder", time.Minute, 30*time.Second, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	id := jam.StandingActorID("acme", "review", "nightly")
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: id, Project: "acme", Role: "review",
		Name: "nightly", SessionKind: "standing"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), sup, nil, anyCred, nil)

	page := get(t, h, "/ui/coves").Body.String()
	if !strings.Contains(page, "<th>Image</th>") || !strings.Contains(page, "<td>ok</td>") {
		t.Fatalf("fresh image not shown ok; page:\n%s", page)
	}
	if strings.Contains(get(t, h, "/ui/roles/acme/review").Body.String(), "image stale") {
		t.Fatal("a fresh standing studio must not be flagged")
	}

	asm = "a2" // a Jam-side rebuild: every raised image is now stale
	page = get(t, h, "/ui/coves").Body.String()
	if !strings.Contains(page, `title="raised on an older image than its role would run now">stale</span>`) {
		t.Fatalf("stale image not flagged; page:\n%s", page)
	}
	if !strings.Contains(get(t, h, "/ui/roles/acme/review").Body.String(), ">image stale</span>") {
		t.Fatal("stale standing studio not flagged on the role page")
	}
}
