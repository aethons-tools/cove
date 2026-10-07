package adminui

import (
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func frameGet(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	return rec.Body.String()
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i:]
	if j := strings.Index(s, to); j >= 0 {
		return s[:j]
	}
	return s
}

func frameHandler(t *testing.T) http.Handler {
	return Handler(attnFixture(t), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, func(string) bool { return true }, nil)
}

// The rail badges each project with its own items — red when any is broken —
// and Jam with Jam's items only.
func TestRailBadges(t *testing.T) {
	rail := between(frameGet(t, frameHandler(t), "/ui/"), `<aside id="rail"`, "</aside>")
	for _, want := range []string{
		// Jam: kit bad + two conflicting destinations; never the projects' items
		`<span class="name">◉ Jam</span> <span class="badge" title="3 config">3</span>`,
		// acme: three broken studios, two roles and one escalation target (no
		// image resolver here, so nothing is stale)
		`<span class="name">acme</span> <span class="badge red" title="3 broken · 3 config">6</span>`,
		// beta: nothing
		`<span class="name">beta</span></a>`,
	} {
		if !strings.Contains(rail, want) {
			t.Errorf("rail missing %s in\n%s", want, rail)
		}
	}
}

// Each tab shows its scope's items for that tab.
func TestTabBadges(t *testing.T) {
	h := frameHandler(t)
	tabs := between(frameGet(t, h, "/ui/projects/acme"), `<nav class="tabs"`, "</nav>")
	for _, want := range []string{
		`>Agents <span class="badge red" title="3 broken">3</span>`,
		`>Roles <span class="badge" title="2 config">2</span>`,
		`>Escalation <span class="badge" title="1 config">1</span>`,
		`>Members</a>`, `>Overview</a>`,
	} {
		if !strings.Contains(tabs, want) {
			t.Errorf("acme tabs missing %s in\n%s", want, tabs)
		}
	}
	tabs = between(frameGet(t, h, "/ui/"), `<nav class="tabs"`, "</nav>")
	if !strings.Contains(tabs, `>Specs <span class="badge" title="3 config">3</span>`) || !strings.Contains(tabs, `>Agents</a>`) {
		t.Errorf("Jam tabs: Specs carries Jam's items, Agents none:\n%s", tabs)
	}
}

// The project Overview and the Dashboard list their scope's items.
func TestNeedsAttentionCards(t *testing.T) {
	h := frameHandler(t)
	card := between(frameGet(t, h, "/ui/projects/acme"), "<h2>Needs attention</h2>", "</section>")
	for _, want := range []string{"s-lost: lost", "role nokit: kit ghost not found", "user:ghost", `href="/ui/agents/s-lost?project=acme"`} {
		if !strings.Contains(card, want) {
			t.Errorf("acme card missing %q in\n%s", want, card)
		}
	}
	card = between(frameGet(t, h, "/ui/"), "<h2>Needs attention</h2>", "</section>")
	if !strings.Contains(card, "kit bad v1 does not parse") || strings.Contains(card, "s-lost") {
		t.Errorf("dashboard card lists Jam's items only:\n%s", card)
	}
	if card := between(frameGet(t, h, "/ui/projects/beta"), "<h2>Needs attention</h2>", "</section>"); !strings.Contains(card, "Nothing needs attention.") {
		t.Errorf("beta card:\n%s", card)
	}
}

// Rows an item names carry its flag.
func TestRowFlags(t *testing.T) {
	h := frameHandler(t)
	for path, want := range map[string]string{
		"/ui/projects/acme/roles":  `<b>nokit</b></a> <span class="attn-flag" title="role nokit: kit ghost not found">⚠</span>`,
		"/ui/agents":               `<span class="attn-flag red" title="s-lost: lost">⚠</span>`,
		"/ui/kits":                 `<b>bad</b></a> <span class="attn-flag" title="kit bad v1 does not parse">⚠</span>`,
		"/ui/projects/acme/agents": `<span class="attn-flag red" title="s-term: terminating">⚠</span>`,
	} {
		if body := frameGet(t, h, path); !strings.Contains(body, want) {
			t.Errorf("%s missing flag %s", path, want)
		}
	}
}

// The rail polls its own fragment, keeping the selection.
func TestRailFragment(t *testing.T) {
	h := frameHandler(t)
	if body := frameGet(t, h, "/ui/projects/acme/roles"); !strings.Contains(body, `<div id="rail-entries" hx-get="/ui/rail?scope=acme" hx-trigger="every 3s"`) {
		t.Error("rail should poll with its scope")
	}
	if body := frameGet(t, h, "/ui/"); !strings.Contains(body, `<div id="rail-entries" hx-get="/ui/rail?jam=1"`) {
		t.Error("Jam's rail should poll with ?jam=1")
	}
	body := frameGet(t, h, "/ui/rail?scope=acme")
	if !strings.HasPrefix(strings.TrimSpace(body), `<div id="rail-entries"`) || strings.Contains(body, "<html") ||
		!strings.Contains(body, `<a href="/ui/projects/acme" aria-current="page">`) {
		t.Errorf("rail fragment:\n%s", body)
	}
	if body := frameGet(t, h, "/ui/rail?jam=1"); !strings.Contains(body, `<a class="jam" href="/ui/" aria-current="page">`) {
		t.Errorf("rail fragment for Jam:\n%s", body)
	}
}

// An agent page linked from a project keeps the project's scope.
func TestAgentPageScope(t *testing.T) {
	h := frameHandler(t)
	body := frameGet(t, h, "/ui/agents/s-lost?project=acme")
	if !strings.Contains(between(body, `<aside id="rail"`, "</aside>"), `<a href="/ui/projects/acme" aria-current="page">`) ||
		!strings.Contains(between(body, `<nav class="tabs"`, "</nav>"), `href="/ui/projects/acme/agents" aria-current="page">Agents`) ||
		!strings.Contains(body, `<a href="/ui/projects/acme/agents">Agents</a> / `) {
		t.Errorf("agent page with ?project=acme should sit under acme's Agents")
	}
	body = frameGet(t, h, "/ui/agents/s-lost")
	if !strings.Contains(between(body, `<nav class="tabs"`, "</nav>"), `href="/ui/agents" aria-current="page">Agents`) {
		t.Errorf("agent page without a project sits under Jam's Agents")
	}
	if body := frameGet(t, h, "/ui/agents/s-lost?project=nope"); !strings.Contains(between(body, `<nav class="tabs"`, "</nav>"), `href="/ui/agents" aria-current="page">Agents`) {
		t.Errorf("an unknown project falls back to Jam")
	}
}

// Narrow screens open the rail from a button in the title bar.
func TestRailDrawerButton(t *testing.T) {
	if body := frameGet(t, frameHandler(t), "/ui/"); !strings.Contains(body, `class="railbtn"`) || !strings.Contains(body, "body.rail-open #rail{display:block}") {
		t.Error("the title bar should carry the rail drawer button")
	}
}

// Fragments re-rendered by a poll or a write keep their rows' flags.
func TestFragmentFlags(t *testing.T) {
	h := frameHandler(t)
	want := `<span class="attn-flag red" title="s-lost: lost">⚠</span>`
	for _, path := range []string{"/ui/agents", "/ui/"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("HX-Request", "true")
		h.ServeHTTP(rec, req)
		if body := rec.Body.String(); rec.Code != http.StatusOK || strings.Contains(body, "<html") || !strings.Contains(body, want) {
			t.Errorf("HX GET %s = %d, want the fragment with %s:\n%s", path, rec.Code, want, body)
		}
	}
}

// The rail's poll swaps only its entry lists, never the "+ New project" form.
func TestRailPollKeepsForm(t *testing.T) {
	h := frameHandler(t)
	page := frameGet(t, h, "/ui/projects/acme")
	aside := between(page, `<aside id="rail"`, ">")
	if strings.Contains(aside, "hx-get") {
		t.Errorf("the rail itself must not be polled: %s", aside)
	}
	frag := strings.TrimSpace(frameGet(t, h, "/ui/rail?scope=acme"))
	if !strings.HasPrefix(frag, `<div id="rail-entries"`) || strings.Contains(frag, "newproj") || strings.Contains(frag, "<form") {
		t.Errorf("the polled fragment holds the entries only:\n%s", frag)
	}
	if !strings.Contains(page, frag) || !strings.Contains(page, `<details class="newproj">`) {
		t.Errorf("the page should carry the polled element verbatim, and the form outside it")
	}
}

// oddHandler serves projects whose names need escaping, and one named "~".
func oddHandler(t *testing.T) http.Handler {
	t.Helper()
	st := jam.NewMemStore()
	for _, p := range []string{"50%off", "a&b+c", "~"} {
		if err := st.CreateProject(p); err != nil {
			t.Fatal(err)
		}
	}
	return Handler(st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, func(string) bool { return true }, nil)
}

var pollRe = regexp.MustCompile(`id="rail-entries" hx-get="([^"]+)"`)

// Project names that need escaping keep their title, tabs and rail selection,
// and the rail's poll URL round-trips.
func TestOddProjectNames(t *testing.T) {
	h := oddHandler(t)
	for _, name := range []string{"50%off", "a&b+c", "~"} {
		page := frameGet(t, h, projectSectionURL(name, sectionMembers))
		if got := html.UnescapeString(between(page, `<div class="scope-title">`, "</div>")); got != `<div class="scope-title">`+name {
			t.Errorf("%s: title %q", name, got)
		}
		tabs := html.UnescapeString(between(page, `<nav class="tabs"`, "</nav>"))
		if !strings.Contains(tabs, `href="`+projectSectionURL(name, sectionMembers)+`" aria-current="page">Members`) {
			t.Errorf("%s: Members tab not current:\n%s", name, tabs)
		}
		current := `<a href="` + projectURL(name) + `" aria-current="page">`
		if rail := html.UnescapeString(between(page, `<aside id="rail"`, "</aside>")); !strings.Contains(rail, current) || strings.Contains(rail, `class="jam" href="/ui/" aria-current`) {
			t.Errorf("%s: rail selection wrong:\n%s", name, rail)
		}
		m := pollRe.FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("%s: no rail poll", name)
		}
		frag := html.UnescapeString(frameGet(t, h, html.UnescapeString(m[1])))
		if !strings.Contains(frag, current) || strings.Contains(frag, `class="jam" href="/ui/" aria-current`) {
			t.Errorf("%s: polled rail (%s) selection wrong:\n%s", name, m[1], frag)
		}
	}
	// Jam's own pages make Jam current, never the project named "~".
	page := frameGet(t, h, "/ui/")
	m := pollRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no rail poll on the dashboard")
	}
	for _, rail := range []string{between(page, `<aside id="rail"`, "</aside>"), frameGet(t, h, html.UnescapeString(m[1]))} {
		if !strings.Contains(rail, `<a class="jam" href="/ui/" aria-current="page">`) || strings.Contains(rail, `<a href="/ui/projects/~" aria-current`) {
			t.Errorf("Jam scope rail:\n%s", rail)
		}
	}
}

// Each Specs page shows the sub-tab strip with its own sub-tab current; other
// pages show none.
func TestSpecsSubTabs(t *testing.T) {
	h := frameHandler(t)
	for path, cur := range map[string]string{"/ui/kits": "Kits", "/ui/destinations": "Destinations", "/ui/model-specs": "Model-specs"} {
		strip := between(frameGet(t, h, path), `<nav class="subtabs"`, "</nav>")
		if strings.Count(strip, "<a ") != 3 || strings.Count(strip, "aria-current") != 1 || !strings.Contains(strip, `aria-current="page">`+cur+"</a>") {
			t.Errorf("%s sub-tabs:\n%s", path, strip)
		}
	}
	for _, path := range []string{"/ui/", "/ui/agents", "/ui/projects/acme"} {
		if strings.Contains(frameGet(t, h, path), `class="subtabs"`) {
			t.Errorf("%s should carry no sub-tabs", path)
		}
	}
}

// A missing project's page shows no tabs for it; the rail has nothing current.
func TestMissingProjectPage(t *testing.T) {
	rec := httptest.NewRecorder()
	frameHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/projects/nope", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusNotFound || strings.Contains(body, `<nav class="tabs"`) || strings.Contains(body, "/ui/projects/nope/members") {
		t.Errorf("404 project page = %d, should carry no tabs:\n%s", rec.Code, between(body, "<main>", "</main>"))
	}
	if strings.Contains(between(body, `<aside id="rail"`, "</aside>"), "aria-current") {
		t.Error("nothing is current in the rail on a missing project's page")
	}
}

// A display name brands the title bar and tab title ("Aethon Jam") and names
// the Jam rail entry and scope ("Aethon"); unset, everything reads "Jam".
func TestDisplayName(t *testing.T) {
	named := Handler(attnFixture(t), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, func(string) bool { return true }, nil, WithDisplayName("Aethon"))
	for path, wants := range map[string][]string{
		"/ui/":              {"<title>Aethon Jam — Dashboard</title>", `<span class="dot"></span>Aethon Jam <small>Admin</small>`, `<span class="name">◉ Aethon</span>`, `<div class="scope-title">Aethon</div>`},
		"/ui/projects/acme": {"<title>Aethon Jam — acme</title>", `<span class="dot"></span>Aethon Jam <small>Admin</small>`, `<span class="name">◉ Aethon</span>`, `<div class="scope-title">acme</div>`},
		"/ui/rail?jam=1":    {`<span class="name">◉ Aethon</span>`},
	} {
		body := frameGet(t, named, path)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %s", path, want)
			}
		}
	}
	// a named Jam's search and 404 pages carry the brand too
	if body := frameGet(t, named, "/ui/search?q=zz"); !strings.Contains(body, "<title>Aethon Jam — Search</title>") || !strings.Contains(body, `<span class="name">◉ Aethon</span>`) {
		t.Error("search page should carry the display name")
	}
	rec := httptest.NewRecorder()
	named.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/projects/nope", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `<span class="dot"></span>Aethon Jam <small>Admin</small>`) {
		t.Errorf("404 page should carry the display name: %d", rec.Code)
	}
	// the name is config text: it is escaped wherever it renders
	hostile := Handler(attnFixture(t), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, func(string) bool { return true }, nil, WithDisplayName(`<i>&"x`))
	if body := frameGet(t, hostile, "/ui/"); strings.Contains(body, `<i>&"x`) || !strings.Contains(body, "&lt;i&gt;&amp;&#34;x Jam") {
		t.Error("the display name must be HTML-escaped")
	}
	body := frameGet(t, frameHandler(t), "/ui/")
	for _, want := range []string{"<title>Jam — Dashboard</title>", `<span class="dot"></span>Jam <small>Admin</small>`, `<span class="name">◉ Jam</span>`, `<div class="scope-title">Jam</div>`} {
		if !strings.Contains(body, want) {
			t.Errorf("unnamed Jam missing %s", want)
		}
	}
}

// The UI talks about agents; studios and sessions only where a page addresses
// one directly. These labels must not come back.
func TestAgentWording(t *testing.T) {
	h := frameHandler(t)
	for _, path := range []string{"/ui/", "/ui/agents", "/ui/projects/acme", "/ui/projects/acme/agents", "/ui/projects/acme/roles/dev",
		"/ui/destinations", "/ui/destinations/git-a", "/ui/model-specs", "/ui/projects/acme/members"} {
		body := frameGet(t, h, path)
		for _, old := range []string{"Session context", "Standing sessions", "Request session", "No studios.", "Studio connector",
			"<h2>Studios</h2>", "<th>Studio</th>", "studio traffic", "How a studio runs", "personal session of this role"} {
			if strings.Contains(body, old) {
				t.Errorf("%s still says %q", path, old)
			}
		}
	}
}
