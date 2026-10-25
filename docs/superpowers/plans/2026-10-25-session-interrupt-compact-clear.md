# Session controls (interrupt, compact, clear) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Interrupt, Compact and Clear buttons in the session header. Each drives Claude's native interrupt, `/compact`, or a fresh conversation inside the cove.

**Architecture:**
- **Path of a press:** Jam's userui handler → `sessionOps.Command` → three new `ControlDown` messages on the Attach stream → `covemaster.Control` → `agentrun.Workload`.
- **Inside agentrun:** the commands are queued in a small command box, like the wake box, and acted on by the episode loop (live process) or by the post-episode wait (no process).

**Tech Stack:** Go, protobuf/gRPC (buf), html/template + htmx.

**Spec:** [docs/superpowers/specs/2026-10-25-session-interrupt-compact-clear-design.md](../specs/2026-10-25-session-interrupt-compact-clear-design.md)

## Global Constraints

- **TDD, hermetic tests** (`just test`). No real claude, Docker or network. agentrun tests use `streamSpawner`/`streamProc` (`internal/agentrun/episode_test.go`).
- **Docs are updated in the same change** as the behaviour (AGENTS.md).
- **Never block** the Attach receive loop or the stdout goroutine. `Workload.Control` only posts and signals.
- **Exact copy, from the spec:**
  - **Compact instructions** (Jam constant `jam.CompactInstructions`): `Keep: the current task and its goal; decisions made and why; work done and work still to do; open questions; the people involved and their latest requests; anything you were told to remember.`
  - **Interrupt acknowledge prompt** (`agentrun.interruptPrompt`): `<by> pressed **Interrupt** in the session header, which stopped your turn. Don't resume what you were doing. In your channel, acknowledge the interrupt with ` + "`send`" + `, ask what they want instead, then end your turn.`
  - **Pending-messages line** (appended to the acknowledge prompt when wakes were coalesced): `New messages may be waiting. Read them, but don't act on any request until the person who interrupted you answers.`
  - **Cleared notice** (`agentrun.clearedNotice`): `Your conversation was cleared at <by>'s request (the Clear button in the session header). Your workspace and memory files are as you left them.`
  - **Clear confirm:** `Clear this session's conversation? The agent keeps its files and memory but forgets the conversation.`
- **Button enablement** (by `inst.Activity`):
  - Interrupt: `running`.
  - Compact: `running`, `holding` or `waiting`.
  - Clear: `waiting`.
  - Anything else gets **409**.
  - Anyone `sessionFor` admits may press.

## Plan amendments to the spec (decided while planning)

1. **The commands bypass `jam.ControlSink`.** They don't go through `jam.ControlSink` (the supervisor's interface). `attach.Server` gets the methods, and `sessionOps` holds a narrow `sessionCommander` interface. The supervisor never sends these commands, and this avoids touching every `ControlSink` fake.
2. **Docs location.** The session header is documented in `docs/usage/jam/session-view.md`, not intercom-ui.md.
3. **Clear-pending vs the standing marker.**
   - At Run start, a present clear-pending file **wins over** the standing conversation marker: start fresh.
   - Consuming the clear-pending file on the first reply needn't remove the marker: `markConversation` rewrites it for the new conversation anyway.
4. **Button placement.** **Interrupt** is a visible header button next to Copy SSH, because speed matters. **Compact** and **Clear** go in the `⋯` menu with Release.
5. **Record events.** A command's record is a synthetic stdout-style line, emitted through `Handle.Event`: `{"type":"cove","subtype":"command","command":"interrupt|compact|clear","by":"<name>"}`. sessionview renders it as a `KindPrompt` line.

## Review Focus

1. **Interrupt pressed twice in one turn** → exactly one `control_request` and one acknowledge turn (Task 3 test `TestInterruptTwiceOneAck`).
2. **Interrupt racing turn end** (the `result` arrives before the press) → nothing written, no acknowledge turn (Task 3 `TestInterruptBetweenTurnsDropped`).
3. **Compact pressed while a Wake is coalesced mid-turn** → `/compact` first, then the resume prompt. The wake is not lost (Task 4 `TestCompactThenCoalescedWake`).
4. **Clear, then cove-master restarts before the agent replies** → the next Run starts without `--continue`, even for a standing session with the marker present (Task 5 `TestClearSurvivesRestart`).
5. **Wake arriving during an idle (maintenance) compact** → not written into the maintenance process. It starts a normal episode after it exits (Task 4 `TestWakeDuringIdleCompactWaits`).

---

### Task 1: Wire — proto, attach server, covemaster decode

**Files:**
- Modify: `internal/jam/attach/proto/attach.proto` (ControlDown oneof + 3 messages, exactly as in spec §1 Wire)
- Regenerate: `internal/jam/attach/attachpb/*.pb.go` via `just buf-gen` (generated files are committed)
- Modify: `internal/jam/attach/server.go` (next to `Wake`)
- Modify: `internal/covemaster/covemaster.go` (`ControlKind`, `Control`, `controlFromPB`)
- Test: `internal/covemaster/client_test.go`

**Interfaces:**
- Produces, in `attach.Server`:
  - `Interrupt(actorID, by string) bool`
  - `Compact(actorID, by, instructions string) bool`
  - `Clear(actorID, by string) bool`
  - Each returns `enqueue`'s result.
- Produces, in `covemaster`:
  - `ControlKind` values `Interrupt`, `Compact` and `Clear`, appended after `RunGate`.
  - `Control` gains fields `By string` and `Instructions string`.

- [ ] **Step 1: Write the failing test** `TestClientDecodesSessionCommands`. Model it on `TestClientDecodesWakeReasons`:
  - Call `srv.Interrupt("w1","Ann")`, `srv.Compact("w1","Ann","keep X")` and `srv.Clear("w1","Bob")`.
  - Assert that `w.controls` yields, in order:
    - `Control{Kind: Interrupt, By: "Ann"}`
    - `Control{Kind: Compact, By: "Ann", Instructions: "keep X"}`
    - `Control{Kind: Clear, By: "Bob"}`
- [ ] **Step 2: Run** `go test ./internal/covemaster -run TestClientDecodesSessionCommands`. Expected: FAIL (compile: undefined `srv.Interrupt`).
- [ ] **Step 3: Implement:**
  1. Edit the proto, then run `just buf-gen`. If buf can't be fetched, see docs/DEVELOPMENT.md.
  2. Add the three server methods.
  3. Add the decode cases in `controlFromPB`.
- [ ] **Step 4: Run** `go test ./internal/covemaster ./internal/jam/attach/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(attach): Interrupt/Compact/Clear control messages`.

### Task 2: Harness and tracker seams

**Files:**
- Modify: `internal/agentrun/harness.go`, `harness_claude.go`, `idle.go`
- Modify: `internal/agentrun/episode_test.go` (fakes)
- Modify: any other `Harness` implementations or test fakes (`grep -rn "EncodeInput(" internal/agentrun`)
- Test: `harness_claude_test.go`, `idle_test.go`

**Interfaces:**
- Produces:
  - `Harness.EncodeInterrupt(requestID string) []byte`
  - Claude encodes it as `{"type":"control_request","request_id":<id>,"request":{"subtype":"interrupt"}}\n`.
  - `(*idleTracker).Busy() bool`: true while a turn is in flight.
  - `(*idleTracker).MarkBusy()`: busy=true without touching `resumeOwed`.
  - `(*idleTracker).Deliver()`: what `Next`'s `actDeliverWake` does (pendingWake=false, busy=true, resumeOwed=true). Use it when the episode loop writes a resume-bearing prompt itself.
- Produces, test fakes: `pipeInput` accepts control requests too and records them on a new `ctrls chan string` (the subtype). Helpers: `nextCtrl(t) string` and `noCtrl(t, d)`. User messages keep going to `msgs` as today.

- [ ] **Step 1: Write failing tests:**
  - `TestClaudeEncodeInterrupt`: unmarshal the bytes; assert `type=="control_request"`, `request_id=="r1"`, `request.subtype=="interrupt"`, and a trailing `\n`.
  - `TestTrackerBusyMarkDeliver`:
    - A new tracker is `Busy()`.
    - After a `result` line it is `!Busy()`.
    - `MarkBusy()` makes it `Busy()` with `ResumeOwed()==false`.
    - With `pendingWake` set (via `Wake()` while busy), `Deliver()` leaves `PendingWake()==false`, `ResumeOwed()==true`, `Busy()`.
- [ ] **Step 2: Run** `go test ./internal/agentrun -run 'TestClaudeEncodeInterrupt|TestTrackerBusyMarkDeliver'`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the methods and update the fakes.
- [ ] **Step 4: Run** `go test ./internal/agentrun`. Expected: PASS (existing tests unaffected).
- [ ] **Step 5: Commit** `feat(agentrun): interrupt encoding and tracker seams`.

### Task 3: Interrupt

**Files:**
- Create: `internal/agentrun/commands.go` (the command box and prompt renderers)
- Modify: `internal/agentrun/workload.go` (`Control`, `episode`)
- Test: `internal/agentrun/commands_test.go`

**Interfaces:**
- Consumes: Task 1 `covemaster.Control{Kind: Interrupt, By}`; Task 2 seams.
- Produces:
  - **`cmdBox`.** Mutex-guarded, with a cap-1 `sig chan struct{}`:
    - `postInterrupt(by string)`, `takeInterrupt() (by string, ok bool)`
    - `postCompact(by, instructions string)`, `takeCompact() (by, instructions string, ok bool)` (two posts collapse; last wins)
    - `postClear(by string)`, `takeClear() (by string, ok bool)`
    - `signal() <-chan struct{}`, `repost()`
  - **`Workload.cmds *cmdBox`** (built in `New`).
  - **`interruptPrompt(by string, rs []covemaster.WakeReason) string`.** Returns the exact acknowledge text (Global Constraints). When `len(rs) > 0` it adds the pending-messages line, then one line per non-squawk reason, rendered as `renderWake` renders them. It never adds `Continue.` or the squawk base prompt.
  - **`Workload.emitCommand(h covemaster.Handle, turn uint32, command, by string)`.** Emits the record line (amendment 5) via `h.Event`. `episode` gains a `turn uint32` parameter to pass it.

**Behaviour in `episode`:**
- `select` on `w.cmds.signal()` while `open`.
- **On an interrupt:**
  - Write `EncodeInterrupt` and set `ackBy` if `tr.Busy() && ackBy == ""` (Task 4 adds `&& !compacting`), using request id `fmt.Sprintf("cove-int-%d", n)` with a per-Workload counter.
  - Otherwise log `agentrun: interrupt ignored` with a `why` attr (`not in a turn` / `compacting` / `already interrupting`).
  - Either way, `emitCommand` runs.
- **At the next turn end:** before the `tr.Next()` switch, when `ackBy != "" && !tr.Busy()`:
  - `write(interruptPrompt(ackBy, w.wake.take()))`
  - `tr.Deliver()`
  - `unhold()`
  - clear `ackBy`, then `continue`.
- **No live episode:** in `awaitWake`, interrupts are taken and dropped with a log.

- [ ] **Step 1: Write failing tests** (streamSpawner):
  - `TestInterruptMidTurnAcks`:
    - Start Run and read the prompt.
    - Control Interrupt `By:"Ann"` → `nextCtrl == "interrupt"`.
    - Emit `{"type":"user",...}` then `{"type":"result","queued_turn_count":0}`.
    - The next stdin message contains `Ann pressed **Interrupt**` and not `New messages may be waiting`.
  - `TestInterruptFoldsCoalescedWakes`:
    - Mid-turn, Wake `squawk` + `alarm "a"`, then Interrupt, then the result.
    - The next message contains the ack text, `New messages may be waiting` and `Alarm "a" fired`.
    - No second message follows within 100ms (`noMessage`).
  - `TestInterruptTwiceOneAck`: two Interrupts mid-turn → exactly one `nextCtrl`, then `noCtrl(100ms)`. After the result, exactly one ack message.
  - `TestInterruptBetweenTurnsDropped`: after the result (with a pending background task, so stdin stays open), Interrupt → `noCtrl` and `noMessage(100ms)`.
  - `TestInterruptRecordsEvent`: the recording Handle's events include a line with `"subtype":"command"`, `"command":"interrupt"` and `"by":"Ann"`.
- [ ] **Step 2: Run** `go test ./internal/agentrun -run Interrupt`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/agentrun`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(agentrun): Interrupt stops the turn and asks for guidance`.

### Task 4: Compact

**Files:**
- Modify: `internal/agentrun/commands.go`, `workload.go`
- Test: `internal/agentrun/commands_test.go`

**Interfaces:**
- Consumes: the Task 3 `cmdBox` (compact), Task 2 `MarkBusy`/`Busy`.
- Produces:
  - `compactText(instructions string) string` returns `"/compact " + instructions`, or `"/compact"` when the instructions are empty.
  - `const compactTimeout = 10 * time.Minute`, a var for tests.

**Behaviour:**
- **Live episode.** Keep `compactPending` (by and instructions) and `compacting bool` in `episode`.
  - On the signal, take the compact into pending.
  - At the decision point, after the ack check and before `tr.Next()`: when pending and `!tr.Busy()`:
    - `write(compactText(...))`
    - `tr.MarkBusy()`
    - `compacting = true`
    - clear pending, `emitCommand`, `continue`.
  - When `compacting && !tr.Busy()`, set `compacting = false`, then fall through to the normal switch (deliver coalesced wake / hold / close).
- **No live episode.**
  - `awaitWake` returns a third outcome. Change its signature to `awaitWake(ctx, timeout) (rs []covemaster.WakeReason, compact *compactReq, ok bool)`, with `compactReq{by, instructions string}`.
  - Run handles a compact by spawning a **maintenance episode**:
    - `Episode{Continued: true, ...}` as usual, with the turn counter incremented.
    - First message `compactText(...)`.
    - **No** `h.Report(Running)`, and no `h.Report(Waiting)` afterwards.
    - `episode(..., maintenance=true)`, which does not read `w.wake.signal()`; wakes stay buffered.
    - Run under a child ctx with `compactTimeout`.
    - Then return to waiting. Buffered wakes resume normally.
  - Skip the maintenance episode (log `agentrun: compact skipped — no conversation`) when a clear is pending (Task 5).
  - Extract the spawn block of Run (connector prepare … `Spawn`) into `func (w *Workload) spawn(ctx context.Context, h covemaster.Handle, turn uint32, continued bool) (Process, *idleTracker, *lineSplitter, error)` so both paths share it.

- [ ] **Step 1: Write failing tests:**
  - `TestCompactMidTurnWaitsForResult`: Compact mid-turn → `noMessage(100ms)`. After the result, the next message == `"/compact keep X"`.
  - `TestCompactThenCoalescedWake`:
    - Mid-turn: Wake squawk, then Compact; then the result → the next message is `/compact keep X`.
    - Emit the compact's `result` → the next message is the resident resume prompt.
  - `TestCompactHoldingWritesNow`: after the result with a background task outstanding (Holding), Compact → the next message is `/compact keep X` straight away.
  - `TestCompactIdleSpawnsMaintenance`:
    - After the episode exits and Waiting is reported, Compact.
    - A new proc spawns with `--continue` in its args, first message `/compact keep X`.
    - No `Running` report is recorded.
    - After its result, stdin closes.
  - `TestInterruptDuringCompactIgnored`: while the boundary `/compact` runs (written, no result yet), Interrupt → `noCtrl(100ms)`.
  - `TestWakeDuringIdleCompactWaits`:
    - During the maintenance proc, Wake squawk → `noMessage(100ms)` on it.
    - After it exits, a new proc spawns whose first message is the resume prompt, and `Running` is reported.
- [ ] **Step 2: Run** `go test ./internal/agentrun -run Compact`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/agentrun`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(agentrun): Compact via native /compact`.

### Task 5: Clear

**Files:**
- Modify: `internal/agentrun/commands.go`, `workload.go` (`Config`, `New`, `Run`)
- Test: `internal/agentrun/commands_test.go`

**Interfaces:**
- Produces:
  - `Config.ClearMarker string`, defaulting to `var defaultClearMarker = "/agent-data/.cove-clear-pending"`. The file's contents are the `by` name.
  - `clearedNotice(by string) string` (exact copy).

**Behaviour:**
- **Clear while a live episode runs:** log it, drop it, `emitCommand` anyway.
- **Clear in `awaitWake`:**
  - Write the marker (0o600) and `emitCommand`. A write error is logged at Warn.
  - Keep waiting: no turn, no report.
  - A Clear queued behind a maintenance episode is handled after it, for free, because maintenance runs synchronously.
- **Before every non-maintenance spawn (and at Run start)**, if the marker exists:
  - `continued = false`.
  - First message: `w.cfg.Prompt + "\n\n" + clearedNotice(by) + "\n\n" + <the wake text that would have been sent>`. At Run start there's no wake text, so it's prompt + notice.
  - This wins over the standing marker (amendment 3).
- **Consuming the marker:** remove it on the first `Reply` of that episode. Chain it into `tr.onReply` next to `markConversation`.

- [ ] **Step 1: Write failing tests:**
  - `TestClearNextEpisodeFresh`:
    - After Waiting, Clear `By:"Bob"`, then Wake squawk.
    - The new proc's args lack `--continue`.
    - Its first message starts with `cfg.Prompt` and contains `cleared at Bob's request` and the resume prompt.
  - `TestClearWhileLiveDropped`: Clear mid-turn → no marker file; the next episode after a Wake has `--continue`.
  - `TestClearSurvivesRestart`:
    - Write the marker by hand.
    - SessionKind `standing`, with the conversation marker present.
    - Run → the first proc lacks `--continue`, and its message contains the notice.
    - After an `assistant` line, the clear marker is gone.
  - `TestClearMarkerKeptIfNoReply`: the fresh episode exits non-zero before any assistant line → the marker still exists.
  - `TestCompactSkippedWhenClearPending`: Clear then Compact while waiting → no maintenance proc spawns within 100ms.
- [ ] **Step 2: Run** `go test ./internal/agentrun -run Clear`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/agentrun`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(agentrun): Clear starts a fresh conversation at the next wake`.

### Task 6: Render the command record

**Files:**
- Modify: `internal/jam/sessionview/view.go` (`build`, `case "cove"`)
- Test: `internal/jam/sessionview/view_test.go` (a table row)

**Interfaces:**
- Consumes: the record line shape (amendment 5).
- Produces: `View` of that line → `Kind: KindPrompt`, `Label: "control"`, `Narrative`/`Summary` = `Interrupt — pressed by Ann` (or `Compact`/`Clear`; the command title-cased).

- [ ] **Step 1: Add a failing table row** `{"command", `{"type":"cove","subtype":"command","command":"interrupt","by":"Ann"}`, KindPrompt, …, "Interrupt — pressed by Ann", "control", false}`, matching the table's columns.
- [ ] **Step 2: Run** `go test ./internal/jam/sessionview`. Expected: FAIL.
- [ ] **Step 3: Implement** (decode `command` and `by` in the env struct).
- [ ] **Step 4: Run** it again. Expected: PASS.
- [ ] **Step 5: Commit** `feat(sessionview): show session-control presses`.

### Task 7: Jam — ops, handler, buttons

**Files:**
- Modify: `internal/jam/userui/session.go` (`SessionOps`, `SessionHead`, `sessionHead`, `sessionAction`)
- Modify: `internal/jam/userui/templates/inbox.html` (session head)
- Modify: `cmd/at-jam/session_ops.go`, `cmd/at-jam/main.go:2690` (pass `rsrv`)
- Create: `internal/jam/sessioncmd.go` (`CompactInstructions` const, and the command kinds)
- Test: `internal/jam/userui/session_test.go`, `cmd/at-jam/session_ops_test.go` (create)

**Interfaces:**
- Consumes: Task 1 `attach.Server` methods.
- Produces:
  - **In `internal/jam`:** `type SessionCommand string` with `CmdInterrupt="interrupt"`, `CmdCompact="compact"`, `CmdClear="clear"`; `func CommandAllowed(c SessionCommand, a Activity) bool` (the enablement table); `const CompactInstructions` (exact copy).
  - **In `userui.SessionOps`:** `Command(ctx context.Context, inst jam.Instance, c jam.SessionCommand, by string) error`.
  - **`SessionHead`** gains `CanInterrupt`, `CanCompact`, `CanClear bool` (`Ops != nil && CommandAllowed(...)`). Shown always when `Ops != nil`; rendered `disabled` when false.
  - **In cmd/at-jam:** `type sessionCommander interface { Interrupt(actorID, by string) bool; Compact(actorID, by, instructions string) bool; Clear(actorID, by string) bool; Connected(actorID string) bool }`. `sessionOps` gains `cmd sessionCommander` and `connectWait time.Duration` (default 30s; poll every 250ms).

**`sessionOps.Command`:**
1. If `inst.Phase == jam.PhaseIdled`, call `sup.Resume(ctx, inst.ActorID)`, then poll `Connected` until true or `connectWait`.
2. Send the command. `false` from the sender, or not connected, returns an error (`no live stream`).

**`sessionAction`:**
- New cases `interrupt|compact|clear`.
- Permission is `sessionFor` only.
- Not `CommandAllowed(inst.Activity)` → 409 `not available while <activity>`.
- `by` = `h.d.Intercom.PartyOf(p.UserID).Label`.
- Success → `HX-Trigger: meChanged` and 204.

**Template:**
- **Interrupt:** a visible `<button class="sh-interrupt" hx-post=".../interrupt" hx-swap="none" {{if not .CanInterrupt}}disabled{{end}}>Interrupt</button>` beside Copy SSH.
- **Compact and Clear:** in the `⋯` menu. Clear gets `hx-confirm` (exact copy).
- **Menu condition:** the menu's `{{if or ...}}` now also opens when `Ops` is set, so these always show.

- [ ] **Step 1: Write failing tests:**
  - `TestSessionCommandsMatrix` (userui, table): for each activity in {running, holding, waiting} × command, the POST gets 204 or 409 per the table. On 204, fakeOps records `(id, cmd, by)`.
  - `TestSessionCommandsAnyViewer`: a non-owner who can see the channel gets 204 on compact (waiting). An outsider gets 404.
  - `TestSessionHeaderCommandButtons`: the waiting head HTML has `>Clear<` without `disabled`, and Interrupt with `disabled`.
  - `TestSessionOpsCommandResumesIdled` (cmd/at-jam): with a fake commander connecting after 2 polls and a stub resumer, `Command` on `PhaseIdled` calls Resume once and then `Clear("w1","Bob")`. It returns an error when never connected (`connectWait` 50ms).
  - `sessionOps` needs Resume through a narrow interface (`resumer interface{ Resume(ctx, id string) error }`) so the test can stub it. Keep the `sup *jam.Supervisor` field for the existing methods.
- [ ] **Step 2: Run** `go test ./internal/jam/userui ./cmd/at-jam -run 'SessionCommand|SessionOpsCommand|CommandButtons'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `just test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): Interrupt/Compact/Clear session buttons`.

### Task 8: Docs

**Files:**
- Modify: `docs/usage/jam/session-view.md` (Header table: the three rows: who, when enabled, what it does)
- Modify: `docs/usage/jam/coves.md` §cove-master (the controls; the maintenance episode; `/agent-data/.cove-clear-pending`)
- Modify: `docs/TODO.md` (drop the context-lifecycle item, or narrow it to auto-policies)
- Modify: the spec's **Status** line → `built — <PRs>`, plus the five amendments above under a "Planning amendments" heading
- Modify: `docs/superpowers/plans/` index, if one exists

- [ ] **Step 1: Edit the docs** (docs-author skill: one owner per fact, links not copies).
- [ ] **Step 2: Run** the docs-audit skill's checker. Expected: no new findings.
- [ ] **Step 3: Commit** `docs: session controls`.
