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
	admin := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	// appui serves the app-level routes the page loads (/app/assets/, the
	// service worker), as jam does.
	app := appui.Handler(appui.Deps{Gate: appui.NewGate(appui.GateConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app/admin/") {
			app.ServeHTTP(w, r)
			return
		}
		admin.ServeHTTP(w, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	}))
	t.Cleanup(srv.Close)

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
