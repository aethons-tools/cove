package sessionevents_test

import (
	"bytes"
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

func TestFileStoreAppendAfterTornTailWarmCache(t *testing.T) {
	dir := t.TempDir()
	const sid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s, _ := sessionevents.OpenFileStore(dir)
	if err := s.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 1, Kind: sessionevents.KindEvent, Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// Simulate an in-process short write: fragment left on disk, same store (warm cache).
	f, err := os.OpenFile(filepath.Join(dir, "w1", sid+".jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"ActorID":"w1","Seq":2,"Ki`)
	f.Close()
	if err := s.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 2, Kind: sessionevents.KindEvent, Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("want seqs 1,2 got %+v", evs)
	}
}

func TestFileStoreSkipsOverlongLine(t *testing.T) {
	dir := t.TempDir()
	const sid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s, _ := sessionevents.OpenFileStore(dir)
	if err := s.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 1, Kind: sessionevents.KindEvent, Raw: []byte(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "w1", sid+".jsonl")
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.Write(append(bytes.Repeat([]byte("x"), 9<<20), '\n'))
	f.Close()
	s2, _ := sessionevents.OpenFileStore(dir)
	if err := s2.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 2, Kind: sessionevents.KindEvent, Raw: []byte(`{"a":2}`)}); err != nil {
		t.Fatal(err)
	}
	evs, err := s2.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if err != nil || len(evs) != 2 || evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("evs=%+v err=%v", evs, err)
	}
	if infos, _ := s2.Streams("w1"); len(infos) != 1 {
		t.Fatalf("streams %+v", infos)
	}
}

func TestFileStoreRoundTripsWorstCaseEscapedLine(t *testing.T) {
	const sid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s, _ := sessionevents.OpenFileStore(t.TempDir())
	raw := bytes.Repeat([]byte("<"), 1<<20) // not JSON → raw_text; each '<' escapes to 6 bytes
	if err := s.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 1, Kind: sessionevents.KindEvent, Raw: raw}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if err != nil || len(evs) != 1 || !bytes.Equal(evs[0].Raw, raw) {
		t.Fatalf("len=%d err=%v", len(evs), err)
	}
}
