package adminui_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

// srcFetcher serves one in-memory repo: ref -> commit -> kit.yml text.
type srcFetcher struct {
	t       *testing.T
	refs    map[string]string
	kits    map[string]string
	fetches int
	err     error
}

func (f *srcFetcher) Resolve(_ context.Context, src gitsource.Source) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	sha, ok := f.refs[src.Ref]
	if !ok {
		return "", errors.Join(gitsource.ErrFetch, errors.New("no such ref"))
	}
	return sha, nil
}

func (f *srcFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	sha, err := f.Resolve(ctx, src)
	if err != nil {
		return err
	}
	f.fetches++
	root := f.t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kit.yml"), []byte(f.kits[sha]), 0o644); err != nil {
		return err
	}
	return fn(root, sha)
}

const (
	srcKitV1 = "kind: studio\nname: web-dev\nprompt: one\n"
	srcKitV2 = "kind: studio\nname: web-dev\nprompt: two\n"
	srcSHA1  = "0123456789abcdef0123456789abcdef01234567"
	srcSHA2  = "fedcba9876543210fedcba9876543210fedcba98"
)

func sourceUI(t *testing.T, f jam.KitFetcher) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	opts := []adminui.Option{}
	if f != nil {
		opts = append(opts, adminui.WithKitFetcher(f))
	}
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, opts...), store
}

func trackedKit(t *testing.T, store jam.Store, f *srcFetcher, repo string) {
	t.Helper()
	src, err := gitsource.Parse(repo + "@main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jam.TrackKit(context.Background(), store, f, nil, "web-dev", src, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestKitPageShowsTrackedSource(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	h, store := sourceUI(t, f)
	trackedKit(t, store, f, "https://github.com/acme/kits.git")
	body := get(t, h, "/ui/kits/web-dev").Body.String()
	for _, want := range []string{
		`href="https://github.com/acme/kits.git"`,
		"main",
		srcSHA1[:12],
		`href="https://github.com/acme/kits/commit/` + srcSHA1 + `"`,
		`hx-post="/ui/kits/web-dev/sync"`,
		`hx-delete="/ui/kits/web-dev/source"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, ">"+srcSHA1+"<") {
		t.Error("commit should be shortened to 12 chars")
	}
}

func TestKitPageCommitLinkFormsAndEscaping(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	h, store := sourceUI(t, f)
	trackedKit(t, store, f, "https://gitlab.example.com/acme/kits")
	body := get(t, h, "/ui/kits/web-dev").Body.String()
	if !strings.Contains(body, `href="https://gitlab.example.com/acme/kits/-/commit/`+srcSHA1+`"`) {
		t.Errorf("gitlab commit link missing:\n%s", body)
	}

	// An unknown host shows the commit as plain text; a non-https repo is
	// not linked at all; markup in stored fields is escaped.
	if err := store.SetKitSource("web-dev", &jam.KitSource{Repo: "http://localhost:1/<b>x</b>", Ref: "<i>ref</i>", Commit: srcSHA1}); err != nil {
		t.Fatal(err)
	}
	body = get(t, h, "/ui/kits/web-dev").Body.String()
	if strings.Contains(body, "<i>ref</i>") || strings.Contains(body, "<b>x</b>") {
		t.Errorf("source fields must be escaped:\n%s", body)
	}
	if strings.Contains(body, `href="http://localhost`) {
		t.Error("non-https repo must not be linked")
	}
	if !strings.Contains(body, srcSHA1[:12]) {
		t.Error("tracked commit should still show as text")
	}
}

func TestKitPageUntrackedShowsTrackForm(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{}, kits: map[string]string{}}
	h, store := sourceUI(t, f)
	if _, err := store.PushKit("plain", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/ui/kits/plain").Body.String()
	if !strings.Contains(body, `hx-post="/ui/kits/plain/source"`) || strings.Contains(body, "/ui/kits/plain/sync") {
		t.Errorf("untracked kit should offer Track only:\n%s", body)
	}
}

func TestKitSourceNoFetcherNoButtons(t *testing.T) {
	h, store := sourceUI(t, nil)
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	trackedKit(t, store, f, "https://github.com/acme/kits")
	body := get(t, h, "/ui/kits/web-dev").Body.String()
	if !strings.Contains(body, srcSHA1[:12]) {
		t.Error("read-only source info should still show")
	}
	for _, bad := range []string{"/sync", "hx-delete=\"/ui/kits/web-dev/source\"", "/ui/kits/web-dev/source\""} {
		if strings.Contains(body, bad) {
			t.Errorf("no fetcher: page must not contain %q", bad)
		}
	}
	if strings.Contains(get(t, h, "/ui/kits").Body.String(), "/ui/kits/import") {
		t.Error("no fetcher: no import form")
	}
	if rec := post(t, h, "/ui/kits/web-dev/sync", nil); rec.Code == http.StatusOK {
		t.Errorf("sync without fetcher = %d", rec.Code)
	}
}

func TestKitSyncButton(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1, srcSHA2: srcKitV2}}
	h, store := sourceUI(t, f)
	trackedKit(t, store, f, "https://github.com/acme/kits")
	f.refs["main"] = srcSHA2
	before := f.fetches
	rec := post(t, h, "/ui/kits/web-dev/sync", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync = %d: %s", rec.Code, rec.Body.String())
	}
	if f.fetches != before+1 {
		t.Errorf("fetches = %d, want %d", f.fetches, before+1)
	}
	k, _ := store.GetKit("web-dev")
	if k.Current != 2 || k.Source.Commit != srcSHA2 {
		t.Fatalf("current=%d source=%+v", k.Current, k.Source)
	}
	if body := rec.Body.String(); !strings.Contains(body, srcSHA2[:12]) || !strings.Contains(body, "v2") {
		t.Errorf("response should re-render the kit body with the new version:\n%s", body)
	}
}

func TestKitTrackAndUntrack(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	h, store := sourceUI(t, f)
	if _, err := store.PushKit("web-dev", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	rec := post(t, h, "/ui/kits/web-dev/source", url.Values{"source": {"https://github.com/acme/kits@main"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("track = %d: %s", rec.Code, rec.Body.String())
	}
	if k, _ := store.GetKit("web-dev"); k.Source == nil || k.Source.Commit != srcSHA1 {
		t.Fatalf("not tracked: %+v", k.Source)
	}
	req := httptest.NewRequest(http.MethodDelete, "/ui/kits/web-dev/source", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("untrack = %d: %s", rec.Code, rec.Body.String())
	}
	if k, _ := store.GetKit("web-dev"); k.Source != nil || len(k.Versions) != 2 {
		t.Fatalf("untrack must keep versions: %+v", k)
	}
}

func TestKitSourceCrossOriginRefused(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	h, store := sourceUI(t, f)
	trackedKit(t, store, f, "https://github.com/acme/kits")
	for _, c := range []struct{ method, path string }{
		{"POST", "/ui/kits/web-dev/sync"},
		{"POST", "/ui/kits/web-dev/source"},
		{"DELETE", "/ui/kits/web-dev/source"},
		{"POST", "/ui/kits/import"},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader("source=https://github.com/acme/kits@main"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s cross-origin = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if f.fetches != 1 {
		t.Errorf("refused requests must not fetch (fetches=%d)", f.fetches)
	}
}

func TestKitImportForm(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: "kind: studio\nprompt: one\n"}}
	h, store := sourceUI(t, f)
	if body := get(t, h, "/ui/kits").Body.String(); !strings.Contains(body, `hx-post="/ui/kits/import"`) {
		t.Errorf("kits page should offer Import from git:\n%s", body)
	}

	// A bad source is an error fragment (400), nothing imported.
	rec := post(t, h, "/ui/kits/import", url.Values{"source": {"not a source"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `class="error"`) {
		t.Fatalf("bad source = %d: %s", rec.Code, rec.Body.String())
	}
	// A fetch failure is 502 with the redacted message.
	rec = post(t, h, "/ui/kits/import", url.Values{"source": {"https://github.com/acme/kits@nope"}})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("fetch failure = %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.ListKits()) != 0 {
		t.Fatal("failed imports must not create kits")
	}

	rec = post(t, h, "/ui/kits/import", url.Values{"source": {"https://github.com/acme/kits@main"}, "name": {"mine"}})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/kits/mine" {
		t.Fatalf("import = %d redirect=%q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	k, ok := store.GetKit("mine")
	if !ok || k.Source != nil || k.Meta[1].Source == nil {
		t.Fatalf("import must record provenance without tracking: %+v", k)
	}
}

func TestKitSyncUnknownKit(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	h, _ := sourceUI(t, f)
	if rec := post(t, h, "/ui/kits/ghost/sync", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown kit sync = %d, want 404", rec.Code)
	}
}
