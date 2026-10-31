# VS Code hub, slice 2: the rail view — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The web app's rail becomes a VS Code view in every window, replacing the Needs You tree.
- A click goes to the row's session window if it's open, otherwise to the conversation in the web app. The hub replaces the web app in slice 3.
- A double-click opens or focuses the session window.
- A ▣ marks sessions whose window is open.

**Architecture:**
- **Server:** each rail row gains `session` (its head session, as `api/conversations/{c}` reports it) and `ssh` (whether the viewer may SSH into it), so the extension needs no extra lookups.
- **web/app:** the rail's row list moves into a shared `RailList` component. The web `Rail` renders it with router links; a new `rail` pane renders it with click and double-click handlers that post intents.
- **web/vscode:**
  - a `RailViewProvider` (`jam.rail`) uses the shared pane port;
  - its state carries the sessions with an open studio window (connected marks);
  - the Needs You class keeps only its badge logic and puts the badge on the rail view.

**Tech Stack:**
- Go (`internal/jam/userui`), with tygo-generated `web/app/src/api/types.ts`;
- React, Mantine and vitest (`web/app`);
- TypeScript, VS Code 1.105 and vitest (`web/vscode`).

**Spec:** `docs/superpowers/specs/2026-10-30-vscode-hub-design.md` (slice 2: "add the rail view"). Read *Ordinary window*, the *Hub*'s rail bullets and *When a session can't have a window*. Slice 1's plan, `2026-10-30-vscode-hub-1-always-connected.md`, is merged into this branch.

## Global Constraints

- **The VS Code rail matches the web rail's groups and cues:**
  - groups: Needs you, Sessions, Channels, History;
  - per row: label, mission and unread badge;
  - no *New message* or *New session* buttons in this slice (slice 6);
  - no chimes, no app badge.
- **Click** a row:
  - if `row.session` has an open studio window (connected mark), run `openSession(row.session)`, which focuses that window;
  - otherwise open the row's conversation in the web app (`webUrl(jamUrl, row.id)`). This is a stand-in until slice 3's hub.
- **Double-click** a row:
  - if `row.session !== ""` and `row.ssh`: `openSession(row.session)`;
  - otherwise the same as a click.
  - A single click's action must not also run as the first half of a double-click: the pane waits `DOUBLE_CLICK_MS = 250` before acting on a click.
- **▣ (title "Has a window open")** shows on rows whose `session` is in the state's `windows`.
- **The `jam.rail` view** shows in every window, first in the Jam container, named "Conversations".
  - The Needs You view, its welcome entries and `jam.needsYou.open` are removed.
  - The activity-bar badge (`badgeFor(needs-you count)`) moves to the rail view (`WebviewView.badge`).
- **Row ids are untrusted:** the host validates `chn_…` and `ses_…` ids (`ID_RE`) before acting.
- **`windows`** comes from the `jam.connected.*` marks in globalState (fresh ones only, per `CONNECTED_STALE_MS`). It's refreshed on view resolve, on live events, and every `CONNECTED_BEAT_MS`.
- **Run checks with:** `go test ./internal/jam/userui/` plus `scripts/app-types-check.sh`; `npm run check` in `web/app` and in `web/vscode`.
- **Docs change in the same change set:** `docs/usage/jam/vscode.md`, `docs/usage/jam/vscode-checklist.md`, and the rail API row in `docs/usage/jam/` (wherever `api/rail` is documented).

## Review Focus

1. **A crafted row in the pane** (`go` intent with a bad channel or session id, or `session` plus `windows` lies): the host refuses bad ids. A click only focuses a window that the extension's own marks say is open. *(Test in Task 3.)*
2. **Double-click doesn't also fire the click:** one `openSession`, no web app tab. *(Test in Task 2.)*
3. **Signed out:** the rail shows "Sign in to Jam to see your conversations." with Sign in, and the badge clears. *(Tests in Tasks 2 and 3.)*
4. **A session whose window went stale** (mark older than `CONNECTED_STALE_MS`): no ▣, and a click goes to the web app. *(Test in Task 3.)*
5. **A history row** (`legacy` id prefix, no session): click opens the web app, and double-click is the same. *(Test in Task 2, via `session: ""`.)*

---

### Task 1: rail rows carry `session` and `ssh`

**Files:**
- Modify: `internal/jam/userui/apitypes.go`. `ChannelRow` gains:
  ```go
  // Session is the actor id of the session whose home this channel is,
  // when the viewer may see it ("" = none), as api/conversations reports it.
  Session string `json:"session"`
  // SSH reports whether the viewer may SSH into Session's studio.
  SSH bool `json:"ssh"`
  ```
- Modify: `internal/jam/userui/api.go`, in `apiRail`. After `Rail(...)`, for every row:
  - `h.headSession(p, row.ID)` sets `Session`;
  - `collab.Decide(h.d.Store, time.Now(), collabPrincipal(r, p), inst, collab.SSH)` sets `SSH`.
  - Compute `now` once.
- Regenerate: `go generate ./internal/jam/userui/`, which updates `web/app/src/api/types.ts`.
- Test: `internal/jam/userui/api_sessions_test.go`.

**Interfaces:**
- Produces: `ChannelRow.session: string` and `ChannelRow.ssh: boolean` in `web/app/src/api/types.ts`.

- [ ] **Step 1: Write the failing test** `TestAPIRailRowSessionAndSSH`, using `frameEnv(t)`, where `personal` is SSH-able and `standing` isn't:

```go
func TestAPIRailRowSessionAndSSH(t *testing.T) {
	e, p, personal, standing, _ := frameEnv(t)
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/app/user/api/rail", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asPrincipal(req, p, false))
	rr := decodeAs[RailResponse](t, rec)
	homeOf := func(inst jam.Instance) string { ch, err := e.Intercom.HomeChannel(inst); if err != nil { t.Fatal(err) }; return string(ch.ID) }
	rows := map[string]ChannelRow{}
	for _, g := range rr.Groups { for _, r := range g.Rows { rows[r.ID] = r } }
	if r := rows[homeOf(personal)]; r.Session != personal.ActorID || !r.SSH { t.Errorf("personal row = %+v", r) }
	if r := rows[homeOf(standing)]; r.Session != standing.ActorID || r.SSH { t.Errorf("standing row = %+v", r) }
	for id, r := range rows { if id != homeOf(personal) && id != homeOf(standing) && (r.Session != "" || r.SSH) { t.Errorf("non-session row %s = %+v", id, r) } }
}
```

(Adapt the helper names if `frameEnv`'s `e` lacks `Intercom`; `TestAPISessionHeadChannel` uses `e.Intercom.HomeChannel`.)

- [ ] **Step 2: Run, and see it fail.** Run: `go test ./internal/jam/userui/ -run TestAPIRailRowSessionAndSSH`. Expected: FAIL to compile, because `ChannelRow` has no `Session` field.
- [ ] **Step 3: Implement** as listed under **Files**, then run `go generate ./internal/jam/userui/`.
- [ ] **Step 4: Run, and see it pass.** Run: `go test ./internal/jam/userui/ && scripts/app-types-check.sh` (after committing the generated file), plus `golangci-lint run ./internal/jam/userui/...` if available. Expected: PASS.
- [ ] **Step 5: Commit** `feat(userui): rail rows carry their session and SSH access`.

### Task 2: the shared `RailList` and the `rail` pane

**Files:**
- Create: `web/app/src/rail/RailList.tsx`:
  - `RailList({ groups, active, rowProps, marker }: { groups: RailGroup[]; active?: string; rowProps: (r: ChannelRow) => Record<string, unknown>; marker?: (r: ChannelRow) => ReactNode })`;
  - renders the group titles and `NavLink` rows exactly as `Rail.tsx` does today (label, mission description, unread badge, `active={r.id === active}`);
  - spreads `rowProps(r)` onto each `NavLink`;
  - `marker(r)` renders before the unread badge in `rightSection`.
- Modify: `web/app/src/rail/Rail.tsx`: render `RailList` with `rowProps = (r) => ({ component: RouterLink, to: … })`. Behaviour unchanged; its existing tests must pass untouched.
- Modify: `web/app/src/bridge/protocol.ts`:
  - `PaneName = SessionPaneName | "board" | "rail"`;
  - `PaneState` gains `windows?: string[]`;
  - `OpenIntent` gains `| { kind: "go"; channel: string; session: string }`.
- Create: `web/app/src/vscode/RailPane.tsx`:
  - `RailPane({ bridge, windows, active }: { bridge: Bridge; windows: string[]; active?: string })`;
  - uses `useRail()` with no selection (Loader, `Problem`, empty copy as `Rail.tsx`);
  - rows are `component: "button"`;
  - `onClick` schedules `go` after `DOUBLE_CLICK_MS`, and `onDoubleClick` cancels it, then posts `{ kind: "session", id: r.session }` when `r.session && r.ssh`, otherwise `go`;
  - `marker` shows `▣` (`title="Has a window open"`, `aria-label="Has a window open"`) when `windows.includes(r.session)`;
  - export `DOUBLE_CLICK_MS = 250`.
- Modify: `web/app/src/vscode/Pane.tsx`:
  - `name === "rail"`: signed in → `<Shell><RailPane bridge windows={view.s.windows ?? []} active={target channel if any} /></Shell>`; signed out → `copy.signedOut`, which is "Sign in to Jam to see your conversations." for `rail` (`copyFor`);
  - `rail` never shows the no-target copy.
- Tests:
  - `web/app/src/rail/RailList.test.tsx` (new);
  - `web/app/src/vscode/RailPane.test.tsx` (new);
  - `web/app/src/vscode/Pane.test.tsx` (signed-out copy for `rail`).

**Interfaces:**
- Consumes: Task 1's `ChannelRow.session` and `ChannelRow.ssh`.
- Produces:
  - the `go` intent `{ kind: "go"; channel: string; session: string }`;
  - `PaneState.windows`;
  - the pane name `"rail"`;
  - `DOUBLE_CLICK_MS`.

- [ ] **Step 1: Write the failing tests.**

`RailPane.test.tsx` uses the fake host (`fakeHost.ts`), with the route `GET /app/user/api/rail` answering two groups:
- `needs-you`: row `chn_A`, `session: SES_A`, `ssh: true`;
- `channels`: row `chn_B`, `session: ""`, `ssh: false`.

Cases (vitest fake timers):
- `"renders the groups and rows like the web rail"`: titles "Needs you" and "Channels" and both labels show, and no "New message" button.
- `"a click posts go after DOUBLE_CLICK_MS"`: click `chn_A`; nothing is posted before `DOUBLE_CLICK_MS`. After it, `h.posted` contains `{ type: "open", intent: { kind: "go", channel: "chn_A", session: SES_A } }`.
- `"a double-click on an SSH-able session posts session, and no go"` *(Review Focus 2)*: after double-click and `DOUBLE_CLICK_MS * 2`, the posted intents are exactly `[{ kind: "session", id: SES_A }]`.
- `"a double-click on a row without a session posts go"` *(Review Focus 5)*: the result is `[{ kind: "go", channel: "chn_B", session: "" }]`.
- `"▣ marks rows whose session has a window"`: with `windows=[SES_A]`, `getByLabelText("Has a window open")` is inside `chn_A`'s row, and there is exactly one.

`RailList.test.tsx`:
- `"active marks the row; marker renders"`;
- `"rowProps are spread onto each row"`.

`Pane.test.tsx`: `"rail signed out shows Sign in"` *(Review Focus 3)*: name `rail`, state `{ signedIn: false, target: null }`, and the text "Sign in to Jam to see your conversations." with a Sign in button.

- [ ] **Step 2: Run, and see them fail.** Run: `cd web/app && npx vitest run src/rail src/vscode`. Expected: FAIL (no `RailList` or `RailPane` module).
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/app && npm run check`. Expected: PASS, including the existing `Rail` tests unchanged.
- [ ] **Step 5: Commit** `feat(app): a shared RailList and the VS Code rail pane`.

### Task 3: the `jam.rail` view in the extension

**Files:**
- Modify: `web/vscode/src/open.ts`: `connectedSet` gains `list(): string[]`, the session ids with a fresh mark. It reads `st.keys()`, so its `st` type gains `keys(): readonly string[]`.
- Modify: `web/vscode/src/bridge/intents.ts`:
  - `IntentAction` gains `| { goTo: { channel: string; session: string } }`;
  - the `go` intent requires `ID_RE.chn.test(channel)`, and `session === "" || ID_RE.ses.test(session)`, else `Error("Not a conversation id.")`.
- Modify: `web/vscode/src/panes/actions.ts`: `ActionDeps` gains `goTo: (channel: string, session: string) => unknown`, and `perform` dispatches to it.
- Create: `web/vscode/src/panes/rail.ts`:
  - `class RailViewProvider implements vscode.WebviewViewProvider`, with `viewType = "jam.rail"`;
  - deps: `PortDeps & { extensionUri; auth: Pick<Auth, "session" | "onChange">; windows: () => string[]; channel: () => Promise<string | undefined> }`;
  - `resolveWebviewView`: `paneHtml(w, uri, "rail")`, then `wirePane` with `state = async () => ({ signedIn, target: null, windows: d.windows() })`;
  - it refreshes on auth change, every `CONNECTED_BEAT_MS` while resolved, and on `refresh()`;
  - `setBadge(b)` stores the badge and applies it to every resolved view (`view.badge = b`).
  - *(Ruling allowed: `active` highlighting can stay unwired in this slice if it needs the target; the host passes `target: null`.)*
- Modify: `web/vscode/src/needsyou.ts`:
  - remove the `TreeDataProvider` parts (`rows`, `getChildren`, `getTreeItem`, `changed`, the `vscode` import if unused) and the `setContext` dep;
  - keep `needsYouRows`, `badgeFor`, `poke`, `refresh`, `onLive` and the signed-out handling; `setBadge` stays.
- Modify: `web/vscode/src/extension.ts`:
  - register `RailViewProvider`, and drop `createTreeView("jam.needsYou")` and the `jam.needsYou.open` command;
  - `NeedsYou.setBadge` goes to `rail.setBadge`;
  - live events also call `rail.refresh()`;
  - `actionDeps.goTo = (channel, session) => guard(async () => { if (session && connected.isConnected(session)) await openSession(openDeps, session); else { const { url } = await openDeps.signedIn(); await ui.openExternal(webUrl(url, channel)); } })()`.
- Modify: `web/vscode/package.json`:
  - `views.jam` starts with `{ "type": "webview", "id": "jam.rail", "name": "Conversations" }`;
  - remove the `jam.needsYou` view, `viewsWelcome`, the `jam.needsYou.open` command, and `menus` (now empty).
- Tests:
  - `web/vscode/test/open.test.ts` (`connectedSet.list`);
  - `test/intents.test.ts`, `test/actions.test.ts`, `test/rail.test.ts` (new);
  - `test/needsyou.test.ts` (drop the tree cases);
  - `test/manifest.test.ts`, `test/extension.test.ts`.

**Interfaces:**
- Consumes: Task 2's `go` intent, `PaneState.windows` and pane name `"rail"`.
- Produces:
  - `RailViewProvider` with `setBadge(b)` and `refresh()`;
  - `connectedSet(...).list()`;
  - `ActionDeps.goTo`.

- [ ] **Step 1: Write the failing tests.**
- `open.test.ts`: `"list() is the fresh marks only"`. Set a mark for ID now and one for ID2 at `now - CONNECTED_STALE_MS - 1`; `list()` is `[ID]` *(Review Focus 4)*.
- `intents.test.ts`:
  - `go` with good ids gives `{ goTo: { channel: CHN, session: SES } }`, and `session: ""` is allowed;
  - a bad channel, a bad session or a non-string gives `Error` *(Review Focus 1)*.
- `actions.test.ts`: `perform({ goTo: { channel: "c", session: "s" } }, d)` calls `d.goTo("c", "s")`.
- `rail.test.ts`, modelled on `session.test.ts`'s setup:
  - `"resolve renders the rail pane"`: the html contains `data-pane="rail"`;
  - `"state carries windows"`: with `windows: () => [SES]`, the posted state is `{ signedIn: true, target: null, windows: [SES] }`;
  - `"signed out posts signedIn false"`;
  - `"setBadge before and after resolve sets view.badge"`;
  - `"refreshes every CONNECTED_BEAT_MS"`: with fake timers, the `windows` dep is called again.
- `manifest.test.ts`:
  - `views.jam[0]` is `{ type: "webview", id: "jam.rail", name: "Conversations" }`, and there's no `jam.needsYou`;
  - `contributes.viewsWelcome` and `contributes.menus` are undefined;
  - the commands are exactly `["jam.forgetHostKey", "jam.openSession", "jam.signIn", "jam.signOut"]`.
- `extension.test.ts`:
  - `"activate registers six view providers and four commands"`, with no `createTreeView`;
  - `"the badge shows on the rail view and clears on sign-out"`, adapted from the existing badge tests, which read `treeView.badge`; they now read the resolved rail view's `badge` *(Review Focus 3)*;
  - `"a rail click on a session with a window focuses it; without one opens the web app"`: resolve `jam.rail`, receive `{ type: "open", intent: { kind: "go", channel: CHN, session: SES } }`:
    - with a fresh `jam.connected.${SES}` mark, the SSH setup runs and `vscode.openFolder` is called with authority `ssh-remote+jam-${SES}`;
    - with no mark, `openExternal` gets `${JAM}/app/user/c/${CHN}` *(Review Focus 1 and 4)*.
- `needsyou.test.ts`: delete the tree item cases, and keep the badge, debounce and signed-out cases (minus `setContext`).

- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run test/open.test.ts test/intents.test.ts test/actions.test.ts test/rail.test.ts test/manifest.test.ts test/extension.test.ts test/needsyou.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npm run check && npm run build`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): the rail view replaces Needs You`.

### Task 4: docs

**Files:**
- `docs/usage/jam/vscode.md`:
  - frontmatter: the Needs You view becomes the Conversations (rail) view;
  - a `## The rail` section, placed before `## Panes`: groups, ▣, click, double-click, every window, the badge;
  - the `## Notifications and the badge` text no longer mentions the Needs You view.
- `docs/usage/jam/vscode-checklist.md`: replace the Needs You steps with rail steps:
  - click with no window opens the web app;
  - click with a window focuses it;
  - double-click opens the studio window;
  - ▣ appears and disappears with the window;
  - the badge counts needs-you.
- **The rail API's row fields:** find where `api/rail` rows are documented (`grep -rn "api/rail" docs/usage`) and add `session` and `ssh`.
- `docs/usage/jam/INDEX.md`: the `vscode.md` row mirrors the frontmatter.

- [ ] **Step 1:** Make the edits.
- [ ] **Step 2:** Run `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep -E "ERROR"`. Expected: no new errors.
- [ ] **Step 3:** Run `grep -rn "Needs You\|needsYou" docs/usage/jam/vscode*.md`. Expected: nothing, except in history notes.
- [ ] **Step 4: Commit** `docs(vscode): the rail view`.
