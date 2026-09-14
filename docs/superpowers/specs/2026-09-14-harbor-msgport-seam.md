# harbor: msgport seam spine — the adapter engine over the Log (slice 0) (COV-172)

**Status:** design approved (ultracode judge-panel + synthesis + adjudication), pre-plan
**Issue:** COV-172 (msgport seam spine — slice 0 of the msgport comms-adapter arc). Library only, wired to nothing.
**Foundation:** COV-171 (`internal/msglog` — the durable append-only Log this consumes). **Design history:** the ultracode seam-design workflow (3 architectures → perspective-diverse judges → opus synthesis); winner "msgport — one resident egress+ingress adapter-engine per Service," grafting the async-outbox's zero-extra-store ingress idempotency + decoupled loops.

## Summary

Build **`internal/msgport`**: a resident engine (New/Run/tick, the wakeon/escalate house style) that turns the message Log into a two-way bridge to an external **Service**, one `Engine` per Service. Its **egress** loop tails the Log and delivers outbound messages onto a surface (exactly-once); its **ingress** loop polls the Service and appends foreign events (human replies) back into the Log (idempotent). This slice is the **pure engine + its contract**, proven hermetically against fakes and **consumed by nothing** — the concrete Linear/Discord adapters, the harbor-backed Directory + delivery-profile, and the cmd wiring are the next slices.

**Two adjudications over the raw synthesis (cleaner boundary + a caught gap):**
- **`msgport` imports only `msglog` + stdlib** (not `internal/harbor`). All harbor coupling (Roster/Instance/delivery-profile → a concrete `Delivery`) hides behind the `Directory` *interface*, whose concrete impl lands at cmd in the Linear track (Slice 1). So the spine is a wired-to-nothing library, exactly like `msglog`. `EgressMark`/cursor are `msgport` value types used in its own `Markers`/`Cursors` interfaces; their durable persistence is a Slice-1 concern (a cmd-layer store), so the spine touches neither `harbor.Store` nor the roster.
- **Echo prevention:** egress delivers a message ONLY if it was authored by an **internal** actor (`msglog.Classify(msg.From) == Internal`). Otherwise an ingested `human → channel` message would be re-egressed to the very surface it came from — an infinite loop. (The synthesis filtered on external *recipients* but never excluded external *senders*; this closes it.)

## 1. Package + contract (`internal/msgport`, imports `msglog` + stdlib only)

```go
package msgport

// Delivery is one External-bound message already resolved to a concrete Service surface.
type Delivery struct {
	Service      string // "linear" | "discord" — the owning Service
	Address      string // the surface to post onto (a Linear ticket identifier, a Discord channel id)
	SenderName   string // the From actor's display identity (Discord webhook username override; a "<name>:" prefix on Linear)
	SenderAvatar string
}

// Event is one Service-native foreign artifact (a human reply), pre-routing.
type Event struct {
	ForeignID      string    // stable Service id (Linear comment id / Discord snowflake) — the dedup anchor
	Surface        string    // Service-native surface it occurred on (ticket/channel id)
	Author         string    // Service-native author handle
	Body           string
	ReplyToForeign string    // Service id of the artifact replied to ("" = top-level)
	At             time.Time
}

// Surface is the egress+ingress+lifecycle contract for ONE Service; the concrete
// client (over *linear.Client, the Discord webhook client) is wired at cmd.
type Surface interface {
	Service() string // selects which External Log targets this engine owns; keys markers/cursors
	// EGRESS: render+deliver one already-resolved message; return the Service-native id (receipt).
	// m.ID is passed as the Service-side dedup key (Discord nonce / Linear body footer).
	Deliver(ctx context.Context, d Delivery, m msglog.Message) (foreignID string, err error)
	// INGRESS: foreign events strictly after `since`, plus the opaque watermark to persist next.
	Poll(ctx context.Context, project, since string) (events []Event, next string, err error)
	Close() error
}

// EgressMark is the BOUNDED per-Service delivery bookkeeping (not stored in msglog — it stays pure).
type EgressMark struct {
	LastMsg string                     // low-water: every Log id <= this is fully delivered for this Service
	Pending map[string]map[string]bool // msgID → set of delivered target.String() (the draining in-flight window)
}

// Markers persists EgressMark per Service. In-memory in tests; a cmd-layer store in Slice 1.
type Markers interface {
	Egress(service string) EgressMark
	SetEgress(service string, m EgressMark) error
}

// Cursors persists the opaque per-(service,project) ingress watermark (an efficiency bound on Poll).
type Cursors interface {
	Ingress(service, project string) string
	SetIngress(service, project, cursor string) error
}

// Directory does all actor↔Service mapping (the concrete impl, over harbor.Roster/Instance, is at cmd).
type Directory interface {
	// Projects this Service should egress/ingress for.
	Projects(service string) []string
	// Resolve maps an External Log target (+ the sender) to a concrete Delivery ON THIS SERVICE;
	// ok=false when this Service doesn't own/can't reach the target (another Service handles it).
	Resolve(service, project string, to, from msglog.Target) (Delivery, bool)
	// Route maps a polled foreign Event to an inbound Log message's From/To/ReplyTo;
	// ok=false when unroutable (logged as an unrouted event, never silently appended).
	Route(service, project string, e Event) (from msglog.Target, to []msglog.Target, replyTo string, ok bool)
}

type Config struct{ EgressPoll, IngressPoll time.Duration } // defaults e.g. 2s / 15s
```

## 2. The Engine

```go
type Engine struct {
	surf Surface
	lg   *msglog.Log
	mk   Markers
	cur  Cursors
	dir  Directory
	cfg  Config
	now  func() time.Time
	log  *slog.Logger
	seen map[string]bool // inbound ids already in the Log for this Service (dedup authority)
}

func New(surf Surface, lg *msglog.Log, mk Markers, cur Cursors, dir Directory, cfg Config, log *slog.Logger) *Engine
func (e *Engine) Run(ctx context.Context) // spawns egressLoop + ingressLoop on INDEPENDENT tickers
```

- `New` rebuilds `seen` from the Log once: scan `lg.List(msglog.Filter{})` and record every id with prefix `"in:" + surf.Service() + ":"`. Defaults applied for zero Config/logger (nil → discard).
- `Run` launches two goroutines — `egressLoop` (ticker `EgressPoll`) and `ingressLoop` (ticker `IngressPoll`) — so a wedged Poll never stalls delivery. Both honor `ctx.Done()`. Each loop runs one `tick` immediately then on its ticker (mirrors wakeon's `Run`).

### egressLoop tick — exactly-once outbound
```
service := surf.Service()
mark := mk.Egress(service)                 // {LastMsg, Pending}
for each m in lg.List(Filter{}) with m.ID > mark.LastMsg:   // append order == id order for outbound
    if Classify(m.From) != Internal: continue              // ECHO GUARD: only deliver internal-authored msgs
    owned := [t in m.To where Classify(t)==External and dir.Resolve(service,m.Project,t,m.From).ok and Delivery.Service==service]
    allDone := true
    for t in owned:
        key := t.String()
        if mark.Pending[m.ID][key]: continue               // already delivered this target
        d,_ := dir.Resolve(...)                             // resolve to a Delivery on this Service
        if _, err := surf.Deliver(ctx, d, m); err != nil:
            log.Warn(...); allDone = false; continue        // leave unmarked → retried next tick (do NOT advance)
        set mark.Pending[m.ID][key] = true                 // mark AFTER deliver
    if len(owned)==0 or every owned target now in Pending[m.ID]: m is "done for this service"; drop m.ID from Pending
    else: allDone = false
// advance LastMsg across the contiguous prefix of done messages; SetEgress(service, mark)
```
- **Bounded:** `Pending` only holds the undelivered in-flight window; it drains as targets deliver, so no unbounded marker growth (the decisive win over an ever-growing per-(msgID,surface) file).
- **Exactly-once across a crash:** mark-after-deliver + `m.ID` passed to `Deliver` as the Service dedup key → a restart between `Deliver` and `SetEgress` re-invokes `Deliver`, which the Service dedups on `m.ID`. No separate startup reconcile pass — the first tick after `New` re-drives everything above `LastMsg`.
- **`LastMsg` advances only across a contiguous all-done prefix**, while `Pending` lets later messages deliver out of order (no strict head-of-line block across projects). `Pending` is the correctness authority; `LastMsg` is a scan optimization.

### ingressLoop tick — idempotent inbound
```
service := surf.Service()
for each project in dir.Projects(service):
    evts, next := surf.Poll(ctx, project, cur.Ingress(service,project))
    for e in evts:
        id := "in:" + service + ":" + e.ForeignID           // deterministic → the Log is the dedup authority
        if e.seen(id): continue
        from,to,replyTo,ok := dir.Route(service,project,e)
        if !ok: log.Warn("msgport: unrouted event", "service",service,"foreign",e.ForeignID); continue
        lg.Append(msglog.Message{ID:id, From:from, To:to, Body:e.Body, At:e.At, Project:project, ReplyTo:replyTo})
        seen[id] = true
    cur.SetIngress(service, project, next)                   // AFTER the batch; may over-report, never under-report
```
- **Idempotent across restarts with zero extra durable store:** the deterministic id + `msglog.Append`'s caller-ID-preservation (it only auto-assigns when `ID==""`) means a re-polled event yields the same id; the `seen` set (rebuilt at `New`) rejects it. The cursor is a pure efficiency bound on `Poll` and MUST over-report (replay a window on crash) rather than under-report (which would drop events).
- `Route` does all actor-model mapping; the `Surface` never sees actor semantics.

## 3. Boundaries

- **`internal/msgport` imports only `msglog` + stdlib** (`context`, `time`, `log/slog`, `strings`). No `internal/harbor`, no grpc/kit/dispatch, no linear/discord client. Verify: `go list -deps ./internal/msgport | grep -E 'aethons-tools/cove'` yields only `msglog` + `msgport`.
- The concrete `Surface` (linear/discord), the concrete `Directory` (over `harbor.Roster`/`Instance` + delivery-profile), and the file-backed `Markers`/`Cursors` all live at **cmd/at-harbor** in later slices — same layer as today's `linearCommenter`.
- `msglog` is UNCHANGED (no delivery-status/cursor/watch added). All delivery bookkeeping is `msgport`'s (`EgressMark`) or the cursor, persisted outside `msglog`.
- Single-writer assumption (the serve process is the sole writer) carries from `msglog`; a second process would double-deliver/ingest — out of scope (single-node MVP).

## Tests (hermetic, temp dir + fakes; no network)

Drive a `fakeSurface` (records `Deliver` calls, returns scripted `Poll` events + a settable error), an in-memory `fakeMarkers`/`fakeCursors`, a `fakeDirectory`, and a temp-dir `*msglog.Log`. Injected clock (settable `e.now`); call `egressTick`/`ingressTick` directly (mirror wakeon's tick-testing).

- **egress delivers external targets of internal-authored messages, skips internal targets** (`actor:` → never Delivered).
- **echo guard:** a message with `From: human:*` is NEVER egressed even if it has an External `To`.
- **exactly-once:** two egress ticks over the same message → `Deliver` called once (Pending suppresses the second).
- **crash replay:** deliver, then DROP the mark (simulate a crash before SetEgress) → next tick re-Delivers (relies on the Service dedup key; assert `m.ID` was passed to `Deliver`).
- **multi-target / partial failure:** a message to `[human:a, channel:b]` where `Deliver` fails for `a` → `b` marked, `a` unmarked, LastMsg NOT advanced; next tick retries only `a`.
- **not-this-service:** a target `dir.Resolve` maps to a different Service → skipped, not delivered.
- **ingress idempotency:** the same `Event.ForeignID` polled twice (across a simulated restart that rebuilds `seen` from the Log) → `Append`ed once (deterministic id + seen-set).
- **unrouted event:** `Route` returns ok=false → logged, not appended, cursor still advances.
- **cursor:** `SetIngress(next)` called after the batch; a Poll that over-reports (replays) doesn't double-append.
- **independent loops:** an ingress `Poll` that blocks/errs doesn't prevent an egress tick from delivering (reason about the goroutine split; a focused test can assert egress proceeds while `Poll` returns an error).

## Deferred (later slices — the tracks fan out from here)

- **Slice 1 (dual-write shadow):** the concrete `linearSurface` (Deliver=PostComment reproduction, Poll=Comments) + the concrete `Directory` over `harbor.Roster`/`Instance` + the roster **delivery-profile** (`Human.Delivery []DeliveryProfile{Service,Handle,Inbox}`) + file-backed `Markers`/`Cursors` at cmd + open `msglog.Log` in `cmdServe` + `handlePost` dual-writes (Append **and** PostComment). Nothing reads the Log yet.
- **Slice 2:** flip wake-on onto the Log (`Inbox{ReadInbox}`; reply-detection keys off a new inbound id **not by lexical sort** — the deterministic `in:` ids aren't time-sortable — but by a seen-set / append-order / `At`; nail this here). **Slice 3:** egress cutover (golden-output parity + Append-only). **Slice 4:** GET + escalation onto the Log. **Slice 5:** Discord (a second Engine + webhook per-sender identity + reply-to ingress) — no seam change.
- Delivery status/receipts surfaced to operators; multi-writer leasing; by-recipient index; deep threads.

## Boundaries recap (hard constraints)

- `internal/msgport`: `msglog` + stdlib only; wired to nothing this slice. `msglog` unchanged. No secret value ever enters a `Delivery`, `Event`, the Log, a marker/cursor, or a log line. Hermetic tests only (no network, no live client).
