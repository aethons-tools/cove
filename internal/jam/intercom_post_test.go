package jam

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// icFixture is a project "acme" with members alice and bob (carol is a user
// but no member), a linear tracker connection, a room "eng", and sessions:
// a ticket session on ACME-7, a personal session alice started, and a
// standing one. Every session runs as an actor granted acme/impl, whose
// addressing the test sets.
type icFixture struct {
	t                 *testing.T
	store             *MemStore
	log               *intercom.Log
	ic                *Intercom
	tail              int64
	project           Project
	tracker           Connection
	alice, bob, carol User
	room              Channel
	ticket, personal  Instance
	standing          Instance
	actor             Actor
	addressing        []string
	now               time.Time
}

func newICFixture(t *testing.T, addressing ...string) *icFixture {
	t.Helper()
	f := &icFixture{t: t, store: NewMemStore(), tail: 10, addressing: addressing, now: time.Now()}
	s := f.store
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.CreateProject("acme"))
	f.project, _ = s.GetProject("acme")
	var err error
	f.tracker, err = s.CreateConnection(Connection{Kind: "linear", Name: "linear"})
	must(err)
	for _, u := range []*User{&f.alice, &f.bob, &f.carol} {
		name := map[*User]string{&f.alice: "alice", &f.bob: "bob", &f.carol: "carol"}[u]
		*u, err = s.CreateUser(User{Name: name})
		must(err)
	}
	must(s.AddMember(f.project.ID, f.alice.ID))
	must(s.AddMember(f.project.ID, f.bob.ID))
	f.room, err = s.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceRoom, Key: "eng", Label: "eng"})
	must(err)
	must(s.PutRole("acme", Role{Name: "impl", Scope: Scope{Addressing: addressing}}))
	f.actor = Actor{ID: "actor", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	f.ticket = Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Unit: "ACME-7", Phase: PhaseLive}
	f.personal = Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", OwnerID: f.alice.ID, SessionKind: "personal", Phase: PhaseLive}
	f.standing = Instance{ActorID: "standing-acme-impl-spider", Project: "acme", Role: "impl", SessionKind: "standing", Phase: PhaseLive}
	for _, inst := range []Instance{f.ticket, f.personal, f.standing} {
		must(s.PutInstance(inst))
	}
	f.log = intercom.NewMemLog(nil)
	f.ic = NewIntercom(s, func() (ident.ID, bool) { return f.tracker.ID, true }, f.log, func() int64 { return f.tail }, nil)
	return f
}

func (f *icFixture) poster(inst Instance) Poster {
	a := f.actor
	return Poster{ID: ident.ID(inst.ActorID), Session: &inst, Actor: &a}
}

func (f *icFixture) plan(inst Instance, addr string) (Planned, error) {
	f.t.Helper()
	return f.ic.Plan(f.poster(inst), addr, f.now)
}

func (f *icFixture) mustPlan(inst Instance, addr string) Planned {
	f.t.Helper()
	p, err := f.plan(inst, addr)
	if err != nil {
		f.t.Fatalf("Plan(%s, %q): %v", inst.ActorID, addr, err)
	}
	return p
}

func ids(xs ...any) []ident.ID {
	var out []ident.ID
	for _, x := range xs {
		switch v := x.(type) {
		case User:
			out = append(out, v.ID)
		case Instance:
			out = append(out, ident.ID(v.ActorID))
		case ident.ID:
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

func isMember(s Store, ch, p ident.ID) bool {
	return slices.ContainsFunc(s.ChannelMembers(ch), func(m ChannelMember) bool { return m.ParticipantID == p })
}

func TestIntercomTicketChannelFollowsSessions(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatalf("SetUp: %v", err)
	}
	ch, ok := f.store.ChannelByKey(f.project.ID, SourceWork, string(f.tracker.ID)+"/ACME-7")
	if !ok || ch.Label != "ACME-7" || len(ch.Bindings) != 1 || ch.Bindings[0] != (Binding{ConnectionID: f.tracker.ID, Ref: "ACME-7", Mode: BindBoth}) {
		t.Fatalf("ticket channel = %+v, %v", ch, ok)
	}
	if got, _ := f.store.ChannelByBinding(f.tracker.ID, "ACME-7"); got.ID != ch.ID {
		t.Fatal("tracker replies on the ticket resolve to its channel")
	}
	if m := f.store.ChannelMembers(ch.ID); len(m) != 1 || m[0] != (ChannelMember{ParticipantID: ident.ID(f.ticket.ActorID), JoinedSeq: 10, Wake: WakeNamed}) {
		t.Fatalf("members = %+v, want the session from the log tail", m)
	}
	// The session ends; a re-dispatch is a new session on the same channel.
	f.tail = 15
	if err := f.ic.Ended(f.ticket); err != nil {
		t.Fatalf("Ended: %v", err)
	}
	next := f.ticket
	next.ActorID = string(ident.New(ident.Session))
	f.tail = 20
	if err := f.ic.SetUp(next); err != nil {
		t.Fatal(err)
	}
	if m := f.store.ChannelMembers(ch.ID); len(m) != 1 || m[0].ParticipantID != ident.ID(next.ActorID) || m[0].JoinedSeq != 20 {
		t.Fatalf("members after re-dispatch = %+v", m)
	}
	if got := f.store.ListChannels(f.project.ID, SourceWork); len(got) != 1 {
		t.Fatalf("ticket channels = %+v, want one per ticket", got)
	}
	// A session with no ticket has no ticket channel.
	if err := f.ic.SetUp(f.standing); err != nil || len(f.store.ListChannels(f.project.ID, SourceWork)) != 1 {
		t.Fatalf("standing SetUp: %v, ticket channels %v", err, f.store.ListChannels(f.project.ID, SourceWork))
	}
}

func TestIntercomPlanDefaults(t *testing.T) {
	f := newICFixture(t)
	// ticket:<its key> is a ticket session's log, created on demand for a
	// session set up before work logs existed; with nothing delivered to it,
	// it is its default too.
	p := f.mustPlan(f.ticket, "ticket:ACME-7")
	if p.Channel.Kind != SourceWork || p.Channel.Label != "ACME-7" || len(p.Audience) != 0 {
		t.Fatalf("own ticket = %+v", p)
	}
	if !isMember(f.store, p.Channel.ID, ident.ID(f.ticket.ActorID)) {
		t.Fatal("the session joins its ticket channel")
	}
	// A member user hears from it.
	if err := f.store.JoinChannel(p.Channel.ID, f.alice.ID, 11); err != nil {
		t.Fatal(err)
	}
	if got := f.mustPlan(f.ticket, "ticket:ACME-7").Audience; !slices.Equal(got, ids(f.alice)) {
		t.Fatalf("audience = %v", got)
	}

	// Personal and standing sessions: intercom_session_test.go.
}

func TestIntercomPlanUsers(t *testing.T) {
	f := newICFixture(t, "user:*")
	p := f.mustPlan(f.standing, "user:alice")
	if p.Channel.Kind != SourceDM || !slices.Equal(p.Audience, ids(f.alice)) {
		t.Fatalf("user:alice = %+v", p)
	}
	if byID := f.mustPlan(f.standing, "user:"+string(f.alice.ID)); byID.Channel.ID != p.Channel.ID {
		t.Fatal("a user by id is the same chat")
	}
	if alias := f.mustPlan(f.standing, "human:alice"); alias.Channel.ID != p.Channel.ID {
		t.Fatal("human: is an alias of user:")
	}
	group := f.mustPlan(f.standing, "chat:user:bob,user:alice")
	if group.Channel.Kind != SourceGroup || group.Channel.ID == p.Channel.ID || !slices.Equal(group.Audience, ids(f.alice, f.bob)) {
		t.Fatalf("group chat = %+v", group)
	}
	if again := f.mustPlan(f.standing, "chat:user:alice,user:bob,user:alice"); again.Channel.ID != group.Channel.ID {
		t.Fatal("member order and repeats don't make a new chat")
	}
	for addr, want := range map[string]error{
		"user:carol":             ErrSendUnresolved, // not a member of the project
		"user:nobody":            ErrSendUnresolved,
		"chat:user:alice,user:x": ErrSendUnresolved,
		"chat:":                  ErrSendDenied,
		"pigeon:alice":           ErrSendDenied,
		"alice":                  ErrSendDenied,
	} {
		if _, err := f.plan(f.standing, addr); !errors.Is(err, want) {
			t.Errorf("Plan(%q) = %v, want %v", addr, err, want)
		}
	}
}

func TestIntercomPlanAddressingCeiling(t *testing.T) {
	f := newICFixture(t, "user:alice", "channel:eng")
	if _, err := f.plan(f.standing, "user:"+string(f.alice.ID)); err != nil {
		t.Fatalf("a name glob allows that person by id: %v", err)
	}
	if _, err := f.plan(f.standing, "user:bob"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("user:bob = %v, want denied", err)
	}
	if _, err := f.plan(f.standing, "chat:user:alice,user:bob"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a chat needs every member allowed: %v", err)
	}
	if _, err := f.plan(f.standing, "user:carol"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("denied before existence: %v", err)
	}
	expired := f.poster(f.standing)
	expired.Actor.Expiry = f.now.Add(-time.Minute)
	if _, err := f.ic.Plan(expired, "user:alice", f.now); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("expired actor: %v", err)
	}
	// The default never consults addressing.
	if _, err := f.plan(f.personal, ""); err != nil {
		t.Fatalf("personal default with narrow addressing: %v", err)
	}
}

func TestIntercomPlanRoomsAndTickets(t *testing.T) {
	f := newICFixture(t, "channel:*", "ticket:*")
	if err := f.store.JoinChannel(f.room.ID, f.bob.ID, 3); err != nil {
		t.Fatal(err)
	}
	r := f.mustPlan(f.standing, "channel:eng")
	if r.Channel.ID != f.room.ID || !slices.Equal(r.Audience, ids(f.bob)) {
		t.Fatalf("room = %+v", r)
	}
	if isMember(f.store, f.room.ID, ident.ID(f.standing.ActorID)) {
		t.Fatal("a session posts to a room without joining it")
	}
	if _, err := f.plan(f.standing, "channel:nope"); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("unknown room: %v", err)
	}

	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	own := f.mustPlan(f.ticket, "ticket:ACME-7")
	other := f.mustPlan(f.standing, "ticket:ACME-7")
	if own.Channel.ID != other.Channel.ID || !slices.Equal(other.Audience, ids(f.ticket)) {
		t.Fatalf("ticket: own %+v, other %+v", own, other)
	}
	if byConn := f.mustPlan(f.standing, "ticket:linear/ACME-7"); byConn.Channel.ID != own.Channel.ID {
		t.Fatal("ticket:<connection>/<key> names the same channel")
	}
	if _, err := f.plan(f.standing, "ticket:ACME-9"); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("a ticket with no channel: %v", err)
	}

	narrow := newICFixture(t)
	if err := narrow.ic.SetUp(narrow.ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := narrow.plan(narrow.ticket, "ticket:ACME-7"); err != nil {
		t.Fatalf("a session may always post to its own ticket: %v", err)
	}
	if _, err := narrow.plan(narrow.standing, "ticket:ACME-7"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("another ticket needs ticket: addressing: %v", err)
	}
	if _, err := narrow.plan(narrow.standing, "channel:eng"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a room needs channel: addressing: %v", err)
	}
}

func TestIntercomAudienceSkipsEndedSessions(t *testing.T) {
	f := newICFixture(t, "user:*")
	chat := f.mustPlan(f.personal, "")
	gone := f.personal
	gone.Phase = PhaseGone
	if err := f.store.PutInstance(gone); err != nil {
		t.Fatal(err)
	}
	p, err := f.ic.PlanChannel(Poster{ID: f.alice.ID}, chat.Channel.ID)
	if err != nil {
		t.Fatalf("PlanChannel: %v", err)
	}
	if len(p.Audience) != 0 {
		t.Fatalf("audience = %v, want the ended session skipped", p.Audience)
	}
}

func TestIntercomPlanChannelForUsers(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	ticket := f.mustPlan(f.ticket, "").Channel
	chat := f.mustPlan(f.personal, "").Channel

	p, err := f.ic.PlanChannel(Poster{ID: f.bob.ID}, ticket.ID)
	if err != nil || !slices.Equal(p.Audience, ids(f.ticket)) {
		t.Fatalf("a member of the project posts to its tickets: %+v, %v", p, err)
	}
	if _, err := f.ic.PlanChannel(Poster{ID: f.carol.ID}, ticket.ID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a non-member: %v", err)
	}
	if _, err := f.ic.PlanChannel(Poster{ID: f.bob.ID}, chat.ID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a chat is for its members: %v", err)
	}
	if p, err := f.ic.PlanChannel(Poster{ID: f.alice.ID}, chat.ID); err != nil || !slices.Equal(p.Audience, ids(f.personal)) {
		t.Fatalf("alice in her chat: %+v, %v", p, err)
	}
	if _, err := f.ic.PlanChannel(Poster{ID: f.bob.ID}, f.room.ID); err != nil {
		t.Fatalf("a member of the project posts to its rooms: %v", err)
	}
	if err := f.store.ArchiveChannel(f.room.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.PlanChannel(Poster{ID: f.bob.ID}, f.room.ID); !errors.Is(err, ErrRemoved) {
		t.Fatalf("archived room: %v", err)
	}
	// Existence is never told apart from refusal.
	if _, err := f.ic.PlanChannel(Poster{ID: f.bob.ID}, "chn_01j9q3zzzzzzzzzzzzzzzzzzzz"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("unknown channel: %v", err)
	}
	if _, err := f.ic.PlanChannel(Poster{ID: f.carol.ID}, f.room.ID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("an archived room someone can't see: %v", err)
	}
	// Sessions post by address, where their addressing applies.
	if _, err := f.ic.PlanChannel(f.poster(f.personal), chat.ID); err == nil {
		t.Fatal("PlanChannel must refuse a session poster")
	}
}

func TestIntercomCanSee(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	ticket := f.mustPlan(f.ticket, "").Channel
	chat := f.mustPlan(f.personal, "").Channel
	for _, c := range []struct {
		who  ident.ID
		ch   Channel
		want bool
	}{
		{f.bob.ID, ticket, true},
		{f.carol.ID, ticket, false},
		{ident.ID(f.ticket.ActorID), ticket, true},
		{ident.ID(f.standing.ActorID), ticket, false},
		{f.alice.ID, chat, true},
		{f.bob.ID, chat, false},
		{f.bob.ID, f.room, true},
		{f.carol.ID, f.room, false},
	} {
		if got := f.ic.CanSee(c.who, c.ch); got != c.want {
			t.Errorf("CanSee(%s, %s %s) = %v, want %v", c.who, c.ch.Kind, c.ch.Label, got, c.want)
		}
	}
}

// The home channel is still a send: an expired actor can't make one.
func TestIntercomDefaultIsAuthorized(t *testing.T) {
	f := newICFixture(t)
	expired := f.poster(f.ticket)
	expired.Actor.Expiry = f.now.Add(-time.Minute)
	if _, err := f.ic.Plan(expired, "", f.now); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("expired default: %v", err)
	}
}

// A chat whose members weren't all joined (a crash or a racing create) is
// completed by the next post rather than delivering to no one.
func TestIntercomChatCompletesMembership(t *testing.T) {
	f := newICFixture(t, "user:*")
	members := []string{f.standing.ActorID, string(f.alice.ID)}
	slices.Sort(members)
	ch, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceDM, Key: strings.Join(members, ","), Label: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.JoinChannel(ch.ID, ident.ID(f.standing.ActorID), 1); err != nil {
		t.Fatal(err)
	}
	p := f.mustPlan(f.standing, "user:alice")
	if p.Channel.ID != ch.ID || !slices.Equal(p.Audience, ids(f.alice)) {
		t.Fatalf("plan = %+v", p)
	}
}

// Leaving the project ends a user's access to its channels, live.
func TestIntercomProjectMembershipGatesUsers(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	ticket := f.mustPlan(f.ticket, "").Channel
	for _, ch := range []ident.ID{ticket.ID, f.room.ID} {
		if err := f.store.JoinChannel(ch, f.bob.ID, 12); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.mustPlan(f.ticket, "").Audience; !slices.Equal(got, ids(f.bob)) {
		t.Fatalf("audience = %v", got)
	}
	if err := f.store.RemoveMember(f.project.ID, f.bob.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.mustPlan(f.ticket, "").Audience; len(got) != 0 {
		t.Fatalf("audience after bob left = %v", got)
	}
	for _, ch := range []Channel{ticket, f.room} {
		if f.ic.CanSee(f.bob.ID, ch) {
			t.Errorf("bob still sees %s %s", ch.Kind, ch.Label)
		}
		if _, err := f.ic.PlanChannel(Poster{ID: f.bob.ID}, ch.ID); !errors.Is(err, ErrSendDenied) {
			t.Errorf("bob still posts to %s %s: %v", ch.Kind, ch.Label, err)
		}
	}
}

// A name glob never matches a person's id: an id is matched only by that
// exact id, user:* or * — so ids never widen what a name glob grants.
func TestIntercomNameGlobsNeverMatchIDs(t *testing.T) {
	for _, globs := range [][]string{{"user:u*"}, {"user:*_*", "user:*0*"}} {
		f := newICFixture(t, globs...)
		for _, addr := range []string{"user:alice", "user:" + string(f.alice.ID)} {
			if _, err := f.plan(f.standing, addr); !errors.Is(err, ErrSendDenied) {
				t.Errorf("%v → %s: %v, want denied", globs, addr, err)
			}
		}
	}
	for _, glob := range []string{"user:*", "*"} {
		f := newICFixture(t, glob)
		if _, err := f.plan(f.standing, "user:"+string(f.alice.ID)); err != nil {
			t.Errorf("%s → alice's id: %v, want allowed", glob, err)
		}
	}
}

// A person who messaged the session in a dm is a reply target by name,
// whatever the session's addressing: under the turn-end rule that reply has
// no other way to reach them. Someone who didn't message it still needs
// addressing.
func TestReplyToPersonInADM(t *testing.T) {
	f := newICFixture(t, "ticket:*")
	f.standing.Name, f.standing.Activity = "spider", ActivityWaiting
	if err := f.store.PutInstance(f.standing); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plan(f.standing, "user:alice"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("before alice wrote: %v", err)
	}
	// Escalation opens a dm with alice; she answers there.
	alice, _ := MemberOf(f.store, f.project.ID, f.alice.ID)
	if err := f.ic.Escalate(context.Background(), f.standing, 1, "infra", []Member{alice}); err != nil {
		t.Fatal(err)
	}
	dm, ok := f.ic.existingChat(f.project, ids(f.standing, f.alice))
	if !ok {
		t.Fatal("no dm")
	}
	if _, err := f.ic.PostTrusted(dm, intercom.Squawk{From: f.alice.ID, Body: "can you look?"}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"user:alice", "user:" + string(f.alice.ID)} {
		if pl, err := f.plan(f.standing, to); err != nil || pl.Channel.ID != dm.ID {
			t.Errorf("reply to %q = %+v, %v; want the dm", to, pl.Channel, err)
		}
	}
	if _, err := f.plan(f.standing, "user:bob"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("bob, who never wrote: %v", err)
	}
}

// ReplyTo names each kind of conversation so that a send to it lands back
// in the same channel.
func TestReplyToRoundTrips(t *testing.T) {
	f := newICFixture(t, "session:*")
	f.standing.Name = "spider"
	if err := f.store.PutInstance(f.standing); err != nil {
		t.Fatal(err)
	}
	// A dm alice opened with it.
	byAlice, err := f.ic.PlanSessionDM(f.alice.ID, f.standing.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.Post(byAlice, intercom.Squawk{From: f.alice.ID, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	home := byAlice.Channel
	// A dm another session opened with it.
	dm := f.mustPlan(f.personal, "chat:session:"+f.standing.ActorID).Channel
	// Its own ticket's work log.
	log := f.mustPlan(f.ticket, "work:ACME-7").Channel
	for _, c := range []struct {
		inst Instance
		ch   Channel
		want string
	}{
		{f.standing, home, "chat:user:" + string(f.alice.ID)},
		{f.standing, dm, "chat:session:" + f.personal.ActorID},
		{f.ticket, log, "work:ACME-7"},
	} {
		to := f.ic.ReplyTo(c.inst, c.ch.ID)
		if to != c.want {
			t.Errorf("ReplyTo(%s) = %q, want %q", c.ch.Kind, to, c.want)
			continue
		}
		if pl, err := f.plan(c.inst, to); err != nil || pl.Channel.ID != c.ch.ID {
			t.Errorf("send to %q = %+v, %v; want channel %s", to, pl.Channel, err, c.ch.ID)
		}
	}
	if to := f.ic.ReplyTo(f.standing, f.room.ID); to != "channel:eng" {
		t.Errorf("room = %q", to)
	}
	if to := f.ic.ReplyTo(f.standing, "chn_nope"); to != "" {
		t.Errorf("unknown channel = %q", to)
	}
}
