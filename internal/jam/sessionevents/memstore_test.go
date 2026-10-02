package sessionevents_test

import (
	"fmt"
	"testing"
	"time"

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

func TestMemStoreStreamsTiebreakByStreamID(t *testing.T) {
	s := sessionevents.NewMemStore()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		s.Append(sessionevents.Event{ActorID: "w1", StreamID: fmt.Sprintf("s%02d", i), Seq: 1, Kind: sessionevents.KindEvent, ReceivedAt: at})
	}
	got, err := s.Streams("w1")
	if err != nil || len(got) != 20 {
		t.Fatalf("%v %d", err, len(got))
	}
	for i, si := range got {
		if want := fmt.Sprintf("s%02d", i); si.StreamID != want {
			t.Fatalf("pos %d = %s, want %s (equal FirstAt must order by StreamID)", i, si.StreamID, want)
		}
	}
}
