# Structured turn end — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A woken session finds its messages in the prompt; its final text is posted for it; what it was shown is committed for it. An optional `turn_end` tool changes the defaults, and `keep_*` tools keep processes alive across turns.

**Architecture:** The cove's long-lived agent process (`jam-studio-agent run`, `internal/agentrun.Workload`) does the turn-end work itself, over Jam's existing cove-facing HTTP endpoints:
- `GET /squawks` for the batch;
- `POST /squawks` to post;
- `POST /squawks/commit`, `/end`, `/idle` and `/alarms/{name}`.

The MCP server (`jam-studio-agent mcp`, a child of claude) reaches the Workload over a local unix socket for `turn_end` and `keep_*`. Jam gains three small things:
- an auto-post marker part, which wake-on skips for sessions, plus a duplicate-reply skip;
- `PUT /mute`;
- a turn-protocol flag on `GET /context` that switches the boilerplate guidance.

**Tech Stack:** Go 1.26, `net/http` over a unix socket, the existing MCP Go SDK, Postgres-free unit tests (in-memory stores and fakes).

**Spec:** `docs/superpowers/specs/2026-10-28-structured-turn-end-design.md`

## Global Constraints

- **Batch cap:** 20 entries or 32 KiB of rendered text, whichever comes first. A single entry over the cap is cut, with a marker naming its id for `read`.
- **What gets posted:** only the `result` event's text of a **normal** turn (`subtype: "success"`, `is_error: false`). The text is trimmed, and empty means nothing is posted. A non-normal turn posts nothing and commits nothing.
- **Never commit anything not shown in full or in part in a delivered batch.** Commit `up_to` = the last entry rendered.
- **Turn-end processing finishes before the next prompt is written into the episode.** Post, commit, then directives. This is what keeps a later batch from re-showing committed messages.
- **Every HTTP call the Workload makes is bounded at 10 s.** Failures are logged at WARN and never stop the run.
- **The auto-post part:** `intercom.TurnEndPartType = "application/vnd.jam.turn-end+json"`, no data, at most one per squawk.
- **Context and sockets:**
  - turn protocol on `GET /context?turn-protocol=2`;
  - agent socket at `/dev/shm/jam-agent.sock`, mode 0600;
  - keep logs in `/agent-data/keep/<name>.log`, capped at 1 MiB with one `.1` rotation;
  - at most 8 kept processes; keep names match `^[a-z0-9][a-z0-9_-]{0,31}$`.
- **Secrets never hit logs or argv** (AGENTS.md). The identity token travels only in `Authorization` headers.
- **Docs are updated in the same change** (AGENTS.md). The owning docs are `docs/usage/jam/turn-end.md`, `intercom.md`, `session-context.md` and `previews.md`.

## Review Focus

1. **A `send` reply and the same final text.** The duplicate-skip must stop the second post (Task 1 test).
2. **A wake arriving mid-turn while the last turn's commit is still in flight.** The next batch must not re-show committed entries (Task 5 ordering test).
3. **A Jam without `/mute` or without the turn-end part (an older Jam).** The cove keeps working: it logs and carries on (Task 4 fake returning 404).
4. **An interrupted turn (`subtype` not success).** Nothing is posted and nothing committed, and the acknowledge turn still gets the batch (Task 5 test).
5. **A kept process that exits while the session is waiting.** It wakes the agent locally with `process-exited` and its last log lines, and Jam sees Running (Task 7 test).

---

### Task 1: Jam — auto-post marker, wake-on skip, duplicate skip

**Files:**
- Modify: `internal/intercom/ask.go` (validateParts), `internal/jam/squawks.go` (handlePost), `internal/wakeon/wakeon.go` (replies)
- Test: `internal/intercom/ask_test.go`, `internal/jam/squawks_test.go`, `internal/wakeon/wakeon_test.go`

**Interfaces:**
- Produces: `const intercom.TurnEndPartType`, and `func intercom.IsTurnEnd(m Squawk) bool`.
- `POST /squawks` accepts `"auto": true`. The squawk then carries `Part{Type: TurnEndPartType}`.
- The response adds `"skipped": true` (with no `id`) when the duplicate check suppressed it.

- [ ] Test `TestValidatePartsTurnEnd`: one turn-end part is OK; two are `ErrInvalid`; a turn-end part with data is `ErrInvalid`.
- [ ] Test `TestSquawksAutoPostMarked`: a POST with `auto:true` → the stored squawk has exactly one part, of type `TurnEndPartType`.
- [ ] Test `TestSquawksAutoPostSkipsDuplicate`: the session posts "Hi" (normal send), then posts `auto` "  Hi\n" to the same (home) channel within 10 min → 200 `{"skipped":true}`, and the log has one squawk. Outside 10 min, or to a different channel, it posts.
- [ ] Test `TestRepliesSkipSessionAutoPosts`: another session's auto-post doesn't wake; its plain post does; a person's post carrying the part (impossible via the API, but via the store) still wakes.
- [ ] Implement:
  - The part check goes in `validateParts`.
  - In `handlePost`, the `auto` field appends the part. The duplicate check uses the channel's recent history (`ChannelBefore`, last 20) for a post by this actor with the same trimmed body within 10 min.
  - In `replies`, `isSession(m.From) && intercom.IsTurnEnd(m)` → skip.
- [ ] Run `GOPROXY=https://proxy.golang.org go test ./internal/intercom/... ./internal/jam/ ./internal/wakeon/` → ok. Commit `feat(intercom): mark auto-posted turn ends; sessions are not woken by them`.

### Task 2: Jam — `PUT /mute`

**Files:**
- Modify: `internal/jam/instance.go` (field), `internal/jam/supervisor.go` (setter, clear at turn start), `internal/jam/turnend_handler.go` (route), `cmd/jam/mux.go` (mount), `internal/wakeon/wakeon.go` (replies honors it)
- Test: `internal/jam/turnend_handler_test.go`, `internal/jam/supervisor_test.go`, `internal/wakeon/wakeon_test.go`

**Interfaces:**
- Produces: `Instance.MessagesMuted bool` (json `messages_muted,omitempty`), and `func (s *Supervisor) SetMessagesMuted(actorID string, on bool) error`.
- `PUT /mute {"on": bool}` → 204.

- [ ] Tests:
  - `TestMuteHandler`: 204, and the field is set; a bad body is 400; a non-PUT is 405.
  - `TestRecordActivityClearsMuteAtTurnStart`: muted, then Waiting→Running → cleared; Running→Waiting keeps it.
  - `TestRepliesMuted`: a muted waiting session gets no squawk wake, but its fired alarm still wakes it.
- [ ] Implement:
  - The clear sits beside `IdleDeadline = time.Time{}` in the `turnStarted` branch.
  - `replies` returns nil when `inst.MessagesMuted`.
  - Route `/mute` through `TurnEndHandler` (method PUT, like `/idle`).
- [ ] Run the package tests → ok. Commit `feat(jam): PUT /mute — new messages don't wake the session until it next runs`.

### Task 3: Jam — turn-protocol guidance

**Files:**
- Modify:
  - `internal/jam/sessionctx/sessionctx.go` (SessionFacts)
  - `internal/jam/sessionctx/boilerplate.go`
  - `internal/jam/context_handler.go` (reads `turn-protocol`)
  - `internal/agentrun/contextrefresh.go` (the fetch sends `?turn-protocol=2`)
- Test: `internal/jam/sessionctx/boilerplate_test.go`, `internal/jam/context_handler_test.go`, `internal/agentrun/contextrefresh_test.go`

**Interfaces:**
- Produces: `SessionFacts.TurnProtocol int` (0 = legacy, 2 = structured) and `const agentrun.TurnProtocol = 2`.

- [ ] Tests:
  - `TestBoilerplateStructuredTurns`: with `TurnProtocol: 2`:
    - the core contains "End your turn with your reply" and "`keep_start`";
    - it does not contain "Background processes you start die";
    - a ticket session's core contains "comment on your ticket";
    - with 0, the output is byte-identical to today.
  - `TestContextHandlerTurnProtocol`: `?turn-protocol=2` changes the fingerprint and includes the new lines.
  - `TestHTTPContextSourceSendsTurnProtocol`: the request URL has `turn-protocol=2`.
- [ ] Implement the new lines, exactly:
  - **turns:** "- Turns: each turn is one `claude -p` run; ending your turn is how you wait. New messages arrive in your prompt. End your turn with your reply — your final text is posted for you (to your own channel unless `turn_end` names another recipient); end with no text when there is nothing to say, and don't also `send` it. Ending your turn marks the messages you were shown as handled; `turn_end` changes that, what wakes you, or what happens next (compact, end). Shell background jobs die with the turn: to keep something running (a preview), use `keep_start` — it survives your turns and wakes you if it exits; agree with the person how long it should run and set a `wake_at` to ask them then."
  - **ticket extra:** "Your final text is a comment on your ticket: end with no text when your `report` already says it."
  - The comms lines stay as they are.
- [ ] Run tests → ok. Commit `feat(sessionctx): structured-turn guidance for coves that speak it`.

### Task 4: agentrun — the final text, the Jam client, batch render

**Files:**
- Modify: `internal/agentrun/harness.go` (Event fields), `harness_claude.go` (ParseEvent), `idle.go` (captures the turn end)
- Create:
  - `internal/agentrun/turnio.go`: the `TurnIO` interface and its HTTP implementation
  - `internal/agentrun/batch.go`: the fetch and render
- Test: `harness_claude_test.go`, `idle_test.go`, `turnio_test.go` (httptest), `batch_test.go`

**Interfaces:**
- Produces:
  - `Event.Text string` and `Event.OK bool` (set on `EventTurnEnd`).
  - `type TurnResult struct{ Text string; OK bool }`.
  - `func (t *idleTracker) TakeTurnEnd() (TurnResult, bool)`: the last TurnEnd not yet taken. It is recorded under the mutex **before** `changed` is signalled.
  - `type TurnIO interface`:
    - `Inbox(ctx, limit int) ([]InboxEntry, error)`
    - `Post(ctx, text, to string) error` (auto:true)
    - `Commit(ctx, upTo string) error`
    - `End(ctx, reason string) error`
    - `SetAlarm(ctx, name, at, note string) error`
    - `SetIdle(ctx, duration string) error` (scope next)
    - `Mute(ctx, on bool) error`
  - `func HTTPTurnIO(baseURL, token string) TurnIO`
  - `type InboxEntry` mirrors the read JSON (id, author, body, at, content_type, channel{label,kind}, parts[{type,ref,name,size}]).
  - `func renderBatch(es []InboxEntry, more bool) (text, upTo string)`

- [ ] Tests:
  - `TestParseEventResultText`: `{"type":"result","subtype":"success","is_error":false,"result":"hi"}` → `Text "hi"`, `OK true`; `subtype "error_during_execution"` → `OK false`.
  - `TestTrackerTakeTurnEnd`: the value is taken once; a second take gets false.
  - `TestHTTPTurnIO`:
    - each method's method, path and body (Inbox = `GET /squawks?limit=N`);
    - the bearer header is set;
    - a 404 on `/mute` returns `ErrUnsupported`.
  - `TestRenderBatch`:
    - the header line format is `From <author> in <channel label> (<kind>) at <RFC3339> — id <id>`;
    - attachments render as `[attachment <name> ref <ref>]`;
    - the 20-entry cap: given 21 entries, it renders 20 with `more` true;
    - the 32 KiB cut: a 40 KiB body is cut, with "… (cut — `read` id <id> for the rest)";
    - `upTo` is the last rendered id;
    - the closing line is as in the spec, depending on `more`.
- [ ] Implement:
  - `Inbox` asks for `limit+1` to learn whether there are `more`.
  - `HTTPTurnIO` follows `httpContextSource`'s style (10 s client).
- [ ] Run `go test ./internal/agentrun/` → ok. Commit `feat(agentrun): turn results, Jam turn client and inbox batch rendering`.

### Task 5: agentrun — delivery with the batch, posting and committing at turn end

**Files:**
- Modify: `internal/agentrun/workload.go` (Config.TurnIO, resume/first prompts, finishTurn), `commands.go` (the interrupt ack takes the batch), `cmd/jam-studio-agent/main.go` (wires `HTTPTurnIO(JAM_BASE_URL, token)`)
- Test: `internal/agentrun/workload_test.go` (fake harness/spawner, fake TurnIO)

**Interfaces:**
- Consumes: Task 4.
- Produces:
  - `Config.TurnIO TurnIO` (nil = legacy behavior, unchanged).
  - `type turnMeta struct{ upTo string; quiet bool }`, as Workload field `inflight` (Run goroutine only).
  - `func (w *Workload) finishTurn(ctx context.Context, r TurnResult)`.

- [ ] Tests (fake TurnIO records calls):
  - `TestWakeDeliversBatch`: on a Wake, the written resume contains the batch text and not "use the intercom `read` tool". When `Inbox` errors, the legacy resume text is written.
  - `TestTurnEndPostsAndCommits`: a normal result "Done." → `Post("Done.", "")` then `Commit(upTo)`, in that order.
  - `TestTurnEndEmptyTextNoPost`: whitespace → no Post; Commit still happens.
  - `TestTurnEndNotNormal`: an error result → neither Post nor Commit.
  - `TestTurnEndBeforeNextResume`: a Wake coalesced mid-turn → `Commit` is recorded before the second `Inbox` call.
  - `TestCompactTurnQuiet`: the turn of a written `/compact` posts and commits nothing.
  - `TestInterruptAckHasBatch`: the acknowledge prompt includes the batch.
  - `TestNilTurnIOUnchanged`: existing workload tests pass with `TurnIO` nil.
- [ ] Implement:
  - `resume()` and the post-exit `renderWake` path call `w.deliverText(ctx, rs)`. When TurnIO is set and Inbox succeeds, that is the wake lines + batch; otherwise the legacy `renderWake(w.resumeText(), rs)`. It sets `w.inflight.upTo`.
  - Writing `compactText` sets `inflight.quiet`.
  - In `episode()`, right after the select, call `if r, ok := tr.TakeTurnEnd(); ok { w.finishTurn(ctx, r) }`. Also drain once after the episode returns.
  - `finishTurn` consumes and resets `inflight`.
  - A standing session's `standingRestartPrompt` gets the batch appended.
- [ ] Run `go test ./internal/agentrun/ ./cmd/jam-studio-agent/` → ok. Commit `feat(agentrun): deliver messages in the wake; post the reply and commit at turn end`.

### Task 6: the agent socket and `turn_end`

**Files:**
- Create:
  - `internal/agentrun/agentsock.go`: the HTTP-over-unix server and its `Client`
  - `cmd/jam-studio-agent/mcp_turn.go`: the `turn_end` tool
- Modify: `workload.go` (directives applied in finishTurn; the socket served for Run's lifetime), `cmd/jam-studio-agent/mcp.go` (registers it; updated `read`/`commit`/`send` descriptions)
- Test: `agentsock_test.go`, `workload_test.go`, `mcp_turn_test.go`

**Interfaces:**
- Produces:
  - `type Directives struct`:
    - `To string`
    - `Post *bool`
    - `Commit *bool`
    - `Next string` (""|wait|compact|end)
    - `Reason string`
    - `WakeOnMessage *bool`
    - `WakeAt *WakeAt{Name, At, Note string}`
    - `IdleTimeout string`
  - `PUT /turn-end` on the socket stores the Directives (the last call wins, per turn).
  - `const agentrun.AgentSocketPath = "/dev/shm/jam-agent.sock"`.
  - `func DialAgentSocket(path string) *AgentClient`, with `SetTurnEnd(ctx, Directives) error`.

- [ ] Tests:
  - `TestTurnEndDirectives`:
    - `post:false` → no Post;
    - `commit:false` → no Commit;
    - `to:"user:x"` → `Post(text, "user:x")`;
    - `next:end` → `End(reason)`, and a missing reason → 400 from the socket;
    - `next:compact` → a compact is posted to cmds (instructions "");
    - `wake_on_message:false` → `Mute(true)`, and the next turn with the default → `Mute(false)` exactly once;
    - `wake_at` → `SetAlarm(name or "turn-end", at, note)`;
    - `idle_timeout:"2h"` → `SetIdle("2h")`.
  - Directives reset after each turn.
  - `TestMCPTurnEndTool`: the tool forwards to the socket; an unreachable socket gives the error "this studio's agent doesn't support turn_end (upgrade the studio)".
- [ ] Implement:
  - The socket listens in Run, is removed on exit, and is chmod 0600. A stale socket file is removed before listening.
  - Validation: `next` ∈ {"", wait, compact, end}; end needs a reason of at most 2000 bytes.
- [ ] Run tests → ok. Commit `feat: turn_end — the optional structured turn end`.

### Task 7: kept processes

**Files:**
- Create: `internal/agentrun/keep.go` (keeper), `cmd/jam-studio-agent/mcp_keep.go`
- Modify: `agentsock.go` (`POST /keeps`, `DELETE /keeps/{name}`, `GET /keeps`), `workload.go` (keeper lifetime; local wake on exit; restart cleanup), `wake.go` (the `process-exited` line)
- Test: `keep_test.go`, `workload_test.go`, `mcp_keep_test.go`

**Interfaces:**
- Produces:
  - `type keeper struct`:
    - `Start(name, command, workdir string) error`
    - `Stop(name string) error`
    - `List() []KeptProcess{Name, Command, PID, Started}`
    - `StopAll()`
  - The registry is persisted at `/agent-data/keep/registry.json`. The exit callback is `func(name string, status int, tail string)`.

- [ ] Tests:
  - `TestKeeperStartStopList`: uses `sh -c 'sleep 30'`. It covers the 8-process cap, an invalid name, and a duplicate name being an error.
  - `TestKeeperExitWakes`: `sh -c 'echo bye; exit 3'` → the callback gets status 3 and a tail containing "bye". The Workload posts the WakeReason `{Kind: "process-exited", Note: name, Detail: "status 3\n<last 20 lines>"}`, and `renderWake` shows "Kept process `x` exited (status 3); last lines:".
  - `TestKeeperSurvivesEpisodeExit`: a kept process is still alive after the episode's process exits, and is gone after Run's ctx is cancelled.
  - `TestKeeperRestartCleanup`: a registry entry from a previous run is killed (best-effort, by pgid) and is named in the first prompt as "stopped by a restart".
- [ ] Implement:
  - Spawn with `Setpgid`. Stdout and stderr go to the log, with a 1 MiB cap and rotation.
  - Stop: SIGTERM to `-pgid`, then SIGKILL after 10 s.
  - The tail is read from the log file.
- [ ] Run tests → ok. Commit `feat: keep_start/keep_stop/keep_list — processes that outlive turns`.

### Task 8: docs and spec reconciliation

**Files:**
- Modify: `docs/usage/jam/turn-end.md`, `intercom.md`, `session-context.md`, `previews.md`, `docs/usage/jam/INDEX.md` (if a row's trigger changes), the spec (architecture moved cove-side), `docs/TODO.md` (deferred items)

- [ ] Use docs-author.
  - turn-end.md owns: the structured turn end, `turn_end`, the defaults, `/mute`, the `process-exited` reason, and keep.
  - intercom.md owns: delivery in the wake, auto-commit, and the auto-post marker plus wake-on skip.
  - session-context.md: the turn-protocol switch.
  - previews.md: points to keep.
- [ ] Run the docs-audit checker → no new errors. Run `STRICT=1 ./scripts/lint.sh`. Commit `docs: structured turn end`.
