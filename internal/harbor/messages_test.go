package harbor

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/msglog"
)

// fakeStore is a minimal messagesStore: canned actor-by-token-hash and
// instance-by-actor-id, so tests don't need a real FileStore. roles/rosters
// back the widened GetRole/GetRoster used by DecideSend for a targeted send.
type fakeStore struct {
	actors    map[string]Actor           // tokenHash -> Actor
	instances map[string]Instance        // actorID -> Instance
	roles     map[string]map[string]Role // project -> role name -> Role
	rosters   map[string]Roster          // project -> Roster
}

func (f *fakeStore) Lookup(tokenHash string) (Actor, bool) {
	a, ok := f.actors[tokenHash]
	return a, ok
}

func (f *fakeStore) GetInstance(actorID string) (Instance, bool) {
	i, ok := f.instances[actorID]
	return i, ok
}

func (f *fakeStore) GetRole(project, name string) (Role, bool) {
	rs, ok := f.roles[project]
	if !ok {
		return Role{}, false
	}
	r, ok := rs[name]
	return r, ok
}

func (f *fakeStore) GetRoster(project string) (Roster, bool) {
	r, ok := f.rosters[project]
	return r, ok
}

// fakeAppender records every message passed to Append, for asserting the
// outbound send. The Log is the authoritative send path: a configured err is
// returned to the caller (but the message is still recorded) so the
// fail-the-send-on-append-error behavior (502) can be exercised.
type fakeAppender struct {
	got []msglog.Message
	err error
}

func (f *fakeAppender) Append(m msglog.Message) (msglog.Message, error) {
	f.got = append(f.got, m)
	return m, f.err
}

// newTestMessagesHandler builds a MessagesHandler backed by a real (temp-file)
// msglog.Log for both the reader and the appender — the same wiring
// production uses — with one actor "cove-AET-7" (bearer "tok-A", ticket
// "AET-7"). It returns the Log too, so a test can seed the actor's inbox via
// lg.Append before issuing a GET.
func newTestMessagesHandler(t *testing.T) (*MessagesHandler, *fakeStore, *msglog.Log, *bytes.Buffer) {
	t.Helper()
	store := &fakeStore{
		actors: map[string]Actor{
			HashToken("tok-A"): {ID: "cove-AET-7"},
		},
		instances: map[string]Instance{
			"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"},
		},
	}
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	var logbuf bytes.Buffer
	slogger := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := NewMessagesHandler(store, lg, lg, slogger)
	return h, store, lg, &logbuf
}

// newReadTestStore builds a fakeStore with one actor+instance enrolled: actor
// actorID, bearer tokenFor(actorID), ticket unit, project project.
func newReadTestStore(t *testing.T, actorID, unit, project string) *fakeStore {
	t.Helper()
	return &fakeStore{
		actors: map[string]Actor{
			HashToken(tokenFor(actorID)): {ID: actorID},
		},
		instances: map[string]Instance{
			actorID: {ActorID: actorID, Unit: unit, Project: project},
		},
	}
}

// tokenFor returns the deterministic bearer token newReadTestStore enrolled
// for actorID.
func tokenFor(actorID string) string { return "tok-" + actorID }

// mustAppend appends m to lg, failing the test on error.
func mustAppend(t *testing.T, lg *msglog.Log, m msglog.Message) {
	t.Helper()
	if _, err := lg.Append(m); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// doGet issues an authenticated GET /messages against h.
func doGet(t *testing.T, h *MessagesHandler, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/messages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMessagesMissingTokenIs401(t *testing.T) {
	h, _, _, _ := newTestMessagesHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/messages", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestMessagesUnknownTokenIs401(t *testing.T) {
	h, _, _, _ := newTestMessagesHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/messages", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestMessagesNoInstanceIs403(t *testing.T) {
	store := &fakeStore{
		actors: map[string]Actor{
			HashToken("tok-B"): {ID: "cove-no-instance"},
		},
		instances: map[string]Instance{},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log)

	req := httptest.NewRequest(http.MethodGet, "/messages", nil)
	req.Header.Set("Authorization", "Bearer tok-B")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestMessagesPostIsSelfScoped is the security-critical assertion: the
// appended target is derived ONLY from the authenticated actor's own
// Instance.Unit. The POST request itself carries no ticket/target field at
// all, so there is no way for a cove to name another cove's ticket. Since the
// Log cutover, handlePost only appends; a separate egress engine delivers to
// Linear.
func TestMessagesPostIsSelfScoped(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7"}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7", Project: "acme"}},
	}
	ap := &fakeAppender{}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(ap.got) != 1 {
		t.Fatalf("append calls = %d, want 1", len(ap.got))
	}
	m := ap.got[0]
	if m.From.Kind != "actor" || m.From.Ref != "cove-AET-7" {
		t.Fatalf("From = %+v, want actor:cove-AET-7", m.From)
	}
	if len(m.To) != 1 || m.To[0].Kind != "channel" || m.To[0].Ref != "AET-7" {
		t.Fatalf("To = %+v, want [channel:AET-7]", m.To)
	}
	if m.Body != "hi" {
		t.Fatalf("Body = %q, want raw \"hi\"", m.Body)
	}
	if m.Project != "acme" {
		t.Fatalf("Project = %q, want acme", m.Project)
	}
}

func TestMessagesMethodNotAllowed(t *testing.T) {
	h, _, _, _ := newTestMessagesHandler(t)
	req := httptest.NewRequest(http.MethodPut, "/messages", nil)
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestMessagesOversizeBodyIs413(t *testing.T) {
	h, _, _, _ := newTestMessagesHandler(t)
	huge := strings.Repeat("a", 32*1024)
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"`+huge+`"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestMessagesEmptyBodyIs400(t *testing.T) {
	h, _, _, _ := newTestMessagesHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":""}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestMessagesNeverLogsToken asserts the bearer token string never appears in
// any logged output, across both the success and failure paths, and that error
// bodies returned to the client are generic (no token, no internal detail).
func TestMessagesNeverLogsToken(t *testing.T) {
	h, _, _, logbuf := newTestMessagesHandler(t)

	const tok = "tok-A"
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	// unknown-token path too.
	req2 := httptest.NewRequest(http.MethodGet, "/messages", nil)
	req2.Header.Set("Authorization", "Bearer some-other-secret-token")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if strings.Contains(logbuf.String(), tok) {
		t.Fatalf("token leaked into logs: %s", logbuf.String())
	}
	if strings.Contains(logbuf.String(), "some-other-secret-token") {
		t.Fatalf("token leaked into logs: %s", logbuf.String())
	}
	if strings.Contains(rec.Body.String(), tok) || strings.Contains(rec2.Body.String(), "some-other-secret-token") {
		t.Fatalf("token leaked into an error body")
	}
}

// TestSendToHumanAppendsRawToHumanTarget asserts a "to":"human:<name>" send
// is authorized via DecideSend and appended with To: human:<name> and the RAW
// body — no @-mention prefix in the Log; rendering (the @-mention posted on
// the cove's own ticket, so replies keep landing where wake-on-reply watches)
// is the egress adapter's job, not handlePost's. It also asserts the message
// body never reaches the logs while the (non-secret) target does.
func TestSendToHumanAppendsRawToHumanTarget(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7", Project: "acme"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}},
		rosters:   map[string]Roster{"acme": {Humans: []Human{{Name: "alice", Handle: "alice.h"}}}},
	}
	ap := &fakeAppender{}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := NewMessagesHandler(store, nil, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"ping","to":"human:alice"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(ap.got) != 1 {
		t.Fatalf("append calls = %d, want 1", len(ap.got))
	}
	m := ap.got[0]
	if len(m.To) != 1 || m.To[0].Kind != "human" || m.To[0].Ref != "alice" {
		t.Fatalf("To = %+v, want [human:alice]", m.To)
	}
	if m.Body != "ping" {
		t.Fatalf("Body = %q, want raw \"ping\" (no @-mention)", m.Body)
	}
	if strings.Contains(logbuf.String(), "ping") {
		t.Fatalf("message body leaked into logs: %s", logbuf.String())
	}
	if !strings.Contains(logbuf.String(), "human:alice") {
		t.Fatalf("expected the (non-secret) target to be logged: %s", logbuf.String())
	}
}

// TestSendToChannelAppendsToChannelTarget asserts a "to":"channel:<name>"
// send is authorized via DecideSend and appended with To: channel:<name> —
// the roster name, not a resolved ticket id; ticket resolution now happens at
// egress, not in handlePost — and the raw body.
func TestSendToChannelAppendsToChannelTarget(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7", Project: "acme"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"channel:*"}}}}},
		rosters:   map[string]Roster{"acme": {Channels: []Channel{{Name: "eng-help", Service: "linear", Ref: "ACME-1"}}}},
	}
	ap := &fakeAppender{}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"heads up","to":"channel:eng-help"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(ap.got) != 1 {
		t.Fatalf("append calls = %d, want 1", len(ap.got))
	}
	m := ap.got[0]
	if len(m.To) != 1 || m.To[0].Kind != "channel" || m.To[0].Ref != "eng-help" {
		t.Fatalf("To = %+v, want [channel:eng-help]", m.To)
	}
	if m.Body != "heads up" {
		t.Fatalf("Body = %q, want raw \"heads up\"", m.Body)
	}
}

// TestMessagesPostAppendFailureIs502 asserts that once the Log is the
// authoritative delivery path, an Append failure fails the send (502).
func TestMessagesPostAppendFailureIs502(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7"}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7", Project: "acme"}},
	}
	ap := &fakeAppender{err: errors.New("disk full")}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
}

// TestMessagesPostNilLogIs503 asserts a send fails with 503 (not a silent
// swallow) when the Log is unconfigured (nil appender).
func TestMessagesPostNilLogIs503(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7"}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7", Project: "acme"}},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log) // nil appender

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// TestSendToDeniedIs403 asserts a target whose form no grant's addressing
// authorizes is denied (403) and — critically — nothing is appended.
func TestSendToDeniedIs403(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}},
		rosters:   map[string]Roster{"acme": {Channels: []Channel{{Name: "secret", Ref: "X"}}}},
	}
	ap := &fakeAppender{}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"x","to":"channel:secret"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(ap.got) != 0 {
		t.Fatalf("Append must not be called on a denied send; got %d", len(ap.got))
	}
}

// TestSendToUnresolvedIs404 asserts a target authorized-in-form but absent
// from the roster of every authorizing grant's project is a 404, distinct
// from the 403 denial path.
func TestSendToUnresolvedIs404(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}},
		rosters:   map[string]Roster{"acme": {Humans: []Human{{Name: "alice", Handle: "a"}}}},
	}
	ap := &fakeAppender{}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"x","to":"human:bob"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if len(ap.got) != 0 {
		t.Fatalf("Append must not be called on an unresolved send; got %d", len(ap.got))
	}
}

// TestTargetsListsAllowedTargets asserts GET /messages/targets returns the
// actor's authorized-and-resolvable targets — a channel not in the role's
// addressing must be excluded — and that it never includes handles in the
// response.
func TestTargetsListsAllowedTargets(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}},
		rosters:   map[string]Roster{"acme": {Humans: []Human{{Name: "alice", Handle: "a"}}, Channels: []Channel{{Name: "eng", Ref: "R"}}}},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log)

	req := httptest.NewRequest(http.MethodGet, "/messages/targets", nil)
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Targets []map[string]string `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	if len(out.Targets) != 1 {
		t.Fatalf("targets = %+v, want exactly 1 (channel must be excluded — not in addressing)", out.Targets)
	}
	tg := out.Targets[0]
	if tg["target"] != "human:alice" || tg["kind"] != "human" || tg["name"] != "alice" {
		t.Fatalf("target = %+v, want human:alice", tg)
	}
	if _, ok := tg["handle"]; ok {
		t.Fatalf("target must not include the handle: %+v", tg)
	}
}

// TestTargetsGetStillReturnsInboxForBareMessagesPath is the regression guard:
// the targets branch must only fire on the exact "/targets" suffix — GET
// /messages must still return the caller's own inbox.
func TestTargetsGetStillReturnsInboxForBareMessagesPath(t *testing.T) {
	h, _, lg, _ := newTestMessagesHandler(t)
	mustAppend(t, lg, msglog.Message{
		From: msglog.Target{Kind: "human", Ref: "alice"},
		To:   []msglog.Target{{Kind: "actor", Ref: "cove-AET-7"}},
		Body: "hello",
	})

	req := httptest.NewRequest(http.MethodGet, "/messages", nil)
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Messages []Comment `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	if len(out.Messages) != 1 || out.Messages[0].Author != "alice" {
		t.Fatalf("messages = %+v, want the seeded inbox (own-ticket read must be unaffected)", out.Messages)
	}
}

func TestReadReturnsInbox(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// two inbound replies to the cove + one of the cove's OWN outbound (must be excluded)
	coveActor := msglog.Target{Kind: "actor", Ref: "cove-1"}
	mustAppend(t, lg, msglog.Message{From: msglog.Target{Kind: "human", Ref: "Alice"}, To: []msglog.Target{coveActor}, Body: "first", Project: "acme"})
	mustAppend(t, lg, msglog.Message{From: coveActor, To: []msglog.Target{{Kind: "channel", Ref: "ACME-7"}}, Body: "my own send", Project: "acme"})
	mustAppend(t, lg, msglog.Message{From: msglog.Target{Kind: "human", Ref: "Alice"}, To: []msglog.Target{coveActor}, Body: "second", Project: "acme"})

	// store: actor "cove-1" with a token, instance Unit "ACME-7"
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, lg, lg, testLogger())

	rec := doGet(t, h, tokenFor("cove-1")) // GET /messages with the actor's bearer
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Messages []Comment `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (own send excluded); got %+v", len(resp.Messages), resp.Messages)
	}
	if resp.Messages[0].Author != "Alice" || resp.Messages[0].Body != "first" {
		t.Fatalf("msg0 = %+v, want Alice/first", resp.Messages[0])
	}
	if resp.Messages[1].Body != "second" {
		t.Fatalf("msg1 = %+v, want second", resp.Messages[1])
	}
	for _, m := range resp.Messages {
		if m.ID == "" || m.At == nil {
			t.Fatalf("id/at missing: %+v", m)
		}
	}
}

func TestReadEmptyInboxIsEmptyArray(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, lg, lg, testLogger())
	rec := doGet(t, h, tokenFor("cove-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"messages":[]`) {
		t.Fatalf("body = %s, want empty array (not null)", got)
	}
}

func TestReadNilReaderIs503(t *testing.T) {
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, nil, nil, testLogger())
	rec := doGet(t, h, tokenFor("cove-1"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestReadIsSelfScoped(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// an inbound to a DIFFERENT cove
	mustAppend(t, lg, msglog.Message{From: msglog.Target{Kind: "human", Ref: "Bob"}, To: []msglog.Target{{Kind: "actor", Ref: "cove-2"}}, Body: "for cove-2", Project: "acme"})
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, lg, lg, testLogger())
	rec := doGet(t, h, tokenFor("cove-1"))
	var resp struct {
		Messages []Comment `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Messages) != 0 {
		t.Fatalf("cove-1 must not see cove-2's inbox; got %+v", resp.Messages)
	}
}

// bytesDiscard is an io.Writer that discards everything, used where a *bytes.Buffer
// isn't needed but slog.NewTextHandler still wants a writer.
type bytesDiscard struct{}

func (bytesDiscard) Write(p []byte) (int, error) { return len(p), nil }
