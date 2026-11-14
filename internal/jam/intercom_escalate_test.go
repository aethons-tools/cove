package jam

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

// An escalation tier opens a group of the session and the tier's people (a
// dm for one person) and posts the notice there, naming everyone asked; the
// session's own channel calls nobody in.
func TestIntercomEscalate(t *testing.T) {
	f := newICFixture(t)
	f.standing.Name = "spider"
	for _, inst := range []*Instance{&f.standing, &f.ticket} {
		inst.Activity = ActivityWaiting
		if err := f.store.PutInstance(*inst); err != nil {
			t.Fatal(err)
		}
	}
	alice, _ := MemberOf(f.store, f.project.ID, f.alice.ID)
	bob, _ := MemberOf(f.store, f.project.ID, f.bob.ID)
	if err := f.ic.Escalate(context.Background(), f.standing, 1, "infra", []Member{alice, bob}); err != nil {
		t.Fatal(err)
	}
	group, ok := f.ic.existingChat(f.project, ids(f.standing, f.alice, f.bob))
	if !ok || group.Kind != SourceGroup {
		t.Fatalf("group = %+v, %v", group, ok)
	}
	for _, u := range []ident.ID{f.alice.ID, f.bob.ID} {
		got := f.log.InboxSince(u, 0, 0)
		if len(got) != 1 || got[0].Channel != group.ID || !IsEscalationNotice(got[0].ID) || !IsNotice(got[0].ID) || IsLocalNotice(got[0].ID) ||
			!strings.Contains(got[0].Body, "alice, bob") || !strings.Contains(got[0].Body, "tier 1") || !strings.Contains(got[0].Body, "infra") {
			t.Fatalf("%s's inbox = %+v", u, got)
		}
		if ids := EscalationNoticeTargets(got[0].ID); !slices.Equal(ids, []ident.ID{f.alice.ID, f.bob.ID}) {
			t.Fatalf("notice targets = %v", ids)
		}
	}
	if home, ok := f.ic.HomeChannelIfExists(f.standing); ok {
		for _, u := range []ident.ID{f.alice.ID, f.bob.ID} {
			if isCurrentMember(f.store, home.ID, u) {
				t.Fatalf("%s was called into the session's channel", u)
			}
		}
	}
	if got := f.store.ListChannels(f.project.ID, SourceWork); len(got) != 0 {
		t.Fatalf("a session on no unit posted to a work log: %+v", got)
	}

	// One person: a dm. The next tier reuses it.
	for tier := range 2 {
		if err := f.ic.Escalate(context.Background(), f.standing, tier, "", []Member{alice}); err != nil {
			t.Fatal(err)
		}
	}
	dm, ok := f.ic.existingChat(f.project, ids(f.standing, f.alice))
	if !ok || dm.Kind != SourceDM {
		t.Fatalf("dm = %+v, %v", dm, ok)
	}
	if got := f.log.ChannelSince(dm.ID, 0, 0); len(got) != 2 ||
		!slices.Equal(EscalationNoticeTargets(got[1].ID), []ident.ID{f.alice.ID}) || !strings.Contains(got[1].Body, "tier 1") {
		t.Fatalf("dm = %+v", got)
	}
}

// A session on a unit also posts the notice to the unit's log; that copy
// targets nobody, so push buzzes each person once, in the dm.
func TestIntercomEscalateWorkLog(t *testing.T) {
	f := newICFixture(t)
	f.ticket.Activity = ActivityWaiting
	if err := f.store.PutInstance(f.ticket); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	alice, _ := MemberOf(f.store, f.project.ID, f.alice.ID)
	if err := f.ic.Escalate(context.Background(), f.ticket, 0, "", []Member{alice}); err != nil {
		t.Fatal(err)
	}
	dm, ok := f.ic.existingChat(f.project, ids(f.ticket, f.alice))
	if !ok {
		t.Fatal("no dm")
	}
	inDM := f.log.ChannelSince(dm.ID, 0, 0)
	if len(inDM) != 1 || !slices.Equal(EscalationNoticeTargets(inDM[0].ID), []ident.ID{f.alice.ID}) {
		t.Fatalf("dm = %+v", inDM)
	}
	work, ok := f.ic.TicketChannelOf(f.ticket)
	if !ok {
		t.Fatal("no work log")
	}
	inLog := f.log.ChannelSince(work.ID, 0, 0)
	if len(inLog) != 1 || !IsEscalationNotice(inLog[0].ID) || EscalationNoticeTargets(inLog[0].ID) != nil ||
		inLog[0].Body != inDM[0].Body || inLog[0].From != ident.ID(f.ticket.ActorID) {
		t.Fatalf("work log = %+v", inLog)
	}
	if isCurrentMember(f.store, work.ID, f.alice.ID) {
		t.Fatal("alice was called into the work log")
	}
}

// A session that is gone or no longer waiting is not escalated: nobody is
// called in and no channel is reopened or rejoined.
func TestIntercomEscalateOnlyWaitingSessions(t *testing.T) {
	f := newICFixture(t)
	alice, _ := MemberOf(f.store, f.project.ID, f.alice.ID)
	for name, mutate := range map[string]func(*Instance){
		"gone":        func(i *Instance) { i.Phase = PhaseGone },
		"terminating": func(i *Instance) { i.Phase = PhaseTerminating },
		"running":     func(i *Instance) { i.Activity = ActivityRunning },
	} {
		inst := f.standing
		inst.Activity = ActivityWaiting
		mutate(&inst)
		if err := f.store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		if err := f.ic.Escalate(context.Background(), inst, 0, "", []Member{alice}); err == nil {
			t.Errorf("%s: escalated", name)
		}
	}
	if got := f.store.ListChannels(f.project.ID, SourceSession); len(got) != 0 {
		t.Fatalf("channels made for a non-waiting session: %+v", got)
	}
	if err := f.store.RemoveInstance(f.standing.ActorID); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.Escalate(context.Background(), f.standing, 0, "", []Member{alice}); err == nil {
		t.Error("a removed session was escalated")
	}
}
