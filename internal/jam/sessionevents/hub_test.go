package sessionevents

import (
	"testing"
	"time"
)

func TestHubDeliversPerActor(t *testing.T) {
	h := NewHub()
	a := h.Subscribe("a", 4)
	defer a.Close()
	b := h.Subscribe("b", 4)
	defer b.Close()
	h.Publish(Event{ActorID: "a", Seq: 1})
	select {
	case ev := <-a.C:
		if ev.Seq != 1 {
			t.Fatal(ev)
		}
	case <-time.After(time.Second):
		t.Fatal("a got nothing")
	}
	select {
	case ev := <-b.C:
		t.Fatalf("b got %v", ev)
	default:
	}
}

func TestHubDropsSlowSubscriberWithoutBlocking(t *testing.T) {
	h := NewHub()
	s := h.Subscribe("a", 1)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.Publish(Event{ActorID: "a", Seq: uint64(i + 1)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	<-s.C // the one buffered event
	if _, ok := <-s.C; ok {
		t.Fatal("slow subscriber's channel should be closed")
	}
	s.Close() // idempotent after a drop
}
