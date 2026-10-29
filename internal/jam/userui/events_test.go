package userui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/notify"
	"github.com/aethons-tools/cove/internal/jam/push"
)

// fakeChanges is a Changes source the test fires by hand.
type fakeChanges struct{ ch chan struct{} }

func (f *fakeChanges) Subscribe() (<-chan struct{}, func()) { return f.ch, func() {} }

// eventsServer serves h with the participant injected, as the /app/user gate would.
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
	e, p := fixture()
	fc := &fakeChanges{ch: make(chan struct{}, 1)}
	srv := eventsServer(t, Handler(e.Deps, nil, WithChanges(fc)), p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/app/user/events", nil)
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
	e, p := fixture()
	srv := eventsServer(t, Handler(e.Deps, nil), p)
	resp, err := http.Get(srv.URL + "/app/user/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("events without a change source = %d, want 204", resp.StatusCode)
	}
}

func TestEventsRequiresParticipant(t *testing.T) {
	e, _ := fixture()
	h := Handler(e.Deps, nil, WithChanges(&fakeChanges{ch: make(chan struct{})}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/app/user/events", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("events without a participant = %d, want 403", rec.Code)
	}
}

func openStream(t *testing.T, url string) (*bufio.Scanner, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", url+"/app/user/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events = %d, want 200", resp.StatusCode)
	}
	return bufio.NewScanner(resp.Body), cancel
}

func waitSubscribers(t *testing.T, hub *notify.Hub, u jam.Participant, want int) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if hub.Subscribers(u.UserID) == want {
			return
		}
	}
	t.Fatalf("subscribers = %d, want %d", hub.Subscribers(u.UserID), want)
}

func TestEventsStreamsNotifyToItsRecipient(t *testing.T) {
	e, p := fixture()
	hub := notify.NewHub(nil)
	srv := eventsServer(t, Handler(e.Deps, nil, WithNotify(hub)), p)
	sc, _ := openStream(t, srv.URL)
	waitSubscribers(t, hub, p, 1)
	ev := notify.Event{Payload: push.Payload{Title: "bob · eng", Body: "hi", Channel: engID, ID: "sq1", Unread: 2, Kind: "messages"}, Session: "ses_01j9q3ffffffffffffffffffff"}
	hub.Publish(p.UserID, ev)
	readUntil(t, sc, "event: notify")
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), "data: ") {
		t.Fatalf("no data line: %q", sc.Text())
	}
	var got notify.Event
	if err := json.Unmarshal([]byte(strings.TrimPrefix(sc.Text(), "data: ")), &got); err != nil || got != ev {
		t.Fatalf("data = %q (%v), want %+v", sc.Text(), err, ev)
	}
}

func TestEventsNotifyNeverReachesAnotherUser(t *testing.T) {
	e, alice := fixture()
	bob := jam.Participant{Issuer: "https://idp", Subject: "sub-bob", UserID: "usr_bob", Projects: alice.Projects, Name: "bob"}
	hub := notify.NewHub(nil)
	fc := &fakeChanges{ch: make(chan struct{}, 1)}
	h := Handler(e.Deps, nil, WithNotify(hub), WithChanges(fc))
	srvA := eventsServer(t, h, alice)
	srvB := eventsServer(t, h, bob)
	scA, _ := openStream(t, srvA.URL)
	_, _ = openStream(t, srvB.URL)
	waitSubscribers(t, hub, alice, 1)
	waitSubscribers(t, hub, bob, 1)
	hub.Publish(bob.UserID, notify.Event{Payload: push.Payload{Title: "secret"}})
	fc.ch <- struct{}{}
	for scA.Scan() {
		if scA.Text() == "event: notify" || strings.Contains(scA.Text(), "secret") {
			t.Fatalf("alice saw bob's notification: %q", scA.Text())
		}
		if scA.Text() == "event: changed" {
			return
		}
	}
	t.Fatal("stream ended before changed")
}

func TestEventsWithOnlyNotifyIsAStream(t *testing.T) {
	e, p := fixture()
	srv := eventsServer(t, Handler(e.Deps, nil, WithNotify(notify.NewHub(nil))), p)
	resp, err := http.Get(srv.URL + "/app/user/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events = %d %q, want 200 text/event-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestEventsNotifyUnsubscribesOnDisconnect(t *testing.T) {
	e, p := fixture()
	hub := notify.NewHub(nil)
	srv := eventsServer(t, Handler(e.Deps, nil, WithNotify(hub)), p)
	_, cancel := openStream(t, srv.URL)
	waitSubscribers(t, hub, p, 1)
	cancel()
	waitSubscribers(t, hub, p, 0)
}
