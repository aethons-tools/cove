package adminui_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// studioFixture: a waiting personal session sess-1 of acme/dev with an open
// escalation and egress failures, two squawks about it (and one not), and one
// captured session stream.
func studioFixture(t *testing.T, sup *jam.Supervisor) http.Handler {
	t.Helper()
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
	return adminui.Handler(store, testLogger(), sup, nil, anyCred, l, adminui.WithSessions(st, hub))
}

func TestStudioPageShowsRuntime(t *testing.T) {
	rec := get(t, studioFixture(t, &jam.Supervisor{}), "/ui/coves/sess-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("studio page = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<h1 class="mono">sess-1</h1>`, `aria-current="page">Agents`,
		`class="pill phase-live"`, "waiting",
		"personal", "alice", // kind + owner
		`href="/ui/projects/acme"`, `href="/ui/roles/acme/dev"`, "COV-9",
		"jam-a", "colima",
		"wait seq 7",       // wake-on baseline
		"tier 1", "deploy", // open escalation
		"2 nag(s)",                // personal nags
		"abcdef123456",            // egress fingerprint (short)
		`class="pill phase-lost"`, // egress failures flagged
		`hx-delete="/ui/coves/sess-1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("studio page missing %q", want)
		}
	}
}

func TestStudioPageSessionAndSquawks(t *testing.T) {
	body := get(t, studioFixture(t, nil), "/ui/coves/sess-1").Body.String()
	for _, want := range []string{
		fmt.Sprintf(`href="/ui/coves/sess-1/session?stream=%s"`, sessSID), "2 event(s)",
		"need a decision", "go ahead",
		`href="/ui/intercom?participant=sess-1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("studio page missing %q", want)
		}
	}
	if strings.Contains(body, "unrelated chatter") {
		t.Errorf("squawks should be only this studio's")
	}
	if strings.Contains(body, `hx-delete="/ui/coves/sess-1"`) {
		t.Errorf("no supervisor: no teardown")
	}
	if i, j := strings.Index(body, "go ahead"), strings.Index(body, "need a decision"); i > j {
		t.Errorf("squawks should be newest first")
	}
}

// A torn-down studio is gone from the registry, but its session and squawks
// remain as an audit trail.
func TestStudioPageGoneStillShowsAudit(t *testing.T) {
	store := newStore(t)
	l := newIntercomLog(t, intercom.LegacySquawk{From: actor("old-1"), To: []intercom.Target{human("alice")}, Body: "last words", At: time.Now(), Project: "acme"})
	st := sessionevents.NewMemStore()
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, l, adminui.WithSessions(st, sessionevents.NewHub()))
	rec := get(t, h, "/ui/coves/old-1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not running") || !strings.Contains(rec.Body.String(), "last words") {
		t.Fatalf("gone studio = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, h, "/ui/coves/never-was"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", rec.Code)
	}
}

func TestStudiosTableLinksStudioPage(t *testing.T) {
	body := get(t, studioFixture(t, nil), "/ui/coves").Body.String()
	if !strings.Contains(body, `href="/ui/coves/sess-1"`) || !strings.Contains(body, `href="/ui/coves/sess-1/session"`) {
		t.Errorf("studios row should link the studio page and its timeline")
	}
}

// On the channel log, a studio's squawks are those in its conversations: its
// ticket's, including people's replies there.
func TestStudioPageChannelLogSquawks(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := jam.AddPerson(store, "acme", jam.Human{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := store.LookupName(ident.User, "alice")
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
	if _, err := ic.PostTrusted(ticket, intercom.Squawk{From: alice, Body: "a reply on the ticket"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, adminui.NewSquawkReader(store, ic, lg, nil))
	if body := get(t, h, "/ui/coves/sess-2").Body.String(); !strings.Contains(body, "a reply on the ticket") || !strings.Contains(body, "COV-1 · ticket") {
		t.Errorf("studio page lacks its ticket's squawk:\n%s", body)
	}
}
