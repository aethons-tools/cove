# domain rename: the intercom / squawk vocabulary

**Status:** design approved (scope + naming map + both judgment calls confirmed), pre-plan
**Motivation:** "message"/"messaging"/"msg" is badly overloaded across the codebase. Rename the msglog-backed messaging system to **intercom**, and one entry in its log (a "message") to a **squawk**, so the domain vocabulary is workable.
**Touches:** `internal/msglog`→`internal/intercom` (core + file + pg + conformance), `internal/msgport`→`internal/relay`, `internal/harbor` (messages handler + adminui + instance/identity docs), `cmd/at-harbor` (config key, wiring, state-file prefixes, msgport_* files), `cmd/cove-master` (MCP server identity + DTOs), and live docs under `docs/usage/harbor/`.
**Explicitly out of scope:** `internal/switchboard` + `cmd/at-switchboard` (a distinct in-sandbox Discord conductor with its own unrelated `Message` type — left untouched); the design/spec history under `docs/superpowers/` (dated records, left as-is).

## Problem

Two unrelated concepts both read as "message", and "msg"/"message" appears in ~130 files (~1,450 Go lines): the durable log entry (`msglog.Message`), the harbor inbox DTO (`harbor.Comment`), the adapter engine (`internal/msgport`), the admin UI "Messages" view, the `message-log:` config key, the `messages`/`message_recipients` tables, and more. The overload makes the code hard to talk about and hard to navigate. The switchboard subsystem *also* has a `Message` type, compounding the confusion.

harbor is pre-release: the Phase-1 Postgres backend "starts empty" (no data migration), CI Postgres is ephemeral, and the only dev database is the disposable `dev/` compose. So the breaking surfaces (config key, DB schema, endpoints, on-disk state files, MCP identity) can be **hard-renamed with no back-compat shims**.

## The rename

**System = intercom. Log entry = squawk.** Sub-terms that are generic rather than "message"-derived are kept.

### 1. Packages & directories

| Now | New |
|---|---|
| `internal/msglog` | `internal/intercom` |
| `internal/msglog/msglogpg` | `internal/intercom/intercompg` |
| `internal/msglog/msglogtest` | `internal/intercom/intercomtest` |
| `internal/msgport` | `internal/relay` |

Import path base stays `github.com/aethons-tools/cove/internal/...`. Package clauses change to `intercom`, `intercompg`, `intercomtest`, `relay`. Use `git mv` for the directories so history follows.

### 2. Go identifiers

**`internal/intercom`** (was `internal/msglog`):
- `Message` → **`Squawk`** (the log entry). All method signatures that carried `Message` now carry `Squawk`; slices `[]Message`→`[]Squawk`.
- **Kept** (generic, not message-derived): `Log`, `Store` (interface), `Target`, `Reach` + `Internal`/`External`, `Filter`, `Open`, `Prepare`, `Classify`, `Append`, `ReadInbox`, `ReadInboxSince`, `ReadInboxBefore`, `ReadThread`, `List`, `ListSince`, `SeqOf`, `TailSeq`, `SeenIDs`, `Close`. Only the entry type name and doc vocabulary change.
- Package doc: "Package intercom is harbor's durable, append-only squawk Log: one envelope (`Squawk{...}`) per entry…".

**`internal/intercompg`** (was `msglogpg`):
- `Store` (pg) kept; internal helper `scanMessages` → `scanSquawks`; error prefix `"msglogpg: "` → `"intercompg: "`.

**`internal/intercomtest`** (was `msglogtest`): conformance harness; `msglog.Store`/`msglog.Message` references follow the package + type rename.

**`internal/relay`** (was `internal/msgport`):
- **Kept** (directional/generic): `Engine`, `Surface`, `Delivery`, `Event`, `EgressMark`, `Markers`, `Cursors`, `Directory`, `Config`, `New`, `Run`, egress/ingress methods. Only the package clause, import path, doc vocabulary, and the `msglog`→`intercom` type references change. Error/log prefixes `"msgport: "` → `"relay: "`.

**`internal/harbor`**:
- `Comment` (API DTO, "one message in a cove's inbox, returned by GET /messages") → **`Squawk`** (harbor package; distinct from `intercom.Squawk`, no Go collision).
- `MessagesHandler` → `SquawksHandler`; `NewMessagesHandler` → `NewSquawksHandler`; file `messages.go` → `squawks.go`.
- Unexported: `maxMessageBodyBytes`→`maxSquawkBodyBytes`; `messagesStore`/`inboxReader`/`appender` follow (`squawkStore`, others unchanged if generic).
- Doc-only: `Instance.CommitSeq` comment ("last message the cove committed" → "last squawk…"); `Delivery`/`DeliveryFor` docs ("how a Human receives messages" → "…receives squawks"). **Field/type names `CommitSeq`, `Delivery` kept.**

**`internal/harbor/adminui`** (`messages.go`):
- `MessageReader`→`SquawkReader`; `handleMessages`→`handleIntercom`; `msgRow`→`squawkRow`; `messagesData`→`squawksData`; `toRow(m msglog.Message)` follows the type rename. File `messages.go`→`intercom.go`; template `templates/messages.html`→`templates/intercom.html`.

**`cmd/cove-master`** (`mcp.go`):
- MCP server `Implementation.Name` `"messaging"` → `"intercom"`; `messageOut`→`squawkOut`; `Messages []messageOut`→`Squawks []squawkOut` (JSON tag `messages`→`squawks`).
- **Kept:** MCP tool verbs `send`, `read`, `commit`, `list_targets`, `escalate` (not message-derived).

### 3. Breaking surfaces (hard rename, no shims)

- **Config (`cmd/at-harbor/config.go`):** YAML key `message-log:` → **`intercom-log:`** (still a path to the JSONL log; struct field `MessageLog`→`IntercomLog`). Update `unknownServeKeys`/schema derivation automatically via the struct tag.
- **On-disk state files (`cmd/at-harbor/main.go`):** `msgport-cursors.json` / `msgport-markers.json` / `msgport-receipts.json` → `relay-cursors.json` / `relay-markers.json` / `relay-receipts.json`.
- **DB (`internal/intercompg/migrations/`):** rewrite the embedded migration SQL **in place** (deployments start empty, so no `ALTER…RENAME`):
  - `0001_messages.sql` → `0001_squawks.sql`: table `messages`→`squawks`, `message_recipients`→`squawk_recipients`, FK column `message_id`→`squawk_id`; indexes renamed to match (`idx_recipients_target`, `idx_squawks_reply_to`, `idx_squawks_project_id`).
  - `0002_seq.sql`: `ALTER TABLE squawks ADD COLUMN seq BIGSERIAL`; `idx_squawks_seq`.
  - Bookkeeping table `msglog_schema_migrations` → `intercom_schema_migrations`.
  - Advisory lock `const migrateAdvisoryLock = 0x6d73676c6f67 // "msglog"` → `0x696e746572636f6d // "intercom"` (8 bytes, exact). Distinct from the control-plane store lock, as before.
- **HTTP endpoints (`internal/harbor/messages.go`→`squawks.go`):** `/messages`, `/messages/targets`, `/messages/commit` → `/squawks`, `/squawks/targets`, `/squawks/commit`. The cove-side callers in `cmd/cove-master` follow.
- **Admin UI route (`internal/harbor/adminui/adminui.go`):** `GET /ui/messages` → `GET /ui/intercom`; nav label/page title "Messages" → "Intercom" (table lists squawks).

### 4. Docs (live only)

- Rename `docs/usage/harbor/messaging.md` → `intercom.md`; retitle and rewrite vocabulary (the `/squawks` endpoint, `cove-master mcp` stdio delivery, MCP server "intercom").
- `docs/usage/harbor/comms-addressing.md`: keep the filename (its subject is the target/access-graph, not the log); update "message" vocabulary to "squawk" where it means the log entry.
- Update mentions in `serve.md` (the `intercom-log:` key, `/ui/intercom`, the Postgres "message log follows the store backend" paragraph → "squawk log"), `coves.md`, `ui.md`, `escalation.md`, `DEVELOPMENT.md`.
- `docs/usage/harbor/INDEX.md`: update the row that pointed at `messaging.md` (path + summary), and any frontmatter `owns:`/`prereqs:` cross-refs naming `messaging.md`.
- **Leave `docs/superpowers/` history untouched.**

## Execution

**One atomic PR.** A rename cannot compile in partial states — package clauses, import paths, the DB schema, the config key, and the endpoints all move together — so splitting across PRs would leave non-building intermediates. Sequence within the PR:

1. `git mv` the four package directories; update package clauses and every import path (`internal/msglog*` → `internal/intercom*`, `internal/msgport` → `internal/relay`).
2. Rename identifiers with mechanical passes (`gopls`/`gofmt -r 'Message -> Squawk'` scoped per package to avoid touching the unrelated `switchboard.Message`, then manual review), file renames via `git mv`.
3. Edit the non-Go breaking surfaces by hand: migration SQL + advisory lock, config key + struct tag, state-file prefixes, HTTP routes, admin route/template, MCP identity + DTOs.
4. Rewrite the live docs and INDEX rows.

## Testing / verification

Behavior is unchanged — this is a pure rename — so the existing test suite is the safety net:
- `just test` (hermetic unit tests, including the intercom conformance harness) — green.
- `just lint` (vet + gofmt + shellcheck/hadolint) — green.
- `just build` of the binaries — compiles (proves imports/packages/config all line up).
- `just integration-harbor` against the `dev/` Postgres — the rewritten migrations apply cleanly and the pg backend round-trips squawks (updates `HARBOR_TEST_POSTGRES_DSN` references only if the DSN itself is unaffected — it is, the DB name stays `harbor`).
- `grep` gate: no `msglog`/`msgport`/`message-log`/`messageOut`/`/messages` references remain **in the renamed surfaces** (switchboard's `Message` and the `docs/superpowers/` history are the only allowed remaining "message" hits, plus generic English uses of the word in prose).

## Risks / notes

- **Scoping the identifier rename** so `gofmt -r 'Message -> Squawk'` never rewrites `switchboard.Message`: run it per-package (in the renamed dirs) rather than repo-wide.
- **The word "message" as plain English** legitimately survives in some comments/log strings; the grep gate targets identifiers/paths/keys, not every occurrence of the English word.
- **AGENTS.md rule:** docs land in the same PR — enforced by including the doc rewrites in the single PR above.
