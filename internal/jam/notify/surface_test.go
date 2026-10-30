package notify

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam/push"
	"github.com/aethons-tools/cove/internal/relay"
)

var t0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

type harness struct {
	s     *Surface
	calls int
}

func newHarness(ok bool) *harness {
	h := &harness{}
	h.s = &Surface{
		Hub: NewHub(nil),
		Event: func(m intercom.Squawk, user ident.ID) (Event, bool) {
			h.calls++
			return Event{Payload: push.Payload{ID: m.ID, Title: "t"}, Session: "ses_y"}, ok
		},
		Now: func() time.Time { return t0 },
	}
	return h
}

func deliver(h *harness, at time.Time) (string, error) {
	return h.s.Deliver(context.Background(),
		relay.Delivery{Service: Service, Address: "usr_alice"},
		intercom.Squawk{ID: "sqk_1", At: at})
}

func TestSurfaceDeliverPublishesTheEvent(t *testing.T) {
	h := newHarness(true)
	ch, cancel := h.s.Hub.Subscribe("usr_alice")
	defer cancel()
	rcpt, err := deliver(h, t0)
	if rcpt != "" || err != nil {
		t.Fatalf("got %q, %v", rcpt, err)
	}
	if e := recv(t, ch); e.ID != "sqk_1" || e.Session != "ses_y" {
		t.Fatalf("event = %+v", e)
	}
}

func TestSurfaceDropsSquawksOlderThanMaxAge(t *testing.T) {
	h := newHarness(true)
	ch, cancel := h.s.Hub.Subscribe("usr_alice")
	defer cancel()
	if rcpt, err := deliver(h, t0.Add(-11*time.Minute)); rcpt != "" || err != nil {
		t.Fatalf("got %q, %v", rcpt, err)
	}
	if h.calls != 0 || !empty(ch) {
		t.Fatalf("calls=%d, published=%v", h.calls, !empty(ch))
	}
}

func TestSurfaceSkipsWhenNoLongerARecipient(t *testing.T) {
	h := newHarness(false)
	ch, cancel := h.s.Hub.Subscribe("usr_alice")
	defer cancel()
	if rcpt, err := deliver(h, t0); rcpt != "" || err != nil {
		t.Fatalf("got %q, %v", rcpt, err)
	}
	if h.calls != 1 || !empty(ch) {
		t.Fatalf("calls=%d", h.calls)
	}
}

func TestSurfaceSkipsUsersWithNoStream(t *testing.T) {
	h := newHarness(true)
	if rcpt, err := deliver(h, t0); rcpt != "" || err != nil {
		t.Fatalf("got %q, %v", rcpt, err)
	}
	if h.calls != 0 {
		t.Fatal("Event called with no subscriber")
	}
}

func TestSurfaceHasNoIngress(t *testing.T) {
	h := newHarness(true)
	evs, next, err := h.s.Poll(context.Background(), "p", "s")
	if evs != nil || next != "" || err != nil {
		t.Fatalf("got %v %q %v", evs, next, err)
	}
	if h.s.Service() != "notify" {
		t.Fatalf("service = %q", h.s.Service())
	}
	if h.s.Close() != nil {
		t.Fatal("close")
	}
}

func TestEventJSON(t *testing.T) {
	got, err := json.Marshal(Event{push.Payload{Title: "a · b", Body: "hi", Channel: "chn_x", ID: "sqk_1", Unread: 2, Kind: "messages"}, "ses_y"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"title":"a · b","body":"hi","channel":"chn_x","id":"sqk_1","unread":2,"kind":"messages","session":"ses_y"}`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
}
