package adminui_test

import (
	"context"
	"fmt"
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

// queueUpgrader is a jam.StandingUpgrader queue: QueueUpgrade records the
// name as queued (or fails with err); UpgradeState reports it.
type queueUpgrader struct {
	state map[string]string
	err   error
}

func (q *queueUpgrader) QueueUpgrade(_, _, name string, _ bool) error {
	if q.err != nil {
		return q.err
	}
	q.state[name] = jam.UpgradeQueued
	return nil
}

func (q *queueUpgrader) UpgradeState(_, _, name string) string { return q.state[name] }

// Upgrade (COV-251): each standing row has an Upgrade button (behind a
// confirm), emphasized when the studio's image is stale. The POST queues the
// upgrade with the reconciler and answers with the re-rendered role (its row
// showing the pending state) plus a success flash; an already-current studio
// gets an "already current" flash; a pending reset is a 409 error; without a
// supervisor the button is absent and the POST is 503.
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
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: id, Project: "acme", Role: "review", Name: "nightly", SessionKind: "standing"}); err != nil {
		t.Fatal(err)
	}
	q := &queueUpgrader{state: map[string]string{}}
	sup.SetStandingUpgrader(q)
	h := adminui.Handler(store, testLogger(), sup, nil, anyCred, nil)
	const btn = `hx-post="/ui/roles/acme/review/standing/nightly/upgrade"`
	const flash = `<div id="flash" hx-swap-oob="innerHTML"><p class="ok">`
	const path = "/ui/roles/acme/review/standing/nightly/upgrade"

	body := get(t, h, "/ui/roles/acme/review").Body.String()
	if !strings.Contains(body, `<button class="small" `+btn) || !strings.Contains(body, `hx-confirm="Upgrade standing session nightly?`) {
		t.Fatalf("role page lacks a confirmed upgrade button:\n%s", body)
	}
	if rec := post(t, h, path, url.Values{}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), flash) ||
		!strings.Contains(rec.Body.String(), "already runs the current image") || len(q.state) != 0 {
		t.Fatalf("already current = %d: %s", rec.Code, rec.Body.String())
	}

	asm = "a2" // stale
	if body := get(t, h, "/ui/roles/acme/review").Body.String(); !strings.Contains(body, `<button class="small primary" `+btn) {
		t.Fatalf("a stale studio's upgrade button must be emphasized:\n%s", body)
	}
	rec := post(t, h, path, url.Values{})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), flash) || !strings.Contains(rec.Body.String(), "queued") ||
		!strings.Contains(rec.Body.String(), `id="role"`) || strings.Contains(rec.Body.String(), `class="error"`) {
		t.Fatalf("upgrade = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), ">upgrade: queued</span>") {
		t.Fatalf("the row must show the pending upgrade:\n%s", rec.Body.String())
	}

	q.err = fmt.Errorf("%w: x", jam.ErrStandingResetPending)
	if rec := post(t, h, path, url.Values{}); rec.Code != http.StatusConflict {
		t.Fatalf("during a pending reset = %d: %s", rec.Code, rec.Body.String())
	}

	ro := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	if strings.Contains(get(t, ro, "/ui/roles/acme/review").Body.String(), "/standing/nightly/upgrade") {
		t.Error("no supervisor: the upgrade button must be hidden")
	}
	if rec := post(t, ro, path, url.Values{}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no supervisor: upgrade = %d, want 503", rec.Code)
	}
}
