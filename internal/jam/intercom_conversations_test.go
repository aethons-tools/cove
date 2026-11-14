package jam

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// The conversation model's interface (COV-346): units of work and their
// workers, the work:/worker: targets, session: as a dm, session members in
// chat:, and the addressing exemptions.

func (f *icFixture) workLog() Channel {
	f.t.Helper()
	ch, ok := f.store.ChannelByKey(f.project.ID, SourceWork, string(f.tracker.ID)+"/ACME-7")
	if !ok {
		f.t.Fatal("no work log for ACME-7")
	}
	return ch
}

func TestIntercomWorkLogSwapsWorker(t *testing.T) {
	f := newICFixture(t)
	f.ticket.RaisedAt = time.Unix(1000, 0)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if ch := f.workLog(); ch.Worker != ident.ID(f.ticket.ActorID) || !isMember(f.store, ch.ID, ident.ID(f.ticket.ActorID)) {
		t.Fatalf("work log = %+v, want the ticket session as its worker and member", ch)
	}
	// A person follows the unit; a new session takes it over while the old
	// one still lives.
	if err := f.store.JoinChannel(f.workLog().ID, f.alice.ID, 11); err != nil {
		t.Fatal(err)
	}
	next := f.ticket
	next.ActorID = string(ident.New(ident.Session))
	next.RaisedAt = time.Unix(2000, 0)
	if err := f.store.PutInstance(next); err != nil {
		t.Fatal(err)
	}
	f.tail = 20
	if err := f.ic.SetUp(next); err != nil {
		t.Fatal(err)
	}
	ch := f.workLog()
	if ch.Worker != ident.ID(next.ActorID) {
		t.Fatalf("worker = %q, want the new session", ch.Worker)
	}
	var members []ident.ID
	for _, m := range f.store.ChannelMembers(ch.ID) {
		members = append(members, m.ParticipantID)
	}
	if !slices.Equal(members, ids(f.alice, next)) {
		t.Fatalf("members = %v, want the follower and the new worker only", members)
	}
	// The old session set up again (a restart's Reconcile) doesn't take the
	// unit back from the newer one, but may still post to its log.
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if ch := f.workLog(); ch.Worker != ident.ID(next.ActorID) || isMember(f.store, ch.ID, ident.ID(f.ticket.ActorID)) {
		t.Fatalf("after the old session's SetUp: %+v", ch)
	}
	if p := f.mustPlan(f.ticket, "work:ACME-7"); p.Channel.ID != ch.ID {
		t.Fatalf("own log = %+v", p)
	}
	// The worker ends: the unit has none until it is dispatched again.
	if err := f.ic.Ended(next); err != nil {
		t.Fatal(err)
	}
	if ch := f.workLog(); ch.Worker != "" || isMember(f.store, ch.ID, ident.ID(next.ActorID)) {
		t.Fatalf("after the worker ended: %+v", ch)
	}
}

func TestIntercomPlanWorkTargets(t *testing.T) {
	f := newICFixture(t, "ticket:ACME-*", "session:*")
	if _, err := f.plan(f.standing, "worker:ACME-7"); !errors.Is(err, ErrNoWorker) {
		t.Fatalf("worker: of a unit with no log = %v, want ErrNoWorker", err)
	}
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	log := f.mustPlan(f.standing, "ticket:ACME-7")
	if work := f.mustPlan(f.standing, "work:ACME-7"); work.Channel.ID != log.Channel.ID || work.Channel.Kind != SourceWork {
		t.Fatalf("work: = %+v, want the log ticket: names", work)
	}
	if byConn := f.mustPlan(f.standing, "work:linear/ACME-7"); byConn.Channel.ID != log.Channel.ID {
		t.Fatal("work:<connection>/<key> names the same log")
	}
	dm := f.mustPlan(f.standing, "worker:ACME-7")
	if dm.Channel.Kind != SourceDM || !slices.Equal(dm.Audience, ids(f.ticket)) {
		t.Fatalf("worker: = %+v, want a dm with the worker", dm)
	}
	if isMember(f.store, log.Channel.ID, ident.ID(f.standing.ActorID)) {
		t.Fatal("a dm with the worker doesn't join its log")
	}
	if again := f.mustPlan(f.standing, "session:"+f.ticket.ActorID); again.Channel.ID != dm.Channel.ID {
		t.Fatal("worker: and session: of the same session are one dm")
	}
	if _, err := f.plan(f.ticket, "worker:ACME-7"); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("the worker addressing itself = %v, want ErrSendUnresolved", err)
	}
	if _, err := f.plan(f.standing, "worker:ACME-9"); !errors.Is(err, ErrNoWorker) {
		t.Fatalf("worker: of an unworked unit = %v, want ErrNoWorker", err)
	}
	if err := f.ic.Ended(f.ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plan(f.standing, "worker:ACME-7"); !errors.Is(err, ErrNoWorker) {
		t.Fatalf("worker: after the worker ended = %v, want ErrNoWorker", err)
	}

	// work: and worker: are authorized by ticket: globs, checked first.
	for _, addr := range []string{"work:OTHER-1", "worker:OTHER-1", "work:", "worker:"} {
		if _, err := f.plan(f.standing, addr); !errors.Is(err, ErrSendDenied) {
			t.Errorf("Plan(%q) = %v, want ErrSendDenied", addr, err)
		}
	}
}

func TestIntercomPlanSessionIsDM(t *testing.T) {
	f := newICFixture(t, "session:*")
	p := f.mustPlan(f.personal, "session:"+f.standing.ActorID)
	if p.Channel.Kind != SourceDM || !slices.Equal(p.Audience, ids(f.standing)) {
		t.Fatalf("session: = %+v, want a dm with that session", p)
	}
	back := f.mustPlan(f.standing, "session:"+f.personal.ActorID)
	if back.Channel.ID != p.Channel.ID {
		t.Fatal("one dm for each pair, either way")
	}
	if own := f.mustPlan(f.standing, "session:"+f.standing.ActorID); own.Channel.Kind != SourceSession {
		t.Fatalf("session:<itself> = %+v, want its own channel", own)
	}
}

func TestIntercomPlanChatWithSessions(t *testing.T) {
	f := newICFixture(t, "user:*", "session:*")
	p := f.mustPlan(f.personal, "chat:user:alice,session:"+f.standing.ActorID)
	if p.Channel.Kind != SourceGroup || !slices.Equal(p.Audience, ids(f.alice, f.standing)) {
		t.Fatalf("chat with a session = %+v", p)
	}
	if dm := f.mustPlan(f.personal, "chat:session:"+f.standing.ActorID); dm.Channel.Kind != SourceDM {
		t.Fatalf("chat of one session = %+v, want a dm", dm)
	}
	if _, err := f.plan(f.personal, "chat:session:nobody"); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("unknown session = %v", err)
	}
	narrow := newICFixture(t, "user:*")
	if _, err := narrow.plan(narrow.personal, "chat:user:alice,session:"+narrow.standing.ActorID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a session member needs session: addressing: %v", err)
	}
}

func TestIntercomPersonalSessionReachesItsStarter(t *testing.T) {
	f := newICFixture(t) // no addressing
	p := f.mustPlan(f.personal, "user:alice")
	if !slices.Equal(p.Audience, ids(f.alice)) {
		t.Fatalf("user:<starter> = %+v", p)
	}
	if _, err := f.plan(f.personal, "user:bob"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("user:bob = %v, want denied", err)
	}
	if _, err := f.plan(f.standing, "user:alice"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a session alice didn't start = %v, want denied", err)
	}
}

func TestIntercomReplyNeedsNoAddressing(t *testing.T) {
	f := newICFixture(t) // no addressing
	if _, err := f.plan(f.standing, "user:bob"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("before any message = %v, want denied", err)
	}
	dm, err := f.ic.chat(f.project, []ident.ID{f.bob.ID, ident.ID(f.standing.ActorID)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.PostTrusted(dm, intercom.Squawk{From: f.bob.ID, Body: "ping"}); err != nil {
		t.Fatal(err)
	}
	if p := f.mustPlan(f.standing, "user:bob"); p.Channel.ID != dm.ID {
		t.Fatalf("reply = %+v, want the dm it was delivered in", p)
	}
	if _, err := f.plan(f.standing, "chat:user:alice,user:bob"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("another conversation = %v, want denied", err)
	}

	// The same for a work log it isn't the worker of.
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plan(f.standing, "work:ACME-7"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("work log before delivery = %v", err)
	}
	if _, err := f.ic.PostTrusted(f.workLog(), intercom.Squawk{From: ident.ID(f.ticket.ActorID), Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.JoinChannel(f.workLog().ID, ident.ID(f.standing.ActorID), f.tail); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.PostTrusted(f.workLog(), intercom.Squawk{From: f.alice.ID, Body: "for you"}); err != nil {
		t.Fatal(err)
	}
	if p := f.mustPlan(f.standing, "work:ACME-7"); p.Channel.ID != f.workLog().ID {
		t.Fatalf("reply in the log = %+v", p)
	}
}

func TestListTargetsWork(t *testing.T) {
	f := newICFixture(t, "ticket:ACME-*")
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceWork, Key: string(f.tracker.ID) + "/ACME-9", Label: "ACME-9"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceWork, Key: string(f.tracker.ID) + "/OPS-1", Label: "OPS-1"}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tg := range ListTargets(f.store, f.ic.Tracker, f.actor, f.now) {
		got = append(got, tg.Kind+":"+tg.Name)
	}
	slices.Sort(got)
	// ACME-9 has no worker; OPS-1 isn't in the addressing.
	if want := []string{"work:ACME-7", "work:ACME-9", "worker:ACME-7"}; !slices.Equal(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	// The worker itself isn't offered its own worker: target.
	self := f.actor
	self.ID = f.ticket.ActorID
	for _, tg := range ListTargets(f.store, f.ic.Tracker, self, f.now) {
		if tg.Kind == "worker" && tg.Name == "ACME-7" {
			t.Fatalf("the worker is offered worker:<its own key>: %+v", tg)
		}
	}
}
