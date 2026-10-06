# intercom 1b: sessions and project references — plan series

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Each sub-plan is its own PR, test-first, written against the code the previous one landed.

**Spec:** slice 1 spec §4 (sessions), plus the re-sequencing note: project references, tombstones and rename moved here from 1a.

**Status:** for review before any code. This series changes how running standing sessions find their state volumes.

## Where things stand (inventory, 2026-10-06)

- **Session identity is the actor id, built per kind:**
  - standing: `standing-<project>-<role>-<name>`, recomputed on every reconcile;
  - ticket: `cove-<ISSUE>`, with dedup by that id;
  - personal: `personal-<owner>-<hex>`;
  - manual: the operator's `--id`.
- **The id keys everything:**
  - the instance row, actor/token, allocator reservation, session_events;
  - Discord receipts, nag ids, `actor:<id>` log targets, unread cursors;
  - the container and its volumes (`atcove-cove-<id>[-agent-data|-workspace|-docker]`, labelled `harbor.cove.state=<id>`).
- **Standing state survives teardown, upgrade and Jam restarts only because the id is recomputed from the name.** Reset purges all three volumes; the reconciler's sweep purges any state owner that is no longer declared.
- **A non-standing cove's `-docker` volume** (created implicitly by `-v` when Docker is on) is never removed. It leaks.
- **Project names key:** roles (PK + FK), grants, instances, allocator streams (`<project>/<role>`), session_events, relay cursors. The log (`squawks.project`) waits for slice 2 under option B.

## Sub-plans

### 1b-1: session ids for new sessions; standing sessions by declaration

- **Grandfathering.** Every live session keeps its current id as its session id (registered in `participants`, kind `ses`). Its volumes, inbox rows, receipts and nags keep working. Nothing is renamed.
- **`standing_sessions(project_id, role, name) → session_id`** (new table and store methods).
  - It is seeded from every declared standing session, with its current `StandingActorID`, so live state carries over untouched.
  - The reconciler looks sessions up through it instead of recomputing `StandingActorID`.
  - A declaration with no entry mints a `ses_` id.
  - **Reset** = teardown + purge, then the entry is replaced with a fresh `ses_`. That is "reset ends the session; the next raise is a new one".
  - **Upgrade and restart** keep the entry, so the same session and volumes are used.
  - **Removing a declaration** drops the entry; sweep purges the volumes.
  - Sweep and the 409 holder check key on the map.
  - `StandingActorID` stays only to seed the map, then is deleted.
- **New sessions get `ses_` ids:**
  - **ticket** sessions: a `ses_` id, with dedup by "a live instance whose `Unit` is the ticket". A re-dispatch is a new session.
  - **personal** sessions: `ses_`.
  - **manual** raises: unchanged in this sub-plan. `--id` remains the session id, because the CLI and UI address the session by it. Turning `--id` into a label is listed under open questions.
- **The `-docker` volume becomes studio-side** (decided 2026-10-06): removed on every teardown, any kind.
  - This fixes the non-standing leak.
  - A standing session upgraded or restarted into a new studio starts with a clean Docker store. agent-data and workspace stay with the session.
- **Tests:**
  - reconciler: seeding, reset mints a new id and new volumes, upgrade keeps the id, dismissal and sweep;
  - dispatcher dedup by unit;
  - launcher: `-docker` removed at teardown;
  - grandfathered ids still route inbox, receipts and nags.

### 1b-2: roles, grants and instances by project id

- Store APIs take project ids. Edges (admin API, CLI, UI, serve config) resolve names. `DefaultProject` is resolved once.
- `roles` PK becomes `(project_id, name)` with an FK to `projects(id)`. `Grant.Project` and `Instance.Project` hold ids, rewritten by a migration step.
- **Allocator:** `stream_id = <project_id>/<role>` and `category = project_id`, rewritten in place by exact mapping so live reservation counts carry over. Per-owner caps count by user id. `OutstandingReservations` stops parsing names.
- **session_events:** add `project_id` / `owner_id` columns (backfilled); the text columns stay as labels.
- **Relay cursor keys:** `service/<project_id>`, with a one-time state-file rewrite and a `.bak`.
- **Size:** this is the large mechanical part (about 200 call sites, about 300 test sites). It splits further into 1b-2a (the store and its callers) and 1b-2b (allocator, events, relay) if the diff gets unwieldy.

### 1b-3: project tombstones and rename

- **`RemoveProject` tombstones.** The id keeps resolving as "name (removed)", and the name is freed.
- **`RenameProject`** is a single-row update. Admin API `PUT /admin/projects/{p}/name`, `at-jam project rename`, and the UI.
- **`legacy_human_aliases`** stays keyed by project *name* at the time of migration (frozen history), so it needs no rewrite.

## Open questions for review

1. **Manual raises.** Keep `--id` as the session id (proposed), or mint `ses_` and make `--id` a unique label that `studio teardown <label>` resolves?
2. **Ticket sessions.** Should a ticket's re-dispatch see the previous session's inbox history? The spec says no ("a session is a session"), and slice 2's ticket channel is where shared history lives. Proposed: no.
3. **Upgrade drops the Docker cache** (images and build cache). This was agreed earlier. Confirm it's acceptable for your standing sessions, since the first build after an upgrade will be slower.
