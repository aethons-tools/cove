package storetest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// runChannelConformance exercises the ChannelStore contract (intercom slice
// 2a): the channel registry, bindings, membership, and rooms as the roster's
// channels.
func runChannelConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	setup := func(t *testing.T) (jam.Store, jam.Project, jam.Connection) {
		t.Helper()
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		p, _ := s.GetProject("acme")
		c, err := s.CreateConnection(jam.Connection{Kind: "linear", Name: "linear"})
		if err != nil {
			t.Fatal(err)
		}
		return s, p, c
	}
	room := func(p jam.Project, key string, bs ...jam.Binding) jam.Channel {
		return jam.Channel{ProjectID: p.ID, Kind: jam.SourceRoom, Key: key, Label: key, Bindings: bs}
	}
	mustChannel := func(t *testing.T, s jam.Store, c jam.Channel) jam.Channel {
		t.Helper()
		got, err := s.CreateChannel(c)
		if err != nil {
			t.Fatalf("CreateChannel %s: %v", c.Key, err)
		}
		return got
	}

	t.Run("channel_create_and_lookups", func(t *testing.T) {
		s, p, c := setup(t)
		ch := mustChannel(t, s, room(p, "eng", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
		if ch.ID.Kind() != ident.Channel || ch.Status != jam.StatusLive {
			t.Fatalf("created = %+v", ch)
		}
		if got, ok := s.GetChannel(ch.ID); !ok || !reflect.DeepEqual(got, ch) {
			t.Fatalf("GetChannel = %+v, %v; want %+v", got, ok, ch)
		}
		if got, ok := s.ChannelByKey(p.ID, jam.SourceRoom, "eng"); !ok || got.ID != ch.ID {
			t.Fatalf("ChannelByKey = %+v, %v", got, ok)
		}
		if got, ok := s.ChannelByBinding(c.ID, "ACME-1"); !ok || got.ID != ch.ID {
			t.Fatalf("ChannelByBinding = %+v, %v", got, ok)
		}
		if got := s.ListChannels(p.ID, jam.SourceRoom); len(got) != 1 || got[0].ID != ch.ID {
			t.Fatalf("ListChannels = %+v", got)
		}
		if got := s.ListChannels(p.ID, jam.SourceChat); len(got) != 0 {
			t.Fatalf("ListChannels(chat) = %+v", got)
		}
		if e, ok := s.Resolve(ch.ID); !ok || e.Kind != ident.Channel || e.Label() != "eng" {
			t.Fatalf("Resolve = %+v, %v", e, ok)
		}
		// A returned channel is a copy.
		ch.Bindings[0].Ref = "mutated"
		if got, _ := s.GetChannel(ch.ID); got.Bindings[0].Ref != "ACME-1" {
			t.Fatalf("GetChannel must return copies: %+v", got)
		}
	})

	t.Run("channel_validation", func(t *testing.T) {
		s, p, c := setup(t)
		bad := []jam.Channel{
			{ProjectID: "prj_01j9q3zzzzzzzzzzzzzzzzzzzz", Kind: jam.SourceRoom, Key: "x", Label: "x"},
			{ProjectID: p.ID, Kind: "carrier-pigeon", Key: "x", Label: "x"},
			{ProjectID: p.ID, Kind: jam.SourceRoom, Key: "", Label: "x"},
			room(p, "x", jam.Binding{ConnectionID: "con_01j9q3zzzzzzzzzzzzzzzzzzzz", Ref: "R", Mode: jam.BindBoth}),
			room(p, "x", jam.Binding{ConnectionID: c.ID, Ref: "", Mode: jam.BindBoth}),
			room(p, "x", jam.Binding{ConnectionID: c.ID, Ref: "R", Mode: "sideways"}),
			// A surface is bound once per channel, whatever the modes.
			room(p, "x", jam.Binding{ConnectionID: c.ID, Ref: "R", Mode: jam.BindEgress}, jam.Binding{ConnectionID: c.ID, Ref: "R", Mode: jam.BindEgress}),
			room(p, "x", jam.Binding{ConnectionID: c.ID, Ref: "R", Mode: jam.BindBoth}, jam.Binding{ConnectionID: c.ID, Ref: "R", Mode: jam.BindEgress}),
		}
		for _, b := range bad {
			if _, err := s.CreateChannel(b); err == nil {
				t.Errorf("CreateChannel(%+v) must fail", b)
			}
		}
	})

	t.Run("channel_key_unique_and_archive_frees_it", func(t *testing.T) {
		s, p, c := setup(t)
		ch := mustChannel(t, s, room(p, "eng", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
		if _, err := s.CreateChannel(room(p, "eng")); !errors.Is(err, jam.ErrChannelExists) {
			t.Fatalf("duplicate key: %v, want ErrChannelExists", err)
		}
		if err := s.ArchiveChannel(ch.ID); err != nil {
			t.Fatalf("ArchiveChannel: %v", err)
		}
		if got, _ := s.GetChannel(ch.ID); got.Status != jam.StatusArchived {
			t.Fatalf("archived = %+v", got)
		}
		if e, _ := s.Resolve(ch.ID); e.Label() != "eng (archived)" {
			t.Fatalf("Resolve archived = %q", e.Label())
		}
		if _, ok := s.ChannelByKey(p.ID, jam.SourceRoom, "eng"); ok {
			t.Fatal("an archived channel must not be found by key")
		}
		if _, ok := s.ChannelByBinding(c.ID, "ACME-1"); ok {
			t.Fatal("an archived channel must not be found by binding")
		}
		if len(s.ListChannels(p.ID, jam.SourceRoom)) != 0 {
			t.Fatal("ListChannels lists live channels only")
		}
		again := mustChannel(t, s, room(p, "eng", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
		if again.ID == ch.ID {
			t.Fatal("a channel re-created after archiving is a new channel")
		}
		if err := s.ArchiveChannel(ch.ID); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("archive twice: %v, want ErrRemoved", err)
		}
		if err := s.ArchiveChannel("chn_01j9q3zzzzzzzzzzzzzzzzzzzz"); !errors.Is(err, jam.ErrChannelNotFound) {
			t.Fatalf("archive unknown: %v, want ErrChannelNotFound", err)
		}
	})

	t.Run("binding_taken", func(t *testing.T) {
		s, p, c := setup(t)
		mustChannel(t, s, room(p, "eng", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
		if _, err := s.CreateChannel(room(p, "ops", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth})); !errors.Is(err, jam.ErrBindingTaken) {
			t.Fatalf("second ingress binding: %v, want ErrBindingTaken", err)
		}
		// An egress-only binding never resolves ingress, so it may share a ref.
		mustChannel(t, s, room(p, "ops", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindEgress}))
		if got, _ := s.ChannelByBinding(c.ID, "ACME-1"); got.Key != "eng" {
			t.Fatalf("ChannelByBinding = %+v, want eng", got)
		}
	})

	t.Run("channel_rename_and_rebind", func(t *testing.T) {
		s, p, c := setup(t)
		ch := mustChannel(t, s, room(p, "eng", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
		other := mustChannel(t, s, room(p, "ops"))
		if err := s.RenameChannel(ch.ID, "ops", "ops"); !errors.Is(err, jam.ErrChannelExists) {
			t.Fatalf("rename onto a live key: %v, want ErrChannelExists", err)
		}
		if err := s.RenameChannel(ch.ID, "help", "help"); err != nil {
			t.Fatalf("RenameChannel: %v", err)
		}
		if got, ok := s.ChannelByKey(p.ID, jam.SourceRoom, "help"); !ok || got.ID != ch.ID || got.Label != "help" {
			t.Fatalf("renamed = %+v, %v", got, ok)
		}
		if err := s.SetChannelBindings(other.ID, []jam.Binding{{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}}); !errors.Is(err, jam.ErrBindingTaken) {
			t.Fatalf("rebind onto a taken binding: %v, want ErrBindingTaken", err)
		}
		if err := s.SetChannelBindings(ch.ID, []jam.Binding{{ConnectionID: c.ID, Ref: "ACME-2", Mode: jam.BindBoth}}); err != nil {
			t.Fatalf("SetChannelBindings: %v", err)
		}
		if _, ok := s.ChannelByBinding(c.ID, "ACME-1"); ok {
			t.Fatal("the old binding must be released")
		}
		if got, _ := s.ChannelByBinding(c.ID, "ACME-2"); got.ID != ch.ID {
			t.Fatalf("new binding = %+v", got)
		}
	})

	t.Run("channel_membership", func(t *testing.T) {
		s, p, _ := setup(t)
		ch := mustChannel(t, s, room(p, "eng"))
		u, _ := s.CreateUser(jam.User{Name: "alice"})
		legacy := ident.ID("standing-acme-impl-spider") // a grandfathered session id
		if err := s.JoinChannel(ch.ID, u.ID, 5); err != nil {
			t.Fatalf("JoinChannel: %v", err)
		}
		if err := s.JoinChannel(ch.ID, u.ID, 7); err != nil {
			t.Fatalf("JoinChannel again: %v", err)
		}
		if err := s.JoinChannel(ch.ID, legacy, 6); err != nil {
			t.Fatalf("JoinChannel legacy session: %v", err)
		}
		want := []jam.ChannelMember{{ParticipantID: legacy, JoinedSeq: 6}, {ParticipantID: u.ID, JoinedSeq: 5}}
		if got := s.ChannelMembers(ch.ID); !reflect.DeepEqual(got, want) {
			t.Fatalf("members = %+v, want %+v (joining twice is a no-op)", got, want)
		}
		if got := s.ChannelsOf(u.ID); len(got) != 1 || got[0] != ch.ID {
			t.Fatalf("ChannelsOf = %+v", got)
		}
		if err := s.LeaveChannel(ch.ID, u.ID, 9); err != nil {
			t.Fatalf("LeaveChannel: %v", err)
		}
		if err := s.LeaveChannel(ch.ID, u.ID, 10); err != nil {
			t.Fatalf("LeaveChannel when not a member is a no-op: %v", err)
		}
		if got := s.ChannelMembers(ch.ID); len(got) != 1 || got[0].ParticipantID != legacy {
			t.Fatalf("members after leave = %+v", got)
		}
		if len(s.ChannelsOf(u.ID)) != 0 {
			t.Fatal("ChannelsOf lists current memberships only")
		}
		if err := s.JoinChannel(ch.ID, u.ID, 12); err != nil {
			t.Fatalf("rejoin: %v", err)
		}
		if got := s.ChannelMembers(ch.ID); len(got) != 2 || got[1] != (jam.ChannelMember{ParticipantID: u.ID, JoinedSeq: 12}) {
			t.Fatalf("members after rejoin = %+v", got)
		}
		// Joining and leaving at the start of the log (seq 0) ends the membership.
		if err := s.JoinChannel(ch.ID, "usr_01j9q3zzzzzzzzzzzzzzzzzzzz", 0); err != nil {
			t.Fatal(err)
		}
		if err := s.LeaveChannel(ch.ID, "usr_01j9q3zzzzzzzzzzzzzzzzzzzz", 0); err != nil {
			t.Fatal(err)
		}
		if got := s.ChannelMembers(ch.ID); len(got) != 2 {
			t.Fatalf("members after a seq-0 join and leave = %+v", got)
		}
		if err := s.ArchiveChannel(ch.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.JoinChannel(ch.ID, u.ID, 20); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("join archived: %v, want ErrRemoved", err)
		}
		if err := s.LeaveChannel(ch.ID, u.ID, 20); err != nil {
			t.Fatalf("leaving an archived channel is allowed: %v", err)
		}
	})

	t.Run("project_removal_removes_its_channels", func(t *testing.T) {
		s, p, c := setup(t)
		ch := mustChannel(t, s, room(p, "eng", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
		if err := s.RemoveProject("acme"); err != nil {
			t.Fatalf("RemoveProject: %v", err)
		}
		if _, ok := s.GetChannel(ch.ID); ok {
			t.Fatal("a removed project's channels go with it")
		}
		if _, ok := s.ChannelByBinding(c.ID, "ACME-1"); ok {
			t.Fatal("their bindings too")
		}
	})

	t.Run("connection_in_use_by_a_binding", func(t *testing.T) {
		s, p, c := setup(t)
		ch := mustChannel(t, s, room(p, "eng", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
		if err := s.RemoveConnection(c.ID); !errors.Is(err, jam.ErrConnectionInUse) {
			t.Fatalf("remove bound connection: %v, want ErrConnectionInUse", err)
		}
		if err := s.ArchiveChannel(ch.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveConnection(c.ID); err != nil {
			t.Fatalf("remove after archive: %v", err)
		}
	})

	t.Run("roster_channels_are_rooms", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		if err := s.AddChannel("acme", jam.RosterChannel{Name: "eng", Ref: "ACME-1"}); err != nil {
			t.Fatalf("AddChannel: %v", err)
		}
		if err := s.AddChannel("acme", jam.RosterChannel{Name: "chat", Service: "discord", Ref: "123"}); err != nil {
			t.Fatalf("AddChannel discord: %v", err)
		}
		want := []jam.RosterChannel{{Name: "chat", Service: "discord", Ref: "123"}, {Name: "eng", Service: "linear", Ref: "ACME-1"}}
		if r, _ := s.GetRoster("acme"); !reflect.DeepEqual(r.Channels, want) {
			t.Fatalf("roster channels = %+v, want %+v", r.Channels, want)
		}
		if p, _ := s.GetProject("acme"); !reflect.DeepEqual(p.Roster.Channels, want) {
			t.Fatalf("project roster channels = %+v", p.Roster.Channels)
		}
		p, _ := s.GetProject("acme")
		rooms := s.ListChannels(p.ID, jam.SourceRoom)
		if len(rooms) != 2 || rooms[1].Key != "eng" || len(rooms[1].Bindings) != 1 || rooms[1].Bindings[0].Mode != jam.BindBoth {
			t.Fatalf("rooms = %+v", rooms)
		}
		lin, ok := s.ConnectionOfKind("linear")
		if !ok || rooms[1].Bindings[0].ConnectionID != lin.ID {
			t.Fatalf("the room binds the linear connection (created on demand): %+v, %+v", lin, rooms[1].Bindings)
		}
		// An upsert by name rebinds the same room.
		if err := s.AddChannel("acme", jam.RosterChannel{Name: "eng", Service: "linear", Ref: "ACME-2"}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.ChannelByKey(p.ID, jam.SourceRoom, "eng"); got.ID != rooms[1].ID || got.Bindings[0].Ref != "ACME-2" {
			t.Fatalf("upserted room = %+v", got)
		}
		if err := s.AddChannel("acme", jam.RosterChannel{Name: "x", Service: "carrier-pigeon", Ref: "1"}); err == nil {
			t.Fatal("a service with no connection kind must be refused")
		}
		if err := s.AddChannel("acme", jam.RosterChannel{Name: "x", Service: "linear"}); err == nil {
			t.Fatal("a channel needs a ref")
		}
		if err := s.RemoveChannel("acme", "eng"); err != nil {
			t.Fatalf("RemoveChannel: %v", err)
		}
		if r, _ := s.GetRoster("acme"); len(r.Channels) != 1 || r.Channels[0].Name != "chat" {
			t.Fatalf("roster after remove = %+v", r.Channels)
		}
		if got, _ := s.GetChannel(rooms[1].ID); got.Status != jam.StatusArchived {
			t.Fatalf("a removed roster channel's room is archived: %+v", got)
		}
	})

	t.Run("roster_view_and_demoted_rooms", func(t *testing.T) {
		s, p, c := setup(t)
		// As the migration leaves two channels that shared a ref: the first
		// keeps ingress, the other only posts there.
		mustChannel(t, s, room(p, "zeta", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
		alpha := mustChannel(t, s, room(p, "alpha", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindEgress}))
		r, _ := s.GetRoster("acme")
		if len(r.Channels) != 2 || r.Channels[0].Name != "zeta" {
			t.Fatalf("roster = %+v: the channel that receives a ref's replies must come first", r.Channels)
		}
		// Re-saving the demoted channel unchanged keeps it as it is.
		if err := s.AddChannel("acme", jam.RosterChannel{Name: "alpha", Service: "linear", Ref: "ACME-1"}); err != nil {
			t.Fatalf("re-save demoted: %v", err)
		}
		if got, _ := s.GetChannel(alpha.ID); got.Bindings[0].Mode != jam.BindEgress {
			t.Fatalf("re-saved = %+v", got)
		}
	})

	t.Run("export_import_round_trips_channels", func(t *testing.T) {
		s, p, c := setup(t)
		live := mustChannel(t, s, room(p, "eng", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
		gone := mustChannel(t, s, room(p, "old"))
		if err := s.ArchiveChannel(gone.ID); err != nil {
			t.Fatal(err)
		}
		u, _ := s.CreateUser(jam.User{Name: "alice"})
		if err := s.JoinChannel(live.ID, u.ID, 3); err != nil {
			t.Fatal(err)
		}
		snap := s.ExportConfig()
		if snap.Version != jam.ConfigSnapshotVersion || len(snap.Channels) != 2 {
			t.Fatalf("export = version %d, channels %+v", snap.Version, snap.Channels)
		}
		for _, pr := range snap.Projects {
			if len(pr.Roster.Channels) != 0 {
				t.Fatalf("rooms travel as channels, not roster channels: %+v", pr.Roster.Channels)
			}
		}
		s2 := newStore(t)
		if err := s2.ImportConfig(snap); err != nil {
			t.Fatalf("import: %v", err)
		}
		if got, _ := s2.GetChannel(live.ID); !reflect.DeepEqual(got, live) {
			t.Fatalf("imported = %+v, want %+v", got, live)
		}
		if got, _ := s2.GetChannel(gone.ID); got.Status != jam.StatusArchived {
			t.Fatalf("imported archived = %+v", got)
		}
		if len(s2.ChannelMembers(live.ID)) != 0 {
			t.Fatal("membership is runtime state and is not exported")
		}
		if r, _ := s2.GetRoster("acme"); len(r.Channels) != 1 || r.Channels[0] != (jam.RosterChannel{Name: "eng", Service: "linear", Ref: "ACME-1"}) {
			t.Fatalf("imported roster channels = %+v", r.Channels)
		}
	})

	t.Run("import_v2_roster_channels_become_rooms", func(t *testing.T) {
		s := newStore(t)
		v2 := jam.ConfigSnapshot{Version: 2, Projects: []jam.Project{{Name: "acme", Roster: jam.Roster{Channels: []jam.RosterChannel{
			{Name: "eng", Service: "linear", Ref: "ACME-1"},
		}}}}}
		if err := s.ImportConfig(v2); err != nil {
			t.Fatalf("import: %v", err)
		}
		p, _ := s.GetProject("acme")
		rooms := s.ListChannels(p.ID, jam.SourceRoom)
		if len(rooms) != 1 || rooms[0].Key != "eng" || rooms[0].Bindings[0].Ref != "ACME-1" {
			t.Fatalf("rooms = %+v", rooms)
		}
		if r, _ := s.GetRoster("acme"); len(r.Channels) != 1 || r.Channels[0].Name != "eng" {
			t.Fatalf("roster channels = %+v", r.Channels)
		}
		if snap := s.ExportConfig(); len(snap.Projects[0].Roster.Channels) != 0 {
			t.Fatalf("the stored doc no longer holds roster channels: %+v", snap.Projects[0].Roster.Channels)
		}
	})

	t.Run("import_rejects_inconsistent_channels", func(t *testing.T) {
		base := func() jam.ConfigSnapshot {
			s, p, c := setup(t)
			mustChannel(t, s, room(p, "eng", jam.Binding{ConnectionID: c.ID, Ref: "ACME-1", Mode: jam.BindBoth}))
			return s.ExportConfig()
		}
		cases := map[string]func(*jam.ConfigSnapshot){
			"bad id":             func(s *jam.ConfigSnapshot) { s.Channels[0].ID = "usr_01j9q3zzzzzzzzzzzzzzzzzzzz" },
			"unknown project":    func(s *jam.ConfigSnapshot) { s.Channels[0].ProjectID = "prj_01j9q3zzzzzzzzzzzzzzzzzzzz" },
			"unknown connection": func(s *jam.ConfigSnapshot) { s.Channels[0].Bindings[0].ConnectionID = "con_01j9q3zzzzzzzzzzzzzzzzzzzz" },
			"bad status":         func(s *jam.ConfigSnapshot) { s.Channels[0].Status = jam.StatusRemoved },
			"duplicate key": func(s *jam.ConfigSnapshot) {
				dup := s.Channels[0]
				dup.ID, dup.Bindings = ident.New(ident.Channel), nil
				s.Channels = append(s.Channels, dup)
			},
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				snap := base()
				mutate(&snap)
				if err := newStore(t).ImportConfig(snap); !errors.Is(err, jam.ErrInvalidConfig) {
					t.Fatalf("import: %v, want ErrInvalidConfig", err)
				}
			})
		}
	})
}
