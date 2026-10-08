package attach

import (
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const sid = "0123456789abcdef0123456789abcdef"

func evMsg(seq uint64, raw string) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Event{Event: &attachpb.SessionEvent{StreamId: sid, Seq: seq, Turn: 1, Raw: []byte(raw)}}}
}

func TestAttachIngestsEventsAndAcks(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	st := sessionevents.NewMemStore()
	srv.SetSessionEvents(sessionevents.NewIngest(st, sessionevents.NewHub(), nil))
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 3; i++ {
		if err := stream.Send(evMsg(i, `{"type":"system"}`)); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.After(3 * time.Second)
	var acked uint64
	for acked < 3 {
		got := make(chan *attachpb.ControlDown, 1)
		go func() { cd, _ := stream.Recv(); got <- cd }()
		select {
		case cd := <-got:
			if a := cd.GetAck(); a != nil && a.GetStreamId() == sid {
				acked = a.GetSeq()
			}
		case <-deadline:
			t.Fatalf("never acked 3 (last %d)", acked)
		}
	}
	rows, _ := st.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	if len(rows) != 3 || rows[0].Stamp.Role != "guest" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestAttachDropsBadStreamIDWithoutBreakingStream(t *testing.T) {
	store, _, srv, dial, tok, secret := harness(t)
	st := sessionevents.NewMemStore()
	srv.SetSessionEvents(sessionevents.NewIngest(st, sessionevents.NewHub(), nil))
	cc := dial()
	defer cc.Close()
	stream, _ := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Event{Event: &attachpb.SessionEvent{StreamId: "../x", Seq: 1}}})
	stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Status{Status: attachpb.Activity_WAITING}})
	if !eventually(func() bool { i, _ := store.GetInstance("w1"); return i.Activity == "waiting" }) {
		t.Fatal("stream stopped processing after a bad event")
	}
}

func TestAttachWithoutSessionEventsIgnoresEvents(t *testing.T) {
	store, _, _, dial, tok, secret := harness(t) // no SetSessionEvents
	cc := dial()
	defer cc.Close()
	stream, _ := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	stream.Send(evMsg(1, `{}`))
	stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Status{Status: attachpb.Activity_WAITING}})
	if !eventually(func() bool { i, _ := store.GetInstance("w1"); return i.Activity == "waiting" }) {
		t.Fatal("event without ingest broke the stream")
	}
}

// failOnceStore fails the Nth Append call once, then delegates.
type failOnceStore struct {
	sessionevents.Store
	mu    sync.Mutex
	calls int
	failN int
}

func (f *failOnceStore) Append(ev sessionevents.Event) error {
	f.mu.Lock()
	f.calls++
	fail := f.calls == f.failN
	f.mu.Unlock()
	if fail {
		return errors.New("disk on fire")
	}
	return f.Store.Append(ev)
}

func TestAttachStoreErrorEndsStreamAndReplayLeavesNoGap(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	fs := sessionevents.NewMemStore()
	st := &failOnceStore{Store: fs, failN: 2} // seq 2 fails
	srv.SetSessionEvents(sessionevents.NewIngest(st, sessionevents.NewHub(), nil))
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 3; i++ {
		_ = stream.Send(evMsg(i, `{"type":"system"}`))
	}
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("want Unavailable, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not end on store error")
	}
	// The cove reconnects and replays from its last ack (1).
	stream2, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	_ = stream2.Send(evMsg(2, `{"type":"system"}`))
	_ = stream2.Send(evMsg(3, `{"type":"system"}`))
	if !eventually(func() bool {
		rows, _ := fs.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
		return len(rows) == 3
	}) {
		rows, _ := fs.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
		t.Fatalf("rows %+v", rows)
	}
	rows, _ := fs.List(sessionevents.Filter{ActorID: "w1", StreamID: sid})
	for i, r := range rows {
		if r.Kind != sessionevents.KindEvent || r.Seq != uint64(i+1) {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
}
