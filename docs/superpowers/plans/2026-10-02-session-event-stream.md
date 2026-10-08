# Session Event Stream Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Jam-managed coves stream their Claude Code `stream-json` events up the Attach gRPC stream to Jam, which persists them losslessly (file or Postgres), and operators watch them live in the admin UI and export them as NDJSON.

**Architecture:** In the cove, `agentrun` runs `claude -p --output-format stream-json --verbose`, tees stdout into a line splitter, and hands each line to `covemaster.Handle.Event`; the covemaster client redacts its own secrets, sequences events into a bounded in-memory buffer, sends them as `StatusUp.event`, and trims on cumulative `ControlDown.ack`. In Jam, a grpc-free `internal/jam/sessionevents` package (Ingest → Store → Hub) dedupes/gap-marks/persists; the Attach server calls it and acks on a 250 ms tick; the admin UI renders a live SSE timeline from Store backfill + Hub; an `/admin/sessions/...` route exports NDJSON.

**Tech Stack:** Go 1.27, gRPC/protobuf (`buf generate` via `just buf-gen`), pgx v5, `html/template` + htmx + EventSource, stdlib `net/http`.

**Spec:** `docs/superpowers/specs/2026-10-02-session-event-stream-design.md` — read it before starting any task.

## Global Constraints

- Scope: Jam-managed coves only (cove-master / `agentrun`). Do NOT touch `at-cove work`/dispatch, `at-cove chat`, or the switchboard.
- Proto package stays `harbor.attach.v1`; new fields only (`StatusUp.event = 3`, `ControlDown.ack = 5`). Never renumber existing fields.
- `stream_id` is 32 lowercase hex chars; Jam drops events whose `stream_id` fails `^[0-9a-f]{32}$`.
- Max event line: 1 MiB (`1 << 20` bytes) kept; the rest is counted in `truncated_bytes`.
- Cove buffer defaults: 10 000 events or 64 MiB, whichever first; overflow drops oldest.
- Done flush: wait up to 5 s for an ack covering the last seq, then send Done regardless.
- Jam ack cadence: one `EventAck` per stream with a new high-water per 250 ms tick, sent from the per-connection send goroutine, only after persistence.
- Redaction marker: `«redacted»` (same as `internal/logging.Scrub`).
- Storage selection: `store-postgres` set → `sessionpg`; else `session-events-dir` set → file store; else no-op store (events acked and dropped).
- Retention: `session-events-retention` (Go duration or `<N>d`); unset/empty = keep forever; sweeper runs at start then every 24 h.
- `internal/jam/sessionevents` must not import `internal/jam` or grpc. `internal/jam` may import nothing new except via the generic `WithAdminRoute` option (it does not import `sessionevents`).
- Tests are hermetic (`go test ./...`); Postgres tests behind `//go:build integration` and skip without `JAM_TEST_POSTGRES_DSN`.
- TDD: every task writes the failing test first.
- No task is done until the docs it affects are updated in the same change (Task 12 owns the new doc leaf; earlier tasks must not leave docs describing old behavior — Task 3 updates the argv mention if `coves.md` shows it).
- Commits: end messages with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Go env in this sandbox: `GOPROXY=direct GOSUMDB=off` are pre-exported; `just buf-gen` sets its own proxy.

## Review Focus

1. **A `user` event whose `message.content` is a plain string** (the initial prompt echo), not a block array → `DeriveIndex` must still set `Type`/`SessionID` and never error. Test: Task 4 `TestDeriveIndexStringContent`.
2. **Tool output containing a `\u0000` escape** → Postgres `jsonb` rejects it; the event must be stored (as `raw_text`), not dropped or retried forever. Test: Task 5 conformance `nul escape` case (runs against sessionpg in Task 8).
3. **Jam restarts mid-session; the cove replays its unacked tail** → no duplicate rows, no false gap. Test: Task 6 `TestIngestRecoversHighWaterAfterRestart`.
4. **An event published between SSE backfill and live subscription** → appears exactly once in the browser. Test: Task 11 `TestSessionSSESubscribeBeforeBackfill`.
5. **A cove talking to an old Jam that never acks** → Done is delayed by at most the flush timeout and still sent; claude never blocks on a full buffer. Test: Task 2 `TestDoneWithoutAckStillReportsDone` and `TestEventNeverBlocksWhenBufferFull`.

## Follow-ups (file as tickets; not in this plan)

- Guarantee the cove identity token and launch secret have enough entropy/length that exact-match redaction cannot hit ordinary text (owner-deferred, see spec §2).
- Automated-feedback rules over the Hub/Index (goal 3) — next spec.

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/attach/proto/attach.proto` (modify) | `SessionEvent`, `EventAck` messages |
| `internal/jam/attach/attachpb/*.pb.go` (regenerate) | generated code |
| `internal/covemaster/eventbuf.go` (create) | bounded, sequenced, ack-trimmed event buffer |
| `internal/covemaster/covemaster.go` (modify) | `Handle.Event`, Config fields |
| `internal/covemaster/client.go` (modify) | stream_id, redaction, send/replay/ack/flush |
| `internal/agentrun/linesplit.go` (create) | stdout → capped lines |
| `internal/agentrun/spawner.go` (modify) | `Spawn` takes a stdout writer |
| `internal/agentrun/workload.go` (modify) | stream-json argv, turn counter, tee |
| `internal/jam/sessionevents/event.go` (create) | types, Store interface, JSON encoding, stream-id check |
| `internal/jam/sessionevents/index.go` (create) | `DeriveIndex` |
| `internal/jam/sessionevents/hub.go` (create) | per-actor live fan-out |
| `internal/jam/sessionevents/filestore.go` (create) | JSONL file backend |
| `internal/jam/sessionevents/nopstore.go` (create) | disabled backend |
| `internal/jam/sessionevents/sessioneventstest/conformance.go` (create) | shared Store suite |
| `internal/jam/sessionevents/ingest.go` (create) | dedupe, gap rows, persist, publish |
| `internal/jam/sessionevents/retention.go` (create) | sweeper + `ParseRetention` |
| `internal/jam/sessionevents/export.go` (create) | NDJSON export handler |
| `internal/jam/sessionevents/sessionpg/*` (create) | Postgres backend + migration |
| `internal/jam/attach/server.go` (modify) | ingest call, ack tick |
| `internal/jam/admin.go` (modify) | `AdminOption`, `WithAdminRoute` |
| `internal/jam/adminui/session.go` (create) | session page, SSE, view model |
| `internal/jam/adminui/templates/session.html` (create) | timeline template |
| `internal/jam/adminui/templates/coves.html` (modify) | link to session page |
| `internal/jam/adminui/adminui.go` (modify) | `WithSessions` option, routes |
| `cmd/at-jam/config.go`, `cmd/at-jam/main.go` (modify) | config keys + wiring |
| `docs/usage/jam/session-events.md` (create) + `INDEX.md`, `coves.md`, `serve.md`, `ui.md`, `docs/usage/observability.md` (modify) | docs |

---
### Task 1: Wire — `SessionEvent` and `EventAck`

**Files:**
- Modify: `internal/jam/attach/proto/attach.proto`
- Regenerate: `internal/jam/attach/attachpb/attach.pb.go`, `attach_grpc.pb.go`
- Test: `internal/jam/attach/attachpb/attachpb_test.go`

**Interfaces:**
- Produces (generated Go): `attachpb.SessionEvent{StreamId string; Seq uint64; Turn uint32; ObservedUnixMs int64; Raw []byte; TruncatedBytes uint64}`, `attachpb.StatusUp_Event{Event *SessionEvent}`, `attachpb.EventAck{StreamId string; Seq uint64}`, `attachpb.ControlDown_Ack{Ack *EventAck}`.

- [ ] **Step 1: Write the failing test** — append to `attachpb_test.go`:

```go
func TestSessionEventRoundTrip(t *testing.T) {
	in := &StatusUp{Msg: &StatusUp_Event{Event: &SessionEvent{
		StreamId: "0123456789abcdef0123456789abcdef", Seq: 7, Turn: 2,
		ObservedUnixMs: 1700000000000, Raw: []byte(`{"type":"result"}`), TruncatedBytes: 5,
	}}}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out StatusUp
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	ev := out.GetEvent()
	if ev.GetSeq() != 7 || ev.GetTurn() != 2 || ev.GetTruncatedBytes() != 5 || string(ev.GetRaw()) != `{"type":"result"}` {
		t.Fatalf("round trip mismatch: %+v", ev)
	}
	ack := &ControlDown{Msg: &ControlDown_Ack{Ack: &EventAck{StreamId: "s", Seq: 9}}}
	b, _ = proto.Marshal(ack)
	var outAck ControlDown
	if err := proto.Unmarshal(b, &outAck); err != nil || outAck.GetAck().GetSeq() != 9 {
		t.Fatalf("ack round trip: %v %+v", err, outAck.GetAck())
	}
}
```

(Import `google.golang.org/protobuf/proto` if the file does not already.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/jam/attach/attachpb/ -run TestSessionEventRoundTrip`
Expected: FAIL — `undefined: StatusUp_Event`.

- [ ] **Step 3: Edit the proto**

In `attach.proto`, change `StatusUp` and `ControlDown` and add the two messages:

```proto
message StatusUp {
  oneof msg {
    Activity     status    = 1;
    Heartbeat    heartbeat = 2;
    SessionEvent event     = 3;
  }
}

// SessionEvent is one line of the agent's claude stream-json stdout.
message SessionEvent {
  string stream_id        = 1; // random per cove-master process: 32 lowercase hex chars
  uint64 seq              = 2; // monotonic per stream_id, from 1
  uint32 turn             = 3; // claude invocation number, from 1
  int64  observed_unix_ms = 4; // when cove-master read the line
  bytes  raw              = 5; // the line, no trailing newline, secrets redacted
  uint64 truncated_bytes  = 6; // bytes dropped from raw; 0 = intact
}
```

```proto
message ControlDown {
  oneof msg {
    Wake        wake     = 1;
    Teardown    teardown = 2;
    TierChanged tier     = 3;
    RotateToken rotate   = 4; // reserved
    EventAck    ack      = 5;
  }
}

// EventAck is cumulative: every event of stream_id with seq <= this is durable.
message EventAck {
  string stream_id = 1;
  uint64 seq       = 2;
}
```

- [ ] **Step 4: Regenerate**

Run: `just buf-gen`
Expected: `attach.pb.go` / `attach_grpc.pb.go` change. Note: regeneration also copies the proto's leading package comment into both generated files' headers — that drift is expected and fine to commit.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/jam/attach/... ./internal/covemaster/...`
Expected: PASS (existing tests unaffected; old switch statements ignore the new oneof cases).

- [ ] **Step 6: Commit**

```bash
git add internal/jam/attach/proto/attach.proto internal/jam/attach/attachpb/
git commit -m "feat(attach): SessionEvent up / EventAck down on the Attach stream

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: covemaster — sequenced, redacted, ack-trimmed event delivery

**Files:**
- Create: `internal/covemaster/eventbuf.go`, `internal/covemaster/eventbuf_test.go`, `internal/covemaster/events_test.go`
- Modify: `internal/covemaster/covemaster.go`, `internal/covemaster/client.go`, `internal/covemaster/client_test.go` (test workloads only if they implement `Handle` — they don't; they consume it)

**Interfaces:**
- Consumes: Task 1 generated types.
- Produces:
  - `covemaster.Handle` becomes `interface{ Report(Activity); Event(turn uint32, raw []byte, truncatedBytes uint64) }`. `raw` is only valid during the call (the implementation copies).
  - `covemaster.Config` gains `EventBufferEvents int` (default 10000), `EventBufferBytes int` (default 64 MiB), `FlushTimeout time.Duration` (default 5s).
  - `(*Client).StreamID() string`.

- [ ] **Step 1: Write the failing buffer tests** — `eventbuf_test.go`:

```go
package covemaster

import (
	"testing"
	"time"
)

func TestEventBufSequencesAndTrims(t *testing.T) {
	b := newEventBuf("s", 100, 1<<20)
	now := time.UnixMilli(1000)
	for i := 0; i < 3; i++ {
		b.add(1, []byte("x"), 0, now)
	}
	got := b.since(0)
	if len(got) != 3 || got[0].Seq != 1 || got[2].Seq != 3 {
		t.Fatalf("since(0): %+v", got)
	}
	b.ack(2)
	if got := b.since(0); len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("after ack(2): %+v", got)
	}
	if b.ackedSeq() != 2 || b.lastSeq() != 3 {
		t.Fatalf("acked=%d last=%d", b.ackedSeq(), b.lastSeq())
	}
	b.ack(1) // stale ack is a no-op
	if b.ackedSeq() != 2 {
		t.Fatalf("stale ack moved acked to %d", b.ackedSeq())
	}
}

func TestEventBufOverflowDropsOldest(t *testing.T) {
	b := newEventBuf("s", 2, 1<<20)
	for i := 0; i < 5; i++ {
		b.add(1, []byte("x"), 0, time.Now())
	}
	got := b.since(0)
	if len(got) != 2 || got[0].Seq != 4 || got[1].Seq != 5 {
		t.Fatalf("want seqs 4,5 got %+v", got)
	}
}

func TestEventBufByteCap(t *testing.T) {
	b := newEventBuf("s", 100, 10)
	b.add(1, []byte("123456"), 0, time.Now())
	b.add(1, []byte("123456"), 0, time.Now()) // 12 bytes > 10 → oldest dropped
	if got := b.since(0); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("byte cap: %+v", got)
	}
}

func TestEventBufCopiesRaw(t *testing.T) {
	b := newEventBuf("s", 10, 1<<20)
	raw := []byte("abc")
	b.add(1, raw, 0, time.Now())
	raw[0] = 'Z'
	if string(b.since(0)[0].Raw) != "abc" {
		t.Fatal("buffer aliased the caller's slice")
	}
}

func TestEventBufNotifies(t *testing.T) {
	b := newEventBuf("s", 10, 1<<20)
	b.add(1, []byte("a"), 0, time.Now())
	select {
	case <-b.notify:
	default:
		t.Fatal("add did not signal notify")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/covemaster/ -run TestEventBuf`
Expected: FAIL — `undefined: newEventBuf`.

- [ ] **Step 3: Implement `eventbuf.go`**

```go
package covemaster

import (
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// eventBuf holds session events not yet acked by Jam, in seq order. It is
// bounded by count and bytes; overflow drops the oldest (Jam records the hole
// as a gap). add never blocks.
type eventBuf struct {
	mu        sync.Mutex
	streamID  string
	next      uint64 // last assigned seq
	acked     uint64
	items     []*attachpb.SessionEvent
	bytes     int
	maxEvents int
	maxBytes  int
	notify    chan struct{} // buffer 1, coalescing: "something new to send"
}

func newEventBuf(streamID string, maxEvents, maxBytes int) *eventBuf {
	return &eventBuf{streamID: streamID, maxEvents: maxEvents, maxBytes: maxBytes, notify: make(chan struct{}, 1)}
}

func (b *eventBuf) add(turn uint32, raw []byte, truncated uint64, now time.Time) {
	b.mu.Lock()
	b.next++
	ev := &attachpb.SessionEvent{
		StreamId: b.streamID, Seq: b.next, Turn: turn,
		ObservedUnixMs: now.UnixMilli(), Raw: append([]byte(nil), raw...), TruncatedBytes: truncated,
	}
	b.items = append(b.items, ev)
	b.bytes += len(ev.Raw)
	for len(b.items) > 1 && (len(b.items) > b.maxEvents || b.bytes > b.maxBytes) {
		b.bytes -= len(b.items[0].Raw)
		b.items[0] = nil
		b.items = b.items[1:]
	}
	b.mu.Unlock()
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// since returns buffered events with seq > after, in order.
func (b *eventBuf) since(after uint64) []*attachpb.SessionEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*attachpb.SessionEvent
	for _, ev := range b.items {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return out
}

// ack trims every event with seq <= seq. Stale acks are no-ops.
func (b *eventBuf) ack(seq uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if seq <= b.acked {
		return
	}
	b.acked = seq
	i := 0
	for i < len(b.items) && b.items[i].Seq <= seq {
		b.bytes -= len(b.items[i].Raw)
		b.items[i] = nil
		i++
	}
	b.items = b.items[i:]
}

func (b *eventBuf) ackedSeq() uint64 { b.mu.Lock(); defer b.mu.Unlock(); return b.acked }
func (b *eventBuf) lastSeq() uint64  { b.mu.Lock(); defer b.mu.Unlock(); return b.next }
```

- [ ] **Step 4: Run buffer tests** — `go test ./internal/covemaster/ -run TestEventBuf` → PASS.

- [ ] **Step 5: Write the failing client tests** — `events_test.go`. These use a hand-written fake `RuntimeServer` (not the real attach server, which gains acks only in Task 7) so the test controls acks.

```go
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
		DialOptions: []grpc.DialOption{fakeHarness(t, f), grpc.WithTransportCredentials(insecure.NewCredentials())},
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
	go func() { done <- c.Run(context.Background(), &eventWorkload{lines: []string{"1", "2", "3"}, release: rel}) }()
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
	go func() { done <- c.Run(context.Background(), &eventWorkload{lines: []string{"1", "2", "3"}, release: rel}) }()
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
```

- [ ] **Step 6: Run to verify failure**

Run: `go test ./internal/covemaster/ -run 'TestEvents|TestDone|TestEventNever'`
Expected: FAIL — `c.Event undefined` / `Config has no field FlushTimeout`.

- [ ] **Step 7: Implement in `covemaster.go`**

Replace the `Handle` interface and extend `Config`:

```go
// Handle lets the workload report activity and session events to the client.
type Handle interface {
	Report(Activity)
	// Event hands one line of the agent's stream-json stdout to the client.
	// It never blocks. raw is only valid during the call; the client copies it.
	Event(turn uint32, raw []byte, truncatedBytes uint64)
}
```

Add to `Config`:

```go
	EventBufferEvents int           // max buffered unacked events; default 10000
	EventBufferBytes  int           // max buffered unacked raw bytes; default 64 MiB
	FlushTimeout      time.Duration // Done waits this long for the final ack; default 5s
```

Add helper:

```go
func eventMsg(ev *attachpb.SessionEvent) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Event{Event: ev}}
}
```

- [ ] **Step 8: Implement in `client.go`**

Add constants and fields; set defaults in `New`; add `Event`, `StreamID`, `redact`; extend `session`:

```go
const (
	defaultEventBufferEvents = 10000
	defaultEventBufferBytes  = 64 << 20
	defaultFlushTimeout      = 5 * time.Second
	redactedMarker           = "«redacted»" // same marker as internal/logging.Scrub
)
```

Add field `events *eventBuf` to `Client`. In `New`:

```go
	if cfg.EventBufferEvents <= 0 {
		cfg.EventBufferEvents = defaultEventBufferEvents
	}
	if cfg.EventBufferBytes <= 0 {
		cfg.EventBufferBytes = defaultEventBufferBytes
	}
	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = defaultFlushTimeout
	}
	return &Client{cfg: cfg, log: newLogger(log), activity: make(chan Activity, 1),
		events: newEventBuf(newStreamID(), cfg.EventBufferEvents, cfg.EventBufferBytes)}
```

```go
// newStreamID returns 16 random bytes as 32 lowercase hex chars.
func newStreamID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b[:])
}

// StreamID identifies this client's event stream to Jam.
func (c *Client) StreamID() string { return c.events.streamID }

// Event implements Handle: redact this cove's own secrets, then buffer.
func (c *Client) Event(turn uint32, raw []byte, truncatedBytes uint64) {
	c.events.add(turn, c.redact(raw), truncatedBytes, time.Now())
}

// redact replaces exact occurrences of the cove's identity token and launch
// secret — the only secrets a Jam cove holds. The marker has no `"` or `\`, so
// a valid JSON line stays valid.
func (c *Client) redact(raw []byte) []byte {
	for _, s := range []string{c.cfg.Token, c.cfg.LaunchSecret} {
		if s != "" {
			raw = bytes.ReplaceAll(raw, []byte(s), []byte(redactedMarker))
		}
	}
	return raw
}
```

(Imports: `bytes`, `crypto/rand`, `encoding/hex`.)

In `session`, after the "re-send the latest activity" block, add a sender that replays from the last ack:

```go
	// Replay every unacked event (a reconnect resends exactly the unacked
	// tail), then keep sending as new events arrive.
	sent := c.events.ackedSeq()
	sendEvents := func() error {
		for _, ev := range c.events.since(sent) {
			if err := stream.Send(eventMsg(ev)); err != nil {
				return err
			}
			sent = ev.Seq
		}
		return nil
	}
	if err := sendEvents(); err != nil {
		return classify(ctx, err, recvErr)
	}
```

Add to the main `select`:

```go
		case <-c.events.notify:
			if err := sendEvents(); err != nil {
				return classify(ctx, err, recvErr)
			}
```

Add an `Ack` case in the control switch (before `default`):

```go
			case *attachpb.ControlDown_Ack:
				if a := cd.GetAck(); a.GetStreamId() == c.events.streamID {
					c.events.ack(a.GetSeq())
				}
```

Replace the start of the `case <-doneCh:` branch so events are flushed and acked before Done:

```go
		case <-doneCh:
			if err := sendEvents(); err != nil {
				return classify(ctx, err, recvErr)
			}
			c.awaitFinalAck(ctx, controlCh)
			if err := stream.Send(statusMsg(Done)); err != nil {
				return classify(ctx, err, recvErr)
			}
			// ... existing CloseSend + wait unchanged ...
```

```go
// awaitFinalAck waits up to FlushTimeout for Jam to ack the last event, so the
// audit trail is complete before Done tears the cove down. An old Jam never
// acks; that costs at most FlushTimeout.
func (c *Client) awaitFinalAck(ctx context.Context, controlCh <-chan *attachpb.ControlDown) {
	last := c.events.lastSeq()
	if c.events.ackedSeq() >= last {
		return
	}
	timer := time.NewTimer(c.cfg.FlushTimeout)
	defer timer.Stop()
	for c.events.ackedSeq() < last {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			c.log.Warn("session events: final ack not received; reporting Done anyway", "last_seq", last, "acked", c.events.ackedSeq())
			return
		case cd := <-controlCh:
			if a := cd.GetAck(); a != nil && a.GetStreamId() == c.events.streamID {
				c.events.ack(a.GetSeq())
			}
		}
	}
}
```

- [ ] **Step 9: Run all covemaster tests**

Run: `go test ./internal/covemaster/ -race`
Expected: PASS (new and existing).

- [ ] **Step 10: Fix compile fallout**

`agentrun`'s test `recordHandle` must now implement `Event`. Add (it is extended properly in Task 3):

```go
func (h *recordHandle) Event(uint32, []byte, uint64) {}
```

Run: `go build ./... && go test ./internal/agentrun/` → PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/covemaster/ internal/agentrun/workload_test.go
git commit -m "feat(covemaster): sequenced, redacted, ack-trimmed session events over Attach

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: agentrun — stream-json argv, line splitting, turn numbering

**Files:**
- Create: `internal/agentrun/linesplit.go`, `internal/agentrun/linesplit_test.go`
- Modify: `internal/agentrun/spawner.go`, `internal/agentrun/workload.go`, `internal/agentrun/spawner_test.go`, `internal/agentrun/workload_test.go`, `internal/agentrun/resident_failure_test.go` (only if it defines a Spawner)
- Docs: if `docs/usage/jam/coves.md` (or any doc — `grep -rn "strict-mcp-config" docs/`) shows the claude argv, add `--output-format stream-json --verbose` there.

**Interfaces:**
- Consumes: `covemaster.Handle.Event(turn uint32, raw []byte, truncatedBytes uint64)` (Task 2).
- Produces: `Spawner.Spawn(ctx context.Context, bin string, args []string, dir string, stdout io.Writer) (Process, error)`; `const maxEventLine = 1 << 20`.

- [ ] **Step 1: Write the failing splitter tests** — `linesplit_test.go`:

```go
package agentrun

import (
	"strings"
	"testing"
)

type gotLine struct {
	s       string
	dropped uint64
}

func collect() (*lineSplitter, *[]gotLine) {
	var out []gotLine
	s := &lineSplitter{max: 8, emit: func(b []byte, d uint64) { out = append(out, gotLine{string(b), d}) }}
	return s, &out
}

func TestLineSplitterSplitsAcrossWrites(t *testing.T) {
	s, out := collect()
	s.Write([]byte("ab"))
	s.Write([]byte("c\nde\n\nf"))
	s.Flush()
	want := []gotLine{{"abc", 0}, {"de", 0}, {"f", 0}}
	if len(*out) != len(want) {
		t.Fatalf("got %+v", *out)
	}
	for i := range want {
		if (*out)[i] != want[i] {
			t.Fatalf("line %d: got %+v want %+v", i, (*out)[i], want[i])
		}
	}
}

func TestLineSplitterTruncatesAndCounts(t *testing.T) {
	s, out := collect()
	s.Write([]byte(strings.Repeat("x", 5)))
	s.Write([]byte(strings.Repeat("y", 10) + "\n"))
	if len(*out) != 1 || (*out)[0].s != "xxxxxyyy" || (*out)[0].dropped != 7 {
		t.Fatalf("got %+v", *out)
	}
}

func TestLineSplitterStripsCR(t *testing.T) {
	s, out := collect()
	s.Write([]byte("ok\r\n"))
	if (*out)[0].s != "ok" {
		t.Fatalf("got %q", (*out)[0].s)
	}
}

func TestLineSplitterWriteNeverErrors(t *testing.T) {
	s, _ := collect()
	n, err := s.Write([]byte(strings.Repeat("z", 100)))
	if n != 100 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
```

- [ ] **Step 2: Run** — `go test ./internal/agentrun/ -run TestLineSplitter` → FAIL (`undefined: lineSplitter`).

- [ ] **Step 3: Implement `linesplit.go`**

```go
package agentrun

import "bytes"

// maxEventLine caps one stream-json line forwarded as a session event; the
// rest is counted, not sent (the full line is still in cove-master.log).
const maxEventLine = 1 << 20

// lineSplitter is an io.Writer that cuts claude's stdout into lines and emits
// each (without the newline), keeping at most max bytes and counting the rest.
// Write never fails, so it is safe inside an io.MultiWriter. emit's slice is
// only valid during the call.
type lineSplitter struct {
	max     int
	emit    func(line []byte, dropped uint64)
	buf     []byte
	dropped uint64
}

func (s *lineSplitter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.keep(p)
			break
		}
		s.keep(p[:i])
		s.Flush()
		p = p[i+1:]
	}
	return n, nil
}

func (s *lineSplitter) keep(c []byte) {
	room := s.max - len(s.buf)
	if room < 0 {
		room = 0
	}
	if room > len(c) {
		room = len(c)
	}
	s.buf = append(s.buf, c[:room]...)
	s.dropped += uint64(len(c) - room)
}

// Flush emits any pending partial line (call once the process has exited).
func (s *lineSplitter) Flush() {
	line := bytes.TrimSuffix(s.buf, []byte("\r"))
	if len(line) > 0 || s.dropped > 0 {
		s.emit(line, s.dropped)
	}
	s.buf, s.dropped = s.buf[:0], 0
}
```

- [ ] **Step 4: Run** — `go test ./internal/agentrun/ -run TestLineSplitter` → PASS.

- [ ] **Step 5: Write the failing workload tests** — in `workload_test.go`:

Make `recordHandle` record events (replace the Task 2 stub):

```go
type recordedEvent struct {
	turn    uint32
	raw     string
	dropped uint64
}

// in recordHandle add: events []recordedEvent
func (h *recordHandle) Event(turn uint32, raw []byte, dropped uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, recordedEvent{turn, string(raw), dropped})
}

func (h *recordHandle) eventList() []recordedEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedEvent(nil), h.events...)
}
```

Update both fakes' `Spawn` to the new signature and keep the writer; `scriptedSpawner` gains `lines [][]string` (per call) written to stdout inside `Wait` before the result file:

```go
func (f *fakeSpawner) Spawn(ctx context.Context, bin string, args []string, dir string, stdout io.Writer) (Process, error) {
	f.bin, f.args, f.dir, f.stdout = bin, args, dir, stdout
	...
}
```

```go
func (f *scriptedSpawner) Spawn(_ context.Context, bin string, args []string, dir string, stdout io.Writer) (Process, error) {
	f.mu.Lock()
	i := len(f.calls)
	f.calls = append(f.calls, scriptedCall{bin: bin, args: append([]string(nil), args...), dir: dir})
	f.mu.Unlock()
	return scriptedProc{wait: func() error {
		if i < len(f.lines) && stdout != nil {
			for _, l := range f.lines[i] {
				io.WriteString(stdout, l+"\n")
			}
		}
		// ... existing result-writing body unchanged ...
	}}, nil
}
```

Update `TestRunSpawnArgs`'s expected argv:

```go
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--mcp-config", mcp, "--strict-mcp-config", "do the thing"}
```

Add to `TestRunResumesOnWake`'s (or a new test's) resume assertion: the `--continue` turn's argv is `-p --continue --output-format stream-json --verbose ...`. New tests:

```go
func TestRunForwardsStdoutLinesWithTurns(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{dir: dir,
		lines:   [][]string{{`{"type":"system"}`, `{"type":"result"}`}, {`{"type":"assistant"}`}},
		results: []string{`{"status":{"needs-input":{}}}`, `{"status":{"ok":{}}}`}}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	done := runAsync(context.Background(), w, h)
	waitFor(t, func() bool { return h.count(covemaster.Waiting) == 1 })
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := h.eventList()
	want := []recordedEvent{{1, `{"type":"system"}`, 0}, {1, `{"type":"result"}`, 0}, {2, `{"type":"assistant"}`, 0}}
	if len(got) != len(want) {
		t.Fatalf("events: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestRunForwardsTrailingPartialLine(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{}
	f.proc = scriptedProc{wait: func() error { io.WriteString(f.stdout, `{"no":"newline"}`); return nil }}
	w := New(Config{WorkDir: dir, Prompt: "p", MCPConfigPath: mcpConfigFile(t, dir), Spawner: f}, nil)
	h := &recordHandle{}
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if ev := h.eventList(); len(ev) != 1 || ev[0].raw != `{"no":"newline"}` {
		t.Fatalf("events: %+v", ev)
	}
}
```

(`fakeSpawner` gains a `stdout io.Writer` field. `runAsync`, `waitFor`, `mcpConfigFile` already exist in the file.)

Spawner test (`spawner_test.go`) — update the four existing `Spawn(..., "")` calls to pass `nil` as the new last arg, and add:

```go
func TestExecSpawnerTeesStdout(t *testing.T) {
	needSh(t)
	var buf bytes.Buffer
	p, err := execSpawner{grace: time.Second}.Spawn(context.Background(), "sh", []string{"-c", "echo hello"}, "", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello\n" {
		t.Fatalf("tee got %q", buf.String())
	}
}
```

- [ ] **Step 6: Run** — `go test ./internal/agentrun/` → FAIL (signature mismatch / argv).

- [ ] **Step 7: Implement**

`spawner.go`:

```go
type Spawner interface {
	// Spawn starts bin. stdout, when non-nil, receives a copy of the process's
	// stdout (cove-master's own stdout — the agent log — always gets it too).
	Spawn(ctx context.Context, bin string, args []string, dir string, stdout io.Writer) (Process, error)
}
```

```go
func (s execSpawner) Spawn(ctx context.Context, bin string, args []string, dir string, stdout io.Writer) (Process, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = s.grace
	cmd.Stdout = os.Stdout
	if stdout != nil {
		// Not an *os.File, so exec copies through a pipe and Wait returns only
		// after the copy drains — every line reaches stdout before Wait returns.
		cmd.Stdout = io.MultiWriter(os.Stdout, stdout)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execProcess{cmd: cmd}, nil
}
```

`workload.go` — argv:

```go
func (w *Workload) claudeArgs(prompt string, continued bool) []string {
	args := []string{"-p"}
	if continued {
		args = append(args, "--continue")
	}
	// stream-json stdout is the session event source (see docs/usage/jam/session-events.md).
	args = append(args, "--output-format", "stream-json", "--verbose")
	return append(args, "--dangerously-skip-permissions", "--mcp-config", w.cfg.MCPConfigPath, "--strict-mcp-config", prompt)
}
```

In `Run`, declare `var turn uint32` before the loop and replace the spawn/wait lines:

```go
		turn++
		t := turn
		split := &lineSplitter{max: maxEventLine, emit: func(line []byte, dropped uint64) { h.Event(t, line, dropped) }}
		proc, err := w.spawner.Spawn(ctx, "claude", args, w.cfg.WorkDir, split)
		...
		waitErr := proc.Wait()
		split.Flush()
```

- [ ] **Step 8: Run** — `go test ./internal/agentrun/ ./cmd/cove-master/ -race` → PASS.

- [ ] **Step 9: Docs** — `grep -rn "strict-mcp-config\|claude -p" docs/usage/jam/` and update any argv shown for the cove-master workload to include `--output-format stream-json --verbose`.

- [ ] **Step 10: Commit**

```bash
git add internal/agentrun/ docs/
git commit -m "feat(agentrun): run claude with stream-json and forward each stdout line as a session event

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `sessionevents` core — types, JSON encoding, index, hub

**Files:**
- Create: `internal/jam/sessionevents/event.go`, `index.go`, `hub.go`, `event_test.go`, `index_test.go`, `hub_test.go`

**Interfaces:**
- Produces (package `sessionevents`, imports only stdlib):

```go
const (
	KindEvent = "event"
	KindGap   = "gap"
)

type Stamp struct {
	Project, Role, Unit, Owner, SessionKind string
	RaisedAt                                time.Time
}

type Index struct {
	Type, Subtype, ToolName, ClaudeSessionID string
	CostUSD                                  float64
	InputTokens, OutputTokens, DurationMS    int64
	IsError                                  bool
}

type Event struct {
	ActorID, StreamID      string
	Seq                    uint64
	Kind                   string // KindEvent | KindGap
	GapFrom, GapTo         uint64 // KindGap only
	Turn                   uint32
	ObservedAt, ReceivedAt time.Time
	TruncatedBytes         uint64
	Raw                    []byte // line bytes (content-equal after a store round trip)
	Stamp                  Stamp
	Index                  Index
}
// Event implements json.Marshaler/Unmarshaler with the flat wire form below.

type Filter struct {
	ActorID, StreamID string // both required
	AfterSeq          uint64
	Limit             int // <= 0 → unbounded
}

type StreamInfo struct {
	StreamID        string
	FirstAt, LastAt time.Time // ReceivedAt of first/last row
	LastSeq         uint64
	Events          int
}

type Store interface {
	// Append persists ev. Appending an (actor, stream, seq) that already exists
	// is a no-op returning nil.
	Append(ev Event) error
	// HighWater is the largest stored seq for the stream (0 if none).
	HighWater(actorID, streamID string) (uint64, error)
	// List returns events in seq order.
	List(f Filter) ([]Event, error)
	// Streams lists an actor's streams, newest (by FirstAt) first.
	Streams(actorID string) ([]StreamInfo, error)
	// DeleteBefore removes data last received before t; returns rows removed.
	DeleteBefore(t time.Time) (int, error)
}

func ValidStreamID(s string) bool          // ^[0-9a-f]{32}$
func DeriveIndex(raw []byte) Index         // best effort, never fails
type Hub struct{ ... }
func NewHub() *Hub
func (h *Hub) Subscribe(actorID string, buf int) *Sub
func (h *Hub) Publish(ev Event)            // never blocks; a full subscriber is dropped (C closed)
type Sub struct{ C <-chan Event; ... }
func (s *Sub) Close()
```

Wire JSON form (one object; used by the file store and the export):

```json
{"actor_id":"w1","stream_id":"…","seq":3,"kind":"event","gap_from":0,"gap_to":0,"turn":1,
 "observed_at":"…","received_at":"…","truncated_bytes":0,
 "project":"default","role":"guest","unit":"","owner":"","session_kind":"","raised_at":"…",
 "type":"assistant","subtype":"","tool_name":"Bash","claude_session_id":"…",
 "cost_usd":0,"input_tokens":0,"output_tokens":0,"duration_ms":0,"is_error":false,
 "raw":{…}}            // or "raw_text":"…" when Raw is not valid JSON; neither for an empty Raw
```

- [ ] **Step 1: Write the failing tests**

`event_test.go`:

```go
package sessionevents

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidStreamID(t *testing.T) {
	for _, ok := range []string{"0123456789abcdef0123456789abcdef"} {
		if !ValidStreamID(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "../../etc/passwd", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcde", "0123456789abcdef0123456789abcdef0"} {
		if ValidStreamID(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestEventJSONRoundTripValidRaw(t *testing.T) {
	in := Event{ActorID: "w1", StreamID: "s", Seq: 3, Kind: KindEvent, Turn: 2,
		ReceivedAt: time.Unix(10, 0).UTC(), Raw: []byte(`{"type":"result","x":"<b>"}`),
		Stamp: Stamp{Project: "p"}, Index: Index{Type: "result", CostUSD: 0.5}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, ok := m["raw"].(map[string]any); !ok || m["raw_text"] != nil {
		t.Fatalf("valid JSON must encode as an object under raw: %s", b)
	}
	var out Event
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !RawEqual(out.Raw, in.Raw) || out.Seq != 3 || out.Stamp.Project != "p" || out.Index.CostUSD != 0.5 {
		t.Fatalf("round trip: %+v", out)
	}
}

func TestEventJSONRoundTripInvalidRaw(t *testing.T) {
	in := Event{ActorID: "w1", StreamID: "s", Seq: 1, Kind: KindEvent, Raw: []byte(`{"type":"assist`), TruncatedBytes: 99}
	b, _ := json.Marshal(in)
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["raw_text"] != `{"type":"assist` || m["raw"] != nil {
		t.Fatalf("invalid JSON must encode under raw_text: %s", b)
	}
	var out Event
	json.Unmarshal(b, &out)
	if string(out.Raw) != `{"type":"assist` || out.TruncatedBytes != 99 {
		t.Fatalf("round trip: %+v", out)
	}
}
```

`index_test.go` (shapes copied from a real `claude -p --output-format stream-json --verbose` capture, claude 2.1.287):

```go
package sessionevents

import "testing"

func TestDeriveIndexToolUse(t *testing.T) {
	ix := DeriveIndex([]byte(`{"type":"assistant","session_id":"S1","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo hi"}}]}}`))
	if ix.Type != "assistant" || ix.ToolName != "Bash" || ix.ClaudeSessionID != "S1" {
		t.Fatalf("%+v", ix)
	}
}

func TestDeriveIndexToolResultError(t *testing.T) {
	ix := DeriveIndex([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"boom","is_error":true}]}}`))
	if ix.Type != "user" || !ix.IsError {
		t.Fatalf("%+v", ix)
	}
}

func TestDeriveIndexStringContent(t *testing.T) {
	ix := DeriveIndex([]byte(`{"type":"user","session_id":"S2","message":{"role":"user","content":"do the thing"}}`))
	if ix.Type != "user" || ix.ClaudeSessionID != "S2" || ix.ToolName != "" {
		t.Fatalf("%+v", ix)
	}
}

func TestDeriveIndexResult(t *testing.T) {
	ix := DeriveIndex([]byte(`{"type":"result","subtype":"success","is_error":false,"duration_ms":3170,"total_cost_usd":0.0472883,"usage":{"input_tokens":10,"cache_creation_input_tokens":200,"cache_read_input_tokens":30,"output_tokens":6}}`))
	if ix.Subtype != "success" || ix.DurationMS != 3170 || ix.CostUSD != 0.0472883 || ix.InputTokens != 240 || ix.OutputTokens != 6 {
		t.Fatalf("%+v", ix)
	}
}

func TestDeriveIndexGarbage(t *testing.T) {
	if ix := DeriveIndex([]byte(`not json`)); ix != (Index{}) {
		t.Fatalf("%+v", ix)
	}
}
```

`hub_test.go`:

```go
package sessionevents

import (
	"testing"
	"time"
)

func TestHubDeliversPerActor(t *testing.T) {
	h := NewHub()
	a := h.Subscribe("a", 4)
	defer a.Close()
	b := h.Subscribe("b", 4)
	defer b.Close()
	h.Publish(Event{ActorID: "a", Seq: 1})
	select {
	case ev := <-a.C:
		if ev.Seq != 1 {
			t.Fatal(ev)
		}
	case <-time.After(time.Second):
		t.Fatal("a got nothing")
	}
	select {
	case ev := <-b.C:
		t.Fatalf("b got %v", ev)
	default:
	}
}

func TestHubDropsSlowSubscriberWithoutBlocking(t *testing.T) {
	h := NewHub()
	s := h.Subscribe("a", 1)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.Publish(Event{ActorID: "a", Seq: uint64(i + 1)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	<-s.C // the one buffered event
	if _, ok := <-s.C; ok {
		t.Fatal("slow subscriber's channel should be closed")
	}
	s.Close() // idempotent after a drop
}
```

- [ ] **Step 2: Run** — `go test ./internal/jam/sessionevents/` → FAIL (package does not exist).

- [ ] **Step 3: Implement `event.go`**

```go
// Package sessionevents is Jam's store and live fan-out for managed-cove
// session events — each line of a cove agent's claude stream-json stdout,
// delivered over the Attach stream. It is grpc-free and never imports
// internal/jam. See docs/usage/jam/session-events.md.
package sessionevents

import (
	"bytes"
	"encoding/json"
	"regexp"
	"time"
)

// (constants, Stamp, Index, Event, Filter, StreamInfo, Store exactly as in the
// Interfaces block above)

var streamIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidStreamID reports whether s is a cove-generated stream id. Stores use it
// as a path segment, so anything else is rejected at ingest.
func ValidStreamID(s string) bool { return streamIDRe.MatchString(s) }

type wireEvent struct {
	ActorID         string          `json:"actor_id"`
	StreamID        string          `json:"stream_id"`
	Seq             uint64          `json:"seq"`
	Kind            string          `json:"kind"`
	GapFrom         uint64          `json:"gap_from,omitempty"`
	GapTo           uint64          `json:"gap_to,omitempty"`
	Turn            uint32          `json:"turn"`
	ObservedAt      time.Time       `json:"observed_at"`
	ReceivedAt      time.Time       `json:"received_at"`
	TruncatedBytes  uint64          `json:"truncated_bytes"`
	Project         string          `json:"project"`
	Role            string          `json:"role"`
	Unit            string          `json:"unit"`
	Owner           string          `json:"owner"`
	SessionKind     string          `json:"session_kind"`
	RaisedAt        time.Time       `json:"raised_at"`
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	ToolName        string          `json:"tool_name"`
	ClaudeSessionID string          `json:"claude_session_id"`
	CostUSD         float64         `json:"cost_usd"`
	InputTokens     int64           `json:"input_tokens"`
	OutputTokens    int64           `json:"output_tokens"`
	DurationMS      int64           `json:"duration_ms"`
	IsError         bool            `json:"is_error"`
	Raw             json.RawMessage `json:"raw,omitempty"`
	RawText         *string         `json:"raw_text,omitempty"`
}

func (e Event) MarshalJSON() ([]byte, error) {
	w := wireEvent{ActorID: e.ActorID, StreamID: e.StreamID, Seq: e.Seq, Kind: e.Kind, GapFrom: e.GapFrom, GapTo: e.GapTo,
		Turn: e.Turn, ObservedAt: e.ObservedAt, ReceivedAt: e.ReceivedAt, TruncatedBytes: e.TruncatedBytes,
		Project: e.Stamp.Project, Role: e.Stamp.Role, Unit: e.Stamp.Unit, Owner: e.Stamp.Owner,
		SessionKind: e.Stamp.SessionKind, RaisedAt: e.Stamp.RaisedAt,
		Type: e.Index.Type, Subtype: e.Index.Subtype, ToolName: e.Index.ToolName, ClaudeSessionID: e.Index.ClaudeSessionID,
		CostUSD: e.Index.CostUSD, InputTokens: e.Index.InputTokens, OutputTokens: e.Index.OutputTokens,
		DurationMS: e.Index.DurationMS, IsError: e.Index.IsError}
	switch {
	case len(e.Raw) == 0:
	case json.Valid(e.Raw):
		w.Raw = json.RawMessage(e.Raw)
	default:
		s := string(e.Raw)
		w.RawText = &s
	}
	return json.Marshal(w)
}

func (e *Event) UnmarshalJSON(b []byte) error {
	var w wireEvent
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*e = Event{ActorID: w.ActorID, StreamID: w.StreamID, Seq: w.Seq, Kind: w.Kind, GapFrom: w.GapFrom, GapTo: w.GapTo,
		Turn: w.Turn, ObservedAt: w.ObservedAt, ReceivedAt: w.ReceivedAt, TruncatedBytes: w.TruncatedBytes,
		Stamp: Stamp{Project: w.Project, Role: w.Role, Unit: w.Unit, Owner: w.Owner, SessionKind: w.SessionKind, RaisedAt: w.RaisedAt},
		Index: Index{Type: w.Type, Subtype: w.Subtype, ToolName: w.ToolName, ClaudeSessionID: w.ClaudeSessionID,
			CostUSD: w.CostUSD, InputTokens: w.InputTokens, OutputTokens: w.OutputTokens, DurationMS: w.DurationMS, IsError: w.IsError}}
	switch {
	case len(w.Raw) > 0:
		e.Raw = []byte(w.Raw)
	case w.RawText != nil:
		e.Raw = []byte(*w.RawText)
	}
	return nil
}

// RawEqual reports whether two raw lines are the same event: JSON-equal when
// both are valid JSON (stores may compact or reorder), byte-equal otherwise.
func RawEqual(a, b []byte) bool {
	if json.Valid(a) && json.Valid(b) {
		var x, y any
		json.Unmarshal(a, &x)
		json.Unmarshal(b, &y)
		xb, _ := json.Marshal(x)
		yb, _ := json.Marshal(y)
		return bytes.Equal(xb, yb)
	}
	return bytes.Equal(a, b)
}
```

- [ ] **Step 4: Implement `index.go`**

```go
package sessionevents

import "encoding/json"

type rawEnvelope struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	SessionID    string          `json:"session_id"`
	Message      *rawMessage     `json:"message"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	DurationMS   int64           `json:"duration_ms"`
	IsError      bool            `json:"is_error"`
	Usage        *rawUsage       `json:"usage"`
}

type rawMessage struct {
	// Content is a block array for assistant/tool messages but a plain string
	// for a user prompt — decode lazily so neither shape fails the envelope.
	Content json.RawMessage `json:"content"`
}

type rawBlock struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	IsError bool   `json:"is_error"`
}

type rawUsage struct {
	InputTokens   int64 `json:"input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
}

// DeriveIndex extracts the thin, queryable index from one stream-json line.
// It is best-effort: unknown or malformed input yields a zero/partial Index,
// never an error — the raw line is what is stored.
func DeriveIndex(raw []byte) Index {
	var env rawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Index{}
	}
	ix := Index{Type: env.Type, Subtype: env.Subtype, ClaudeSessionID: env.SessionID}
	if env.Message != nil {
		var blocks []rawBlock
		if json.Unmarshal(env.Message.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "tool_use" && ix.ToolName == "" {
					ix.ToolName = b.Name
				}
				if b.Type == "tool_result" && b.IsError {
					ix.IsError = true
				}
			}
		}
	}
	if env.Type == "result" {
		ix.CostUSD, ix.DurationMS, ix.IsError = env.TotalCostUSD, env.DurationMS, env.IsError
		if u := env.Usage; u != nil {
			ix.InputTokens = u.InputTokens + u.CacheCreation + u.CacheRead
			ix.OutputTokens = u.OutputTokens
		}
	}
	return ix
}
```

- [ ] **Step 5: Implement `hub.go`**

```go
package sessionevents

import "sync"

// Hub fans live events out to per-actor subscribers (the admin UI's SSE now;
// automated feedback / the Studio view later). Publish never blocks: a
// subscriber whose buffer is full is dropped (its C is closed) and recovers by
// re-subscribing and backfilling from the Store.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[*Sub]struct{}
}

type Sub struct {
	C      <-chan Event
	c      chan Event
	actor  string
	hub    *Hub
	closed bool // guarded by hub.mu
}

func NewHub() *Hub { return &Hub{subs: map[string]map[*Sub]struct{}{}} }

func (h *Hub) Subscribe(actorID string, buf int) *Sub {
	if buf < 1 {
		buf = 1
	}
	c := make(chan Event, buf)
	s := &Sub{C: c, c: c, actor: actorID, hub: h}
	h.mu.Lock()
	if h.subs[actorID] == nil {
		h.subs[actorID] = map[*Sub]struct{}{}
	}
	h.subs[actorID][s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[ev.ActorID] {
		select {
		case s.c <- ev:
		default:
			h.removeLocked(s)
		}
	}
}

// Close unsubscribes; safe to call more than once and after a drop.
func (s *Sub) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s)
}

func (h *Hub) removeLocked(s *Sub) {
	if s.closed {
		return
	}
	s.closed = true
	delete(h.subs[s.actor], s)
	if len(h.subs[s.actor]) == 0 {
		delete(h.subs, s.actor)
	}
	close(s.c)
}
```

- [ ] **Step 6: Run** — `go test ./internal/jam/sessionevents/ -race` → PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jam/sessionevents/
git commit -m "feat(sessionevents): event types, raw/raw_text encoding, stream-json index, live hub

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Stores — conformance suite, JSONL file store, no-op store

**Files:**
- Create: `internal/jam/sessionevents/sessioneventstest/conformance.go`, `internal/jam/sessionevents/filestore.go`, `internal/jam/sessionevents/nopstore.go`, `internal/jam/sessionevents/filestore_test.go`

**Interfaces:**
- Consumes: Task 4 types.
- Produces: `sessioneventstest.RunConformance(t *testing.T, newStore func(t *testing.T) sessionevents.Store)`; `sessionevents.OpenFileStore(dir string) (*FileStore, error)`; `sessionevents.NopStore{}` (value type implementing Store).

- [ ] **Step 1: Write the conformance suite** — `sessioneventstest/conformance.go`:

```go
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
```

- [ ] **Step 2: Write the file-store test** — `filestore_test.go`:

```go
package sessionevents_test

import (
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

func TestNopStore(t *testing.T) {
	var s sessionevents.Store = sessionevents.NopStore{}
	if err := s.Append(sessionevents.Event{Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Streams("w1"); len(got) != 0 {
		t.Fatal(got)
	}
}
```

- [ ] **Step 3: Run** — `go test ./internal/jam/sessionevents/...` → FAIL (`undefined: OpenFileStore`).

- [ ] **Step 4: Implement `nopstore.go`**

```go
package sessionevents

import "time"

// NopStore is the "no storage configured" backend: events are accepted (and
// therefore acked) and dropped, so coves never back up.
type NopStore struct{}

func (NopStore) Append(Event) error                         { return nil }
func (NopStore) HighWater(string, string) (uint64, error)   { return 0, nil }
func (NopStore) List(Filter) ([]Event, error)               { return nil, nil }
func (NopStore) Streams(string) ([]StreamInfo, error)       { return nil, nil }
func (NopStore) DeleteBefore(time.Time) (int, error)        { return 0, nil }
```

- [ ] **Step 5: Implement `filestore.go`**

```go
package sessionevents

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxRecordBytes bounds one JSONL record: a 1 MiB raw line can roughly double
// when JSON-escaped as raw_text, plus the envelope.
const maxRecordBytes = 4 << 20

var safeActorRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// FileStore keeps one append-only JSONL file per (actor, stream) under
// dir/<actor>/<stream>.jsonl. Single-writer (the serve process).
type FileStore struct {
	dir  string
	mu   sync.Mutex
	high map[string]uint64 // path → last appended seq (lazy)
}

func OpenFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session-events-dir: %w", err)
	}
	return &FileStore{dir: dir, high: map[string]uint64{}}, nil
}

func (s *FileStore) path(actorID, streamID string) (string, error) {
	if !safeActorRe.MatchString(actorID) || strings.Trim(actorID, ".") == "" {
		return "", fmt.Errorf("sessionevents: unsafe actor id %q", actorID)
	}
	if !ValidStreamID(streamID) {
		return "", fmt.Errorf("sessionevents: invalid stream id %q", streamID)
	}
	return filepath.Join(s.dir, actorID, streamID+".jsonl"), nil
}

func readAll(path string) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxRecordBytes)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // a torn final line from a crash: skip, never fail the read
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func (s *FileStore) highLocked(path string) (uint64, error) {
	if hw, ok := s.high[path]; ok {
		return hw, nil
	}
	evs, err := readAll(path)
	if err != nil {
		return 0, err
	}
	var hw uint64
	for _, e := range evs {
		if e.Seq > hw {
			hw = e.Seq
		}
	}
	s.high[path] = hw
	return hw, nil
}

func (s *FileStore) Append(ev Event) error {
	p, err := s.path(ev.ActorID, ev.StreamID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hw, err := s.highLocked(p)
	if err != nil {
		return err
	}
	if ev.Seq <= hw {
		return nil
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.high[p] = ev.Seq
	return nil
}

func (s *FileStore) HighWater(actorID, streamID string) (uint64, error) {
	p, err := s.path(actorID, streamID)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.highLocked(p)
}

func (s *FileStore) List(f Filter) ([]Event, error) {
	p, err := s.path(f.ActorID, f.StreamID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, err := readAll(p)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range evs {
		if e.Seq > f.AfterSeq {
			out = append(out, e)
			if f.Limit > 0 && len(out) == f.Limit {
				break
			}
		}
	}
	return out, nil
}

func (s *FileStore) Streams(actorID string) ([]StreamInfo, error) {
	if !safeActorRe.MatchString(actorID) || strings.Trim(actorID, ".") == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, actorID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []StreamInfo
	for _, de := range entries {
		id, ok := strings.CutSuffix(de.Name(), ".jsonl")
		if !ok || !ValidStreamID(id) {
			continue
		}
		evs, err := readAll(filepath.Join(s.dir, actorID, de.Name()))
		if err != nil || len(evs) == 0 {
			continue
		}
		out = append(out, StreamInfo{StreamID: id, FirstAt: evs[0].ReceivedAt, LastAt: evs[len(evs)-1].ReceivedAt,
			LastSeq: evs[len(evs)-1].Seq, Events: len(evs)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstAt.After(out[j].FirstAt) })
	return out, nil
}

// DeleteBefore removes whole stream files whose last event was received
// before t (a stream is the retention unit for the file backend).
func (s *FileStore) DeleteBefore(t time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := filepath.Glob(filepath.Join(s.dir, "*", "*.jsonl"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range files {
		evs, err := readAll(p)
		if err != nil || len(evs) == 0 || !evs[len(evs)-1].ReceivedAt.Before(t) {
			continue
		}
		if err := os.Remove(p); err != nil {
			return n, err
		}
		delete(s.high, p)
		n += len(evs)
	}
	return n, nil
}
```

- [ ] **Step 6: Run** — `go test ./internal/jam/sessionevents/... -race` → PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jam/sessionevents/
git commit -m "feat(sessionevents): JSONL file store, no-op store, shared store conformance suite

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Ingest — dedupe, gap rows, persist, publish

**Files:**
- Create: `internal/jam/sessionevents/ingest.go`, `internal/jam/sessionevents/ingest_test.go`

**Interfaces:**
- Consumes: Store, Hub, DeriveIndex, ValidStreamID (Tasks 4–5).
- Produces:

```go
type Incoming struct {
	StreamID       string
	Seq            uint64
	Turn           uint32
	ObservedAt     time.Time
	Raw            []byte
	TruncatedBytes uint64
}
var ErrBadStreamID = errors.New("sessionevents: invalid stream id")
var ErrBadSeq = errors.New("sessionevents: seq must be >= 1")
func NewIngest(store Store, hub *Hub, now func() time.Time) *Ingest // now nil → time.Now
func (in *Ingest) Append(actorID string, stamp Stamp, ev Incoming) (highWater uint64, err error)
```

- [ ] **Step 1: Write the failing tests** — `ingest_test.go`:

```go
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
```

- [ ] **Step 2: Run** — `go test ./internal/jam/sessionevents/ -run TestIngest` → FAIL.

- [ ] **Step 3: Implement `ingest.go`**

```go
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
```

Note the gap row's `Seq` is `GapTo` (a seq that never holds a real event), so it fits the `(actor, stream, seq)` key and sorts in place; if the gap append succeeds but the event append fails, a retry re-appends the same gap seq, which every Store treats as a no-op.

- [ ] **Step 4: Run** — `go test ./internal/jam/sessionevents/... -race` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/sessionevents/
git commit -m "feat(sessionevents): ingest with replay dedupe, explicit gap rows, publish-after-persist

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Attach server — ingest events, ack on a tick

**Files:**
- Modify: `internal/jam/attach/server.go`
- Test: `internal/jam/attach/events_test.go` (create), plus extend `internal/covemaster/client_test.go` with one end-to-end test against the real server

**Interfaces:**
- Consumes: `sessionevents.Ingest.Append`, `sessionevents.Stamp`, `sessionevents.Incoming` (Task 6); generated types (Task 1).
- Produces: `func (s *Server) SetSessionEvents(in *sessionevents.Ingest)` (call before serving); `const ackInterval = 250 * time.Millisecond` (package var `ackEvery` overridable in tests).

- [ ] **Step 1: Write the failing tests** — `internal/jam/attach/events_test.go` (reuses `harness`, `authCtx`, `eventually` from `server_test.go`):

```go
package attach

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const sid = "0123456789abcdef0123456789abcdef"

func evMsg(seq uint64, raw string) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Event{Event: &attachpb.SessionEvent{StreamId: sid, Seq: seq, Turn: 1, Raw: []byte(raw)}}}
}

func TestAttachIngestsEventsAndAcks(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	st, _ := sessionevents.OpenFileStore(t.TempDir())
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
	st, _ := sessionevents.OpenFileStore(t.TempDir())
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
```

End-to-end in `internal/covemaster/client_test.go` (real server, real acks — proves the two halves agree):

```go
func TestEventsEndToEndWithRealAttachServer(t *testing.T) {
	_, srv, dialOpt, tok, secret := serverHarness(t)
	st, _ := sessionevents.OpenFileStore(t.TempDir())
	srv.SetSessionEvents(sessionevents.NewIngest(st, sessionevents.NewHub(), nil))
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: secret,
		DialOptions: []grpc.DialOption{dialOpt, grpc.WithTransportCredentials(insecure.NewCredentials())}}, nil)
	start := time.Now()
	if err := c.Run(context.Background(), &eventWorkload{lines: []string{`{"type":"system"}`, `{"type":"result"}`}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.List(sessionevents.Filter{ActorID: "w1", StreamID: c.StreamID()})
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Done waited for the flush timeout — real server did not ack")
	}
}
```

- [ ] **Step 2: Run** — `go test ./internal/jam/attach/ ./internal/covemaster/` → FAIL (`SetSessionEvents undefined`).

- [ ] **Step 3: Implement in `server.go`**

Add field `events *sessionevents.Ingest` to `Server` and:

```go
// ackEvery is how often a connection flushes cumulative EventAcks.
var ackEvery = 250 * time.Millisecond

// SetSessionEvents enables session-event ingest. Call before serving. Without
// it, events are ignored (never acked) — at-jam always sets one, using a
// no-op store when storage is not configured.
func (s *Server) SetSessionEvents(in *sessionevents.Ingest) { s.events = in }

// ackState collects the latest durable seq per stream for one connection.
type ackState struct {
	mu      sync.Mutex
	pending map[string]uint64
}

func (a *ackState) set(stream string, seq uint64) {
	a.mu.Lock()
	a.pending[stream] = seq
	a.mu.Unlock()
}

func (a *ackState) drain() map[string]uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.pending
	a.pending = map[string]uint64{}
	return out
}

func stampOf(inst jam.Instance) sessionevents.Stamp {
	return sessionevents.Stamp{Project: inst.Project, Role: inst.Role, Unit: inst.Unit, Owner: inst.Owner,
		SessionKind: inst.SessionKind, RaisedAt: inst.RaisedAt}
}
```

In `Attach`, create `acks := &ackState{pending: map[string]uint64{}}` before the send goroutine; give the send loop a ticker that flushes acks (only this goroutine ever calls `stream.Send`):

```go
	go func() { // send loop: control messages + coalesced event acks
		tick := time.NewTicker(ackEvery)
		defer tick.Stop()
		for {
			select {
			case <-stream.Context().Done():
				return
			case cd, ok := <-ch:
				if !ok {
					return
				}
				if err := stream.Send(cd); err != nil {
					return
				}
			case <-tick.C:
				for id, seq := range acks.drain() {
					if err := stream.Send(&attachpb.ControlDown{Msg: &attachpb.ControlDown_Ack{Ack: &attachpb.EventAck{StreamId: id, Seq: seq}}}); err != nil {
						return
					}
				}
			}
		}
	}()
```

Add a recv-loop case:

```go
		case *attachpb.StatusUp_Event:
			if s.events == nil {
				continue
			}
			ev := m.Event
			inst, _ := s.store.GetInstance(actorID)
			hw, err := s.events.Append(actorID, stampOf(inst), sessionevents.Incoming{
				StreamID: ev.GetStreamId(), Seq: ev.GetSeq(), Turn: ev.GetTurn(),
				ObservedAt: time.UnixMilli(ev.GetObservedUnixMs()), Raw: ev.GetRaw(), TruncatedBytes: ev.GetTruncatedBytes(),
			})
			if err != nil {
				// Never log raw content — it is agent output (see session-events.md).
				s.log.Warn("session event not stored", "actor", actorID, "seq", ev.GetSeq(), "err", err.Error())
				continue
			}
			acks.set(ev.GetStreamId(), hw)
```

(Imports: `time`, `internal/jam/sessionevents`.)

- [ ] **Step 4: Run** — `go test ./internal/jam/attach/ ./internal/covemaster/ -race` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/attach/ internal/covemaster/client_test.go
git commit -m "feat(attach): ingest session events and ack them on a 250ms tick

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: `sessionpg` — Postgres backend

**Files:**
- Create: `internal/jam/sessionevents/sessionpg/sessionpg.go`, `migrations.go`, `migrations/0001_session_events.sql`, `sessionpg_integration_test.go`

**Interfaces:**
- Consumes: `sessionevents.Store` contract + conformance suite (Task 5).
- Produces: `sessionpg.New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Store, error)`; `*Store` implements `sessionevents.Store`; `Close()` is a no-op (pool owned by the control-plane store).

- [ ] **Step 1: Write the integration test** — `sessionpg_integration_test.go`:

```go
//go:build integration

package sessionpg_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessioneventstest"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessionpg"
)

func TestPostgresSessionEventsConformance(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres session-events integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	sessioneventstest.RunConformance(t, func(t *testing.T) sessionevents.Store {
		s, err := sessionpg.New(context.Background(), pool, nil)
		if err != nil {
			t.Fatalf("sessionpg.New: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `TRUNCATE session_events`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return s
	})
}
```

- [ ] **Step 2: Run** — `go vet -tags integration ./internal/jam/sessionevents/sessionpg/` → FAIL (package missing). If a Postgres is reachable, also `JAM_TEST_POSTGRES_DSN=… go test -tags integration ./internal/jam/sessionevents/sessionpg/`.

- [ ] **Step 3: Migration** — `migrations/0001_session_events.sql`:

```sql
CREATE TABLE session_events (
    actor_id          text        NOT NULL,
    stream_id         text        NOT NULL,
    seq               bigint      NOT NULL,
    kind              text        NOT NULL,
    gap_from          bigint      NOT NULL DEFAULT 0,
    gap_to            bigint      NOT NULL DEFAULT 0,
    turn              integer     NOT NULL DEFAULT 0,
    observed_at       timestamptz NOT NULL,
    received_at       timestamptz NOT NULL,
    truncated_bytes   bigint      NOT NULL DEFAULT 0,
    project           text        NOT NULL DEFAULT '',
    role              text        NOT NULL DEFAULT '',
    unit              text        NOT NULL DEFAULT '',
    owner             text        NOT NULL DEFAULT '',
    session_kind      text        NOT NULL DEFAULT '',
    raised_at         timestamptz NOT NULL,
    type              text        NOT NULL DEFAULT '',
    subtype           text        NOT NULL DEFAULT '',
    tool_name         text        NOT NULL DEFAULT '',
    claude_session_id text        NOT NULL DEFAULT '',
    cost_usd          double precision NOT NULL DEFAULT 0,
    input_tokens      bigint      NOT NULL DEFAULT 0,
    output_tokens     bigint      NOT NULL DEFAULT 0,
    duration_ms       bigint      NOT NULL DEFAULT 0,
    is_error          boolean     NOT NULL DEFAULT false,
    raw               jsonb,
    raw_text          text,
    PRIMARY KEY (actor_id, stream_id, seq)
);
CREATE INDEX session_events_actor_received ON session_events (actor_id, received_at);
CREATE INDEX session_events_type_tool ON session_events (type, tool_name);
```

`migrations.go`:

```go
package sessionpg

import "embed"

//go:embed migrations/*.sql
var migrationFiles embed.FS
```

- [ ] **Step 4: Implement `sessionpg.go`**

Copy `internal/intercom/intercompg/intercompg.go`'s `migrate` method verbatim, changing: the lock constant to `const migrateAdvisoryLock = 0x73657373696f6e65 // "sessione"`, the table to `session_events_schema_migrations`, and the error prefixes to `sessionpg:`. Then:

```go
// Package sessionpg is the Postgres backend for sessionevents.Store. It shares
// the control-plane *pgxpool.Pool and owns its own migrations.
package sessionpg

// imports: context, encoding/json, errors, fmt, io, io/fs, log/slog, sort,
// strconv, strings, time, pgx, pgconn, pgxpool, sessionevents

type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ sessionevents.Store = (*Store)(nil)

func New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Store{pool: pool, log: log}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return nil }

const insertSQL = `INSERT INTO session_events (actor_id, stream_id, seq, kind, gap_from, gap_to, turn,
  observed_at, received_at, truncated_bytes, project, role, unit, owner, session_kind, raised_at,
  type, subtype, tool_name, claude_session_id, cost_usd, input_tokens, output_tokens, duration_ms, is_error,
  raw, raw_text)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
ON CONFLICT (actor_id, stream_id, seq) DO NOTHING`

func (s *Store) insert(ev sessionevents.Event, raw, rawText any) error {
	_, err := s.pool.Exec(context.Background(), insertSQL,
		ev.ActorID, ev.StreamID, int64(ev.Seq), ev.Kind, int64(ev.GapFrom), int64(ev.GapTo), int32(ev.Turn),
		ev.ObservedAt, ev.ReceivedAt, int64(ev.TruncatedBytes),
		ev.Stamp.Project, ev.Stamp.Role, ev.Stamp.Unit, ev.Stamp.Owner, ev.Stamp.SessionKind, ev.Stamp.RaisedAt,
		ev.Index.Type, ev.Index.Subtype, ev.Index.ToolName, ev.Index.ClaudeSessionID,
		ev.Index.CostUSD, ev.Index.InputTokens, ev.Index.OutputTokens, ev.Index.DurationMS, ev.Index.IsError,
		raw, rawText)
	return err
}

// textSafe makes a raw line storable in a text column: Postgres text cannot
// hold NUL bytes, so they become U+FFFD (the only lossy case; logged).
func (s *Store) textSafe(ev sessionevents.Event) string {
	t := string(ev.Raw)
	if strings.ContainsRune(t, 0) {
		s.log.Warn("sessionpg: NUL bytes replaced in raw_text", "actor", ev.ActorID, "seq", ev.Seq)
		t = strings.ReplaceAll(t, "\x00", "\uFFFD")
	}
	return t
}

func (s *Store) Append(ev sessionevents.Event) error {
	if len(ev.Raw) > 0 && json.Valid(ev.Raw) {
		err := s.insert(ev, string(ev.Raw), nil)
		var pgErr *pgconn.PgError
		// 22P05 untranslatable_character: jsonb rejects \u0000 — keep the line as text.
		if errors.As(err, &pgErr) && pgErr.Code == "22P05" {
			return s.insert(ev, nil, s.textSafe(ev))
		}
		return err
	}
	if len(ev.Raw) == 0 {
		return s.insert(ev, nil, nil)
	}
	return s.insert(ev, nil, s.textSafe(ev))
}

func (s *Store) HighWater(actorID, streamID string) (uint64, error) {
	var hw int64
	err := s.pool.QueryRow(context.Background(),
		`SELECT COALESCE(MAX(seq), 0) FROM session_events WHERE actor_id=$1 AND stream_id=$2`, actorID, streamID).Scan(&hw)
	return uint64(hw), err
}

const selectCols = `actor_id, stream_id, seq, kind, gap_from, gap_to, turn, observed_at, received_at, truncated_bytes,
  project, role, unit, owner, session_kind, raised_at, type, subtype, tool_name, claude_session_id,
  cost_usd, input_tokens, output_tokens, duration_ms, is_error, raw::text, raw_text`

func (s *Store) List(f sessionevents.Filter) ([]sessionevents.Event, error) {
	q := `SELECT ` + selectCols + ` FROM session_events WHERE actor_id=$1 AND stream_id=$2 AND seq > $3 ORDER BY seq`
	args := []any{f.ActorID, f.StreamID, int64(f.AfterSeq)}
	if f.Limit > 0 {
		q += ` LIMIT $4`
		args = append(args, f.Limit)
	}
	rows, err := s.pool.Query(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionevents.Event
	for rows.Next() {
		var e sessionevents.Event
		var seq, gf, gt, trunc int64
		var turn int32
		var raw, rawText *string
		if err := rows.Scan(&e.ActorID, &e.StreamID, &seq, &e.Kind, &gf, &gt, &turn, &e.ObservedAt, &e.ReceivedAt, &trunc,
			&e.Stamp.Project, &e.Stamp.Role, &e.Stamp.Unit, &e.Stamp.Owner, &e.Stamp.SessionKind, &e.Stamp.RaisedAt,
			&e.Index.Type, &e.Index.Subtype, &e.Index.ToolName, &e.Index.ClaudeSessionID,
			&e.Index.CostUSD, &e.Index.InputTokens, &e.Index.OutputTokens, &e.Index.DurationMS, &e.Index.IsError,
			&raw, &rawText); err != nil {
			return nil, err
		}
		e.Seq, e.GapFrom, e.GapTo, e.TruncatedBytes, e.Turn = uint64(seq), uint64(gf), uint64(gt), uint64(trunc), uint32(turn)
		switch {
		case raw != nil:
			e.Raw = []byte(*raw)
		case rawText != nil:
			e.Raw = []byte(*rawText)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Streams(actorID string) ([]sessionevents.StreamInfo, error) {
	rows, err := s.pool.Query(context.Background(), `SELECT stream_id, MIN(received_at), MAX(received_at), MAX(seq), COUNT(*)
FROM session_events WHERE actor_id=$1 GROUP BY stream_id ORDER BY MIN(received_at) DESC`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionevents.StreamInfo
	for rows.Next() {
		var si sessionevents.StreamInfo
		var last int64
		if err := rows.Scan(&si.StreamID, &si.FirstAt, &si.LastAt, &last, &si.Events); err != nil {
			return nil, err
		}
		si.LastSeq = uint64(last)
		out = append(out, si)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBefore(t time.Time) (int, error) {
	tag, err := s.pool.Exec(context.Background(), `DELETE FROM session_events WHERE received_at < $1`, t)
	return int(tag.RowsAffected()), err
}
```

(`raw::text` returns the jsonb as text; passing `string(ev.Raw)` for the jsonb parameter lets Postgres parse it.)

- [ ] **Step 5: Verify** — `go build ./... && go vet -tags integration ./internal/jam/sessionevents/sessionpg/` → clean. With a Postgres available: `JAM_TEST_POSTGRES_DSN=… go test -tags integration ./internal/jam/sessionevents/sessionpg/ -v` → PASS (including the `\u0000` case → stored via `raw_text`). If no Postgres is reachable from the sandbox, say so explicitly in the task report — do not claim it passed.

- [ ] **Step 6: Commit**

```bash
git add internal/jam/sessionevents/sessionpg/
git commit -m "feat(sessionpg): Postgres session-events store with jsonb + raw_text fallback

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Retention, serve config, and at-jam wiring

**Files:**
- Create: `internal/jam/sessionevents/retention.go`, `internal/jam/sessionevents/retention_test.go`
- Modify: `cmd/at-jam/config.go`, `cmd/at-jam/config_test.go`, `cmd/at-jam/main.go`

**Interfaces:**
- Consumes: FileStore, NopStore, sessionpg.New, Ingest, Hub, `Server.SetSessionEvents`.
- Produces: `sessionevents.ParseRetention(s string) (time.Duration, error)` (`""` → 0 = keep forever; `<N>d` = N days; else `time.ParseDuration`; negative → error); `sessionevents.RunRetention(ctx context.Context, store Store, keep, every time.Duration, now func() time.Time, log *slog.Logger)`; serve-config fields `SessionEventsDir` (`session-events-dir`), `SessionEventsRetention` (`session-events-retention`); in `main.go` the locals `sessStore sessionevents.Store` and `sessHub *sessionevents.Hub` (Tasks 10–11 use them).

- [ ] **Step 1: Write the failing tests**

`retention_test.go`:

```go
package sessionevents_test

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

func TestParseRetention(t *testing.T) {
	cases := map[string]time.Duration{"": 0, "90d": 90 * 24 * time.Hour, "36h": 36 * time.Hour}
	for in, want := range cases {
		got, err := sessionevents.ParseRetention(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v %v want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "-1h", "xd", "-3d"} {
		if _, err := sessionevents.ParseRetention(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestRunRetentionSweepsOnStart(t *testing.T) {
	st, _ := sessionevents.OpenFileStore(t.TempDir())
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 1, Kind: sessionevents.KindEvent, ReceivedAt: old, Raw: []byte(`{}`)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sessionevents.RunRetention(ctx, st, 24*time.Hour, time.Hour, func() time.Time { return old.Add(72 * time.Hour) }, nil)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := st.List(sessionevents.Filter{ActorID: "w1", StreamID: sid}); len(got) == 0 {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("retention did not sweep on start")
}
```

In `cmd/at-jam/config_test.go`:

```go
func TestParseServeConfigSessionEvents(t *testing.T) {
	c, err := parseServeConfig([]byte("session-events-dir: /var/lib/jam/events\nsession-events-retention: 30d\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.SessionEventsDir != "/var/lib/jam/events" || c.SessionEventsRetention != "30d" {
		t.Fatalf("%+v", c)
	}
	if err := c.validateSessionEvents(); err != nil {
		t.Fatal(err)
	}
	bad, _ := parseServeConfig([]byte("session-events-retention: forever\n"))
	if err := bad.validateSessionEvents(); err == nil {
		t.Fatal("want a validation error")
	}
}
```

- [ ] **Step 2: Run** — `go test ./internal/jam/sessionevents/ ./cmd/at-jam/ -run 'Retention|SessionEvents'` → FAIL.

- [ ] **Step 3: Implement `retention.go`**

```go
package sessionevents

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// ParseRetention parses session-events-retention: "" (keep forever → 0),
// "<N>d" (days), or any time.ParseDuration string. Negative is an error.
func ParseRetention(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	var d time.Duration
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("session-events-retention %q: %w", s, err)
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("session-events-retention %q: %w", s, err)
		}
	}
	if d < 0 {
		return 0, fmt.Errorf("session-events-retention %q: must not be negative", s)
	}
	return d, nil
}

// RunRetention deletes events older than keep, once at start and then every
// `every`, until ctx is done.
func RunRetention(ctx context.Context, store Store, keep, every time.Duration, now func() time.Time, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if now == nil {
		now = time.Now
	}
	sweep := func() {
		n, err := store.DeleteBefore(now().Add(-keep))
		if err != nil {
			log.Warn("session events retention sweep failed", "err", err.Error())
			return
		}
		if n > 0 {
			log.Info("session events retention sweep", "removed", n)
		}
	}
	sweep()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
```

- [ ] **Step 4: Config** — in `serveConfig` (after `IntercomLog`):

```go
	// SessionEventsDir is the file backend for managed-cove session events
	// (one JSONL per stream). Ignored when store-postgres is set (events then
	// go to Postgres). Neither → events are acked and dropped. See
	// docs/usage/jam/session-events.md.
	SessionEventsDir string `yaml:"session-events-dir"`
	// SessionEventsRetention bounds how long session events are kept: "<N>d"
	// or a Go duration; empty keeps forever.
	SessionEventsRetention string `yaml:"session-events-retention"`
```

```go
// validateSessionEvents checks session-events-retention parses.
func (c serveConfig) validateSessionEvents() error {
	_, err := sessionevents.ParseRetention(c.SessionEventsRetention)
	return err
}
```

In `main.go`, next to the existing `cfg.validateWake()` call, add the same pattern for `cfg.validateSessionEvents()` (print `at-jam: <err>` to stderr, return 1).

- [ ] **Step 5: Wiring in `main.go`** — immediately after the `if intercomLog != nil { ... }` notifier block (after `rsrv` exists, before any listener serves):

```go
	// Session events (docs/usage/jam/session-events.md): backend follows the
	// store backend like the message log; unset → a no-op store, so coves are
	// still acked and never back up.
	var sessStore sessionevents.Store = sessionevents.NopStore{}
	switch {
	case pgPool != nil:
		ss, err := sessionpg.New(context.Background(), pgPool, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: session events (postgres):", err)
			return 1
		}
		sessStore = ss
		log.Info("Jam session events: postgres (shared control-plane database)")
	case cfg.SessionEventsDir != "":
		fstore, err := sessionevents.OpenFileStore(cfg.SessionEventsDir)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		sessStore = fstore
		log.Info("Jam session events: file", "dir", cfg.SessionEventsDir)
	default:
		log.Info("Jam session events: not stored (no session-events-dir or store-postgres)")
	}
	sessHub := sessionevents.NewHub()
	rsrv.SetSessionEvents(sessionevents.NewIngest(sessStore, sessHub, nil))
	if keep, _ := sessionevents.ParseRetention(cfg.SessionEventsRetention); keep > 0 {
		go sessionevents.RunRetention(context.Background(), sessStore, keep, 24*time.Hour, nil, log)
	}
```

(Import `internal/jam/sessionevents` and `internal/jam/sessionevents/sessionpg`. `sessHub` is unused until Task 11 — if the compiler complains, add `_ = sessHub` with a `// used by the admin UI (Task 11)` comment and remove it in Task 11.)

- [ ] **Step 6: Run** — `go build ./... && go test ./internal/jam/sessionevents/ ./cmd/at-jam/ -race` → PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jam/sessionevents/retention*.go cmd/at-jam/
git commit -m "feat(at-jam): session-events-dir / retention config; wire ingest into Attach

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: NDJSON export at `/admin/sessions/{actor_id}/events`

**Files:**
- Create: `internal/jam/sessionevents/export.go`, `internal/jam/sessionevents/export_test.go`
- Modify: `internal/jam/admin.go`, `internal/jam/admin_test.go`, `cmd/at-jam/main.go`

**Interfaces:**
- Consumes: `sessionevents.Store` (Task 5), `sessStore` in `main.go` (Task 9).
- Produces: `sessionevents.ExportHandler(store Store) http.Handler` (expects the `{actor_id}` path value); `jam.AdminOption`, `jam.WithAdminRoute(pattern string, h http.Handler) jam.AdminOption`; `NewAdminHandler(..., ui, me http.Handler, opts ...AdminOption)` (variadic → existing callers compile unchanged).

- [ ] **Step 1: Write the failing tests**

`export_test.go`:

```go
package sessionevents_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

func exportServer(t *testing.T, st sessionevents.Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /admin/sessions/{actor_id}/events", sessionevents.ExportHandler(st))
	return mux
}

func TestExportNDJSONLatestStreamByDefault(t *testing.T) {
	st, _ := sessionevents.OpenFileStore(t.TempDir())
	older, newer := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	t0 := time.Unix(1000, 0)
	st.Append(sessionevents.Event{ActorID: "w1", StreamID: older, Seq: 1, Kind: "event", ReceivedAt: t0, Raw: []byte(`{"n":0}`)})
	for i := uint64(1); i <= 3; i++ {
		st.Append(sessionevents.Event{ActorID: "w1", StreamID: newer, Seq: i, Kind: "event", ReceivedAt: t0.Add(time.Hour), Raw: []byte(`{"n":1}`)})
	}
	rec := httptest.NewRecorder()
	exportServer(t, st).ServeHTTP(rec, httptest.NewRequest("GET", "/admin/sessions/w1/events?after_seq=1&limit=1", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	sc := bufio.NewScanner(rec.Body)
	var lines []map[string]any
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 1 || lines[0]["stream_id"] != newer || lines[0]["seq"].(float64) != 2 {
		t.Fatalf("lines %+v", lines)
	}
}

func TestExportErrors(t *testing.T) {
	st, _ := sessionevents.OpenFileStore(t.TempDir())
	h := exportServer(t, st)
	for path, code := range map[string]int{
		"/admin/sessions/nobody/events":             404,
		"/admin/sessions/w1/events?after_seq=x":     400,
		"/admin/sessions/w1/events?stream=../etc":   400,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != code {
			t.Errorf("%s: got %d want %d", path, rec.Code, code)
		}
	}
}
```

In `internal/jam/admin_test.go` (an extra route is mounted inside the operator guard):

```go
func TestWithAdminRouteIsGuarded(t *testing.T) {
	store, _ := NewFileStore(t.TempDir() + "/s.json")
	extra := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("extra")) })
	h := NewAdminHandler(store, nil, nil, denyAll{}, func(string) bool { return true }, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, WithAdminRoute("GET /admin/sessions/{actor_id}/events", extra))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/sessions/w1/events", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated request reached the route: %d", rec.Code)
	}
	h = NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, WithAdminRoute("GET /admin/sessions/{actor_id}/events", extra))
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/sessions/w1/events", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "extra" {
		t.Fatalf("route not mounted: %d %q", rec.Code, rec.Body.String())
	}
}

type denyAll struct{}

func (denyAll) Authenticate(*http.Request) (Operator, error) { return Operator{}, errors.New("no") }
```

(If `OperatorAuthenticator.Authenticate` has a different return type, match the interface in `internal/jam/operator.go`; if a deny-all fake already exists in the test file, reuse it.)

- [ ] **Step 2: Run** — `go test ./internal/jam/ ./internal/jam/sessionevents/ -run 'Export|WithAdminRoute'` → FAIL.

- [ ] **Step 3: Implement `export.go`**

```go
package sessionevents

import (
	"encoding/json"
	"net/http"
	"strconv"
)

const (
	defaultExportLimit = 1000
	maxExportLimit     = 10000
)

// ExportHandler serves one stream of an actor's session events as NDJSON (the
// Event wire form). Query: stream (default: the newest), after_seq, limit
// (default 1000, max 10000). Mount it behind the operator authenticator —
// events carry agent output.
func ExportHandler(store Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := r.PathValue("actor_id")
		q := r.URL.Query()
		var after uint64
		if v := q.Get("after_seq"); v != "" {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				http.Error(w, "after_seq must be an unsigned integer", http.StatusBadRequest)
				return
			}
			after = n
		}
		limit := defaultExportLimit
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
				return
			}
			limit = min(n, maxExportLimit)
		}
		stream := q.Get("stream")
		if stream != "" && !ValidStreamID(stream) {
			http.Error(w, "invalid stream id", http.StatusBadRequest)
			return
		}
		if stream == "" {
			streams, err := store.Streams(actor)
			if err != nil {
				http.Error(w, "listing streams failed", http.StatusInternalServerError)
				return
			}
			if len(streams) == 0 {
				http.Error(w, "no session events for this actor", http.StatusNotFound)
				return
			}
			stream = streams[0].StreamID
		}
		evs, err := store.List(Filter{ActorID: actor, StreamID: stream, AfterSeq: after, Limit: limit})
		if err != nil {
			http.Error(w, "listing events failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for _, e := range evs {
			if err := enc.Encode(e); err != nil {
				return
			}
		}
	})
}
```

- [ ] **Step 4: Implement `WithAdminRoute`** in `internal/jam/admin.go`:

```go
// AdminOption mounts extra routes on the /admin/* mux, inside the operator
// authenticator — for features whose packages jam must not import (e.g. the
// session-events export).
type AdminOption func(*http.ServeMux)

func WithAdminRoute(pattern string, h http.Handler) AdminOption {
	return func(m *http.ServeMux) { m.Handle(pattern, h) }
}
```

Change the signature to `func NewAdminHandler(store Store, sup *Supervisor, alloc SessionAllocator, auth OperatorAuthenticator, credExists func(string) bool, login *OperatorLoginConfig, log *slog.Logger, ui, me http.Handler, opts ...AdminOption) http.Handler` and, just before `guarded := authMiddleware(auth, log, mux)`:

```go
	for _, o := range opts {
		o(mux)
	}
```

- [ ] **Step 5: Wire** in `main.go` — the `jam.NewAdminHandler(...)` call gains a final argument:

```go
			jam.WithAdminRoute("GET /admin/sessions/{actor_id}/events", sessionevents.ExportHandler(sessStore)))
```

- [ ] **Step 6: Run** — `go build ./... && go test ./internal/jam/... ./cmd/at-jam/ -race` → PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jam/admin.go internal/jam/admin_test.go internal/jam/sessionevents/export*.go cmd/at-jam/main.go
git commit -m "feat(at-jam): NDJSON session-events export behind the operator authenticator

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Admin UI — live session timeline

**Files:**
- Create: `internal/jam/adminui/session.go`, `internal/jam/adminui/session_test.go`, `internal/jam/adminui/templates/session.html`
- Modify: `internal/jam/adminui/adminui.go`, `internal/jam/adminui/templates/coves.html`, `cmd/at-jam/main.go`

**Interfaces:**
- Consumes: `sessionevents.Store`, `*sessionevents.Hub`, `sessionevents.Event` (Tasks 4–5); `sessStore`, `sessHub` in `main.go` (Task 9).
- Produces: `adminui.WithSessions(store sessionevents.Store, hub *sessionevents.Hub) adminui.Option`; routes `GET /ui/coves/{id}/session` and `GET /ui/coves/{id}/session/events`.

SSE protocol (used by the page's inline script): `event: ev` (data = one rendered `<div class="ev">` fragment, `id: <stream>:<seq>`), `event: totals` (data = rendered `<div id="totals">`), `event: stream` (data = the stream id being followed, sent once). A `Last-Event-ID` of `<stream>:<seq>` resumes after that seq.

- [ ] **Step 1: Write the failing tests** — `session_test.go`:

```go
package adminui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const sid = "0123456789abcdef0123456789abcdef"

func sessionUI(t *testing.T) (http.Handler, *sessionevents.FileStore, *sessionevents.Hub, *sessionevents.Ingest) {
	t.Helper()
	st, err := sessionevents.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := sessionevents.NewHub()
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil, adminui.WithSessions(st, hub))
	return h, st, hub, sessionevents.NewIngest(st, hub, nil)
}

func in(seq uint64, raw string) sessionevents.Incoming {
	return sessionevents.Incoming{StreamID: sid, Seq: seq, Turn: 1, Raw: []byte(raw)}
}

// sse runs the SSE handler until stop() is called and returns the body.
func sse(t *testing.T, h http.Handler, path, lastID string) (stop func() string) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	return func() string { cancel(); <-done; return rec.Body.String() }
}

func TestSessionPageRenders(t *testing.T) {
	h, _, _, ing := sessionUI(t)
	ing.Append("w1", sessionevents.Stamp{}, in(1, `{"type":"system","subtype":"init"}`))
	rec := get(t, h, "/ui/coves/w1/session")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/ui/coves/w1/session/events?stream="+sid) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionPageNotConfigured(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
	if rec := get(t, h, "/ui/coves/w1/session"); !strings.Contains(rec.Body.String(), "not configured") {
		t.Fatalf("%s", rec.Body.String())
	}
}

func TestSessionSSEBackfillThenLive(t *testing.T) {
	h, _, _, ing := sessionUI(t)
	ing.Append("w1", sessionevents.Stamp{}, in(1, `{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`))
	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sid, "")
	time.Sleep(100 * time.Millisecond)
	ing.Append("w1", sessionevents.Stamp{}, in(2, `{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`))
	time.Sleep(100 * time.Millisecond)
	body := stop()
	if !strings.Contains(body, "id: "+sid+":1") || !strings.Contains(body, "id: "+sid+":2") || strings.Index(body, "first") > strings.Index(body, "second") {
		t.Fatalf("body:\n%s", body)
	}
	if !strings.Contains(body, "event: totals") {
		t.Fatal("no totals event")
	}
}

func TestSessionSSEResumesFromLastEventID(t *testing.T) {
	h, _, _, ing := sessionUI(t)
	for i := uint64(1); i <= 3; i++ {
		ing.Append("w1", sessionevents.Stamp{}, in(i, `{"type":"system","subtype":"init"}`))
	}
	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sid, sid+":2")
	time.Sleep(100 * time.Millisecond)
	body := stop()
	if strings.Contains(body, "id: "+sid+":1\n") || strings.Contains(body, "id: "+sid+":2\n") || !strings.Contains(body, "id: "+sid+":3\n") {
		t.Fatalf("resume sent wrong events:\n%s", body)
	}
}

func TestSessionSSESubscribeBeforeBackfill(t *testing.T) {
	// Events appended while the handler is starting must appear exactly once.
	h, _, _, ing := sessionUI(t)
	ing.Append("w1", sessionevents.Stamp{}, in(1, `{}`))
	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sid, "")
	for i := uint64(2); i <= 50; i++ {
		ing.Append("w1", sessionevents.Stamp{}, in(i, `{}`))
	}
	time.Sleep(200 * time.Millisecond)
	body := stop()
	for i := 1; i <= 50; i++ {
		id := "id: " + sid + ":" + itoa(i) + "\n"
		if n := strings.Count(body, id); n != 1 {
			t.Fatalf("seq %d delivered %d times", i, n)
		}
	}
}

func TestSessionRendersAgentOutputInert(t *testing.T) {
	h, _, _, ing := sessionUI(t)
	ing.Append("w1", sessionevents.Stamp{}, in(1, `{"type":"user","message":{"content":[{"type":"tool_result","content":"<script>alert(1)</script>","is_error":true}]}}`))
	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sid, "")
	time.Sleep(100 * time.Millisecond)
	body := stop()
	if strings.Contains(body, "<script>alert") {
		t.Fatalf("unescaped agent output:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("escaped output missing:\n%s", body)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
```

(Add `strconv` to the imports. `newStore`, `testLogger`, `anyCred`, `get` already exist in `adminui_test.go`.)

- [ ] **Step 2: Run** — `go test ./internal/jam/adminui/ -run Session` → FAIL (`undefined: adminui.WithSessions`).

- [ ] **Step 3: Option and routes** in `adminui.go`

Extend `options` with `sessStore sessionevents.Store; sessHub *sessionevents.Hub`, add:

```go
// WithSessions enables the live session-event timeline (/ui/coves/{id}/session).
func WithSessions(store sessionevents.Store, hub *sessionevents.Hub) Option {
	return func(o *options) { o.sessStore, o.sessHub = store, hub }
}
```

Register `"session": mustParse("session.html")` in `pages`. In `Handler`, after the `/ui/intercom` route:

```go
	registerSession(mux, o.sessStore, o.sessHub)
```

In `coves.html`, make the id a link: `<td><a href="/ui/coves/{{.ID}}/session">{{.ID}}</a></td>`.

- [ ] **Step 4: Implement `session.go`**

```go
package adminui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const (
	sessionBackfillPage = 500
	sessionSubBuffer    = 256
)

func registerSession(mux *http.ServeMux, store sessionevents.Store, hub *sessionevents.Hub) {
	mux.HandleFunc("GET /ui/coves/{id}/session", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Title": "Session", "ActorID": r.PathValue("id"), "Enabled": store != nil}
		if store != nil {
			streams, _ := store.Streams(r.PathValue("id"))
			selected := r.URL.Query().Get("stream")
			if !sessionevents.ValidStreamID(selected) && len(streams) > 0 {
				selected = streams[0].StreamID
			}
			data["Streams"], data["Stream"] = streams, selected
		}
		render(w, "session", data)
	})
	mux.HandleFunc("GET /ui/coves/{id}/session/events", func(w http.ResponseWriter, r *http.Request) {
		if store == nil || hub == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		serveSessionEvents(w, r, store, hub)
	})
}

type eventView struct {
	Seq            uint64
	Turn           uint32
	Label          string
	Summary        string
	Detail         string
	Raw            string
	IsError        bool
	Progress       bool // system/thinking_tokens — hidden unless toggled
	Gap            bool
	TruncatedBytes uint64
}

type totals struct {
	Turns                     uint32
	ToolCalls                 int
	InputTokens, OutputTokens int64
	CostUSD                   float64
}

func (t *totals) add(ev sessionevents.Event) {
	if ev.Turn > t.Turns {
		t.Turns = ev.Turn
	}
	if ev.Index.Type == "assistant" && ev.Index.ToolName != "" {
		t.ToolCalls++
	}
	if ev.Index.Type == "result" {
		t.InputTokens += ev.Index.InputTokens
		t.OutputTokens += ev.Index.OutputTokens
		t.CostUSD += ev.Index.CostUSD
	}
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func pretty(raw []byte) string {
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") == nil {
		return b.String()
	}
	return string(raw)
}

type viewBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// blockText renders a tool_result content field: a string, or an array of
// {type:text,text} blocks.
func blockText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []viewBlock
	if json.Unmarshal(c, &parts) == nil {
		var out []string
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return strings.Join(out, "\n")
	}
	return string(c)
}

func viewOf(ev sessionevents.Event) eventView {
	v := eventView{Seq: ev.Seq, Turn: ev.Turn, Raw: pretty(ev.Raw), TruncatedBytes: ev.TruncatedBytes, IsError: ev.Index.IsError}
	if ev.Kind == sessionevents.KindGap {
		v.Gap, v.Label = true, "gap"
		v.Summary = fmt.Sprintf("⚠ %d events lost (seq %d–%d)", ev.GapTo-ev.GapFrom+1, ev.GapFrom, ev.GapTo)
		return v
	}
	var env struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Result string `json:"result"`
	}
	if json.Unmarshal(ev.Raw, &env) != nil {
		v.Label, v.Detail = "unparsed", string(ev.Raw)
		return v
	}
	var blocks []viewBlock
	_ = json.Unmarshal(env.Message.Content, &blocks)
	switch env.Type {
	case "system":
		v.Label = "system/" + env.Subtype
		v.Progress = env.Subtype == "thinking_tokens"
	case "assistant":
		v.Label = "assistant"
		for _, b := range blocks {
			switch b.Type {
			case "text":
				v.Summary, v.Detail = clip(b.Text, 300), b.Text
			case "thinking":
				v.Label, v.Summary, v.Detail = "thinking", clip(b.Thinking, 160), b.Thinking
			case "tool_use":
				v.Label, v.Summary, v.Detail = "tool_use "+b.Name, clip(string(b.Input), 160), pretty(b.Input)
			}
		}
	case "user":
		v.Label = "user"
		for _, b := range blocks {
			if b.Type == "tool_result" {
				text := blockText(b.Content)
				v.Label, v.Detail = "tool_result", text
				v.Summary = fmt.Sprintf("%d bytes", len(text))
				v.IsError = v.IsError || b.IsError
			}
		}
	case "result":
		v.Label = "result"
		v.Summary = fmt.Sprintf("$%.4f · in %d / out %d tokens · %s", ev.Index.CostUSD, ev.Index.InputTokens,
			ev.Index.OutputTokens, time.Duration(ev.Index.DurationMS)*time.Millisecond)
		v.Detail = env.Result
	default:
		v.Label = env.Type
	}
	return v
}

// sseWrite emits one SSE message; multi-line data gets one data: line each.
func sseWrite(w http.ResponseWriter, event, id, data string) {
	var b strings.Builder
	if id != "" {
		fmt.Fprintf(&b, "id: %s\n", id)
	}
	fmt.Fprintf(&b, "event: %s\n", event)
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	_, _ = w.Write([]byte(b.String()))
}

func fragment(name string, data any) string {
	var b bytes.Buffer
	if err := pages["session"].ExecuteTemplate(&b, name, data); err != nil {
		return ""
	}
	return b.String()
}

// serveSessionEvents streams one cove's events: subscribe FIRST (so nothing
// published during backfill is missed), backfill from the store, then drain
// live events, skipping any seq already sent. Totals are computed over the
// whole stream even when resuming via Last-Event-ID.
func serveSessionEvents(w http.ResponseWriter, r *http.Request, store sessionevents.Store, hub *sessionevents.Hub) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	actor := r.PathValue("id")
	stream := r.URL.Query().Get("stream")
	var resumeAfter uint64
	if lid := r.Header.Get("Last-Event-ID"); lid != "" {
		if s, n, ok := strings.Cut(lid, ":"); ok && sessionevents.ValidStreamID(s) {
			if seq, err := strconv.ParseUint(n, 10, 64); err == nil {
				stream, resumeAfter = s, seq
			}
		}
	}
	if stream != "" && !sessionevents.ValidStreamID(stream) {
		http.Error(w, "invalid stream id", http.StatusBadRequest)
		return
	}
	sub := hub.Subscribe(actor, sessionSubBuffer)
	defer sub.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")

	var tot totals
	var last uint64 // highest seq handled (sent or counted)
	emit := func(ev sessionevents.Event) {
		tot.add(ev)
		last = ev.Seq
		if ev.Seq > resumeAfter {
			sseWrite(w, "ev", fmt.Sprintf("%s:%d", ev.StreamID, ev.Seq), fragment("session-event", viewOf(ev)))
		}
	}
	if stream != "" {
		sseWrite(w, "stream", "", stream)
		for {
			evs, err := store.List(sessionevents.Filter{ActorID: actor, StreamID: stream, AfterSeq: last, Limit: sessionBackfillPage})
			if err != nil {
				return
			}
			for _, ev := range evs {
				emit(ev)
			}
			if len(evs) < sessionBackfillPage {
				break
			}
		}
		sseWrite(w, "totals", "", fragment("session-totals", tot))
	}
	fl.Flush()

	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return // dropped as a slow subscriber; EventSource reconnects with Last-Event-ID
			}
			if stream == "" { // no stream yet: follow the first one that appears
				stream = ev.StreamID
				sseWrite(w, "stream", "", stream)
			}
			if ev.StreamID != stream || ev.Seq <= last {
				continue
			}
			emit(ev)
			sseWrite(w, "totals", "", fragment("session-totals", tot))
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		}
		fl.Flush()
	}
}
```

Note the backfill's first `List` uses `AfterSeq: 0` (not `resumeAfter`) so totals cover the whole stream; `emit` only *sends* events past `resumeAfter`.

- [ ] **Step 5: Template** — `templates/session.html`:

```html
{{define "content"}}
<h1>Session — {{.ActorID}}</h1>
{{if not .Enabled}}
<p class="empty">Session events are not configured on this Jam.</p>
{{else}}
<form method="get">
  <label>Stream
    <select name="stream" onchange="this.form.submit()">
      {{range .Streams}}<option value="{{.StreamID}}" {{if eq .StreamID $.Stream}}selected{{end}}>{{.FirstAt.Format "2006-01-02 15:04:05"}} · {{.Events}} events</option>{{end}}
    </select>
  </label>
  <label><input type="checkbox" id="show-progress"> show progress events</label>
</form>
<div id="totals"></div>
<div id="events" data-src="/ui/coves/{{.ActorID}}/session/events?stream={{.Stream}}"></div>
<style>
  .ev { border-bottom: 1px solid #eee; padding: 4px 0; }
  .ev .label { font-weight: 600; margin-right: 8px; }
  .ev.error .label { color: #b00020; }
  .ev.gap { color: #b06000; }
  .ev.progress { display: none; }
  body.show-progress .ev.progress { display: block; }
  .ev pre { white-space: pre-wrap; background: #f6f6f6; padding: 6px 8px; }
</style>
<script>
  document.getElementById("show-progress").addEventListener("change", e =>
    document.body.classList.toggle("show-progress", e.target.checked));
  // The URL lives in a data attribute: html/template would JS-escape "/" inside a script string.
  const es = new EventSource(document.getElementById("events").dataset.src);
  es.addEventListener("ev", e => document.getElementById("events").insertAdjacentHTML("beforeend", e.data));
  es.addEventListener("totals", e => { document.getElementById("totals").outerHTML = e.data; });
</script>
{{end}}
{{end}}

{{define "session-totals"}}<div id="totals">turns {{.Turns}} · tool calls {{.ToolCalls}} · tokens in {{.InputTokens}} / out {{.OutputTokens}} · cost ${{printf "%.4f" .CostUSD}}</div>{{end}}

{{define "session-event"}}<div class="ev{{if .IsError}} error{{end}}{{if .Progress}} progress{{end}}{{if .Gap}} gap{{end}}">
  <span class="turn">t{{.Turn}}</span> <span class="label">{{.Label}}</span>{{if .IsError}}<span class="badge">error</span>{{end}}
  <span class="summary">{{.Summary}}</span>
  {{if .TruncatedBytes}}<span class="trunc">✂ {{.TruncatedBytes}} bytes dropped</span>{{end}}
  {{if .Detail}}<details><summary>details</summary><pre>{{.Detail}}</pre></details>{{end}}
  {{if .Raw}}<details><summary>raw JSON</summary><pre>{{.Raw}}</pre></details>{{end}}
</div>{{end}}
```

The empty `#totals` is filled by the first SSE `totals` event. `sseWrite` splits multi-line fragments into one `data:` line each, which EventSource rejoins with newlines.

- [ ] **Step 6: Wire** in `main.go` — the `adminui.Handler(...)` call gains `adminui.WithSessions(sessStore, sessHub)` (and remove any `_ = sessHub` from Task 9).

- [ ] **Step 7: Run** — `go build ./... && go test ./internal/jam/adminui/ ./cmd/at-jam/ -race` → PASS (existing coves-page tests may assert the old `<td>{{.ID}}</td>` — update them to the link form).

- [ ] **Step 8: Manual check (optional, if a browser is available)** — `just dev-watch` / `at-jam serve` with `session-events-dir` set, open `/ui/coves`, click a studio, confirm the timeline streams and the progress toggle works. Otherwise state "UI not exercised in a browser" in the task report.

- [ ] **Step 9: Commit**

```bash
git add internal/jam/adminui/ cmd/at-jam/main.go
git commit -m "feat(adminui): live session-event timeline per studio over SSE

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Docs and final verification

**Files:**
- Create: `docs/usage/jam/session-events.md`
- Modify: `docs/usage/jam/INDEX.md`, `docs/usage/jam/coves.md`, `docs/usage/jam/serve.md`, `docs/usage/jam/ui.md`, `docs/usage/observability.md`

Use the **docs-author** skill; run the **docs-audit** skill at the end.

- [ ] **Step 1: Write the leaf** — `docs/usage/jam/session-events.md`, frontmatter in the house schema (copy the key set from `ui.md`: `summary`, `read_when`, `owns`, `prereqs`, `tier: leaf`, `updated: <today>`). It owns: what is captured (every stream-json stdout line of a managed cove's agent, incl. hook and `thinking_tokens` progress events); the cove side (turns, 1 MiB truncation + `truncated_bytes`, own-secret redaction with `«redacted»` and its prefix edge case, bounded buffer, replay, gap rows, 5 s Done flush); storage selection and config keys (`session-events-dir`, `session-events-retention`, Postgres via `store-postgres`, no-op otherwise); the raw/raw_text encoding and content-equal (not byte-exact) guarantee; retention; the operator-only sensitivity stance and why it is an exception to observability rule 3; the export API (`GET /admin/sessions/{actor_id}/events`, params, NDJSON fields); the UI entry point (link to `ui.md#session-timeline`). Stay within the leaf size budget in `/agent-data/reference/progressive-disclosure.md`.

- [ ] **Step 2: Link, don't copy**
  - `INDEX.md`: one row — `| [session-events.md](session-events.md) | You want to watch, audit, or export what a managed studio's agent did — the captured Claude Code event stream, its storage/retention config, redaction, and the export API. |`
  - `coves.md` § The Attach stream: one sentence noting the stream also carries session events up and acks down, linking `session-events.md`.
  - `serve.md` config table: rows for `session-events-dir` and `session-events-retention`, each one line linking `session-events.md`.
  - `ui.md`: a `## Session timeline` section (what the page shows, the progress toggle, past streams) linking `session-events.md` for storage/sensitivity; update its `owns`/`updated` frontmatter.
  - `observability.md` rule 3: append one sentence — session events are a separate, operator-only audit channel that carries agent output by design; see `jam/session-events.md`.

- [ ] **Step 3: Audit** — run the docs-audit skill's checker; fix every orphan, dangling link, oversize, or duplication finding.

- [ ] **Step 4: Full verification**

Run: `just lint && just test`
Expected: PASS. Also `go vet -tags integration ./...`. Run the Postgres suite if `JAM_TEST_POSTGRES_DSN` is available; report explicitly if it was not run.

- [ ] **Step 5: Commit**

```bash
git add docs/
git commit -m "docs(jam): session events — capture, storage, retention, export, UI

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
