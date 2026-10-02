# jam: Postgres-only storage (drop file backends Postgres already provides)

**Status:** draft for owner review
**Scope:** remove every Jam file-backed storage option whose role Postgres already fills — the control-plane `store:` (`jam.FileStore`), `intercom-log` (`intercom.Log` JSONL), and `session-events-dir` (`sessionevents.FileStore`). `store-postgres` becomes required for `at-jam serve`.
**Keeps (no Postgres equivalent today):** the subscription pool store (`pool.store`, `at-jam pool --store`), the relay cursors/markers/receipts files, and the credentials file (host supply, not storage).
**Does not change:** the `Store` / `intercom.Store` / `sessionevents.Store` interfaces' behaviour; Postgres schemas; `at-jam export`/`import` (admin-API based — the migration path from a file Jam); the allocator's nil-ledger fallback (made dead-in-practice by this change; its removal is a follow-up).

## Why

Two storage implementations per subsystem double the surface (conformance, migrations, docs, "which backend am I on" branches) while production runs Postgres. A file Jam is also a weaker product: no allocator ledger (personal sessions 409), optional intercom, dropped session events.

## Decisions

### 1. Config

- `store-postgres` is **required**; `serve` refuses to start without it (today "neither set" is silently accepted and fails on first write).
- `store`, `intercom-log`, `session-events-dir` become **removed keys**: a set value is a hard startup error with a migration hint, following the existing removed-key pattern (`discordConfig.DeprecatedBotToken`, `cmd/at-jam/config.go:184-186,383-385`). Hint text: export the old Jam's config with `at-jam export` (running it on the old version), then `at-jam import` into a Postgres Jam — see `docs/usage/jam/backup.md`. Squawk history and session events in the old files are not migrated (same as today's file→Postgres story); the error says so.
- The removed fields stay in `serveConfig` only as detection fields (like `DeprecatedBotToken`), never used otherwise.

### 2. Relay state gets its own home

Relay cursors/markers/receipts are file state with no Postgres equivalent, today written to `filepath.Dir(cfg.Store)` (`main.go:1884,1888,1901`) — even under Postgres. New serve-config key:

- `state-dir:` — directory for Jam's remaining file state (relay cursors, markers, receipts). Default `$XDG_STATE_HOME/at-jam`, falling back to `~/.local/state/at-jam`. Created `0700` at startup when a relay needs it.
- Add a small `atJamStateDir()` helper next to the existing `atJamConfigDir()` (`cmd/at-jam/config.go:663`).
- Operators upgrading move their three relay files from the old store directory into `state-dir` (or set `state-dir` to the old directory). Documented in serve.md; missing files just mean the relays start from scratch (cursors re-seed), which is the existing first-run behaviour.

### 3. Always-on intercom and session events

With Postgres mandatory, the message Log is always `intercompg` and session events always `sessionpg`. The `intercomLog != nil` / "not configured" branches (`main.go:1720-1772`, guards at 1743, 1850, 1865, 1882, 1955, 2050, 2122, 2131; admin UI "not configured" notice) become unconditional. Simplify them where the change is mechanical; keep `sessionevents.NopStore` only if a test still needs it.

### 4. Hermetic tests: in-memory stores replace the file stores

Postgres tests stay integration-only (`JAM_TEST_POSTGRES_DSN`), so the default `go test ./...` needs in-memory backends:

| Subsystem | New | Replaces in tests | Conformance |
|---|---|---|---|
| control plane | `jam.NewMemStore()` — `memState` + the FileStore mutators minus `save()` (lock → validate → apply) | `jam.NewFileStore` in ~34 test files / ~122 call sites | `storetest` runs hermetically on MemStore |
| intercom | `intercom.NewMemLog()` — in-memory `intercom.Store` (same append/Seq/cursor semantics as `Log`) | `intercom.Open` in 11 test files | `intercomtest.RunConformance` runs hermetically on MemLog |
| session events | `sessionevents.NewMemStore()` | `OpenFileStore` in 7 test files | `sessioneventstest.RunConformance` runs hermetically on MemStore |

These are exported (test helpers used across packages) but documented as **not for production**; `serve` never constructs them. Deleted: `internal/jam/filestore.go` (moving the `Store` interface to its own file, `store.go`), `filestore_test.go`, the file-format migrations (`migrateIdentities` v1/v2/v3→v4), `internal/intercom/log.go` file persistence and its file-specific tests, `internal/jam/sessionevents/filestore.go` and its file-specific tests (torn-tail/over-long-line cases go with it). File-format-specific behaviour has no in-memory analogue and is not ported.

### 5. Docs

Update the user docs that describe the removed keys or file fallbacks: `docs/usage/jam/serve.md` (sample config, key table: `store-postgres` required, removed keys, `state-dir`; fix its "no data migration" text to point at backup.md), `intercom.md`, `ui.md`, `intercom-ui.md`, `session-events.md`, `personal-sessions.md`, `standing-sessions.md`, `discord.md`, `backup.md` (migration path for file-Jam operators), `docs/OVERVIEW.md`, `docs/DEVELOPMENT.md` (hermetic story = in-memory stores). docs-audit clean.

## Testing

- Config: removed keys → error naming the key and the hint; missing `store-postgres` → error; `state-dir` default/explicit parsing; serve wiring test that relay paths derive from `state-dir`.
- Each in-memory store passes its subsystem's conformance suite hermetically; Postgres conformance unchanged under the integration tag.
- Full `go test ./...`, `just lint`, `go vet -tags integration ./...`, and the Postgres integration suites.

## Out of scope / follow-ups

- Make the allocator ledger mandatory and delete the nil-ledger fallback (`allocator.go`, `dispatcher.go`, `ErrNeedsLedger`).
- Move pool store and relay state into Postgres.
- An in-repo migration tool for squawk history / session events from old files.
