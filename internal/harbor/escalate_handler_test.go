package harbor

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeEscStore is a minimal escalateStore: canned actor-by-token-hash and
// instance-by-actor-id, so tests don't need a real FileStore. Mirrors
// fakeStore in messages_test.go.
type fakeEscStore struct {
	actors    map[string]Actor    // tokenHash -> Actor
	instances map[string]Instance // actorID -> Instance
}

func newFakeEscStore() *fakeEscStore {
	return &fakeEscStore{actors: map[string]Actor{}, instances: map[string]Instance{}}
}

func (f *fakeEscStore) Lookup(tokenHash string) (Actor, bool) {
	a, ok := f.actors[tokenHash]
	return a, ok
}

func (f *fakeEscStore) GetInstance(actorID string) (Instance, bool) {
	i, ok := f.instances[actorID]
	return i, ok
}

// fakeCategorySetter records SetEscalationCategory calls.
type fakeCategorySetter struct {
	actorID  string
	category string
	called   bool
	err      error
}

func (f *fakeCategorySetter) SetEscalationCategory(actorID, category string) error {
	f.called = true
	f.actorID = actorID
	f.category = category
	return f.err
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(bytesDiscard{}, nil))
}

// postEscalate issues a POST /escalate with the given bearer token and raw
// JSON body, returning the recorder.
func postEscalate(t *testing.T, h http.Handler, tok, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/escalate", strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEscalateStampsCategory(t *testing.T) {
	st := newFakeEscStore()
	st.actors[HashToken("tok-for-h")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
	setter := &fakeCategorySetter{}
	h := NewEscalateHandler(st, setter, testLogger())

	rec := postEscalate(t, h, "tok-for-h", `{"category":"infra"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if setter.actorID != "cove-1" || setter.category != "infra" {
		t.Fatalf("SetEscalationCategory(%q,%q), want (cove-1, infra)", setter.actorID, setter.category)
	}
}

func TestEscalateMissingTokenIs401(t *testing.T) {
	st := newFakeEscStore()
	setter := &fakeCategorySetter{}
	h := NewEscalateHandler(st, setter, testLogger())

	rec := postEscalate(t, h, "", `{"category":"infra"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
	if setter.called {
		t.Fatal("SetEscalationCategory should not have been called")
	}
}

func TestEscalateUnknownTokenIs401(t *testing.T) {
	st := newFakeEscStore()
	setter := &fakeCategorySetter{}
	h := NewEscalateHandler(st, setter, testLogger())

	rec := postEscalate(t, h, "not-a-real-token", `{"category":"infra"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
	if setter.called {
		t.Fatal("SetEscalationCategory should not have been called")
	}
}

func TestEscalateNoInstanceIs403(t *testing.T) {
	st := newFakeEscStore()
	st.actors[HashToken("tok-B")] = Actor{ID: "cove-no-instance"}
	setter := &fakeCategorySetter{}
	h := NewEscalateHandler(st, setter, testLogger())

	rec := postEscalate(t, h, "tok-B", `{"category":"infra"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	if setter.called {
		t.Fatal("SetEscalationCategory should not have been called")
	}
}

func TestEscalateOversizeBodyIs413(t *testing.T) {
	st := newFakeEscStore()
	st.actors[HashToken("tok-A")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
	setter := &fakeCategorySetter{}
	h := NewEscalateHandler(st, setter, testLogger())

	huge := strings.Repeat("a", 32*1024)
	rec := postEscalate(t, h, "tok-A", `{"category":"`+huge+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413", rec.Code)
	}
	if setter.called {
		t.Fatal("SetEscalationCategory should not have been called for an oversize body")
	}
}

func TestEscalateGetIs405(t *testing.T) {
	st := newFakeEscStore()
	st.actors[HashToken("tok-A")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
	setter := &fakeCategorySetter{}
	h := NewEscalateHandler(st, setter, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/escalate", nil)
	req.Header.Set("Authorization", "Bearer tok-A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405", rec.Code)
	}
	if setter.called {
		t.Fatal("SetEscalationCategory should not have been called")
	}
}

func TestEscalateEmptyCategoryAllowed(t *testing.T) {
	st := newFakeEscStore()
	st.actors[HashToken("tok-A")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
	setter := &fakeCategorySetter{}
	h := NewEscalateHandler(st, setter, testLogger())

	rec := postEscalate(t, h, "tok-A", `{"category":""}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if !setter.called || setter.actorID != "cove-1" || setter.category != "" {
		t.Fatalf("SetEscalationCategory(%q,%q) called=%v, want (cove-1, \"\") called=true", setter.actorID, setter.category, setter.called)
	}
}

// TestEscalateNeverLogsToken asserts the bearer token string never appears in
// logged output, across success and failure paths.
func TestEscalateNeverLogsToken(t *testing.T) {
	st := newFakeEscStore()
	st.actors[HashToken("tok-A")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
	setter := &fakeCategorySetter{}
	var logbuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := NewEscalateHandler(st, setter, log)

	const tok = "tok-A"
	rec := postEscalate(t, h, tok, `{"category":"infra"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204", rec.Code)
	}

	rec2 := postEscalate(t, h, "some-other-secret-token", `{"category":"infra"}`)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec2.Code)
	}

	if strings.Contains(logbuf.String(), tok) {
		t.Fatalf("token leaked into logs: %s", logbuf.String())
	}
	if strings.Contains(logbuf.String(), "some-other-secret-token") {
		t.Fatalf("token leaked into logs: %s", logbuf.String())
	}
}
