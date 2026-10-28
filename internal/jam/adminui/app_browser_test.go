//go:build browser

package adminui_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	// React tracks input values itself: set it through the native setter and
	// fire input, as typing would.
	setName := chromedp.Evaluate[bool](`(() => {
		const el = document.querySelector('input[value="alice"]');
		Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(el, "alicia");
		el.dispatchEvent(new Event("input", { bubbles: true }));
		return true;
	})()`)
	err = chromedp.Do(ctx,
		problems.Watch(ctx),
		chromedp.Navigate(srv.URL+"/app/admin/next/config/users/"+string(alice.ID)),
		chromedp.WaitVisible(nameInput),
		browsertest.Into(new(bool), setName),
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
