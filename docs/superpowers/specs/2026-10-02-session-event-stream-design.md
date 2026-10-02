# jam: capture managed-cove session event streams

**Status:** design approved section-by-section (2026-10-02); amended during planning (sessionpg package, raw/raw_text, ack tick, stream_id validation, thinking_tokens hidden, /admin export path)
**Scope:** Jam-managed coves only (cove-master / `agentrun` — ticket, personal, standing sessions). The agent's Claude Code session events (`claude -p --output-format stream-json --verbose`) flow up the existing Attach stream to Jam, are persisted losslessly, and are watchable live in the admin UI.
**Goals, in order:** (1) live watching, (2) a durable audit trail that outlives the cove, (3) automated feedback (stuck/loop detection, budgets, escalation). This spec delivers (1) and (2) and builds the seam for (3); feedback rules are a later spec.
**Builds on:** the Attach gRPC stream (`internal/jam/attach`, `internal/covemaster`), the `agentrun` turn loop, the file-or-Postgres store split, the `/me/events` SSE pattern, the admin UI's operator gate.
**Does not change:** dispatched `at-cove work` workers, interactive `at-cove chat`, the switchboard — out of scope permanently; `/me` (the forthcoming Studio view may later take cues from the hub, nothing this round); worker-result.json semantics; the activity/heartbeat protocol.

## Decisions

- **Source:** stream-json stdout of `claude -p`. Not hooks, not transcript tailing (neither is live + complete for `-p` coves; transcripts die with `--rm` containers).
- **Transport:** the existing Attach stream — already authenticated (identity token + launch secret), ordered, reconnecting. No new endpoint, no new egress.
- **Store raw, derive later.** The audit record is the exact stream-json line. Jam derives a thin index at ingest for the UI and for feedback; index parse failures never block storage.
- **A deliberate exception to "raw agent output stays VM-local"** (`docs/usage/observability.md` rule 3, which governs the structured log sink): session events are a separate, operator-only audit channel. `observability.md` gains a note pointing at `session-events.md`.
- **Sensitive by default.** Events carry tool inputs/outputs (file contents, command output, possibly secrets the agent read). Viewing is operator-only; retention is configurable.

## 1. Wire (attach.proto, package stays `harbor.attach.v1`)

```proto
message StatusUp {
  oneof msg {
    Activity     status    = 1;
    Heartbeat    heartbeat = 2;
    SessionEvent event     = 3;
  }
}
message SessionEvent {
  string stream_id        = 1; // random per cove-master process: 32 lowercase hex chars
  uint64 seq              = 2; // monotonic per stream_id, from 1
  uint32 turn             = 3; // claude invocation number, from 1
  int64  observed_unix_ms = 4; // when cove-master read the line
  bytes  raw              = 5; // the stream-json line (no trailing newline)
  uint64 truncated_bytes  = 6; // bytes dropped from raw; 0 = intact
}

message ControlDown {
  oneof msg {
    // ... existing 1–4 ...
    EventAck ack = 5;
  }
}
message EventAck {
  string stream_id = 1;
  uint64 seq       = 2; // cumulative: every event of stream_id with seq <= this is durable
}
```

Jam rejects (drops, logs) any event whose `stream_id` does not match `^[0-9a-f]{32}$` — the file store uses it as a path segment.

Compatibility: an old Jam ignores `event` (its recv switch has no case); an old cove-master never sends one and logs-and-ignores `ack`. Any mix is safe. Original line size = `len(raw) + truncated_bytes`.

## 2. Cove side

### agentrun

- Every turn runs `claude -p [--continue] --output-format stream-json --verbose --dangerously-skip-permissions --mcp-config … --strict-mcp-config <prompt>`.
- `Spawner.Spawn` gains a stdout `io.Writer`. agentrun tees claude's stdout to (a) cove-master's stdout (→ `/agent-data/cove-master.log`, full fidelity, unchanged location) and (b) a line splitter that calls `Handle.Event(turn, line)`.
- `turn` starts at 1 and increments per claude invocation (each `--continue` after a Wake).
- stderr is not an event; it stays log-only.
- `Handle` gains `Event(turn uint32, raw []byte)`. It must never block — claude must not stall on Jam.
- Lines over **1 MiB** are cut to 1 MiB; `truncated_bytes` records the remainder. Keeps every frame under gRPC's 4 MiB default.

### covemaster client

- Generates `stream_id` at `New`.
- **Redaction:** before buffering, `Event` replaces every exact occurrence of the client's own `Token` and `LaunchSecret` with `«redacted»` (the `logging.Scrub` marker; contains no `"` or `\`, so valid JSON stays valid). These are the only secrets a Jam cove holds — Anthropic/git credentials are broker-injected. Edge: a secret straddling the 1 MiB truncation cut may leave a prefix. **Follow-up ticket (deferred by the owner):** guarantee the token and launch secret carry enough entropy/length that exact-match redaction can never hit ordinary text (a short secret would both over-redact and, by its redaction pattern, reveal itself).
- `Event` assigns `seq` and appends to an **in-memory bounded buffer** (default 10 000 events or 64 MiB, whichever first). No disk spool: claude is cove-master's child, so a cove-master death ends the session anyway.
- Each session (connection) sends buffered events from `lastAcked+1`, then live. `EventAck` trims the buffer. A reconnect therefore replays exactly the unacked tail.
- **Overflow:** drop oldest. Jam sees the seq hole and records a gap (§3) — loss is explicit, never silent.
- **Done ordering:** when the workload finishes, the client sends all buffered events, waits up to 5 s for an ack covering the last seq, then sends `Done`. A missing ack is logged, not fatal.

## 3. Jam ingest and storage

### Package `internal/jam/sessionevents` (grpc-free)

```go
type Event struct {
    ActorID, StreamID string
    Seq               uint64
    Turn              uint32
    ObservedAt, ReceivedAt time.Time
    Raw               []byte // exact line bytes; encoded as `raw` (valid JSON) or `raw_text` (anything else) in JSONL and export
    TruncatedBytes    uint64
    Kind              string // "event" | "gap"
    GapFrom, GapTo    uint64 // Kind == "gap"
    Stamp             Stamp  // from the Instance at ingest
    Index             Index  // derived, best-effort
}
type Stamp struct { Project, Role, Unit, Owner, SessionKind string; RaisedAt time.Time }
type Index struct {
    Type, Subtype, ToolName, ClaudeSessionID string
    CostUSD   float64
    InputTokens, OutputTokens int64
    DurationMS int64
    IsError    bool
}

type Store interface {
    Append(ev Event) error
    HighWater(actorID, streamID string) (uint64, bool)
    List(f Filter) ([]Event, error) // by actor, stream, after_seq, limit; seq order
    Streams(actorID string) ([]StreamInfo, error) // past + current sessions
    DeleteBefore(t time.Time) (int, error)
}
```

### Ingest

`Ingest.Append(actorID string, ev Event) (ack uint64, err error)`, called from the Attach recv loop:

1. `seq <= highWater` → duplicate (replay); drop, re-ack.
2. `seq > highWater+1` → append a `gap{from: highWater+1, to: seq-1}` row first.
3. Stamp from the Instance, derive the Index (tolerant JSON parse; unknown types/fields leave Index empty), `Store.Append`.
4. Publish to the Hub.
5. Return the new high-water.

The Attach server coalesces acks: the per-connection send loop emits one `EventAck` per stream with a new high-water on a 250 ms tick (not via the drop-on-full control channel), always after persistence — acked means durable. Acks are cumulative, so a lost one is repaired by the next.

### Backends (same selection rule as the intercom log)

- **File:** serve-config `session-events-dir: <path>` — one append-only JSONL file per `(actor_id, stream_id)`; high-water recovered by scanning the tail at open.
- **Postgres:** when `store-postgres` is set — package `internal/jam/sessionevents/sessionpg`, following the `intercompg` pattern (own embedded `migrations/0001_session_events.sql`, own `session_events_schema_migrations` table and advisory lock, shares the control-plane pool):
  - table `session_events`: `actor_id`, `stream_id`, `seq` (PK of the three), `kind`, `gap_from`, `gap_to`, `turn`, `observed_at`, `received_at`, `project`, `role`, `unit`, `owner`, `session_kind`, `raised_at`, `type`, `subtype`, `tool_name`, `claude_session_id`, `cost_usd`, `input_tokens`, `output_tokens`, `duration_ms`, `is_error`, `truncated_bytes`, `raw jsonb` (NULL when not valid JSON), `raw_text text` (set only then).
  - indexes: `(actor_id, received_at)`, `(type, tool_name)`.
  - A line that is not valid JSON (e.g. truncated) goes to `raw_text` — the original bytes are never lost. Both backends are content-equal, not byte-exact (`jsonb` normalizes key order/whitespace; the JSONL encoder compacts and escapes `<>&`); the exact bytes remain in `cove-master.log`. `jsonb` rejects `\u0000` escapes — such lines fall back to `raw_text`.
- **Neither configured:** a no-op store — events are acked and dropped, so coves never back up. This is the kill switch.

### Retention

`session-events-retention: <duration>` (e.g. `90d`; unset = keep forever). A daily sweeper calls `DeleteBefore`. The file backend deletes whole files whose last event precedes the cutoff.

### Hub

In-memory per-actor fan-out; one buffered channel per subscriber. A full subscriber is dropped (channel closed), never blocking ingest; it recovers by reconnecting and backfilling from the Store. The Hub is the seam for the live UI now and automated feedback / the Studio view later.

## 4. Live view, export, access

### Admin UI (behind the existing `/ui/` gate: loopback or operator OIDC)

- `GET /ui/coves/{id}/session` — timeline for one cove, linked from each Studios row; a stream selector lists current and past streams (`Store.Streams`), so torn-down coves remain viewable — the audit viewer.
  - Header: running totals — turns, tool calls, tokens, cost — updated live.
  - Body grouped by turn: assistant text (escaped); `tool_use` → tool name + one-line input summary, expandable to full input; `tool_result` collapsed with size + error badge; `result` → cost/tokens/duration; per-event raw-JSON toggle (`<pre>`, escaped).
  - `system/thinking_tokens` progress ticks (≈40 per short turn in a real capture) are hidden by default behind a "show progress events" toggle.
  - Inline markers: gaps ("⚠ N events lost") and truncation ("✂ X dropped").
- `GET /ui/coves/{id}/session/events?stream=` — SSE of server-rendered HTML fragments (the `/me/events` pattern). On connect: backfill from the Store, then subscribe to the Hub. SSE id = `stream_id:seq`; `Last-Event-ID` resumes with no dups or holes.

### Export (admin JSON API, existing authenticator)

`GET /admin/sessions/{actor_id}/events?stream=&after_seq=&limit=` (behind the existing `/admin/*` operator authenticator) → NDJSON, one object per event: raw event plus stamp and index fields. The audit trail is usable without the UI and is the hook for feedback tooling.

### Access

Operators only. All rendering HTML-escaped. Nothing pushed to `/me`, Discord, or Linear.

## 5. Testing (TDD, hermetic)

- **Fixtures:** real stream-json captured once into `testdata/` — plain turn, tool-using turn, error result, oversized tool_result.
- **agentrun:** fake spawner writes fixtures to the stdout writer; assert Event calls, turn numbering across `--continue`, `truncated_bytes`, stderr not forwarded, argv includes `--output-format stream-json --verbose`.
- **covemaster (bufconn):** seq monotonic; replay from `lastAcked+1` after a forced disconnect; ack trims; overflow drops oldest; flush-then-Done ordering.
- **attach + ingest:** dup drop, gap insertion, ack coalescing and ack-after-persist, old cove (no events) unaffected.
- **Store conformance:** one suite run against file and Postgres (pg behind the existing integration tag); retention sweeper with a fake clock; non-JSON raw round-trips.
- **Hub:** slow subscriber dropped, ingest never blocks.
- **Admin UI:** `<script>` in tool output and raw JSON renders inert; SSE backfill → live → `Last-Event-ID` resume without dups; routes sit behind the operator gate.

## 6. Rollout

Jam first (ingest + ack), then rebuild cove images. Storage is on only when `session-events-dir` or `store-postgres` is configured. Docs updated in the same PRs: a new leaf `docs/usage/jam/session-events.md` owns the feature (what is captured, storage, retention, sensitivity, export); `coves.md` (Attach wire), `serve.md` (config rows) and `ui.md` (session view) link to it; `docs/usage/jam/INDEX.md` gains its row; docs-audit run.

## Slices

1. Proto + covemaster buffer/seq/ack/replay.
2. agentrun stream-json + tee + `Handle.Event`.
3. `sessionevents`: ingest, file store, Hub; wired into Attach and serve config.
4. Postgres store (`sessionpg`), retention sweeper.
5. Admin UI session view + SSE.
6. NDJSON export endpoint.

## Out of scope

Dispatch / chat / switchboard sessions; `/me` and the Studio view; automated-feedback rules (next spec, consuming the Hub and Index); any change to secret handling — events are agent output and never include injected secret values by construction of the existing air-gap, but may include anything the agent itself read.
