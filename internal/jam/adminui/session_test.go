package adminui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const sessSID = "0123456789abcdef0123456789abcdef"

func sessionUI(t *testing.T) (http.Handler, *sessionevents.FileStore, *sessionevents.Hub, *sessionevents.Ingest) {
	t.Helper()
	st, err := sessionevents.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := sessionevents.NewHub()
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil, adminui.WithSessions(st, hub))
	return h, st, hub, sessionevents.NewIngest(st, hub, nil)
}

func sessIn(seq uint64, raw string) sessionevents.Incoming {
	return sessionevents.Incoming{StreamID: sessSID, Seq: seq, Turn: 1, Raw: []byte(raw)}
}

// sse runs the SSE handler until stop() is called and returns the body.
func sse(t *testing.T, h http.Handler, path, lastID string) (stop func() string) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	return func() string { cancel(); <-done; return rec.Body.String() }
}

func TestSessionPageRenders(t *testing.T) {
	h, _, _, ing := sessionUI(t)
	ing.Append("w1", sessionevents.Stamp{}, sessIn(1, `{"type":"system","subtype":"init"}`))
	rec := get(t, h, "/ui/coves/w1/session")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/ui/coves/w1/session/events?stream="+sessSID) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionPageNotConfigured(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
	if rec := get(t, h, "/ui/coves/w1/session"); !strings.Contains(rec.Body.String(), "not configured") {
		t.Fatalf("%s", rec.Body.String())
	}
}

func TestSessionSSEBackfillThenLive(t *testing.T) {
	h, _, _, ing := sessionUI(t)
	ing.Append("w1", sessionevents.Stamp{}, sessIn(1, `{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`))
	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sessSID, "")
	time.Sleep(100 * time.Millisecond)
	ing.Append("w1", sessionevents.Stamp{}, sessIn(2, `{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`))
	time.Sleep(100 * time.Millisecond)
	body := stop()
	if !strings.Contains(body, "id: "+sessSID+":1") || !strings.Contains(body, "id: "+sessSID+":2") || strings.Index(body, "first") > strings.Index(body, "second") {
		t.Fatalf("body:\n%s", body)
	}
	if !strings.Contains(body, "event: totals") {
		t.Fatal("no totals event")
	}
}

func TestSessionSSEResumesFromLastEventID(t *testing.T) {
	h, _, _, ing := sessionUI(t)
	for i := uint64(1); i <= 3; i++ {
		ing.Append("w1", sessionevents.Stamp{}, sessIn(i, `{"type":"system","subtype":"init"}`))
	}
	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sessSID, sessSID+":2")
	time.Sleep(100 * time.Millisecond)
	body := stop()
	if strings.Contains(body, "id: "+sessSID+":1\n") || strings.Contains(body, "id: "+sessSID+":2\n") || !strings.Contains(body, "id: "+sessSID+":3\n") {
		t.Fatalf("resume sent wrong events:\n%s", body)
	}
}

func TestSessionSSESubscribeBeforeBackfill(t *testing.T) {
	// Events appended while the handler is starting must appear exactly once.
	h, _, _, ing := sessionUI(t)
	ing.Append("w1", sessionevents.Stamp{}, sessIn(1, `{}`))
	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sessSID, "")
	for i := uint64(2); i <= 50; i++ {
		ing.Append("w1", sessionevents.Stamp{}, sessIn(i, `{}`))
	}
	time.Sleep(200 * time.Millisecond)
	body := stop()
	for i := 1; i <= 50; i++ {
		id := "id: " + sessSID + ":" + strconv.Itoa(i) + "\n"
		if n := strings.Count(body, id); n != 1 {
			t.Fatalf("seq %d delivered %d times", i, n)
		}
	}
}

func TestSessionRendersAgentOutputInert(t *testing.T) {
	h, _, _, ing := sessionUI(t)
	ing.Append("w1", sessionevents.Stamp{}, sessIn(1, `{"type":"user","message":{"content":[{"type":"tool_result","content":"<script>alert(1)</script>","is_error":true}]}}`))
	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sessSID, "")
	time.Sleep(100 * time.Millisecond)
	body := stop()
	if strings.Contains(body, "<script>alert") {
		t.Fatalf("unescaped agent output:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("escaped output missing:\n%s", body)
	}
}
