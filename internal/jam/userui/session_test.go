package userui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

type fakeOps struct{ torn, upgraded []string }

func (f *fakeOps) Teardown(_ context.Context, id string) error {
	f.torn = append(f.torn, id)
	return nil
}
func (f *fakeOps) Upgrade(inst jam.Instance) (string, error) {
	f.upgraded = append(f.upgraded, inst.ActorID)
	return "queued", nil
}
func (f *fakeOps) UpgradeState(jam.Instance) string { return "" }

// sessionEnv adds sessions with home channels alice is in: her personal
// session and a standing one; ops and events are fakes.
func sessionEnv(t *testing.T) (*env, jam.Participant, jam.Instance, jam.Instance, *fakeOps) {
	t.Helper()
	e, p := fixture()
	ops := &fakeOps{}
	events := sessionevents.NewMemStore()
	e.Deps.Ops, e.Deps.Totals = ops, sessionevents.NewTotalsCache(events)
	now := time.Now()
	personal := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "proj", Role: "pair", SessionKind: jam.SessionKindPersonal,
		OwnerID: p.UserID, Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, RaisedAt: now.Add(-90 * time.Minute), TurnEndedAt: now.Add(-5 * time.Minute)}
	standing := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "proj", Role: "dev", SessionKind: jam.SessionKindStanding,
		Name: "spider", Phase: jam.PhaseLive, Activity: jam.ActivityRunning, RaisedAt: now.Add(-time.Hour)}
	for _, inst := range []jam.Instance{personal, standing} {
		if err := e.st.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		ch, err := e.Intercom.HomeChannel(inst)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.JoinChannel(ch.ID, p.UserID, 0); err != nil {
			t.Fatal(err)
		}
	}
	_ = events.Append(sessionevents.Event{ActorID: personal.ActorID, StreamID: "s", Seq: 1, Kind: sessionevents.KindEvent, Turn: 1, ObservedAt: now,
		Index: sessionevents.Index{Type: "result", CostUSD: 0.42, InputTokens: 1200, OutputTokens: 300}})
	return e, p, personal, standing, ops
}

func asPrincipal(r *http.Request, p jam.Participant, admin bool) *http.Request {
	r = jam.WithParticipant(r, p)
	pr := appui.Principal{User: &p, Admin: admin}
	if admin {
		pr.Operator = jam.Operator{ID: "op"}
	}
	return appui.WithPrincipal(r, pr)
}

func (e *env) homeOf(t *testing.T, inst jam.Instance) string {
	t.Helper()
	ch, err := e.Intercom.HomeChannel(inst)
	if err != nil {
		t.Fatal(err)
	}
	return string(ch.ID)
}

func TestSessionHeader(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	h := Handler(e.Deps, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/?c="+e.homeOf(t, personal), nil), p, false))
	body := rec.Body.String()
	for _, want := range []string{`id="session-head"`, "personal", "pair", "proj", "$0.42", "1h 30m", "is idle", `data-tab="previews"`, `data-tab="artifacts"`, `data-tab="status"`, "Release"} {
		if !strings.Contains(body, want) {
			t.Errorf("header missing %q", want)
		}
	}
	for _, gone := range []string{">Restart<", ">Upgrade<"} {
		if strings.Contains(body, gone) {
			t.Errorf("a member sees %q", gone)
		}
	}
	// A room has no session header.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/?c="+engID, nil), p, false))
	if strings.Contains(rec.Body.String(), `id="session-head"`) {
		t.Error("a room shows a session header")
	}
}

func TestSessionActionsPermissions(t *testing.T) {
	e, p, personal, standing, ops := sessionEnv(t)
	h := Handler(e.Deps, nil)
	post := func(inst jam.Instance, action string, admin bool) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("POST", "/app/user/session/"+inst.ActorID+"/"+action, nil), p, admin))
		return rec.Code
	}
	if c := post(standing, "restart", false); c != http.StatusForbidden {
		t.Fatalf("member restart = %d", c)
	}
	if c := post(standing, "upgrade", false); c != http.StatusForbidden {
		t.Fatalf("member upgrade = %d", c)
	}
	if c := post(standing, "release", true); c != http.StatusForbidden {
		t.Fatalf("release a standing session = %d", c)
	}
	if c := post(personal, "release", false); c != http.StatusNoContent || len(ops.torn) != 1 || ops.torn[0] != personal.ActorID {
		t.Fatalf("owner release = %d %v", c, ops.torn)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("POST", "/app/user/session/"+standing.ActorID+"/restart", nil), p, true))
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Error("ending a session should reload the page (its head would 404)")
	}
	if c := post(standing, "restart", true); c != http.StatusNoContent || ops.torn[len(ops.torn)-1] != standing.ActorID {
		t.Fatalf("operator restart = %d %v", c, ops.torn)
	}
	if c := post(standing, "upgrade", true); c != http.StatusNoContent || len(ops.upgraded) != 1 {
		t.Fatalf("operator upgrade = %d %v", c, ops.upgraded)
	}
	if c := post(jam.Instance{ActorID: string(ident.New(ident.Session))}, "restart", true); c != http.StatusNotFound {
		t.Fatalf("no such session = %d", c)
	}
	// An operator's header shows Restart and Upgrade on the standing session.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/session/"+standing.ActorID+"/head", nil), p, true))
	for _, want := range []string{">Restart<", ">Upgrade<", "spider", "standing"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("operator head missing %q", want)
		}
	}
}

// Someone who can't see the session's conversation gets nothing.
func TestSessionHiddenFromOutsiders(t *testing.T) {
	e, _, personal, _, _ := sessionEnv(t)
	bob := jam.Participant{UserID: ident.New(ident.User), Name: "bob", Projects: []string{"proj"}}
	h := Handler(e.Deps, nil)
	for _, path := range []string{"/head", "/release"} {
		rec := httptest.NewRecorder()
		method := "GET"
		if path == "/release" {
			method = "POST"
		}
		h.ServeHTTP(rec, asPrincipal(httptest.NewRequest(method, "/app/user/session/"+personal.ActorID+path, nil), bob, false))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s by an outsider = %d", method, path, rec.Code)
		}
	}
}

// A ticket session's home is its ticket channel: the head shows there, not in
// its own session channel (where the head's routes would resolve elsewhere).
func TestSessionHeadOnlyInHome(t *testing.T) {
	e, p, _, _, _ := sessionEnv(t)
	e.Deps.Intercom = jam.NewIntercom(e.st, func() (ident.ID, bool) { return "linear", true }, e.lg, nil, nil) // a tracker: ticket channels
	inst := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "proj", Role: "dev", Unit: "COV-1", Phase: jam.PhaseLive, RaisedAt: time.Now()}
	if err := e.st.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	proj, _ := e.st.GetProject("proj")
	ticket, err := e.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: proj.ID, Kind: jam.SourceTicket, Key: "linear/COV-1", Label: "COV-1"})
	if err != nil {
		t.Fatal(err)
	}
	own, err := e.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: proj.ID, Kind: jam.SourceSession, Key: inst.ActorID, Label: "own"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []ident.ID{ticket.ID, own.ID} {
		if err := e.st.JoinChannel(ch, p.UserID, 0); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler(e.Deps, nil)
	get := func(ch ident.ID) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/?c="+string(ch), nil), p, false))
		return rec.Body.String()
	}
	if !strings.Contains(get(ticket.ID), `id="session-head"`) {
		t.Error("no head in the ticket channel")
	}
	if strings.Contains(get(own.ID), `id="session-head"`) {
		t.Error("a head in the session's own channel, which isn't its home")
	}
}
