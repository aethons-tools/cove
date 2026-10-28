package adminui_test

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

func TestDashboardAPI(t *testing.T) {
	store := newStore(t)
	if err := store.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	mgrs := func() []jam.ManagerStatus {
		return []jam.ManagerStatus{{Name: "m1", Studios: 1, Capacity: 4, LastSeen: time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC),
			Strays: []jam.ManagedStudio{{ActorID: "ghost"}}}}
	}
	conds := condition.New(condition.Options{})
	conds.Raise(condition.Condition{Key: "mgr:m1", Severity: condition.Critical, Summary: "manager m1 is down"})
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, adminui.WithManagers(mgrs), adminui.WithConditions(conds, ""))
	var d adminui.Dashboard
	if rec := apiDo(t, h, "GET", "/app/admin/api/dashboard", nil, &d); rec.Code != 200 {
		t.Fatalf("dashboard = %d %s", rec.Code, rec.Body)
	}
	if d.Stats.Projects < 1 {
		t.Errorf("stats = %+v", d.Stats)
	}
	// Jam-scope items only, as the old card.
	if len(d.Attention) != 1 || d.Attention[0].Path != "health" || d.Attention[0].Kind != "broken" || d.Attention[0].Why != "manager m1 is down" {
		t.Errorf("attention = %+v", d.Attention)
	}
	if !d.HasManagers || len(d.Managers) != 1 || d.Managers[0].Connected || len(d.Managers[0].Strays) != 1 || d.Managers[0].Strays[0] != "ghost" || d.Managers[0].LastSeen == "" {
		t.Errorf("managers = %+v", d.Managers)
	}
	if d.Coves == nil {
		t.Error("coves is null")
	}
	if d.JamContext.Scope != "Jam-wide" {
		t.Errorf("jam context = %+v", d.JamContext)
	}

	// Without the option, no Managers panel.
	var plain adminui.Dashboard
	apiDo(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "GET", "/app/admin/api/dashboard", nil, &plain)
	if plain.HasManagers || plain.Managers == nil {
		t.Errorf("no managers: %+v", plain)
	}
}

func TestJamContextAPI(t *testing.T) {
	store := newStore(t)
	var logs bytes.Buffer
	h := adminui.Handler(store, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil, anyCred, nil)
	var p adminui.ContextPanel
	if rec := apiDo(t, h, "POST", "/app/admin/api/jam/context", map[string]string{"yaml": "core: hello secretish"}, &p); rec.Code != 200 || !strings.Contains(p.Core, "hello secretish") {
		t.Fatalf("set = %d %s", rec.Code, rec.Body)
	}
	if rec := apiDo(t, h, "POST", "/app/admin/api/jam/context", map[string]string{"yaml": "core: [unclosed"}, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "bad_request") {
		t.Errorf("bad yaml = %d %s", rec.Code, rec.Body)
	}
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/jam/context", nil, &p); rec.Code != 200 || !p.Empty {
		t.Errorf("clear = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(logs.String(), "secretish") {
		t.Error("the context reached the log")
	}
	if !strings.Contains(logs.String(), "ui jam context set") || !strings.Contains(logs.String(), "ui jam context cleared") {
		t.Errorf("logs = %s", logs.String())
	}
	r := httptest.NewRequest("POST", "/app/admin/api/jam/context", strings.NewReader(`{"yaml":"core: x"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	if rec.Code != 403 {
		t.Errorf("cross-origin = %d", rec.Code)
	}
}

func TestHealthAPI(t *testing.T) {
	now := time.Now()
	conds := condition.New(condition.Options{Now: func() time.Time { return now }})
	conds.Raise(condition.Condition{Key: "mgr:m1", Severity: condition.Critical, Summary: "manager m1 is down", Fix: "jam manager restart m1", Detail: "dial refused"})
	conds.Raise(condition.Condition{Key: "kit:k", Severity: condition.Warning, Summary: "kit k stale"})
	now = now.Add(time.Hour)
	conds.Clear("kit:k")
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil, adminui.WithConditions(conds, "http://am.example/"))
	var d adminui.Health
	if rec := apiDo(t, h, "GET", "/app/admin/api/health", nil, &d); rec.Code != 200 {
		t.Fatalf("health = %d %s", rec.Code, rec.Body)
	}
	if len(d.Open) != 1 || d.Open[0].Severity != "critical" || d.Open[0].Fix != "jam manager restart m1" || d.Open[0].Detail != "dial refused" || d.Open[0].Key != "mgr:m1" || d.Open[0].Age == "" {
		t.Errorf("open = %+v", d.Open)
	}
	if len(d.Resolved) != 1 || d.Resolved[0].Summary != "kit k stale" || d.Resolved[0].Lasted == "" {
		t.Errorf("resolved = %+v", d.Resolved)
	}
	if d.Silences != "http://am.example/#/silences" {
		t.Errorf("silences = %q", d.Silences)
	}
	var none adminui.Health
	apiDo(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "GET", "/app/admin/api/health", nil, &none)
	if none.Open == nil || none.Resolved == nil || none.Silences != "" {
		t.Errorf("no tracker = %+v", none)
	}
}

func TestChannelLogAPI(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, fixtureLog(t))
	var l adminui.ChannelLog
	apiDo(t, h, "GET", "/app/admin/api/intercom?log=legacy&q=hold", nil, &l)
	if !l.Configured || !l.Legacy || !l.Filtered || len(l.Rows) != 1 || l.Rows[0].Body == "" || len(l.Rows[0].To) != 1 || l.Rows[0].To[0].Target != "actor:cove-1" {
		t.Errorf("q=hold = %+v", l)
	}
	l = adminui.ChannelLog{}
	apiDo(t, h, "GET", "/app/admin/api/intercom?log=legacy&since=bad&project=beta", nil, &l)
	if !l.BadDate || !l.Filtered || len(l.Rows) != 1 {
		t.Errorf("bad date = %+v", l)
	}
	l = adminui.ChannelLog{}
	apiDo(t, h, "GET", "/app/admin/api/intercom?log=legacy", nil, &l)
	if l.Filtered || len(l.Rows) != 3 || l.Rows[0].At < l.Rows[2].At {
		t.Errorf("all = %+v", l)
	}
	var off adminui.ChannelLog
	apiDo(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "GET", "/app/admin/api/intercom", nil, &off)
	if off.Configured || off.Rows == nil {
		t.Errorf("no log = %+v", off)
	}
}

func TestSearchAPI(t *testing.T) {
	store := newStore(t)
	alice, err := store.CreateUser(jam.User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser(jam.User{Name: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, fixtureLog(t))
	var r adminui.SearchResult
	apiDo(t, h, "GET", "/app/admin/api/search?q=a", nil, &r)
	if !r.TooShort || r.Groups == nil {
		t.Errorf("q=a = %+v", r)
	}
	r = adminui.SearchResult{}
	apiDo(t, h, "GET", "/app/admin/api/search?q=alice", nil, &r)
	if r.Jump != "config/users/"+string(alice.ID) || r.Total != 1 {
		t.Errorf("q=alice = %+v", r)
	}
	r = adminui.SearchResult{}
	apiDo(t, h, "GET", "/app/admin/api/search?q=acme", nil, &r)
	if r.Jump != "" || len(r.Groups) < 2 {
		t.Errorf("q=acme (two exact) = %+v", r)
	}
	r = adminui.SearchResult{}
	apiDo(t, h, "GET", "/app/admin/api/search?q=deploy+started", nil, &r)
	var squawks *adminui.SearchGroup
	for i, g := range r.Groups {
		if g.Key == "squawks" {
			squawks = &r.Groups[i]
		}
		for _, hit := range g.Hits {
			if strings.HasPrefix(hit.Path, "/") || strings.Contains(hit.Path, "/app/admin") {
				t.Errorf("hit path %q is not an app path", hit.Path)
			}
		}
	}
	if squawks == nil || squawks.More != "health/intercom?q=deploy+started" {
		t.Errorf("squawks = %+v", squawks)
	}
}
