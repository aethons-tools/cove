package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/relay"
)

func openTestLog(t *testing.T) *intercom.Log {
	t.Helper()
	lg, err := intercom.Open(filepath.Join(t.TempDir(), "intercom.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	return lg
}

var nagInst = harbor.Instance{
	ActorID: "pers-1", Project: "acme", Role: "pair", Owner: "alice",
	SessionKind: harbor.SessionKindPersonal, Phase: harbor.PhaseIdled, Activity: harbor.ActivityWaiting,
}

// Nag and NotifyReclaimed append a squawk from the cove to its owner, stamped
// with the project, through the log's normal append path (id, time and Seq
// assigned like any squawk).
func TestIntercomNaggerAppendsSquawks(t *testing.T) {
	lg := openTestLog(t)
	n := intercomNagger{log: lg}
	ctx := context.Background()
	if err := n.Nag(ctx, nagInst, 4*time.Hour+29*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := n.NotifyReclaimed(ctx, nagInst, 72*time.Hour+30*time.Minute); err != nil {
		t.Fatal(err)
	}
	got := lg.List(intercom.Filter{})
	if len(got) != 2 {
		t.Fatalf("appended %d squawks, want 2: %+v", len(got), got)
	}
	for _, m := range got {
		if m.From != (intercom.Target{Kind: "actor", Ref: "pers-1"}) || len(m.To) != 1 || m.To[0] != (intercom.Target{Kind: "human", Ref: "alice"}) || m.Project != "acme" {
			t.Fatalf("squawk shape = %+v", m)
		}
		if m.ID == "" || m.At.IsZero() {
			t.Fatalf("squawk not prepared (id/at): %+v", m)
		}
	}
	if want := "Your personal session pers-1 (pair) has been waiting on you for 4h. Reply to this message to pick it back up, or release it with: at-harbor session release pers-1"; got[0].Body != want {
		t.Fatalf("nag body = %q\nwant       %q", got[0].Body, want)
	}
	if want := "Reclaimed your personal session pers-1 (pair) after 3d 0h 30m without a reply."; got[1].Body != want {
		t.Fatalf("reclaim body = %q\nwant         %q", got[1].Body, want)
	}
	if got[1].Seq <= got[0].Seq {
		t.Fatalf("Seq not assigned in append order: %d, %d", got[0].Seq, got[1].Seq)
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

// nagRosterStore is a real FileStore with a discord project whose owner alice
// has a discord inbox, holding the personal session's Instance.
func nagRosterStore(t *testing.T) *harbor.FileStore {
	t.Helper()
	st := newTestStore(t)
	if err := st.AddHuman("acme", harbor.Human{Name: "alice", Handle: "alice.h", Delivery: []harbor.DeliveryProfile{{Service: "discord", Address: "inbox-A"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutInstance(nagInst); err != nil {
		t.Fatal(err)
	}
	return st
}

// deliverOverDiscord runs a squawk through the relay's egress steps for the
// discord engine — Resolve by the squawk's own project, then Deliver — and
// returns the posted Discord message id.
func deliverOverDiscord(t *testing.T, dir *directory, surf *discordSurface, m intercom.Squawk) (relay.Delivery, bool) {
	t.Helper()
	d, ok := dir.Resolve("discord", m.Project, m.To[0], m.From)
	if !ok {
		return d, false
	}
	if _, err := surf.Deliver(context.Background(), d, m); err != nil {
		t.Fatal(err)
	}
	return d, true
}

// A nag lands in the owner's Discord inbox; the owner's Discord reply to it is
// routed (via the delivery receipt) back to the cove's inbox as an external
// reply — which is what wakes it.
func TestNagReplyRoutesBackToCove(t *testing.T) {
	st := nagRosterStore(t)
	lg := openTestLog(t)
	rec := mustReceipts(t)
	dir := &directory{store: st, receipts: rec}
	client := &fakeDiscordClient{postID: "D-nag"}
	surf := &discordSurface{dial: func([]string) discordClient { return client }, receipts: rec}

	if err := (intercomNagger{log: lg}).Nag(context.Background(), nagInst, 5*time.Hour); err != nil {
		t.Fatal(err)
	}
	nag := lg.List(intercom.Filter{})[0]
	d, ok := deliverOverDiscord(t, dir, surf, nag)
	if !ok || d.Address != "inbox-A" || len(client.posts) != 1 || client.posts[0].channel != "inbox-A" {
		t.Fatalf("nag not delivered to alice's inbox: %+v %v posts=%+v", d, ok, client.posts)
	}

	// alice replies to the nag in Discord → the ingress steps: Route, Append.
	from, to, replyTo, ok := dir.Route("discord", "acme", relay.Event{Author: "alice", ReplyToForeign: "D-nag", ForeignID: "D-reply", Body: "still here"})
	if !ok || len(to) != 1 || to[0] != (intercom.Target{Kind: "actor", Ref: "pers-1"}) {
		t.Fatalf("reply to a nag not routed to the cove: to=%+v ok=%v", to, ok)
	}
	if _, err := lg.Append(intercom.Squawk{From: from, To: to, Body: "still here", Project: "acme", ReplyTo: replyTo}); err != nil {
		t.Fatal(err)
	}
	inbox := lg.ReadInboxSince(intercom.Target{Kind: "actor", Ref: "pers-1"}, nag.Seq, 0)
	if len(inbox) != 1 || intercom.Classify(inbox[0].From) != intercom.External {
		t.Fatalf("cove inbox after reply = %+v, want one external reply", inbox)
	}
}

// The reclaim notice is appended just before the session is torn down, so the
// relay usually delivers it after the Instance is gone: human delivery must
// resolve from the squawk's project, not the sender's live Instance.
func TestReclaimNoticeDeliversAfterInstanceRemoved(t *testing.T) {
	st := nagRosterStore(t)
	lg := openTestLog(t)
	rec := mustReceipts(t)
	dir := &directory{store: st, receipts: rec}
	client := &fakeDiscordClient{postID: "D-reclaim"}
	surf := &discordSurface{dial: func([]string) discordClient { return client }, receipts: rec}

	if err := (intercomNagger{log: lg}).NotifyReclaimed(context.Background(), nagInst, 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveInstance("pers-1"); err != nil { // teardown deregisters it
		t.Fatal(err)
	}
	if _, ok := st.GetInstance("pers-1"); ok {
		t.Fatal("instance still present")
	}
	notice := lg.List(intercom.Filter{})[0]
	d, ok := deliverOverDiscord(t, dir, surf, notice)
	if !ok || d.Service != "discord" || d.Address != "inbox-A" {
		t.Fatalf("reclaim notice did not resolve to alice's inbox after teardown: %+v %v", d, ok)
	}
	if len(client.posts) != 1 || client.posts[0].content != "pers-1: "+notice.Body {
		t.Fatalf("posted = %+v", client.posts)
	}
}
