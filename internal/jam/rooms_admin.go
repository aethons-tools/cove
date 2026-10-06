package jam

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// Rooms in the admin plane (intercom slice 2a-3): `at-jam room` and
// /admin/projects/{project}/rooms, replacing the roster-channel verbs. A
// room's ref binds it on a connection (by name or id; default: the linear
// one) — the surface it posts to and receives replies from.

// RoomBody adds or updates a room: its name, the connection its ref is on (a
// name, an id or a kind; "" = the linear connection), and the ref.
type RoomBody struct {
	Name       string `json:"name"`
	Connection string `json:"connection,omitempty"`
	Ref        string `json:"ref"`
}

// RoomView is one room for the admin plane. PostOnly marks a room that only
// posts to its ref: another channel receives that ref's replies.
type RoomView struct {
	ID           ident.ID `json:"id"`
	Name         string   `json:"name"`
	Connection   string   `json:"connection,omitempty"`
	ConnectionID ident.ID `json:"connection_id,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	Ref          string   `json:"ref,omitempty"`
	PostOnly     bool     `json:"post_only,omitempty"`
}

// NewRoomView renders room ch.
func NewRoomView(store Store, ch Channel) RoomView {
	v := RoomView{ID: ch.ID, Name: ch.Key}
	if len(ch.Bindings) > 0 {
		b := ch.Bindings[0]
		v.ConnectionID, v.Ref, v.PostOnly = b.ConnectionID, b.Ref, b.Mode != BindBoth
		if c, ok := store.GetConnection(b.ConnectionID); ok {
			v.Connection, v.Kind = c.Name, c.Kind
		}
	}
	return v
}

// ListRooms returns project's live rooms, sorted by name.
func ListRooms(store Store, p Project) []RoomView {
	out := []RoomView{}
	for _, ch := range store.ListChannels(p.ID, SourceRoom) {
		out = append(out, NewRoomView(store, ch))
	}
	return out
}

// PutRoom creates project's room b.Name, or rebinds it (created=false).
// Re-binding a post-only room to its current ref keeps it post-only.
func PutRoom(store Store, project string, b RoomBody) (RoomView, bool, error) {
	p, ok := store.GetProject(project)
	if !ok {
		return RoomView{}, false, fmt.Errorf("%w: %q", ErrProjectNotFound, project)
	}
	if b.Ref == "" {
		return RoomView{}, false, fmt.Errorf("room %q: ref required", b.Name)
	}
	conn, err := roomConnection(store, b.Connection)
	if err != nil {
		return RoomView{}, false, err
	}
	bind := Binding{ConnectionID: conn.ID, Ref: b.Ref, Mode: BindBoth}
	if ch, ok := store.ChannelByKey(p.ID, SourceRoom, b.Name); ok {
		if slices.Contains(ch.Bindings, Binding{ConnectionID: conn.ID, Ref: b.Ref, Mode: BindEgress}) {
			bind.Mode = BindEgress
		}
		if err := store.SetChannelBindings(ch.ID, []Binding{bind}); err != nil {
			return RoomView{}, false, err
		}
		ch, _ = store.GetChannel(ch.ID)
		return NewRoomView(store, ch), false, nil
	}
	if err := ValidateEntityName(b.Name); err != nil {
		return RoomView{}, false, err
	}
	ch, err := store.CreateChannel(Channel{ProjectID: p.ID, Kind: SourceRoom, Key: b.Name, Label: b.Name, Bindings: []Binding{bind}})
	if err != nil {
		return RoomView{}, false, err
	}
	return NewRoomView(store, ch), true, nil
}

// roomConnection resolves a room's connection: a connection name or id, a
// kind (its connection of that kind), or "" (the linear connection).
func roomConnection(store Store, ref string) (Connection, error) {
	if ref == "" {
		ref = "linear"
	}
	if id, err := ResolveRegistryRef(store, ident.Connection, ref); err == nil {
		c, _ := store.GetConnection(id)
		return c, nil
	}
	if slices.Contains(ConnectionKinds, ref) {
		if c, ok := store.ConnectionOfKind(ref); ok {
			return c, nil
		}
	}
	return Connection{}, fmt.Errorf("%w: %q", ErrConnectionNotFound, ref)
}

// resolveRoom finds project's live room ref names (a name or a channel id).
func resolveRoom(store Store, project, ref string) (Channel, error) {
	p, ok := store.GetProject(project)
	if !ok {
		return Channel{}, fmt.Errorf("%w: %q", ErrProjectNotFound, project)
	}
	if id, err := ident.Parse(ref); err == nil && id.Kind() == ident.Channel {
		if ch, ok := store.GetChannel(id); ok && ch.ProjectID == p.ID && ch.Kind == SourceRoom && ch.Status == StatusLive {
			return ch, nil
		}
	} else if ch, ok := store.ChannelByKey(p.ID, SourceRoom, ref); ok {
		return ch, nil
	}
	return Channel{}, fmt.Errorf("%w: room %q in %q", ErrChannelNotFound, ref, project)
}

// RenameRoom renames project's room ref (a name or an id). Nothing refers to
// a room by name but addressing globs (channel:<name>), which follow names.
func RenameRoom(store Store, project, ref, name string) error {
	ch, err := resolveRoom(store, project, ref)
	if err != nil {
		return err
	}
	if err := ValidateEntityName(name); err != nil {
		return err
	}
	return store.RenameChannel(ch.ID, name, name)
}

// RemoveRoom archives project's room ref (a name or an id).
func RemoveRoom(store Store, project, ref string) error {
	ch, err := resolveRoom(store, project, ref)
	if err != nil {
		return err
	}
	return store.ArchiveChannel(ch.ID)
}

func registerRooms(mux *http.ServeMux, store Store, log *slog.Logger) {
	fail := func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), RegistryErrStatus(err)) }
	mux.HandleFunc("GET /admin/projects/{project}/rooms", func(w http.ResponseWriter, r *http.Request) {
		p, ok := store.GetProject(r.PathValue("project"))
		if !ok {
			fail(w, fmt.Errorf("%w: %q", ErrProjectNotFound, r.PathValue("project")))
			return
		}
		writeJSON(w, http.StatusOK, ListRooms(store, p))
	})
	mux.HandleFunc("POST /admin/projects/{project}/rooms", func(w http.ResponseWriter, r *http.Request) {
		var b RoomBody
		if !decode(w, r, &b) {
			return
		}
		v, created, err := PutRoom(store, r.PathValue("project"), b)
		if err != nil {
			fail(w, err)
			return
		}
		log.Info("admin room put", "operator", OperatorID(r), "project", r.PathValue("project"), "room", v.ID, "name", v.Name, "created", created)
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, v)
	})
	mux.HandleFunc("PUT /admin/projects/{project}/rooms/{room}/name", func(w http.ResponseWriter, r *http.Request) {
		var b RenameBody
		if !decode(w, r, &b) {
			return
		}
		if err := RenameRoom(store, r.PathValue("project"), r.PathValue("room"), b.Name); err != nil {
			fail(w, err)
			return
		}
		log.Info("admin room renamed", "operator", OperatorID(r), "project", r.PathValue("project"), "room", r.PathValue("room"), "name", b.Name)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/projects/{project}/rooms/{room}", func(w http.ResponseWriter, r *http.Request) {
		if err := RemoveRoom(store, r.PathValue("project"), r.PathValue("room")); err != nil {
			fail(w, err)
			return
		}
		log.Info("admin room removed", "operator", OperatorID(r), "project", r.PathValue("project"), "room", r.PathValue("room"))
		w.WriteHeader(http.StatusNoContent)
	})
}
