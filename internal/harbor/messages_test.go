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
// instance-by-actor-id, so tests don't need a real FileStore.
type fakeStore struct {
	actors    map[string]Actor    // tokenHash -> Actor
	instances map[string]Instance // actorID -> Instance
}

func (f *fakeStore) Lookup(tokenHash string) (Actor, bool) {
	a, ok := f.actors[tokenHash]
	return a, ok
}

func (f *fakeStore) GetInstance(actorID string) (Instance, bool) {
	i, ok := f.instances[actorID]
	return i, ok
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

// bytesDiscard is an io.Writer that discards everything, used where a *bytes.Buffer
// isn't needed but slog.NewTextHandler still wants a writer.
type bytesDiscard struct{}

func (bytesDiscard) Write(p []byte) (int, error) { return len(p), nil }
