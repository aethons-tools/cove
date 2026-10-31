package userui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// The package's shared test fixture: a project, a room and a viewer over
// real in-memory stores, and the fakes the handler's sources take.

// engID is the fixture room's channel id.
const engID = "chn_01j9q3eeeeeeeeeeeeeeeeeeee"

// env is the user area over a real store and channel log: project proj with
// alice (the viewer, bound to an OIDC identity), a room eng, and alice's
// "hi from alice" in it.
type env struct {
	Deps
	st    *jam.MemStore
	lg    *intercom.Log
	alice jam.User
	room  jam.Channel
}

var fixtureAt = time.Date(2026, 9, 28, 14, 3, 0, 0, time.UTC)

// post appends body from a participant into the room (trusted, as relay
// ingress does), at the fixture time.
func (e *env) post(from ident.ID, body, contentType string) intercom.Squawk {
	m, err := e.Intercom.PostTrusted(e.room, intercom.Squawk{From: from, Body: body, ContentType: contentType, At: fixtureAt})
	if err != nil {
		panic(err)
	}
	return m
}

func fixture() (*env, jam.Participant) {
	st := jam.NewMemStore()
	if err := st.CreateProject("proj"); err != nil {
		panic(err)
	}
	if err := jam.AddPerson(st, "proj", jam.Human{Name: "alice", Identity: []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "sub-alice"}}}); err != nil {
		panic(err)
	}
	proj, _ := st.GetProject("proj")
	aliceID, _ := st.LookupName(ident.User, "alice")
	alice, _ := st.GetUser(aliceID)
	room, err := st.CreateChannel(jam.Channel{ID: engID, ProjectID: proj.ID, Kind: jam.SourceRoom, Key: "eng", Label: "eng"})
	if err != nil {
		panic(err)
	}
	lg := intercom.NewMemLog(nil)
	ic := jam.NewIntercom(st, func() (ident.ID, bool) { return "", false }, lg, nil, nil)
	e := &env{Deps: Deps{Store: st, Intercom: ic, Log: lg}, st: st, lg: lg, alice: alice, room: room}
	e.post(alice.ID, "hi from alice", "")
	p := jam.Participant{Issuer: "https://idp", Subject: "sub-alice", UserID: alice.ID, Projects: []string{"proj"}, Name: "alice"}
	return e, p
}

const askerID = ident.ID("ses_01j9q3ffffffffffffffffffff")

// ask posts a session's ask into the room (trusted, as the store folds it).
func (e *env) ask(ev intercom.AskEvent) intercom.Squawk {
	ev.Event = intercom.AskEventAsk
	m, err := e.Intercom.PostTrusted(e.room, intercom.Squawk{From: askerID, Body: "asked", ContentType: intercom.ContentPlain, Parts: []intercom.Part{intercom.AskPart(ev)}})
	if err != nil {
		panic(err)
	}
	return m
}

var yesNoOpts = []intercom.AskOption{{ID: "yes", Label: "Ship it"}, {ID: "no", Label: "Hold", Detail: "wait for CI"}}

type cmdCall struct {
	id  string
	cmd jam.SessionCommand
	by  string
}

type fakeOps struct {
	torn, upgraded, restarted []string
	cmds                      []cmdCall
	cmdErr                    error
}

func (f *fakeOps) Command(_ context.Context, inst jam.Instance, c jam.SessionCommand, by string) error {
	f.cmds = append(f.cmds, cmdCall{inst.ActorID, c, by})
	return f.cmdErr
}

func (f *fakeOps) Teardown(_ context.Context, id string) error {
	f.torn = append(f.torn, id)
	return nil
}

func (f *fakeOps) Restart(_ context.Context, id string) error {
	f.restarted = append(f.restarted, id)
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

// ticketSession adds a ticket session (COV-1) whose home is its ticket
// channel, with a session channel of its own besides; p is in both.
func ticketSession(t *testing.T, e *env, p jam.Participant) (inst jam.Instance, ticket, own ident.ID) {
	t.Helper()
	e.Deps.Intercom = jam.NewIntercom(e.st, func() (ident.ID, bool) { return "linear", true }, e.lg, nil, nil) // a tracker: ticket channels
	inst = jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "proj", Role: "dev", Unit: "COV-1", Phase: jam.PhaseLive, RaisedAt: time.Now()}
	if err := e.st.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	proj, _ := e.st.GetProject("proj")
	tc, err := e.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: proj.ID, Kind: jam.SourceTicket, Key: "linear/COV-1", Label: "COV-1"})
	if err != nil {
		t.Fatal(err)
	}
	oc, err := e.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: proj.ID, Kind: jam.SourceSession, Key: inst.ActorID, Label: "own"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []ident.ID{tc.ID, oc.ID} {
		if err := e.st.JoinChannel(ch, p.UserID, 0); err != nil {
			t.Fatal(err)
		}
	}
	return inst, tc.ID, oc.ID
}

// fakeFrames records what it was asked to mint.
type fakeFrames struct {
	calls []frameCall
	err   error
}

type frameCall struct {
	p                      collab.Principal
	display, actorID, name string
	right                  collab.Right
}

func (f *fakeFrames) FrameURL(p collab.Principal, display, actorID, name string, right collab.Right) (string, error) {
	f.calls = append(f.calls, frameCall{p, display, actorID, name, right})
	if f.err != nil {
		return "", f.err
	}
	return "https://" + name + "--" + actorID + ".preview.test/__jam/frame?t=tk", nil
}

// frameEnv is sessionEnv with previews and a fake Frames on; the personal
// session (alice's: preview and ssh) exposes app, and the standing one's
// role grants members preview only.
func frameEnv(t *testing.T) (*env, jam.Participant, jam.Instance, jam.Instance, *fakeFrames) {
	t.Helper()
	e, p, personal, standing, _ := sessionEnv(t)
	ff := &fakeFrames{}
	e.Deps.Frames = ff
	e.Deps.PreviewURL = func(actorID, name string) (string, error) {
		return "https://" + name + "--" + actorID + ".preview.test/", nil
	}
	personal.Exposures = []jam.Exposure{{Name: "app", Port: 5173}, {Name: "ide", Port: 4000}}
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	proj, _ := e.st.GetProject("proj")
	if err := e.st.PutRole(string(proj.ID), jam.Role{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	return e, p, personal, standing, ff
}

var thinkStream = strings.Repeat("a", 32)

// thinkEnv is sessionEnv with an events store and hub wired into Deps.
func thinkEnv(t *testing.T) (*env, jam.Participant, jam.Instance, *sessionevents.MemStore, *sessionevents.Hub) {
	t.Helper()
	e, p, personal, _, _ := sessionEnv(t)
	store, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	e.Deps.Events, e.Deps.Hub = store, hub
	return e, p, personal, store, hub
}

// textEvent is an assistant text block.
func textEvent(actor string, seq uint64, text string) sessionevents.Event {
	raw, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}}}})
	return sessionevents.Event{ActorID: actor, StreamID: thinkStream, Seq: seq, Kind: sessionevents.KindEvent,
		ObservedAt: time.Unix(1000+int64(seq), 0), Raw: raw}
}

func appendText(t *testing.T, st sessionevents.Store, actor string, from, to uint64) {
	t.Helper()
	for s := from; s <= to; s++ {
		if err := st.Append(textEvent(actor, s, fmt.Sprintf("said %d", s))); err != nil {
			t.Fatal(err)
		}
	}
}

// asWho wraps h so every request comes from p.
func asWho(h http.Handler, p jam.Participant) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, asPrincipal(r, p, false)) })
}

func thinkGet(h http.Handler, p jam.Participant, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", path, nil), p, false))
	return rec
}

// sseLines reads an SSE body line by line onto a channel (closed at EOF).
func sseLines(body io.Reader) <-chan string {
	out := make(chan string, 64)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			out <- sc.Text()
		}
	}()
	return out
}

// sseRead collects lines into buf until want appears and its message ends
// (or the deadline).
func sseRead(t *testing.T, lines <-chan string, buf *strings.Builder, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !strings.Contains(buf.String(), want) || !strings.HasSuffix(buf.String(), "\n\n") {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before %q:\n%s", want, buf.String())
			}
			buf.WriteString(l + "\n")
		case <-deadline:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

// isIn reports whether p is a current member of ch.
func isIn(st *jam.MemStore, ch, p ident.ID) bool {
	for _, m := range st.ChannelMembers(ch) {
		if m.ParticipantID == p && !m.Left {
			return true
		}
	}
	return false
}
