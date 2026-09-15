# Intercom / Squawk Rename Implementation Plan

> **For agentic workers:** This is a single atomic rename — the tree does NOT compile in intermediate states, so there is exactly one commit at the end, gated by a full build/test/lint/grep pass. Do not commit partway. Work through the phases in order.

**Goal:** Rename the msglog-backed messaging system to **intercom** and a log entry to a **squawk**, across code, config, DB schema, endpoints, state files, MCP identity, and live docs.

**Architecture:** Pure rename, behavior unchanged. Mechanical passes (`git mv`, path/selector `sed`, scoped `gofmt -r`) followed by hand edits to non-Go breaking surfaces, then the existing test suite is the safety net.

**Tech Stack:** Go 1.26, `pgx/v5`, `just` task runner. Branch: `intercom-squawk-rename` (already created).

## Global Constraints

- **Spec is authoritative for the full identifier map:** `docs/superpowers/specs/2026-09-15-intercom-squawk-rename.md`. This plan carries the execution; consult the spec's §2/§3 tables for every kept-vs-renamed decision.
- **Never touch `internal/switchboard` or `cmd/at-switchboard`** — its `Message` type is a different concept and stays. This is why `gofmt -r 'Message -> Squawk'` is run **per-package inside the renamed dirs only**, never repo-wide.
- **Never touch `docs/superpowers/`** history (except adding this plan/spec, already done).
- **Kept identifiers** (generic, not message-derived): `Target`, `Reach`/`Internal`/`External`, `Filter`, `Log`, `Store`, `Delivery`, `Engine`, `Surface`, `Markers`, `Cursors`, `CommitSeq`, `inbox`, egress/ingress, and the MCP tool verbs `send`/`read`/`commit`/`list_targets`/`escalate`.
- **Package map:** `internal/msglog`→`internal/intercom`, `msglogpg`→`intercompg`, `msglogtest`→`intercomtest`, `internal/msgport`→`internal/relay`.
- **Type map:** `msglog.Message`→`intercom.Squawk`; `harbor.Comment`→`harbor.Squawk`.
- One commit, exact trailer:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Phase 1: Move package directories

- [ ] **Step 1:** `git mv` the directories (children before parent so paths resolve):
  ```bash
  git mv internal/msglog/msglogpg   internal/msglog/intercompg
  git mv internal/msglog/msglogtest internal/msglog/intercomtest
  git mv internal/msglog            internal/intercom
  git mv internal/msgport           internal/relay
  ```

## Phase 2: Package clauses & import paths

- [ ] **Step 2:** Rewrite package clauses:
  ```bash
  sed -i 's/^package msglogpg$/package intercompg/'     internal/intercom/intercompg/*.go
  sed -i 's/^package msglogtest$/package intercomtest/' internal/intercom/intercomtest/*.go
  sed -i 's/^package msglog$/package intercom/'         internal/intercom/*.go
  sed -i 's/^package msgport$/package relay/'           internal/relay/*.go
  ```

- [ ] **Step 3:** Rewrite import paths repo-wide (most-specific first so `msglog` doesn't clobber `msglogpg`/`msglogtest`). Scope to Go files, exclude the vendored `.gopath`:
  ```bash
  files=$(rg -l --glob '*.go' --glob '!.gopath/**' 'internal/(msglog|msgport)')
  for f in $files; do
    sed -i \
      -e 's#internal/msglog/msglogpg#internal/intercom/intercompg#g' \
      -e 's#internal/msglog/msglogtest#internal/intercom/intercomtest#g' \
      -e 's#internal/msglog#internal/intercom#g' \
      -e 's#internal/msgport#internal/relay#g' "$f"
  done
  ```

- [ ] **Step 4:** Rewrite package-selector references (the qualified `pkg.` prefixes). Most-specific first:
  ```bash
  files=$(rg -l --glob '*.go' --glob '!.gopath/**' '\b(msglogpg|msglogtest|msglog|msgport)\.')
  for f in $files; do
    sed -i \
      -e 's/\bmsglogpg\./intercompg./g' \
      -e 's/\bmsglogtest\./intercomtest./g' \
      -e 's/\bmsglog\./intercom./g' \
      -e 's/\bmsgport\./relay./g' "$f"
  done
  ```

## Phase 3: Type & identifier renames

- [ ] **Step 5:** `Message` → `Squawk` inside the intercom packages ONLY (scoped — never repo-wide, to spare `switchboard.Message`). `gofmt -r` is AST-aware, so it renames only the identifier, not substrings:
  ```bash
  gofmt -w -r 'Message -> Squawk' internal/intercom/*.go
  gofmt -w -r 'Message -> Squawk' internal/intercom/intercompg/*.go
  gofmt -w -r 'Message -> Squawk' internal/intercom/intercomtest/*.go
  ```
  Then fix qualified references in consumers (they now read `intercom.Message` after Phase 2 Step 4):
  ```bash
  files=$(rg -l --glob '*.go' --glob '!.gopath/**' 'intercom\.Message')
  for f in $files; do sed -i 's/\bintercom\.Message\b/intercom.Squawk/g' "$f"; done
  ```

- [ ] **Step 6:** Rename remaining Go identifiers by hand (see spec §2). At minimum:
  - `internal/intercompg`: helper `scanMessages`→`scanSquawks`; error prefix string `"msglogpg: "`→`"intercompg: "`.
  - `internal/relay`: error/log prefix strings `"msgport"`→`"relay"`.
  - `internal/harbor` (`messages.go`→`squawks.go`): `Comment`→`Squawk`, `MessagesHandler`→`SquawksHandler`, `NewMessagesHandler`→`NewSquawksHandler`, `maxMessageBodyBytes`→`maxSquawkBodyBytes`, `messagesStore`→`squawkStore`. `git mv internal/harbor/messages.go internal/harbor/squawks.go`.
  - `internal/harbor/adminui` (`messages.go`→`intercom.go`): `MessageReader`→`SquawkReader`, `handleMessages`→`handleIntercom`, `msgRow`→`squawkRow`, `messagesData`→`squawksData`. `git mv internal/harbor/adminui/messages.go internal/harbor/adminui/intercom.go` and `git mv internal/harbor/adminui/templates/messages.html internal/harbor/adminui/templates/intercom.html`.
  - `cmd/cove-master/mcp.go`: `messageOut`→`squawkOut`, field `Messages`→`Squawks` (JSON tag `messages`→`squawks`), MCP server `Implementation{Name: "messaging"}`→`Name: "intercom"`.
  - `cmd/at-harbor`: `git mv cmd/at-harbor/msgport_discord.go cmd/at-harbor/relay_discord.go`, same for `msgport_linear.go`→`relay_linear.go` and their `_test.go` / `msgport_seed_test.go`→`relay_seed_test.go`.

## Phase 4: Non-Go breaking surfaces

- [ ] **Step 7: Config key.** In `cmd/at-harbor/config.go`: field `MessageLog string \`yaml:"message-log"\`` → `IntercomLog string \`yaml:"intercom-log"\``. Update its doc comment. Fix every reference to `cfg.MessageLog`/`c.MessageLog` in `cmd/at-harbor/main.go` and tests.

- [ ] **Step 8: State-file prefixes.** In `cmd/at-harbor/main.go`: the derived paths `msgport-cursors.json` / `msgport-markers.json` / `msgport-receipts.json` → `relay-cursors.json` / `relay-markers.json` / `relay-receipts.json`.

- [ ] **Step 9: DB migrations** (in `internal/intercom/intercompg/migrations/`, rewrite in place — deployments start empty, no `ALTER…RENAME`):
  - `git mv .../migrations/0001_messages.sql .../migrations/0001_squawks.sql`, then edit: `messages`→`squawks`, `message_recipients`→`squawk_recipients`, `message_id`→`squawk_id`; index names `idx_messages_*`→`idx_squawks_*`, `idx_recipients_target` kept or → `idx_squawk_recipients_target` (match whatever the queries use).
  - `0002_seq.sql`: `ALTER TABLE messages`→`ALTER TABLE squawks`; `idx_messages_seq`→`idx_squawks_seq`.
  - In `intercompg.go`: every SQL string referencing those tables/columns; bookkeeping table `msglog_schema_migrations`→`intercom_schema_migrations`; advisory lock `const migrateAdvisoryLock = 0x6d73676c6f67` → `0x696e746572636f6d` (comment `// "intercom"`).

- [ ] **Step 10: HTTP endpoints.** In `internal/harbor/squawks.go` and every caller (`cmd/cove-master`, tests, docs): `/messages`→`/squawks`, `/messages/targets`→`/squawks/targets`, `/messages/commit`→`/squawks/commit`.

- [ ] **Step 11: Admin UI route.** In `internal/harbor/adminui/adminui.go`: `GET /ui/messages`→`GET /ui/intercom`; nav label / page title "Messages"→"Intercom"; template load path → `intercom.html`.

## Phase 5: Docs (live only)

- [ ] **Step 12:** `git mv docs/usage/harbor/messaging.md docs/usage/harbor/intercom.md`; retitle and rewrite vocabulary (`/squawks` endpoint, `cove-master mcp` stdio, MCP server "intercom").
- [ ] **Step 13:** Update `docs/usage/harbor/comms-addressing.md` (keep filename), `serve.md` (the `intercom-log:` key, `/ui/intercom`, the "message log follows the store backend"→"squawk log" paragraph), `coves.md`, `ui.md`, `escalation.md`, `DEVELOPMENT.md`.
- [ ] **Step 14:** `docs/usage/harbor/INDEX.md`: update the row that pointed at `messaging.md` (path + one-line summary) and any frontmatter `owns:`/`prereqs:` cross-refs naming `messaging.md`. Also update the harbor `dev/`… no-op; and re-point the `docs/usage/harbor/serve.md` frontmatter if it names the renamed doc.

## Phase 6: Verification gate (the single hard gate)

- [ ] **Step 15:** `just build` → compiles (proves packages/imports/config line up). Fix undefined-reference errors until clean.
- [ ] **Step 16:** `just test` → green (hermetic units, incl. the intercom conformance harness).
- [ ] **Step 17:** `just lint` → green (`go vet` + `gofmt` + shell/Dockerfile lint).
- [ ] **Step 18:** Migrations round-trip: `just dev-up`, `export HARBOR_TEST_POSTGRES_DSN="host=localhost port=15432 dbname=harbor user=harbor password=harbor sslmode=disable"`, `just integration-harbor` → green (rewritten migrations apply, pg backend round-trips squawks). If Docker is unavailable in this environment, note it and rely on the hermetic file-backend conformance tests instead.
- [ ] **Step 19: Grep gate.** No renamed-surface leftovers remain:
  ```bash
  rg -n --glob '!.gopath/**' --glob '!docs/superpowers/**' \
     'msglog|msgport|message-log|messageOut|/messages|/ui/messages|0x6d73676c6f67' \
     -g '!internal/switchboard/**' -g '!cmd/at-switchboard/**'
  ```
  Expected: only legitimate plain-English uses of "message" in prose/comments (not identifiers, paths, keys, routes). Every hit is triaged; identifier/path/key/route hits are bugs to fix.

- [ ] **Step 20: Commit** (single commit, exact trailer from Global Constraints):
  ```bash
  git add -A
  git commit -F - <<'EOF'
  intercom: rename the msglog messaging system to intercom/squawk

  Rename the msglog-backed messaging system to "intercom" and a log entry
  to a "squawk". Packages msglog→intercom (+intercompg/intercomtest),
  msgport→relay; type Message→Squawk; breaking surfaces (config key
  intercom-log, DB tables squawks/squawk_recipients + advisory lock,
  /squawks + /ui/intercom endpoints, relay-*.json state files, MCP server
  identity) hard-renamed with no shims (pre-release, Postgres starts empty).
  switchboard and docs/superpowers history untouched.

  Spec: docs/superpowers/specs/2026-09-15-intercom-squawk-rename.md

  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  EOF
  ```

## Self-review notes (author)

- **Spec coverage:** every §2/§3/§4 surface maps to a step (packages→P1/2, identifiers→P3/6, config→S7, state files→S8, DB→S9, endpoints→S10, admin UI→S11, docs→P5). ✔
- **Ordering hazard:** import-path and selector seds MUST run most-specific-first (`msglogpg`/`msglogtest` before `msglog`) — encoded in Steps 3–4. ✔
- **Switchboard safety:** `gofmt -r Message->Squawk` is dir-scoped to `internal/intercom*` only; grep gate excludes switchboard. ✔
- **Index name drift:** Step 9 says match index names to whatever the queries actually reference — the executor confirms against `intercompg.go` rather than assuming. ✔
