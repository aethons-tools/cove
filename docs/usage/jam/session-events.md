---
summary: Session events — Jam's operator-only capture of a managed studio agent's Claude Code stream-json output; how it is captured, buffered, redacted, stored (Postgres), retained, exported (NDJSON), and what its sensitivity means.
read_when: You want to watch, audit, or export what a managed studio's agent did — the captured Claude Code event stream — or you are configuring its storage/retention, wondering what is redacted or lost, or deciding who may see it.
owns: the session-event capture pipeline (cove side and Jam side), its storage backends + config keys, the raw/raw_text encoding guarantee, retention, the export API, and the operator-only sensitivity stance
prereqs: coves.md for the Attach stream and managed studios; serve.md for the serve config and store-postgres; ui.md#session-timeline for the browser view
tier: leaf
updated: 2026-10-02
---

# Session events

Jam records every `stream-json` stdout line of a managed studio's agent
(`claude -p --output-format stream-json --verbose`) as an ordered, per-stream
event log that operators can watch live, audit, and export. This includes hook
events and `system`/`thinking_tokens` progress events. It is **operator-only**
and carries agent output by design (see [Sensitivity](#sensitivity)).

## Cove side (cove-master)

- `internal/agentrun` writes the agent's stdout to the VM-local
  `/agent-data/agent-stream.jsonl` (append, 0600; never `cove-master.log`, which
  Jam reads on teardown) and to a line splitter; each line goes to `covemaster` as an event tagged with its
  **turn** (the episode — claude process — number, from 1; one episode can answer several prompts, see [coves.md](coves.md)).
- **Cost is cumulative per episode.** A `result` line's `total_cost_usd` is the
  process's running total, while `usage` is per result. Sum the *last*
  `total_cost_usd` of each `turn`, not every result.
- **Truncation:** a line over 1 MiB is cut; `truncated_bytes` counts the rest.
  The full line stays in `/agent-data/agent-stream.jsonl`.
- **Redaction:** exact occurrences of the cove's own identity token and launch
  secret become `«redacted»` before buffering. Edge case: a secret straddling
  the 1 MiB cut can leave a prefix behind. Redaction is exact-match, so it
  relies on the secrets being long and random (a hardening follow-up is deferred).
- **Buffer:** in memory, 10 000 events or 64 MiB. Overflow drops the oldest;
  Jam records a **gap row** ("⚠ N events lost"). After a reconnect the cove
  replays from the last ack.
- **Stream id:** 32 lowercase hex, random per cove-master process; Jam drops
  events with an invalid one.
- **Done** waits up to 5 s for the final ack. An old Jam never acks, so the cove
  still reaches Done after the 5 s delay.

## Jam side

The Attach server dedupes replays, turns a reported gap into a gap row
(`seq` = the gap's end), **persists, then publishes** to the live hub, and acks
per stream on a 250 ms tick, only after persistence. See
[coves.md](coves.md#the-attach-stream) for the stream itself.

## Storage and config

Session events are always stored in the Postgres table `session_events` (own migrations table `session_events_schema_migrations`, shared pool); `store-postgres` is required by `serve`.

| Key | Meaning |
|-----|---------|
| `session-events-retention` | `<N>d` or a Go duration; empty keeps forever. |

## Encoding guarantee

Each event stores `raw` (when the line is valid JSON) or `raw_text` (anything
else). The guarantee is **content-equal, not byte-exact**: `jsonb` normalizes; `raw_text` is
UTF-8-normalized (invalid bytes become U+FFFD) and, in Postgres, NUL becomes
U+FFFD (the derived index text columns are sanitized the same way). Any Postgres data exception (SQLSTATE class 22, e.g. a `\u0000` escape,
lone surrogate, or invalid UTF-8) falls back to sanitized `raw_text`. Exact
bytes remain in the cove's `/agent-data/agent-stream.jsonl`.

## Retention

A sweeper runs at start, then every 24 h. Rows are deleted by `received_at`.

## Export API

`GET /admin/sessions/{actor_id}/events` (behind the `/admin` operator auth)
returns `application/x-ndjson`, one event per line.

| Param | Meaning |
|-------|---------|
| `stream` | Stream id; default is the newest stream. |
| `after_seq` | Return only events after this sequence number. |
| `limit` | Default 1000, max 10000. |

Errors: 400 for a bad param or stream id; 404 when there are no events.

Fields: `actor_id`, `stream_id`, `seq`, `kind`, `gap_from`, `gap_to`, `turn`,
`observed_at`, `received_at`, `truncated_bytes`, `project`, `role`, `unit`,
`owner`, `session_kind`, `raised_at`, and the extracted `type`, `subtype`,
`tool_name`, `claude_session_id`, `cost_usd`, `input_tokens`, `output_tokens`,
`duration_ms`, `is_error`, plus `raw` or `raw_text`.

## UI

The browser timeline lives at `/ui/coves/{id}/session`; see
[ui.md](ui.md#session-timeline). The studio's page lists its streams
([ui-pages.md](ui-pages.md#studio-pages)). There is no `/me` exposure.

## Sensitivity

Events carry tool inputs and outputs: file contents, command output, possibly
secrets the agent read. They are visible to operators only. This is a
deliberate exception to [observability rule 3](../observability.md) ("raw
agent/VM output stays VM-local"), which governs the structured log sink, not
this separate audit channel.
