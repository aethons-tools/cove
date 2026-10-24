package appui_test

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

func titlebarFor(t *testing.T, p appui.Principal, current string) appui.Titlebar {
	t.Helper()
	var got appui.Titlebar
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = appui.TitlebarFor(r, current) })
	sh := appui.Shell{Brand: "Acme Jam", SignOut: true, AdminBadges: func(*http.Request) map[string]appui.Badge {
		return map[string]appui.Badge{"health": {N: 2, Red: true, Title: "2 red"}}
	}}
	r := appui.WithPrincipal(httptest.NewRequest(http.MethodGet, "/app/", nil), p)
	appui.WithShell(probe, sh).ServeHTTP(httptest.NewRecorder(), r)
	return got
}

func keys(tb appui.Titlebar) []string {
	var k []string
	for _, tab := range tb.Tabs {
		k = append(k, tab.Key)
	}
	return k
}

var adminKeys = []string{"dashboard", "projects", "agents", "users", "specs", "config", "health"}

func TestTitlebarTabsAndRoles(t *testing.T) {
	u := &jam.Participant{Name: "Ada"}
	op := jam.Operator{ID: "op"}
	cases := []struct {
		name  string
		p     appui.Principal
		keys  []string
		roles string
		out   bool
	}{
		{"member", appui.Principal{User: u}, []string{"inbox"}, "member", true},
		{"admin", appui.Principal{Admin: true, Operator: op}, adminKeys, "admin", true},
		{"both", appui.Principal{User: u, Admin: true, Operator: op}, append([]string{"inbox"}, adminKeys...), "member · admin", true},
		{"local", appui.Principal{Admin: true, Local: true}, adminKeys, "admin", false},
	}
	for _, c := range cases {
		tb := titlebarFor(t, c.p, "")
		if !reflect.DeepEqual(keys(tb), c.keys) || tb.Roles != c.roles || tb.SignOut != c.out || tb.Brand != "Acme Jam" {
			t.Errorf("%s: %v %q %v %q", c.name, keys(tb), tb.Roles, tb.SignOut, tb.Brand)
		}
	}
	if tb := titlebarFor(t, cases[3].p, ""); tb.Name != "Local admin" {
		t.Errorf("name = %q", tb.Name)
	}
}

func TestTitlebarCurrentAndBadges(t *testing.T) {
	tb := titlebarFor(t, appui.Principal{Admin: true, Operator: jam.Operator{ID: "op"}}, "agents")
	for _, tab := range tb.Tabs {
		if tab.Current != (tab.Key == "agents") {
			t.Errorf("%s current = %v", tab.Key, tab.Current)
		}
		if tab.Key == "health" && (tab.Badge != appui.Badge{N: 2, Red: true, Title: "2 red"}) {
			t.Errorf("health badge = %+v", tab.Badge)
		}
		if tab.Key != "health" && tab.Badge.N != 0 {
			t.Errorf("%s badge = %+v", tab.Key, tab.Badge)
		}
	}
}

func TestTitlebarBrandFallbackAndEmptyName(t *testing.T) {
	r := appui.WithPrincipal(httptest.NewRequest(http.MethodGet, "/app/", nil), appui.Principal{User: &jam.Participant{}})
	tb := appui.TitlebarFor(r, "") // no shell at all
	if tb.Brand != "Jam" {
		t.Errorf("brand = %q", tb.Brand)
	}
	if out := render(t, tb); !strings.Contains(out, "app-titlebar") {
		t.Errorf("empty name render: %s", out)
	}
}

func render(t *testing.T, tb appui.Titlebar) string {
	t.Helper()
	tpl := template.Must(template.New("x").Parse(appui.TitlebarTemplate))
	var sb strings.Builder
	if err := tpl.ExecuteTemplate(&sb, "app-titlebar", tb); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func TestTitlebarRender(t *testing.T) {
	member := render(t, titlebarFor(t, appui.Principal{User: &jam.Participant{Name: "Ada"}}, "inbox"))
	for _, want := range []string{`href="/app/user/"`, `src="/app/static/brand/j-logo-plain.svg"`, "Acme Jam", "/app/auth/logout"} {
		if !strings.Contains(member, want) {
			t.Errorf("member missing %q", want)
		}
	}
	if strings.Contains(member, "/app/admin/") {
		t.Error("member sees admin links")
	}
	local := render(t, titlebarFor(t, appui.Principal{Admin: true, Local: true}, "dashboard"))
	if !strings.Contains(local, "Local admin") || strings.Contains(local, "/app/auth/logout") {
		t.Errorf("local: %s", local)
	}
}

func TestTitlebarRenderEscapesHostileStrings(t *testing.T) {
	tb := titlebarFor(t, appui.Principal{User: &jam.Participant{Name: `"><img onerror=1>`}}, "inbox")
	tb.Brand = `<b>x</b>`
	out := render(t, tb)
	for _, raw := range []string{`<b>x</b>`, `<img onerror=1>`, `"><img onerror`} {
		if strings.Contains(out, raw) {
			t.Errorf("raw %q in output: %s", raw, out)
		}
	}
	for _, esc := range []string{`&lt;b&gt;x&lt;/b&gt;`, `&lt;img onerror=1&gt;`} {
		if !strings.Contains(out, esc) {
			t.Errorf("missing escaped %q: %s", esc, out)
		}
	}
}

func TestTitlebarPushItem(t *testing.T) {
	u := &jam.Participant{Name: "Ada", UserID: "usr_ada"}
	op := jam.Operator{ID: "op"}
	bar := func(p appui.Principal, push bool) string {
		t.Helper()
		var got appui.Titlebar
		probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = appui.TitlebarFor(r, "") })
		r := appui.WithPrincipal(httptest.NewRequest(http.MethodGet, "/app/", nil), p)
		appui.WithShell(probe, appui.Shell{Push: push}).ServeHTTP(httptest.NewRecorder(), r)
		return render(t, got)
	}
	for _, c := range []struct {
		name string
		p    appui.Principal
		push bool
		want bool
	}{
		{"member, push on", appui.Principal{User: u}, true, true},
		{"member+admin, push on", appui.Principal{User: u, Admin: true, Operator: op}, true, true},
		{"member, push off", appui.Principal{User: u}, false, false},
		{"admin only, push on", appui.Principal{Admin: true, Operator: op}, true, false},
		{"local admin, push on", appui.Principal{Admin: true, Local: true}, true, false},
	} {
		out := bar(c.p, c.push)
		if got := strings.Contains(out, `id="push-toggle"`); got != c.want {
			t.Errorf("%s: push item shown = %v, want %v", c.name, got, c.want)
		}
		if got := strings.Contains(out, `data-user="usr_ada"`); got != c.want {
			t.Errorf("%s: push item user = %v, want %v", c.name, got, c.want)
		}
		if got := strings.Contains(out, `src="/app/static/push.js?v=`); got != c.want {
			t.Errorf("%s: push.js included = %v, want %v", c.name, got, c.want)
		}
	}
}

// A member's menu offers Sounds (with or without push); an admin-only
// principal's doesn't.
func TestTitlebarSoundsItem(t *testing.T) {
	u := &jam.Participant{Name: "Ada", UserID: "usr_ada"}
	bar := func(p appui.Principal) string {
		t.Helper()
		var got appui.Titlebar
		probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = appui.TitlebarFor(r, "") })
		r := appui.WithPrincipal(httptest.NewRequest(http.MethodGet, "/app/", nil), p)
		appui.WithShell(probe, appui.Shell{}).ServeHTTP(httptest.NewRecorder(), r)
		return render(t, got)
	}
	out := bar(appui.Principal{User: u})
	for _, want := range []string{`id="sounds-toggle"`, `id="sounds-panel"`, `name="messages"`, `name="needs"`, `name="volume"`, `data-preview="needs"`, `src="/app/static/sounds.js?v=`} {
		if !strings.Contains(out, want) {
			t.Errorf("member menu missing %q", want)
		}
	}
	if out := bar(appui.Principal{Admin: true, Operator: jam.Operator{ID: "op"}}); strings.Contains(out, "sounds-toggle") {
		t.Error("an admin-only menu offers Sounds")
	}
}
