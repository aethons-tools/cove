package covemaster

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// fakeRuntime records StatusUp events and acks them when autoAck is set.
type fakeRuntime struct {
	attachpb.UnimplementedRuntimeServer
	mu       sync.Mutex
	events   []*attachpb.SessionEvent
	statuses []attachpb.Activity
	autoAck  bool
	dropNext int // close the stream after this many events (0 = never)
}

func (f *fakeRuntime) Attach(s attachpb.Runtime_AttachServer) error {
	n := 0
	for {
		m, err := s.Recv()
		if err != nil {
			return err
		}
		switch x := m.GetMsg().(type) {
		case *attachpb.StatusUp_Event:
			f.mu.Lock()
			f.events = append(f.events, x.Event)
			drop := f.dropNext
			f.mu.Unlock()
			n++
			if f.autoAck {
				_ = s.Send(&attachpb.ControlDown{Msg: &attachpb.ControlDown_Ack{Ack: &attachpb.EventAck{StreamId: x.Event.StreamId, Seq: x.Event.Seq}}})
			}
			if drop > 0 && n == drop {
				f.mu.Lock()
				f.dropNext = 0
				f.mu.Unlock()
				return nil // server ends the RPC → client reconnects
			}
		case *attachpb.StatusUp_Status:
			f.mu.Lock()
			f.statuses = append(f.statuses, x.Status)
			f.mu.Unlock()
		}
	}
}

func (f *fakeRuntime) seqs() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []uint64
	for _, e := range f.events {
		out = append(out, e.Seq)
	}
	return out
}

func (f *fakeRuntime) gotDone() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.statuses {
		if s == attachpb.Activity_DONE {
			return true
		}
	}
	return false
}

func fakeHarness(t *testing.T, f *fakeRuntime) grpc.DialOption {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	attachpb.RegisterRuntimeServer(gs, f)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	return grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.DialContext(context.Background()) })
}

func fakeClient(t *testing.T, f *fakeRuntime, mut func(*Config)) *Client {
	cfg := Config{Addr: "bufnet", Token: "tok-SECRET-0001", LaunchSecret: "ls-SECRET-0002",
		DialOptions:  []grpc.DialOption{fakeHarness(t, f), grpc.WithTransportCredentials(insecure.NewCredentials())},
		FlushTimeout: 300 * time.Millisecond}
	if mut != nil {
		mut(&cfg)
	}
	return New(cfg, nil)
}

// eventWorkload emits lines via h.Event, then returns (or blocks until release).
type eventWorkload struct {
	lines   []string
	release chan struct{}
}

func (w *eventWorkload) Run(ctx context.Context, h Handle) error {
	h.Report(Running)
	for _, l := range w.lines {
		h.Event(1, []byte(l), 0)
	}
	if w.release != nil {
		select {
		case <-w.release:
		case <-ctx.Done():
		}
	}
	return nil
}
func (*eventWorkload) Control(Control) {}

func TestEventsDeliveredInOrderThenDone(t *testing.T) {
	f := &fakeRuntime{autoAck: true}
	c := fakeClient(t, f, nil)
	if err := c.Run(context.Background(), &eventWorkload{lines: []string{`{"a":1}`, `{"a":2}`, `{"a":3}`}}); err != nil {
		t.Fatal(err)
	}
	if got := f.seqs(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("seqs: %v", got)
	}
	if !f.gotDone() {
		t.Fatal("no Done")
	}
	if len(c.StreamID()) != 32 {
		t.Fatalf("stream id %q", c.StreamID())
	}
}

func TestEventsRedactOwnSecrets(t *testing.T) {
	f := &fakeRuntime{autoAck: true}
	c := fakeClient(t, f, nil)
	_ = c.Run(context.Background(), &eventWorkload{lines: []string{`{"out":"AT_JAM_TOKEN=tok-SECRET-0001 x=ls-SECRET-0002"}`}})
	f.mu.Lock()
	raw := string(f.events[0].Raw)
	f.mu.Unlock()
	if strings.Contains(raw, "SECRET") || strings.Count(raw, "«redacted»") != 2 {
		t.Fatalf("not redacted: %s", raw)
	}
}

func TestEventsReplayUnackedAfterReconnect(t *testing.T) {
	// No acks, and the server drops the stream after the 2nd event: the client
	// must reconnect and resend from lastAcked+1 = 1, so seq 1 and 2 arrive twice.
	f := &fakeRuntime{dropNext: 2}
	rel := make(chan struct{})
	c := fakeClient(t, f, nil)
	done := make(chan error, 1)
	go func() {
		done <- c.Run(context.Background(), &eventWorkload{lines: []string{"1", "2", "3"}, release: rel})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(f.seqs()) < 5 {
		time.Sleep(10 * time.Millisecond)
	}
	close(rel)
	<-done
	got := f.seqs()
	if len(got) < 5 || got[0] != 1 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("want replay from 1 after drop, got %v", got)
	}
}

func TestEventsAckTrimsReplay(t *testing.T) {
	// With acks, a reconnect must NOT resend acked events.
	f := &fakeRuntime{autoAck: true, dropNext: 2}
	rel := make(chan struct{})
	c := fakeClient(t, f, nil)
	done := make(chan error, 1)
	go func() {
		done <- c.Run(context.Background(), &eventWorkload{lines: []string{"1", "2", "3"}, release: rel})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(f.seqs()) < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	close(rel)
	<-done
	got := f.seqs()
	seen := map[uint64]int{}
	for _, s := range got {
		seen[s]++
	}
	if seen[3] != 1 || seen[1] > 2 { // seq 1 may race its ack once; seq 3 is sent exactly once
		t.Fatalf("unexpected resend pattern %v", got)
	}
}

func TestDoneWithoutAckStillReportsDone(t *testing.T) {
	f := &fakeRuntime{} // an "old Jam": never acks
	c := fakeClient(t, f, func(c *Config) { c.FlushTimeout = 200 * time.Millisecond })
	start := time.Now()
	if err := c.Run(context.Background(), &eventWorkload{lines: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if !f.gotDone() {
		t.Fatal("Done not reported when acks never arrive")
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("flush wait unbounded: %v", el)
	}
}

func TestEventNeverBlocksWhenBufferFull(t *testing.T) {
	c := New(Config{Addr: "unused", EventBufferEvents: 2}, nil) // never connected
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			c.Event(1, []byte("x"), 0)
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("Event blocked with no connection and a full buffer")
	}
}
