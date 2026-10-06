package adminui_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// searchFixture: one of everything, with "zephyr" planted in a field of each
// kind of entity, plus squawks mentioning it.
func searchFixture(t *testing.T) http.Handler {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "zephyr-labs")
	mustCreateProject(t, store, "acme")
	if err := store.AddDestination(jam.Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://zephyr.example"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PushStudioKit(store, "web", "kind: studio\nprompt: You maintain the Zephyr service.\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "zephyr-dev", Kit: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(jam.Actor{ID: "bot-zephyr", TokenHash: "h1", Grants: []jam.Grant{{Project: "acme", Role: "zephyr-dev"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutInstance(jam.Instance{ActorID: "studio-7", Project: "acme", Role: "zephyr-dev", Unit: "COV-42", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddHuman("acme", jam.Human{Name: "zoe", Handle: "zephyr-zoe"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddChannel("acme", jam.RosterChannel{Name: "ops", Service: "discord", Ref: "zephyr-ops"}); err != nil {
		t.Fatal(err)
	}
	var squawks []intercom.LegacySquawk
	t0 := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for i := range 12 {
		squawks = append(squawks, intercom.LegacySquawk{From: human("zoe"), To: []intercom.Target{actor("studio-7")},
			Body: fmt.Sprintf("zephyr update %d", i), At: t0.Add(time.Duration(i) * time.Minute), Project: "acme"})
	}
	squawks = append(squawks, intercom.LegacySquawk{From: human("zoe"), To: []intercom.Target{channel("ops")}, Body: "nothing relevant", At: t0, Project: "acme"})
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, newIntercomLog(t, squawks...))
}

func TestSearchFindsEveryKind(t *testing.T) {
	rec := get(t, searchFixture(t), "/ui/search?q=ZEPHYR") // case-insensitive
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="/ui/projects/zephyr-labs"`,      // project by name
		`href="/ui/roles/acme/zephyr-dev"`,     // role by name
		`href="/ui/coves/studio-7"`,            // studio by role
		"bot-<mark>zephyr</mark>",              // actor by id
		"human:zoe",                            // human by handle
		"channel:ops",                          // channel by ref
		`href="/ui/kits/web"`,                  // kit by prompt
		`href="/ui/destinations/gh"`,           // destination by upstream
		"<mark>zephyr</mark> update 11",        // newest squawk, highlighted
		`href="/ui/intercom?q=ZEPHYR"`,         // the rest of the squawks
		`data-group="squawks" data-count="12"`, // full count, 10 shown
	} {
		if !strings.Contains(body, want) {
			t.Errorf("search results missing %q", want)
		}
	}
	if strings.Contains(body, "zephyr update 1<") || strings.Contains(body, "nothing relevant") {
		t.Errorf("squawks: only the newest 10 matches should show")
	}
}

// Typing in the page's box fetches just the results fragment.
func TestSearchLiveFragment(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ui/search?q=zephyr", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	searchFixture(t).ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `id="search-results"`) || strings.Contains(body, "<nav") {
		t.Errorf("live search should return only the results fragment")
	}
}

func TestSearchExactMatchJumps(t *testing.T) {
	h := searchFixture(t)
	for q, want := range map[string]string{
		"studio-7":        "/ui/coves/studio-7",
		"web":             "/ui/kits/web",
		"acme/zephyr-dev": "/ui/roles/acme/zephyr-dev",
	} {
		rec := get(t, h, "/ui/search?go=1&q="+q)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("q=%s: %d → %q, want 303 → %q", q, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	// no single exact match → the results page
	if rec := get(t, h, "/ui/search?go=1&q=zephyr"); rec.Code != http.StatusOK {
		t.Errorf("non-exact go = %d, want 200 results", rec.Code)
	}
}

func TestSearchShortQueryAndNoMatches(t *testing.T) {
	h := searchFixture(t)
	if body := get(t, h, "/ui/search?q=z").Body.String(); !strings.Contains(body, "at least 2 characters") {
		t.Errorf("a 1-char query should prompt for more")
	}
	if body := get(t, h, "/ui/search?q=qqqqq").Body.String(); !strings.Contains(body, "No matches") {
		t.Errorf("a miss should say so")
	}
}

func TestSearchBoxOnEveryPage(t *testing.T) {
	body := get(t, searchFixture(t), "/ui/roles").Body.String()
	for _, want := range []string{`action="/ui/search"`, `id="q-top"`, `name="go" value="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("topbar search missing %q", want)
		}
	}
}

// Highlighting escapes the matched text like any other output.
func TestSearchHighlightEscapes(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "p")
	if err := store.AddHuman("p", jam.Human{Name: "x", Handle: "<b>evil</b>"}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/search?q=evil").Body.String()
	if strings.Contains(body, "<b>") || !strings.Contains(body, "&lt;b&gt;<mark>evil</mark>&lt;/b&gt;") {
		t.Errorf("highlight must escape the surrounding text")
	}
}
