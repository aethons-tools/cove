//go:build browser

package adminui_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/browsertest"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// The admin app loads under its CSP for a loopback admin (no user), deep-links
// into the Config tree, and the tree's links and the back button navigate
// (admin SPA slice 1).
func TestBrowserAdminTree(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browsertest.Ctx(t)
	store := jam.NewMemStore()
	if err := store.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "coder"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser(jam.User{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	srv := adminServer(t, store)

	var problems browsertest.Problems
	coder := chromedp.CSS(`nav[aria-label="Config"] a[href="/app/admin/next/config/projects/acme/agents/coder"][aria-current="page"]`)
	users := chromedp.CSS(`nav[aria-label="Config"] a[href="/app/admin/next/config/users"]`)
	var atUsers, back string
	err := chromedp.Do(ctx,
		problems.Watch(ctx),
		chromedp.Navigate(srv.URL+"/app/admin/next/config/projects/acme/agents/coder"),
		chromedp.WaitVisible(coder),
		chromedp.Click(users),
		chromedp.WaitVisible(chromedp.CSS(`nav[aria-label="Config"] a[href="/app/admin/next/config/users"][aria-current="page"]`)),
		browsertest.Into(&atUsers, chromedp.Location()),
		chromedp.NavigateBack(),
		chromedp.WaitVisible(coder),
		browsertest.Into(&back, chromedp.Location()),
	)
	if err != nil {
		t.Fatalf("%v (at %q; problems %v)", err, atUsers, problems.All())
	}
	if !strings.HasSuffix(atUsers, "/app/admin/next/config/users") || !strings.HasSuffix(back, "/agents/coder") {
		t.Errorf("locations = %q then %q", atUsers, back)
	}
	for _, pr := range problems.All() {
		t.Errorf("page problem: %s", pr)
	}
}

// A user rename from the user's page reaches the store and the Config tree
// (admin SPA slice 2).
func TestBrowserAdminUserRename(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browsertest.Ctx(t)
	store := jam.NewMemStore()
	alice, err := store.CreateUser(jam.User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	srv := adminServer(t, store)

	var problems browsertest.Problems
	nameInput := chromedp.CSS(`input[value="alice"]`)
	err = chromedp.Do(ctx,
		problems.Watch(ctx),
		chromedp.Navigate(srv.URL+"/app/admin/next/config/users/"+string(alice.ID)),
		chromedp.WaitVisible(nameInput),
		browsertest.SetInput(`input[value="alice"]`, "alicia"),
		browsertest.ClickText("button", "Rename"),
		browsertest.WaitText("h2", "alicia"),
		// The open page's branch (Users) is open; the tree refetches after the write.
		browsertest.WaitText(`nav[aria-label="Config"] a`, "alicia"),
	)
	if err != nil {
		t.Fatalf("%v (problems %v)", err, problems.All())
	}
	if u, _ := store.GetUser(alice.ID); u.Name != "alicia" {
		t.Errorf("stored name = %q", u.Name)
	}
	for _, pr := range problems.All() {
		t.Errorf("page problem: %s", pr)
	}
}

// A project rename moves the page to the new name and the tree follows
// (admin SPA slice 3).
func TestBrowserAdminProjectRename(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browsertest.Ctx(t)
	store := jam.NewMemStore()
	if err := store.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	srv := adminServer(t, store)
	var problems browsertest.Problems
	var at string
	err := chromedp.Do(ctx,
		problems.Watch(ctx),
		chromedp.Navigate(srv.URL+"/app/admin/next/config/projects/acme"),
		chromedp.WaitVisible(chromedp.CSS(`input[aria-label="New name"]`)),
		browsertest.SetInput(`input[aria-label="New name"]`, "acme2"),
		browsertest.ClickText("button", "Rename"),
		browsertest.WaitText("h2", "acme2"),
		browsertest.WaitText(`nav[aria-label="Config"] a`, "acme2"),
		browsertest.Into(&at, chromedp.Location()),
	)
	if err != nil {
		t.Fatalf("%v (problems %v)", err, problems.All())
	}
	if !strings.HasSuffix(at, "/app/admin/next/config/projects/acme2") {
		t.Errorf("location = %q", at)
	}
	if _, ok := store.GetProject("acme2"); !ok {
		t.Error("the store has no acme2")
	}
	for _, pr := range problems.All() {
		t.Errorf("page problem: %s", pr)
	}
}

// A role's scope edit from its page reaches the store (admin SPA slice 4).
func TestBrowserAdminRoleScope(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browsertest.Ctx(t)
	store := jam.NewMemStore()
	if err := store.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "coder", Scope: jam.Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	srv := adminServer(t, store)
	var problems browsertest.Problems
	err := chromedp.Do(ctx,
		problems.Watch(ctx),
		chromedp.Navigate(srv.URL+"/app/admin/next/config/projects/acme/agents/coder"),
		browsertest.ClickText("summary", "Edit scope"),
		chromedp.WaitVisible(chromedp.CSS(`input[value="1h"]`)),
		browsertest.SetInput(`input[value="1h"]`, "2h"),
		browsertest.ClickText("button", "Save scope"),
		browsertest.WaitText("p", "TTL 2h"),
	)
	if err != nil {
		t.Fatalf("%v (problems %v)", err, problems.All())
	}
	if r, _ := store.GetRole("acme", "coder"); r.Scope.TTL != 2*time.Hour {
		t.Errorf("stored TTL = %v", r.Scope.TTL)
	}
	for _, pr := range problems.All() {
		t.Errorf("page problem: %s", pr)
	}
}

// adminServer serves store's admin UI to a loopback admin, with the app-level
// routes the page loads (/app/assets/, the service worker) from appui, as jam
// does.
func adminServer(t *testing.T, store jam.Store) *httptest.Server {
	t.Helper()
	admin := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	app := appui.Handler(appui.Deps{Gate: appui.NewGate(appui.GateConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app/admin/") {
			app.ServeHTTP(w, r)
			return
		}
		admin.ServeHTTP(w, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	}))
	t.Cleanup(srv.Close)
	return srv
}
