//go:build browser

package adminui_test

import (
	"errors"
	"fmt"
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
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
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

// A destination's edit from its page reaches the store (admin SPA slice 5).
func TestBrowserAdminDestinationNote(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browsertest.Ctx(t)
	store := jam.NewMemStore()
	if err := store.AddDestination(jam.Destination{Name: "gh", Route: "/gh/", Upstream: "https://api.github.com", IdentityIn: jam.ApplyBearer, Apply: jam.ApplyBearer}); err != nil {
		t.Fatal(err)
	}
	srv := adminServer(t, store)
	var problems browsertest.Problems
	err := chromedp.Do(ctx,
		problems.Watch(ctx),
		chromedp.Navigate(srv.URL+"/app/admin/next/config/destinations/gh"),
		browsertest.ClickText("summary", "Edit destination"),
		chromedp.WaitVisible(chromedp.CSS(`input[maxlength="300"]`)),
		browsertest.SetInput(`input[maxlength="300"]`, "use gh for GitHub"),
		browsertest.ClickText("button", "Save destination"),
		browsertest.WaitText("div", "use gh for GitHub"),
	)
	if err != nil {
		t.Fatalf("%v (problems %v)", err, problems.All())
	}
	note := "(no destination gh)"
	for _, d := range store.ListDestinations() {
		if d.Name == "gh" {
			note = d.Note
		}
	}
	if note != "use gh for GitHub" {
		t.Errorf("stored note = %q", note)
	}
	for _, pr := range problems.All() {
		t.Errorf("page problem: %s", pr)
	}
}

// Pinning an older kit version from the rail makes it current (admin SPA
// slice 6a).
func TestBrowserAdminKitPin(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browsertest.Ctx(t)
	store := jam.NewMemStore()
	for _, p := range []string{"one", "two"} {
		if _, err := store.PushKit("k", "kind: studio\nprompt: "+p+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	srv := adminServer(t, store)
	var problems browsertest.Problems
	err := chromedp.Do(ctx,
		problems.Watch(ctx),
		chromedp.Navigate(srv.URL+"/app/admin/next/config/kits/k"),
		browsertest.ClickText(`nav[aria-label="Versions"] button`, "Pin"),
		browsertest.ClickText(`[role="dialog"] button`, "Pin"),
		browsertest.WaitText(".mantine-Badge-label", "current v1"),
	)
	if err != nil {
		t.Fatalf("%v (problems %v)", err, problems.All())
	}
	if k, _ := store.GetKit("k"); k.Current != 1 {
		t.Errorf("current = %d", k.Current)
	}
	for _, pr := range problems.All() {
		t.Errorf("page problem: %s", pr)
	}
}

// A kit's draft, opened, edited and published from its page (admin SPA
// slice 6b).
func TestBrowserAdminKitDraft(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browsertest.Ctx(t)
	store := jam.NewMemStore()
	if _, _, err := jam.PushKitFiles(store, "k", kittreetest.Files("kit.yml", "kind: studio\nprompt: one\n")); err != nil {
		t.Fatal(err)
	}
	srv := adminServer(t, store)
	var problems browsertest.Problems
	err := chromedp.Do(ctx,
		problems.Watch(ctx),
		chromedp.Navigate(srv.URL+"/app/admin/next/config/kits/k"),
		browsertest.ClickText("button", "Open draft"),
		browsertest.ClickText(`table[aria-label="Draft files"] a`, "kit.yml"),
		chromedp.WaitVisible(chromedp.CSS(`textarea[aria-label="kit.yml"]`)),
		browsertest.SetInput(`textarea[aria-label="kit.yml"]`, "kind: studio\nprompt: two\n"),
		browsertest.ClickText("button", "Save"),
		browsertest.WaitText(".mantine-Notification-description", "saved kit.yml"),
		browsertest.ClickText("button", "Publish"),
		browsertest.ClickText(`[role="dialog"] button`, "Publish"),
		browsertest.WaitText("h4", "Published v2"),
	)
	if err != nil {
		t.Fatalf("%v (problems %v)", err, problems.All())
	}
	if k, _ := store.GetKit("k"); k.Current != 2 {
		t.Errorf("current = %d", k.Current)
	}
	if v, _ := jam.LoadKitVersion(store, "k", 2); !strings.Contains(string(v.KitYML), "prompt: two") {
		t.Errorf("v2 kit.yml = %q", v.KitYML)
	}
	for _, pr := range problems.All() {
		t.Errorf("page problem: %s", pr)
	}
}

// adminServer serves store's admin UI to a loopback admin, with the app-level
// routes the page loads (/app/assets/, the service worker) from appui, as jam
// does.
// An enrollment from the Agents tab mints the actor and shows its token
// once (admin SPA slice 7).
func TestBrowserAdminEnroll(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browsertest.Ctx(t)
	store := jam.NewMemStore()
	if err := store.CreateProject("default"); err != nil && !errors.Is(err, jam.ErrProjectExists) {
		t.Fatal(err)
	}
	if err := store.PutRole("default", jam.Role{Name: "coder"}); err != nil {
		t.Fatal(err)
	}
	srv := adminServer(t, store)
	// The open form's inputs, in order: Agent id, Role, Project, Destination overrides.
	in := func(n int) string {
		return fmt.Sprintf(`details[open] form .mantine-InputWrapper-root:nth-of-type(%d) input`, n)
	}
	var problems browsertest.Problems
	err := chromedp.Do(ctx,
		problems.Watch(ctx),
		chromedp.Navigate(srv.URL+"/app/admin/next/agents"),
		browsertest.ClickText("summary", "Enroll agent"),
		chromedp.WaitVisible(chromedp.CSS(in(1))),
		browsertest.SetInput(in(1), "bot-1"),
		browsertest.SetInput(in(2), "coder"),
		browsertest.ClickText("button", "Enroll"),
		browsertest.WaitText("b", "Enrolled bot-1."),
	)
	if err != nil {
		t.Fatalf("%v (problems %v)", err, problems.All())
	}
	var got *jam.Actor
	for _, a := range store.ListActors() {
		if a.ID == "bot-1" {
			got = &a
		}
	}
	if got == nil || len(got.Grants) != 1 || got.Grants[0].Role != "coder" {
		t.Errorf("enrolled actor = %+v", got)
	}
	for _, pr := range problems.All() {
		t.Errorf("page problem: %s", pr)
	}
}

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
