package adminui_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// An agent's view carries its runtime, its captured session streams and —
// from the legacy log — the squawks to or from it, newest first, and no one
// else's.
func TestAgentAPIRuntimeSessionAndSquawks(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.PutInstance(jam.Instance{
		ActorID: "sess-1", Project: "acme", Role: "dev", Unit: "COV-9", Owner: "alice", SessionKind: "personal",
		Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Lease: jam.Lease{Holder: "jam-a"},
		Backend: "colima", RaisedAt: now.Add(-time.Hour), LastSeen: now,
		WaitingSince: now.Add(-10 * time.Minute), WaitSeq: 7,
		EscalationTier: 1, TierPingedAt: now.Add(-5 * time.Minute), EscalationCategory: "deploy", Nags: 2,
		Egress: "abcdef1234567890abcdef", EgressFailures: 2,
	}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	l := newIntercomLog(t,
		intercom.LegacySquawk{From: actor("sess-1"), To: []intercom.Target{human("alice")}, Body: "need a decision", At: t0, Project: "acme"},
		intercom.LegacySquawk{From: human("alice"), To: []intercom.Target{actor("sess-1")}, Body: "go ahead", At: t0.Add(time.Minute), Project: "acme"},
		intercom.LegacySquawk{From: human("bob"), To: []intercom.Target{channel("eng")}, Body: "unrelated chatter", At: t0, Project: "acme"},
	)
	st := sessionevents.NewMemStore()
	hub := sessionevents.NewHub()
	ing := sessionevents.NewIngest(st, hub, nil)
	ing.Append("sess-1", sessionevents.Stamp{}, sessIn(1, `{"type":"system","subtype":"init"}`))
	ing.Append("sess-1", sessionevents.Stamp{}, sessIn(2, `{"type":"assistant"}`))
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, l, adminui.WithSessions(st, hub))

	var d adminui.AgentDetail
	if rec := apiDo(t, h, "GET", "/app/admin/api/agents/sess-1", nil, &d); rec.Code != 200 {
		t.Fatalf("agent = %d %s", rec.Code, rec.Body)
	}
	if !d.Running || d.Kind != "personal" || d.Status != "waiting" || d.Owner != "alice" || d.Project != "acme" || d.Role != "dev" || d.Unit != "COV-9" ||
		d.Lease != "jam-a" || d.Backend != "colima" || d.WaitSeq != 7 || d.EscalationTier != 1 || d.EscalationCategory != "deploy" || d.Nags != 2 ||
		d.EgressID != "abcdef123456" || d.EgressFailures != 2 || d.WaitingSince == "" || d.TierPingedAt == "" || d.CanEdit {
		t.Errorf("runtime = %+v", d)
	}
	if !d.SessionsEnabled || len(d.Streams) != 1 || !d.SquawksConfigured {
		t.Errorf("streams = %+v", d.Streams)
	}
	var bodies []string
	for _, s := range d.Squawks {
		bodies = append(bodies, rowText(s))
	}
	if strings.Join(bodies, "|") != "go ahead|need a decision" || d.SquawksMore {
		t.Errorf("squawks = %q more %v", bodies, d.SquawksMore)
	}
}

// A torn-down studio is gone from the registry, but its session streams and
// squawks remain an audit trail: known, not running, no kind.
func TestAgentAPIGoneKeepsAudit(t *testing.T) {
	store := newStore(t)
	l := newIntercomLog(t, intercom.LegacySquawk{From: actor("old-1"), To: []intercom.Target{human("alice")}, Body: "last words", At: time.Now(), Project: "acme"})
	st := sessionevents.NewMemStore()
	hub := sessionevents.NewHub()
	sessionevents.NewIngest(st, hub, nil).Append("old-2", sessionevents.Stamp{}, sessIn(1, `{"type":"assistant"}`))
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, l, adminui.WithSessions(st, hub))
	var d adminui.AgentDetail
	if rec := apiDo(t, h, "GET", "/app/admin/api/agents/old-1", nil, &d); rec.Code != 200 || d.Running || d.HasActor || d.Kind != "" || len(d.Squawks) != 1 {
		t.Errorf("gone studio = %d %+v", rec.Code, d)
	}
	d = adminui.AgentDetail{}
	if rec := apiDo(t, h, "GET", "/app/admin/api/agents/old-2", nil, &d); rec.Code != 200 || len(d.Streams) != 1 || len(d.Squawks) != 0 {
		t.Errorf("streams only = %d %+v", rec.Code, d)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/agents/never-was", nil, nil), 404, "not_found")
}

// On the channel log, an agent's squawks are those in its conversations —
// its ticket's, people's replies there included — and in any channel it has
// posted to; past the cap the view says there are more.
func TestAgentAPIChannelLogSquawks(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := jam.AddPerson(store, "acme", jam.Human{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := store.LookupName(ident.User, "alice")
	acme, _ := store.GetProject("acme")
	tracker, err := store.CreateConnection(jam.Connection{Kind: "linear", Name: "linear"})
	if err != nil {
		t.Fatal(err)
	}
	inst := jam.Instance{ActorID: "sess-2", Project: "acme", Unit: "COV-1", Phase: jam.PhaseLive}
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	lg := intercom.NewMemLog(nil)
	ic := jam.NewIntercom(store, func() (ident.ID, bool) { return tracker.ID, true }, lg, nil, nil)
	if err := ic.SetUp(inst); err != nil {
		t.Fatal(err)
	}
	ticket, _ := ic.HomeChannel(inst)
	other, err := store.CreateChannel(jam.Channel{ProjectID: acme.ID, Kind: jam.SourceRoom, Key: "eng", Label: "eng"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ic.PostTrusted(ticket, intercom.Squawk{From: alice, Body: "a reply on the ticket"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.PostTrusted(other, intercom.Squawk{From: alice, Body: "not for it"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, adminui.NewSquawkReader(store, ic, lg, nil))
	var d adminui.AgentDetail
	apiDo(t, h, "GET", "/app/admin/api/agents/sess-2", nil, &d)
	if len(d.Squawks) != 1 || rowText(d.Squawks[0]) != "a reply on the ticket" || d.Squawks[0].Channel != "COV-1 · ticket" || !d.CanEdit {
		t.Fatalf("squawks = %+v", d.Squawks)
	}
	for i := range 50 {
		if _, err := ic.PostTrusted(ticket, intercom.Squawk{From: alice, Body: fmt.Sprintf("ping %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	// Then it posts to eng: the channel becomes one of its conversations.
	if _, err := ic.PostTrusted(other, intercom.Squawk{From: "sess-2", Body: "joining in"}); err != nil {
		t.Fatal(err)
	}
	d = adminui.AgentDetail{}
	apiDo(t, h, "GET", "/app/admin/api/agents/sess-2", nil, &d)
	if len(d.Squawks) != 50 || !d.SquawksMore {
		t.Errorf("capped squawks = %d more %v", len(d.Squawks), d.SquawksMore)
	}
	found := false
	for _, s := range d.Squawks {
		found = found || rowText(s) == "joining in"
	}
	if !found {
		t.Error("its own post to eng is not among its squawks")
	}
}
