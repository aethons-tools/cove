# Session mission Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each session carries a one-line **mission** that the agent sets with a new `mission` tool. Ticket sessions start with the ticket's title as their mission. The mission shows as a muted second line under the name in the `/app/user` rail and in the session header, and Clear resets it.

**Architecture:**
- **Data:** `jam.Instance.Mission` is persisted through the JSON instance document, so no migration is needed. `Supervisor.SetMission` writes it, after normalisation by `jam.NormalizeMission`.
- **The agent's tool:** cove-master's MCP tool `mission` calls `PUT /mission`. A self-scoped `jam.MissionHandler` sets the caller's own mission.
- **Display:** the rail and header read the mission from the backing active session.

**Tech Stack:** Go, MCP go-sdk, html/template + htmx.

**Spec:** [docs/superpowers/specs/2026-10-26-session-mission-design.md](../specs/2026-10-26-session-mission-design.md)

## Global Constraints

- **Workflow:** TDD with hermetic tests (`just test`). Docs are updated in the same change.
- **Never touch `unit` semantics.**
- **Normalisation:** trim, then collapse every whitespace run (newlines included) to one space. Cap at **120 runes**: longer text is cut to 119 runes plus `…`. An empty result clears the mission.
- **Tool name** `mission`, one argument `text`. Agent-facing description, verbatim: `Set your current mission: one line saying what you are focused on now, shown beside your name in people's inbox. Set it when your focus changes (a new task, a ticket picked up); empty text clears it.`
- **Boilerplate line**, verbatim: ``- Mission: when your focus changes (a new task, a ticket picked up), set a one-line `mission` saying what you are on — people see it beside your name.`` The core must stay within `BudgetBoilerplate`.
- **Standing restart prompt:** `standingRestartPrompt` gains the sentence ``Set your `mission` again once you know your current focus.``
- **Endpoint:** `PUT /mission` with body `{"text": "..."}`, capped at 2048 bytes. Answers 204. Auth and self-scope as in `/escalate`.

## Review Focus

1. **Multi-line or very long mission text** → the stored mission is one line of 120 runes ending in `…`, and it never errors (Task 1 `TestNormalizeMission`).
2. **A ticket channel with two live sessions** → shows the mission of the most recently raised one (Task 4 `TestChannelMissionTicketLatest`).
3. **A Gone session's channel** → no mission, never a stale one (Task 4 `TestChannelMissionInactiveHidden`).
4. **A DM with one person and one session** → shows that session's mission. A room never shows one (Task 4 `TestChannelMissionChatAndRoom`).
5. **Clear where `SetMission` fails** → the press still succeeds and the failure is logged (Task 5 `TestSessionOpsClearResetsMission`).

---

### Task 1: Data, normalisation, setter, raise copy

**Files:**
- Modify: `internal/jam/instance.go` (add `Mission string \`json:"mission,omitempty"\``)
- Create: `internal/jam/mission.go` (`NormalizeMission`, `MissionMaxRunes = 120`)
- Modify: `internal/jam/supervisor.go`. `RaiseSpec` gains `Mission string`; Raise copies `spec.Mission` (normalised) onto the new Instance; add `SetMission`, next to `SetEscalationCategory`.
- Modify: `internal/dispatcher/dispatcher.go` (RaiseSpec gets `Mission: iss.Title`)
- Test: `internal/jam/mission_test.go`, `internal/jam/supervisor_test.go`, `internal/dispatcher/dispatcher_test.go`

**Interfaces:**
- Produces:
  - `func NormalizeMission(s string) string`
  - `func (s *Supervisor) SetMission(actorID, mission string) error`. It normalises, then follows `SetEscalationCategory`'s locking pattern, and returns an error when there is no instance.
  - `RaiseSpec.Mission`.

- [ ] **Step 1: Write failing tests:**
  - `TestNormalizeMission`, table-driven:
    - `"  fix  the\n\ttests "` → `"fix the tests"`
    - `""` and `" \n "` → `""`
    - 130 × `"é"` → 119 × `"é"` + `"…"` (rune count 120)
    - exactly 120 runes → unchanged
  - `TestSupervisorSetMission`:
    - After raising a session, `SetMission(id, " a\nb ")` stores `"a b"`.
    - An unknown id returns an error.
  - `TestRaiseCopiesMission`: `RaiseSpec{Mission: "Ticket title"}` → the instance has `Mission == "Ticket title"`.
  - Dispatcher test: the raised spec carries `Mission` == the issue's `Title`.
- [ ] **Step 2: Run** `go test ./internal/jam ./internal/dispatcher -run 'Mission|Raise'`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/jam ./internal/dispatcher`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): session mission field and setter`.

### Task 2: `PUT /mission` endpoint and the cove-master tool

**Files:**
- Create: `internal/jam/mission_handler.go`, modelled on `escalate_handler.go` (narrow store and setter interfaces)
- Modify: `cmd/at-jam/mux.go`. `squawksMux` gains a `missionH` param and `case "/mission"`. `coveHTTPHandler` builds `jam.NewMissionHandler(st, sup, log)` alongside `escH` and logs the mount.
- Modify: `internal/jam/setup_config.go` (`IntercomTools` gains `"mission"`, keeping the list sorted)
- Modify: `cmd/cove-master/mcp.go`. Add input struct `missionIn{Text string \`json:"text" jsonschema:"one line: what you are focused on now; empty clears it"\`}`, a client method `mission(ctx, text) error` that sends `PUT /mission`, and `mcp.AddTool` with the exact description.
- Modify: `cmd/cove-master/mcp_test.go` (tool-name list)
- Test: `internal/jam/mission_handler_test.go`, `cmd/cove-master/mcp_test.go`, any `cmd/at-jam/mux_test.go` covering routing

**Interfaces:**
- Consumes: Task 1 `SetMission`.
- Produces: `func NewMissionHandler(store missionStore, setter missionSetter, log *slog.Logger) *MissionHandler`. The handler:
  - accepts only `PUT` (405 with `Allow: PUT` for anything else);
  - returns 401 for a missing or unknown token, 403 when there is no instance, 413 when the body is over 2048 bytes, and 400 for bad JSON;
  - otherwise calls `setter.SetMission(actor.ID, req.Text)` and returns 204, or 500 with a log on a setter error.

- [ ] **Step 1: Write failing tests:**
  - `TestMissionHandler` (table):
    - PUT with a valid token and `{"text":"x"}` → 204, and the setter got `(actorID, "x")`.
    - POST → 405.
    - No token → 401; an unknown token → 401; no instance → 403.
    - A 3000-byte body → 413; bad JSON → 400.
  - A mux routing test: `/mission` reaches `missionH`.
  - A cove-master test: the tool list includes `mission`. Calling it sends `PUT /mission` with `{"text":"…"}`, following how the existing `end`/`report` client tests use an httptest server.
- [ ] **Step 2: Run** `go test ./internal/jam ./cmd/at-jam ./cmd/cove-master -run 'Mission|IntercomTools|Tools'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/jam ./cmd/at-jam ./cmd/cove-master`. Expected: PASS, including `TestIntercomToolsMatchJam`.
- [ ] **Step 5: Commit** `feat: mission tool and PUT /mission`.

### Task 3: Telling agents — boilerplate and the standing restart prompt

**Files:**
- Modify: `internal/jam/sessionctx/boilerplate.go` (append the exact line to `lines`, after the Intercom line, for every kind)
- Modify: `internal/agentrun/workload.go` (`standingRestartPrompt` gains the exact sentence)
- Test: `internal/jam/sessionctx/boilerplate_test.go`, and the agentrun test that asserts the restart prompt, if one exists (grep `standingRestartPrompt`)

- [ ] **Step 1: Write a failing test.** `TestBoilerplateMissionLine`: for each kind (standing, personal, ephemeral with and without Unit), `Core` contains the exact line and `len(Core) <= BudgetBoilerplate`.
- [ ] **Step 2: Run** `go test ./internal/jam/sessionctx -run Mission`. Expected: FAIL.
- [ ] **Step 3: Implement** both edits. Update any golden or expected text the change breaks.
- [ ] **Step 4: Run** `go test ./internal/jam/sessionctx ./internal/agentrun`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(sessionctx): tell agents to set their mission`.

### Task 4: Rail and header show the mission

**Files:**
- Modify: `internal/jam/channels.go` (`ChannelView.Mission string`)
- Modify: `internal/jam/channel_views.go` (`userChannel` resolves the mission)
- Modify: `internal/jam/userui/userui.go` (`ChannelRow.Mission`, copied in the rail grouping)
- Modify: `internal/jam/userui/session.go` (`SessionHead.Mission = inst.Mission`)
- Modify: `internal/jam/userui/templates/inbox.html` (rail row and session head)
- Modify: `internal/jam/userui/templates/layout.html` (CSS)
- Test: `internal/jam/channel_views_test.go` (or wherever `UserChannels` is tested), `internal/jam/userui/userui_test.go`, `internal/jam/userui/session_test.go`

**Interfaces:**
- **Mission resolution in `userChannel`**, from the members it already walks. Only sessions with `instanceActive(inst)` count; Idled is active.
  - **`SourceSession`:** the mission of the instance whose ActorID is `ch.Key`, if it is active.
  - **`SourceTicket`:** the active member session with the latest `RaisedAt`.
  - **`SourceChat`:** the mission only when exactly one active session is among the members.
  - **Anything else** (rooms): `""`.
- **Template, rail:** the row's `<span class="cove">` and a `<span class="mission" title="{{.Mission}}">{{.Mission}}</span>` sit inside `<span class="stack">`. The mission span renders only when it is non-empty.
- **CSS:** `.thread .stack{display:flex;flex-direction:column;min-width:0;flex:1}` and `.thread .mission{font-size:12px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}`. Move `flex:1` from `.cove` to `.stack`. Use the theme's existing muted-colour variable name.
- **Template, header:** in the identity block, `{{if .Mission}}<div class="sh-mission" title="{{.Mission}}">{{.Mission}}</div>{{end}}` under the name, styled like `.thread .mission`.

- [ ] **Step 1: Write failing tests:**
  - `TestChannelMissionSession`: a session channel whose session has `Mission "m"` → `v.Mission == "m"`.
  - `TestChannelMissionTicketLatest`: a ticket channel with two active sessions → the mission of the later-raised one.
  - `TestChannelMissionInactiveHidden`: the session is Gone → `""`.
  - `TestChannelMissionChatAndRoom`:
    - a chat with the viewer and one session → that mission;
    - a chat with two sessions → `""`;
    - a room → `""`.
  - Rail render test: the HTML has `class="mission"` with the text, and a row without a mission has no `class="mission"`.
  - Header render test: `sh-mission` with the text appears, and is absent when the mission is empty.
- [ ] **Step 2: Run** `go test ./internal/jam ./internal/jam/userui -run 'Mission'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/jam/... `. Expected: PASS, including the existing browser and styles tests (`styles_test.go`, `titlebar_parity_test.go`). If a browser test is skipped for lack of a browser, say so in the report.
- [ ] **Step 5: Commit** `feat(userui): show each session's mission in the rail and header`.

### Task 5: Clear resets the mission

**Files:**
- Modify: `cmd/at-jam/session_ops.go`. `sessionOps` gains `mission missionSetter` (`interface{ SetMission(actorID, mission string) error }`). After a successful `CmdClear` send, call `o.mission.SetMission(inst.ActorID, "")`. On error, log at Warn (via the existing logger, or add a `log *slog.Logger` field if `sessionOps` has none) and still return nil.
- Modify: `cmd/at-jam/main.go` (wire `mission: sup`)
- Test: `cmd/at-jam/session_ops_test.go`

- [ ] **Step 1: Write a failing test.** `TestSessionOpsClearResetsMission`:
  - A Clear that is sent → the stub setter got `(id, "")`.
  - Interrupt and Compact → the setter is not called.
  - A Clear whose send fails (no stream) → the setter is not called, and an error is returned.
  - The setter returns an error → `Command` still returns nil.
- [ ] **Step 2: Run** `go test ./cmd/at-jam -run SessionOpsClear`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./cmd/at-jam`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): Clear resets the session's mission`.

### Task 6: Docs

**Files:**
- `docs/usage/jam/intercom.md`: the `mission` tool, in the tool list and in "What the tools do", with the endpoint and normalisation.
- `docs/usage/jam/session-view.md`: the Header identity shows the mission; the Clear row says it resets the mission.
- `docs/usage/jam/intercom-ui.md`: rail rows show the mission line, and which rows get one.
- `docs/usage/jam/session-context.md`: the boilerplate table row.
- `docs/usage/jam/requisitioner.md`: a ticket's title becomes the starting mission.
- The spec's Status line → `built — PR pending`.

- [ ] **Step 1: Edit** with the docs-author skill: one owner per fact, links not copies, `updated:` set to today.
- [ ] **Step 2: Run** the docs-audit checker. Expected: no new findings beyond the existing baseline.
- [ ] **Step 3: Commit** `docs: session mission`.
