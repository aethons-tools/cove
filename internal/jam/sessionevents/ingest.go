package sessionevents

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrBadStreamID = errors.New("sessionevents: invalid stream id")
	ErrBadSeq      = errors.New("sessionevents: seq must be >= 1")
)

// Incoming is one event as received from a cove (already authenticated to an
// actor by the Attach server).
type Incoming struct {
	StreamID       string
	Seq            uint64
	Turn           uint32
	ObservedAt     time.Time
	Raw            []byte
	TruncatedBytes uint64
}

type streamKey struct{ actor, stream string }

// Ingest is the single write path: dedupe replays, record seq holes as gap
// rows, persist, then publish. Appends are serialized (one mutex) — simple and
// sufficient for v1 fleet sizes.
type Ingest struct {
	store Store
	hub   *Hub
	now   func() time.Time
	mu    sync.Mutex
	hw    map[streamKey]uint64
}

func NewIngest(store Store, hub *Hub, now func() time.Time) *Ingest {
	if now == nil {
		now = time.Now
	}
	return &Ingest{store: store, hub: hub, now: now, hw: map[streamKey]uint64{}}
}

// Append returns the stream's durable high-water — the value to ack.
func (in *Ingest) Append(actorID string, stamp Stamp, ev Incoming) (uint64, error) {
	if !ValidStreamID(ev.StreamID) {
		return 0, ErrBadStreamID
	}
	if ev.Seq == 0 {
		return 0, ErrBadSeq
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	k := streamKey{actorID, ev.StreamID}
	hw, ok := in.hw[k]
	if !ok {
		var err error
		if hw, err = in.store.HighWater(actorID, ev.StreamID); err != nil {
			return 0, err
		}
		in.hw[k] = hw
	}
	if ev.Seq <= hw {
		return hw, nil // replay of something already durable
	}
	now := in.now()
	if ev.Seq > hw+1 {
		gap := Event{ActorID: actorID, StreamID: ev.StreamID, Seq: ev.Seq - 1, Kind: KindGap,
			GapFrom: hw + 1, GapTo: ev.Seq - 1, Turn: ev.Turn, ReceivedAt: now, Stamp: stamp}
		if err := in.store.Append(gap); err != nil {
			return hw, err
		}
		in.hub.Publish(gap)
	}
	e := Event{ActorID: actorID, StreamID: ev.StreamID, Seq: ev.Seq, Kind: KindEvent, Turn: ev.Turn,
		ObservedAt: ev.ObservedAt, ReceivedAt: now, TruncatedBytes: ev.TruncatedBytes,
		Raw: append([]byte(nil), ev.Raw...), Stamp: stamp, Index: DeriveIndex(ev.Raw)}
	if err := in.store.Append(e); err != nil {
		return hw, err // not durable → not acked; the cove resends on reconnect
	}
	in.hw[k] = ev.Seq
	in.hub.Publish(e)
	return ev.Seq, nil
}
