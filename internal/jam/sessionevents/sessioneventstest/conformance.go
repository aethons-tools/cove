// Package sessioneventstest is the shared behavioral suite every
// sessionevents.Store backend must pass.
package sessioneventstest

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const (
	streamA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	streamB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func ev(actor, stream string, seq uint64, at time.Time, raw string) sessionevents.Event {
	return sessionevents.Event{ActorID: actor, StreamID: stream, Seq: seq, Kind: sessionevents.KindEvent,
		Turn: 1, ObservedAt: at, ReceivedAt: at, Raw: []byte(raw),
		Stamp: sessionevents.Stamp{Project: "default", Role: "guest", RaisedAt: at},
		Index: sessionevents.DeriveIndex([]byte(raw))}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// RunConformance runs the shared Store behavior suite against newStore.
func RunConformance(t *testing.T, newStore func(t *testing.T) sessionevents.Store) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	t.Run("append list order and filters", func(t *testing.T) {
		s := newStore(t)
		for i := uint64(1); i <= 5; i++ {
			must(t, s.Append(ev("w1", streamA, i, t0.Add(time.Duration(i)*time.Second), `{"type":"system"}`)))
		}
		got, err := s.List(sessionevents.Filter{ActorID: "w1", StreamID: streamA, AfterSeq: 2, Limit: 2})
		must(t, err)
		if len(got) != 2 || got[0].Seq != 3 || got[1].Seq != 4 {
			t.Fatalf("got %+v", got)
		}
		hw, err := s.HighWater("w1", streamA)
		must(t, err)
		if hw != 5 {
			t.Fatalf("high water %d", hw)
		}
		if hw, _ := s.HighWater("w1", streamB); hw != 0 {
			t.Fatalf("empty stream high water %d", hw)
		}
	})

	t.Run("duplicate append is a no-op", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Append(ev("w1", streamA, 1, t0, `{"n":1}`)))
		must(t, s.Append(ev("w1", streamA, 1, t0, `{"n":"dup"}`)))
		got, _ := s.List(sessionevents.Filter{ActorID: "w1", StreamID: streamA})
		if len(got) != 1 || !sessionevents.RawEqual(got[0].Raw, []byte(`{"n":1}`)) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("raw round trips", func(t *testing.T) {
		s := newStore(t)
		lines := []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"<script>x</script> & ok"}]}}`,
			`{"type":"user","tool_use_result":{"stdout":"a\u0000b"}}`, // nul escape: jsonb rejects it
			`{"type":"assist`, // truncated, not JSON
		}
		for i, l := range lines {
			must(t, s.Append(ev("w1", streamA, uint64(i+1), t0, l)))
		}
		got, _ := s.List(sessionevents.Filter{ActorID: "w1", StreamID: streamA})
		if len(got) != len(lines) {
			t.Fatalf("got %d events", len(got))
		}
		for i, l := range lines {
			if !sessionevents.RawEqual(got[i].Raw, []byte(l)) {
				t.Errorf("line %d: got %q want %q", i, got[i].Raw, l)
			}
		}
	})

	t.Run("gap rows, stamp and index round trip", func(t *testing.T) {
		s := newStore(t)
		g := sessionevents.Event{ActorID: "w1", StreamID: streamA, Seq: 9, Kind: sessionevents.KindGap, GapFrom: 4, GapTo: 9, ReceivedAt: t0}
		must(t, s.Append(g))
		e := ev("w1", streamA, 10, t0, `{"type":"result","total_cost_usd":0.25,"is_error":true}`)
		e.TruncatedBytes = 7
		must(t, s.Append(e))
		got, _ := s.List(sessionevents.Filter{ActorID: "w1", StreamID: streamA})
		if got[0].Kind != sessionevents.KindGap || got[0].GapFrom != 4 || got[0].GapTo != 9 {
			t.Fatalf("gap: %+v", got[0])
		}
		if got[1].Index.CostUSD != 0.25 || !got[1].Index.IsError || got[1].Stamp.Role != "guest" || got[1].TruncatedBytes != 7 {
			t.Fatalf("event: %+v", got[1])
		}
	})

	t.Run("streams newest first", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Append(ev("w1", streamA, 1, t0, `{}`)))
		must(t, s.Append(ev("w1", streamA, 2, t0.Add(time.Minute), `{}`)))
		must(t, s.Append(ev("w1", streamB, 1, t0.Add(time.Hour), `{}`)))
		must(t, s.Append(ev("other", streamA, 1, t0, `{}`)))
		got, err := s.Streams("w1")
		must(t, err)
		if len(got) != 2 || got[0].StreamID != streamB || got[1].StreamID != streamA || got[1].LastSeq != 2 || got[1].Events != 2 {
			t.Fatalf("streams: %+v", got)
		}
	})

	t.Run("delete before", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Append(ev("w1", streamA, 1, t0, `{}`)))
		must(t, s.Append(ev("w1", streamB, 1, t0.Add(48*time.Hour), `{}`)))
		n, err := s.DeleteBefore(t0.Add(24 * time.Hour))
		must(t, err)
		if n < 1 {
			t.Fatalf("deleted %d", n)
		}
		if got, _ := s.List(sessionevents.Filter{ActorID: "w1", StreamID: streamA}); len(got) != 0 {
			t.Fatalf("old stream survived: %+v", got)
		}
		if got, _ := s.List(sessionevents.Filter{ActorID: "w1", StreamID: streamB}); len(got) != 1 {
			t.Fatalf("new stream lost: %+v", got)
		}
	})
}
