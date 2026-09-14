# harbor: comms message-log substrate — the unified Log (slice 1) (COV-171)

**Status:** design approved, pre-plan
**Issue:** COV-171 (comms message-log substrate — slice 1 of the unified-comms direction). Substrate only: the durable Log + envelope + classification + in-process API, **wired to nothing yet**.
**Foundation:** COV-161 (C1 — the `channel`/`human` target vocabulary this generalizes), COV-149 (Instance registry — the `actor` notion), COV-160/162 (wake-on — which a later slice subsumes onto the Log). **Design history:** this session's comms-model discussion; `docs/superpowers/specs/2026-09-10-harbor-design.md` §"Escalation, roster & the comms access-graph".

## Summary

Introduce the substrate the whole comms hub will converge on: **one durable, append-only message Log** as the source of truth, with a single message envelope (`{from, to[], body, …}`) for *all* comms. This slice is the pure spine — a focused `internal/msglog` package (append-only file + envelope types + internal/external classification + a hermetic in-process API) — **consumed by nothing**. Later slices retrofit C1/C2 onto it and add adapters (Linear, Discord DM).

**Model (from the design discussion — see COV-171 for the full framing):**
- The **actor model is the semantic frame, humans included**: a human is an *external actor*. So "SMS-like" human messaging is just an actor message whose recipient is *external* — not a separate system.
- **Internal vs external is a derived property of a target:** `actor` → internal (in-band); `human` → external; `channel` → per its Service. Internal targets never touch an adapter.
- **Two deliberate extensions** over the bare actor primitive: **channels** (shared multi-subscriber conduits) and the **durable Log** (re-readable, event-sourced).

## 1. Package + storage

New package **`internal/msglog`** — stdlib-only (`encoding/json`, `os`, `sync`, `time`, `crypto/rand`, `fmt`). It imports **nothing** from `internal/harbor`/grpc/kit; `internal/harbor` will consume it in a later slice. Hermetic (temp-dir) tests.

**Storage = an append-only JSONL file + an in-memory mirror** (the FileStore pattern: load all on open, mutate memory + append to disk). Chosen over stuffing messages into the FileStore JSON because that store rewrites its *entire* file on every `save()` — wrong for an ever-growing log.

```go
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File   // opened O_APPEND|O_CREATE|O_WRONLY
	msgs []Message  // in-memory mirror, chronological (append order)
}

// Open loads (or creates) the log at path: reads every line into msgs, then
// opens the file for append. A malformed trailing line (a torn last append) is
// tolerated — logged and skipped — so a crash mid-append can't wedge startup.
func Open(path string) (*Log, error)

// Close flushes and closes the append handle.
func (l *Log) Close() error
```

- **Append is O(1):** assign id/timestamp, append to `msgs`, write one `json.Marshal(msg)+"\n"` line under `mu`. **Reads scan the in-memory `msgs`** under `mu` (v1 volume is low; a by-recipient / by-thread index and file rotation are explicitly deferred). No `fsync` in v1 (matches FileStore; durability-hardening deferred).
- Concurrency: `mu` guards both `msgs` and the file write, so concurrent `Append`s never interleave a partial line and readers always see a consistent snapshot (they copy out under the lock).

## 2. The envelope

```go
// Target addresses a participant or conduit. Kind ∈ {"actor","human","channel"}:
//   actor:<coveID>   — an internal harbor cove (delivered in-band)
//   human:<name>     — an external human (rendered onto a surface by an adapter)
//   channel:<name>   — a shared conduit (a ticket thread, a chat channel)
type Target struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Message is one immutable Log entry.
type Message struct {
	ID      string    `json:"id"`
	From    Target    `json:"from"`
	To      []Target  `json:"to"`
	Body    string    `json:"body"`
	At      time.Time `json:"at"`
	Project string    `json:"project,omitempty"`
	ReplyTo string    `json:"reply_to,omitempty"` // ID of the message this replies to (optional; threading)
}
```

- `From` is a `Target` too (symmetric — a cove, or an ingested human reply).
- `To` is a **set** (multi-recipient) — an escalation ping to several humans is one message.
- **IDs are time-sortable + unique:** `newID(at) = fmt.Sprintf("%020d-%s", at.UnixNano(), randHex(8))` (crypto/rand); lexically sortable by time, so append order == ID order.
- **`ReplyTo` is included now** (cheap, and what reply-ingestion + thread-reads will need). **Per-target delivery status is deferred** (adapters own delivery; this slice has none).
- `Target` string form for logs/keys: `String()` returns `Kind+":"+Ref` (e.g. `"actor:cove-1"`), matching C1's `human:`/`channel:` convention.

## 3. Classification (`Reach`)

```go
type Reach int
const (
	Internal Reach = iota // delivered in-band; never touches an adapter
	External              // rendered onto a surface by an adapter
)

// Classify is the structural rule, correct for every target kind that exists
// today: actor → Internal, human → External, channel → External (every current
// channel is a Linear ticket, an external surface). Per-channel internal/external
// resolution — for a future agent-only channel — is deferred to when such a
// channel type exists, and will take a directory lookup then.
func Classify(t Target) Reach
```

- Kept **pure/structural** (no roster dependency) so `msglog` stays self-contained and hermetically testable. It is 100% correct for the current world (no internal channels exist yet); the doc + a code comment record the one future refinement.

## 4. The in-process API

```go
func (l *Log) Append(m Message) (Message, error) // assigns ID (if empty) + At (if zero); returns the stored message
func (l *Log) ReadInbox(t Target) []Message      // messages where t ∈ To (a recipient's "delivery" = its readable inbox)
func (l *Log) ReadThread(rootID string) []Message // the root message (ID==rootID) + its direct replies (ReplyTo==rootID), chronological
func (l *Log) List(f Filter) []Message           // by Project and/or [Since,Until]; empty Filter = all
type Filter struct {
	Project string
	Since   time.Time // zero = unbounded
	Until   time.Time // zero = unbounded
}
```

- **"In-band delivery for an internal recipient" is exactly `ReadInbox`** — the message sits in the Log and the addressed cove can read it. No push/wake in this slice (wake-on-via-log is a later slice).
- `ReadInbox` matches a target by value (`Kind`+`Ref`) against each message's `To`.
- All readers return **copies** (a fresh slice; `To` slices copied) so a caller can't mutate the in-memory mirror.
- Validation on `Append`: empty `Body` and an empty `To` are rejected (`fmt.Errorf`); a `Target` with an empty `Kind`/`Ref`, or a `Kind` ∉ {actor,human,channel}, is rejected.

## 5. Wired to nothing

`internal/harbor`, `/messages`, wake-on, escalation, and the CLI are **untouched** in this slice. The Log is a standalone, tested package; the first consumer (the C1 retrofit / the Linear adapter) is the next slice. No config, no wiring, no admin surface here.

## Tests (hermetic, temp dir)

- **round-trip:** `Append` then `ReadInbox`/`List` returns the message; `Append` assigns a non-empty ID and stamps `At` when zero, and preserves a caller-supplied ID/At.
- **persistence:** `Open` → `Append` × N → `Close`; re-`Open` the same path → all N present, in order.
- **multi-recipient inbox:** a message with `To: [actor:a, human:b]` appears in `ReadInbox(actor:a)` AND `ReadInbox(human:b)`, not in `ReadInbox(actor:c)`.
- **thread:** `ReadThread(root)` returns the root + only its direct replies, chronological.
- **filter:** `List` by project and by time window.
- **classify:** `actor`→Internal, `human`→External, `channel`→External.
- **validation:** empty body / empty To / bad Target kind → error, and nothing is appended (memory + file unchanged).
- **torn-line tolerance:** a file whose last line is truncated JSON loads the prior valid messages (skips + logs the bad tail), and the next `Append` still produces valid lines.
- **concurrency:** N goroutines `Append` concurrently → the file has N well-formed JSON lines and `msgs` has N entries (guarded by `mu`; reason about the lock — `-race` is unbuildable in-sandbox without cgo).

## Deferred (later slices)

- **Consumers:** retrofit C1 `send`/`read` (and escalation pings) to append to the Log; **wake-on-via-log** (an inbound message addressed to a Waiting cove wakes it) subsuming COV-160/162's ticket-comment polling.
- **Adapters:** the **Linear adapter** (egress ticket-channel messages via `PostComment`, ingress comments back as messages) and the **Discord DM adapter** (inbox-channel webhook egress with per-sender name/avatar + reply-to ingestion).
- **Participant delivery-profile:** per-Service handle + inbox channel on external actors (only an adapter consumes it).
- **Per-target delivery status / receipts;** an internal agent-only **channel** type + its Reach resolution; a **by-recipient/by-thread index** + **file rotation/compaction** + **fsync** durability; deep (multi-level) threads.

## Boundaries (hard constraints)

- `internal/msglog` imports **only the stdlib** — no `internal/harbor`, grpc, kit, dispatch. `internal/harbor` consumes it later, never the reverse.
- The Log is single-node (the serve process is the sole writer), consistent with the FileStore's MVP model; multi-writer/rotation is deferred.
- No secrets or tokens are ever part of a message body path that this package logs; the package logs only structural errors (a torn line), never message bodies.
