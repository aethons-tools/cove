package adminui_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
)

func TestAgentsAPI(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	for _, r := range []string{"coder", "rev"} {
		if err := store.PutRole("acme", jam.Role{Name: r}); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	h := adminui.Handler(store, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil, anyCred, nil)
	var e adminui.Enrolled
	if rec := apiDo(t, h, "POST", "/app/admin/api/enrollments", map[string]string{"id": "bot-1", "role": "coder", "project": "acme"}, &e); rec.Code != 200 || e.Token == "" || e.ID != "bot-1" {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(logs.String(), e.Token) {
		t.Error("the token reached the log")
	}
	var list adminui.AgentsResponse
	rec := apiDo(t, h, "GET", "/app/admin/api/agents", nil, &list)
	if strings.Contains(rec.Body.String(), e.Token) || len(list.Agents) != 1 || list.Agents[0].Status != "pending" || list.CanEdit || len(list.Filters) == 0 {
		t.Fatalf("list = %+v", list)
	}
	var d adminui.AgentDetail
	rec = apiDo(t, h, "GET", "/app/admin/api/agents/bot-1", nil, &d)
	if strings.Contains(rec.Body.String(), e.Token) || !d.HasActor || d.Running || len(d.Grants) != 1 {
		t.Fatalf("detail = %+v", d)
	}
	apiDo(t, h, "POST", "/app/admin/api/agents/bot-1/grants", map[string]string{"role": "rev", "project": "acme"}, &d)
	if len(d.Grants) != 2 {
		t.Errorf("grants = %+v", d.Grants)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/agents/bot-1/grants/acme/rev", nil, &d)
	if len(d.Grants) != 1 {
		t.Errorf("after remove = %+v", d.Grants)
	}
	apiDo(t, h, "GET", "/app/admin/api/agents?status=running", nil, &list)
	if len(list.Agents) != 0 || list.Status != "running" {
		t.Errorf("filtered = %+v", list)
	}
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/enrollments/bot-1", nil, nil); rec.Code != 204 {
		t.Errorf("revoke = %d %s", rec.Code, rec.Body)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/agents/bot-1", nil, nil), 404, "not_found")
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/agents", map[string]string{"id": "x", "role": "coder", "project": "acme"}, nil), 503, "internal")
}

func TestAgentsAPIRaise(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), newSup(t, store), nil, anyCred, nil)
	var c adminui.Created
	if rec := apiDo(t, h, "POST", "/app/admin/api/agents", map[string]string{"id": "w1", "role": "worker", "project": "acme"}, &c); rec.Code != 201 || !strings.HasPrefix(c.Path, "agents/") {
		t.Fatalf("raise = %d %s", rec.Code, rec.Body)
	}
	var list adminui.AgentsResponse
	apiDo(t, h, "GET", "/app/admin/api/agents", nil, &list)
	if !list.CanEdit || len(list.Agents) != 1 || !list.Agents[0].HasStudio {
		t.Fatalf("list = %+v", list)
	}
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/agents/"+c.ID+"/studio", nil, nil); rec.Code != 204 {
		t.Errorf("teardown = %d %s", rec.Code, rec.Body)
	}
}

func TestAgentAPIGone(t *testing.T) {
	store := newStore(t)
	log := fakeLog{{At: time.Unix(100, 0), From: "ghost", FromID: "ghost-9", AgentID: "ghost-9", Channel: "eng", ChannelID: "chn_1", Body: "hi", ContentType: "text/plain"}}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, log)
	var d adminui.AgentDetail
	if rec := apiDo(t, h, "GET", "/app/admin/api/agents/ghost-9", nil, &d); rec.Code != 200 || d.Running || d.HasActor || len(d.Squawks) != 1 {
		t.Fatalf("gone = %d %+v", rec.Code, d)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/agents/nobody", nil, nil), 404, "not_found")
}

func TestAgentAPISetup(t *testing.T) {
	h, _ := setupStudioFixture(t)
	var d adminui.AgentDetail
	apiDo(t, h, "GET", "/app/admin/api/agents/sess-1", nil, &d)
	if !d.SetupEnabled || !d.Running {
		t.Fatalf("detail = %+v", d)
	}
	var s adminui.SetupResponse
	apiDo(t, h, "GET", "/app/admin/api/agents/sess-1/setup", nil, &s)
	var stale *adminui.SetupSection
	for i := range s.Sections {
		if s.Sections[i].DiffPath != "" {
			stale = &s.Sections[i]
		}
	}
	if stale == nil || s.Stale == 0 {
		t.Fatalf("no stale section with a diff path: %+v", s)
	}
	var sec adminui.SetupSection
	apiDo(t, h, "GET", "/app/admin/api/"+stale.DiffPath, nil, &sec)
	if !sec.Diffed || sec.Name != stale.Name {
		t.Errorf("section diff = %+v", sec)
	}
	apiDo(t, h, "GET", "/app/admin/api/agents/sess-1/setup?diff=role", nil, &s)
	diffed := false
	for _, x := range s.Sections {
		diffed = diffed || x.Diffed
	}
	if !diffed {
		t.Error("?diff=role diffed nothing")
	}
	h2, _ := adminHandler(t)
	wantErr(t, apiDo(t, h2, "GET", "/app/admin/api/agents/sess-1/setup", nil, nil), 404, "not_found")
}

func TestAgentAPIUnshare(t *testing.T) {
	store, carol, _ := accessFixture(t)
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithStudioAccess(collabhttp.NewLive()))
	var d adminui.AgentDetail
	apiDo(t, h, "GET", "/app/admin/api/agents/sess-1", nil, &d)
	if !d.Access.Enabled || len(d.Access.Shares) != 1 || d.Access.MembersSSH != "members" {
		t.Fatalf("access = %+v", d.Access)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/agents/sess-1/shares/"+string(carol), nil, &d)
	if len(d.Access.Shares) != 0 {
		t.Errorf("after unshare = %+v", d.Access.Shares)
	}
	wantErr(t, apiDo(t, h, "DELETE", "/app/admin/api/agents/sess-1/shares/"+string(carol), nil, nil), 404, "not_found")
}
