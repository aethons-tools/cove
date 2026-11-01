package adminui_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

func TestRolesAPI(t *testing.T) {
	seed := newStore(t)
	mustCreateProject(t, seed, "acme")
	e := editOn(t, editSetup{seed: seed})
	h := e.H
	var c adminui.Created
	if rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles", map[string]any{"name": "coder", "destinations": "", "ttlSeconds": "3600"}, &c); rec.Code != 201 ||
		c.Path != "config/projects/acme/agents/coder" {
		t.Fatalf("create = %d %+v %s", rec.Code, c, rec.Body)
	}
	rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles", map[string]any{"name": "coder"}, nil)
	wantErr(t, rec, 409, "conflict")
	if !strings.Contains(rec.Body.String(), "; edit it on its page") {
		t.Errorf("409 message = %s", rec.Body)
	}
	var d adminui.RoleDetail
	if rec := apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/coder", nil, &d); rec.Code != 200 || d.Name != "coder" || d.TTL != "1h" || d.Form.TTL != "1h" {
		t.Fatalf("get = %d %+v", rec.Code, d)
	}
	if d.Dests == nil || d.Addressing == nil || d.Standing == nil || d.Holders == nil || d.Running == nil || d.Allocation == nil {
		t.Errorf("nil lists: %+v", d)
	}
	rec = apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/coder/scope", map[string]string{"kit": "nope"}, nil)
	wantErr(t, rec, 400, "bad_request")
	if !strings.Contains(rec.Body.String(), `kit \"nope\" does not exist`) {
		t.Errorf("missing kit = %s", rec.Body)
	}
	var res adminui.RoleResult
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/coder/scope", map[string]string{"ttl": "2h", "addressing": "user:*, channel:eng"}, &res)
	if res.Role.TTL != "2h" || strings.Join(res.Role.Addressing, ",") != "user:*,channel:eng" {
		t.Errorf("scope = %+v", res.Role)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/coder/allocation", map[string]string{"maxPersonal": "x"}, nil), 400, "bad_request")
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/coder/allocation", map[string]string{"maxPersonal": "2", "idleAfter": "90m"}, &res)
	if r, _ := e.Store().GetRole("acme", "coder"); r.Allocation.MaxPersonal != 2 || r.Allocation.IdleAfter != 90*time.Minute {
		t.Errorf("allocation = %+v", r.Allocation)
	}
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/coder/egress", map[string]string{"domains": "a.example.com\n.b.example.com"}, &res)
	if !res.Role.EgressManaged || len(res.Role.EgressDomains) != 2 {
		t.Errorf("egress = %+v", res.Role)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/projects/acme/roles/coder/egress", nil, &res)
	if res.Role.EgressManaged {
		t.Error("egress still managed")
	}
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/coder/context", map[string]string{"yaml": "core: hi\n"}, &res)
	if res.Role.Context.Core != "hi" || res.Role.Context.Scope != "role" {
		t.Errorf("context = %+v", res.Role.Context)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/projects/acme/roles/coder/context", nil, &res)
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/coder/standing", map[string]string{"name": "ic", "prompt": "Mind the shop."}, &res)
	if len(res.Role.Standing) != 1 || res.Role.Standing[0].Name != "ic" {
		t.Fatalf("standing = %+v", res.Role.Standing)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/projects/acme/roles/coder/standing/ic", nil, &res)
	if len(res.Role.Standing) != 0 {
		t.Errorf("after dismiss = %+v", res.Role.Standing)
	}
	var pa adminui.ProjectAgents // live state: read over the branch's config
	apiDo(t, uiHandler(t, e.Store()), "GET", "/app/admin/api/projects/acme/agents", nil, &pa)
	if len(pa.Roles) != 1 || pa.Roles[0].Name != "coder" || pa.Agents == nil || pa.CanRequest {
		t.Errorf("agents view = %+v", pa)
	}
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/projects/acme/roles/coder", nil, nil); rec.Code != 204 {
		t.Errorf("delete = %d %s", rec.Code, rec.Body)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/coder", nil, nil), 404, "not_found")
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/projects/gone/agents", nil, nil), 404, "not_found")
}

func TestRoleRequestLocal(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "coder"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil)
	var d adminui.RoleDetail
	apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/coder", nil, &d)
	if !d.CanRequest {
		t.Error("canRequest false with a supervisor")
	}
	// The gate names a loopback admin's operator "local".
	local := appui.Principal{Admin: true, Local: true, Operator: jam.Operator{ID: "local"}}
	rec := apiDoAs(t, h, local, "POST", "/app/admin/api/projects/acme/roles/coder/request", map[string]string{}, nil)
	wantErr(t, rec, 403, "forbidden")
	if !strings.Contains(rec.Body.String(), "sign in to request an agent (/app/auth/login)") {
		t.Errorf("request = %s", rec.Body)
	}
	if len(store.ListInstances()) != 0 {
		t.Error("a refused request raised something")
	}
}

func TestRoleSetupOff(t *testing.T) {
	h, store := adminHandler(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	var d adminui.RoleDetail
	apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/dev", nil, &d)
	if d.SetupEnabled || d.CanRequest {
		t.Errorf("setup %v request %v without deps", d.SetupEnabled, d.CanRequest)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/dev/setup", nil, nil), 404, "not_found")
}

func TestRoleSetup(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "dev", Scope: jam.Scope{Egress: &jam.EgressPolicy{Domains: []string{"new.example.com"}}}}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	h := adminui.Handler(store, slog.New(slog.NewTextHandler(&logs, nil)), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithSetupDeps(jam.SetupDeps{Store: store}))
	var d adminui.RoleDetail
	apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/dev", nil, &d)
	if !d.SetupEnabled {
		t.Error("setup off with deps")
	}
	var s adminui.SetupResponse
	apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/dev/setup", nil, &s)
	if len(s.Sections) != len(jam.SetupSectionNames()) || len(s.SectionNames) != len(jam.SetupSectionNames()) || s.Sections == nil {
		t.Fatalf("setup = %+v", s)
	}
	found := false
	for _, sec := range s.Sections {
		found = found || strings.Contains(sec.Body, "new.example.com")
	}
	if !found || !strings.Contains(logs.String(), "ui setup inspected") {
		t.Errorf("egress body or audit line missing: %+v / %s", s.Sections, logs.String())
	}
	apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/dev/setup?sections=egress", nil, &s)
	if len(s.Sections) != 1 || !s.Sections[0].Open {
		t.Errorf("filtered = %+v", s.Sections)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/dev/setup?sections=bogus", nil, nil), 400, "bad_request")
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/nope/setup", nil, nil), 404, "not_found")
}

// A pending standing reset answers 200 with the reason as the message.
func TestRoleStandingResetPending(t *testing.T) {
	store := seedRichRole(t)
	sup := newSup(t, store)
	sup.SetStandingResetter(&teardownResetter{sup: sup, err: errors.New("volume in use")})
	id := jam.SeedStandingSession(store, "acme", "review", "nightly")
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: id, Project: "acme", Role: "review", Name: "nightly", SessionKind: jam.SessionKindStanding}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), sup, nil, credKnown, nil)
	var res adminui.RoleResult
	if rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/review/standing/nightly/reset", nil, &res); rec.Code != 200 || !strings.Contains(res.Message, "volume in use") {
		t.Errorf("pending reset = %d %+v", rec.Code, res.Message)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/review/standing/nobody/reset", nil, nil), 404, "not_found")
}
