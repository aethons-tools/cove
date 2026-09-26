# Session kinds, Slice 5: standing sessions

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** An operator declares **named standing sessions** on a role — long-lived teammates like `reviewer/alice-bot` — and harbor keeps exactly one running per name: raises it if missing, raises it again (same name) if it dies, and tears it down when the name is removed.

**Architecture:**
- **Declarations on the Role.** `RoleAllocation.Standing` is a list of `{Name, Prompt}`, managed with a new `at-harbor standing add|rm|list` verb (the role's other fields are kept). The declaration list is the desired state.
- **A standing reconciler.** A new resident engine (`internal/standing`) walks every project's roles each tick and, per declared name, ensures a live cove: grant (kind standing) → raise → release on raise failure. A standing cove whose name is no longer declared is torn down. A name that keeps failing backs off exponentially instead of being re-raised every tick.
- **Standing coves are resident.** Like personal sessions, they wait after every turn and are never reaped for waiting. Resident behavior, keyed on `personal` today in the launcher and wake-on, becomes `harbor.IsResident(kind)` = personal or standing. The idle ladder stays personal-only (a standing session has no owner to nag).

**Tech Stack:** Go 1.26, `slog`, `just`. Spec: [`../specs/2026-09-26-session-kinds-standing-personal.md`](../specs/2026-09-26-session-kinds-standing-personal.md). Builds on session-kinds Slices 1–4 (merged).

## Decisions in this plan (differences from the spec)

- **Teardown still releases.** The spec said a standing cove's teardown must not release its reservation, so it would be resurrected. With the declaration as the desired state, the reconciler re-grants and re-raises a dead name anyway, so teardown can release normally and needs no special case. The standing cap is the number of declared names, so another kind can't take the slot in between.
- **"Resurrect" means a fresh session under the same name.** The agent's context from before the death is not carried over (portable/rehydratable sessions stay deferred).
- **Works without Postgres.** One actor id per name makes the reconciler idempotent, so with no ledger a standing grant is admitted by the declaration alone. With a ledger, grants and releases are recorded, capped at the number of declared names.
- **No default recipient.** A standing cove has no ticket and no owner, so `send` must name `to` (it's limited by the role's addressing, as for any cove). Its preamble says so.

## Global Constraints

- **Ephemeral and personal behavior unchanged,** except that the resident checks now go through `IsResident`.
- **Actor id per name:** `"standing-" + safe(project) + "-" + safe(role) + "-" + safe(name)`, where `safe` maps characters outside `[A-Za-z0-9._-]` to `-` (as for personal ids). Names are unique within a role; two names that sanitize to the same id are rejected when declared.
- **Validation:** name required, prompt required, duplicate name → 400.
- **Backoff:** after a raise fails for a name, retry after 30s, doubling up to 30m; reset once the name has a live cove. In-memory is fine.
- **Docs in the same change.** TDD. Stage files by path (untracked `.switchboard/`). Each commit builds. End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Standing declarations on the Role

**Files:** `internal/harbor/identity.go`, `internal/harbor/admin.go` (+ new endpoints), `internal/harbor/adminclient/adminclient.go`, `cmd/at-harbor/main.go` (new `standing` verb + command-table brief), tests. The admin UI role form keeps the whole `Allocation`; extend its test to cover `Standing`.

```go
type StandingSession struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

// RoleAllocation gains:
Standing []StandingSession `json:"standing,omitempty"`

// StandingActorID is the actor id harbor raises a standing session under.
func StandingActorID(project, role, name string) string
```

Endpoints (read-modify-write the Role, keeping all its other fields):
- `POST /admin/roles/{project}/{role}/standing` body `{name, prompt}` → 201; 404 unknown role; 400 on missing/duplicate name or an id collision.
- `DELETE /admin/roles/{project}/{role}/standing/{name}` → 204; 404 if not declared.
- `GET /admin/roles/{project}/{role}/standing` → the list (prompts included).

CLI:
```
at-harbor standing add  --project P --role R --name alice-bot --prompt-file F
at-harbor standing rm   --project P --role R alice-bot
at-harbor standing list --project P --role R
```
The prompt file is read on the host, never put on argv.

- [ ] **Tests (fail first):** add/list/rm round-trips via API, client and CLI; the role's scope, kit and other allocation fields survive add/rm; duplicate → 400; unknown role → 404; `StandingActorID` sanitizes.
- [ ] **Commit** — `harbor: declare named standing sessions on a role (session-kinds slice 5)`

---

## Task 2: Admit standing grants

**Files:** `internal/allocator/allocator.go` (+tests), `cmd/at-harbor/policy.go` (+tests).

- `allocator.Policy` gains `StandingNames []string`; `rosterPolicy` fills it from the Role.
- `Allocator.Grant` for `SessionStanding`: `req.Name` must be declared (else `(false, nil)`); with a ledger → `ledger.Grant(ctx, req, Caps{Kind: len(StandingNames)})`; without one → `(true, nil)` (the per-name actor id is the guard).

- [ ] **Tests (fail first):** declared name granted with cap = number of names; undeclared name denied; no ledger → granted; ephemeral and personal unchanged.
- [ ] **Commit** — `allocator: admit declared standing sessions (session-kinds slice 5)`

---

## Task 3: Standing coves are resident

**Files:** `internal/harbor/sessions.go` (or a new `internal/harbor/kinds.go`) for `IsResident`, `internal/harbor/instance.go` + `supervisor.go` (`RaiseSpec`/`Instance` gain `Name`), `internal/harbor/launcher/launcher.go`, `internal/wakeon/wakeon.go` (+tests for each).

```go
const SessionKindStanding = "standing"

// IsResident: sessions that wait after every turn and are never reaped for waiting.
func IsResident(kind string) bool { return kind == SessionKindPersonal || kind == SessionKindStanding }
```

- Launcher sets `Resident` when `IsResident(spec.SessionKind)`.
- Wake-on skips the wait-max teardown when `IsResident(inst.SessionKind)`. The idle ladder stays `personal`-only.
- `Raise` copies `spec.Name` onto the Instance.

- [ ] **Tests (fail first):** standing cove launched resident; a standing cove past wait-max is not torn down and gets no nags; personal and ephemeral unchanged.
- [ ] **Commit** — `harbor: standing sessions are resident (session-kinds slice 5)`

---

## Task 4: The standing reconciler

**Files:** new `internal/standing/standing.go` (+tests), `cmd/at-harbor/main.go` wiring.

Small interfaces (fakes in tests): the roster (projects, roles), the registry (instances), a granter (`Grant`, `RecordRelease`), the supervisor (`Raise`, `Teardown`).

Each tick (default every 30s, runs whenever harbor serves):
1. For every project → role → declared name: `id := harbor.StandingActorID(...)`.
   - A live Instance for `id` → clear its backoff, nothing to do.
   - None, and the name isn't backing off → `Grant(Request{Kind: standing, Name, ReservationID: id, …})`. Denied → log and move on. Granted → `Raise(RaiseSpec{ActorID: id, Project, Role, Name, Prompt: preamble + prompt, SessionKind: standing})`. Raise fails → `RecordRelease`, schedule backoff.
2. For every Instance with `SessionKind == standing` whose name is no longer declared on its role (or whose role is gone) → `Teardown` (records the release).

Preamble:
```text
You are the standing session "<name>" for role <role> in project <project>. You run until an operator
removes you. When you have results or need input, message people with the intercom `send` tool — you
must name the recipient (`to`); replies wake you and are available via `read`.
---
<declared prompt>
```

- [ ] **Tests (fail first, fakes + fake clock):** a declared name with no cove is granted and raised with the preamble; a live one is left alone; a dead one (instance gone) is raised again under the same id; a raise failure releases and backs off (no retry before the backoff, retry after, doubling, capped); a removed name's cove is torn down; a denied grant doesn't raise; ephemeral and personal instances are ignored.
- [ ] **Wiring:** construct and `go Run(ctx)` in `main.go` next to the Supervisor, whenever harbor serves.
- [ ] **Commit** — `standing: keep one live cove per declared standing session (session-kinds slice 5)`

---

## Task 5: Docs + verification

- [ ] **Docs.** New `docs/usage/harbor/standing-sessions.md` (leaf, sibling frontmatter) + `INDEX.md` row: declaring, the `standing` verb, what "kept alive" means (restarts fresh under the same name, with backoff), removal, messaging (must pass `to`, limited by role addressing), no idle nags. Note it in `coves.md` (resident kinds) and `roster.md` (where standing lives on the role).
- [ ] **Commit** — `docs: standing sessions (session-kinds slice 5)`
- [ ] **Verification gate:** `just test`; `go build ./...`; `go build -tags integration ./...`; `just integration-harbor` if Postgres starts, else CI; `just lint`. **Behavior check:** declaring a name raises it; killing it brings it back under the same name; removing the name tears it down; a failing name backs off; ephemeral and personal unchanged.

## Self-review

- **Spec coverage:** Slice 5 — named declarations, keep-alive/resurrect reconcile, dismissal. The spec's "teardown ≠ release" is replaced by release-then-re-grant (see Decisions).
- **Types/names:** `StandingSession`, `RoleAllocation.Standing`, `StandingActorID`, `Policy.StandingNames`, `SessionKindStanding`, `IsResident`, `RaiseSpec`/`Instance` `Name`, `internal/standing`.
- **Out of scope:** carrying a standing session's context across restarts; config-file seeding of standing declarations; standing-specific idle handling.
