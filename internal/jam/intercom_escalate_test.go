package jam

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

// An escalation tier calls its people into the session's home channel and
// posts the notice there, naming everyone asked — once per tier.
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
	home := f.sessionChannel(f.standing)
	for _, u := range []ident.ID{f.alice.ID, f.bob.ID} {
		if !isCurrentMember(f.store, home.ID, u) {
			t.Fatalf("%s not called in", u)
		}
		got := f.log.InboxSince(u, 0, 0)
		if len(got) != 1 || !IsEscalationNotice(got[0].ID) || !IsNotice(got[0].ID) || IsLocalNotice(got[0].ID) ||
			!strings.Contains(got[0].Body, "alice, bob") || !strings.Contains(got[0].Body, "tier 1") || !strings.Contains(got[0].Body, "infra") {
			t.Fatalf("%s's inbox = %+v", u, got)
		}
		if ids := EscalationNoticeTargets(got[0].ID); !slices.Equal(ids, []ident.ID{f.alice.ID, f.bob.ID}) {
			t.Fatalf("notice targets = %v", ids)
		}
	}
	// A ticket session's home is its ticket channel.
	if err := f.ic.Escalate(context.Background(), f.ticket, 0, "", []Member{alice}); err != nil {
		t.Fatal(err)
	}
	ticket, _ := f.ic.TicketChannelOf(f.ticket)
	if !isCurrentMember(f.store, ticket.ID, f.alice.ID) {
		t.Fatal("alice is called into the ticket's conversation")
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
