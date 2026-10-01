package meui

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// fakeChanges is a Changes source the test fires by hand.
type fakeChanges struct{ ch chan struct{} }

func (f *fakeChanges) Subscribe() (<-chan struct{}, func()) { return f.ch, func() {} }

// eventsServer serves h with the participant injected, as the /me gate would.
func eventsServer(t *testing.T, h http.Handler, p jam.Participant) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, jam.WithParticipant(r, p))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readUntil scans the SSE body for a line equal to want, failing on timeout.
func readUntil(t *testing.T, sc *bufio.Scanner, want string) {
	t.Helper()
	found := make(chan bool, 1)
	go func() {
		for sc.Scan() {
			if sc.Text() == want {
				found <- true
				return
			}
		}
		found <- false
	}()
	select {
	case ok := <-found:
		if !ok {
			t.Fatalf("stream ended before %q", want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

func TestEventsStreamsChangedOnEachSignal(t *testing.T) {
	store, log, p := fixture()
	fc := &fakeChanges{ch: make(chan struct{}, 1)}
	srv := eventsServer(t, Handler(store, log, nil, WithChanges(fc)), p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/me/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events = %d %q, want 200 text/event-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	fc.ch <- struct{}{}
	readUntil(t, sc, "event: changed")
	fc.ch <- struct{}{}
	readUntil(t, sc, "event: changed")
}

func TestEventsWithoutChangeSourceIs204(t *testing.T) {
	// 204 tells EventSource to stop reconnecting; the page falls back to polling.
	store, log, p := fixture()
	srv := eventsServer(t, Handler(store, log, nil), p)
	resp, err := http.Get(srv.URL + "/me/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("events without a change source = %d, want 204", resp.StatusCode)
	}
}

func TestEventsRequiresParticipant(t *testing.T) {
	store, log, _ := fixture()
	h := Handler(store, log, nil, WithChanges(&fakeChanges{ch: make(chan struct{})}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/me/events", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("events without a participant = %d, want 403", rec.Code)
	}
}
