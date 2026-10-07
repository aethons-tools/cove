package jam

import (
	"testing"
)

// peopleFixture is project acme chatting over discord with alice (bound to
// 111, inbox-A, @alice.h), bob (unbound, inbox-B), carol (bound to 333) and
// dave (unbound) sharing an inbox, and a room bound to team-ch.
func peopleFixture(t *testing.T) (*MemStore, Project) {
	t.Helper()
	s := NewMemStore()
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	disc := func(addr, uid string) []DeliveryProfile {
		return []DeliveryProfile{{Service: "discord", Address: addr, UserID: uid}}
	}
	for _, h := range []Human{
		{Name: "alice", Handle: "alice.h", Login: "auth0|a", Delivery: disc("inbox-A", "111")},
		{Name: "bob", Delivery: disc("inbox-B", "")},
		{Name: "carol", Delivery: disc("shared", "333")},
		{Name: "dave", Delivery: disc("shared", "")},
	} {
		if err := AddPerson(s, "acme", h); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "team", Connection: "discord", Ref: "team-ch"}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetProject("acme")
	return s, p
}

func TestMembers(t *testing.T) {
	s, p := peopleFixture(t)
	ms := MembersOf(s, p.ID)
	if len(ms) != 4 || ms[0].User.Name != "alice" || ms[0].Handle != "alice.h" || ms[0].DiscordUID != "111" || ms[1].DiscordUID != "" {
		t.Fatalf("members = %+v", ms)
	}
	if inbox, ok := ms[0].Inbox("discord"); !ok || inbox != "inbox-A" {
		t.Fatalf("alice's inbox = %q, %v", inbox, ok)
	}
	if m, ok := MemberByLogin(s, "acme", "auth0|a"); !ok || m.User.Name != "alice" {
		t.Fatalf("by login = %+v, %v", m, ok)
	}
	for _, login := range []string{"", "auth0|nobody"} {
		if _, ok := MemberByLogin(s, "acme", login); ok {
			t.Errorf("login %q must not match", login)
		}
	}
	other, _ := s.CreateUser(User{Name: "eve"})
	if _, ok := MemberOf(s, p.ID, other.ID); ok {
		t.Fatal("a non-member is no member")
	}
}

func TestDiscordAuthorOf(t *testing.T) {
	s, p := peopleFixture(t)
	for _, tc := range []struct {
		name, channel, authorID string
		want, by                string
	}{
		{"bound id in own inbox", "inbox-A", "111", "alice", "id"},
		{"bound id in a shared inbox", "shared", "333", "carol", "id"},
		{"bound id in another member's inbox", "inbox-B", "111", "alice", "id"},
		{"bound id in a room's channel", "team-ch", "333", "carol", "id"},
		{"unique inbox, unbound owner", "inbox-B", "999", "bob", "channel"},
		{"unique inbox, bound owner, other author", "inbox-A", "999", "", ""},
		{"unique inbox, bound owner, empty author id", "inbox-A", "", "", ""},
		{"empty author id, unbound owner (older events)", "inbox-B", "", "bob", "channel"},
		{"unbound author in a shared inbox", "shared", "999", "", ""},
		{"a room's channel is no inbox", "team-ch", "999", "", ""},
		{"unknown channel, unknown id", "nowhere", "999", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, by, ok := DiscordAuthorOf(s, p.ID, tc.channel, tc.authorID)
			if m.User.Name != tc.want || by != tc.by || ok != (tc.want != "") {
				t.Fatalf("= %q,%q,%v; want %q,%q", m.User.Name, by, ok, tc.want, tc.by)
			}
		})
	}
	// An id bound to someone who isn't a member here names nobody.
	if err := s.CreateProject("beta"); err != nil {
		t.Fatal(err)
	}
	beta, _ := s.GetProject("beta")
	if _, _, ok := DiscordAuthorOf(s, beta.ID, "inbox-A", "111"); ok {
		t.Fatal("alice is no member of beta")
	}
}

// A room's channel is never an inbox, whichever discord connection binds it
// and in whatever mode: an unbound member whose inbox is also a room's channel
// gets nobody's replies attributed to them by channel.
func TestDiscordAuthorOfRoomOnAnyConnectionOrMode(t *testing.T) {
	s, p := peopleFixture(t)
	other, err := s.CreateConnection(Connection{Kind: "discord", Name: "acme-bot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChannel(Channel{ProjectID: p.ID, Kind: SourceRoom, Key: "ops", Label: "ops",
		Bindings: []Binding{{ConnectionID: other.ID, Ref: "inbox-B", Mode: BindBoth}}}); err != nil {
		t.Fatal(err)
	}
	if m, _, ok := DiscordAuthorOf(s, p.ID, "inbox-B", "999"); ok {
		t.Fatalf("a room on another discord connection = %+v, want nobody", m)
	}
	disc, _ := s.ConnectionOfKind("discord")
	if err := AddPerson(s, "acme", Human{Name: "erin", Delivery: []DeliveryProfile{{Service: "discord", Address: "inbox-E"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChannel(Channel{ProjectID: p.ID, Kind: SourceRoom, Key: "quiet", Label: "quiet",
		Bindings: []Binding{{ConnectionID: disc.ID, Ref: "inbox-E", Mode: BindEgress}}}); err != nil {
		t.Fatal(err)
	}
	if m, _, ok := DiscordAuthorOf(s, p.ID, "inbox-E", "999"); ok {
		t.Fatalf("a post-only room's channel = %+v, want nobody", m)
	}
}

// An owner with a Discord account but no inbox in a discord-chat project
// can't be reached: a personal session for them is refused up front.
func TestPersonalDeliveryProblemWithoutInbox(t *testing.T) {
	s := NewMemStore()
	mustCreateProject(t, s, "acme")
	if err := s.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	if err := AddPerson(s, "acme", Human{Name: "alice", Login: "local", Delivery: []DeliveryProfile{{Service: "discord", UserID: "111"}}}); err != nil {
		t.Fatal(err)
	}
	owner, ok := MemberByLogin(s, "acme", "local")
	if !ok || owner.DiscordUID != "111" {
		t.Fatalf("owner = %+v, %v", owner, ok)
	}
	if msg := personalDeliveryProblem(s, "acme", owner); msg == "" {
		t.Fatal("an owner with a Discord account but no inbox in the project can't receive DMs")
	}
}

// MemberOf hands out copies: mutating one never reaches the store.
func TestMemberOfCopiesDelivery(t *testing.T) {
	s, p := peopleFixture(t)
	ms := MembersOf(s, p.ID)
	if len(ms) == 0 || len(ms[0].Delivery) == 0 {
		t.Fatalf("fixture members = %+v", ms)
	}
	want := ms[0].Delivery[0].Address
	ms[0].Delivery[0].Address = "MUTATED"
	if again := MembersOf(s, p.ID); again[0].Delivery[0].Address != want {
		t.Fatalf("MemberOf leaked a live Delivery slice: %+v", again[0].Delivery)
	}
}
