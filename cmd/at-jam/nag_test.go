package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// recNotifier records the notices the nagger posts (Intercom.Notify's own
// behaviour — as the session, into its chat with its owner — is jam's test).
type recNotifier struct{ got []intercom.Squawk }

func (r *recNotifier) Notify(inst jam.Instance, id, body string) (intercom.Squawk, error) {
	if id == "" {
		id = "n" + string(rune('0'+len(r.got)))
	}
	m := intercom.Squawk{Seq: int64(len(r.got) + 1), ID: id, From: ident.ID(inst.ActorID), Body: body}
	r.got = append(r.got, m)
	return m, nil
}

// fakeRoster is the nagger's roster view: canned rosters and projects. A
// project's ChatService names a connection whose kind is the name itself.
type fakeRoster struct {
	roster   map[string]jam.Roster
	projects map[string]jam.Project
}

func (f *fakeRoster) GetRoster(p string) (jam.Roster, bool) {
	r, ok := f.roster[p]
	return r, ok
}
func (f *fakeRoster) GetProject(name string) (jam.Project, bool) {
	p, ok := f.projects[name]
	return p, ok
}
func (f *fakeRoster) GetConnection(id ident.ID) (jam.Connection, bool) {
	return jam.Connection{ID: id, Kind: string(id), Name: string(id), Status: jam.StatusLive}, true
}

var nagInst = jam.Instance{
	ActorID: "pers-1", Project: "acme", Role: "pair", Owner: "alice",
	SessionKind: jam.SessionKindPersonal, Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting,
}

// Nag and NotifyReclaimed append a squawk from the cove to its owner, stamped
// with the project, through the log's normal append path (id, time and Seq
// assigned like any squawk).
func TestIntercomNaggerAppendsSquawks(t *testing.T) {
	lg := &recNotifier{}
	n := intercomNagger{log: lg, roster: &fakeRoster{}}
	ctx := context.Background()
	if err := n.Nag(ctx, nagInst, 4*time.Hour+29*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := n.NotifyReclaimed(ctx, nagInst, 72*time.Hour+30*time.Minute); err != nil {
		t.Fatal(err)
	}
	got := lg.got
	if len(got) != 2 {
		t.Fatalf("posted %d notices, want 2: %+v", len(got), got)
	}
	if want := "Your personal session pers-1 (pair) has been waiting on you for 4h. Reply to this message to pick it back up, or release it with: at-jam session release pers-1"; got[0].Body != want {
		t.Fatalf("nag body = %q\nwant       %q", got[0].Body, want)
	}
	if want := "Reclaimed your personal session pers-1 (pair) after 3d 0h 30m without a reply."; got[1].Body != want {
		t.Fatalf("reclaim body = %q\nwant         %q", got[1].Body, want)
	}
}

// A nag carries a recognizable id (jam.NagMessageID) so a reply to it can be
// told apart from a reply to anything else the cove sent.
func TestNagCarriesNagID(t *testing.T) {
	lg := &recNotifier{}
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	n := intercomNagger{log: lg, roster: &fakeRoster{}, now: func() time.Time { return at }}
	if err := n.Nag(context.Background(), nagInst, 5*time.Hour); err != nil {
		t.Fatal(err)
	}
	got := lg.got
	if len(got) != 1 || got[0].ID != jam.NagMessageID("pers-1", at) || !jam.IsNagReply(got[0].ID, "pers-1") {
		t.Fatalf("nag = %+v, want id %q", got, jam.NagMessageID("pers-1", at))
	}
}

// The nag offers keep/release only when a reply can prove the owner: the
// project chats over discord and the owner is bound to their Discord user id,
// or (unbound) their discord inbox is theirs alone.
func TestNagOffersKeepReleaseOnlyForUniqueInbox(t *testing.T) {
	const base = "Your personal session pers-1 (pair) has been waiting on you for 5h. Reply to this message to pick it back up, or release it with: at-jam session release pers-1"
	const hint = ` Reply "keep" to keep it, or "release" to end it.`
	disc := func(addr string) []jam.DeliveryProfile {
		return []jam.DeliveryProfile{{Service: "discord", Address: addr}}
	}
	discordProj := map[string]jam.Project{"acme": {Name: "acme", ChatService: "discord"}}
	for name, tc := range map[string]struct {
		store *fakeRoster
		want  string
	}{
		"unique inbox": {&fakeRoster{projects: discordProj, roster: map[string]jam.Roster{"acme": {Humans: []jam.Human{
			{Name: "alice", Delivery: disc("inbox-A")}, {Name: "bob", Delivery: disc("inbox-B")},
		}}}}, base + hint},
		"shared inbox": {&fakeRoster{projects: discordProj, roster: map[string]jam.Roster{"acme": {Humans: []jam.Human{
			{Name: "alice", Delivery: disc("shared")}, {Name: "bob", Delivery: disc("shared")},
		}}}}, base},
		"no discord profile": {&fakeRoster{projects: discordProj, roster: map[string]jam.Roster{"acme": {Humans: []jam.Human{
			{Name: "alice"},
		}}}}, base},
		"not a discord project": {&fakeRoster{roster: map[string]jam.Roster{"acme": {Humans: []jam.Human{
			{Name: "alice", Delivery: disc("inbox-A")},
		}}}}, base},
		"no roster": {&fakeRoster{projects: discordProj}, base},
		"bound owner, shared inbox": {&fakeRoster{projects: discordProj, roster: map[string]jam.Roster{"acme": {Humans: []jam.Human{
			{Name: "alice", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "shared", UserID: "111"}}}, {Name: "bob", Delivery: disc("shared")},
		}}}}, base + hint},
		"bound owner, own inbox": {&fakeRoster{projects: discordProj, roster: map[string]jam.Roster{"acme": {Humans: []jam.Human{
			{Name: "alice", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-A", UserID: "111"}}},
		}}}}, base + hint},
	} {
		t.Run(name, func(t *testing.T) {
			lg := &recNotifier{}
			if err := (intercomNagger{log: lg, roster: tc.store}).Nag(context.Background(), nagInst, 5*time.Hour); err != nil {
				t.Fatal(err)
			}
			if got := lg.got[0].Body; got != tc.want {
				t.Fatalf("nag body = %q\nwant       %q", got, tc.want)
			}
		})
	}
}

// The keep/release confirmations are sent as the cove to its owner, like nags.
func TestNaggerConfirmations(t *testing.T) {
	lg := &recNotifier{}
	n := intercomNagger{log: lg, roster: &fakeRoster{}}
	ctx := context.Background()
	if err := n.NotifyKept(ctx, nagInst, 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := n.NotifyReleased(ctx, nagInst); err != nil {
		t.Fatal(err)
	}
	got := lg.got
	if len(got) != 2 {
		t.Fatalf("posted %d, want 2", len(got))
	}
	for _, m := range got {
		if jam.IsNagReply(m.ID, "pers-1") {
			t.Fatalf("a confirmation is not a nag: %q", m.ID)
		}
	}
	if want := "Keeping your personal session pers-1 (pair). Next reminder in 4h."; got[0].Body != want {
		t.Fatalf("kept body = %q\nwant      %q", got[0].Body, want)
	}
	if want := "Released your personal session pers-1 (pair)."; got[1].Body != want {
		t.Fatalf("released body = %q\nwant          %q", got[1].Body, want)
	}
}

func TestFormatIdle(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second:                 "1m",
		59 * time.Minute:                 "59m",
		4 * time.Hour:                    "4h",
		4*time.Hour + 30*time.Minute:     "4h 30m",
		24 * time.Hour:                   "1d",
		49*time.Hour + 10*time.Second:    "2d 1h",
		72*time.Hour + 30*time.Minute:    "3d 0h 30m",
		4*time.Hour + 29*time.Second:     "4h",
		26*time.Hour + 5*time.Minute + 1: "1d 2h 5m",
	} {
		if got := formatIdle(d); got != want {
			t.Errorf("formatIdle(%v) = %q, want %q", d, got, want)
		}
	}
}

// NotifyEnded tells a personal session's owner it ended itself; an ownerless
// session (standing, ticket) gets no notice.
func TestIntercomNaggerNotifyEnded(t *testing.T) {
	lg := &recNotifier{}
	n := intercomNagger{log: lg, roster: &fakeRoster{}}
	if err := n.NotifyEnded(context.Background(), nagInst, "wrapped up"); err != nil {
		t.Fatal(err)
	}
	if err := n.NotifyEnded(context.Background(), jam.Instance{ActorID: "s1", Project: "acme"}, "x"); err != nil {
		t.Fatal(err)
	}
	got := lg.got
	if len(got) != 1 || got[0].From != "pers-1" || !strings.Contains(got[0].Body, "wrapped up") {
		t.Fatalf("sent %+v; want one notice from pers-1, none for the ownerless session", got)
	}
}
