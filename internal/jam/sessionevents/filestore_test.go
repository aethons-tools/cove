package sessionevents_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessioneventstest"
)

func TestFileStoreConformance(t *testing.T) {
	sessioneventstest.RunConformance(t, func(t *testing.T) sessionevents.Store {
		s, err := sessionevents.OpenFileStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestFileStoreRejectsUnsafeIDs(t *testing.T) {
	s, _ := sessionevents.OpenFileStore(t.TempDir())
	for _, e := range []sessionevents.Event{
		{ActorID: "..", StreamID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Seq: 1, Kind: sessionevents.KindEvent},
		{ActorID: "a/b", StreamID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Seq: 1, Kind: sessionevents.KindEvent},
		{ActorID: "w1", StreamID: "../x", Seq: 1, Kind: sessionevents.KindEvent},
	} {
		if err := s.Append(e); err == nil {
			t.Errorf("Append(%q,%q) should fail", e.ActorID, e.StreamID)
		}
	}
}

func TestFileStoreHighWaterSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, _ := sessionevents.OpenFileStore(dir)
	s.Append(sessionevents.Event{ActorID: "w1", StreamID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Seq: 4, Kind: sessionevents.KindEvent, Raw: []byte(`{}`)})
	s2, _ := sessionevents.OpenFileStore(dir)
	if hw, _ := s2.HighWater("w1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); hw != 4 {
		t.Fatalf("hw after reopen %d", hw)
	}
}

func TestFileStoreAppendAfterTornTail(t *testing.T) {
	dir := t.TempDir()
	const sid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s, _ := sessionevents.OpenFileStore(dir)
	if err := s.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 1, Kind: sessionevents.KindEvent, Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "w1", sid+".jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"ActorID":"w1","Seq":2,"Ki`)
	f.Close()
	s2, _ := sessionevents.OpenFileStore(dir)
	if err := s2.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 2, Kind: sessionevents.KindEvent, Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	evs, err := s2.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("want seqs 1,2 got %+v", evs)
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
