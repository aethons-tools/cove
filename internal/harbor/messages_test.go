package harbor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	errIDs   map[string]bool   // identifier -> IssueByIdentifier fails for just this one
	comments []Comment
	posted   []postedComment
	err      error // if set, every method fails with this error
}

type postedComment struct {
	issueID string
	body    string
}

func (f *fakeCommenter) IssueByIdentifier(_ context.Context, identifier string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.errIDs[identifier] {
		return "", fmt.Errorf("fakeCommenter: resolve failed for identifier %q", identifier)
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
	h := NewMessagesHandler(store, cmt, log)
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
	h := NewMessagesHandler(store, cmt, log)

	req := httptest.NewRequest(http.MethodGet, "/messages", nil)
	req.Header.Set("Authorization", "Bearer tok-B")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestMessagesPostIsSelfScoped is the security-critical assertion: the ticket
// the comment lands on is derived ONLY from the authenticated actor's own
// Instance.Unit. The POST request itself carries no ticket/target field at all,
// so there is no way for a cove to name another cove's ticket.
func TestMessagesPostIsSelfScoped(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7"}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
	}
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 1 {
		t.Fatalf("PostComment calls = %d, want 1", len(cmt.posted))
	}
	if got := cmt.posted[0]; got.issueID != "iss_7" || got.body != "hi" {
		t.Fatalf("PostComment(%q, %q), want (%q, %q)", got.issueID, got.body, "iss_7", "hi")
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

// TestSendToHumanMentionsOnOwnTicket asserts a "to":"human:<name>" send is
// authorized via DecideSend and delivered as an @-mention on the cove's OWN
// ticket (not a separate thread) — replies keep landing where the existing
// wake-on-reply loop watches. It also asserts the message body never reaches
// the logs while the (non-secret) target does.
func TestSendToHumanMentionsOnOwnTicket(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}},
		rosters:   map[string]Roster{"acme": {Humans: []Human{{Name: "alice", Handle: "alice.h"}}}},
	}
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := NewMessagesHandler(store, cmt, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"ping","to":"human:alice"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 1 {
		t.Fatalf("PostComment calls = %d, want 1", len(cmt.posted))
	}
	got := cmt.posted[0]
	if got.issueID != "iss_7" {
		t.Fatalf("delivered to %q, want own ticket iss_7", got.issueID)
	}
	if !strings.HasPrefix(got.body, "@alice.h ") || !strings.Contains(got.body, "ping") {
		t.Fatalf("body = %q, want @mention prefix", got.body)
	}
	if strings.Contains(logbuf.String(), "ping") {
		t.Fatalf("message body leaked into logs: %s", logbuf.String())
	}
	if !strings.Contains(logbuf.String(), "human:alice") {
		t.Fatalf("expected the (non-secret) target to be logged: %s", logbuf.String())
	}
}

// TestSendToChannelPostsOnChannelThread asserts a "to":"channel:<name>" send
// is delivered verbatim (no @-mention prefix) to the channel's OWN thread —
// resolved via IssueByIdentifier(st.Ref) — not the cove's own ticket.
func TestSendToChannelPostsOnChannelThread(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"channel:*"}}}}},
		rosters:   map[string]Roster{"acme": {Channels: []Channel{{Name: "eng-help", Service: "linear", Ref: "ACME-1"}}}},
	}
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7", "ACME-1": "iss_1"}}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"heads up","to":"channel:eng-help"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 1 {
		t.Fatalf("PostComment calls = %d, want 1", len(cmt.posted))
	}
	got := cmt.posted[0]
	if got.issueID != "iss_1" || got.body != "heads up" {
		t.Fatalf("PostComment(%q, %q), want (%q, %q)", got.issueID, got.body, "iss_1", "heads up")
	}
}

// TestSendToChannelResolveErrorIs502 asserts that when a "to":"channel:<name>"
// send is authorized but the channel's Ref fails to resolve to an issue id
// (IssueByIdentifier errors), the handler reports 502 and delivers nothing —
// distinct from the 403/404 authorization-failure paths.
func TestSendToChannelResolveErrorIs502(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"channel:*"}}}}},
		rosters:   map[string]Roster{"acme": {Channels: []Channel{{Name: "eng-help", Service: "linear", Ref: "ACME-1"}}}},
	}
	cmt := &fakeCommenter{
		ids:    map[string]string{"AET-7": "iss_7"},
		errIDs: map[string]bool{"ACME-1": true},
	}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, log)

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"heads up","to":"channel:eng-help"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 0 {
		t.Fatalf("PostComment must not be called when channel resolution fails")
	}
}

// TestSendToDeniedIs403 asserts a target whose form no grant's addressing
// authorizes is denied (403) and — critically — nothing is delivered.
func TestSendToDeniedIs403(t *testing.T) {
	store := &fakeStore{
		actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7", Grants: []Grant{{Project: "acme", Role: "impl"}}}},
		instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
		roles:     map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}},
		rosters:   map[string]Roster{"acme": {Channels: []Channel{{Name: "secret", Ref: "X"}}}},
	}
	cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, log)

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
	log := slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
	h := NewMessagesHandler(store, cmt, log)

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
}

// TestSendNoTargetStillOwnTicket is the byte-for-byte regression guard: a POST
// with no "to" field must behave exactly as before this change — no
// DecideSend call, no @-mention prefix, body posted verbatim to the cove's
// own ticket.
func TestSendNoTargetStillOwnTicket(t *testing.T) {
	h, _, cmt, _ := newTestMessagesHandler()

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"body":"status"}`))
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(cmt.posted) != 1 {
		t.Fatalf("PostComment calls = %d, want 1", len(cmt.posted))
	}
	if got := cmt.posted[0]; got.issueID != "iss_7" || got.body != "status" {
		t.Fatalf("own-ticket path regressed: issue=%q body=%q, want (iss_7, status)", got.issueID, got.body)
	}
}

// bytesDiscard is an io.Writer that discards everything, used where a *bytes.Buffer
// isn't needed but slog.NewTextHandler still wants a writer.
type bytesDiscard struct{}

func (bytesDiscard) Write(p []byte) (int, error) { return len(p), nil }
