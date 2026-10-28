package appui_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
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

var adminKeys = []string{"dashboard", "config", "agents", "health"}

// The admin tabs open the admin app's pages (the user area shows them too).
func TestTitlebarAdminHrefs(t *testing.T) {
	tb := titlebarFor(t, appui.Principal{Admin: true, Operator: jam.Operator{ID: "op"}}, "")
	got := map[string]string{}
	for _, tab := range tb.Tabs {
		got[tab.Key] = tab.Href
	}
	want := map[string]string{"dashboard": "/app/admin/", "config": "/app/admin/config/tracking", "agents": "/app/admin/agents", "health": "/app/admin/health"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hrefs = %v", got)
	}
}

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
	if tb.Initial != "?" {
		t.Errorf("empty name's initial = %q", tb.Initial)
	}
}
