package userui

import (
	"context"
	"errors"
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

type cmdCall struct {
	id  string
	cmd jam.SessionCommand
	by  string
}

type fakeOps struct {
	torn, upgraded []string
	cmds           []cmdCall
	cmdErr         error
}

func (f *fakeOps) Command(_ context.Context, inst jam.Instance, c jam.SessionCommand, by string) error {
	f.cmds = append(f.cmds, cmdCall{inst.ActorID, c, by})
	return f.cmdErr
}

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
		Raw:   []byte(`{"type":"result","modelUsage":{"claude-x":{"contextWindow":200000}}}`),
		Index: sessionevents.Index{Type: "result", CostUSD: 0.42, InputTokens: 1200, OutputTokens: 300}})
	ctx := `{"type":"assistant","parent_tool_use_id":null,"message":{"model":"claude-x","usage":{"input_tokens":2,"cache_read_input_tokens":84000,"output_tokens":298}}}`
	_ = events.Append(sessionevents.Event{ActorID: personal.ActorID, StreamID: "s", Seq: 2, Kind: sessionevents.KindEvent, Turn: 1, ObservedAt: now,
		Raw: []byte(ctx), Index: sessionevents.DeriveIndex([]byte(ctx))})
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
	for _, want := range []string{`id="session-head"`, "personal", "pair", "proj", "$0.42", "ctx 84k / 200k · 42%", "84,300 of 200,000 tokens", "1h 30m", "is idle", `data-tab="thinking"`, `data-tab="previews"`, `data-tab="artifacts"`, `data-tab="status"`, "Release"} {
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

func TestSessionCommandsMatrix(t *testing.T) {
	e, p, personal, _, ops := sessionEnv(t)
	h := Handler(e.Deps, nil)
	tests := []struct {
		name     string
		phase    jam.Phase
		activity jam.Activity
		cmd      jam.SessionCommand
		want     int
	}{
		{"interrupt running", jam.PhaseLive, jam.ActivityRunning, jam.CmdInterrupt, http.StatusNoContent},
		{"interrupt holding", jam.PhaseLive, jam.ActivityHolding, jam.CmdInterrupt, http.StatusConflict},
		{"interrupt waiting", jam.PhaseLive, jam.ActivityWaiting, jam.CmdInterrupt, http.StatusConflict},
		{"compact running", jam.PhaseLive, jam.ActivityRunning, jam.CmdCompact, http.StatusNoContent},
		{"compact holding", jam.PhaseLive, jam.ActivityHolding, jam.CmdCompact, http.StatusNoContent},
		{"compact waiting", jam.PhaseLive, jam.ActivityWaiting, jam.CmdCompact, http.StatusNoContent},
		{"clear running", jam.PhaseLive, jam.ActivityRunning, jam.CmdClear, http.StatusConflict},
		{"clear holding", jam.PhaseLive, jam.ActivityHolding, jam.CmdClear, http.StatusConflict},
		{"clear waiting", jam.PhaseLive, jam.ActivityWaiting, jam.CmdClear, http.StatusNoContent},
		{"clear blocked", jam.PhaseLive, jam.ActivityBlocked, jam.CmdClear, http.StatusConflict},
		{"clear paused", jam.PhaseIdled, jam.ActivityRunning, jam.CmdClear, http.StatusNoContent},
		{"interrupt paused", jam.PhaseIdled, jam.ActivityRunning, jam.CmdInterrupt, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := personal
			inst.Phase, inst.Activity = tt.phase, tt.activity
			if err := e.st.PutInstance(inst); err != nil {
				t.Fatal(err)
			}
			ops.cmds = nil
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("POST", "/app/user/session/"+inst.ActorID+"/"+string(tt.cmd), nil), p, false))
			if rec.Code != tt.want {
				t.Fatalf("POST %s = %d %q, want %d", tt.cmd, rec.Code, rec.Body.String(), tt.want)
			}
			if tt.want == http.StatusConflict {
				if len(ops.cmds) != 0 {
					t.Errorf("a refused %s was sent: %v", tt.cmd, ops.cmds)
				}
				if want := string(tt.cmd) + " is not available while the session is"; !strings.Contains(rec.Body.String(), want) {
					t.Errorf("409 body = %q, want it to contain %q", rec.Body.String(), want)
				}
				return
			}
			if want := (cmdCall{inst.ActorID, tt.cmd, "alice"}); len(ops.cmds) != 1 || ops.cmds[0] != want {
				t.Errorf("sent %v, want [%v]", ops.cmds, want)
			}
			if rec.Header().Get("HX-Trigger") != "meChanged" {
				t.Errorf("HX-Trigger = %q, want meChanged", rec.Header().Get("HX-Trigger"))
			}
		})
	}
}

// A failed send is a 502 in the other actions' wording.
func TestSessionCommandFailure(t *testing.T) {
	e, p, personal, _, ops := sessionEnv(t)
	ops.cmdErr = errors.New("no live stream")
	h := Handler(e.Deps, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("POST", "/app/user/session/"+personal.ActorID+"/clear", nil), p, false))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "the clear failed; see the jam log") {
		t.Fatalf("failed clear = %d %q", rec.Code, rec.Body.String())
	}
}

// Anyone who can see the session's channel may press; an outsider gets 404.
func TestSessionCommandsAnyViewer(t *testing.T) {
	e, _, personal, _, ops := sessionEnv(t)
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob", Identity: []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "sub-bob"}}}); err != nil {
		t.Fatal(err)
	}
	bobID, _ := e.st.LookupName(ident.User, "bob")
	bob := jam.Participant{Issuer: "https://idp", Subject: "sub-bob", UserID: bobID, Projects: []string{"proj"}, Name: "bob"}
	carol := jam.Participant{UserID: ident.New(ident.User), Name: "carol", Projects: []string{"proj"}}
	h := Handler(e.Deps, nil)
	post := func(who jam.Participant) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("POST", "/app/user/session/"+personal.ActorID+"/compact", nil), who, false))
		return rec.Code
	}
	if c := post(carol); c != http.StatusNotFound {
		t.Errorf("outsider compact = %d, want 404", c)
	}
	if err := e.st.JoinChannel(ident.ID(e.homeOf(t, personal)), bobID, 0); err != nil {
		t.Fatal(err)
	}
	if c := post(bob); c != http.StatusNoContent {
		t.Fatalf("viewer compact = %d, want 204", c)
	}
	if want := (cmdCall{personal.ActorID, jam.CmdCompact, "bob"}); len(ops.cmds) != 1 || ops.cmds[0] != want {
		t.Errorf("sent %v, want [%v]", ops.cmds, want)
	}
}

func TestSessionHeaderCommandButtons(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t) // personal is waiting
	h := Handler(e.Deps, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/session/"+personal.ActorID+"/head", nil), p, false))
	body := rec.Body.String()
	button := func(label string) string {
		end := strings.Index(body, ">"+label+"<")
		if end < 0 {
			t.Fatalf("head has no %s button:\n%s", label, body)
		}
		start := strings.LastIndex(body[:end], "<button")
		return body[start:end]
	}
	if b := button("Interrupt"); !strings.Contains(b, "disabled") || !strings.Contains(b, `/interrupt"`) || !strings.Contains(b, `title="Stop the agent's current turn"`) {
		t.Errorf("Interrupt while waiting = %s, want disabled, posting /interrupt, with its tooltip", b)
	}
	if b := button("Compact"); strings.Contains(b, "disabled") || !strings.Contains(b, `/compact"`) {
		t.Errorf("Compact while waiting = %s, want enabled", b)
	}
	b := button("Clear")
	if strings.Contains(b, "disabled") || !strings.Contains(b, `/clear"`) {
		t.Errorf("Clear while waiting = %s, want enabled", b)
	}
	if want := `hx-confirm="Clear this session's conversation? The agent keeps its files and memory but forgets the conversation."`; !strings.Contains(b, want) {
		t.Errorf("Clear = %s, want %s", b, want)
	}
}

func TestKTokens(t *testing.T) {
	for n, want := range map[int64]string{950: "950", 84300: "84k", 199600: "200k", 1_000_000: "1M", 1_250_000: "1.2M"} {
		if got := kTokens(n); got != want {
			t.Errorf("kTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestContextUse(t *testing.T) {
	at := time.Unix(1000, 0)
	for _, c := range []struct {
		t    sessionevents.Totals
		want string
	}{
		{sessionevents.Totals{}, ""},
		{sessionevents.Totals{LastAt: at, Context: 950, ContextModel: "m"}, "ctx 950"},
		{sessionevents.Totals{LastAt: at, Context: 950, ContextWindow: 1_000_000, ContextModel: "m"}, "ctx 950 / 1M · 0%"},
		{sessionevents.Totals{LastAt: at, ContextModel: "m"}, "ctx —"}, // reset since the last call
	} {
		if got, _ := contextUse(c.t); got != c.want {
			t.Errorf("contextUse(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}

// The header shows the session's mission under its name, and nothing when
// it has none.
func TestSessionHeaderMission(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	h := Handler(e.Deps, nil)
	head := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/?c="+e.homeOf(t, personal), nil), p, false))
		return rec.Body.String()
	}
	if body := head(); strings.Contains(body, `class="sh-mission"`) {
		t.Error("a session without a mission shows a mission line")
	}
	personal.Mission = "triaging ACME-7"
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	if want := `<div class="sh-mission" title="triaging ACME-7">triaging ACME-7</div>`; !strings.Contains(head(), want) {
		t.Errorf("header missing %s", want)
	}
}
