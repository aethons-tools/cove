package sessionview_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// fill stores seqs 1..n of stream for actor w1, received at base+seq seconds.
func fill(t *testing.T, st sessionevents.Store, stream string, n int, base time.Time) {
	t.Helper()
	for i := 1; i <= n; i++ {
		ev := sessionevents.Event{ActorID: "w1", StreamID: stream, Seq: uint64(i), Turn: 1, Kind: sessionevents.KindEvent,
			ReceivedAt: base.Add(time.Duration(i) * time.Second), Raw: []byte(`{}`),
			Index: sessionevents.Index{Type: "assistant", ToolName: "Bash"}}
		if err := st.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
}

func evFrag(v sessionview.EventView) string { return fmt.Sprintf("seq=%d", v.Seq) }

// serve runs Serve in a goroutine; stop cancels it and returns the body,
// done reports when the handler returned on its own.
func serve(src sessionview.Source, query, lastID string) (stop func() string, done <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/events"+query, nil).WithContext(ctx)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	rec := httptest.NewRecorder()
	d := make(chan struct{})
	go func() { sessionview.Serve(rec, req, src); close(d) }()
	return func() string { cancel(); <-d; return rec.Body.String() }, d
}

func TestServeTail(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 10, time.Unix(1000, 0))
	stop, _ := serve(sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag}, "?stream="+streamA, "")
	time.Sleep(100 * time.Millisecond)
	body := stop()
	for _, s := range []int{8, 9, 10} {
		if !strings.Contains(body, fmt.Sprintf("id: %s:%d\n", streamA, s)) {
			t.Fatalf("missing seq %d:\n%s", s, body)
		}
	}
	if strings.Contains(body, fmt.Sprintf("id: %s:7\n", streamA)) {
		t.Fatalf("seq 7 sent outside the tail:\n%s", body)
	}
	if !strings.Contains(body, "event: more\ndata: 8\n") {
		t.Fatalf("no more event:\n%s", body)
	}
	if strings.Contains(body, "event: totals") {
		t.Fatalf("totals sent with nil Totals:\n%s", body)
	}
}

func TestServeTailNoMoreFromStart(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 3, time.Unix(1000, 0))
	stop, _ := serve(sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag}, "?stream="+streamA, "")
	time.Sleep(100 * time.Millisecond)
	if body := stop(); strings.Contains(body, "event: more") {
		t.Fatalf("more sent though seq 1 was sent:\n%s", body)
	}
}

func TestServeTailResumeSkipped(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 10, time.Unix(1000, 0))
	stop, _ := serve(sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag}, "", streamA+":2")
	time.Sleep(100 * time.Millisecond)
	body := stop()
	sk := strings.Index(body, "event: skipped\ndata: 3-7\n")
	if sk < 0 {
		t.Fatalf("no skipped 3-7:\n%s", body)
	}
	for _, s := range []int{8, 9, 10} {
		i := strings.Index(body, fmt.Sprintf("id: %s:%d\n", streamA, s))
		if i < sk {
			t.Fatalf("seq %d missing or before skipped:\n%s", s, body)
		}
	}
	for _, s := range []int{1, 2, 3, 7} {
		if strings.Contains(body, fmt.Sprintf("id: %s:%d\n", streamA, s)) {
			t.Fatalf("seq %d sent:\n%s", s, body)
		}
	}
}

func TestServeTailTotalsWholeStream(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 10, time.Unix(1000, 0))
	var seen []sessionevents.Totals
	src := sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: evFrag,
		Totals: func(tot sessionevents.Totals) string { seen = append(seen, tot); return fmt.Sprint(tot.ToolCalls) }}
	stop, _ := serve(src, "?stream="+streamA, "")
	time.Sleep(100 * time.Millisecond)
	body := stop()
	if !strings.Contains(body, "event: totals\n") || len(seen) != 1 || seen[0].ToolCalls != 10 {
		t.Fatalf("totals: %+v\n%s", seen, body)
	}
}

func TestServeFollowNewestNewstream(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 2, time.Unix(1000, 0))
	stop, done := serve(sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, FollowNewest: true, Event: evFrag}, "", "")
	time.Sleep(100 * time.Millisecond)
	hub.Publish(sessionevents.Event{ActorID: "w1", StreamID: streamB, Seq: 1, Kind: sessionevents.KindEvent})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("handler did not return after newstream:\n%s", stop())
	}
	body := stop()
	if !strings.Contains(body, "event: stream\ndata: "+streamA+"\n") || !strings.Contains(body, fmt.Sprintf("id: %s:2\n", streamA)) {
		t.Fatalf("stream A not backfilled:\n%s", body)
	}
	if !strings.Contains(body, "event: newstream\ndata: "+streamB+"\n") {
		t.Fatalf("no newstream:\n%s", body)
	}
	if strings.Contains(body, "id: "+streamB) {
		t.Fatalf("stream B event sent on A's feed:\n%s", body)
	}
}

func TestServeDroppedSubscriberEnds(t *testing.T) {
	st, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	fill(t, st, streamA, 1, time.Unix(1000, 0))
	block := make(chan struct{})
	ev := func(v sessionview.EventView) string {
		if v.Seq == 2 {
			<-block // stall the handler so its subscription overflows
		}
		return evFrag(v)
	}
	stop, done := serve(sessionview.Source{Store: st, Hub: hub, ActorID: "w1", Tail: 3, Event: ev}, "?stream="+streamA, "")
	time.Sleep(100 * time.Millisecond)
	for i := uint64(2); i <= 2000; i++ {
		hub.Publish(sessionevents.Event{ActorID: "w1", StreamID: streamA, Seq: i, Kind: sessionevents.KindEvent})
	}
	close(block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler still running after its subscriber was dropped")
	}
	_ = stop()
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
