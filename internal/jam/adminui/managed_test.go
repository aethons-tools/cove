package adminui_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// managedSHA is the config commit the fixtures apply from.
const managedSHA = "abc1234def5678abc1234def5678abc1234def56"

const (
	managedBadge = "managed · config @abc1234"
	driftWarning = "This will drift from the config repo (managed · config @abc1234)."
)

// managedTree holds every kind with a UI page once.
var managedTree = map[string]string{
	"users/bob.yml":             "kind: user\nversion: 1\nlogins: [bob@example.com]\n",
	"destinations/gh.yml":       "kind: destination\nversion: 1\nroute: /gh/\nupstream: https://api.github.com\n",
	"model-specs/fast.yml":      "kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.3\nprincipal: {credential: anthropic}\nclaude: {provider: anthropic}\n",
	"kits/web-dev/kit.yml":      "kind: studio\nname: web-dev\nprompt: one\n",
	"projects/app/project.yml":  "kind: project\nversion: 1\nmembers: [{user: bob}]\n",
	"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: web-dev\nmodel-spec: fast\nscope: {destinations: [gh], ttl: 1h}\n",
	"context.md":                "Be kind.\n",
}

// applyManaged applies files to store from https://github.com/acme/jam@main
// at managedSHA, as a sync would.
func applyManaged(t *testing.T, store jam.Store, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for p, b := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := jam.LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := jam.ApplyConfigTree(context.Background(), store, nil, tree, jam.ConfigEnv{CredExists: anyCred},
		jam.ConfigApplyOptions{Source: gitsource.Source{Repo: "https://github.com/acme/jam", Ref: "main"}, Commit: managedSHA, Now: time.Unix(1000, 0)},
		nil, "test")
	if err != nil || !res.Applied {
		t.Fatalf("apply: %+v %v", res, err)
	}
}

// managedUI is a Jam with managedTree applied, and its UI.
func managedUI(t *testing.T, opts ...adminui.Option) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	applyManaged(t, store, managedTree)
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, opts...), store
}

// writeTag is the opening tag of the first element whose attr (hx-post,
// hx-delete) is exactly action; "" when there is none.
func writeTag(body, attr, action string) string {
	i := strings.Index(body, attr+`="`+action+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], "<")
	end := strings.Index(body[i:], ">")
	return body[start : i+end+1]
}

// warns reports whether page's write control attr=action confirms the drift
// warning first; it fails the test when the control is missing.
func warns(t *testing.T, body, attr, action string) bool {
	t.Helper()
	tag := writeTag(body, attr, action)
	if tag == "" {
		t.Fatalf("no element with %s=%q in:\n%s", attr, action, body)
	}
	return strings.Contains(tag, `hx-confirm="`) && strings.Contains(tag, driftWarning)
}

type warnCase struct {
	page, attr, action string
	want               bool
}

func checkWarns(t *testing.T, h http.Handler, cases []warnCase) {
	t.Helper()
	for _, c := range cases {
		if got := warns(t, get(t, h, c.page).Body.String(), c.attr, c.action); got != c.want {
			t.Errorf("%s: %s=%s warns=%v, want %v", c.page, c.attr, c.action, got, c.want)
		}
	}
}

func TestManagedSpecsBadgeAndDriftWarning(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	h, store := managedUI(t, adminui.WithKitFetcher(f))
	if err := jam.CreateDestination(store, jam.Destination{Name: "own", Route: "/own/", Upstream: "https://own.example"}, anyCred); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PushKit("web-dev", "kind: studio\nname: web-dev\nprompt: two\n"); err != nil { // v2: v1 offers Pin
		t.Fatal(err)
	}
	checkWarns(t, h, []warnCase{
		{"/ui/destinations/gh", "hx-post", "/ui/destinations/gh", true},
		{"/ui/destinations/gh", "hx-delete", "/ui/destinations/gh", true},
		{"/ui/destinations/own", "hx-post", "/ui/destinations/own", false},
		{"/ui/destinations/own", "hx-delete", "/ui/destinations/own", false},
		{"/ui/destinations", "hx-delete", "/ui/destinations/gh", true},
		{"/ui/destinations", "hx-delete", "/ui/destinations/own", false},
		{"/ui/model-specs/fast", "hx-post", "/ui/model-specs/fast", true},
		{"/ui/model-specs/fast", "hx-delete", "/ui/model-specs/fast", true},
		{"/ui/model-specs", "hx-delete", "/ui/model-specs/fast", true},
		{"/ui/kits/web-dev", "hx-post", "/ui/kits/web-dev/versions", true},
		{"/ui/kits/web-dev", "hx-post", "/ui/kits/web-dev/pin", true},
		{"/ui/kits/web-dev", "hx-post", "/ui/kits/web-dev/source", true},
		{"/ui/kits", "hx-post", "/ui/kits/import", false}, // import adds a version to any name: no warning
	})
	for page, want := range map[string]bool{"/ui/destinations/gh": true, "/ui/model-specs/fast": true, "/ui/kits/web-dev": true, "/ui/destinations/own": false} {
		body := get(t, h, page).Body.String()
		if got := strings.Contains(body, `class="chip managed" href="/ui/config"`) && strings.Contains(body, ">"+managedBadge+"</a>"); got != want {
			t.Errorf("%s: badge %v, want %v", page, got, want)
		}
	}
	// A save's re-rendered body keeps the badge and the warning.
	rec := post(t, h, "/ui/destinations/gh", url.Values{"route": {"/gh/"}, "upstream": {"https://api.github.com"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ">"+managedBadge+"</a>") || !warns(t, rec.Body.String(), "hx-post", "/ui/destinations/gh") {
		t.Fatalf("save fragment: %d\n%s", rec.Code, rec.Body.String())
	}
	// A managed kit that tracks its own source: Sync and Untrack warn too.
	trackedKit(t, store, f, "https://github.com/acme/kits")
	checkWarns(t, h, []warnCase{
		{"/ui/kits/web-dev", "hx-post", "/ui/kits/web-dev/sync", true},
		{"/ui/kits/web-dev", "hx-delete", "/ui/kits/web-dev/source", true},
	})
}
