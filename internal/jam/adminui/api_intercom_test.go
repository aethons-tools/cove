package adminui_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// logBodies is the rows' bodies, newest first, joined by "|".
func logBodies(t *testing.T, h http.Handler, query string) string {
	t.Helper()
	var l adminui.ChannelLog
	if rec := apiDo(t, h, "GET", "/app/admin/api/intercom"+query, nil, &l); rec.Code != 200 {
		t.Fatalf("GET intercom%s = %d %s", query, rec.Code, rec.Body)
	}
	var bodies []string
	for _, r := range l.Rows {
		bodies = append(bodies, rowText(r))
	}
	return strings.Join(bodies, "|")
}

// rowText is a row's rendered body as its text (one paragraph).
func rowText(r adminui.SquawkRow) string {
	return strings.TrimSpace(strings.NewReplacer("<p>", "", "</p>", "").Replace(r.Body))
}

// A legacy squawk matches a participant that is its author or one of its
// recipients (kind:ref); filters intersect; a day window is inclusive.
func TestChannelLogAPILegacyFilters(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, fixtureLog(t))
	for query, want := range map[string]string{
		"?log=legacy&participant=channel:eng":               "deploy started",             // a recipient only
		"?log=legacy&participant=actor:cove-1":              "please HOLD|deploy started", // its sent and received
		"?log=legacy&participant=human:bob":                 "beta status",
		"?log=legacy&participant=human:nobody":              "",
		"?log=legacy&project=acme&q=deploy":                 "deploy started",
		"?log=legacy&since=2026-09-11&until=2026-09-11":     "please HOLD",
		"?log=legacy&q=nothingmatchesthis":                  "",
		"?log=legacy&since=2026-09-11":                      "beta status|please HOLD",
		"?log=legacy&until=2026-09-10":                      "deploy started",
		"?log=legacy&until=not-a-date&project=acme":         "please HOLD|deploy started", // unbounded on that side
		"?log=legacy&participant=actor:cove-1&project=beta": "",
	} {
		if got := logBodies(t, h, query); got != want {
			t.Errorf("%s = %q, want %q", query, got, want)
		}
	}
	var l adminui.ChannelLog
	apiDo(t, h, "GET", "/app/admin/api/intercom?log=legacy&until=not-a-date", nil, &l)
	if !l.BadDate || !l.Filtered {
		t.Errorf("a bad until = %+v, want it surfaced", l)
	}
}

// The channel log (the default) names each squawk's author and channel,
// links an author the store knows as an actor or a studio to its agent, and
// filters by project and by author or channel id.
func TestChannelLogAPIChannelLog(t *testing.T) {
	store := jam.NewMemStore()
	for _, p := range []string{"acme", "beta"} {
		if err := store.CreateProject(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := jam.AddPerson(store, "acme", jam.Human{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := store.LookupName(ident.User, "alice")
	acme, _ := store.GetProject("acme")
	beta, _ := store.GetProject("beta")
	eng, err := store.CreateChannel(jam.Channel{ProjectID: acme.ID, Kind: jam.SourceRoom, Key: "eng", Label: "eng"})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := store.CreateChannel(jam.Channel{ProjectID: beta.ID, Kind: jam.SourceRoom, Key: "ops", Label: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(jam.Actor{ID: "enrolled-1", TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutInstance(jam.Instance{ActorID: "studio-1", Project: "acme", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	lg := intercom.NewMemLog(nil)
	ic := jam.NewIntercom(store, func() (ident.ID, bool) { return "", false }, lg, nil, nil)
	for _, c := range []struct {
		ch   jam.Channel
		from ident.ID
		body string
	}{{eng, alice, "deploy started"}, {ops, alice, "beta status"}, {eng, "enrolled-1", "by actor"}, {eng, "studio-1", "by studio"}} {
		if _, err := ic.PostTrusted(c.ch, intercom.Squawk{From: c.from, Body: c.body, At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, adminui.NewSquawkReader(store, ic, lg, nil))

	var l adminui.ChannelLog
	apiDo(t, h, "GET", "/app/admin/api/intercom", nil, &l)
	if !l.Configured || l.Legacy || l.Filtered || len(l.Rows) != 4 {
		t.Fatalf("channel log = %+v", l)
	}
	rows := map[string]adminui.SquawkRow{}
	for _, r := range l.Rows {
		rows[rowText(r)] = r
	}
	if r := rows["deploy started"]; r.From != "alice" || r.FromID != string(alice) || r.AgentID != "" || r.Channel != "eng · room" || r.ChannelID != string(eng.ID) || r.Project != "acme" || len(r.To) != 0 {
		t.Errorf("a user's squawk = %+v", r)
	}
	if r := rows["beta status"]; r.Channel != "ops · room" || r.Project != "beta" {
		t.Errorf("beta's squawk = %+v", r)
	}
	for body, id := range map[string]string{"by actor": "enrolled-1", "by studio": "studio-1"} {
		if r := rows[body]; r.AgentID != id {
			t.Errorf("%s: agent = %q, want %q", body, r.AgentID, id)
		}
	}

	for query, want := range map[string][]string{
		"?project=beta":                                   {"beta status"},
		"?participant=" + string(eng.ID):                  {"deploy started", "by actor", "by studio"},
		"?participant=" + string(ops.ID):                  {"beta status"},
		"?participant=" + string(alice):                   {"deploy started", "beta status"},
		"?participant=studio-1":                           {"by studio"},
		"?participant=" + string(alice) + "&project=acme": {"deploy started"},
	} {
		got := strings.Split(logBodies(t, h, query), "|")
		if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
			t.Errorf("%s = %v, want %v", query, got, want)
		}
	}

	// The legacy tab of a reader with no legacy log is empty, not an error.
	var legacy adminui.ChannelLog
	apiDo(t, h, "GET", "/app/admin/api/intercom?log=legacy", nil, &legacy)
	if !legacy.Configured || !legacy.Legacy || len(legacy.Rows) != 0 {
		t.Errorf("legacy without a legacy log = %+v", legacy)
	}
}
