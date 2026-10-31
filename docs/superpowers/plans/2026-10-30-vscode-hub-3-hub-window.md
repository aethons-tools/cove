# VS Code hub, slice 3: the hub window and conversation tabs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
- A hub window: no remote, the rail, and conversations as editor tabs. The session views follow the active tab.
- "Go to conversation X" means X's studio window if it's open, otherwise X's tab in the hub. That covers rail clicks in any window, notification clicks and links.
- A studio window's own conversation also becomes a tab, and the sidebar Conversation view goes away.

**Architecture:**
- **The hub** is a workspace file in global storage (`hub/Jam Hub.code-workspace`, holding no folders). A window on it is `jam.window == hub`.
- **Conversation tabs** are webview panels, one per channel (`ConversationPanels`, modelled on `BoardPanels`), each running the existing `conversation` pane with a fixed channel target.
- **Following the active tab:** `FocusModel` gets a settable session. In the hub, it follows the active conversation tab's session.
- **Handoff from other windows:** a `hub/pending.json` file in global storage. The other window writes it, then opens or focuses the hub. The hub reads it on activation and whenever its window gains focus.

**Tech Stack:**
- TypeScript, VS Code 1.105 and vitest (`web/vscode`);
- React and vitest (`web/app`, copy only).

**Spec:** `docs/superpowers/specs/2026-10-30-vscode-hub-design.md` (slice 3: "add the hub and conversation tabs"). Read *The windows*, *Getting in* (*Jam: Open Hub*, notifications, and the rule for going to a conversation) and *What it reuses*. Slices 1 and 2 are on this branch.

## Global Constraints

- **One hub per window set (this slice: per extension install; per Jam comes in slice 5):**
  - the file is `<globalStorage>/hub/Jam Hub.code-workspace`, holding `{"folders": []}`, created if missing;
  - opening it uses `vscode.openFolder` with `{ forceNewWindow: true, forceLocalWindow: true }`, which focuses a hub already open.
- **`jam.window` values:** `"session"` in a `jam-<id>` remote window; `"hub"` in a window whose `workspaceFile` is the hub file (directory compared case-insensitively on darwin and win32, as slice 1's old `windowKind` did); `""` anywhere else.
- **Going to a conversation:** `goTo(channel, session)`:
  - if `session` has a fresh connected mark → `openSession(session)`;
  - else if this window is the hub → open or reveal that channel's conversation tab;
  - else → write `hub/pending.json` as `{ "channel": <id>, "at": <ms> }` and open the hub.
  - The hub ignores a pending request older than `PENDING_MAX_MS = 60_000`, and deletes the file once read.
- **Conversation tabs:**
  - `viewType` is `jam.conversationTab`, one per channel, with `retainContextWhenHidden: false` and the panel state `{ channel }`;
  - restored after a reload, de-duplicated as `BoardPanels.restore` does;
  - the title is the conversation's label (`tabTitle`), with the icon `media/jam-color.svg`;
  - the target comes from `api.conversation(channel)`: `{ session: conv.session, label: conv.label, channel }`. A chat has `session: ""`.
  - A 404 closes nothing: it shows "That conversation is gone, or you can't see it."
- **Which tabs a window may open:**
  - **the hub:** any conversation;
  - **a studio window:** only its own session's channel. The window opens that tab on its **first** activation in the workspace (workspaceState `jam.conversationTabShown`), and never again after the user closes it.
  - **anywhere else:** none (it hands off to the hub).
- **The hub's views:**
  - Thinking, Status, Artifacts and Previews show the session of the most recently active conversation tab;
  - with no tab yet, or a chat tab, they show "Open a session's conversation to see its <noun>." with no action. This replaces slice 1's "This window isn't a Jam studio window."
  - Their `when` is `jam.window == session || jam.window == hub`.
- **The sidebar `jam.conversation` view is removed.** "Reveal an ask" (`revealConversation`) reveals the conversation tab for the window's current session channel, then scrolls to the anchor.
- **Notifications:**
  - an OS notification's click link is `<scheme>://aethons.jam/go/<chn>`, plus `?session=<ses>` when it has one;
  - a VS Code toast's *Open* runs `goTo(channel, session)`;
  - the URI handler routes `/go/<chn>` (`?session=` optional; both validated) to `goTo`, and `/hub` to opening the hub.
- **New command:** `jam.openHub`, "Open Hub", category Jam.
- **Push focus:** a visible conversation tab in a focused window reports its channel (`FocusReporter.pane`), as the removed view did.
- **Run checks with:** `npm run check` in `web/vscode` and `web/app`.

## Review Focus

1. **A rail click in an ordinary window when the hub isn't open:** the hub opens, and its first activation opens the pending conversation's tab. *(Test in Task 3.)*
2. **The same conversation clicked twice in the hub:** one tab, revealed. After a reload, a revived tab for a channel that's already open closes. *(Test in Task 2.)*
3. **A crafted `/go/…` link** (bad channel, bad session, extra path): refused with the "isn't a Jam link" warning, and nothing opens. *(Test in Task 3.)*
4. **A stale pending file** (over 60 s old, or junk JSON) is ignored and deleted. *(Test in Task 3.)*
5. **A studio window whose user closed its conversation tab:** reloading it doesn't reopen the tab. *(Test in Task 4.)*

---

### Task 1: the hub window

**Files:**
- Modify: `web/vscode/src/open.ts`:
  - add `export type WindowKind = { kind: "session"; id: string } | { kind: "hub" }`;
  - add `export function windowKind(w, hubDir: { scheme: string; path: string }, platform: string): WindowKind | undefined`, which matches a remote `jam-<id>` (via `studioFromAuthority`), or `workspaceFile` exactly `<hubDir>/Jam Hub.code-workspace`, using the old `windowKind`'s directory normalisation from `git show cf86cb2f:web/vscode/src/open.ts`;
  - add `export const HUB_FILE = "Jam Hub.code-workspace"` and `export const HUB_WORKSPACE = JSON.stringify({ folders: [] }, null, 2) + "\n"`;
  - `Ui` gains `openHub(): Promise<void>`.
- Modify: `web/vscode/src/uri.ts`: `Route` gains `{ kind: "hub" }` for the path `/hub`.
- Modify: `web/vscode/src/extension.ts`:
  - `hubDir = joinPath(globalStorageUri, "hub")`;
  - `ui.openHub` writes the hub file if missing (flag `wx`, ignore `EEXIST`), then runs `vscode.openFolder` on it with `{ forceNewWindow: true, forceLocalWindow: true }`;
  - `kind = windowKind(…)` replaces `windowStudio`'s derivation (`windowStudio = kind?.kind === "session" ? kind.id : undefined`);
  - `setContext("jam.window", kind?.kind ?? "")`;
  - register `jam.openHub`;
  - the URI handler's `hub` route calls `ui.openHub()`;
  - the activation log line says `window: hub` for the hub.
- Modify: `web/vscode/package.json`: the `jam.openHub` command.
- Tests: `test/open.test.ts` (`windowKind`), `test/uri.test.ts`, `test/manifest.test.ts`, `test/extension.test.ts`.

**Interfaces:**
- Produces:
  - `windowKind`, `WindowKind`, `HUB_FILE`, `HUB_WORKSPACE`;
  - `Ui.openHub`;
  - the `jam.window` value `"hub"`;
  - the `jam.openHub` command.

- [ ] **Step 1: Write the failing tests.**
- `open.test.ts` `describe("windowKind")`. Cases (hub dir `/gs/hub`):
  - the hub file → `{ kind: "hub" }`;
  - a `jam-<id>` remote → `{ kind: "session", id }`;
  - `/gs/hub/Other.code-workspace` → `undefined`;
  - `/elsewhere/hub/Jam Hub.code-workspace` → `undefined`;
  - `/gs/hub-evil/Jam Hub.code-workspace` → `undefined`;
  - an empty window → `undefined`;
  - on darwin, a case-folded hub dir matches; on linux it doesn't.
- `uri.test.ts`: `route("/hub", "")` → `{ kind: "hub" }`; `route("/hub/x", "")` → `bad`.
- `manifest.test.ts`: the commands include `{ command: "jam.openHub", title: "Open Hub", category: "Jam" }`.
- `extension.test.ts` (`describe("hub window")`):
  - `"jam.openHub writes the hub file and opens it locally in a new window"`: `openFolder` is called with a `file:` uri whose path ends `/hub/Jam Hub.code-workspace` and options `{ forceNewWindow: true, forceLocalWindow: true }`. The file's JSON is `{ folders: [] }`.
  - `"a window on the hub file sets jam.window to hub"`.
  - `"vscode://…/hub opens the hub"`.

- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run test/open.test.ts test/uri.test.ts test/manifest.test.ts test/extension.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npm run check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): the hub window and Jam: Open Hub`.

### Task 2: conversation tabs

**Files:**
- Create: `web/vscode/src/panes/conversation.ts`:
  - `class ConversationPanels` with `static viewType = "jam.conversationTab"`;
  - deps: `PortDeps & { extensionUri; auth: Pick<Auth, "session" | "onChange">; createPanel; resolve: (channel: string) => Promise<SessionTarget | "gone">; reporter: Pick<FocusReporter, "pane" | "drop">; onActive: (t: SessionTarget) => void }`;
  - methods:
    - `open(channel: string): void`: validate `ID_RE.chn` or `LEGACY_RE`, reveal an existing tab, else create one in `ViewColumn.Active`;
    - `restore(panel, state)`;
    - `has(channel): boolean`;
    - `reveal(channel: string, anchor?: string): boolean`: true when a tab exists; it reveals the tab and posts `scrollTo`, or keeps the anchor for the tab's next `ready`, as `SessionPaneProvider.scrollTo` does.
  - Each tab:
    - `paneHtml(w, uri, "conversation")`;
    - `wirePane`, whose `state` is `{ signedIn, target }`, where `target` is the resolved target (`null` plus the gone copy on `"gone"`; see Task 4's copy);
    - `onViewing` reports through `reporter.pane(id, { visible: panel.visible && panel.active, channel })`;
    - `panel.onDidChangeViewState`: when it becomes active, call `onActive(target)` and report;
    - the title comes from `tabTitle(target.label)`.
- Tests: `web/vscode/test/conversation.test.ts` (new, modelled on `test/board.test.ts`).

**Interfaces:**
- Consumes: Task 1 (nothing directly); `tabTitle` from `panes/board.ts`; `LEGACY_RE` from `bridge/intents.ts`.
- Produces: `ConversationPanels` with `open`, `restore`, `has` and `reveal`, and the `onActive(target)` callback.

- [ ] **Step 1: Write the failing tests** (`conversation.test.ts`):
  - `"open creates one panel per channel and reveals it the second time"` *(Review Focus 2)*: two `open(CHN)` calls make one `createWebviewPanel` call and one `reveal`;
  - `"restore of a channel already open disposes the revived tab and reveals the open one"`;
  - `"restore with junk state disposes it"`;
  - `"state posts the resolved target and the title follows its label"`: resolve gives `{ session: SES, label: "alpha", channel: CHN }`; state is `{ signedIn: true, target: {…} }`; `panel.title === "alpha"`;
  - `"a chat target has session \"\""`;
  - `"becoming active calls onActive with the target"`;
  - `"reveal posts scrollTo, or holds it for the next ready"`;
  - `"open refuses a bad id"`: no panel is created.
- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run test/conversation.test.ts`. Expected: FAIL (no module).
- [ ] **Step 3: Implement** `panes/conversation.ts`.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npx vitest run test/conversation.test.ts`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): conversation tabs`.

### Task 3: going to a conversation (`goTo`), the pending handoff, `/go` links and notifications

**Files:**
- Modify: `web/vscode/src/open.ts`:
  - add `export const PENDING_MAX_MS = 60_000`;
  - add `export async function goTo(d: GoDeps, channel: string, session: string): Promise<void>`, with `GoDeps = { isConnected(id): boolean; openSession(id): Promise<void>; isHub: boolean; openTab(channel): void; writePending(p: { channel: string; at: number }): Promise<void>; openHub(): Promise<void>; now(): number }`;
  - add `export function readPending(raw: string | undefined, now: number): string | undefined`, which gives the channel if the JSON is valid, the id is valid (`chn_` or `legacy:`) and it is at most `PENDING_MAX_MS` old.
- Modify: `web/vscode/src/uri.ts`: `Route` gains `{ kind: "go"; channel: string; session: string }` for `/go/<chn>`, with optional `session` from the query; anything invalid is `bad`.
- Modify: `web/vscode/src/notify/event.ts`:
  - `NotifyTarget` becomes `{ channel: string; session: string }`, and `notifyTarget(e)` returns it;
  - `targetLink(t, scheme)` = `${scheme}://aethons.jam/go/${t.channel}` + (`?session=${t.session}` when set).
- Modify: `web/vscode/src/notify/notifier.ts`: the deps `openSession` and `openExternal` are replaced by `goTo(channel, session)`.
- Modify: `web/vscode/src/extension.ts`:
  - build `goDeps`. `writePending` writes `hub/pending.json`; in the hub, `openTab` is `conversations.open`;
  - `actionDeps.goTo`, the notifier and the URI `go` route all call `goTo`;
  - in the hub: on activation and on `onDidChangeWindowState` focused, read `hub/pending.json`, delete it, and open the tab if `readPending` gives a channel.
- Tests:
  - `test/open.test.ts` (`goTo`, `readPending`);
  - `test/uri.test.ts`;
  - `test/notify.test.ts`, `test/osnotify.test.ts` and `test/notifier.test.ts` (whichever cover `targetLink` and the click: update them);
  - `test/extension.test.ts`.

**Interfaces:**
- Consumes:
  - Task 1's `ui.openHub` and `kind`;
  - Task 2's `ConversationPanels.open`.
- Produces: `goTo`, `GoDeps`, `readPending`, `PENDING_MAX_MS`, and the `go` route.

- [ ] **Step 1: Write the failing tests.**
- `open.test.ts` `describe("goTo")`:
  - connected session → `openSession` only;
  - hub → `openTab(channel)` only;
  - elsewhere → `writePending({ channel, at: now })`, then `openHub`, in that order;
  - a session with no fresh mark in the hub → `openTab`.
- `open.test.ts` `describe("readPending")`:
  - a valid entry → channel;
  - an entry older than `PENDING_MAX_MS` → `undefined` *(Review Focus 4)*;
  - junk JSON, a bad id or `undefined` → `undefined`.
- `uri.test.ts`:
  - `route("/go/" + CHN, "")` → `{ kind: "go", channel: CHN, session: "" }`;
  - with `session=SES` → that session;
  - a bad channel, a bad session or `/go/x/y` → `bad` *(Review Focus 3)*.
- Notify tests: `targetLink({ channel: CHN, session: SES }, "vscode")` is `vscode://aethons.jam/go/${CHN}?session=${SES}`; without a session there's no query. A toast's Open calls `goTo(CHN, SES)`.
- `extension.test.ts`:
  - `"a rail click in an ordinary window writes pending and opens the hub"`: `hub/pending.json` holds the channel, and `openFolder` is called on the hub file;
  - `"a hub window opens the pending conversation's tab on activation, and deletes the file"` *(Review Focus 1)*: write the file, activate with `workspaceFile` = the hub file, and `createWebviewPanel` is called with `jam.conversationTab`; the file is gone;
  - `"a stale pending file is ignored and deleted"`;
  - `"a bad /go link warns and opens nothing"`.
- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run`. Expected: FAIL in the new cases.
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npm run check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): going to a conversation: its window, else its hub tab`.

### Task 4: the views follow the hub's tab; the conversation view goes

**Files:**
- Modify: `web/vscode/src/focus.ts`: `FocusModel` gains `setSession(s: string | undefined): void`, which changes `windowSession()` and fires `onChange`. `target()` follows it.
- Modify: `web/vscode/src/panes/session.ts`: `SESSION_PANES` becomes `["thinking", "previews", "status", "artifacts"]`, and `scrollTo` goes (it was the Conversation view's).
- Modify: `web/vscode/src/extension.ts`:
  - no `jam.conversation` provider;
  - create `ConversationPanels` with `onActive: (t) => { if (kind?.kind === "hub") model.setSession(t.session || undefined); }`, plus its serializer;
  - `actionDeps.scrollConversation` and `revealConversation`: perform reveals the tab for the current target's channel (`model.target()` → `conversations.reveal(channel, anchor)`), and if there is no tab, opens it first (hub or studio window only);
  - in a studio window, if `!workspaceState.get("jam.conversationTabShown")`, open the own channel's tab after `model.target()` resolves, then set the flag.
- Modify: `web/vscode/src/panes/actions.ts`: `revealConversation` calls a new dep `revealConversation(anchor: string | null)` instead of `executeCommand("jam.conversation.focus")` plus `scrollConversation`.
- Modify: `web/vscode/package.json`:
  - remove the `jam.conversation` view;
  - the four session views get `"when": "jam.window == session || jam.window == hub"`.
- Modify: `web/app/src/vscode/Pane.tsx`: the `none` copy becomes `` `Open a session's conversation to see its ${noun}.` ``, and for `conversation` it is "That conversation is gone, or you can't see it.".
- Tests:
  - `test/focus.test.ts` (`setSession`);
  - `test/session.test.ts` (four panes);
  - `test/actions.test.ts` (`revealConversation` dep);
  - `test/manifest.test.ts`;
  - `test/extension.test.ts`;
  - `web/app/src/vscode/Pane.test.tsx` and `panes.test.tsx` (the copy).

**Interfaces:**
- Consumes:
  - Task 2's `ConversationPanels` (`open`, `reveal`, `onActive`);
  - Task 3's `goTo`.
- Produces: `FocusModel.setSession`, `ActionDeps.revealConversation`.

- [ ] **Step 1: Write the failing tests.**
- `focus.test.ts`: `"setSession changes the target and fires onChange"`.
- `manifest.test.ts`:
  - `views.jam` is `[rail, status, artifacts, previews]` with the new `when`;
  - `jamPanel` is `[thinking]` with the new `when`;
  - there's no `jam.conversation`.
- `extension.test.ts`:
  - `"activate registers five view providers"` (the rail and four session panes);
  - `"in the hub, the views follow the active conversation tab"`: open a tab for CHN, whose session is SES, and make it active; a resolved Status view's state target is SES. A chat tab (session `""`) gives target null;
  - `"a studio window opens its conversation tab once"` *(Review Focus 5)*: first activation creates a `jam.conversationTab` panel; with `jam.conversationTabShown` already set in workspaceState, activation creates none;
  - `"an Artifacts ask reveals the conversation tab and scrolls"`.
- `Pane.test.tsx` and `panes.test.tsx`: the no-target copy is "Open a session's conversation to see its status.", and so on.
- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run` and `cd web/app && npx vitest run src/vscode`. Expected: FAIL in the new cases.
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/app && npm run check && cd ../vscode && npm run check && npm run build`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): the hub's views follow its active tab; conversations are tabs`.

### Task 5: docs

**Files:**
- `docs/usage/jam/vscode.md`:
  - add `## The hub` (opening it: *Jam: Open Hub*, `…/hub`; tabs; views following the active tab; Previews in the browser; the pending handoff from other windows);
  - in *The rail*, clicks now follow the "go to a conversation" rule (the web app stand-in is gone);
  - in *Panes*, the conversation is a tab (in the hub, and the studio window's own tab, opened once) and the sidebar Conversation view is gone. Reveal goes to the tab. Update the empty-states copy;
  - in *Notifications*, a click goes to the window, else the hub tab, and the link is `…/go/<chn>`;
  - update the frontmatter and the `INDEX.md` row (add "the hub").
- `docs/usage/jam/vscode-checklist.md`: hub steps:
  - *Jam: Open Hub*, and again focuses it;
  - a rail click opens a tab, and a second click reveals it;
  - the views follow the tab;
  - a chat tab empties them;
  - a rail click in an ordinary window opens the hub on that conversation, with the hub closed and with it open;
  - a notification click with and without a studio window;
  - a studio window opens its conversation tab once;
  - a reload keeps the tabs.

- [ ] **Step 1:** Make the edits.
- [ ] **Step 2:** Run `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep ERROR`. Expected: no new errors.
- [ ] **Step 3:** Run `grep -n "web app" docs/usage/jam/vscode.md` and check that every remaining mention is still true.
- [ ] **Step 4: Commit** `docs(vscode): the hub`.
