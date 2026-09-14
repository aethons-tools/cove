# harbor msgport seam spine — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `internal/msgport` — a resident engine (one per Service) that bridges the message Log to an external Service: an egress loop delivers internal-authored outbound messages exactly-once; an ingress loop appends foreign events idempotently. Library only, wired to nothing.

**Architecture:** `msgport` imports ONLY `msglog` + stdlib. The `Engine` runs two independent-ticker loops over narrow interfaces (`Surface`/`Markers`/`Cursors`/`Directory`); all Service- and harbor-specific logic lives behind those interfaces (concrete impls at cmd, later slices). Exactly-once egress = a bounded per-Service `EgressMark{LastMsg, Pending}` (mark-after-deliver + `m.ID` as the Service dedup key). Idempotent ingress = a deterministic `in:<service>:<foreignID>` id + a `seen` set rebuilt from the Log at construction.

**Tech Stack:** Go stdlib + `internal/msglog` only.

## Global Constraints

- **`internal/msgport` imports ONLY `internal/msglog` + stdlib** (`context`, `time`, `log/slog`, `io`, `strings`). No `internal/harbor`, grpc, kit, dispatch, or any Service client. Verify: `go list -deps ./internal/msgport | grep -E 'aethons-tools/cove'` yields only `.../internal/msglog` and `.../internal/msgport`.
- **`msglog` is UNCHANGED** — no delivery-status/cursor/watch. All egress bookkeeping is `msgport`'s `EgressMark` (persisted via the `Markers` interface, in-memory in tests); ingress dedup rides on the Log itself.
- **ECHO GUARD:** egress delivers a message ONLY if `msglog.Classify(msg.From) == msglog.Internal`. An externally-authored (ingested) message is never re-egressed.
- **Exactly-once egress:** mark a target delivered AFTER `Deliver` succeeds; `m.ID` is passed to `Deliver` as the Service-side dedup key. A crash between deliver and mark re-invokes `Deliver` (the Service dedups). `Pending` is bounded (drains as the done-prefix advances).
- **Idempotent ingress:** deterministic id + the `seen` set (rebuilt from the Log at `New`) is the dedup authority; the cursor is only an efficiency bound and MUST over-report (never under-report).
- **Single-goroutine `seen`:** `seen` is populated at `New` (before `Run`) and thereafter mutated ONLY by the ingress loop (one goroutine). No other goroutine touches it → no mutex needed; document the invariant.
- **Wired to nothing** — no `internal/harbor`, no `cmd/at-harbor`, no config, no docs/usage this slice (like the `msglog` substrate; the spec + package godoc are the docs). The concrete Surface/Directory/persistence + cmd wiring are Slice 1.
- **TDD, DRY, YAGNI, frequent commits.** Every task ends green (`GOPROXY=off go build ./... && GOPROXY=off go test ./...`), gofmt-clean, `.at-cove/` untouched. Prefix go commands with `GOPROXY=off`.

## File Structure

- `internal/msgport/msgport.go` — package doc, interfaces (`Surface`/`Markers`/`Cursors`/`Directory`), value types (`Delivery`/`Event`/`EgressMark`/`Config`) (Task 1).
- `internal/msgport/engine.go` — `Engine`, `New`, `Run`, `loop` (Task 1).
- `internal/msgport/egress.go` — `egressTick` + `owned`/`done` helpers (stub in Task 1, implemented Task 2).
- `internal/msgport/ingress.go` — `ingressTick` (stub in Task 1, implemented Task 3).
- Tests: `internal/msgport/fakes_test.go` (Task 1, shared), `engine_test.go` (Task 1), `egress_test.go` (Task 2), `ingress_test.go` (Task 3).

---

### Task 1: Package contract + Engine skeleton + shared fakes

**Files:**
- Create: `internal/msgport/msgport.go`, `internal/msgport/engine.go`, `internal/msgport/egress.go` (stub), `internal/msgport/ingress.go` (stub)
- Test: `internal/msgport/fakes_test.go`, `internal/msgport/engine_test.go`

**Interfaces:**
- Produces: `Surface`, `Markers`, `Cursors`, `Directory` interfaces; `Delivery`, `Event`, `EgressMark`, `Config` types; `Engine`; `New(...) *Engine`; `(*Engine).Run(ctx)`; stub `egressTick`/`ingressTick`. Shared test fakes.

- [ ] **Step 1: Write failing tests**

`internal/msgport/fakes_test.go` (shared across Tasks 1–3):
```go
package msgport

import (
	"context"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

// fakeSurface records Deliver calls and serves scripted Poll results.
type fakeSurface struct {
	mu       sync.Mutex
	service  string
	delivers []deliverCall
	deliverErrFor map[string]bool // target.String() -> return an error on Deliver
	pollEvents    []msglog.Message // unused placeholder to keep imports; real events below
	events   []Event
	pollErr  error
	next     string
}
type deliverCall struct {
	MsgID   string
	Address string
	Target  string // resolved target of this delivery, for assertions (set by tests via Directory)
	Sender  string
}
func (f *fakeSurface) Service() string { return f.service }
func (f *fakeSurface) Deliver(ctx context.Context, d Delivery, m msglog.Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivers = append(f.delivers, deliverCall{MsgID: m.ID, Address: d.Address, Sender: d.SenderName})
	if f.deliverErrFor[d.Address] {
		return "", context.DeadlineExceeded
	}
	return "fid-" + m.ID, nil
}
func (f *fakeSurface) Poll(ctx context.Context, project, since string) ([]Event, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pollErr != nil {
		return nil, since, f.pollErr
	}
	return f.events, f.next, nil
}
func (f *fakeSurface) Close() error { return nil }
func (f *fakeSurface) deliverCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.delivers) }

// fakeMarkers / fakeCursors: in-memory persistence.
type fakeMarkers struct{ m map[string]EgressMark }
func (f *fakeMarkers) Egress(service string) EgressMark { return f.m[service] }
func (f *fakeMarkers) SetEgress(service string, mk EgressMark) error {
	if f.m == nil { f.m = map[string]EgressMark{} }
	f.m[service] = mk
	return nil
}
type fakeCursors struct{ c map[string]string }
func (f *fakeCursors) Ingress(service, project string) string { return f.c[service+"/"+project] }
func (f *fakeCursors) SetIngress(service, project, cursor string) error {
	if f.c == nil { f.c = map[string]string{} }
	f.c[service+"/"+project] = cursor
	return nil
}

// fakeDirectory: scripted mapping. projects, resolve (target->Delivery), route (Event->msg).
type fakeDirectory struct {
	projects []string
	// resolve: keyed by target.String(); absent => not owned/unreachable.
	resolve map[string]Delivery
	// route: keyed by Event.ForeignID => (from, to, replyTo); absent => unrouted.
	route map[string]routed
}
type routed struct {
	from    msglog.Target
	to      []msglog.Target
	replyTo string
}
func (f *fakeDirectory) Projects(service string) []string { return f.projects }
func (f *fakeDirectory) Resolve(service, project string, to, from msglog.Target) (Delivery, bool) {
	d, ok := f.resolve[to.String()]
	return d, ok
}
func (f *fakeDirectory) Route(service, project string, e Event) (msglog.Target, []msglog.Target, string, bool) {
	r, ok := f.route[e.ForeignID]
	return r.from, r.to, r.replyTo, ok
}

var _ = time.Second // keep the time import if unused elsewhere
```

`internal/msgport/engine_test.go`:
```go
package msgport

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

func openLog(t *testing.T) *msglog.Log {
	t.Helper()
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return lg
}

func TestNewRebuildsSeenForItsService(t *testing.T) {
	lg := openLog(t)
	// two inbound-linear ids, one inbound-discord id, one normal outbound id
	_, _ = lg.Append(msglog.Message{ID: "in:linear:c1", From: msglog.Target{Kind: "human", Ref: "a"}, To: []msglog.Target{{Kind: "actor", Ref: "x"}}, Body: "b"})
	_, _ = lg.Append(msglog.Message{ID: "in:linear:c2", From: msglog.Target{Kind: "human", Ref: "a"}, To: []msglog.Target{{Kind: "actor", Ref: "x"}}, Body: "b"})
	_, _ = lg.Append(msglog.Message{ID: "in:discord:c3", From: msglog.Target{Kind: "human", Ref: "a"}, To: []msglog.Target{{Kind: "actor", Ref: "x"}}, Body: "b"})
	_, _ = lg.Append(msglog.Message{From: msglog.Target{Kind: "actor", Ref: "x"}, To: []msglog.Target{{Kind: "human", Ref: "a"}}, Body: "b"})
	e := New(&fakeSurface{service: "linear"}, lg, &fakeMarkers{}, &fakeCursors{}, &fakeDirectory{}, Config{}, nil)
	if !e.seen["in:linear:c1"] || !e.seen["in:linear:c2"] {
		t.Fatal("New must seed seen with this Service's inbound ids")
	}
	if e.seen["in:discord:c3"] {
		t.Fatal("New must NOT seed another Service's inbound ids")
	}
	if len(e.seen) != 2 {
		t.Fatalf("seen has %d ids, want 2", len(e.seen))
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	e := New(&fakeSurface{service: "linear"}, openLog(t), &fakeMarkers{}, &fakeCursors{}, &fakeDirectory{}, Config{EgressPoll: time.Hour, IngressPoll: time.Hour}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `GOPROXY=off go test ./internal/msgport/ -v` → FAIL (package/symbols undefined).

- [ ] **Step 3: Implement `msgport.go`** (interfaces + types + doc)

Transcribe the contract from the spec (`docs/superpowers/specs/2026-09-14-harbor-msgport-seam.md` §1): the package doc comment; `Delivery`, `Event`, `Surface`, `EgressMark`, `Markers`, `Cursors`, `Directory`, `Config`. `Markers.Egress` returns a zero `EgressMark` when unset; `Cursors.Ingress` returns `""` when unset.

- [ ] **Step 4: Implement `engine.go`**

```go
package msgport

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

const (
	defaultEgressPoll  = 2 * time.Second
	defaultIngressPoll = 15 * time.Second
)

// Engine bridges the message Log to one external Service (surf). It runs an
// egress loop (deliver outbound) and an ingress loop (append inbound) on
// independent tickers. Single serve-process writer; `seen` is touched only by
// New and the ingress goroutine.
type Engine struct {
	surf Surface
	lg   *msglog.Log
	mk   Markers
	cur  Cursors
	dir  Directory
	cfg  Config
	log  *slog.Logger
	seen map[string]bool // inbound ids already in the Log for surf.Service()
}

func New(surf Surface, lg *msglog.Log, mk Markers, cur Cursors, dir Directory, cfg Config, log *slog.Logger) *Engine {
	if cfg.EgressPoll <= 0 {
		cfg.EgressPoll = defaultEgressPoll
	}
	if cfg.IngressPoll <= 0 {
		cfg.IngressPoll = defaultIngressPoll
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	e := &Engine{surf: surf, lg: lg, mk: mk, cur: cur, dir: dir, cfg: cfg, log: log, seen: map[string]bool{}}
	prefix := "in:" + surf.Service() + ":"
	for _, m := range lg.List(msglog.Filter{}) {
		if strings.HasPrefix(m.ID, prefix) {
			e.seen[m.ID] = true
		}
	}
	return e
}

// Run drives both loops until ctx is cancelled (egress in a goroutine, ingress inline).
func (e *Engine) Run(ctx context.Context) {
	go e.loop(ctx, e.cfg.EgressPoll, e.egressTick)
	e.loop(ctx, e.cfg.IngressPoll, e.ingressTick)
}

func (e *Engine) loop(ctx context.Context, interval time.Duration, tick func(context.Context)) {
	tick(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick(ctx)
		}
	}
}
```

- [ ] **Step 5: Stub `egress.go` and `ingress.go`**

```go
// egress.go
package msgport
import "context"
func (e *Engine) egressTick(ctx context.Context) {} // implemented in Task 2
```
```go
// ingress.go
package msgport
import "context"
func (e *Engine) ingressTick(ctx context.Context) {} // implemented in Task 3
```

- [ ] **Step 6: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/msgport/ -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass. Confirm boundary: `go list -deps ./internal/msgport | grep -E 'aethons-tools/cove'` → only `msglog` + `msgport`.

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -w internal/msgport/*.go
git add internal/msgport/
git commit -m "harbor: msgport engine skeleton + adapter contract (COV-172)" # + trailers
```

---

### Task 2: Egress loop — exactly-once outbound delivery

**Files:**
- Modify: `internal/msgport/egress.go` (replace the stub)
- Test: `internal/msgport/egress_test.go`

**Interfaces:**
- Consumes: `Surface.Deliver`, `Markers`, `Directory.Resolve`, `msglog.Classify`, the shared fakes (Task 1).

- [ ] **Step 1: Write failing tests**

`internal/msgport/egress_test.go`:
```go
package msgport

import (
	"context"
	"testing"

	"github.com/aethons-tools/cove/internal/msglog"
)

func actorMsg(to ...msglog.Target) msglog.Message {
	return msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, To: to, Body: "hi", Project: "acme"}
}

func newEgressEngine(t *testing.T, surf *fakeSurface, dir *fakeDirectory) (*Engine, *fakeMarkers) {
	mk := &fakeMarkers{}
	e := New(surf, openLog(t), mk, &fakeCursors{}, dir, Config{}, nil)
	return e, mk
}

func TestEgressDeliversExternalSkipsInternal(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{
		"human:alice": {Service: "linear", Address: "ACME-1", SenderName: "cove-1"},
	}}
	e, _ := newEgressEngine(t, surf, dir)
	// human:alice is External+owned → delivered; actor:cove-2 is Internal → skipped
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}, msglog.Target{Kind: "actor", Ref: "cove-2"}))
	e.egressTick(context.Background())
	if surf.deliverCount() != 1 || surf.delivers[0].Address != "ACME-1" {
		t.Fatalf("expected 1 delivery to ACME-1, got %+v", surf.delivers)
	}
}

func TestEgressEchoGuardSkipsExternalAuthored(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"channel:ops": {Service: "linear", Address: "ACME-9"}}}
	e, _ := newEgressEngine(t, surf, dir)
	// an ingested human->channel message must NOT be re-egressed
	_, _ = e.lg.Append(msglog.Message{From: msglog.Target{Kind: "human", Ref: "bob"}, To: []msglog.Target{{Kind: "channel", Ref: "ops"}}, Body: "x", Project: "acme"})
	e.egressTick(context.Background())
	if surf.deliverCount() != 0 {
		t.Fatalf("echo guard: externally-authored message must not egress, got %+v", surf.delivers)
	}
}

func TestEgressExactlyOnceAcrossTicks(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:alice": {Service: "linear", Address: "ACME-1"}}}
	e, _ := newEgressEngine(t, surf, dir)
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
	e.egressTick(context.Background())
	e.egressTick(context.Background())
	if surf.deliverCount() != 1 {
		t.Fatalf("exactly-once: 2 ticks delivered %d times", surf.deliverCount())
	}
}

func TestEgressCrashReplayRedelivers(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:alice": {Service: "linear", Address: "ACME-1"}}}
	e, mk := newEgressEngine(t, surf, dir)
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
	e.egressTick(context.Background()) // delivers + marks
	mk.m = map[string]EgressMark{}     // simulate a crash: the mark never persisted
	e.egressTick(context.Background()) // must re-deliver (Service dedups on m.ID)
	if surf.deliverCount() != 2 {
		t.Fatalf("crash replay: expected re-delivery, got %d", surf.deliverCount())
	}
	// and m.ID is passed to Deliver both times (the Service dedup key)
	if surf.delivers[0].MsgID == "" || surf.delivers[0].MsgID != surf.delivers[1].MsgID {
		t.Fatalf("Deliver must carry a stable m.ID as the dedup key: %+v", surf.delivers)
	}
}

func TestEgressPartialFailureRetriesOnlyFailed(t *testing.T) {
	surf := &fakeSurface{service: "linear", deliverErrFor: map[string]bool{"ACME-A": true}}
	dir := &fakeDirectory{resolve: map[string]Delivery{
		"human:a": {Service: "linear", Address: "ACME-A"},
		"human:b": {Service: "linear", Address: "ACME-B"},
	}}
	e, _ := newEgressEngine(t, surf, dir)
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "a"}, msglog.Target{Kind: "human", Ref: "b"}))
	e.egressTick(context.Background()) // A fails, B succeeds
	surf.deliverErrFor = nil           // A now recovers
	e.egressTick(context.Background()) // retries only A
	var a, b int
	for _, d := range surf.delivers {
		if d.Address == "ACME-A" { a++ }
		if d.Address == "ACME-B" { b++ }
	}
	if a != 2 || b != 1 {
		t.Fatalf("partial failure: A delivered %d (want 2, retried), B %d (want 1, once)", a, b)
	}
}

func TestEgressSkipsOtherService(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:alice": {Service: "discord", Address: "chan-1"}}}
	e, _ := newEgressEngine(t, surf, dir)
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
	e.egressTick(context.Background())
	if surf.deliverCount() != 0 {
		t.Fatal("a target resolving to another Service must not be delivered by this engine")
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/msgport/ -run Egress -v` → FAIL (stub delivers nothing).

- [ ] **Step 3: Implement `egress.go`**

```go
package msgport

import (
	"context"

	"github.com/aethons-tools/cove/internal/msglog"
)

// egressTick delivers every not-yet-delivered External target of each
// internal-authored message above the low-water, exactly once, and advances the
// bounded EgressMark.
func (e *Engine) egressTick(ctx context.Context) {
	service := e.surf.Service()
	mark := e.mk.Egress(service)
	if mark.Pending == nil {
		mark.Pending = map[string]map[string]bool{}
	}
	msgs := e.lg.List(msglog.Filter{})

	// Pass 1: deliver undelivered owned targets.
	seenLast := mark.LastMsg == ""
	for _, m := range msgs {
		if !seenLast {
			if m.ID == mark.LastMsg {
				seenLast = true
			}
			continue
		}
		if msglog.Classify(m.From) != msglog.Internal {
			continue // echo guard: never re-egress an externally-authored message
		}
		owned := e.owned(service, m.Project, m)
		if len(owned) == 0 {
			continue
		}
		got := mark.Pending[m.ID]
		if got == nil {
			got = map[string]bool{}
			mark.Pending[m.ID] = got
		}
		for _, t := range owned {
			key := t.String()
			if got[key] {
				continue
			}
			d, _ := e.dir.Resolve(service, m.Project, t, m.From)
			if _, err := e.surf.Deliver(ctx, d, m); err != nil {
				e.log.Warn("msgport: egress deliver failed", "service", service, "msg", m.ID, "target", key, "error", err.Error())
				continue // leave unmarked → retried next tick
			}
			got[key] = true // mark AFTER deliver
		}
	}

	// Pass 2: advance LastMsg across the contiguous fully-done prefix, GC'ing Pending.
	seenLast = mark.LastMsg == ""
	for _, m := range msgs {
		if !seenLast {
			if m.ID == mark.LastMsg {
				seenLast = true
			}
			continue
		}
		if !e.egressDone(service, m, mark.Pending) {
			break
		}
		mark.LastMsg = m.ID
		delete(mark.Pending, m.ID)
	}

	if err := e.mk.SetEgress(service, mark); err != nil {
		e.log.Warn("msgport: set egress mark failed", "service", service, "error", err.Error())
	}
}

// owned returns m's External targets that resolve to THIS Service.
func (e *Engine) owned(service, project string, m msglog.Message) []msglog.Target {
	var out []msglog.Target
	for _, t := range m.To {
		if msglog.Classify(t) != msglog.External {
			continue
		}
		d, ok := e.dir.Resolve(service, project, t, m.From)
		if !ok || d.Service != service {
			continue
		}
		out = append(out, t)
	}
	return out
}

// egressDone reports whether every owned target of m has been delivered.
func (e *Engine) egressDone(service string, m msglog.Message, pending map[string]map[string]bool) bool {
	if msglog.Classify(m.From) != msglog.Internal {
		return true // echo-guarded: nothing to deliver
	}
	got := pending[m.ID]
	for _, t := range e.owned(service, m.Project, m) {
		if !got[t.String()] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/msgport/ -run Egress -v` → PASS
Then: `GOPROXY=off go test ./internal/msgport/ && GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/msgport/egress.go internal/msgport/egress_test.go
git add internal/msgport/egress.go internal/msgport/egress_test.go
git commit -m "harbor: msgport egress loop — exactly-once outbound delivery (COV-172)" # + trailers
```

---

### Task 3: Ingress loop — idempotent inbound append

**Files:**
- Modify: `internal/msgport/ingress.go` (replace the stub)
- Test: `internal/msgport/ingress_test.go`

**Interfaces:**
- Consumes: `Surface.Poll`, `Cursors`, `Directory.Projects`/`Route`, `msglog.Log.Append`, the shared fakes (Task 1).

- [ ] **Step 1: Write failing tests**

`internal/msgport/ingress_test.go`:
```go
package msgport

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

func routeTo(coveID string) routed {
	return routed{from: msglog.Target{Kind: "human", Ref: "alice"}, to: []msglog.Target{{Kind: "actor", Ref: coveID}}}
}

func TestIngressAppendsRoutedEvent(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "c1", Body: "reply", At: time.Unix(10, 0)}}, next: "cur1"}
	dir := &fakeDirectory{projects: []string{"acme"}, route: map[string]routed{"c1": routeTo("cove-1")}}
	cur := &fakeCursors{}
	e := New(surf, openLog(t), &fakeMarkers{}, cur, dir, Config{}, nil)
	e.ingressTick(context.Background())
	inbox := e.lg.ReadInbox(msglog.Target{Kind: "actor", Ref: "cove-1"})
	if len(inbox) != 1 || inbox[0].ID != "in:linear:c1" || inbox[0].Body != "reply" || inbox[0].From.Ref != "alice" {
		t.Fatalf("expected 1 routed inbound message, got %+v", inbox)
	}
	if cur.c["linear/acme"] != "cur1" {
		t.Fatalf("cursor must advance to next: %v", cur.c)
	}
}

func TestIngressIdempotentAcrossRestart(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "c1", Body: "r", At: time.Unix(1, 0)}}}
	dir := &fakeDirectory{projects: []string{"acme"}, route: map[string]routed{"c1": routeTo("cove-1")}}
	lg := openLog(t)
	e := New(surf, lg, &fakeMarkers{}, &fakeCursors{}, dir, Config{}, nil)
	e.ingressTick(context.Background()) // appends in:linear:c1
	// simulate a restart: a fresh engine over the SAME log rebuilds `seen`; the surface replays c1
	e2 := New(surf, lg, &fakeMarkers{}, &fakeCursors{}, dir, Config{}, nil)
	e2.ingressTick(context.Background())
	if n := len(lg.List(msglog.Filter{})); n != 1 {
		t.Fatalf("idempotent ingress: replayed event must not double-append, got %d", n)
	}
}

func TestIngressUnroutedSkipped(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "c9", Body: "r"}}, next: "cur2"}
	dir := &fakeDirectory{projects: []string{"acme"}, route: map[string]routed{}} // no route for c9
	cur := &fakeCursors{}
	lg := openLog(t)
	e := New(surf, lg, &fakeMarkers{}, cur, dir, Config{}, nil)
	e.ingressTick(context.Background())
	if n := len(lg.List(msglog.Filter{})); n != 0 {
		t.Fatalf("unrouted event must not append, got %d", n)
	}
	if cur.c["linear/acme"] != "cur2" {
		t.Fatal("cursor still advances past an unrouted event")
	}
}

func TestIngressPollErrorSkipsProject(t *testing.T) {
	surf := &fakeSurface{service: "linear", pollErr: context.DeadlineExceeded}
	dir := &fakeDirectory{projects: []string{"acme"}}
	cur := &fakeCursors{}
	e := New(surf, openLog(t), &fakeMarkers{}, cur, dir, Config{}, nil)
	e.ingressTick(context.Background())
	if _, ok := cur.c["linear/acme"]; ok {
		t.Fatal("a Poll error must not advance the cursor")
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/msgport/ -run Ingress -v` → FAIL (stub appends nothing).

- [ ] **Step 3: Implement `ingress.go`**

```go
package msgport

import (
	"context"

	"github.com/aethons-tools/cove/internal/msglog"
)

// ingressTick polls each project on this Service and appends new, routable
// foreign events to the Log with a deterministic id, idempotently.
func (e *Engine) ingressTick(ctx context.Context) {
	service := e.surf.Service()
	for _, project := range e.dir.Projects(service) {
		evts, next, err := e.surf.Poll(ctx, project, e.cur.Ingress(service, project))
		if err != nil {
			e.log.Warn("msgport: ingress poll failed", "service", service, "project", project, "error", err.Error())
			continue // do NOT advance the cursor
		}
		for _, ev := range evts {
			id := "in:" + service + ":" + ev.ForeignID
			if e.seen[id] {
				continue
			}
			from, to, replyTo, ok := e.dir.Route(service, project, ev)
			if !ok {
				e.log.Warn("msgport: unrouted ingress event", "service", service, "foreign", ev.ForeignID)
				continue
			}
			if _, err := e.lg.Append(msglog.Message{ID: id, From: from, To: to, Body: ev.Body, At: ev.At, Project: project, ReplyTo: replyTo}); err != nil {
				e.log.Warn("msgport: ingress append failed", "service", service, "foreign", ev.ForeignID, "error", err.Error())
				continue
			}
			e.seen[id] = true
		}
		if err := e.cur.SetIngress(service, project, next); err != nil {
			e.log.Warn("msgport: set ingress cursor failed", "service", service, "project", project, "error", err.Error())
		}
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/msgport/ -run Ingress -v` → PASS
Then: `GOPROXY=off go test ./internal/msgport/ && GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass. Re-confirm the boundary grep from Task 1 Step 6.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/msgport/ingress.go internal/msgport/ingress_test.go
git add internal/msgport/ingress.go internal/msgport/ingress_test.go
git commit -m "harbor: msgport ingress loop — idempotent inbound append (COV-172)" # + trailers
```

---

## Self-Review

- **Spec coverage:** §1 contract → Task 1; §2 Engine/New/Run → Task 1, egressLoop → Task 2, ingressLoop → Task 3; Tests § → distributed. Boundaries (stdlib+msglog only, echo guard, exactly-once, idempotent, single-goroutine seen) → Global Constraints + tests.
- **Green between tasks:** Task 1 stubs `egressTick`/`ingressTick` so `Run`/`loop` compile and the skeleton tests pass; Tasks 2/3 replace the stubs. The shared fakes are defined once in Task 1 (`fakes_test.go`) so Tasks 2/3 reuse them.
- **Type consistency:** `Delivery`/`Event`/`EgressMark`/`Config` + the four interfaces defined once (Task 1); `owned`/`egressDone` (Task 2) and the ingress deterministic id (Task 3) consume them.
- **The two adjudications are pinned:** the echo guard by `TestEgressEchoGuardSkipsExternalAuthored`; the stdlib+msglog-only boundary by the `go list -deps` grep in Task 1/3.
- **Exactly-once + idempotency** proven by `TestEgressExactlyOnceAcrossTicks` / `TestEgressCrashReplayRedelivers` / `TestEgressPartialFailureRetriesOnlyFailed` and `TestIngressIdempotentAcrossRestart`.
- **Docs:** intentionally no `docs/usage` (no operator surface; wired to nothing) — spec + package godoc are the docs; flag to the final reviewer so it isn't read as a gap.
- **Placeholder scan:** none — all code is concrete. The `fakes_test.go` `var _ = time.Second` guard is only to keep the `time` import if a task trims its use; the implementer may drop it once `time` is used by a fake field (it is, via `Event.At` in Task 3 tests). Not a TBD.
