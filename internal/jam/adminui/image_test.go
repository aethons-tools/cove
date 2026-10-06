package adminui_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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

// upgraderFunc adapts a func to jam.StandingUpgrader.
type upgraderFunc func(ctx context.Context, project, role, name string) error

func (f upgraderFunc) UpgradeStanding(ctx context.Context, project, role, name string) error {
	return f(ctx, project, role, name)
}

// Upgrade (COV-251): each standing row has an Upgrade button (behind a
// confirm), emphasized when the studio's image is stale; the POST refuses a
// mid-episode session (409 naming its state) and otherwise re-raises it on the
// current image through the standing reconciler; without a supervisor the
// button is absent and the POST is 503.
func TestEditStandingUpgrade(t *testing.T) {
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
	raise := func(ctx context.Context) error {
		_, _, _, err := sup.Raise(ctx, jam.RaiseSpec{ActorID: id, Project: "acme", Role: "review", Name: "nightly", SessionKind: "standing"})
		return err
	}
	if err := raise(context.Background()); err != nil {
		t.Fatal(err)
	}
	sup.SetStandingUpgrader(upgraderFunc(func(ctx context.Context, _, _, _ string) error {
		if err := sup.Teardown(ctx, id); err != nil {
			return err
		}
		return raise(ctx)
	}))
	setActivity := func(a jam.Activity) {
		inst, _ := store.GetInstance(id)
		inst.Activity = a
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	h := adminui.Handler(store, testLogger(), sup, nil, anyCred, nil)
	const btn = `hx-post="/ui/roles/acme/review/standing/nightly/upgrade"`

	body := get(t, h, "/ui/roles/acme/review").Body.String()
	if !strings.Contains(body, `<button class="small" `+btn) || !strings.Contains(body, `hx-confirm="Upgrade standing session nightly?`) {
		t.Fatalf("role page lacks a confirmed upgrade button:\n%s", body)
	}
	asm = "a2" // stale
	if body := get(t, h, "/ui/roles/acme/review").Body.String(); !strings.Contains(body, `<button class="small primary" `+btn) {
		t.Fatalf("a stale studio's upgrade button must be emphasized:\n%s", body)
	}

	setActivity(jam.ActivityRunning)
	if rec := post(t, h, "/ui/roles/acme/review/standing/nightly/upgrade", url.Values{}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "running") {
		t.Fatalf("busy upgrade = %d: %s", rec.Code, rec.Body.String())
	}
	setActivity(jam.ActivityWaiting)
	if rec := post(t, h, "/ui/roles/acme/review/standing/nightly/upgrade", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("upgrade = %d: %s", rec.Code, rec.Body.String())
	}
	if inst, _ := store.GetInstance(id); !strings.HasSuffix(inst.ImageTag, "-a2") {
		t.Fatalf("re-raised on %q, want the a2 image", inst.ImageTag)
	}
	if body := get(t, h, "/ui/roles/acme/review").Body.String(); strings.Contains(body, "image stale") {
		t.Fatal("an upgraded studio must no longer be flagged stale")
	}

	ro := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	if strings.Contains(get(t, ro, "/ui/roles/acme/review").Body.String(), "/standing/nightly/upgrade") {
		t.Error("no supervisor: the upgrade button must be hidden")
	}
	if rec := post(t, ro, "/ui/roles/acme/review/standing/nightly/upgrade", url.Values{}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no supervisor: upgrade = %d, want 503", rec.Code)
	}
}
