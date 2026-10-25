package adminui_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestDraftPublish(t *testing.T) {
	store, h := draftFixture(t)
	if _, _, err := jam.PushKitFiles(store, "web", kittreetest.Files("kit.yml", "kind: studio\nbase:\n  kit: app\n")); err != nil {
		t.Fatal(err)
	}
	putDraft(t, store, "run.sh", 0, "echo two\n")
	pub := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-publish")
	if !strings.Contains(pub, `id="draft-publish-button">Publish</button>`) || strings.Contains(pub, `id="draft-drift"`) || strings.Contains(pub, `name="drift"`) {
		t.Fatalf("publish card:\n%s", pub)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}, "pin": {"1"}})
	body := rec.Body.String()
	done := section(body, "draft-published")
	if rec.Code != http.StatusOK || !strings.Contains(done, "<h2>Published v2</h2>") || !strings.Contains(done, "<p>pinned web v2 (app@v1)</p>") || strings.Contains(done, `id="config-write"`) {
		t.Fatalf("publish = %d:\n%s", rec.Code, done)
	}
	if k, _ := store.GetKit("app"); k.Current != 2 {
		t.Fatalf("current = v%d", k.Current)
	}
	if _, open := store.GetKitDraft("app"); open || !strings.Contains(body, `id="draft-none"`) {
		t.Fatal("publish should close the draft and offer a new one")
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}}); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "kit app has no draft") {
		t.Fatalf("publish again = %d %s", rec.Code, rec.Body)
	}
}

// Review Focus 5: a draft with problems cannot be published from the page,
// and a crafted POST is refused anyway.
func TestDraftPublishBlockedByProblems(t *testing.T) {
	store, h := draftFixture(t)
	if _, err := jam.RemoveKitDraftFile(store, nil, "app", "kit.yml", draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	pub := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-publish")
	if !strings.Contains(pub, `id="draft-publish-button" disabled`) || !strings.Contains(pub, "kit app: kit.yml missing at the root") {
		t.Fatalf("publish card:\n%s", pub)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {draftETag(t, store)}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "draft app has problems: kit app: kit.yml missing at the root") {
		t.Fatalf("crafted publish = %d %s", rec.Code, rec.Body)
	}
	if k, _ := store.GetKit("app"); k.Current != 1 {
		t.Fatal("a draft with problems was published")
	}
	if _, open := store.GetKitDraft("app"); !open {
		t.Fatal("a refused publish closed the draft")
	}
}

// A publish needs the draft's ETag: none is 428, a stale one 412, and
// neither publishes.
func TestDraftPublishNeedsETag(t *testing.T) {
	store, h := draftFixture(t)
	stale := draftETag(t, store)
	putDraft(t, store, "run.sh", 0, "echo two\n")
	for etag, want := range map[string]int{"": http.StatusPreconditionRequired, stale: http.StatusPreconditionFailed} {
		if rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {etag}}); rec.Code != want {
			t.Errorf("publish with etag %q = %d %s, want %d", etag, rec.Code, rec.Body, want)
		}
	}
	if k, _ := store.GetKit("app"); k.Current != 1 {
		t.Fatal("a publish without the current ETag published")
	}
}

// markManaged records kit name as applied from the config repo.
func markManaged(t *testing.T, store jam.Store, name string) {
	t.Helper()
	if err := store.ApplyConfig(jam.ConfigWrite{Marks: []jam.ConfigMark{{Kind: jam.ConfigKindKit, Name: name, Commit: "abc1234", Digest: "d", AppliedAt: time.Unix(1, 0).UTC()}}}); err != nil {
		t.Fatal(err)
	}
}

// A config-managed kit shows the drift warning before publishing and needs
// the drift box ticked (the page requires it; the server refuses without
// it), then shows the files to commit and the config write command: paths
// only, no content.
func TestDraftPublishManagedOffersConfigWrite(t *testing.T) {
	store, h := draftFixture(t)
	markManaged(t, store, "app")
	putDraft(t, store, "run.sh", 0, "echo SECRET-CONTENT\n")
	putDraft(t, store, "docs/new.md", 0, "n\n")
	pub := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-publish")
	if !strings.Contains(pub, `id="draft-drift">`+jam.DraftDriftWarning) || !strings.Contains(pub, `hx-confirm="Publish the draft as the next version of app? `+jam.DraftDriftWarning+`"`) ||
		!strings.Contains(pub, `<input type="checkbox" name="drift" value="1" required> I understand this drifts from the config repo`) {
		t.Fatalf("drift warning:\n%s", pub)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "kit app is managed by the config repo: tick “I understand this drifts from the config repo” to publish") {
		t.Fatalf("unconfirmed publish = %d %s", rec.Code, rec.Body)
	}
	if k, _ := store.GetKit("app"); k.Current != 1 {
		t.Fatal("an unconfirmed managed publish published")
	}
	rec = post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}, "drift": {"1"}})
	done := section(rec.Body.String(), "draft-published")
	for _, want := range []string{
		`id="config-write"`, "<code>at-jam config write &lt;repo&gt;</code>",
		`<li data-change="added">kits/app/docs/new.md`, `<li data-change="modified">kits/app/run.sh`,
		`id="draft-published-warning">` + jam.DraftDriftWarning,
	} {
		if rec.Code != http.StatusOK || !strings.Contains(done, want) {
			t.Errorf("published panel (%d) missing %q:\n%s", rec.Code, want, done)
		}
	}
	if strings.Contains(done, "SECRET-CONTENT") || strings.Contains(done, "kits/app/kit.yml") {
		t.Error("the panel must list changed paths only, never content")
	}
}

// Ruling 26: publishing an unmanaged kit with a managed follower pinned
// drifts too; the panel lists only that follower's kit.yml.
func TestDraftPublishManagedFollowerFiles(t *testing.T) {
	store, h := draftFixture(t)
	if _, _, err := jam.PushKitFiles(store, "web", kittreetest.Files("kit.yml", "kind: studio\nbase:\n  kit: app\n")); err != nil {
		t.Fatal(err)
	}
	markManaged(t, store, "web")
	putDraft(t, store, "run.sh", 0, "echo two\n")
	pub := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-publish")
	if strings.Contains(pub, `id="draft-drift"`) {
		t.Fatalf("an unmanaged kit shows the drift warning:\n%s", pub)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}, "pin": {"1"}, "drift": {"1"}})
	done := section(rec.Body.String(), "draft-published")
	if rec.Code != http.StatusOK || !strings.Contains(done, `<li data-change="modified">kits/web/kit.yml`) || strings.Contains(done, "kits/app/") || !strings.Contains(done, jam.DraftDriftWarning) {
		t.Fatalf("publish = %d:\n%s", rec.Code, done)
	}
}

// Ruling 27: a publish leaves one audit line: operator, kit, version, base,
// a tree prefix, the pins and drift, never content or a path.
func TestDraftPublishAudit(t *testing.T) {
	store, _ := draftFixture(t)
	putDraft(t, store, "run.sh", 0, "echo secret\n")
	var logs bytes.Buffer
	h := adminui.Handler(store, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil, anyCred, nil)
	d, _ := store.GetKitDraft("app")
	if rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {jam.DraftETag(d.Tree)}}); rec.Code != http.StatusOK {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	if want := `msg="ui kit draft published" operator="" kit=app version=2 base=1 tree=` + d.Tree[:12] + " pinned=0 drift=false\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("logs missing %q:\n%s", want, logs.String())
	}
	if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "run.sh") {
		t.Errorf("the audit line names content or a path:\n%s", logs.String())
	}
}

// A publish with Pin followers that would rewrite a config-managed follower
// warns before, naming it, and needs the drift box when Pin is ticked (the
// page requires it with Pin; the server refuses without it).
func TestDraftPublishPinDriftWarnsFirst(t *testing.T) {
	store, h := draftFixture(t)
	for name, yml := range map[string]string{"web": "kind: studio\nbase:\n  kit: app\n", "api": "kind: studio\nbase:\n  kit: app\n"} {
		if _, _, err := jam.PushKitFiles(store, name, kittreetest.Files("kit.yml", yml)); err != nil {
			t.Fatal(err)
		}
	}
	markManaged(t, store, "web")
	putDraft(t, store, "run.sh", 0, "echo two\n")
	pub := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-publish")
	for _, want := range []string{
		`id="draft-pin-drift">Pinning rewrites config-managed web: `,
		`<input type="checkbox" name="drift" value="1" data-require-with="pin"> I understand this drifts from the config repo`,
	} {
		if !strings.Contains(pub, want) {
			t.Errorf("publish card missing %q:\n%s", want, pub)
		}
	}
	if strings.Contains(pub, `id="draft-drift"`) || strings.Contains(pub, "managed api") {
		t.Errorf("only the managed follower is named:\n%s", pub)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}, "pin": {"1"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "pinning followers drifts config-managed web from the config repo: tick “I understand this drifts from the config repo” to publish") {
		t.Fatalf("unconfirmed pin publish = %d %s", rec.Code, rec.Body)
	}
	if k, _ := store.GetKit("app"); k.Current != 1 {
		t.Fatal("an unconfirmed pin publish published")
	}
	rec = post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}, "pin": {"1"}, "drift": {"1"}})
	if rec.Code != http.StatusOK || !strings.Contains(section(rec.Body.String(), "draft-published"), `kits/web/kit.yml`) {
		t.Fatalf("confirmed pin publish = %d %s", rec.Code, rec.Body)
	}
}

// Without Pin, a managed follower is not rewritten: no box is needed.
func TestDraftPublishNoPinNoDriftBox(t *testing.T) {
	store, h := draftFixture(t)
	if _, _, err := jam.PushKitFiles(store, "web", kittreetest.Files("kit.yml", "kind: studio\nbase:\n  kit: app\n")); err != nil {
		t.Fatal(err)
	}
	markManaged(t, store, "web")
	putDraft(t, store, "run.sh", 0, "echo two\n")
	if rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {draftETag(t, store)}}); rec.Code != http.StatusOK {
		t.Fatalf("publish without pin = %d %s", rec.Code, rec.Body)
	}
}
