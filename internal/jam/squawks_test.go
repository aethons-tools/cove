package jam

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// sqFixture is the /squawks handler over the intercom fixture: each session
// is enrolled as an actor whose bearer is "tok-" + its id.
type sqFixture struct {
	*icFixture
	h      *SquawksHandler
	legacy *intercom.LegacyLog
	logbuf *bytes.Buffer
}

func newSqFixture(t *testing.T, addressing ...string) *sqFixture {
	t.Helper()
	f := &sqFixture{icFixture: newICFixture(t, addressing...), legacy: intercom.NewLegacyMemLog(), logbuf: &bytes.Buffer{}}
	for _, inst := range []Instance{f.ticket, f.personal, f.standing} {
		a := f.actor
		a.ID, a.TokenHash = inst.ActorID, HashToken("tok-"+inst.ActorID)
		if err := f.store.AddActor(a); err != nil {
			t.Fatal(err)
		}
	}
	f.h = NewSquawksHandler(f.store, f.ic, f.log, f.legacy, slog.New(slog.NewTextHandler(f.logbuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return f
}

func (f *sqFixture) do(method, path string, inst Instance, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if inst.ActorID != "" {
		req.Header.Set("Authorization", "Bearer tok-"+inst.ActorID)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

type sendResp struct {
	ID      string `json:"id"`
	Channel Party  `json:"channel"`
}

func (f *sqFixture) send(inst Instance, body string) (int, sendResp) {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/squawks", inst, body)
	var r sendResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			f.t.Fatalf("decode send: %v", err)
		}
	}
	return rec.Code, r
}

type readResp struct {
	Squawks         []Squawk `json:"squawks"`
	CommittedCursor string   `json:"committed_cursor"`
	PageFirst       string   `json:"page_first"`
	PageLast        string   `json:"page_last"`
}

func (f *sqFixture) read(inst Instance, query string) readResp {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/squawks?"+query, inst, "")
	if rec.Code != http.StatusOK {
		f.t.Fatalf("read %q = %d %s", query, rec.Code, rec.Body)
	}
	var r readResp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		f.t.Fatalf("decode read: %v", err)
	}
	return r
}

func bodiesOf(sq []Squawk) []string {
	out := make([]string, len(sq))
	for i, s := range sq {
		out[i] = s.Body
	}
	return out
}

// deliver posts body from a person into the session's default channel.
func (f *sqFixture) reply(inst Instance, from ident.ID, body string) intercom.Squawk {
	f.t.Helper()
	ch, err := f.ic.HomeChannel(inst)
	if err != nil {
		f.t.Fatal(err)
	}
	m, err := f.ic.PostTrusted(ch, intercom.Squawk{From: from, Body: body})
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func TestSquawksAuth(t *testing.T) {
	f := newSqFixture(t)
	if rec := f.do(http.MethodGet, "/squawks", Instance{}, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/squawks", Instance{ActorID: "nobody"}, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d", rec.Code)
	}
	if err := f.store.RemoveInstance(f.standing.ActorID); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(http.MethodGet, "/squawks", f.standing, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no instance = %d", rec.Code)
	}
	if rec := f.do(http.MethodDelete, "/squawks", f.ticket, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/squawks/commit", f.ticket, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET commit = %d", rec.Code)
	}
}

func TestSquawksSendValidation(t *testing.T) {
	f := newSqFixture(t)
	for body, want := range map[string]int{
		`{"body":""}`: http.StatusBadRequest,
		`not json`:    http.StatusBadRequest,
		`{"body":"x","content_type":"text/html"}`:                    http.StatusBadRequest,
		`{"body":"` + strings.Repeat("x", maxSquawkBodyBytes) + `"}`: http.StatusRequestEntityTooLarge,
	} {
		if code, _ := f.send(f.ticket, body); code != want {
			t.Errorf("send %.40q = %d, want %d", body, code, want)
		}
	}
	unconfigured := NewSquawksHandler(f.store, nil, nil, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	f.h = unconfigured
	if code, _ := f.send(f.ticket, `{"body":"x"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("send with no log = %d", code)
	}
	if rec := f.do(http.MethodGet, "/squawks", f.ticket, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("read with no log = %d", rec.Code)
	}
}

func TestSquawksSendDefaults(t *testing.T) {
	f := newSqFixture(t)
	code, r := f.send(f.ticket, `{"body":"status: working"}`)
	if code != http.StatusOK || r.ID == "" || r.Channel.Kind != "ticket" || r.Channel.Label != "ACME-7" {
		t.Fatalf("ticket default = %d %+v", code, r)
	}
	code, r = f.send(f.personal, `{"body":"done?"}`)
	if code != http.StatusOK || r.Channel.Kind != "session" {
		t.Fatalf("personal default = %d %+v", code, r)
	}
	if got := f.log.InboxSince(f.alice.ID, 0, 0); len(got) != 1 || got[0].Body != "done?" {
		t.Fatalf("alice's inbox = %+v", got)
	}
	if code, r = f.send(f.standing, `{"body":"hello?"}`); code != http.StatusOK || r.Channel.Kind != "session" {
		t.Fatalf("standing default = %d %+v", code, r)
	}
}

func TestSquawksSendAddressed(t *testing.T) {
	f := newSqFixture(t, "user:alice", "channel:eng")
	code, r := f.send(f.standing, `{"body":"hi alice","to":"user:alice"}`)
	if code != http.StatusOK || r.Channel.Kind != "chat" {
		t.Fatalf("to user = %d %+v", code, r)
	}
	if code, r := f.send(f.standing, `{"body":"hi room","to":"channel:eng"}`); code != http.StatusOK || r.Channel.ID != f.room.ID {
		t.Fatalf("to room = %d %+v", code, r)
	}
	for to, want := range map[string]int{
		"user:bob":      http.StatusForbidden,
		"user:nobody":   http.StatusForbidden, // not allowed in form: never tells whether bob or nobody exists
		"channel:ops":   http.StatusForbidden,
		"ticket:ACME-7": http.StatusForbidden,
		"alice":         http.StatusForbidden,
	} {
		if code, _ := f.send(f.standing, `{"body":"x","to":"`+to+`"}`); code != want {
			t.Errorf("to %q = %d, want %d", to, code, want)
		}
	}
	wide := newSqFixture(t, "user:*", "channel:*")
	if code, _ := wide.send(wide.standing, `{"body":"x","to":"user:nobody"}`); code != http.StatusNotFound {
		t.Fatalf("allowed but unknown = %d, want 404", code)
	}
	if code, _ := wide.send(wide.standing, `{"body":"x","to":"channel:ops"}`); code != http.StatusNotFound {
		t.Fatalf("allowed but unknown room = %d, want 404", code)
	}
}

func TestSquawksSendCarriesContentTypeAndNeverLogsSecrets(t *testing.T) {
	f := newSqFixture(t, "user:*")
	if code, _ := f.send(f.standing, `{"body":"secret body 2 * 3","to":"user:alice","content_type":"text/plain"}`); code != http.StatusOK {
		t.Fatalf("send = %d", code)
	}
	if got := f.log.InboxSince(f.alice.ID, 0, 0); len(got) != 1 || got[0].ContentType != intercom.ContentPlain {
		t.Fatalf("logged = %+v", got)
	}
	f.do(http.MethodGet, "/squawks", Instance{ActorID: "some-other-secret"}, "")
	logs := f.logbuf.String()
	for _, secret := range []string{"tok-" + f.standing.ActorID, "some-other-secret", "secret body"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("%q leaked into the logs: %s", secret, logs)
		}
	}
}

func TestSquawksReadIsTheInbox(t *testing.T) {
	f := newSqFixture(t, "user:*")
	if code, _ := f.send(f.personal, `{"body":"question"}`); code != http.StatusOK {
		t.Fatal(code)
	}
	m1 := f.reply(f.personal, f.alice.ID, "answer 1")
	f.reply(f.personal, f.alice.ID, "answer 2")
	f.reply(f.ticket, f.bob.ID, "someone else's")

	r := f.read(f.personal, "")
	if got := bodiesOf(r.Squawks); len(got) != 2 || got[0] != "answer 1" || got[1] != "answer 2" {
		t.Fatalf("inbox = %q (own sends and others' inboxes excluded)", got)
	}
	s := r.Squawks[0]
	if s.ID != m1.ID || s.Author != "alice" || s.From == nil || *s.From != (Party{ID: f.alice.ID, Kind: "user", Label: "alice"}) ||
		s.Channel == nil || s.Channel.Kind != "session" || s.At == nil || s.ContentType != intercom.ContentMarkdown {
		t.Fatalf("entry = %+v (from %+v, channel %+v)", s, s.From, s.Channel)
	}
	if r.PageFirst != m1.ID {
		t.Fatalf("page_first = %q", r.PageFirst)
	}
	// An old client decodes the entry it always knew.
	var old struct {
		Squawks []struct {
			ID, Author, Body string
		} `json:"squawks"`
	}
	raw, _ := json.Marshal(r)
	if err := json.Unmarshal(raw, &old); err != nil || old.Squawks[0].Author != "alice" {
		t.Fatalf("old client view = %+v, %v", old, err)
	}
}

func TestSquawksQueueAnchorsAndCommit(t *testing.T) {
	f := newSqFixture(t)
	var ids []string
	for _, b := range []string{"1", "2", "3", "4"} {
		ids = append(ids, f.reply(f.personal, f.alice.ID, b).ID)
	}
	if got := bodiesOf(f.read(f.personal, "limit=2").Squawks); strings.Join(got, ",") != "1,2" {
		t.Fatalf("default page = %q", got)
	}
	rec := f.do(http.MethodPost, "/squawks/commit", f.personal, `{"up_to":"`+ids[1]+`"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ids[1]) {
		t.Fatalf("commit = %d %s", rec.Code, rec.Body)
	}
	cases := map[string]string{
		"":                       "3,4",
		"dir=backward":           "1", // strictly before the cursor
		"anchor=start&limit=1":   "1",
		"anchor=end&limit=1":     "4",
		"anchor=id&id=" + ids[2]: "4",
		"anchor=id&id=" + ids[2] + "&dir=backward": "1,2",
	}
	for q, want := range cases {
		if got := strings.Join(bodiesOf(f.read(f.personal, q).Squawks), ","); got != want {
			t.Errorf("read %q = %q, want %q", q, got, want)
		}
	}
	for q, want := range map[string]int{"anchor=id": 400, "anchor=id&id=nope": 400, "anchor=sideways": 400, "limit=0": 400, "limit=x": 400} {
		if rec := f.do(http.MethodGet, "/squawks?"+q, f.personal, ""); rec.Code != want {
			t.Errorf("read %q = %d, want %d", q, rec.Code, want)
		}
	}
	// Commit is monotonic, self-scoped, and refuses unknown ids.
	f.do(http.MethodPost, "/squawks/commit", f.personal, `{"up_to":"`+ids[0]+`"}`)
	if inst, _ := f.store.GetInstance(f.personal.ActorID); inst.CommitCursor != ids[1] {
		t.Fatalf("commit went backwards: %q", inst.CommitCursor)
	}
	if inst, _ := f.store.GetInstance(f.ticket.ActorID); inst.CommitCursor != "" {
		t.Fatal("another session's cursor moved")
	}
	for body, want := range map[string]int{`{"up_to":"nope"}`: 400, `{}`: 400, `x`: 400} {
		if rec := f.do(http.MethodPost, "/squawks/commit", f.personal, body); rec.Code != want {
			t.Errorf("commit %q = %d, want %d", body, rec.Code, want)
		}
	}
}

func TestSquawksReadLimitIsCapped(t *testing.T) {
	f := newSqFixture(t)
	ch, err := f.ic.HomeChannel(f.personal)
	if err != nil {
		t.Fatal(err)
	}
	for range maxReadLimit + 5 {
		if _, err := f.ic.PostTrusted(ch, intercom.Squawk{From: f.alice.ID, Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.read(f.personal, "anchor=start&limit=100000").Squawks; len(got) != maxReadLimit {
		t.Fatalf("page = %d, want %d", len(got), maxReadLimit)
	}
}

// A session from before the cutover still reads (and commits past) the
// legacy replies it hadn't processed, ahead of its new ones.
func TestSquawksReadUnionsTheLegacyInbox(t *testing.T) {
	f := newSqFixture(t)
	old, err := f.legacy.Append(intercom.LegacySquawk{From: intercom.Target{Kind: "human", Ref: "alice"},
		To: []intercom.Target{{Kind: "actor", Ref: f.personal.ActorID}}, Body: "before the upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	f.log = intercom.NewMemLog(f.legacy) // the channel log continues the legacy one
	f.ic.lg = f.log
	f.h = NewSquawksHandler(f.store, f.ic, f.log, f.legacy, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	f.reply(f.personal, f.alice.ID, "after the upgrade")

	r := f.read(f.personal, "anchor=start")
	if got := strings.Join(bodiesOf(r.Squawks), ","); got != "before the upgrade,after the upgrade" {
		t.Fatalf("inbox = %q", got)
	}
	if s := r.Squawks[0]; s.Channel != nil || s.Author != "alice" || s.From.Kind != "user" {
		t.Fatalf("legacy entry = %+v", s)
	}
	if rec := f.do(http.MethodPost, "/squawks/commit", f.personal, `{"up_to":"`+old.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("commit a legacy id = %d", rec.Code)
	}
	if got := bodiesOf(f.read(f.personal, "").Squawks); len(got) != 1 || got[0] != "after the upgrade" {
		t.Fatalf("after commit = %q", got)
	}
	if got := bodiesOf(f.read(f.personal, "anchor=end&dir=backward&limit=5").Squawks); len(got) != 2 {
		t.Fatalf("backward from the end = %q", got)
	}
}

func TestSquawksTargets(t *testing.T) {
	f := newSqFixture(t, "user:*", "channel:*")
	rec := f.do(http.MethodGet, "/squawks/targets", f.ticket, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("targets = %d", rec.Code)
	}
	var r struct {
		Targets []targetOut `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, t := range r.Targets {
		got[t.Target] = true
	}
	for _, want := range []string{"ticket:ACME-7", "user:alice", "user:bob"} {
		if !got[want] {
			t.Errorf("targets lack %q: %+v", want, r.Targets)
		}
	}
	if got["user:carol"] {
		t.Error("a non-member is no target")
	}
}

func TestSquawksCallInAndLeave(t *testing.T) {
	f := newSqFixture(t, "user:*", "session:*")
	rec := f.do(http.MethodPost, "/squawks/call-in", f.personal, `{"who":"user:bob"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("call-in = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Channel Party `json:"channel"`
		Member  Party `json:"member"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Channel.Kind != "session" || out.Member.ID != f.bob.ID || out.Member.Kind != "user" {
		t.Fatalf("call-in response = %+v, %v", out, err)
	}
	chat := f.mustPlan(f.personal, "user:alice").Channel
	for body, want := range map[string]int{
		`not json`:             http.StatusBadRequest,
		`{"who":""}`:           http.StatusBadRequest,
		`{"who":"user:carol"}`: http.StatusNotFound, // not a member of the project
		`{"who":"pigeon:x"}`:   http.StatusForbidden,
		`{"who":"user:bob","channel":"` + string(chat.ID) + `"}`:      http.StatusConflict,
		`{"who":"user:bob","channel":"chn_01j9q3zzzzzzzzzzzzzzzzzz"}`: http.StatusForbidden,
	} {
		if rec := f.do(http.MethodPost, "/squawks/call-in", f.personal, body); rec.Code != want {
			t.Errorf("call-in %s = %d, want %d (%s)", body, rec.Code, want, rec.Body)
		}
	}
	// A session leaves a channel it joined, never its own.
	if code, r := f.send(f.ticket, `{"body":"hi","to":"session:`+f.standing.ActorID+`"}`); code != http.StatusOK {
		t.Fatalf("ticket → standing = %d", code)
	} else {
		if rec := f.do(http.MethodPost, "/squawks/leave", f.ticket, `{"channel":"`+string(r.Channel.ID)+`"}`); rec.Code != http.StatusNoContent {
			t.Fatalf("leave = %d %s", rec.Code, rec.Body)
		}
		if rec := f.do(http.MethodPost, "/squawks/leave", f.standing, `{"channel":"`+string(r.Channel.ID)+`"}`); rec.Code != http.StatusForbidden {
			t.Fatalf("leave own home = %d", rec.Code)
		}
	}
	if rec := f.do(http.MethodPost, "/squawks/leave", f.ticket, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("leave without channel = %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/squawks/call-in", f.ticket, ``); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET call-in = %d", rec.Code)
	}
}
