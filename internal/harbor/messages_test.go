package harbor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// fakeCommenter records PostComment calls, returns canned Comments, and maps a
// ticket identifier (e.g. "AET-7") to an internal issue id.
type fakeCommenter struct {
	ids      map[string]string // identifier -> issue id
	comments []Comment
	posted   []postedComment
	err      error // if set, every method fails with this error
}

type postedComment struct {
	issueID string
	body    string
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

func (f *fakeCommenter) IssueByIdentifier(_ context.Context, identifier string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	id, ok := f.ids[identifier]
	if !ok {
		return "", fmt.Errorf("fakeCommenter: no issue for identifier %q", identifier)
	}
	return id, nil
}

func (f *fakeCommenter) PostComment(_ context.Context, issueID, body string) error {
	if f.err != nil {
		return f.err
	}
	f.posted = append(f.posted, postedComment{issueID: issueID, body: body})
	return nil
}

func (f *fakeCommenter) Comments(_ context.Context, issueID string) ([]Comment, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.comments, nil
}

func newTestMessagesHandler() (*MessagesHandler, *fakeStore, *fakeCommenter, *bytes.Buffer) {
	store := &fakeStore{
		actors: map[string]Actor{
			HashToken("tok-A"): {ID: "cove-AET-7"},
		},
		instances: map[string]Instance{
			"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"},
		},
	}
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// A configured appender, so a bare POST succeeds (204) by default; tests
	// that specifically want the unconfigured-Log path build their own handler.
	h := NewMessagesHandler(store, cmt, &fakeAppender{}, log)
	return h, store, cmt, &logbuf
}

func TestMessagesMissingTokenIs401(t *testing.T) {
	h, _, cmt, _ := newTestMessagesHandler()
	req := httptest.NewRequest(http.MethodGet, "/messages", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(cmt.posted) != 0 || cmt.comments != nil && rec.Code == http.StatusOK {
		t.Fatalf("commenter should not have been called")
	}
}

func TestMessagesUnknownTokenIs401(t *testing.T) {
	h, _, cmt, _ := newTestMessagesHandler()
	req := httptest.NewRequest(http.MethodGet, "/messages", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("PostComment should not have been called")
	}
}

func TestMessagesNoInstanceIs403(t *testing.T) {
	store := &fakeStore{
		actors: map[string]Actor{
			HashToken("tok-B"): {ID: "cove-no-instance"},
		},
		instances: map[string]Instance{},
	}
	cmt := &fakeCommenter{ids: map[string]string{}}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, nil, log)

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
// Log cutover, handlePost never calls PostComment — it only appends; a
// separate egress engine delivers to Linear.
func TestMessagesPostIsSelfScoped(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7"}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7", Project: "acme"}},
	}
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	ap := &fakeAppender{}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("handlePost must not PostComment after cutover; got %d", len(cmt.posted))
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

// TestMessagesPostDoesNotResolveTicket asserts POST /messages is fully
// decoupled from the tracker: even when IssueByIdentifier fails (e.g. a
// Linear outage), a send still succeeds (204) and appends, since a send only
// writes to the Log — it never resolves or talks to the tracker directly.
func TestMessagesPostDoesNotResolveTicket(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7"}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7", Project: "acme"}},
	}
	cmt := &fakeCommenter{err: fmt.Errorf("tracker down")}
	ap := &fakeAppender{}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (tracker outage must not block a send); body=%s", rec.Code, rec.Body.String())
	}
	if len(ap.got) != 1 {
		t.Fatalf("append calls = %d, want 1", len(ap.got))
	}
}

func TestMessagesGetReturnsTaggedInbox(t *testing.T) {
	h, _, cmt, _ := newTestMessagesHandler()
	cmt.comments = []Comment{
		{ID: "c1", Author: "alice", Body: "hello"},
		{ID: "c2", Author: "bob", Body: "world"},
	}

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
	if len(out.Messages) != 2 || out.Messages[0].Author != "alice" || out.Messages[1].Body != "world" {
		t.Fatalf("messages = %+v, want the canned inbox", out.Messages)
	}
}

func TestMessagesMethodNotAllowed(t *testing.T) {
	h, _, _, _ := newTestMessagesHandler()
	req := httptest.NewRequest(http.MethodPut, "/messages", nil)
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestMessagesOversizeBodyIs413(t *testing.T) {
	h, _, cmt, _ := newTestMessagesHandler()
	huge := strings.Repeat("a", 32*1024)
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"`+huge+`"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("PostComment should not have been called for an oversize body")
	}
}

func TestMessagesEmptyBodyIs400(t *testing.T) {
	h, _, cmt, _ := newTestMessagesHandler()
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":""}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("PostComment should not have been called for an empty body")
	}
}

// TestMessagesNeverLogsToken asserts the bearer token string never appears in
// any logged output, across both the success and failure paths, and that error
// bodies returned to the client are generic (no token, no internal detail).
func TestMessagesNeverLogsToken(t *testing.T) {
	h, _, _, logbuf := newTestMessagesHandler()

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
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	ap := &fakeAppender{}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := NewMessagesHandler(store, cmt, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"ping","to":"human:alice"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("handlePost must not PostComment after cutover; got %d", len(cmt.posted))
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
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	ap := &fakeAppender{}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"heads up","to":"channel:eng-help"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("handlePost must not PostComment after cutover; got %d", len(cmt.posted))
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
// authoritative delivery path, an Append failure fails the send (502) —
// unlike the old best-effort shadow-write, there is no live PostComment left
// to fall back on.
func TestMessagesPostAppendFailureIs502(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7"}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7", Project: "acme"}},
	}
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	ap := &fakeAppender{err: errors.New("disk full")}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("PostComment must not be called after cutover")
	}
}

// TestMessagesPostNilLogIs503 asserts a send fails with 503 (not a silent
// swallow, and not a fall-back live PostComment — both removed by the
// cutover) when the Log is unconfigured (nil appender).
func TestMessagesPostNilLogIs503(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7"}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7", Project: "acme"}},
	}
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, nil, log) // nil appender

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("PostComment must not be called when the Log is unconfigured")
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
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	ap := &fakeAppender{}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"x","to":"channel:secret"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("PostComment must not be called on a denied send")
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
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	ap := &fakeAppender{}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, ap, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"x","to":"human:bob"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("PostComment must not be called on an unresolved send")
	}
	if len(ap.got) != 0 {
		t.Fatalf("Append must not be called on an unresolved send; got %d", len(ap.got))
	}
}

// TestTargetsListsAllowedTargets asserts GET /messages/targets returns the
// actor's authorized-and-resolvable targets — a channel not in the role's
// addressing must be excluded — and that it never resolves a ticket (no
// IssueByIdentifier call) and never includes handles in the response.
func TestTargetsListsAllowedTargets(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}},
		rosters:   map[string]Roster{"acme": {Humans: []Human{{Name: "alice", Handle: "a"}}, Channels: []Channel{{Name: "eng", Ref: "R"}}}},
	}
	// No ids configured: if the handler tried to resolve a ticket it would 502.
	cmt := &fakeCommenter{ids: map[string]string{}}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, nil, log)

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
// /messages must still return the caller's own-ticket inbox.
func TestTargetsGetStillReturnsInboxForBareMessagesPath(t *testing.T) {
	h, _, cmt, _ := newTestMessagesHandler()
	cmt.comments = []Comment{{ID: "c1", Author: "alice", Body: "hello"}}

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
		t.Fatalf("messages = %+v, want the canned inbox (own-ticket read must be unaffected)", out.Messages)
	}
}

// bytesDiscard is an io.Writer that discards everything, used where a *bytes.Buffer
// isn't needed but slog.NewTextHandler still wants a writer.
type bytesDiscard struct{}

func (bytesDiscard) Write(p []byte) (int, error) { return len(p), nil }
