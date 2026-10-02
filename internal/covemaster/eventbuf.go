package covemaster

import (
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// eventBuf holds session events not yet acked by Jam, in seq order. It is
// bounded by count and bytes; overflow drops the oldest (Jam records the hole
// as a gap). add never blocks.
type eventBuf struct {
	mu        sync.Mutex
	streamID  string
	next      uint64 // last assigned seq
	acked     uint64
	items     []*attachpb.SessionEvent
	bytes     int
	maxEvents int
	maxBytes  int
	notify    chan struct{} // buffer 1, coalescing: "something new to send"
	ackedCh   chan struct{} // buffer 1, coalescing: "ack high-water advanced"
}

func newEventBuf(streamID string, maxEvents, maxBytes int) *eventBuf {
	return &eventBuf{streamID: streamID, maxEvents: maxEvents, maxBytes: maxBytes, notify: make(chan struct{}, 1), ackedCh: make(chan struct{}, 1)}
}

func (b *eventBuf) add(turn uint32, raw []byte, truncated uint64, now time.Time) {
	b.mu.Lock()
	b.next++
	ev := &attachpb.SessionEvent{
		StreamId: b.streamID, Seq: b.next, Turn: turn,
		ObservedUnixMs: now.UnixMilli(), Raw: append([]byte(nil), raw...), TruncatedBytes: truncated,
	}
	b.items = append(b.items, ev)
	b.bytes += len(ev.Raw)
	for len(b.items) > 1 && (len(b.items) > b.maxEvents || b.bytes > b.maxBytes) {
		b.bytes -= len(b.items[0].Raw)
		b.items[0] = nil
		b.items = b.items[1:]
	}
	b.mu.Unlock()
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// since returns buffered events with seq > after, in order.
func (b *eventBuf) since(after uint64) []*attachpb.SessionEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*attachpb.SessionEvent
	for _, ev := range b.items {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return out
}

// ack trims every event with seq <= seq. Stale acks are no-ops.
func (b *eventBuf) ack(seq uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if seq <= b.acked {
		return
	}
	b.acked = seq
	select {
	case b.ackedCh <- struct{}{}:
	default:
	}
	i := 0
	for i < len(b.items) && b.items[i].Seq <= seq {
		b.bytes -= len(b.items[i].Raw)
		b.items[i] = nil
		i++
	}
	b.items = b.items[i:]
}

func (b *eventBuf) ackedSeq() uint64 { b.mu.Lock(); defer b.mu.Unlock(); return b.acked }
func (b *eventBuf) lastSeq() uint64  { b.mu.Lock(); defer b.mu.Unlock(); return b.next }
