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

// AdvanceCommitCursor mimics FileStore/PostgresStore semantics: monotonic
// forward only (a no-op if upTo <= the current cursor), error if the actor
// has no instance.
func (f *fakeStore) AdvanceCommitCursor(actorID, upTo string) (Instance, error) {
	i, ok := f.instances[actorID]
	if !ok {
		return Instance{}, errors.New("no instance")
	}
	if upTo > i.CommitCursor {
		i.CommitCursor = upTo
	}
	f.instances[actorID] = i
	return i, nil
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

// doGetQuery issues an authenticated GET to /messages with the given query
// string appended (e.g. "anchor=start&limit=2").
func doGetQuery(t *testing.T, h *MessagesHandler, token, query string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/messages"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// doCommit issues an authenticated POST /messages/commit {"up_to": upTo}. An
// empty upTo sends an empty JSON object (no up_to field at all), to exercise
// the missing-field 400 path.
func doCommit(t *testing.T, h *MessagesHandler, token, upTo string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{}`
	if upTo != "" {
		body = `{"up_to":"` + upTo + `"}`
	}
	req := httptest.NewRequest(http.MethodPost, "/messages/commit", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// readResp mirrors the GET /messages JSON shape, for decoding in tests.
type readResp struct {
	Messages        []Comment `json:"messages"`
	CommittedCursor string    `json:"committed_cursor"`
	PageFirst       string    `json:"page_first"`
	PageLast        string    `json:"page_last"`
}

// seedInbox appends n messages (bodies "m0".."m(n-1)") addressed to actor:coveID,
// returning their ids in append order.
func seedInbox(t *testing.T, lg *msglog.Log, coveID string, n int) []string {
	t.Helper()
	coveActor := msglog.Target{Kind: "actor", Ref: coveID}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		m, err := lg.Append(msglog.Message{From: msglog.Target{Kind: "human", Ref: "Alice"}, To: []msglog.Target{coveActor}, Body: "m", Project: "acme"})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		ids = append(ids, m.ID)
	}
	return ids
}

// TestReadDefaultAnchorIsNextAfterCommitCursor asserts the default GET (no
// anchor/dir) returns the next page strictly after the cove's durable
// CommitCursor — seeded mid-log here — and that the response carries
// committed_cursor/page_first/page_last, and that the read itself never
// advances the cursor.
func TestReadDefaultAnchorIsNextAfterCommitCursor(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ids := seedInbox(t, lg, "cove-1", 5)
	store := &fakeStore{
		actors:    map[string]Actor{HashToken(tokenFor("cove-1")): {ID: "cove-1"}},
		instances: map[string]Instance{"cove-1": {ActorID: "cove-1", Unit: "ACME-7", Project: "acme", CommitCursor: ids[1]}},
	}
	h := NewMessagesHandler(store, lg, lg, testLogger())

	rec := doGet(t, h, tokenFor("cove-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp readResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (ids[2..4]); got %+v", len(resp.Messages), resp.Messages)
	}
	if resp.Messages[0].ID != ids[2] || resp.Messages[2].ID != ids[4] {
		t.Fatalf("messages = %+v, want ids[2..4] = %v", resp.Messages, ids[2:])
	}
	if resp.CommittedCursor != ids[1] {
		t.Fatalf("committed_cursor = %q, want %q", resp.CommittedCursor, ids[1])
	}
	if resp.PageFirst != ids[2] || resp.PageLast != ids[4] {
		t.Fatalf("page_first/page_last = %q/%q, want %q/%q", resp.PageFirst, resp.PageLast, ids[2], ids[4])
	}

	// A read must never advance the cursor.
	inst, _ := store.GetInstance("cove-1")
	if inst.CommitCursor != ids[1] {
		t.Fatalf("CommitCursor changed by a read: %q, want unchanged %q", inst.CommitCursor, ids[1])
	}
}

// TestReadAnchorStartIgnoresCommitCursor asserts anchor=start reads from the
// beginning of the log regardless of where the cursor sits.
func TestReadAnchorStartIgnoresCommitCursor(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ids := seedInbox(t, lg, "cove-1", 3)
	store := &fakeStore{
		actors:    map[string]Actor{HashToken(tokenFor("cove-1")): {ID: "cove-1"}},
		instances: map[string]Instance{"cove-1": {ActorID: "cove-1", Unit: "ACME-7", CommitCursor: ids[2]}},
	}
	h := NewMessagesHandler(store, lg, lg, testLogger())

	rec := doGetQuery(t, h, tokenFor("cove-1"), "anchor=start")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp readResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Messages) != 3 || resp.Messages[0].ID != ids[0] {
		t.Fatalf("messages = %+v, want all 3 starting at %q", resp.Messages, ids[0])
	}
}

// TestReadAnchorEndIgnoresCommitCursor asserts anchor=end returns the last
// `limit` messages regardless of the cursor.
func TestReadAnchorEndIgnoresCommitCursor(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ids := seedInbox(t, lg, "cove-1", 5)
	store := &fakeStore{
		actors:    map[string]Actor{HashToken(tokenFor("cove-1")): {ID: "cove-1"}},
		instances: map[string]Instance{"cove-1": {ActorID: "cove-1", Unit: "ACME-7", CommitCursor: ids[0]}},
	}
	h := NewMessagesHandler(store, lg, lg, testLogger())

	rec := doGetQuery(t, h, tokenFor("cove-1"), "anchor=end&limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp readResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Messages) != 2 || resp.Messages[0].ID != ids[3] || resp.Messages[1].ID != ids[4] {
		t.Fatalf("messages = %+v, want the last 2 (%v)", resp.Messages, ids[3:])
	}
}

// TestReadAnchorIDForwardAndBackward asserts anchor=id&id=<x> combined with
// dir selects the window strictly after (forward, default) or strictly
// before (backward) the given id.
func TestReadAnchorIDForwardAndBackward(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ids := seedInbox(t, lg, "cove-1", 5)
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, lg, lg, testLogger())

	fwd := doGetQuery(t, h, tokenFor("cove-1"), "anchor=id&id="+ids[2])
	var fwdResp readResp
	if err := json.Unmarshal(fwd.Body.Bytes(), &fwdResp); err != nil {
		t.Fatalf("decode forward: %v", err)
	}
	if len(fwdResp.Messages) != 2 || fwdResp.Messages[0].ID != ids[3] || fwdResp.Messages[1].ID != ids[4] {
		t.Fatalf("forward messages = %+v, want %v", fwdResp.Messages, ids[3:])
	}

	back := doGetQuery(t, h, tokenFor("cove-1"), "anchor=id&id="+ids[2]+"&dir=backward")
	var backResp readResp
	if err := json.Unmarshal(back.Body.Bytes(), &backResp); err != nil {
		t.Fatalf("decode backward: %v", err)
	}
	if len(backResp.Messages) != 2 || backResp.Messages[0].ID != ids[0] || backResp.Messages[1].ID != ids[1] {
		t.Fatalf("backward messages = %+v, want %v", backResp.Messages, ids[:2])
	}
}

// TestReadAnchorIDMissingIDIs400 asserts anchor=id without ?id= is a 400.
func TestReadAnchorIDMissingIDIs400(t *testing.T) {
	h, _, _, _ := newTestMessagesHandler(t)
	rec := doGetQuery(t, h, "tok-A", "anchor=id")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestReadInvalidAnchorIs400 asserts an unrecognized anchor value is a 400.
func TestReadInvalidAnchorIs400(t *testing.T) {
	h, _, _, _ := newTestMessagesHandler(t)
	rec := doGetQuery(t, h, "tok-A", "anchor=bogus")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestReadInvalidLimitIs400 asserts a non-numeric or non-positive limit is a 400.
func TestReadInvalidLimitIs400(t *testing.T) {
	h, _, _, _ := newTestMessagesHandler(t)
	for _, v := range []string{"abc", "0", "-5"} {
		rec := doGetQuery(t, h, "tok-A", "limit="+v)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s: status = %d, want 400", v, rec.Code)
		}
	}
}

// TestReadLimitIsCappedAtMax asserts a limit above maxReadLimit is silently
// capped rather than honored or rejected.
func TestReadLimitIsCappedAtMax(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedInbox(t, lg, "cove-1", maxReadLimit+5)
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, lg, lg, testLogger())

	rec := doGetQuery(t, h, tokenFor("cove-1"), "anchor=start&limit=100000")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp readResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Messages) != maxReadLimit {
		t.Fatalf("messages = %d, want capped at %d", len(resp.Messages), maxReadLimit)
	}
}

// TestCommitAdvancesCursorAndReturnsIt asserts POST /messages/commit calls
// AdvanceCommitCursor and echoes the resulting cursor.
func TestCommitAdvancesCursorAndReturnsIt(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken(tokenFor("cove-1")): {ID: "cove-1"}},
		instances: map[string]Instance{"cove-1": {ActorID: "cove-1", Unit: "ACME-7", Project: "acme"}},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log)

	rec := doCommit(t, h, tokenFor("cove-1"), "msg-005")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		CommittedCursor string `json:"committed_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CommittedCursor != "msg-005" {
		t.Fatalf("committed_cursor = %q, want msg-005", resp.CommittedCursor)
	}
	inst, _ := store.GetInstance("cove-1")
	if inst.CommitCursor != "msg-005" {
		t.Fatalf("store CommitCursor = %q, want msg-005", inst.CommitCursor)
	}
}

// TestCommitMissingUpToIs400 asserts an absent/empty up_to is a 400 and does
// not touch the store.
func TestCommitMissingUpToIs400(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken(tokenFor("cove-1")): {ID: "cove-1"}},
		instances: map[string]Instance{"cove-1": {ActorID: "cove-1", Unit: "ACME-7"}},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log)

	rec := doCommit(t, h, tokenFor("cove-1"), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if inst, _ := store.GetInstance("cove-1"); inst.CommitCursor != "" {
		t.Fatalf("CommitCursor = %q, want unchanged (empty) on a rejected commit", inst.CommitCursor)
	}
}

// TestCommitOnlyAdvancesCallersOwnInstance is the security-critical
// assertion for commit: the actor advanced is the one derived from the
// bearer token, never a value the request body could name (there is no such
// field). A second actor's instance must be untouched.
func TestCommitOnlyAdvancesCallersOwnInstance(t *testing.T) {
	store := &fakeStore{
		actors: map[string]Actor{
			HashToken(tokenFor("cove-1")): {ID: "cove-1"},
			HashToken(tokenFor("cove-2")): {ID: "cove-2"},
		},
		instances: map[string]Instance{
			"cove-1": {ActorID: "cove-1", Unit: "ACME-7"},
			"cove-2": {ActorID: "cove-2", Unit: "ACME-8"},
		},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log)

	rec := doCommit(t, h, tokenFor("cove-1"), "msg-100")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if inst, _ := store.GetInstance("cove-2"); inst.CommitCursor != "" {
		t.Fatalf("cove-2 CommitCursor = %q, want unchanged (empty)", inst.CommitCursor)
	}
	if inst, _ := store.GetInstance("cove-1"); inst.CommitCursor != "msg-100" {
		t.Fatalf("cove-1 CommitCursor = %q, want msg-100", inst.CommitCursor)
	}
}

// TestHandleCommitStoreErrorIs403 exercises handleCommit's own defensive
// branch (AdvanceCommitCursor failing for an actor with no instance) directly
// — unreachable via ServeHTTP, which already gates on GetInstance beforehand.
func TestHandleCommitStoreErrorIs403(t *testing.T) {
	store := &fakeStore{instances: map[string]Instance{}}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log)

	req := httptest.NewRequest(http.MethodPost, "/messages/commit", strings.NewReader(`{"up_to":"msg-1"}`))
	rec := httptest.NewRecorder()
	h.handleCommit(rec, req, Actor{ID: "ghost"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestCommitGetIsMethodNotAllowed asserts a GET to /messages/commit is
// rejected with 405 (Allow: POST) rather than silently falling through to
// handleGet, which would otherwise treat it as a bare inbox read.
func TestCommitGetIsMethodNotAllowed(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken(tokenFor("cove-1")): {ID: "cove-1"}},
		instances: map[string]Instance{"cove-1": {ActorID: "cove-1", Unit: "ACME-7"}},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log)

	req := httptest.NewRequest(http.MethodGet, "/messages/commit", nil)
	req.Header.Set("Authorization", "Bearer "+tokenFor("cove-1"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "POST" {
		t.Fatalf("Allow header = %q, want POST", got)
	}
}

// TestCommitMalformedBodyIs400 asserts a non-empty but malformed JSON body
// decodes to a 400 (distinct from the empty-body/missing-field 400 case).
func TestCommitMalformedBodyIs400(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken(tokenFor("cove-1")): {ID: "cove-1"}},
		instances: map[string]Instance{"cove-1": {ActorID: "cove-1", Unit: "ACME-7"}},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log)

	req := httptest.NewRequest(http.MethodPost, "/messages/commit", strings.NewReader(`{not-json`))
	req.Header.Set("Authorization", "Bearer "+tokenFor("cove-1"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestCommitOversizeBodyIs413 asserts a commit body over maxMessageBodyBytes
// is rejected as 413, matching handlePost's oversize handling.
func TestCommitOversizeBodyIs413(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken(tokenFor("cove-1")): {ID: "cove-1"}},
		instances: map[string]Instance{"cove-1": {ActorID: "cove-1", Unit: "ACME-7"}},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, nil, nil, log)

	huge := strings.Repeat("a", 32*1024)
	req := httptest.NewRequest(http.MethodPost, "/messages/commit", strings.NewReader(`{"up_to":"`+huge+`"}`))
	req.Header.Set("Authorization", "Bearer "+tokenFor("cove-1"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

// bytesDiscard is an io.Writer that discards everything, used where a *bytes.Buffer
// isn't needed but slog.NewTextHandler still wants a writer.
type bytesDiscard struct{}

func (bytesDiscard) Write(p []byte) (int, error) { return len(p), nil }
