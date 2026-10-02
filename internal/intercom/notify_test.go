package intercom

import (
	"testing"
	"time"
)

func newNotifier(t *testing.T) *Notifier {
	t.Helper()
	l := NewMemLog()
	return NewNotifier(l)
}

func hi() Squawk {
	return Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "hi"}
}

// signalled reports whether ch fires within a short wait.
func signalled(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(200 * time.Millisecond):
		return false
	}
}

func TestNotifierSignalsSubscribersOnAppend(t *testing.T) {
	n := newNotifier(t)
	a, cancelA := n.Subscribe()
	defer cancelA()
	b, cancelB := n.Subscribe()
	defer cancelB()
	if _, err := n.Append(hi()); err != nil {
		t.Fatal(err)
	}
	if !signalled(a) || !signalled(b) {
		t.Fatal("every subscriber should be signalled after a successful append")
	}
	// The wrapped store really got the message.
	if got := n.ListSince(0, 0); len(got) != 1 {
		t.Fatalf("store has %d messages, want 1", len(got))
	}
}

func TestNotifierCoalescesAndNeverBlocks(t *testing.T) {
	n := newNotifier(t)
	ch, cancel := n.Subscribe()
	defer cancel()
	// A subscriber that isn't reading must not block appends; a burst
	// collapses into one pending signal.
	for i := 0; i < 5; i++ {
		if _, err := n.Append(hi()); err != nil {
			t.Fatal(err)
		}
	}
	if !signalled(ch) {
		t.Fatal("want one pending signal after the burst")
	}
	if signalled(ch) {
		t.Fatal("a burst should coalesce into a single signal")
	}
}

func TestNotifierSkipsFailedAppendAndCancelledSubscribers(t *testing.T) {
	n := newNotifier(t)
	ch, cancel := n.Subscribe()
	if _, err := n.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}}); err == nil {
		t.Fatal("invalid squawk should fail")
	}
	if signalled(ch) {
		t.Fatal("a failed append must not signal")
	}
	cancel()
	cancel() // idempotent
	if _, err := n.Append(hi()); err != nil {
		t.Fatal(err)
	}
	if signalled(ch) {
		t.Fatal("a cancelled subscriber must not be signalled")
	}
}
