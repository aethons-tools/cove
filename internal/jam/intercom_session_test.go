package jam

import (
	"errors"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// isCurrentMember reports whether p is in ch and hasn't left.
func isCurrentMember(s Store, ch, p ident.ID) bool {
	return slices.ContainsFunc(s.ChannelMembers(ch), func(m ChannelMember) bool { return m.ParticipantID == p && !m.Left })
}

func (f *icFixture) sessionChannel(inst Instance) Channel {
	f.t.Helper()
	ch, ok := f.store.ChannelByKey(f.project.ID, SourceSession, inst.ActorID)
	if !ok {
		f.t.Fatalf("no session channel for %s", inst.ActorID)
	}
	return ch
}

// A session without a ticket gets its own channel at setup: the session is
// in it for good, and a personal session's starter is called in once.
func TestIntercomSessionChannelLifecycle(t *testing.T) {
	f := newICFixture(t)
	f.personal.Name = "alice-help"
	if err := f.ic.SetUp(f.personal); err != nil {
		t.Fatalf("SetUp: %v", err)
	}
	ch := f.sessionChannel(f.personal)
	if ch.Label != "alice-help" || len(ch.Bindings) != 0 || ch.Status != StatusLive {
		t.Fatalf("session channel = %+v", ch)
	}
	if !isCurrentMember(f.store, ch.ID, ident.ID(f.personal.ActorID)) || !isCurrentMember(f.store, ch.ID, f.alice.ID) {
		t.Fatalf("members = %+v, want the session and its starter", f.store.ChannelMembers(ch.ID))
	}
	// The starter may leave; setting the session up again doesn't call them back.
	if err := f.store.LeaveChannel(ch.ID, f.alice.ID, 12); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if isCurrentMember(f.store, ch.ID, f.alice.ID) {
		t.Fatal("a starter who left was called back in")
	}
	if got := f.store.ListChannels(f.project.ID, SourceSession); len(got) != 2 {
		t.Fatalf("session channels = %+v, want the personal and (by Reconcile) the standing one", got)
	}
	if m := f.store.ChannelMembers(f.sessionChannel(f.standing).ID); len(m) != 1 || m[0].ParticipantID != ident.ID(f.standing.ActorID) {
		t.Fatalf("standing members = %+v", m)
	}
	if _, ok := f.store.ChannelByKey(f.project.ID, SourceSession, f.ticket.ActorID); ok {
		t.Fatal("a ticket session's home is its ticket: no session channel")
	}

	// It ends: the channel is archived, but Jam's notice about the end still
	// reaches the channel's members.
	if err := f.store.JoinChannel(ch.ID, f.bob.ID, 13); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.Ended(f.personal); err != nil {
		t.Fatalf("Ended: %v", err)
	}
	if got, _ := f.store.GetChannel(ch.ID); got.Status != StatusArchived {
		t.Fatalf("after Ended = %+v", got)
	}
	m, err := f.ic.Notify(f.personal, "", "it ended")
	if err != nil || m.Channel != ch.ID {
		t.Fatalf("Notify after Ended = %+v, %v", m, err)
	}
	if got := f.log.InboxSince(f.bob.ID, 0, 0); len(got) != 1 || got[0].Body != "it ended" {
		t.Fatalf("bob's inbox = %+v", got)
	}
	if _, err := f.plan(f.standing, "session:"+f.personal.ActorID); err == nil {
		t.Fatal("an archived session channel takes no posts")
	}
}

// With no address, every session posts to its home channel: its ticket's,
// else its own session channel (no more 400 for a standing session).
func TestIntercomPlanHomeChannels(t *testing.T) {
	f := newICFixture(t)
	if p := f.mustPlan(f.ticket, ""); p.Channel.Kind != SourceTicket {
		t.Fatalf("ticket home = %+v", p.Channel)
	}
	p := f.mustPlan(f.personal, "")
	if p.Channel.Kind != SourceSession || p.Channel.Key != f.personal.ActorID || !slices.Equal(p.Audience, ids(f.alice)) {
		t.Fatalf("personal home = %+v", p)
	}
	s := f.mustPlan(f.standing, "")
	if s.Channel.Kind != SourceSession || len(s.Audience) != 0 {
		t.Fatalf("standing home = %+v", s)
	}
	// A starter who left the project hears nothing, but the session still posts.
	if err := f.store.RemoveMember(f.project.ID, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if again := f.mustPlan(f.personal, ""); again.Channel.ID != p.Channel.ID || len(again.Audience) != 0 {
		t.Fatalf("after the starter left the project = %+v", again)
	}
}

// session:<label|id> addresses another session's home channel, within the
// poster's addressing; posting there joins the poster.
func TestIntercomSessionAddressing(t *testing.T) {
	f := newICFixture(t, "session:spider", "session:ses_*")
	f.standing.Name = "spider"
	if err := f.store.PutInstance(f.standing); err != nil {
		t.Fatal(err)
	}
	p := f.mustPlan(f.personal, "session:spider")
	if p.Channel.Kind != SourceSession || p.Channel.Key != f.standing.ActorID || !p.Join || !slices.Equal(p.Audience, ids(f.standing)) {
		t.Fatalf("session:spider = %+v", p)
	}
	m, err := f.ic.Post(p, intercom.Squawk{From: ident.ID(f.personal.ActorID), Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !isCurrentMember(f.store, m.Channel, ident.ID(f.personal.ActorID)) {
		t.Fatal("posting by session: joins the poster")
	}
	if byID := f.mustPlan(f.personal, "session:"+f.standing.ActorID); byID.Channel.ID != p.Channel.ID {
		t.Fatal("a session by id is the same channel")
	}
	// A ticket session's home is its ticket channel, which needs ticket:
	// addressing (TestIntercomSessionAddressToATicketNeedsTicketAddressing).
	if _, err := f.plan(f.personal, "session:"+f.ticket.ActorID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("session:<ticket session> without ticket: = %v", err)
	}
	// One's own session needs no addressing.
	if own := f.mustPlan(f.standing, "session:spider"); own.Channel.ID != p.Channel.ID {
		t.Fatal("own session")
	}
	for addr, want := range map[string]error{
		"session:other":         ErrSendDenied,     // not allowed, whether or not it exists
		"session:ses_nosuch":    ErrSendUnresolved, // allowed, absent
		"session:":              ErrSendDenied,
		"session:" + "standing": ErrSendDenied,
	} {
		if _, err := f.plan(f.personal, addr); !errors.Is(err, want) {
			t.Errorf("Plan(%q) = %v, want %v", addr, err, want)
		}
	}
	// Two live sessions sharing a label: the label names neither.
	dup := Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Name: "spider", Phase: PhaseLive}
	if err := f.store.PutInstance(dup); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plan(f.personal, "session:spider"); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("ambiguous label: %v", err)
	}
	// An ended session is gone.
	gone := f.ticket
	gone.Phase = PhaseGone
	if err := f.store.PutInstance(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plan(f.personal, "session:"+gone.ActorID); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("ended session: %v", err)
	}
}

// People see a standing session's channel project-wide; a personal
// session's only as members (invite-only).
func TestIntercomSessionChannelVisibility(t *testing.T) {
	f := newICFixture(t)
	personal := f.mustPlan(f.personal, "").Channel
	standing := f.mustPlan(f.standing, "").Channel
	for _, c := range []struct {
		who  ident.ID
		ch   Channel
		want bool
	}{
		{f.alice.ID, personal, true},
		{f.bob.ID, personal, false},
		{f.bob.ID, standing, true},
		{f.carol.ID, standing, false},
		{ident.ID(f.ticket.ActorID), standing, false},
	} {
		if got := f.ic.CanSee(c.who, c.ch); got != c.want {
			t.Errorf("CanSee(%s, %s) = %v, want %v", c.who, c.ch.Label, got, c.want)
		}
	}
}

// From /me, a person reaches a session in its home channel, joining it —
// unless it is someone else's personal session.
func TestIntercomPlanHome(t *testing.T) {
	f := newICFixture(t)
	p, err := f.ic.PlanHome(f.bob.ID, ident.ID(f.standing.ActorID))
	if err != nil || p.Channel.Kind != SourceSession || !p.Join || !slices.Equal(p.Audience, ids(f.standing)) {
		t.Fatalf("bob → standing = %+v, %v", p, err)
	}
	if _, err := f.ic.PlanHome(f.bob.ID, ident.ID(f.personal.ActorID)); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("bob → alice's personal session: %v, want ErrSendDenied", err)
	}
	if _, err := f.ic.PlanHome(f.alice.ID, ident.ID(f.personal.ActorID)); err != nil {
		t.Fatalf("alice → her personal session: %v", err)
	}
	if _, err := f.ic.PlanHome(f.carol.ID, ident.ID(f.standing.ActorID)); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("carol (no member) → standing: %v", err)
	}
	if _, err := f.ic.PlanHome(f.bob.ID, "ses_nosuch"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("unknown session: %v, want ErrSendDenied (never tells)", err)
	}
}

// A relay post into a personal session's channel (a Discord reply in a
// shared inbox) never makes its author a member: the channel is invite-only.
// A standing session's channel is open, so a person posting there joins.
func TestIntercomTrustedPostJoinsOnlyOpenSessionChannels(t *testing.T) {
	f := newICFixture(t)
	personal := f.mustPlan(f.personal, "").Channel
	if _, err := f.ic.PostTrusted(personal, intercom.Squawk{From: f.bob.ID, Body: "reply in a shared inbox"}); err != nil {
		t.Fatal(err)
	}
	if isCurrentMember(f.store, personal.ID, f.bob.ID) || f.ic.CanSee(f.bob.ID, personal) {
		t.Fatal("a trusted post joined bob to a personal session's channel")
	}
	standing := f.mustPlan(f.standing, "").Channel
	if _, err := f.ic.PostTrusted(standing, intercom.Squawk{From: f.bob.ID, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if !isCurrentMember(f.store, standing.ID, f.bob.ID) {
		t.Fatal("posting in a standing session's channel joins")
	}
}

// A session set up again under the same id (a restart, an upgrade) gets its
// channel back, with whoever was in it — not a new one.
func TestIntercomSessionChannelSurvivesRestart(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.standing); err != nil {
		t.Fatal(err)
	}
	ch := f.sessionChannel(f.standing)
	if err := f.store.JoinChannel(ch.ID, f.bob.ID, 11); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.Ended(f.standing); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.SetUp(f.standing); err != nil {
		t.Fatal(err)
	}
	again := f.sessionChannel(f.standing)
	if again.ID != ch.ID || !isCurrentMember(f.store, ch.ID, f.bob.ID) {
		t.Fatalf("after restart = %+v (was %s), members %+v", again, ch.ID, f.store.ChannelMembers(again.ID))
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

// A ticket session's home is its ticket: reaching it by session: needs the
// ticket: addressing too, never a way around it.
func TestIntercomSessionAddressToATicketNeedsTicketAddressing(t *testing.T) {
	f := newICFixture(t, "session:*")
	if _, err := f.plan(f.standing, "session:"+f.ticket.ActorID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("session: to a ticket session without ticket: addressing = %v, want ErrSendDenied", err)
	}
	g := newICFixture(t, "session:*", "ticket:ACME-*")
	if p, err := g.plan(g.standing, "session:"+g.ticket.ActorID); err != nil || p.Channel.Kind != SourceTicket {
		t.Fatalf("with ticket: addressing = %+v, %v", p, err)
	}
}

// Jam's notice for a session that is gone and never had its own channel
// doesn't conjure one up.
func TestIntercomNotifyNeverCreatesForAGoneSession(t *testing.T) {
	f := newICFixture(t)
	if err := f.store.RemoveInstance(f.standing.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.Notify(f.standing, "", "ended"); err == nil {
		t.Fatal("Notify for a gone session with no channel must fail")
	}
	if got := f.store.ListChannels(f.project.ID, SourceSession); len(got) != 0 {
		t.Fatalf("a channel was created for a gone session: %+v", got)
	}
}

// Session channels are runtime state: a config export leaves them out (a
// restore makes them again for live sessions; an older Jam can't read them).
func TestSessionChannelsAreNotExported(t *testing.T) {
	f := newICFixture(t)
	f.mustPlan(f.standing, "")
	for _, ch := range f.store.ExportConfig().Channels {
		if ch.Kind == SourceSession {
			t.Fatalf("exported a session channel: %+v", ch)
		}
	}
}

// The ticket: requirement for reaching a ticket session honours every form
// a ticket: glob takes (bare key, or scoped to the tracker connection), and
// list_targets offers only the sessions a send would reach.
func TestIntercomSessionTicketFormsAndTargets(t *testing.T) {
	f := newICFixture(t, "session:*", "ticket:linear/*")
	if p, err := f.plan(f.standing, "session:"+f.ticket.ActorID); err != nil || p.Channel.Kind != SourceTicket {
		t.Fatalf("with a connection-scoped ticket: glob = %+v, %v", p, err)
	}
	g := newICFixture(t, "session:*")
	a := Actor{ID: g.standing.ActorID, Grants: g.actor.Grants}
	for _, tg := range ListTargets(g.store, a, g.now) {
		if tg.Kind == "session" && (tg.Name == g.ticket.ActorID || tg.Name == sessionLabel(g.ticket)) {
			t.Fatalf("list_targets offers a ticket session a send would refuse: %+v", tg)
		}
	}
}

// A gone ticket session's notice lands in its ticket's channel without
// joining the dead session back.
func TestIntercomNotifyGoneTicketSession(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.Ended(f.ticket); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveInstance(f.ticket.ActorID); err != nil {
		t.Fatal(err)
	}
	m, err := f.ic.Notify(f.ticket, "", "done")
	if err != nil {
		t.Fatal(err)
	}
	if isCurrentMember(f.store, m.Channel, ident.ID(f.ticket.ActorID)) {
		t.Fatal("a gone session was joined back to its ticket")
	}
}
