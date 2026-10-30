package notify

import (
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/push"
)

func ev(id string) Event { return Event{Payload: push.Payload{ID: id}} }

func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

func empty(ch <-chan Event) bool {
	select {
	case <-ch:
		return false
	default:
		return true
	}
}

func TestHubFansOutToEachSubscriberOfTheUser(t *testing.T) {
	h := NewHub(nil)
	a, ca := h.Subscribe("usr_alice")
	b, cb := h.Subscribe("usr_alice")
	defer ca()
	defer cb()
	h.Publish("usr_alice", ev("sqk_1"))
	if recv(t, a).ID != "sqk_1" || recv(t, b).ID != "sqk_1" {
		t.Fatal("wrong event")
	}
	if h.Subscribers("usr_alice") != 2 {
		t.Fatalf("subscribers = %d", h.Subscribers("usr_alice"))
	}
}

func TestHubOtherUsersGetNothing(t *testing.T) {
	h := NewHub(nil)
	_, ca := h.Subscribe("usr_alice")
	bob, cb := h.Subscribe("usr_bob")
	defer ca()
	defer cb()
	h.Publish("usr_alice", ev("sqk_1"))
	if !empty(bob) {
		t.Fatal("bob received alice's event")
	}
}

func TestHubPublishNeverBlocks(t *testing.T) {
	h := NewHub(nil)
	ch, cancel := h.Subscribe("usr_alice")
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < SubBuffer+1; i++ {
			h.Publish("usr_alice", ev("sqk"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Publish blocked")
	}
	n := 0
	for !empty(ch) {
		n++
	}
	if n != SubBuffer {
		t.Fatalf("got %d events, want %d", n, SubBuffer)
	}
}

func TestHubCancelStopsDeliveryAndIsIdempotent(t *testing.T) {
	h := NewHub(nil)
	ch, cancel := h.Subscribe("usr_alice")
	cancel()
	cancel()
	h.Publish("usr_alice", ev("sqk_1"))
	if !empty(ch) {
		t.Fatal("delivered after cancel")
	}
	if h.Subscribers("usr_alice") != 0 {
		t.Fatal("still subscribed")
	}
}

func TestHubConcurrentPublishSubscribe(t *testing.T) {
	h := NewHub(nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Publish(ident.ID("usr_alice"), ev("x"))
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, cancel := h.Subscribe("usr_alice")
				cancel()
			}
		}()
	}
	wg.Wait()
}
