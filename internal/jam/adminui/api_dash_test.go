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
