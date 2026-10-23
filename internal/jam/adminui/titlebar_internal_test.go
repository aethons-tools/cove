package adminui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

// asAdmin serves h as the gate would for a Local admin: the principal on the
// request and the shell (with BadgeSource's tab badges over store) on the
// context.
func asAdmin(h http.Handler, store jam.Store, conds *condition.Tracker) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	})
	return appui.WithShell(inner, appui.Shell{AdminBadges: BadgeSource(store, nil, conds)})
}

// adminFrameHandler is frameHandler served to a Local admin.
func adminFrameHandler(t *testing.T) http.Handler {
	t.Helper()
	st := attnFixture(t)
	return asAdmin(Handler(st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, func(string) bool { return true }, nil), st, nil)
}

// titleTabs is the title bar's tab row.
func titleTabs(body string) string { return between(body, `<nav class="app-tabs"`, "</nav>") }

// A Jam page renders the app title bar with its tab current, full width:
// no old top bar, no rail, no in-page tab row.
func TestJamPageTitlebar(t *testing.T) {
	body := frameGet(t, adminFrameHandler(t), "/app/admin/agents")
	if !strings.Contains(body, `class="app-titlebar"`) {
		t.Fatal("no app title bar")
	}
	if tabs := titleTabs(body); !strings.Contains(tabs, `href="/app/admin/agents" aria-current="page">Agents`) || strings.Count(tabs, "aria-current") != 1 {
		t.Errorf("Agents should be the one current title-bar tab:\n%s", tabs)
	}
	for _, gone := range []string{`class="topbar"`, `id="rail"`, `<nav class="tabs"`, `class="railbtn"`, `<small>Admin</small>`} {
		if strings.Contains(body, gone) {
			t.Errorf("Jam page still carries %s", gone)
		}
	}
	if !strings.Contains(body, `<div class="shell solo">`) {
		t.Error("a page without a rail should be single-column")
	}
}

// A project page keeps a rail of projects only, its own tabs, and Projects
// current in the title bar.
func TestProjectPageTitlebarAndRail(t *testing.T) {
	body := frameGet(t, adminFrameHandler(t), "/app/admin/projects/acme")
	rail := between(body, `<aside id="rail"`, "</aside>")
	if !strings.Contains(rail, `<a href="/app/admin/projects/acme" aria-current="page">`) || !strings.Contains(rail, `href="/app/admin/projects/beta"`) {
		t.Errorf("rail should list the projects with acme current:\n%s", rail)
	}
	if strings.Contains(rail, "◉") || strings.Contains(rail, `class="jam"`) || strings.Contains(rail, `href="/app/admin/"`) {
		t.Errorf("rail should carry no Jam entry:\n%s", rail)
	}
	if tabs := between(body, `<nav class="tabs"`, "</nav>"); !strings.Contains(tabs, `href="/app/admin/projects/acme" aria-current="page">Overview`) {
		t.Errorf("project tabs missing:\n%s", tabs)
	}
	if !strings.Contains(titleTabs(body), `href="/app/admin/projects" aria-current="page">Projects`) {
		t.Errorf("Projects should be current in the title bar:\n%s", titleTabs(body))
	}
	if !strings.Contains(body, `class="railbtn"`) {
		t.Error("a page with a rail carries the drawer button")
	}
}

// The projects list is a page under Projects with the rail, nothing selected.
func TestProjectsListPage(t *testing.T) {
	body := frameGet(t, adminFrameHandler(t), "/app/admin/projects")
	if !strings.Contains(titleTabs(body), `href="/app/admin/projects" aria-current="page">Projects`) {
		t.Errorf("Projects should be current:\n%s", titleTabs(body))
	}
	rail := between(body, `<aside id="rail"`, "</aside>")
	if !strings.Contains(rail, `href="/app/admin/projects/acme"`) || strings.Contains(rail, "aria-current") {
		t.Errorf("rail should list projects with nothing current:\n%s", rail)
	}
	main := between(body, "<main>", "</main>")
	if !strings.Contains(main, "<h1>Projects</h1>") || !strings.Contains(main, `href="/app/admin/projects/acme"`) ||
		!strings.Contains(main, `<span class="badge red" title="3 broken · 3 config">6</span>`) {
		t.Errorf("projects list content:\n%s", main)
	}
}

// The Jam-wide channel log sits under Health as "Channel log".
func TestChannelLogUnderHealth(t *testing.T) {
	h := adminFrameHandler(t)
	if body := frameGet(t, h, "/app/admin/health"); !strings.Contains(between(body, "<main>", "</main>"), `<a href="/app/admin/intercom">Channel log</a>`) {
		t.Error("Health should link the Channel log")
	}
	body := frameGet(t, h, "/app/admin/intercom")
	if !strings.Contains(titleTabs(body), `href="/app/admin/health" aria-current="page">Health`) {
		t.Errorf("Health should be current on the channel log:\n%s", titleTabs(body))
	}
	for _, want := range []string{"<title>Jam — Channel log</title>", "<h1>Channel log</h1>"} {
		if !strings.Contains(body, want) {
			t.Errorf("channel log missing %s", want)
		}
	}
}

// Specs pages keep their sub-tabs, with Specs current in the title bar.
func TestSpecsUnderTitlebar(t *testing.T) {
	body := frameGet(t, adminFrameHandler(t), "/app/admin/kits")
	if !strings.Contains(titleTabs(body), `href="/app/admin/specs" aria-current="page">Specs`) {
		t.Errorf("Specs should be current:\n%s", titleTabs(body))
	}
	if strip := between(body, `<nav class="subtabs"`, "</nav>"); !strings.Contains(strip, `aria-current="page">Kits</a>`) {
		t.Errorf("sub-tabs:\n%s", strip)
	}
}

// The search box sits in the slim bar under the title bar.
func TestSearchInSubbar(t *testing.T) {
	bar := between(frameGet(t, adminFrameHandler(t), "/app/admin/agents"), `<div class="subbar">`, "</form>")
	for _, want := range []string{`action="/app/admin/search"`, `id="q-top"`} {
		if !strings.Contains(bar, want) {
			t.Errorf("subbar missing %s:\n%s", want, bar)
		}
	}
}

// The title-bar tabs carry the attention badges: each Jam tab its own items,
// Dashboard Jam's total, Projects every project's.
func TestTitlebarBadges(t *testing.T) {
	tabs := titleTabs(frameGet(t, adminFrameHandler(t), "/app/admin/"))
	for _, want := range []string{
		`>Specs <span class="badge" title="3 config">3</span>`,
		`>Dashboard <span class="badge" title="3 config">3</span>`,
		`>Projects <span class="badge red" title="3 broken · 3 config">6</span>`,
		`>Agents</a>`,
	} {
		if !strings.Contains(tabs, want) {
			t.Errorf("title-bar tabs missing %s in\n%s", want, tabs)
		}
	}
}

func TestBadgeSourceCountsConditions(t *testing.T) {
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "k:x", Severity: condition.Warning, Summary: "x"})
	b := BadgeSource(jam.NewMemStore(), nil, tr)(httptest.NewRequest(http.MethodGet, "/app/admin/", nil))
	if b["dashboard"].N != 1 || b["health"].N != 1 || b["projects"].N != 0 || b["agents"].N != 0 {
		t.Errorf("badges = %+v", b)
	}
}
