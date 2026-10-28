package adminui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// fakeLog is a SquawkReader holding fixed entries.
type fakeLog []adminui.Logged

func (l fakeLog) Squawks(legacy bool) []adminui.Logged {
	if legacy {
		return nil
	}
	return l
}

func TestProjectsAPI(t *testing.T) {
	h, store := adminHandler(t)
	var c adminui.Created
	if rec := apiDo(t, h, "POST", "/app/admin/api/projects", map[string]string{"name": "acme"}, &c); rec.Code != 201 || c.Path != "config/projects/acme" {
		t.Fatalf("create = %d %+v %s", rec.Code, c, rec.Body)
	}
	var list adminui.ProjectsResponse
	apiDo(t, h, "GET", "/app/admin/api/projects", nil, &list)
	if len(list.Projects) != 1 || list.Projects[0].Name != "acme" {
		t.Fatalf("list = %+v", list)
	}
	var d adminui.ProjectDetail
	if rec := apiDo(t, h, "GET", "/app/admin/api/projects/acme", nil, &d); rec.Code != 200 || d.Name != "acme" || !d.Renamable || d.Context.Scope != "project" {
		t.Fatalf("get = %d %+v", rec.Code, d)
	}
	if d.Members == nil || d.Rooms == nil || d.Escalation == nil || d.Squawks == nil || d.ChatServices == nil {
		t.Errorf("nil lists: %+v", d)
	}
	if rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/rename", map[string]string{"name": "acme2"}, &c); rec.Code != 200 || c.Path != "config/projects/acme2" {
		t.Fatalf("rename = %d %+v %s", rec.Code, c, rec.Body)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/projects/acme", nil, nil), 404, "not_found")
	if err := store.PutRole("acme2", jam.Role{Name: "coder"}); err != nil {
		t.Fatal(err)
	}
	apiDo(t, h, "GET", "/app/admin/api/projects/acme2", nil, &d)
	if d.InUseBy == "" || d.Roles != 1 {
		t.Errorf("in use = %+v", d)
	}
	wantErr(t, apiDo(t, h, "DELETE", "/app/admin/api/projects/acme2", nil, nil), 409, "conflict")
	if err := store.RemoveRole("acme2", "coder"); err != nil {
		t.Fatal(err)
	}
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/projects/acme2", nil, nil); rec.Code != 204 {
		t.Errorf("delete = %d %s", rec.Code, rec.Body)
	}
	if _, ok := store.GetProject("default"); !ok {
		mustCreateProject(t, store, "default")
	}
	d = adminui.ProjectDetail{}
	apiDo(t, h, "GET", "/app/admin/api/projects/default", nil, &d)
	if d.Name != "default" || d.Renamable {
		t.Error("default is renamable")
	}
}

func TestProjectSectionsAPI(t *testing.T) {
	h, store := adminHandler(t)
	mustCreateProject(t, store, "acme")
	if _, err := store.CreateConnection(jam.Connection{Kind: "discord", Name: "disc", CredName: "tok"}); err != nil {
		t.Fatal(err)
	}
	var d adminui.ProjectDetail
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/chat-service", map[string]string{"service": "disc"}, &d)
	if d.ChatService != "disc" {
		t.Errorf("chat service = %q", d.ChatService)
	}
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/context", map[string]string{"yaml": "core: hi\n"}, &d)
	if d.Context.Core != "hi" || d.Context.Empty {
		t.Errorf("context = %+v", d.Context)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/projects/acme/context", nil, &d)
	if !d.Context.Empty {
		t.Errorf("cleared context = %+v", d.Context)
	}
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/rooms", map[string]string{"name": "eng", "connection": "disc", "ref": "123"}, &d)
	if len(d.Rooms) != 1 || d.Rooms[0].Name != "eng" || d.Rooms[0].Ref != "123" {
		t.Fatalf("rooms = %+v", d.Rooms)
	}
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/escalation", map[string]string{"category": "", "tiers": "user:ghost,channel:eng@15m"}, &d)
	if len(d.DefaultChain.Tiers) != 1 || len(d.Escalation) != 1 {
		t.Fatalf("chain = %+v", d.DefaultChain)
	}
	tg := d.DefaultChain.Tiers[0].Targets
	if len(tg) != 2 || !tg[0].Unknown || tg[1].Unknown || d.DefaultChain.Tiers[0].Timeout != "15m" {
		t.Errorf("targets = %+v", d.DefaultChain.Tiers[0])
	}
	rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/escalation", map[string]string{"category": "x", "tiers": "user:a@soon"}, nil)
	wantErr(t, rec, 400, "bad_request")
	apiDo(t, h, "DELETE", "/app/admin/api/projects/acme/escalation?category=", nil, &d)
	if len(d.Escalation) != 0 {
		t.Errorf("after clear = %+v", d.Escalation)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/projects/acme/rooms/eng", nil, &d)
	if len(d.Rooms) != 0 {
		t.Errorf("after room removal = %+v", d.Rooms)
	}
}

func TestProjectMembersAPI(t *testing.T) {
	h, store := adminHandler(t)
	mustCreateProject(t, store, "acme")
	alice, _ := store.CreateUser(jam.User{Name: "alice"})
	var d adminui.ProjectDetail
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/members", map[string]any{"user": "alice", "delivery": "discord:chan-1", "add": true}, &d)
	if len(d.Members) != 1 || d.Members[0].UserID != string(alice.ID) || d.Members[0].DeliverySpec != "discord:chan-1" {
		t.Fatalf("members = %+v", d.Members)
	}
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/members", map[string]any{"user": "alice", "delivery": "", "add": true}, &d)
	if d.Members[0].DeliverySpec != "discord:chan-1" {
		t.Errorf("re-add lost delivery: %+v", d.Members[0])
	}
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/members", map[string]any{"user": string(alice.ID), "delivery": ""}, &d)
	if d.Members[0].DeliverySpec != "" {
		t.Errorf("edit kept delivery: %+v", d.Members[0])
	}
	rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/members", map[string]any{"user": "alice", "delivery": "discord:user:42"}, nil)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "is an account") {
		t.Errorf("discord user id = %d %s", rec.Code, rec.Body)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/projects/acme/members/"+string(alice.ID), nil, &d)
	if len(d.Members) != 0 {
		t.Errorf("after remove = %+v", d.Members)
	}
}

func TestProjectAPIGone(t *testing.T) {
	h, _ := adminHandler(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/app/admin/api/projects/gone/chat-service"}, {"POST", "/app/admin/api/projects/gone/members"},
		{"DELETE", "/app/admin/api/projects/gone/rooms/x"}, {"POST", "/app/admin/api/projects/gone/escalation"},
	} {
		var body any
		if c.method == "POST" {
			body = map[string]string{}
		}
		wantErr(t, apiDo(t, h, c.method, c.path, body, nil), 404, "not_found")
	}
}

func TestProjectIntercomLog(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	log := fakeLog{{At: time.Unix(100, 0), From: "alice", FromID: "usr_1", Channel: "eng", ChannelID: "chn_1", Project: "acme", Body: "**hi**", ContentType: "text/markdown"}}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, log)
	var d adminui.ProjectDetail
	apiDo(t, h, "GET", "/app/admin/api/projects/acme?section=intercom", nil, &d)
	if !d.LogConfigured || len(d.Squawks) != 1 || !strings.Contains(d.Squawks[0].Body, "<strong>hi</strong>") || d.Squawks[0].From != "alice" {
		t.Errorf("log = %v %+v", d.LogConfigured, d.Squawks)
	}
	apiDo(t, h, "GET", "/app/admin/api/projects/acme", nil, &d)
	if len(d.Squawks) != 0 {
		t.Errorf("squawks without the section = %+v", d.Squawks)
	}
}
