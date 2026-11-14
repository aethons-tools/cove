package jam

import (
	"errors"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// isCurrentMember reports whether p is in ch and hasn't left.
func isCurrentMember(s Store, ch, p ident.ID) bool {
	return slices.ContainsFunc(s.ChannelMembers(ch), func(m ChannelMember) bool { return m.ParticipantID == p && !m.Left })
}

// starterDM is the dm between a personal session and the person who started
// it, failing the test when there is none.
func (f *icFixture) starterDM(inst Instance) Channel {
	f.t.Helper()
	key, kind, _ := chatKey([]ident.ID{inst.OwnerID, ident.ID(inst.ActorID)})
	ch, ok := f.store.ChannelByKey(f.project.ID, kind, key)
	if !ok {
		f.t.Fatalf("no dm between %s and %s", inst.OwnerID, inst.ActorID)
	}
	return ch
}

// legacySessionChannel stores a live session channel for inst with members,
// as an older Jam made one.
func (f *icFixture) legacySessionChannel(inst Instance, members ...ident.ID) Channel {
	f.t.Helper()
	ch, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceSession, Key: inst.ActorID, Label: sessionLabel(inst)})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, m := range append([]ident.ID{ident.ID(inst.ActorID)}, members...) {
		if err := f.store.JoinChannel(ch.ID, m, 1); err != nil {
			f.t.Fatal(err)
		}
	}
	return ch
}

// No session has a channel of its own: setting one up makes none. A
// personal session's starter gets a dm with it; a ticket session becomes
// its unit's worker.
func TestIntercomSetUpMakesNoSessionChannel(t *testing.T) {
	f := newICFixture(t)
	for _, inst := range []Instance{f.ticket, f.personal, f.standing} {
		if err := f.ic.SetUp(inst); err != nil {
			t.Fatalf("SetUp(%s): %v", inst.ActorID, err)
		}
	}
	if got := f.store.ListChannels(f.project.ID, SourceSession); len(got) != 0 {
		t.Fatalf("session channels = %+v, want none", got)
	}
	dm := f.starterDM(f.personal)
	if dm.Kind != SourceDM || !isCurrentMember(f.store, dm.ID, f.alice.ID) || !isCurrentMember(f.store, dm.ID, ident.ID(f.personal.ActorID)) {
		t.Fatalf("starter dm = %+v, members %+v", dm, f.store.ChannelMembers(dm.ID))
	}
	if log, ok := f.ic.TicketChannelOf(f.ticket); !ok || log.Worker != ident.ID(f.ticket.ActorID) {
		t.Fatalf("ticket log = %+v, %v", log, ok)
	}
	if got := f.store.ChannelsOf(ident.ID(f.standing.ActorID)); len(got) != 0 {
		t.Fatalf("a standing session with no rooms is in %v", got)
	}
}

// At serve start, each live session channel an older Jam made is archived:
// its members and history stay, and it takes no more posts.
func TestIntercomReconcileArchivesSessionChannels(t *testing.T) {
	f := newICFixture(t)
	old := f.legacySessionChannel(f.standing, f.bob.ID)
	if _, err := f.ic.PostTrusted(old, intercom.Squawk{From: f.bob.ID, Body: "before the move"}); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := f.store.GetChannel(old.ID)
	if got.Status != StatusArchived {
		t.Fatalf("after Reconcile = %+v, want archived", got)
	}
	if h := f.log.ChannelSince(old.ID, 0, 0); len(h) != 1 || h[0].Body != "before the move" {
		t.Fatalf("history = %+v", h)
	}
	if !isCurrentMember(f.store, old.ID, f.bob.ID) || !f.ic.CanSee(f.bob.ID, got) || f.ic.CanSee(f.alice.ID, got) {
		t.Fatal("an archived session channel stays readable by its members only")
	}
	if _, err := f.ic.PostTrusted(got, intercom.Squawk{From: f.bob.ID, Body: "after"}); !errors.Is(err, ErrRemoved) {
		t.Fatalf("post to an archived session channel: %v", err)
	}
	if err := f.ic.Reconcile(); err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}
	if got := f.store.ListChannels(f.project.ID, SourceSession); len(got) != 0 {
		t.Fatalf("live session channels = %+v", got)
	}
}

// A room bound to the same issue as a unit's log gives the log the issue's
// replies (the room only posts to it from then on, keeping its history),
// both for a log that already exists and for one made now.
func TestIntercomLogTakesARoomsIssueBinding(t *testing.T) {
	f := newICFixture(t)
	bind := Binding{ConnectionID: f.tracker.ID, Ref: "ACME-7", Mode: BindBoth}
	if err := f.store.SetChannelBindings(f.room.ID, []Binding{bind}); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	log, _ := f.ic.TicketChannelOf(f.ticket)
	if holder, ok := f.store.ChannelByBinding(f.tracker.ID, "ACME-7"); !ok || holder.ID != log.ID {
		t.Fatalf("the issue's replies go to %+v, want the log %s", holder, log.ID)
	}
	if room, _ := f.store.GetChannel(f.room.ID); !slices.Equal(room.Bindings, []Binding{{ConnectionID: f.tracker.ID, Ref: "ACME-7", Mode: BindEgress}}) {
		t.Fatalf("room bindings = %+v, want post-only", room.Bindings)
	}

	// A log an older Jam left unbound takes the binding at serve start.
	g := newICFixture(t)
	unbound, err := g.store.CreateChannel(Channel{ProjectID: g.project.ID, Kind: SourceWork, Key: string(g.tracker.ID) + "/ACME-9", Label: "ACME-9"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.store.SetChannelBindings(g.room.ID, []Binding{{ConnectionID: g.tracker.ID, Ref: "ACME-9", Mode: BindBoth}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ic.PostTrusted(g.room, intercom.Squawk{From: g.bob.ID, Body: "room history"}); err != nil {
		t.Fatal(err)
	}
	if err := g.ic.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if holder, ok := g.store.ChannelByBinding(g.tracker.ID, "ACME-9"); !ok || holder.ID != unbound.ID {
		t.Fatalf("after Reconcile the issue's replies go to %+v, want the log", holder)
	}
	if h := g.log.ChannelSince(g.room.ID, 0, 0); len(h) != 1 {
		t.Fatalf("the room's history = %+v", h)
	}
}

// session:<label|id> goes to the dm between the sender and that session,
// within the sender's addressing.
func TestIntercomSessionAddressIsADM(t *testing.T) {
	f := newICFixture(t, "session:spider", "session:ses_*")
	f.standing.Name = "spider"
	if err := f.store.PutInstance(f.standing); err != nil {
		t.Fatal(err)
	}
	p := f.mustPlan(f.personal, "session:spider")
	if p.Channel.Kind != SourceDM || p.Channel.Key != chatKeyOf(f.personal, f.standing) || !slices.Equal(p.Audience, ids(f.standing)) {
		t.Fatalf("session:spider = %+v", p)
	}
	if byID := f.mustPlan(f.personal, "session:"+f.standing.ActorID); byID.Channel.ID != p.Channel.ID {
		t.Fatal("a session by id is the same dm")
	}
	if back := f.mustPlan(f.standing, "session:"+f.personal.ActorID); back.Channel.ID != p.Channel.ID {
		t.Fatal("the other way round is the same dm")
	}
	if tp, err := f.plan(f.personal, "session:"+f.ticket.ActorID); err != nil || tp.Channel.Kind != SourceDM {
		t.Fatalf("session:<ticket session> = %+v, %v; want a dm", tp, err)
	}
	for addr, want := range map[string]error{
		"session:other":      ErrSendDenied,     // not allowed, whether or not it exists
		"session:ses_nosuch": ErrSendUnresolved, // allowed, absent
		"session:":           ErrSendDenied,
		"session:standing":   ErrSendDenied,
	} {
		if _, err := f.plan(f.personal, addr); !errors.Is(err, want) {
			t.Errorf("Plan(%q) = %v, want %v", addr, err, want)
		}
	}
	if _, err := f.plan(f.standing, "session:spider"); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("session:<itself> = %v, want ErrSendUnresolved (no dm with oneself)", err)
	}
	dup := Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Name: "spider", Phase: PhaseLive}
	if err := f.store.PutInstance(dup); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plan(f.personal, "session:spider"); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("ambiguous label: %v", err)
	}
	gone := f.ticket
	gone.Phase = PhaseGone
	if err := f.store.PutInstance(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plan(f.personal, "session:"+gone.ActorID); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("ended session: %v", err)
	}
}

// chatKeyOf is the dm key of two sessions.
func chatKeyOf(a, b Instance) string {
	key, _, _ := chatKey([]ident.ID{ident.ID(a.ActorID), ident.ID(b.ActorID)})
	return key
}

// With no address (an older studio's turn end, an ask or a whiteboard), a
// session posts where it was last delivered a message, else in its unit's
// log, else in the dm with the person who started it; a session with none of
// those must name its conversation.
func TestIntercomDefaultConversation(t *testing.T) {
	f := newICFixture(t)
	if p := f.mustPlan(f.ticket, ""); p.Channel.Kind != SourceWork {
		t.Fatalf("ticket default = %+v, want its log", p.Channel)
	}
	if p := f.mustPlan(f.personal, ""); p.Channel.ID != f.starterDM(f.personal).ID || !slices.Equal(p.Audience, ids(f.alice)) {
		t.Fatalf("personal default = %+v, want the dm with its starter", p)
	}
	if _, err := f.plan(f.standing, ""); !errors.Is(err, ErrSendNeedsTo) {
		t.Fatalf("standing default = %v, want ErrSendNeedsTo", err)
	}
	// bob writes to the standing session: its reply goes back to him.
	dm, err := f.ic.PlanSessionDM(f.bob.ID, f.standing.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.Post(dm, intercom.Squawk{From: f.bob.ID, Body: "status?"}); err != nil {
		t.Fatal(err)
	}
	if p := f.mustPlan(f.standing, ""); p.Channel.ID != dm.Channel.ID {
		t.Fatalf("standing default after bob's dm = %+v, want that dm", p.Channel)
	}
	// So does the ticket session's, rather than its log.
	tdm, err := f.ic.PlanSessionDM(f.bob.ID, f.ticket.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.Post(tdm, intercom.Squawk{From: f.bob.ID, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if p := f.mustPlan(f.ticket, ""); p.Channel.ID != tdm.Channel.ID {
		t.Fatalf("ticket default after a dm = %+v", p.Channel)
	}
}

// Jam's notices about a session (the idle ladder's nags, an SSH connection)
// go to the dm with the person who started it, else to its unit's log;
// a standing session's reach no one. A gone session's notice never makes a
// channel it didn't have.
func TestIntercomNotifyDestinations(t *testing.T) {
	f := newICFixture(t)
	m, err := f.ic.Notify(f.personal, "", "idle for 4h")
	if err != nil || m.Channel != f.starterDM(f.personal).ID {
		t.Fatalf("personal notice = %+v, %v; want the starter's dm", m, err)
	}
	if got := f.log.InboxSince(f.alice.ID, 0, 0); len(got) != 1 || got[0].Body != "idle for 4h" {
		t.Fatalf("alice's inbox = %+v", got)
	}
	m, err = f.ic.Notify(f.ticket, "", "connected over SSH")
	if log, _ := f.ic.TicketChannelOf(f.ticket); err != nil || m.Channel != log.ID {
		t.Fatalf("ticket notice = %+v, %v; want its log", m, err)
	}
	if m, err := f.ic.Notify(f.standing, "", "connected over SSH"); err != nil || m.Channel != "" {
		t.Fatalf("standing notice = %+v, %v; want none and no error", m, err)
	}

	// It ends: the starter still learns why, in the same dm.
	dm := f.starterDM(f.personal)
	if err := f.ic.Ended(f.personal); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveInstance(f.personal.ActorID); err != nil {
		t.Fatal(err)
	}
	if m, err := f.ic.Notify(f.personal, "", "released"); err != nil || m.Channel != dm.ID {
		t.Fatalf("notice after the end = %+v, %v", m, err)
	}
	gone := Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Unit: "ACME-99", Phase: PhaseGone}
	if m, err := f.ic.Notify(gone, "", "ended"); err != nil || m.Channel != "" {
		t.Fatalf("gone session with no log = %+v, %v", m, err)
	}
	if _, ok := f.ic.TicketChannelOf(gone); ok {
		t.Fatal("a notice made a log for a gone session")
	}
}

// From /me, a person reaches a session in the dm between them, and nothing
// else; someone else's personal session isn't theirs to reach.
func TestIntercomPlanSessionDM(t *testing.T) {
	f := newICFixture(t)
	p, err := f.ic.PlanSessionDM(f.bob.ID, f.standing.ActorID)
	if err != nil || p.Channel.Kind != SourceDM || !slices.Equal(p.Audience, ids(f.standing)) {
		t.Fatalf("bob → standing = %+v, %v", p, err)
	}
	if _, err := f.ic.PlanSessionDM(f.bob.ID, f.personal.ActorID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("bob → alice's personal session: %v, want ErrSendDenied", err)
	}
	if f.ic.MayReach(f.bob.ID, f.personal) || !f.ic.MayReach(f.alice.ID, f.personal) {
		t.Fatal("MayReach: only the starter reaches a personal session")
	}
	if _, err := f.ic.PlanSessionDM(f.alice.ID, f.personal.ActorID); err != nil {
		t.Fatalf("alice → her personal session: %v", err)
	}
	if _, err := f.ic.PlanSessionDM(f.carol.ID, f.standing.ActorID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("carol (no member) → standing: %v", err)
	}
	if _, err := f.ic.PlanSessionDM(f.bob.ID, "ses_nosuch"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("unknown session: %v, want ErrSendDenied (never tells)", err)
	}
	if got := f.store.ListChannels(f.project.ID, SourceSession); len(got) != 0 {
		t.Fatalf("a dm made session channels: %+v", got)
	}
}

// From /me, worker:<key> is the dm with the unit's current worker; with no
// worker, nothing is sent.
func TestIntercomPlanWorkerDM(t *testing.T) {
	f := newICFixture(t)
	if _, err := f.ic.PlanWorkerDM(f.bob.ID, "ACME-7"); !errors.Is(err, ErrNoWorker) {
		t.Fatalf("no log yet: %v, want ErrNoWorker", err)
	}
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	p, err := f.ic.PlanWorkerDM(f.bob.ID, "ACME-7")
	if err != nil || p.Channel.Kind != SourceDM || !slices.Equal(p.Audience, ids(f.ticket)) {
		t.Fatalf("bob → worker:ACME-7 = %+v, %v", p, err)
	}
	if _, err := f.ic.PlanWorkerDM(f.carol.ID, "ACME-7"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("carol (no member): %v, want ErrSendDenied", err)
	}
	if err := f.ic.Ended(f.ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.PlanWorkerDM(f.bob.ID, "ACME-7"); !errors.Is(err, ErrNoWorker) {
		t.Fatalf("worker ended: %v, want ErrNoWorker", err)
	}
}

// A bare "*" never reaches sessions: session: needs an explicit glob, so no
// ceiling written before sessions were addressable allows it.
func TestIntercomStarDoesNotAddressSessions(t *testing.T) {
	f := newICFixture(t, "*")
	if _, err := f.plan(f.ticket, "session:"+f.personal.ActorID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("* → session: %v, want ErrSendDenied", err)
	}
	if _, err := f.plan(f.ticket, "user:alice"); err != nil {
		t.Fatalf("* still reaches people: %v", err)
	}
}

// Session channels an older Jam made are left out of a config export (an
// older Jam can't import the kind, and they are archived anyway).
func TestSessionChannelsAreNotExported(t *testing.T) {
	f := newICFixture(t)
	f.legacySessionChannel(f.standing)
	for _, ch := range f.store.ExportConfig().Channels {
		if ch.Kind == SourceSession {
			t.Fatalf("exported a session channel: %+v", ch)
		}
	}
}

// list_targets offers ticket sessions to a session with only session:
// addressing, as a send would reach them.
func TestIntercomSessionTargetsIncludeTicketSessions(t *testing.T) {
	g := newICFixture(t, "session:*")
	a := Actor{ID: g.standing.ActorID, Grants: g.actor.Grants}
	found := false
	for _, tg := range ListTargets(g.store, nil, a, g.now) {
		if tg.Kind == "session" && (tg.Name == g.ticket.ActorID || tg.Name == sessionLabel(g.ticket)) {
			found = true
		}
	}
	if !found {
		t.Fatal("list_targets leaves out a ticket session session: reaches")
	}
}

// user:<x> is the dm with that person; a personal session reaches its
// starter without addressing, and anyone else only as addressing allows.
func TestIntercomUserAddressIsADM(t *testing.T) {
	f := newICFixture(t, "user:bob")
	if p := f.mustPlan(f.personal, "user:alice"); p.Channel.ID != f.starterDM(f.personal).ID {
		t.Fatalf("a personal session to its starter = %+v", p.Channel)
	}
	if p := f.mustPlan(f.standing, "user:bob"); p.Channel.Kind != SourceDM || !slices.Equal(p.Audience, ids(f.bob)) {
		t.Fatalf("user:bob = %+v", p)
	}
	if _, err := f.plan(f.standing, "user:alice"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("user:alice outside addressing: %v", err)
	}
}

// call_in with no channel calls someone into the session's unit's log; a
// session with no unit names the room.
func TestIntercomCallInDefault(t *testing.T) {
	f := newICFixture(t, "user:*")
	ch, who, err := f.ic.CallIn(f.poster(f.ticket), "", "user:bob")
	if log, _ := f.ic.TicketChannelOf(f.ticket); err != nil || ch.ID != log.ID || who != f.bob.ID {
		t.Fatalf("call_in into the default = %+v, %s, %v", ch, who, err)
	}
	if _, _, err := f.ic.CallIn(f.poster(f.standing), "", "user:bob"); !errors.Is(err, ErrSendNeedsTo) {
		t.Fatalf("call_in with no unit and no channel: %v, want ErrSendNeedsTo", err)
	}
}

// defaultGroup gives inst a default conversation with people: their group
// (or dm, for one), in which the first of them writes to it.
func (f *icFixture) defaultGroup(inst Instance, people ...ident.ID) Channel {
	f.t.Helper()
	ch, err := f.ic.chat(f.project, append([]ident.ID{ident.ID(inst.ActorID)}, people...))
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.ic.PostTrusted(ch, intercom.Squawk{From: people[0], Body: "hello"}); err != nil {
		f.t.Fatal(err)
	}
	if got := f.mustPlan(inst, "").Channel; got.ID != ch.ID {
		f.t.Fatalf("default conversation = %+v, want %s", got, ch.ID)
	}
	return ch
}

// On the turn-end rule, an ask or a whiteboard with no `to` is refused as a
// send is: the studio fills in the turn's last `to`, so Jam never guesses
// from where the latest message arrived. An older studio keeps the default
// conversation.
func TestIntercomAskAndBoardNeedATo(t *testing.T) {
	f := newICFixture(t)
	dm := f.defaultGroup(f.standing, f.alice.ID)
	rule := f.standing
	rule.TurnProtocol = sessionctx.TurnProtocolTurnRule
	if _, _, err := f.ic.Ask(f.poster(rule), "", AskInput{Question: "q", Options: askYesNo}, f.now); !errors.Is(err, ErrSendNeedsTo) {
		t.Fatalf("ask with no to on the turn-end rule: %v, want ErrSendNeedsTo", err)
	}
	bs := NewBoards(f.log, f.ic)
	if _, err := bs.Create(f.poster(rule), "", "Plan", f.now); !errors.Is(err, ErrSendNeedsTo) {
		t.Fatalf("board with no to on the turn-end rule: %v, want ErrSendNeedsTo", err)
	}
	if _, ch, err := f.ic.Ask(f.poster(f.standing), "", AskInput{Question: "q", Options: askYesNo}, f.now); err != nil || ch.ID != dm.ID {
		t.Fatalf("an older studio's ask = %+v, %v; want its default conversation", ch, err)
	}
	if _, err := bs.Create(f.poster(f.standing), "", "Plan", f.now); err != nil {
		t.Fatalf("an older studio's board: %v", err)
	}
}
