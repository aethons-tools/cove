# Admin SPA slice 7 (the Agents tab) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The admin app's **Agents** title-bar tab works, at parity with the old Agents pages:
- **the list:** status filter, polled every 3 s, Raise agent, Enroll agent (its token shown once, with Copy), Teardown;
- **an agent's page:**
  - its identity and grants (add, remove), Revoke, Teardown;
  - Runtime, Waiting & escalation, and Egress;
  - Access: shares with Unshare, live connections, previews;
  - its session streams and squawks;
  - **Setup** as a section: sections, the section filter, "Diff vs role", and each stale section's diff;
- **its session timeline**, on the user area's **Thinking** components: the stream picker, totals, and live events.

**Architecture:** `adminui/api_agents.go` holds the JSON twins of `agents.go`, `agent.go`, `access.go` and `writes.go`'s enroll, grant and studio routes, plus `setup_page.go`'s studio routes. `adminui/api_session.go` serves the timeline:
- `GET …/session` returns its streams and totals;
- a JSON-rows SSE feed is built on `sessionview.Serve`, pinned to `?stream=`, with a totals event;
- the page and raw routes match the user area's Thinking routes, and stay operator-only.

The app's Thinking components are made source-agnostic: a `ThinkingSource` gives the feed URL, page, raw and an availability check. The user area passes its routes and the admin passes its own. Agents and holders link to `agents/<id>` (spec decision (a)).

**Tech Stack:** as slice 1.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§3 an agent's Setup as a section; §4 "the agent session timeline keeps its server-sent stream, gaining the JSON-rows form the Thinking tab uses, so the timeline reuses the Thinking tab's components"; §6 slice 7). The 2026-10-24 policy decision (session event content is operator-only in the admin) holds.

## Global Constraints

- **Parity confirms:**
  - "Tear down <id>?"
  - "Revoke <id>? This invalidates its token."
  - "Remove <role> grant on <project> from <id>?"
  - "Unshare <id> from <user>? Their open SSH connections close unless they have access another way."
- **Statuses:** Raise and Teardown without a supervisor answer **503** "runtime supervisor not configured", and the page hides them (`canEdit`). Unshare is mounted only with collaboration (`live`). Setup is mounted only with setup deps, for a studio that isn't gone.
- **The enrollment token** is returned exactly once, in the enroll answer. It's never logged and never re-fetchable. The UI shows "Enrolled <id>. Copy this identity token now — it is not shown again." with Copy.
- **Status filter groups and labels** are `agentFilters`, kept in the URL (`?status=`). The empty texts are "No agents with this status." and "No agents.".
- **The timeline** keeps the old behaviour:
  - a stream picker ("<start> · N events"), with the newest stream when none is chosen;
  - totals: turns, episodes, tool calls, tokens in/out, cost;
  - progress events hidden unless asked (the Thinking "Progress" chip);
  - raw JSON on demand.

  Sessions not configured → "Session events are not configured on this Jam.".

## Review Focus

1. **A gone studio with only an audit trail:** not enrolled, not running, but with streams or squawks. The page shows the "no longer running" banner, no Teardown or Revoke, and the session and squawks. Nothing at all known → "Agent not found". Tested in Task 1 and Task 4.
2. **The enroll token** appears once in the UI and never in any GET or log line. Reloading the list doesn't show it again. Tested in Task 1 and Task 3.
3. **The timeline on a pinned older stream** loads that stream's events and pages earlier ones within it. The user area's Thinking tab still follows the newest stream. Tested in Task 2 and Task 5.
4. **Studio Setup** "Diff vs role" and a stale section's lazy diff show the diff lines, through `SetupDisplayText` on the server. Tested in Task 1 and Task 4.
5. **Teardown or Revoke from the agent page** navigates to the list. From the list, Teardown refreshes the table in place and keeps the filter. Tested in Task 3 and Task 4.

---

### Task 1: Agents API

**Files:**
- Create: `internal/jam/adminui/api_agents.go`, `internal/jam/adminui/api_agents_test.go`
- Modify:
  - `internal/jam/adminui/apitypes.go`;
  - `internal/jam/adminui/writes.go` (extract `enroll(store, credExists, id, project, role, dests string) (token string, err error)`, `addGrant(...)` and `raiseManual(...)`, so the HTML and JSON routes run one implementation);
  - `internal/jam/adminui/access.go` (extract `unshare(...)`);
  - `internal/jam/adminui/api.go` (registration: takes `sessStore`, `accessDeps`, `canEdit`, `setup`).

**Interfaces:**
- Produces (types, camelCase json, lists never nil; times as the pages show them):
  ```go
  type AgentListRow struct { ID, Name, Kind, Project, Role, Unit, Status, Connector, Image, LastSeen string; MoreGrants int; HasStudio bool; Attn *AttnFlag `tstype:"AttnFlag | null"` }
  type AgentsResponse struct { Agents []AgentListRow; Filters []KV; Status string; CanEdit bool } // Filters: key → label
  type Enrolled struct { ID, Token string }
  type GrantRow struct { Project, Role string; Destinations []string }
  type ShareRow struct { UserID, User string; Rights []string; ExpiresAt string; Expired bool; GrantedBy string }
  type LiveRow struct { Name, Right, Since string }
  type PreviewRow struct { Name string; Port int; URL string }
  type AccessCard struct { Enabled bool; Requester *UserRefRow `tstype:"UserRefRow | null"`; MembersPreview, MembersSSH, RoleProject, RoleName string; Shares []ShareRow; Live []LiveRow; PreviewsOn bool; Previews []PreviewRow }
  type UserRefRow struct { ID, Name string }
  type StreamRow struct { StreamID, Started, LastEvent string; Events int }
  type AgentDetail struct {
      ID string; Running bool; Status, Kind, Owner, OwnerID, Name, Project, Role, Unit string
      HasActor bool; Grants []GrantRow; TokenExpiry string
      Raised, LastSeen, Lease, Backend, Location string
      WaitingSince string; WaitSeq int64; EscalationTier int; TierPingedAt, EscalationCategory string; Nags int; LastNagAt string; CommitSeq int64
      EgressID string; EgressKnown bool; EgressFailures int
      Access AccessCard; SessionsEnabled bool; Streams []StreamRow
      SquawksConfigured bool; Squawks []SquawkRow; SquawksMore bool
      CanEdit, SetupEnabled bool
  }
  ```
- Routes:
  - `GET api/agents?status=` → `AgentsResponse`;
  - `POST api/agents {id, role, project, unit, prompt}` (raise) → 201 `Created{ID: sid, Path: "agents/<sid>"}`, or 503 without a supervisor;
  - `DELETE api/agents/{id}/studio` (teardown) → 204;
  - `POST api/enrollments {id, role, project, destinations}` → `Enrolled`;
  - `DELETE api/enrollments/{id}` → 204;
  - `POST api/agents/{id}/grants {role, project, destinations}` and `DELETE api/agents/{id}/grants/{project}/{role}` → `AgentDetail`;
  - `DELETE api/agents/{id}/shares/{user}` → `AgentDetail` (only with `live`);
  - `GET api/agents/{id}` → `AgentDetail`, or 404;
  - `GET api/agents/{id}/setup?sections=&diff=role` → `SetupResponse` (slice 4's type), with each stale section's `diffPath`;
  - `GET api/agents/{id}/setup/sections/{section}` → `SetupSection`.

  Log lines are the HTML routes'. The enroll route never logs the token.
- [ ] **Step 1: Write the failing tests** in `api_agents_test.go`:
  - `TestAgentsAPI`:
    - enroll returns a token, and the token appears in no GET body;
    - the list shows the enrolled agent with status `pending`;
    - add a grant, then remove it;
    - revoke → 204;
    - `?status=running` filters.
  - `TestAgentsAPIRaise`: without a supervisor, raise and teardown → 503. With `&jam.Supervisor{}` (the HTML tests' fake), raise → 201 plus its path.
  - `TestAgentAPIGone`: an agent with only squawks (a fake log) → 200 with `running: false`, `hasActor: false` and its squawks; nothing known → 404.
  - `TestAgentAPISetup`: reuse `setup_page_test.go`'s studio fixture. GET setup → sections; `?diff=role` → diffed; a section's `diffPath` GET → one section diffed; no setup deps → 404.
  - `TestAgentAPIUnshare`: reuse `access_test.go`'s fixture. Unshare → the share is gone; a missing share → 404.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run 'AgentsAPI|AgentAPI'` → FAIL.
- [ ] **Step 3:** Implement, extract, then regenerate types.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): agents API`.

### Task 2: Session timeline API

**Files:**
- Create: `internal/jam/adminui/api_session.go`, `internal/jam/adminui/api_session_test.go`
- Modify: `internal/jam/adminui/apitypes.go`

**Interfaces:**
- Produces:
  ```go
  type SessionEvent struct { /* the same JSON fields as userui.ThinkingEvent: stream, seq, turn, kind, tool, toolUseId, at, text, detail, detailTruncated, isError, truncatedBytes, costUsd, durationMs, claudeSession */ }
  type SessionTotals struct { Turns, Episodes, ToolCalls int; InputTokens, OutputTokens int64; CostUSD float64 }
  type SessionInfo struct { Enabled bool; Streams []StreamRow; Stream string; Totals SessionTotals } // Stream: the chosen or newest
  type SessionPage struct { Events []SessionEvent; Before uint64 }
  type SessionRaw struct { Raw string; Truncated int }
  ```
  `SessionEvent`'s converter is `sessionEventOf(sessionview.EventView)`, matching `userui.thinkingEventOf` field for field. A Go test pins the JSON keys equal to `userui.ThinkingEvent`'s.
- Routes, all operator-only through the admin gate:
  - `GET api/agents/{id}/session[?stream=]` → `SessionInfo`;
  - `GET api/agents/{id}/session/events?stream=` → SSE via `sessionview.Serve`, with `Event` the `SessionEvent` JSON, `Totals` the `SessionTotals` JSON, and pinned to `stream` when given (else following the newest, as today's page does with no streams);
  - `GET api/agents/{id}/session/page?stream=&before=` → `SessionPage`;
  - `GET api/agents/{id}/session/raw?stream=&seq=` → `SessionRaw`.

  These answer 204 / `enabled: false` when sessions aren't configured.
- [ ] **Step 1: Write the failing tests** in `api_session_test.go`, with a `sessionevents` mem store and hub (as `session_test.go` does):
  - info lists the streams, newest first, and the totals;
  - the page returns events below `before` within the stream;
  - raw returns the event's JSON;
  - the events route streams `ev` lines whose data parses as `SessionEvent` (read the first lines with a cancellable request);
  - not configured → `enabled: false` and 204;
  - the JSON keys equal `userui.ThinkingEvent`'s.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement, then regenerate types.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): session timeline API`.

### Task 3: The Agents list

**Files:**
- Create: `web/app/src/admin/agents/AgentsPage.tsx`, `web/app/src/admin/agents/agents.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (`agents`, `agents/:id`, `agents/:id/session`), and remove the `agents/*` `NotMovedYet` route.

**Interfaces:**
- Produces: **AgentsPage** (`?status=`, polled every 3 s while visible), with:
  - the heading "Agents" and the sub "every agent, running or not — refreshes every 3s";
  - "Raise agent" (when `canEdit`): Label, Role `TypeAhead kind="roles" project=<the form's project>`, Project `TypeAhead kind="projects"` (default "default"), Unit, Workload prompt. It navigates to the new agent.
  - "Enroll agent": Agent id, Role, Project, Destination overrides. The answer shows the token panel with Copy. The panel stays until dismissed and is never refetched.
  - the filter chips as links (`?status=`);
  - a `DataTable` with these columns:
    - Agent: a link, with name, id, unit and `AttnFlag`;
    - Kind;
    - Project: a link to the project node;
    - Role: a link to the role leaf, plus "+N";
    - Status;
    - Connector;
    - Image;
    - Last seen;
    - Teardown: a `ConfirmAction` when `canEdit && hasStudio`; refreshes the list.
- [ ] **Step 1: Write the failing tests** in `agents.test.tsx`:
  - rows and links;
  - the filter chip marks the current one and links `?status=`;
  - enroll shows the token panel with the parity text, and Copy;
  - raise navigates;
  - Teardown confirms and refetches;
  - no Raise or Teardown without `canEdit`;
  - the empty texts.
- [ ] **Step 2:** `npx vitest run src/admin/agents` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin agents list`.

### Task 4: An agent's page

**Files:**
- Create: `web/app/src/admin/agents/AgentPage.tsx`, `web/app/src/admin/agents/agent.test.tsx`
- Modify: `web/app/src/admin/ui/SetupView.tsx` (an optional `diffToggle`: "Diff vs role" / "Hide diff" adds `diff=role` to the query)

**Interfaces:**
- Produces: **AgentPage**, with:
  - a header: id, status, the facts chip (kind · owner (a link to the user) · name), project and role links, unit, "Open live timeline" (`agents/<id>/session`, when sessions are enabled), Teardown (`canEdit && running`), Revoke (`hasActor`);
  - the not-running banner (enrolled or gone);
  - Identity: grant badges (each a role link with a × remove that confirms), Token expiry, and "+ Grant" (Role `TypeAhead`, Project, Destination overrides) → the page refreshes from the answer; or "Not enrolled — …";
  - when running: Runtime, Waiting & escalation and Egress (`Facts`, with the old texts), and Access (requester, members policy linking the role, the shares table with Unshare, the live connections, previews or "Previews are off on this Jam.", or "Studio collaboration is off on this Jam.");
  - Session: the streams table (links `agents/<id>/session?stream=<id>`), or "Session capture is not configured on this Jam.";
  - Squawks: `SquawkTable`, plus "all in Intercom" (the old `/app/admin/intercom?participant=` until slice 8 moves it);
  - Setup (when `setupEnabled`): opened on demand, with `SetupView path="agents/<id>/setup" diffToggle`.

  Not found → "Agent not found" with its text.
- [ ] **Step 1: Write the failing tests** in `agent.test.tsx`:
  - a running agent's facts and links;
  - a grant remove confirms with the parity text;
  - + Grant posts;
  - Revoke and Teardown navigate to `agents`;
  - a gone agent shows the banner and no Teardown or Revoke;
  - Unshare confirms;
  - Setup "Diff vs role" refetches with `diff=role`;
  - a stale section's diff loads via `diffPath`;
  - not found.
- [ ] **Step 2:** `npx vitest run src/admin/agents/agent` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin agent page`.

### Task 5: The timeline on the Thinking components

**Files:**
- Modify:
  - `web/app/src/session/thinking/useThinkingFeed.ts` and `ThinkingTab.tsx` (take a `ThinkingSource` instead of building `/app/user` URLs);
  - `web/app/src/session/SessionView.tsx` (or wherever `ThinkingTab` is mounted: pass the user source);
  - existing Thinking tests (pass the user source).
- Create: `web/app/src/session/thinking/source.ts`, `web/app/src/admin/agents/TimelinePage.tsx`, `web/app/src/admin/agents/timeline.test.tsx`

**Interfaces:**
- Produces:
  - `type ThinkingSource = { events(stream: string): string; page(stream: string, before: number): Promise<ThinkingPage>; raw(stream: string, seq: number): Promise<ThinkingRaw>; check(): Promise<number> /* an HTTP status: 404 = gone */; pinned?: string /* follow this stream only */ }`.
  - `userThinkingSource(id)` keeps today's URLs, and the user area's behaviour is unchanged.
  - `adminSessionSource(id, stream)` uses `/app/admin/api/agents/<id>/session/events?stream=`, `…/page`, `…/raw` and `…/session` for the check.
  - `ThinkingTab({ source, busy, … })`: the pane query it used for availability becomes `source.check`.
  - **TimelinePage** (`agents/:id/session`, `?stream=`) has:
    - a header with id and the totals line: turns, episodes, tool calls, tokens in/out, cost (with `num` and `usd` formatting as the user header does);
    - the stream select ("<start> · N events"), which changes `?stream=`;
    - `ThinkingTab` with the admin source, pinned to the chosen stream.

    Not configured → "Session events are not configured on this Jam." with a link back to the agent.
- [ ] **Step 1: Write the failing tests:**
  - `timeline.test.tsx`: the stream select lists the streams and changing it sets `?stream=`; the totals render; events from a mocked source render through the Thinking rows; not configured → its banner.
  - The existing `ThinkingTab.test.tsx` / `useThinkingFeed.test.tsx` pass with the user source unchanged in behaviour.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` and `scripts/app.sh` (budgets) → PASS.
- [ ] **Step 5: Commit** `feat(app): admin session timeline on the Thinking components`.

### Task 6: Browser test and docs

**Files:**
- Modify: `internal/jam/adminui/app_browser_test.go` (`TestBrowserAdminEnroll`), `docs/web-app.md`, `docs/usage/jam/ui-console.md`, `docs/usage/jam/ui-editing.md` (the preview's Agents tab), and the spec's status line.

- [ ] **Step 1:** Write `TestBrowserAdminEnroll`:
  - open `agents`, then "Enroll agent";
  - fill Agent id and Role (a project with role `coder`) and Enroll;
  - wait for "Copy this identity token now";
  - assert the actor exists and there are no page problems.
- [ ] **Step 2:** `just app && just test-browser` → PASS, including the user area's Thinking browser tests.
- [ ] **Step 3:** Update the docs, then run the docs audit.
- [ ] **Step 4:** `just test && just lint && (cd web/app && npm run check)` → PASS.
- [ ] **Step 5: Commit** `test(admin): browser test for an enrollment; docs`.
