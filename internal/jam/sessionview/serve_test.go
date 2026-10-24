package sessionview_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionview"
)

const (
	streamA = "0123456789abcdef0123456789abcdef"
	streamB = "fedcba9876543210fedcba9876543210"
)

func TestWriteSSEFraming(t *testing.T) {
	cases := []struct{ name, event, id, data, want string }{
		{"plain", "ev", "a:1", "x", "id: a:1\nevent: ev\ndata: x\n\n"},
		{"no id", "ev", "", "x", "event: ev\ndata: x\n\n"},
		{"CR in data", "ev", "", "a\rb", "event: ev\ndata: a\ndata: b\n\n"},
		{"CRLF in data", "ev", "", "a\r\nb", "event: ev\ndata: a\ndata: b\n\n"},
		{"LF CR mix", "ev", "", "a\n\rb", "event: ev\ndata: a\ndata: \ndata: b\n\n"},
		{"CR in id", "ev", "a\rretry: 1", "x", "id: a retry: 1\nevent: ev\ndata: x\n\n"},
		{"CRLF in id", "ev", "a\r\nb", "x", "id: a b\nevent: ev\ndata: x\n\n"},
		{"CR in event", "e\rid: z", "", "x", "event: e id: z\ndata: x\n\n"},
		{"CRLF in event", "e\r\nf", "", "x", "event: e f\ndata: x\n\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sessionview.WriteSSE(rec, c.event, c.id, c.data)
			if got := rec.Body.String(); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
			if strings.Contains(strings.ReplaceAll(rec.Body.String(), "\r\n", ""), "\r") {
				t.Fatal("raw CR in output")
			}
		})
	}
}

// fill stores seqs from..to of stream for actor w1, received at base+seq seconds.
func fill(t *testing.T, st sessionevents.Store, stream string, from, to int, base time.Time) {
	t.Helper()
	for i := from; i <= to; i++ {
		ev := sessionevents.Event{ActorID: "w1", StreamID: stream, Seq: uint64(i), Turn: 1, Kind: sessionevents.KindEvent,
			ReceivedAt: base.Add(time.Duration(i) * time.Second), Raw: []byte(`{}`),
			Index: sessionevents.Index{Type: "assistant", ToolName: "Bash"}}
		if err := st.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
}

func evFrag(v sessionview.EventView) string { return fmt.Sprintf("seq=%d", v.Seq) }

func idLine(stream string, seq int) string { return fmt.Sprintf("id: %s:%d\n", stream, seq) }

// feed is a live SSE response read in the background.
type feed struct {
	mu   sync.Mutex
	body strings.Builder
	eof  chan struct{}
}

// open serves src over a real server and starts reading its response.
func open(t *testing.T, src sessionview.Source, query, lastID string) *feed {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sessionview.Serve(w, r, src) }))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // runs before srv.Close, ending the handler
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	f := &feed{eof: make(chan struct{})}
	go func() {
		defer close(f.eof)
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			f.mu.Lock()
			f.body.Write(buf[:n])
			f.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return f
}

func (f *feed) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.body.String()
}

// waitFor blocks until the body contains s.
func (f *feed) waitFor(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(f.String(), s) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in:\n%s", s, f.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitEOF blocks until the handler has ended the response.
func (f *feed) waitEOF(t *testing.T) {
	t.Helper()
	select {
	case <-f.eof:
	case <-time.After(5 * time.Second):
		t.Fatalf("response still open:\n%s", f.String())
	}
}

// barrier publishes a live event and waits for it: everything the handler
// wrote before it (the backfill) is then in the body.
func (f *feed) barrier(t *testing.T, hub *sessionevents.Hub, stream string, seq int) {
	t.Helper()
	hub.Publish(sessionevents.Event{ActorID: "w1", StreamID: stream, Seq: uint64(seq), Kind: sessionevents.KindEvent})
	f.waitFor(t, idLine(stream, seq))
}

func TestServeTail(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 10, time.Unix(1000, 0))
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag}, "?stream="+streamA, "")
	f.barrier(t, hub, streamA, 11)
	body := f.String()
	for _, s := range []int{8, 9, 10} {
		if !strings.Contains(body, idLine(streamA, s)) {
			t.Fatalf("missing seq %d:\n%s", s, body)
		}
	}
	if strings.Contains(body, idLine(streamA, 7)) {
		t.Fatalf("seq 7 sent outside the tail:\n%s", body)
	}
	if !strings.Contains(body, "event: more\ndata: 8\n") {
		t.Fatalf("no more event:\n%s", body)
	}
	if strings.Contains(body, "event: totals") {
		t.Fatalf("totals sent with nil Totals:\n%s", body)
	}
}

func TestServeTailLiveDedup(t *testing.T) {
	// A live event the tail backfill already sent (published again, as when
	// it raced the backfill) is not sent twice; a new one is.
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 10, time.Unix(1000, 0))
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag}, "?stream="+streamA, "")
	f.waitFor(t, idLine(streamA, 10))
	hub.Publish(sessionevents.Event{ActorID: "w1", StreamID: streamA, Seq: 10, Kind: sessionevents.KindEvent})
	f.barrier(t, hub, streamA, 11)
	if n := strings.Count(f.String(), idLine(streamA, 10)); n != 1 {
		t.Fatalf("seq 10 sent %d times:\n%s", n, f.String())
	}
}

func TestServeTailNoMoreFromStart(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 3, time.Unix(1000, 0))
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag}, "?stream="+streamA, "")
	f.barrier(t, hub, streamA, 4)
	if body := f.String(); strings.Contains(body, "event: more") {
		t.Fatalf("more sent though seq 1 was sent:\n%s", body)
	}
}

func TestServeTailResumeNoMore(t *testing.T) {
	// The client already holds everything up to the resume point.
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 10, time.Unix(1000, 0))
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag}, "", streamA+":8")
	f.barrier(t, hub, streamA, 11)
	body := f.String()
	if !strings.Contains(body, idLine(streamA, 9)) || !strings.Contains(body, idLine(streamA, 10)) {
		t.Fatalf("resume did not send 9 and 10:\n%s", body)
	}
	if strings.Contains(body, "event: more") || strings.Contains(body, "event: skipped") {
		t.Fatalf("more/skipped sent on a gapless resume:\n%s", body)
	}
}

func TestServeTailResumeSkipped(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 10, time.Unix(1000, 0))
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag}, "", streamA+":2")
	f.barrier(t, hub, streamA, 11)
	body := f.String()
	sk := strings.Index(body, "event: skipped\ndata: 3-7\n")
	if sk < 0 {
		t.Fatalf("no skipped 3-7:\n%s", body)
	}
	for _, s := range []int{8, 9, 10} {
		if i := strings.Index(body, idLine(streamA, s)); i < sk {
			t.Fatalf("seq %d missing or before skipped:\n%s", s, body)
		}
	}
	for _, s := range []int{1, 2, 3, 7} {
		if strings.Contains(body, idLine(streamA, s)) {
			t.Fatalf("seq %d sent:\n%s", s, body)
		}
	}
	if strings.Contains(body, "event: more") {
		t.Fatalf("more sent alongside skipped:\n%s", body)
	}
}

func TestServeTailTotalsWholeStream(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 10, time.Unix(1000, 0))
	src := sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag,
		Totals: func(tot sessionevents.Totals) string { return fmt.Sprintf("tools=%d", tot.ToolCalls) }}
	f := open(t, src, "?stream="+streamA, "")
	f.waitFor(t, "event: totals\ndata: tools=10\n")
}

func TestServeFollowNewestNewstream(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 2, time.Unix(1000, 0))
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, FollowNewest: true, Event: evFrag}, "", "")
	f.waitFor(t, "event: stream\ndata: "+streamA+"\n")
	f.waitFor(t, idLine(streamA, 2))               // backfilled, so subscribed
	fill(t, st, streamB, 1, 1, time.Unix(2000, 0)) // ingest persists, then publishes
	hub.Publish(sessionevents.Event{ActorID: "w1", StreamID: streamB, Seq: 1, Kind: sessionevents.KindEvent})
	f.waitEOF(t)
	body := f.String()
	if !strings.Contains(body, "event: newstream\ndata: "+streamB+"\n") {
		t.Fatalf("no newstream:\n%s", body)
	}
	if strings.Contains(body, "id: "+streamB) {
		t.Fatalf("stream B event sent on A's feed:\n%s", body)
	}
}

func TestServeResumeStaleStreamNewstream(t *testing.T) {
	// A reconnect resumes A via Last-Event-ID, but B is now the newest: the
	// feed must move to B rather than stay on A.
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 3, time.Unix(1000, 0))
	fill(t, st, streamB, 1, 2, time.Unix(2000, 0))
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, FollowNewest: true, Event: evFrag}, "", streamA+":1")
	f.waitEOF(t)
	body := f.String()
	if !strings.Contains(body, "event: newstream\ndata: "+streamB+"\n") {
		t.Fatalf("no newstream to B:\n%s", body)
	}
	if strings.Contains(body, "event: ev") || strings.Contains(body, "event: stream\n") {
		t.Fatalf("stale stream backfilled:\n%s", body)
	}
}

func TestServePinnedStreamNoNewstream(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 2, time.Unix(1000, 0))
	fill(t, st, streamB, 1, 1, time.Unix(2000, 0)) // B is the newest
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, FollowNewest: true, Event: evFrag}, "?stream="+streamA, "")
	f.waitFor(t, idLine(streamA, 2))
	hub.Publish(sessionevents.Event{ActorID: "w1", StreamID: streamB, Seq: 2, Kind: sessionevents.KindEvent})
	f.barrier(t, hub, streamA, 3)
	if body := f.String(); strings.Contains(body, "event: newstream") || strings.Contains(body, "id: "+streamB) {
		t.Fatalf("pinned stream switched:\n%s", body)
	}
}

func TestServeOlderStreamNoNewstream(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 2, time.Unix(1000, 0))
	fill(t, st, streamB, 1, 2, time.Unix(2000, 0)) // B is the newest, followed
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, FollowNewest: true, Event: evFrag}, "", "")
	f.waitFor(t, idLine(streamB, 2))
	fill(t, st, streamA, 3, 3, time.Unix(3000, 0)) // a late event on the older stream
	hub.Publish(sessionevents.Event{ActorID: "w1", StreamID: streamA, Seq: 3, Kind: sessionevents.KindEvent})
	f.barrier(t, hub, streamB, 3)
	if body := f.String(); strings.Contains(body, "event: newstream") || strings.Contains(body, idLine(streamA, 3)) {
		t.Fatalf("older stream's event switched the feed:\n%s", body)
	}
}

func TestServeDroppedSubscriberEnds(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 1, time.Unix(1000, 0))
	block := make(chan struct{})
	ev := func(v sessionview.EventView) string {
		if v.Seq == 2 {
			<-block // stall the handler so its subscription overflows
		}
		return evFrag(v)
	}
	f := open(t, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: ev}, "?stream="+streamA, "")
	f.waitFor(t, idLine(streamA, 1)) // backfilled, so subscribed
	for i := uint64(2); i <= 2000; i++ {
		hub.Publish(sessionevents.Event{ActorID: "w1", StreamID: streamA, Seq: i, Kind: sessionevents.KindEvent})
	}
	close(block)
	f.waitEOF(t)
}

func TestServeRejectsBadStream(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	req := httptest.NewRequest("GET", "/events?stream=nope", nil)
	rec := httptest.NewRecorder()
	sessionview.Serve(rec, req, sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Event: evFrag})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d", rec.Code)
	}
}

// Serve re-checks Allowed on each keep-alive tick and ends the feed once it
// reports false.
func TestServeAllowedRevoked(t *testing.T) {
	old := sessionview.KeepAlive
	sessionview.KeepAlive = 10 * time.Millisecond
	defer func() { sessionview.KeepAlive = old }()
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, 1, time.Unix(1000, 0))
	var mu sync.Mutex
	allowed := true
	src := sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag,
		Allowed: func() bool { mu.Lock(); defer mu.Unlock(); return allowed }}
	f := open(t, src, "?stream="+streamA, "")
	f.waitFor(t, idLine(streamA, 1))
	f.waitFor(t, ": keep-alive") // still allowed: the feed stays open
	select {
	case <-f.eof:
		t.Fatal("feed ended while allowed")
	default:
	}
	mu.Lock()
	allowed = false
	mu.Unlock()
	f.waitEOF(t)
}
