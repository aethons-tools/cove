package sessionevents

import (
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory Store for tests and tooling. Not for production:
// nothing is persisted — Jam stores session events in Postgres (sessionpg).
type MemStore struct {
	mu      sync.Mutex
	streams map[streamKey][]Event // seq-ascending
}

var _ Store = (*MemStore)(nil)

func NewMemStore() *MemStore { return &MemStore{streams: map[streamKey][]Event{}} }

func clone(e Event) Event {
	e.Raw = append([]byte(nil), e.Raw...)
	return e
}

func (s *MemStore) Append(ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := streamKey{ev.ActorID, ev.StreamID}
	evs := s.streams[k]
	if n := len(evs); n > 0 && ev.Seq <= evs[n-1].Seq {
		return nil // existing (actor, stream, seq): no-op
	}
	s.streams[k] = append(evs, clone(ev))
	return nil
}

func (s *MemStore) HighWater(actorID, streamID string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.streams[streamKey{actorID, streamID}]
	if len(evs) == 0 {
		return 0, nil
	}
	return evs[len(evs)-1].Seq, nil
}

func (s *MemStore) List(f Filter) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, e := range s.streams[streamKey{f.ActorID, f.StreamID}] {
		if e.Seq > f.AfterSeq {
			out = append(out, clone(e))
			if f.Limit > 0 && len(out) == f.Limit {
				break
			}
		}
	}
	return out, nil
}

func (s *MemStore) Streams(actorID string) ([]StreamInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []StreamInfo
	for k, evs := range s.streams {
		if k.actor != actorID || len(evs) == 0 {
			continue
		}
		out = append(out, StreamInfo{StreamID: k.stream, FirstAt: evs[0].ReceivedAt, LastAt: evs[len(evs)-1].ReceivedAt,
			LastSeq: evs[len(evs)-1].Seq, Events: len(evs)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstAt.After(out[j].FirstAt) })
	return out, nil
}

// DeleteBefore removes whole streams whose last event was received before t.
func (s *MemStore) DeleteBefore(t time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, evs := range s.streams {
		if len(evs) > 0 && evs[len(evs)-1].ReceivedAt.Before(t) {
			n += len(evs)
			delete(s.streams, k)
		}
	}
	return n, nil
}
