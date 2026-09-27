# Session kinds, Slice 3: personal sessions — the long-lived conversation

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A personal session becomes a conversation that lasts until its owner releases it. The cove works its first prompt, messages its owner over the intercom (Discord), waits, wakes when the owner replies, continues with `claude --continue`, and repeats. It is never torn down for waiting too long. The intercom machinery this needs runs whether or not a dispatcher is configured, for every project with a Discord chat service.

**Architecture:**
- **Resident cove loop.** `agentrun` gains a resident mode (`AT_COVE_RESIDENT=1`): after every turn it reports Waiting and blocks until a Wake or shutdown, with no MaxWait. The launcher sets it for personal sessions.
- **Harbor treats personal coves as long-lived.** Wake-on skips its wait-max teardown for them (it still pauses them when idle), and escalation skips coves with no ticket.
- **The cove can talk to its owner, and only its owner.** `Supervisor.Raise` gives a personal cove an addressing override of just `human:<owner>`, and a send with no `to` from a ticketless cove defaults to its owner.
- **Intercom wiring leaves the dispatcher block.** The squawks endpoint, wake-on and the Discord relay are built whenever harbor has an intercom log, and Discord routing covers every Discord-enabled project.

**Tech Stack:** Go 1.26, `slog`, `just`. Spec: [`../specs/2026-09-26-session-kinds-standing-personal.md`](../specs/2026-09-26-session-kinds-standing-personal.md). Builds on session-kinds Slice 2 (merged: login-linked Humans, personal caps, owner + `SessionKind` on Instances, `session request|list|release`).

## Global Constraints

- **Ephemeral / dispatcher behavior unchanged.** Dispatcher coves still end on `ok`, wait at most `MaxWait` on `needs-input`, get torn down after `wait-max`, and escalate on their ticket. Existing `runtime.dispatcher` wake settings keep working.
- **Personal ⇒ resident.** The trigger is `SessionKind == "personal"` on the RaiseSpec/Instance; don't add a separate flag to the admin API.
- **Least privilege for the cove's messages.** The personal cove's grant override sets `Addressing` to exactly `["human:<owner>"]` (overrides replace the role's addressing).
- **Personal sessions need Discord.** Messages to a ticketless cove's owner can only be delivered via Discord (the Linear fallback needs a ticket). The request handler refuses (400, with a clear message) if the project's chat service isn't Discord or the owner has no Discord delivery profile — fail at request time, not silently later.
- **v1 limit, documented:** the owner can only *reply* to the cove's messages (Discord reply routing); they can't start a new thread. The cove always speaks first.
- **Wake settings location:** a new top-level `runtime.wake` block (`poll-interval`, `wait-max`, `warm-timeout`). Precedence: `runtime.wake` > the existing `runtime.dispatcher` wake fields > engine defaults.
- **Docs in the same change.** TDD. Stage files by path (untracked `.switchboard/`). Each commit builds. End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Resident cove loop

**Files:** `internal/agentrun/workload.go` (+tests), `cmd/cove-master/main.go` (`buildAgentConfig`, +tests), `internal/harbor/launcher/launcher.go` and the cove-master launch path it uses (`internal/connect/covemaster.go`) (+tests), the cove-master MCP `send` tool description (`cmd/cove-master/mcp.go`).

- `agentrun.Config.Resident bool`. In resident mode, after **every** turn — worker-result `ok`, `needs-input`, `error`, or missing/unreadable — log the outcome, report Waiting, and block on `wake` or `ctx.Done()` only (no MaxWait timer). A Wake resumes with `--continue` and a resident resume prompt:

  ```go
  const residentResumePrompt = "Your owner may have replied — use the intercom `read` tool to fetch new messages, then continue. " +
  	"Use `send` to message your owner when you have results or need input."
  ```
  Shutdown (ctx cancelled by a Teardown control) returns `ctx.Err()`. Non-resident behavior is unchanged.
- `cmd/cove-master`: `AT_COVE_RESIDENT=1` (or `true`) sets `Resident`.
- Launcher: when `spec.SessionKind == "personal"`, pass `AT_COVE_RESIDENT=1` into the cove-master environment alongside the existing `AT_COVE_*` vars.
- MCP `send` description: omitting `to` messages "this cove's default recipient — its ticket, or its owner for a personal session".

- [ ] **Tests (fail first):** resident `ok` → Waiting, not Done; resident loop resumes on Wake with `residentResumePrompt` and `--continue`; resident waits past what would be MaxWait (inject a short MaxWait and assert it doesn't return); resident + missing worker-result → Waiting; ctx cancel ends the loop; non-resident unchanged. Env parsing. Launcher sets the env var only for personal.
- [ ] **Commit** — `agentrun: resident mode for personal sessions (session-kinds slice 3)`

---

## Task 2: Wake-on and escalation leave personal coves alone

**Files:** `internal/wakeon/wakeon.go` (+tests), `internal/escalate/escalate.go` (+tests).

- Wake-on: skip the wait-max teardown when `inst.SessionKind == "personal"`. Keep everything else (pause after warm-timeout, resume + wake on an external reply).
- Escalation: skip instances with `Unit == ""` (no ticket to escalate on). This also stops the per-tick warnings for ticketless coves.

- [ ] **Tests (fail first):** a personal Instance waiting longer than MaxWait is not torn down, but is still idled after warm-timeout and woken on a reply; an ephemeral one past MaxWait still is torn down. Escalation ignores a Waiting instance with no Unit.
- [ ] **Commit** — `wakeon, escalate: leave personal sessions to their owner (session-kinds slice 3)`

---

## Task 3: A personal cove can message its owner — and only its owner

**Files:** `internal/harbor/supervisor.go` (`Raise`) (+tests), `internal/harbor/squawks.go` (`handlePost`) (+tests).

- `Supervisor.Raise`: when `spec.Owner != ""`, enroll with `&Override{Addressing: []string{"human:" + spec.Owner}}` instead of `nil`.
- `handlePost` default target when `to` is omitted: the ticket channel as today when `inst.Unit != ""`; else `human:<inst.Owner>` when `inst.Owner != ""`; else **400** `"no default recipient: pass \"to\""` (today this path fails with a 502 on an empty channel ref).

- [ ] **Tests (fail first):** a personal cove's actor has the owner-only addressing override; its `send` without `to` goes to `human:<owner>`; its `send` to another human is refused by the existing addressing check; a ticketless, ownerless cove gets 400; a dispatcher cove is unchanged.
- [ ] **Commit** — `harbor: personal coves message their owner by default, and only them (session-kinds slice 3)`

---

## Task 4: Request-time checks and the personal-session preamble

**Files:** `internal/harbor/sessions.go` (+tests).

- Before granting: the project's chat service must be Discord and the owner must have a Discord delivery profile (`Human.DeliveryFor("discord")` or the existing equivalent). Otherwise **400** with what's missing, e.g. `"personal sessions need project acme's chat service set to discord (project chat-service set …)"` / `"alice has no discord delivery profile (project roster add-human … --delivery discord:<inbox-channel>)"`.
- Prefix the owner's prompt with a short preamble so the agent knows how the session works:

  ```text
  You are a personal session for <owner>. Work on the request below. When you have results or need
  input, message <owner> with the intercom `send` tool (omit `to`); they will reply, and you will
  be resumed with their reply available via `read`. This session stays open until <owner> releases it.
  ---
  <owner's prompt>
  ```

- [ ] **Tests (fail first):** non-Discord project → 400 (no grant attempted); owner without a Discord profile → 400; the raised spec's prompt starts with the preamble and contains the owner's prompt.
- [ ] **Commit** — `harbor: check personal-session delivery at request time (session-kinds slice 3)`

---

## Task 5: Run the intercom without a dispatcher

**Files:** `cmd/at-harbor/main.go`, `cmd/at-harbor/config.go` (+validation/tests), `cmd/at-harbor/relay_linear.go` (the `directory`) and `relay_discord.go` as needed (+tests).

- **Config:** add `runtime.wake { poll-interval, wait-max, warm-timeout }`. Resolve each field: `runtime.wake` if set, else the matching `runtime.dispatcher` field, else the engine default. Add it to the known-keys set so it isn't flagged as unknown.
- **Move out of `if dc := cfg.Runtime.Dispatcher; dc != nil`** and gate on `intercomLog != nil` instead:
  - the squawks handler and `squawksMux` (with the `/escalate` HTTP handler, which needs only the store and supervisor),
  - the wake-on engine (configured as above),
  - the Discord relay engine (still also gated on `runtime.discord`), with its cursors/markers/receipts files.
- **Stays in the dispatcher block:** the tracker, the dispatcher itself, the escalation engine, and the Linear relay engine (these need the tracker).
- **Discord covers every Discord project:** `Projects("discord")` returns every store project whose chat service is `discord`, not just the dispatcher's project. Linear routing (`selfIdentity`, `routeLinear`) keeps its current single-project behavior and only runs with a dispatcher. Split or parameterize `directory` as needed so the Discord path needs no tracker.

- [ ] **Tests (fail first):** config precedence for each wake field; unknown-key check accepts `runtime.wake`; the directory lists all Discord projects; a serve-wiring test (or the closest existing one) shows `/squawks` is served with an intercom log and no dispatcher.
- [ ] **Commit** — `at-harbor: run the intercom without a dispatcher (session-kinds slice 3)`

---

## Task 6: Docs + verification

- [ ] **Docs.**
  - `personal-sessions.md`: the conversation flow (cove speaks first; reply to its Discord message to continue), the Discord + delivery-profile requirement, reply-only v1 limit, sessions stay until released (paused when idle), and remove "runs its prompt once".
  - `intercom.md` / `serve.md`: the intercom runs without a dispatcher; the `runtime.wake` block and its precedence.
  - `dispatcher.md`: its wake fields are now a fallback for `runtime.wake`.
  - `coves.md`: resident mode for personal sessions.
- [ ] **Commit** — `docs: long-lived personal sessions (session-kinds slice 3)`
- [ ] **Verification gate:** `just test`; `go build ./...`; `go build -tags integration ./...` (the integration-tagged packages compile); `just integration-harbor` if Postgres starts, else CI; `just lint`. **Behavior check:** dispatcher coves behave as before; a personal cove waits after every turn, isn't reaped for waiting, messages only its owner, and wakes on the owner's Discord reply; harbor with no dispatcher still serves `/squawks`, runs wake-on and the Discord relay.

## Self-review

- **Spec coverage:** Slice 3 of the revised sequence — resident loop, wake-on/escalation exemptions, send default + owner-only addressing, intercom without a dispatcher.
- **Types/names:** `agentrun.Config.Resident`, `AT_COVE_RESIDENT`, `residentResumePrompt`, `runtime.wake`, `Instance.SessionKind == "personal"` as the single trigger.
- **Out of scope:** the idle ladder (pestering, reclaim) — Slice 4; standing sessions — Slice 5; human-initiated threads to a cove.
