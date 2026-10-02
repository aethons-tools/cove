package sessionevents_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessioneventstest"
)

func TestMemStoreConformance(t *testing.T) {
	sessioneventstest.RunConformance(t, func(t *testing.T) sessionevents.Store { return sessionevents.NewMemStore() })
}

func TestMemStoreCopiesRaw(t *testing.T) {
	s := sessionevents.NewMemStore()
	raw := []byte(`{"a":1}`)
	s.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 1, Kind: sessionevents.KindEvent, Raw: raw})
	raw[2] = 'Z'
	got, _ := s.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if string(got[0].Raw) != `{"a":1}` {
		t.Fatalf("store aliased caller's slice: %s", got[0].Raw)
	}
}

func TestNopStore(t *testing.T) {
	var s sessionevents.Store = sessionevents.NopStore{}
	if err := s.Append(sessionevents.Event{Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Streams("w1"); len(got) != 0 {
		t.Fatal(got)
	}
}
