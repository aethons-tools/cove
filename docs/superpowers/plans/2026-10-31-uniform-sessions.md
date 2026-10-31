# Uniform sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every Jam session behaves the same way whatever started it: home is its own session channel, it waits rather than exits, it is never reaped for waiting, and a crashed session is re-raised with its state.

**Architecture:** Mostly deletions of kind/ticket branches in `internal/jam` (intercom), `internal/wakeon`, `internal/agentrun`, `internal/dispatcher`, prompts and the `board-execute` skill. One addition: a `recovering` phase on `Instance` that the Supervisor's reconcile loop re-raises under the same actor id onto the same manager, with persisted state volumes for every kind.

**Tech Stack:** Go 1.x, hermetic tests (`just test` / `go test ./...`; in this sandbox `GOPROXY=https://proxy.golang.org,direct`).

**Spec:** `docs/superpowers/specs/2026-10-31-uniform-sessions-design.md`

## Global Constraints

- Personal sessions keep owner-only addressing and invite-only channels (spec non-goal).
- Ticket state still moves through `report`; a ticket session stays a **member** of its ticket channel (issue comments reach and wake it).
- No default idle timeout is added for any role.
- Final teardown marks an unfinished ticket blocked; recoverable teardown (Lost, Done-without-end) never does.
- Re-raise: backoff 30s doubling to 30m; give up after 5 failed attempts or 2h since the crash → final teardown + escalation notice.
- Sessions raised before this change (no `RaisePrompt` recorded) are not re-raised.
- Every task updates the docs that own its behaviour (`docs/usage/jam/…`) in the same commit (AGENTS.md rule).
- Template payload (`images/…/image-files/…`) is the studio's side of the line; the `board-execute` skill lives there.

## Review Focus

1. A Linear comment on a ticket whose session's home is now its session channel must still wake the session (ticket-channel membership kept) — Task 2 test.
2. `send to=ticket:<own key>` must post to the ticket channel, not the new home — Task 2 test.
3. The standing state sweep must not purge the volumes of a live or recovering non-standing session — Task 6 test.
4. A recovering session must not be double-dispatched by the Requisitioner nor release its allocator slot — Task 6 test.
5. An operator End on a ticket session (final) still marks the ticket blocked when there is no terminal report — Task 4 test.

---

### Task 1: Prompts — no worker is told to exit

**Files:**
- Modify: `internal/jam/sessionctx/boilerplate.go` (default case, `structuredTicket`)
- Modify: `internal/dispatcher/dispatcher.go:66-73` (`turnEndProtocol`)
- Modify: `internal/agentrun/workload.go:33-46,240-248` (`resumePrompt`, `resumeText`)
- Modify: `images/jam-studio-base/**/skills/board-execute/SKILL.md`
- Test: `internal/jam/sessionctx/boilerplate_test.go`, `internal/agentrun/workload_test.go`, `internal/dispatcher/dispatcher_test.go`
- Docs: `docs/usage/jam/turn-end.md` (reporting a ticket), `docs/usage/jam/requisitioner.md` (flow step 6)

- [ ] **Step 1: Failing tests**
  - `TestBoilerplateEphemeralTicketOwnChannel`: Kind ephemeral, Unit "COV-1" → core contains the `ownChannel` sentence, contains "`report`", contains "only once your work has landed", does **not** contain "posts to your ticket" nor "as your last action once it is finished"; structured variant does not contain "a comment on your ticket".
  - `TestResumeTextEphemeralNeutral`: ephemeral, non-resident workload → `resumeText()` does not contain "ticket" unconditionally (contains "`read`" and "own channel").
  - `TestTurnEndProtocolNoExitOnQuestion`: `turnEndProtocol` contains "`call_in`" and does not contain "ask with `send` and end your turn".
- [ ] **Step 2:** run, expect FAIL.
- [ ] **Step 3: Implement**
  - Boilerplate default case: `comms = ownChannel` (trimmed) for every ephemeral session; if `f.Unit != ""` append to turns: "- Your ticket: keep its state current with `report` (in-review with the PR link, needs-input, blocked, done). Call `end` only once your work has landed (merged, or the PR handed off) and the ticket is reported; if you cannot land it, report needs-input or blocked, say why in your channel, `call_in` whoever can help, and wait — escalation brings people to you." Drop `structuredTicket`.
  - `turnEndProtocol`: replace last two bullets with the same landing rule and "If you need a person, `call_in` them (or `send` to them) and end your turn; their reply wakes you. Never end with work that is not pushed."
  - `resumePrompt` = "A message may have arrived — use the intercom `read` tool to fetch new messages, then continue. `send` without `to` posts to your own channel. If you have a ticket, keep it current with `report`."
  - `board-execute`: replace "finish with `end`…" with the landing rule; add "talk in your session channel; `call_in` people".
- [ ] **Step 4:** tests pass; `go test ./internal/jam/sessionctx/ ./internal/agentrun/ ./internal/dispatcher/`.
- [ ] **Step 5:** commit `feat(prompts): sessions wait instead of exiting; ticket wording only for ticket sessions`.

### Task 2: Home channel is the session channel for every session

**Files:**
- Modify: `internal/jam/intercom_post.go` — `HomeChannel`, `SetUp`, `Ended`, `Notify`, `homeWithMember`, `resolveTicket` own-ticket branch, `MayReach`
- Modify: `internal/jam/intercom_escalate.go:88-92`
- Test: `internal/jam/intercom_post_test.go`, `internal/jam/intercom_session_test.go`, `internal/jam/intercom_escalate_test.go` (update existing ticket-home expectations)
- Docs: `docs/usage/jam/intercom.md` (home channel), `comms-addressing.md` (`user:` and `ticket:` rows), `escalation.md`

**Interfaces:**
- Produces: `func (ic *Intercom) HomeChannel(inst Instance) (Channel, error)` — always the session channel. `func (ic *Intercom) ownTicketChannel(inst Instance) (Channel, bool, error)` — joins inst to its ticket channel (create=true) when it has one.

- [ ] **Step 1: Failing tests**
  - `TestIntercomTicketSessionHomeIsSessionChannel`: raise ticket inst (tracker set) → `HomeChannel` kind `SourceSession`; `SetUp` also made inst a member of the ticket channel.
  - `TestIntercomTicketSessionHearsIssueComments`: a trusted post into the ticket channel appears in the session's inbox.
  - `TestIntercomOwnTicketSendGoesToTicket`: `Plan(p, "ticket:COV-1")` from the COV-1 session → ticket channel.
  - `TestIntercomTicketSessionUserSendJoinsHome`: user X in session channel; `Plan("user:X")` → session channel (not a chat).
  - `TestIntercomEndedTicketSessionLeavesBoth`: `Ended` → not a member of ticket channel; session channel archived.
  - `TestIntercomEscalateTicketSessionIntoSessionChannel`: escalation notice in session channel; body lacks "its issue included".
- [ ] **Step 2:** run, expect FAIL.
- [ ] **Step 3: Implement** — `HomeChannel` returns `sessionChannel(inst)`; `SetUp` calls `HomeChannel` then `ownTicketChannel` (ignore ok=false); `Ended` leaves the ticket channel (if any) **and** archives the session channel; `Notify` gone path tries own session channel first (already does); `homeWithMember` drops the `Unit` guard; `resolveTicket` `own` case returns `ownTicketChannel`; `MayReach` removes `inst.Unit != "" ||`; escalation drops the `SourceTicket` subject/after branch.
- [ ] **Step 4:** `go test ./internal/jam/... ./cmd/jam/...` — fix tests that asserted ticket-as-home (update expectation, not behaviour).
- [ ] **Step 5:** commit `feat(intercom)!: every session's home is its own session channel`.

### Task 3: Addressing — no extra `ticket:` gate to reach a ticket session

**Files:**
- Modify: `internal/jam/decide.go:241,265-282` (delete `sessionTicketAllowed`), `internal/jam/intercom_post.go:828-834`, `internal/jam/intercom_membership.go:150`
- Test: replace `TestIntercomSessionAddressToATicketNeedsTicketAddressing` with `TestIntercomSessionAddressReachesTicketSession` (session: glob alone → session channel); `list_targets` lists ticket sessions with only `session:` globs.
- Docs: `comms-addressing.md`

- [ ] Steps 1–5 as usual; commit `feat(intercom): reaching a ticket session needs only session: addressing`.

### Task 4: Waiting and final teardown

**Files:**
- Modify: `internal/wakeon/wakeon.go:376-389` (remove the wait-max branch), `Config.MaxWait`/`defaultMaxWait`; `cmd/jam/config.go:210`, `cmd/jam/main.go:2578` (warn "wait-max is deprecated and ignored" when set)
- Modify: `internal/jam/supervisor.go` — new `SetTicketCloser(TicketCloser)`; `Teardown` marks unfinished ticket blocked (reason "ended") for **final** teardowns
- Modify: `internal/jam/userui` / admin End path → reason "ended by operator"
- Test: `internal/wakeon/wakeon_test.go`, `internal/jam/supervisor_test.go`
- Docs: `intercom.md` (wait-max), `turn-end.md` (blocked on end), `serve.md` config table

**Interfaces:**
- Produces: `type TicketCloser interface { BlockUnfinished(ctx context.Context, inst Instance, reason string) error }` moved into `internal/jam` (wakeon's interface aliases it). `func (s *Supervisor) TeardownFinal(ctx context.Context, actorID, reason string) error` — blocks an unfinished ticket (bounded 15s, best effort, records the report) then `Teardown`.

- [ ] **Step 1: Failing tests**
  - `TestWakeonEphemeralNeverReapedForWaiting`: ephemeral inst Waiting 2h, no idle deadline → no Teardown.
  - `TestSupervisorTeardownFinalBlocksUnfinishedTicket`: ticket inst, no report → closer called with reason; terminal report → not called.
- [ ] **Step 2–4:** implement; wakeon's end/idle paths and operator End call `TeardownFinal`; Reconcile's Lost/Done keep plain `Teardown` (Task 6 makes them recoverable).
- [ ] **Step 5:** commit `feat!: no session is reaped for waiting; final teardowns block unfinished tickets`.

### Task 5: Bugs fixed alongside

**Files / tests (one commit each):**
- Admin raise takes a grant: `internal/jam/standing_sessions.go:156` `RaiseManual` → `Grant(ephemeral)`; denied → 409. Test `TestRaiseManualTakesGrant`.
- Admin raise `--unit` claims the ticket (In Progress) via the tracker when the unit is a tracker key. Test in `cmd/jam`.
- `report` checks `TicketUnit`: `internal/jam/report.go:120` → 400 for free-text unit. Test `TestReportFreeTextUnit400`.
- Ticket-channel asks answerable by any project member who can see the channel: `internal/jam/intercom_asks.go:165-172`. Test `TestAskInTicketChannelAnswerableByProjectMember`.
- Docs: `coves.md` (manual raise cap), `turn-end.md`, `asks.md`.

### Task 6: Re-raise with persisted state for every kind

**Files:**
- Modify: `internal/jam/instance.go` — `PhaseRecovering Phase = "recovering"`; fields `RaisePrompt string`, `Recovery *Recovery`
- Create: `internal/jam/recovery.go` — `type Recovery struct{ Since, Next time.Time; Attempts int; Reason string }`; `func (s *Supervisor) recoverable(inst Instance) bool`; `func (s *Supervisor) markRecovering(ctx, inst, reason)`; `func (s *Supervisor) reraise(ctx, inst) error`
- Modify: `internal/jam/supervisor.go` — `Raise` stores `RaisePrompt`; `Reconcile` Lost/Done paths call `markRecovering` when `recoverable`; Reconcile drives due recoveries
- Modify: `internal/jam/launcher/launcher.go:278` — `stateMounts` for every kind
- Modify: `internal/agentrun/workload.go:330,505` — `persists` for every kind (conversation marker); restart prompt generalised (`restartPrompt`)
- Modify: `internal/standing/standing.go` `sweep` — skip ids with a non-Gone instance
- Modify: all `Phase != PhaseGone` "is live" sites that must exclude recovering (wakeon tick, escalation, UI live lists, `MayReach`, Notify) — audit list in the commit message
- Test: `internal/jam/recovery_test.go`, `internal/standing/standing_test.go`, `internal/agentrun/workload_test.go`, `internal/jam/launcher/launcher_test.go`
- Docs: `coves.md` (lifecycle), `requisitioner.md` (dedup), `standing-sessions.md` (sweep), `studio-manager.md` (volumes)

**Behaviour:**
- `recoverable(inst)`: `inst.RaisePrompt != ""` (raised after this change) and `inst.EndRequested == nil`.
- `markRecovering`: tears the studio down via the launcher (volumes kept), revokes the actor, keeps the Instance with `Phase=recovering`, `Activity=""`, `Recovery{Since: now, Next: now+30s}`; does **not** call `channels.Ended` or `RecordRelease`.
- `reraise`: `Raise(RaiseSpec{ActorID, Project, Role, Unit, Mission, Owner, OwnerID, Name, SessionKind, Prompt: RaisePrompt, Placement.Managers: [inst.Manager] if still configured})`, then restores `CommitSeq/CommitCursor`, `Focus`, `Report`, `Alarms`. Failure → `Attempts++`, `Next = now + min(30s<<(Attempts-1), 30m)`. Give up when `Attempts >= 5` or `now-Since >= 2h` → `TeardownFinal(reason "re-raise gave up")` + post an escalation-style notice in its channel.
- Requisitioner dedup (`working`) already treats non-Gone as working → recovering counts.

- [ ] **Step 1: Failing tests**
  - `TestReconcileLostTicketSessionRecovers`: Probe dead → instance phase recovering, closer not called, release not recorded.
  - `TestRecoveringReraisedSameIDAndManager`: after Next → launcher RaiseOn called with same ActorID, Placement=[old manager]; CommitSeq preserved.
  - `TestRecoveryGivesUpAfterFiveFailures`: launcher fails 5× → final teardown, closer called with "re-raise gave up", release recorded.
  - `TestRecoveringLegacyInstanceTornDownFinal`: RaisePrompt "" → today's teardown.
  - `TestStandingSweepSkipsLiveSessions`: owner id with a live ephemeral instance is not purged; a gone one is.
  - `TestStateMountsEveryKind`: ephemeral raise gets both mounts.
  - `TestWorkloadResumesConversationEveryKind`: marker present, ephemeral → restart prompt.
- [ ] **Step 2–4:** implement and run `go test ./...`.
- [ ] **Step 5:** commit `feat: crashed sessions of every kind are re-raised with their state`.

### Task 7: UI — same controls for every kind

**Files:** `internal/jam/userui/session.go:98-123,205-226`; web app session actions if they gate on kind (`web/app/…`).
- End: operator, or the session's starter (personal: shown as Release). Restart: operator, every kind (teardown → recovering re-raise for non-standing).
- Test: `internal/jam/userui/session_test.go` — ticket session exposes Restart for operators.
- Docs: `docs/usage/jam/intercom-ui.md` / `web-app.md` rows.
- Commit `feat(ui): same session controls for every kind`.

### Task 8: Whole-branch verification

- [ ] `go build ./... && go vet ./... && go test ./...` (note the known flaky `TestClearNextEpisodeFresh`).
- [ ] docs-audit checker passes.
- [ ] Spec status line updated to "built".
