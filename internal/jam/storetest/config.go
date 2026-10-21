package storetest

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// ConfigWriteFixture is a write touching every config kind: the write, and the
// ids of its user, connection and project. Exported for the Postgres
// atomicity test.
func ConfigWriteFixture() (jam.ConfigWrite, ident.ID, ident.ID, ident.ID) {
	uid, cid, pid := ident.New(ident.User), ident.New(ident.Connection), ident.New(ident.Project)
	return jam.ConfigWrite{
		Connections:  []jam.Connection{{ID: cid, Kind: "linear", Name: "acme", CredName: "linear", Status: jam.StatusLive}},
		Users:        []jam.User{{ID: uid, Name: "bob", Status: jam.StatusLive, Logins: []string{"bob@acme"}}},
		Destinations: []jam.Destination{{Name: "gh", Route: "/gh/", Upstream: "https://api.github.com", CredName: "gh"}},
		ModelSpecs:   []jam.ModelSpec{{Name: "fast", Type: jam.HarnessClaude, Version: "2.1.3"}},
		Kits:         []jam.Kit{{Name: "web", Current: 1, Versions: map[int]string{1: `{"kind":"studio"}`}}},
		Projects:     []jam.Project{{ID: pid, Name: "app", ChatService: string(cid)}},
		Memberships:  []jam.Membership{{ProjectID: pid, UserID: uid}},
		Channels: []jam.Channel{{ID: ident.New(ident.Channel), ProjectID: pid, Kind: jam.SourceRoom, Key: "ops", Label: "ops",
			Status: jam.StatusLive, Bindings: []jam.Binding{{ConnectionID: cid, Ref: "ACME-1", Mode: jam.BindBoth}}}},
		Roles:      []jam.ConfigRoleWrite{{ProjectID: pid, Role: jam.Role{Name: "ic", Kit: "web", ModelSpec: "fast"}}},
		JamContext: &sessionctx.Layer{Core: "be kind"},
		Marks: []jam.ConfigMark{{Kind: jam.ConfigKindRole, Name: "app/ic", Commit: "dir", Digest: "d1", AppliedAt: time.Unix(100, 0).UTC()},
			{Kind: jam.ConfigKindDestination, Name: "gh", Commit: "dir", Digest: "d2", AppliedAt: time.Unix(100, 0).UTC()}},
	}, uid, cid, pid
}

func runConfigConformance(t *testing.T, mk func(t *testing.T) jam.Store) {
	full := ConfigWriteFixture
	t.Run("apply config writes every kind and the marks", func(t *testing.T) {
		st := mk(t)
		w, uid, cid, pid := full()
		if err := st.ApplyConfig(w); err != nil {
			t.Fatal(err)
		}
		if u, ok := st.GetUser(uid); !ok || u.Name != "bob" || u.Logins[0] != "bob@acme" {
			t.Fatalf("user %+v", u)
		}
		if c, ok := st.GetConnection(cid); !ok || c.CredName != "linear" {
			t.Fatalf("connection %+v", c)
		}
		if _, ok := st.GetKit("web"); !ok {
			t.Fatal("kit")
		}
		if _, ok := st.GetModelSpec("fast"); !ok {
			t.Fatal("model-spec")
		}
		if p, ok := st.GetProject("app"); !ok || p.ID != pid {
			t.Fatalf("project %+v", p)
		}
		if _, ok := st.GetMembership(pid, uid); !ok {
			t.Fatal("membership")
		}
		if _, ok := st.ChannelByKey(pid, jam.SourceRoom, "ops"); !ok {
			t.Fatal("room")
		}
		if r, ok := st.GetRole("app", "ic"); !ok || r.Kit != "web" {
			t.Fatalf("role %+v", r)
		}
		if st.GetJamContext().Core != "be kind" {
			t.Fatal("jam context")
		}
		marks := st.ListConfigMarks()
		if len(marks) != 2 || marks[0].Kind != jam.ConfigKindDestination || marks[1].Name != "app/ic" {
			t.Fatalf("marks %+v", marks)
		}
	})
	t.Run("apply config removes and tombstones", func(t *testing.T) {
		st := mk(t)
		w, uid, cid, pid := full()
		if err := st.ApplyConfig(w); err != nil {
			t.Fatal(err)
		}
		ch, _ := st.ChannelByKey(pid, jam.SourceRoom, "ops")
		ch.Status = jam.StatusArchived
		del := jam.ConfigWrite{
			RemoveRoles:        []jam.ConfigRoleWrite{{ProjectID: pid, Role: jam.Role{Name: "ic"}}},
			RemoveMemberships:  []jam.Membership{{ProjectID: pid, UserID: uid}},
			Channels:           []jam.Channel{ch},
			Projects:           []jam.Project{{ID: pid, Name: "app", Status: jam.StatusRemoved}},
			RemoveKits:         []string{"web"},
			RemoveModelSpecs:   []string{"fast"},
			RemoveDestinations: []string{"gh"},
			Connections:        []jam.Connection{{ID: cid, Kind: "linear", Name: "acme", Status: jam.StatusRemoved}},
			Users:              []jam.User{{ID: uid, Name: "bob", Status: jam.StatusRemoved}},
			JamContext:         &sessionctx.Layer{},
			RemoveMarks:        []jam.ConfigKey{{Kind: jam.ConfigKindRole, Name: "app/ic"}},
		}
		if err := st.ApplyConfig(del); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.GetProject("app"); ok {
			t.Fatal("project still live")
		}
		if _, ok := st.GetKit("web"); ok {
			t.Fatal("kit kept")
		}
		if u, _ := st.GetUser(uid); u.Status != jam.StatusRemoved {
			t.Fatalf("user %+v", u)
		}
		if !st.GetJamContext().Empty() || len(st.ListConfigMarks()) != 1 {
			t.Fatalf("context %+v marks %+v", st.GetJamContext(), st.ListConfigMarks())
		}
	})
	t.Run("a bad record late in the set writes nothing", func(t *testing.T) {
		st := mk(t)
		w, _, _, _ := full()
		w.Roles = append(w.Roles, jam.ConfigRoleWrite{ProjectID: ident.New(ident.Project), Role: jam.Role{Name: "orphan"}})
		if err := st.ApplyConfig(w); err == nil {
			t.Fatal("a role in an unknown project must be refused")
		}
		if len(st.ListUsers())+len(st.ListKits())+len(st.ListDestinations())+len(st.ListConfigMarks()) != 0 {
			t.Fatal("partial write")
		}
		if _, ok := st.GetProject("app"); ok {
			t.Fatal("project written")
		}
	})
	t.Run("an empty write is a no-op", func(t *testing.T) {
		st := mk(t)
		if err := st.ApplyConfig(jam.ConfigWrite{}); err != nil {
			t.Fatal(err)
		}
	})
}
