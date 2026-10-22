package adminui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// trackManaged records the source managedUI's objects came from.
func trackManaged(t *testing.T, store jam.Store) {
	t.Helper()
	if err := store.SetConfigSource(&jam.ConfigSource{Repo: "https://github.com/acme/jam", Ref: "main", Commit: managedSHA, AppliedAt: time.Unix(1000, 0)}); err != nil {
		t.Fatal(err)
	}
}

func TestConfigPageShowsSourceAndManaged(t *testing.T) {
	h, store := managedUI(t)
	trackManaged(t, store)
	for _, d := range store.ListDestinations() {
		if d.Name == "gh" {
			d.Note = "edited in Jam"
			if err := store.AddDestination(d); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.SetJamContext(sessionctx.Layer{}); err != nil { // the managed Jam context goes missing
		t.Fatal(err)
	}
	body := get(t, h, "/ui/config").Body.String()
	for _, want := range []string{
		`<a href="/ui/config" aria-current="page">Config`,
		`href="https://github.com/acme/jam"`,
		`href="https://github.com/acme/jam/commit/` + managedSHA + `"`,
		"1970-01-01 00:16 UTC",
		`href="/ui/destinations/gh"`, `href="/ui/projects/app/roles/ic"`, `href="/ui/users/bob"`,
		`<span class="chip st-drifted">drifted</span>`, `<span class="chip st-missing">missing</span>`, "in sync",
		"2 differ from the repo",
		`hx-delete="/ui/config/managed/role/app/ic"`,
		`hx-delete="/ui/config/source"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("config page missing %q", want)
		}
	}
	if strings.Contains(body, `href="/ui/#jam-context"`) {
		t.Error("a missing object links its page")
	}
}

func TestConfigPageRelease(t *testing.T) {
	h, store := managedUI(t)
	rec := del(t, h, "/ui/config/managed/role/app/ic")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "released role app/ic") {
		t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
	}
	for _, mk := range store.ListConfigMarks() {
		if mk.Kind == jam.ConfigKindRole {
			t.Fatalf("role still managed: %+v", mk)
		}
	}
	if rec := del(t, h, "/ui/config/managed/role/app/ic"); rec.Code != http.StatusNotFound {
		t.Fatalf("second release: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodDelete, "/ui/config/managed/user/bob", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || len(store.ListConfigMarks()) == 0 {
		t.Fatalf("cross-origin release: %d", rec.Code)
	}
}

func TestConfigPageUntrack(t *testing.T) {
	h, store := managedUI(t)
	trackManaged(t, store)
	rec := del(t, h, "/ui/config/source")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No config source is tracked") {
		t.Fatalf("untrack: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := store.GetConfigSource(); ok || len(store.ListConfigMarks()) == 0 {
		t.Fatal("untrack must forget the source and keep the marks")
	}
}

// A Jam the drift check cannot render: the page says so instead of failing.
func TestConfigPageDriftUnknown(t *testing.T) {
	h, store := managedUI(t)
	if _, err := store.PushKit("bad", "listen: :443"); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/ui/config")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Could not compare with the live Jam") || !strings.Contains(rec.Body.String(), "unknown") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// A stored source that does not re-parse (a hand-made URL with userinfo) is
// shown as "invalid": no credential reaches the page.
func TestConfigPageInvalidSource(t *testing.T) {
	h, store := managedUI(t)
	if err := store.SetConfigSource(&jam.ConfigSource{Repo: "https://user:hunter2@github.com/acme/jam", Ref: "main", Commit: managedSHA}); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/ui/config").Body.String()
	if strings.Contains(body, "hunter2") || !strings.Contains(body, "Stop following invalid?") {
		t.Fatalf("invalid source rendered:\n%s", body)
	}
	if !strings.Contains(body, `<span class="mono">abc1234</span>`) && !strings.Contains(body, `>abc1234</a>`) {
		t.Error("source commit is not shortened to 7 chars")
	}
}

// cfgTreeFetcher serves one config repo: every ref resolves to sha, holding files.
type cfgTreeFetcher struct {
	t       *testing.T
	sha     string
	files   map[string]string
	fetches int
}

func (f *cfgTreeFetcher) Resolve(context.Context, gitsource.Source) (string, error) {
	return f.sha, nil
}

func (f *cfgTreeFetcher) Fetch(_ context.Context, _ gitsource.Source, fn func(root, commit string) error) error {
	f.fetches++
	root := f.t.TempDir()
	for p, b := range f.files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(b), 0o644); err != nil {
			return err
		}
	}
	return fn(root, f.sha)
}

func configUI(t *testing.T, f jam.KitFetcher, conds *condition.Tracker) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, adminui.WithKitFetcher(f),
		adminui.WithConfigEnv(jam.ConfigEnv{Conditions: conds}), adminui.WithConditions(conds, "")), store
}

var planDigestRE = regexp.MustCompile(`name="plan" value="([0-9a-f]{64})"`)

func planDigest(t *testing.T, body string) string {
	t.Helper()
	m := planDigestRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no plan digest in:\n%s", body)
	}
	return m[1]
}

func TestConfigPageTrackDiffApply(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	body := get(t, h, "/ui/config").Body.String()
	if !strings.Contains(body, `hx-post="/ui/config/source"`) || strings.Contains(body, `hx-post="/ui/config/diff"`) {
		t.Fatalf("untracked page:\n%s", body)
	}
	if !strings.Contains(body, `hx-disabled-elt="find button[type=submit]"`) || !strings.Contains(body, `class="htmx-indicator`) {
		t.Errorf("Track shows no progress or stays enabled in flight:\n%s", body)
	}
	rec := post(t, h, "/ui/config/source", url.Values{"source": {"https://github.com/acme/jam@main"}})
	body = rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("track: %d %s", rec.Code, body)
	}
	for _, want := range []string{"tracking https://github.com/acme/jam@main", `<span class="chip">create</span>`, "users/bob.yml",
		`hx-post="/ui/config/diff"`, `hx-post="/ui/config/apply"`, "+ kind: user"} {
		if !strings.Contains(body, want) {
			t.Errorf("track answer missing %q", want)
		}
	}
	if _, ok := store.GetConfigSource(); !ok || len(store.ListUsers()) != 0 {
		t.Fatal("track must record the source and apply nothing")
	}
	rec = post(t, h, "/ui/config/apply", url.Values{"plan": {planDigest(t, body)}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "applied config @abc1234: 1 created") || !strings.Contains(rec.Body.String(), `href="/ui/users/bob"`) {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	if src, _ := store.GetConfigSource(); len(store.ListUsers()) != 1 || src.Commit != managedSHA {
		t.Fatalf("users=%v source=%+v", store.ListUsers(), src)
	}
}

func TestConfigApplyIsBoundToTheShownPlan(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	trackManaged(t, store)
	body := post(t, h, "/ui/config/diff", url.Values{}).Body.String()
	if strings.Contains(body, `name="force"`) {
		t.Fatal("force offered without drift")
	}
	digest := planDigest(t, body)
	if rec := post(t, h, "/ui/config/apply", url.Values{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("apply without a plan: %d", rec.Code)
	}
	// The Jam changes after the review: the plan on screen is stale.
	if _, err := store.CreateUser(jam.User{ID: ident.New(ident.User), Name: "carol", Status: jam.StatusLive}); err != nil {
		t.Fatal(err)
	}
	rec := post(t, h, "/ui/config/apply", url.Values{"plan": {digest}})
	if rec.Code != http.StatusConflict || len(store.ListUsers()) != 1 {
		t.Fatalf("stale apply: %d users=%v", rec.Code, store.ListUsers())
	}
	if !strings.Contains(rec.Body.String(), "changed since this plan was shown") || !strings.Contains(rec.Body.String(), "Run Diff again") {
		t.Errorf("stale apply message: %s", rec.Body.String())
	}
}

func TestConfigPageForceOnlyWithDrift(t *testing.T) {
	conds := condition.New(condition.Options{})
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\nlogins: [bob@repo]\n"}}
	h, store := configUI(t, f, conds)
	applyManaged(t, store, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	trackManaged(t, store)
	if err := store.SetUserLogins(userID(t, store, "bob"), []string{"bob@jam"}); err != nil {
		t.Fatal(err)
	}
	jam.CheckConfigDrift(store, conds, nil)
	if !conds.IsOpen(jam.ConfigDriftKey) {
		t.Fatal("drift not raised")
	}
	body := post(t, h, "/ui/config/diff", url.Values{}).Body.String()
	if !strings.Contains(body, "user bob drifted (edited in Jam since config @abc1234)") || !strings.Contains(body, `name="force"`) ||
		strings.Contains(body, `hx-post="/ui/config/apply"`) {
		t.Fatalf("drifted diff:\n%s", body)
	}
	body = post(t, h, "/ui/config/diff", url.Values{"force": {"1"}}).Body.String()
	if strings.Contains(body, "drifted (edited") || !strings.Contains(body, `<input type="hidden" name="force" value="1">`) || !strings.Contains(body, " checked") {
		t.Fatalf("forced diff:\n%s", body)
	}
	rec := post(t, h, "/ui/config/apply", url.Values{"plan": {planDigest(t, body)}, "force": {"1"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "1 updated") {
		t.Fatalf("forced apply: %d %s", rec.Code, rec.Body.String())
	}
	if u, _ := store.GetUser(userID(t, store, "bob")); len(u.Logins) != 1 || u.Logins[0] != "bob@repo" {
		t.Fatalf("logins %v", u.Logins)
	}
	if conds.IsOpen(jam.ConfigDriftKey) {
		t.Fatal("the apply left config.drift open")
	}
}

func TestConfigPageNoFetcherNoGitActions(t *testing.T) {
	h, store := managedUI(t)
	trackManaged(t, store)
	body := get(t, h, "/ui/config").Body.String()
	for _, absent := range []string{`hx-post="/ui/config/diff"`, `hx-post="/ui/config/source"`, `hx-post="/ui/config/apply"`} {
		if strings.Contains(body, absent) {
			t.Errorf("no fetcher, yet %s", absent)
		}
	}
	if rec := post(t, h, "/ui/config/diff", url.Values{}); rec.Code == http.StatusOK {
		t.Fatal("diff answered without a fetcher")
	}
}

func TestConfigPageGitCrossOriginRefused(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	trackManaged(t, store)
	for _, p := range []string{"/ui/config/diff", "/ui/config/apply", "/ui/config/source"} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("source=https://github.com/acme/jam@main&plan=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s cross-origin = %d, want 403", p, rec.Code)
		}
	}
	if f.fetches != 0 {
		t.Errorf("refused requests fetched %d times", f.fetches)
	}
}

func TestConfigTrackRefusedWhileTracked(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	trackManaged(t, store)
	before, _ := store.GetConfigSource()
	rec := post(t, h, "/ui/config/source", url.Values{"source": {"https://github.com/acme/other@main"}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "a config source is already tracked (https://github.com/acme/jam@main); Untrack it first") {
		t.Fatalf("track while tracked: %d %s", rec.Code, rec.Body.String())
	}
	if after, _ := store.GetConfigSource(); after != before {
		t.Fatalf("tracked source changed: %+v -> %+v", before, after)
	}
	if f.fetches != 0 {
		t.Errorf("refused track fetched %d times", f.fetches)
	}
}

func TestConfigPlanDiffEscapesRepoText(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"context.md": "<script>alert(1)</script> \"><img src=x onerror=alert(2)>\n"}}
	h, store := configUI(t, f, nil)
	trackManaged(t, store)
	body := post(t, h, "/ui/config/diff", url.Values{}).Body.String()
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(body, "&lt;img src=x") {
		t.Fatalf("repo text not shown escaped:\n%s", body)
	}
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
		t.Fatalf("repo text rendered as markup:\n%s", body)
	}
}

func TestConfigApplyConfirmWithoutChanges(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	applyManaged(t, store, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	trackManaged(t, store)
	body := post(t, h, "/ui/config/diff", url.Values{}).Body.String()
	if !strings.Contains(body, `hx-confirm="Record this plan's commit?"`) || strings.Contains(body, "Apply 0 change(s)") {
		t.Fatalf("zero-change confirm:\n%s", body)
	}
}
