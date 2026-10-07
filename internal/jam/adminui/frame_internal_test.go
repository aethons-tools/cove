package adminui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if body := frameGet(t, h, "/ui/projects/acme/roles"); !strings.Contains(body, `hx-get="/ui/rail?scope=acme" hx-trigger="every 3s"`) {
		t.Error("rail should poll with its scope")
	}
	body := frameGet(t, h, "/ui/rail?scope=acme")
	if !strings.HasPrefix(strings.TrimSpace(body), `<aside id="rail"`) || strings.Contains(body, "<html") ||
		!strings.Contains(body, `<a href="/ui/projects/acme" aria-current="page">`) {
		t.Errorf("rail fragment:\n%s", body)
	}
	if body := frameGet(t, h, "/ui/rail?scope=~"); !strings.Contains(body, `<a class="jam" href="/ui/" aria-current="page">`) {
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
