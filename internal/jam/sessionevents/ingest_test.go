package sessionevents_test

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const sid = "0123456789abcdef0123456789abcdef"

func newIngest(t *testing.T, dir string) (*sessionevents.Ingest, *sessionevents.FileStore, *sessionevents.Hub) {
	t.Helper()
	st, err := sessionevents.OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	hub := sessionevents.NewHub()
	return sessionevents.NewIngest(st, hub, func() time.Time { return time.Unix(100, 0) }), st, hub
}

func in(seq uint64, raw string) sessionevents.Incoming {
	return sessionevents.Incoming{StreamID: sid, Seq: seq, Turn: 1, Raw: []byte(raw)}
}

func TestIngestStoresIndexesAndPublishes(t *testing.T) {
	ing, st, hub := newIngest(t, t.TempDir())
	sub := hub.Subscribe("w1", 8)
	defer sub.Close()
	hw, err := ing.Append("w1", sessionevents.Stamp{Project: "p"}, in(1, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`))
	if err != nil || hw != 1 {
		t.Fatalf("hw=%d err=%v", hw, err)
	}
	got, _ := st.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if len(got) != 1 || got[0].Index.ToolName != "Bash" || got[0].Stamp.Project != "p" || got[0].ReceivedAt.Unix() != 100 {
		t.Fatalf("stored %+v", got)
	}
	if ev := <-sub.C; ev.Seq != 1 {
		t.Fatalf("published %+v", ev)
	}
}

func TestIngestDropsDuplicates(t *testing.T) {
	ing, st, _ := newIngest(t, t.TempDir())
	ing.Append("w1", sessionevents.Stamp{}, in(1, `{}`))
	ing.Append("w1", sessionevents.Stamp{}, in(2, `{}`))
	hw, err := ing.Append("w1", sessionevents.Stamp{}, in(1, `{"replayed":true}`))
	if err != nil || hw != 2 {
		t.Fatalf("hw=%d err=%v", hw, err)
	}
	if got, _ := st.List(sessionevents.Filter{ActorID: "w1", StreamID: sid}); len(got) != 2 {
		t.Fatalf("got %d rows", len(got))
	}
}

func TestIngestRecordsGap(t *testing.T) {
	ing, st, _ := newIngest(t, t.TempDir())
	ing.Append("w1", sessionevents.Stamp{}, in(1, `{}`))
	hw, _ := ing.Append("w1", sessionevents.Stamp{}, in(5, `{}`))
	if hw != 5 {
		t.Fatalf("hw %d", hw)
	}
	got, _ := st.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if len(got) != 3 || got[1].Kind != sessionevents.KindGap || got[1].GapFrom != 2 || got[1].GapTo != 4 || got[1].Seq != 4 || got[2].Seq != 5 {
		t.Fatalf("rows %+v", got)
	}
}

func TestIngestRecoversHighWaterAfterRestart(t *testing.T) {
	dir := t.TempDir()
	ing, _, _ := newIngest(t, dir)
	ing.Append("w1", sessionevents.Stamp{}, in(1, `{}`))
	ing.Append("w1", sessionevents.Stamp{}, in(2, `{}`))
	// Jam restarts; the cove replays its unacked tail 1..3.
	ing2, st2, _ := newIngest(t, dir)
	for _, s := range []uint64{1, 2, 3} {
		ing2.Append("w1", sessionevents.Stamp{}, in(s, `{}`))
	}
	got, _ := st2.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if len(got) != 3 {
		t.Fatalf("want 3 rows, no dups, no gap; got %+v", got)
	}
	for _, e := range got {
		if e.Kind == sessionevents.KindGap {
			t.Fatalf("false gap: %+v", e)
		}
	}
}

func TestIngestRejectsBadInput(t *testing.T) {
	ing, _, _ := newIngest(t, t.TempDir())
	if _, err := ing.Append("w1", sessionevents.Stamp{}, sessionevents.Incoming{StreamID: "../../x", Seq: 1}); err != sessionevents.ErrBadStreamID {
		t.Fatalf("bad stream id: %v", err)
	}
	if _, err := ing.Append("w1", sessionevents.Stamp{}, sessionevents.Incoming{StreamID: sid, Seq: 0}); err != sessionevents.ErrBadSeq {
		t.Fatalf("seq 0: %v", err)
	}
}
