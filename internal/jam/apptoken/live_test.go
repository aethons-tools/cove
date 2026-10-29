package apptoken

import (
	"context"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

func TestLiveRevokeCancelsTracked(t *testing.T) {
	l := NewLive()
	a1, doneA1 := l.Track(context.Background(), "apt_a")
	a2, doneA2 := l.Track(context.Background(), "apt_a")
	b, doneB := l.Track(context.Background(), "apt_b")
	defer doneA1()
	defer doneA2()
	defer doneB()
	l.Revoke("apt_a")
	for _, c := range []context.Context{a1, a2} {
		select {
		case <-c.Done():
		default:
			t.Fatal("tracked ctx not cancelled")
		}
	}
	if b.Err() != nil {
		t.Fatal("other token's ctx cancelled")
	}
	l.Revoke("apt_none") // no-op
}

func TestLiveDoneUntracks(t *testing.T) {
	l := NewLive()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, done := l.Track(parent, "apt_a")
	done()
	done() // idempotent
	if n := l.count(); n != 0 {
		t.Fatalf("still tracking %d", n)
	}
	if parent.Err() != nil {
		t.Fatal("done cancelled the parent")
	}
}

func TestLiveSweepCancelsRemovedUser(t *testing.T) {
	l := NewLive()
	gone, d1 := l.Track(context.Background(), "apt_gone")
	kept, d2 := l.Track(context.Background(), "apt_kept")
	defer d1()
	defer d2()
	l.Sweep(func(id ident.ID) bool { return id == "apt_kept" })
	if gone.Err() == nil || kept.Err() != nil {
		t.Fatalf("gone=%v kept=%v", gone.Err(), kept.Err())
	}
}
