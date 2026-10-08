package jam

import (
	"errors"
	"net/http"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

func roomsFixture(t *testing.T) (*MemStore, Project, Connection) {
	t.Helper()
	s := NewMemStore()
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetProject("acme")
	c, err := s.CreateConnection(Connection{Kind: "linear", Name: "linear-acme"})
	if err != nil {
		t.Fatal(err)
	}
	return s, p, c
}

func TestPutRoom(t *testing.T) {
	s, p, c := roomsFixture(t)
	v, created, err := PutRoom(s, "acme", RoomBody{Name: "eng", Ref: "ACME-1"})
	if err != nil || !created {
		t.Fatalf("PutRoom = %+v, %v, %v", v, created, err)
	}
	if v.ID.Kind() != ident.Channel || v.Name != "eng" || v.Connection != "linear-acme" || v.Kind != "linear" || v.Ref != "ACME-1" || v.PostOnly {
		t.Fatalf("view = %+v (the default connection is the linear one)", v)
	}
	// By connection name or id, and an upsert by name rebinds the same room.
	d, err := s.CreateConnection(Connection{Kind: "discord", Name: "discord-main"})
	if err != nil {
		t.Fatal(err)
	}
	again, created, err := PutRoom(s, "acme", RoomBody{Name: "eng", Connection: "discord-main", Ref: "42"})
	if err != nil || created || again.ID != v.ID || again.ConnectionID != d.ID {
		t.Fatalf("upsert = %+v, %v, %v", again, created, err)
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "ops", Connection: string(c.ID), Ref: "ACME-1"}); err != nil {
		t.Fatalf("by connection id: %v", err)
	}
	if got := ListRooms(s, p); len(got) != 2 || got[0].Name != "eng" || got[1].Name != "ops" {
		t.Fatalf("ListRooms = %+v", got)
	}
	for name, b := range map[string]RoomBody{
		"no ref":          {Name: "x"},
		"bad name":        {Name: "a b", Ref: "R"},
		"no such conn":    {Name: "x", Connection: "nope", Ref: "R"},
		"taken binding":   {Name: "x", Connection: "linear-acme", Ref: "ACME-1"},
		"unknown project": {Name: "x", Ref: "R"},
	} {
		project := "acme"
		if name == "unknown project" {
			project = "nope"
		}
		if _, _, err := PutRoom(s, project, b); err == nil {
			t.Errorf("%s: PutRoom must fail", name)
		}
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "x", Connection: "linear-acme", Ref: "ACME-1"}); !errors.Is(err, ErrBindingTaken) {
		t.Fatalf("taken binding: %v, want ErrBindingTaken", err)
	}
}

func TestRenameAndRemoveRoom(t *testing.T) {
	s, p, _ := roomsFixture(t)
	v, _, err := PutRoom(s, "acme", RoomBody{Name: "eng", Ref: "ACME-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "ops", Ref: "ACME-2"}); err != nil {
		t.Fatal(err)
	}
	if err := RenameRoom(s, "acme", "eng", "ops"); !errors.Is(err, ErrChannelExists) {
		t.Fatalf("rename onto a live room: %v", err)
	}
	if err := RenameRoom(s, "acme", "eng", "bad name"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("rename to a bad name: %v", err)
	}
	if err := RenameRoom(s, "acme", string(v.ID), "help"); err != nil {
		t.Fatalf("rename by id: %v", err)
	}
	if got := ListRooms(s, p); got[0].Name != "help" {
		t.Fatalf("rooms = %+v", got)
	}
	if err := RemoveRoom(s, "acme", "help"); err != nil {
		t.Fatalf("RemoveRoom: %v", err)
	}
	if got := ListRooms(s, p); len(got) != 1 {
		t.Fatalf("rooms after remove = %+v", got)
	}
	if err := RemoveRoom(s, "acme", "help"); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	// Another project's room (or a chat) is not this project's room.
	if err := s.CreateProject("beta"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRoom(s, "beta", "ops"); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("another project's room: %v", err)
	}
}

func TestAdminRoomRoutes(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if _, err := store.CreateConnection(Connection{Kind: "linear", Name: "linear"}); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, "POST", "/admin/projects/acme/rooms", RoomBody{Name: "eng", Ref: "ACME-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST room = %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/rooms", RoomBody{Name: "eng", Ref: "ACME-2"}); rec.Code != http.StatusOK {
		t.Fatalf("POST room again (upsert) = %d", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/rooms", RoomBody{Name: "ops", Ref: "ACME-2"}); rec.Code != http.StatusConflict {
		t.Fatalf("POST room on a taken ref = %d, want 409", rec.Code)
	}
	var rooms []RoomView
	getJSON(t, h, "/admin/projects/acme/rooms", &rooms)
	if len(rooms) != 1 || rooms[0].Ref != "ACME-2" {
		t.Fatalf("rooms = %+v", rooms)
	}
	if rec := doJSON(t, h, "PUT", "/admin/projects/acme/rooms/eng/name", RenameBody{Name: "help"}); rec.Code != http.StatusNoContent {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, h, "DELETE", "/admin/projects/acme/rooms/help", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	if rec := doReq(t, h, "DELETE", "/admin/projects/acme/rooms/help", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE again = %d, want 404", rec.Code)
	}
	if rec := doReq(t, h, "GET", "/admin/projects/nope/rooms", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown project = %d", rec.Code)
	}
}

// A kind names its connection of that kind, never another kind's connection
// that happens to carry the kind's name; with none, one is created (as the
// roster's channels always did).
func TestRoomConnectionByKind(t *testing.T) {
	s := NewMemStore()
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateConnection(Connection{Kind: "discord", Name: "linear"}); err != nil {
		t.Fatal(err)
	}
	v, _, err := PutRoom(s, "acme", RoomBody{Name: "eng", Ref: "ACME-1"})
	if err != nil || v.Kind != "linear" {
		t.Fatalf("default = %+v, %v; want a linear connection", v, err)
	}
	w, _, err := PutRoom(s, "acme", RoomBody{Name: "chat", Connection: " Discord ", Ref: "42"})
	if err != nil || w.Kind != "discord" {
		t.Fatalf("by kind = %+v, %v", w, err)
	}
}

// A post-only room is promoted once no other channel holds its ref.
func TestPutRoomPromotesPostOnly(t *testing.T) {
	s, p, c := roomsFixture(t)
	a, err := s.CreateChannel(Channel{ProjectID: p.ID, Kind: SourceRoom, Key: "a", Label: "a", Bindings: []Binding{{ConnectionID: c.ID, Ref: "ACME-1", Mode: BindBoth}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChannel(Channel{ProjectID: p.ID, Kind: SourceRoom, Key: "b", Label: "b", Bindings: []Binding{{ConnectionID: c.ID, Ref: "ACME-1", Mode: BindEgress}}}); err != nil {
		t.Fatal(err)
	}
	if v, _, err := PutRoom(s, "acme", RoomBody{Name: "b", Ref: "ACME-1"}); err != nil || !v.PostOnly {
		t.Fatalf("while a holds the ref: %+v, %v", v, err)
	}
	if err := s.ArchiveChannel(a.ID); err != nil {
		t.Fatal(err)
	}
	if v, _, err := PutRoom(s, "acme", RoomBody{Name: "b", Ref: "ACME-1"}); err != nil || v.PostOnly {
		t.Fatalf("after a is gone: %+v, %v", v, err)
	}
}
