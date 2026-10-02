package covemaster

import (
	"testing"
	"time"
)

func TestEventBufSequencesAndTrims(t *testing.T) {
	b := newEventBuf("s", 100, 1<<20)
	now := time.UnixMilli(1000)
	for i := 0; i < 3; i++ {
		b.add(1, []byte("x"), 0, now)
	}
	got := b.since(0)
	if len(got) != 3 || got[0].Seq != 1 || got[2].Seq != 3 {
		t.Fatalf("since(0): %+v", got)
	}
	b.ack(2)
	if got := b.since(0); len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("after ack(2): %+v", got)
	}
	if b.ackedSeq() != 2 || b.lastSeq() != 3 {
		t.Fatalf("acked=%d last=%d", b.ackedSeq(), b.lastSeq())
	}
	b.ack(1) // stale ack is a no-op
	if b.ackedSeq() != 2 {
		t.Fatalf("stale ack moved acked to %d", b.ackedSeq())
	}
}

func TestEventBufOverflowDropsOldest(t *testing.T) {
	b := newEventBuf("s", 2, 1<<20)
	for i := 0; i < 5; i++ {
		b.add(1, []byte("x"), 0, time.Now())
	}
	got := b.since(0)
	if len(got) != 2 || got[0].Seq != 4 || got[1].Seq != 5 {
		t.Fatalf("want seqs 4,5 got %+v", got)
	}
}

func TestEventBufByteCap(t *testing.T) {
	b := newEventBuf("s", 100, 10)
	b.add(1, []byte("123456"), 0, time.Now())
	b.add(1, []byte("123456"), 0, time.Now()) // 12 bytes > 10 → oldest dropped
	if got := b.since(0); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("byte cap: %+v", got)
	}
}

func TestEventBufCopiesRaw(t *testing.T) {
	b := newEventBuf("s", 10, 1<<20)
	raw := []byte("abc")
	b.add(1, raw, 0, time.Now())
	raw[0] = 'Z'
	if string(b.since(0)[0].Raw) != "abc" {
		t.Fatal("buffer aliased the caller's slice")
	}
}

func TestEventBufNotifies(t *testing.T) {
	b := newEventBuf("s", 10, 1<<20)
	b.add(1, []byte("a"), 0, time.Now())
	select {
	case <-b.notify:
	default:
		t.Fatal("add did not signal notify")
	}
}
