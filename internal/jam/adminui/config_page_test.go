package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
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
