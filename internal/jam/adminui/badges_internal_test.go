package adminui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

// BadgeSource keys the user area's admin-tab badges by tab: config is the
// configuration trouble, agents the studios' runtime trouble (broken or out
// of date), health the open conditions, dashboard all of them.
func TestBadgeSourceTabs(t *testing.T) {
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "k:crit", Severity: condition.Critical, Summary: "down"})
	tr.Raise(condition.Condition{Key: "k:warn", Severity: condition.Warning, Summary: "slow"})
	tr.Raise(condition.Condition{Key: "k:info", Severity: condition.Info, Summary: "fyi"}) // never a badge
	b := BadgeSource(attnFixture(t), newImage{}, tr)(httptest.NewRequest(http.MethodGet, "/app/user/", nil))
	want := map[string]appui.Badge{
		// the unparseable kit, the two conflicting destinations, the roles
		// naming a missing kit and spec, the escalation naming nobody
		"config": {N: 6, Title: "6 config"},
		// lost, terminating and egress-failing studios; the stale image
		"agents":    {N: 4, Red: true, Title: "3 broken · 1 out of date"},
		"health":    {N: 2, Red: true, Title: "1 broken · 1 warning"},
		"dashboard": {N: 12, Red: true, Title: "4 broken · 1 out of date · 6 config · 1 warning"},
	}
	if len(b) != len(want) {
		t.Errorf("badges = %+v, want keys dashboard/config/agents/health", b)
	}
	for k, w := range want {
		if b[k] != w {
			t.Errorf("badge %s = %+v, want %+v", k, b[k], w)
		}
	}
}

// End to end: BadgeSource as the user area's shell wires it (appui.Shell's
// AdminBadges) puts one config item (a role naming a missing kit), one
// broken studio (lost) and one critical condition on exactly the title-bar
// tabs that own them — the keys appui.TitlebarFor looks up.
func TestBadgeSourceTitlebar(t *testing.T) {
	st := jam.NewMemStore()
	if err := st.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PushKit("ghost", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRole("acme", jam.Role{Name: "dev", Kit: "ghost"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveKit("ghost"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutInstance(jam.Instance{ActorID: "s-lost", Project: "acme", Role: "dev", Phase: jam.PhaseLost}); err != nil {
		t.Fatal(err)
	}
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "mgr:m1", Severity: condition.Critical, Summary: "manager m1 down"})

	var tb appui.Titlebar
	h := appui.WithShell(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		tb = appui.TitlebarFor(r, "")
	}), appui.Shell{AdminBadges: BadgeSource(st, nil, tr)})
	r := appui.WithPrincipal(httptest.NewRequest(http.MethodGet, "/app/user/", nil), appui.Principal{Admin: true, User: &jam.Participant{Name: "alice"}})
	h.ServeHTTP(httptest.NewRecorder(), r)

	got := map[string]appui.Badge{}
	for _, tab := range tb.Tabs {
		got[tab.Key] = tab.Badge
	}
	want := map[string]appui.Badge{
		"inbox":     {}, // the user area's own tab carries no admin badge
		"config":    {N: 1, Title: "1 config"},
		"agents":    {N: 1, Red: true, Title: "1 broken"},
		"health":    {N: 1, Red: true, Title: "1 broken"},
		"dashboard": {N: 3, Red: true, Title: "2 broken · 1 config"},
	}
	if len(got) != len(want) {
		t.Errorf("tabs = %+v", tb.Tabs)
	}
	for k, w := range want {
		if b, ok := got[k]; !ok || b != w {
			t.Errorf("tab %s badge = %+v (present %v), want %+v", k, b, ok, w)
		}
	}
}

// A category chain's target that names nobody is one config item however
// many tiers name it, saying which chain, on the project's escalation page.
func TestAttentionCategoryChainTarget(t *testing.T) {
	st := jam.NewMemStore()
	if err := st.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEscalationPolicy("acme", "deploy", []jam.EscalationTier{
		{Targets: []string{"user:ghost"}, Timeout: time.Minute},
		{Targets: []string{"user:ghost"}, Timeout: time.Hour},
	}); err != nil {
		t.Fatal(err)
	}
	rows := attnRowsOf(attention(st, nil))
	if len(rows) != 1 || rows[0].Kind != "config" || rows[0].Why != "escalation target user:ghost (chain deploy) names nobody here" ||
		rows[0].Path != "config/projects/acme/escalation" {
		t.Errorf("attention = %+v", rows)
	}
}

// With nothing wrong every tab's badge is empty; a lone warning condition
// badges health and dashboard only.
func TestBadgeSourceCountsConditions(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/app/user/", nil)
	for k, bd := range BadgeSource(jam.NewMemStore(), nil, nil)(r) {
		if bd != (appui.Badge{}) {
			t.Errorf("empty Jam: badge %s = %+v", k, bd)
		}
	}
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "k:x", Severity: condition.Warning, Summary: "x"})
	b := BadgeSource(jam.NewMemStore(), nil, tr)(r)
	if b["dashboard"].N != 1 || b["health"] != (appui.Badge{N: 1, Title: "1 warning"}) || b["config"].N != 0 || b["agents"].N != 0 {
		t.Errorf("badges = %+v", b)
	}
}
