# VS Code hub, slice 5: several Jams — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The extension works with more than one Jam:
- a sign-in and a hub per Jam;
- each window shows one Jam: the hub's own, a studio window's, or an ordinary window's pick, with a switcher;
- the launcher opens its Jam's hub;
- notifications come from every Jam you're signed in to.

A single-Jam user sees no change.

**Architecture:**
- **The Jam list** is `jam.url` (the default) plus a new `jam.urls` setting, normalised and de-duplicated.
- **Sign-in per Jam:** one `Auth` per Jam (the existing class, given a secret key and a token file):
  - the default Jam keeps today's `jam.session` key and `default-app-token.json`;
  - every other Jam uses `jam.session:<url>` and `vscode-<slug>-app-token.json`. The ssh block's ProxyCommand passes `--app vscode-<slug>` to `jam ssh-proxy`.
- **Each window resolves its Jam once at activation** (`windowJam`), and the existing single-Jam plumbing (`jamUrl()`, `current()`, the panes' `auth`) runs on it. Switching an ordinary window's Jam stores the choice and reloads the window.
- **Notifications from other Jams:** every other signed-in Jam gets its own `LiveStream` and `Notifier` in each window, with a lock per Jam. Their clicks go through `goTo`, which now carries the Jam.

**Tech Stack:** TypeScript, VS Code 1.105, vitest (`web/vscode`).

**Spec:** `docs/superpowers/specs/2026-10-30-vscode-hub-design.md`, *Several Jams*, and *Ordinary window* (the switcher). Slices 1–4 are on this branch.

## Global Constraints

- **`jam.urls`:** a string array, `scope: application`, default `[]`, described as "More Jams' addresses, besides `jam.url`; each gets its own sign-in and hub." Invalid entries are skipped and logged once.
- **`jamList(url, urls)`:** `[normalize(jam.url), ...normalize(urls)]`, valid ones only, duplicates removed, in that order. The default Jam is element 0. If `jam.url` is invalid, there is no default and the list is just the valid `jam.urls`.
- **`jamSlug(url)`:** the host with `:` → `-` and every non-`[A-Za-z0-9.-]` byte → `-`.
  - `appFor(url, defaultUrl)`: `"default"` for the default Jam, else `vscode-<slug>`.
  - `hubFileFor(url)`: `Jam Hub (<slug>).code-workspace`. This replaces slice 3's fixed `Jam Hub.code-workspace`; the branch hasn't shipped, so there's no migration.
  - `pendingFileFor(url)`: `pending-<slug>.json` in the hub dir.
- **Secrets:** the default Jam uses `jam.session`; others use `jam.session:<url>`. Token files are `<configDir>/<app>-app-token.json`.
- **The ssh block** gains ` --app <app>` after `--admin-url …` only for a non-default Jam. The default Jam's block is unchanged, so the Go golden fixture still matches.
- **Window Jam:**
  - **hub:** the Jam whose `hubFileFor` matches the workspace file name; a hub file for a Jam no longer listed is "no Jam", and the rail shows sign-in copy.
  - **studio window:** globalState `jam.studioJam.<session>`, recorded by `openSession`, else the default Jam.
  - **ordinary window:** workspaceState `jam.windowJam` if it's in the list, else the default Jam.
- **New commands:**
  - `jam.switchJam`, "Switch Jam…": only when the list has more than one Jam (context key `jam.multiJam`). It picks a Jam, stores `jam.windowJam` and reloads the window. In a hub or studio window it says "This window belongs to <url>." and does nothing.
  - `jam.addJam`, "Add a Jam…": an input box, validated with `normalizeJamUrl`. It appends to `jam.urls` (global), signs in to it, then offers *Open its hub*.
- **`/hub?jam=X`:**
  - X in the list → open X's hub, with no warning;
  - X valid but not in the list → a warning "This launcher is for X, which isn't one of your Jams." with *Add it*, which runs `jam.addJam` prefilled. The hub doesn't open;
  - no `jam` → the window's Jam's hub.
- **`goTo(d, jam, channel, session)`:** as in slice 3, except "this window is the hub" means the hub for `jam`. Pending requests are written to `pendingFileFor(jam)`, and the hub opened is `jam`'s.
  - **Links:** `/go/<chn>` gains an optional `jam=` (validated by `normalizeJamUrl` and required to be in the list; otherwise `bad`). Without it, it uses the window's Jam.
  - A notification's link includes `&jam=<url>` when its Jam isn't the default.
- **Notifications:**
  - one `LiveStream` and `Notifier` per signed-in Jam: the window's Jam as today, and every other listed Jam that has a session;
  - each lock lives in `<globalStorage>/notify/<slug>`; the default Jam keeps `<globalStorage>/notify`;
  - only the window's Jam feeds the rail, badge and panes;
  - streams for other Jams start and stop with their sign-in (`Auth.onChange`).
- **Run checks with:** `cd web/vscode && npm run check` (plus `go test ./cmd/jam/` if the fixture is touched).

## Review Focus

1. **A single-Jam user** (`jam.urls` empty): same secrets key, same token file, the ssh block is byte-identical, no `jam.multiJam`, and no extra streams. *(Tests in Tasks 1, 2 and 5.)*
2. **A studio window opened from Jam B** keeps B after a reload and in its ssh block (`--app vscode-<slugB>`). *(Test in Task 3.)*
3. **An ordinary window switched to B, then B removed from `jam.urls`:** it falls back to the default Jam. *(Test in Task 3.)*
4. **A notification from Jam B in a Jam-A window:** Open writes B's pending file and opens B's hub, not A's. *(Test in Task 5.)*
5. **A launcher for an unlisted Jam:** a warning plus *Add it*; no hub opens and no Jam is added silently. *(Test in Task 4.)*

---

### Task 1: the Jam list and per-Jam names

**Files:**
- Create: `web/vscode/src/jams.ts`, exporting:
  - `jamList(url: string, urls: unknown): { list: string[]; bad: string[] }`;
  - `jamSlug(url: string): string`;
  - `appFor(url: string, defaultUrl: string | undefined): string`;
  - `hubFileFor(url: string): string`;
  - `pendingFileFor(url: string): string`;
  - `hubJamOf(fileName: string, list: string[]): string | undefined`.
  - Use `normalizeJamUrl` from `config.ts`.
- Modify: `web/vscode/src/config.ts`: `settings()` also returns `urls: string[]`, read from `jam.urls`, raw.
- Modify: `web/vscode/package.json`: the `jam.urls` setting.
- Modify: `web/vscode/src/ssh/config.ts`: `BlockOpts` gains an optional `app?: string`. When set, the ProxyCommand reads `… ssh-proxy --admin-url '<url>' --app <app> --participant <studio>`.
- Tests: `test/jams.test.ts` (new), `test/config.test.ts`, `test/sshconfig.test.ts`, `test/manifest.test.ts`.

**Interfaces:** produces everything above; Tasks 2–5 use it.

- [ ] **Step 1: Write the failing tests.**
- `jams.test.ts`:
  - `jamList("https://a.example", ["https://b.example/", "https://a.example", "nope", 3])` → `{ list: ["https://a.example", "https://b.example"], bad: ["nope", "3"] }`;
  - `jamList("", ["https://b.example"])` → `{ list: ["https://b.example"], bad: [] }`;
  - `jamSlug("https://h:8443")` → `h-8443`;
  - `appFor(B, A)` → `vscode-b.example`, and `appFor(A, A)` → `default`;
  - `hubFileFor("https://jam.example.com")` → `Jam Hub (jam.example.com).code-workspace`;
  - `hubJamOf("Jam Hub (b.example).code-workspace", [A, B])` → B; an unknown name → `undefined`.
- `sshconfig.test.ts`: with `app: "vscode-b.example"`, the ProxyCommand line contains ` --admin-url 'https://b.example' --app vscode-b.example --participant `. Without `app`, the output still equals the golden fixture *(Review Focus 1)*.
- `manifest.test.ts`: `configuration.properties["jam.urls"]` is `{ type: "array", items: { type: "string" }, default: [], scope: "application", markdownDescription: expect.any(String) }`.
- `config.test.ts`: `settings((k) => (k === "urls" ? ["x"] : …)).urls` is `["x"]`.

- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run test/jams.test.ts test/sshconfig.test.ts test/manifest.test.ts test/config.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npm run check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): the Jam list and per-Jam names`.

### Task 2: a sign-in per Jam

**Files:**
- Modify: `web/vscode/src/auth.ts`:
  - the constructor deps gain `key?: string` (default `SESSION_KEY`). `session()`, `signIn()` and `signOut()` use `this.d.key`;
  - `onChange` callbacks also receive the Jam URL `(s, url)`. Existing callers ignore the second argument.
- Create: `web/vscode/src/auths.ts`:
  - `class Auths`, with deps `{ make: (key: string, tokenFile: string) => Auth; defaultUrl: () => string | undefined; tokenFile: (app: string) => string }`;
  - `authFor(url): Auth`, which makes and caches one Auth per `(key, tokenFile)`. The default Jam gets `SESSION_KEY` and `tokenFile("default")`; others get `` `${SESSION_KEY}:${url}` `` and `tokenFile(appFor(url, default))`;
  - `onChange(cb: (url: string, s: Session | undefined) => void): () => void`, for every Auth made, past and future.
- Modify: `web/vscode/src/extension.ts`: build `auths`. `auth` (the window's) is `auths.authFor(windowJamUrl)` once Task 3 lands; in this task, it is `auths.authFor(jamSettings().url)` when that is valid.
- Tests: `test/auth.test.ts` (the key), `test/auths.test.ts` (new).

**Interfaces:**
- Consumes: Task 1's `appFor`.
- Produces: `Auths.authFor`, `Auths.onChange`, and `Auth` with a `key`.

- [ ] **Step 1: Write the failing tests.**
- `auth.test.ts`: `"a custom key stores and reads its own secret"`. Sign in with `key: "jam.session:https://b"`; the store holds that key and not `jam.session`.
- `auths.test.ts`:
  - `"the default Jam keeps jam.session and default-app-token.json"` *(Review Focus 1)*;
  - `"another Jam gets jam.session:<url> and vscode-<slug>-app-token.json"`;
  - `"authFor caches per Jam"`;
  - `"onChange reports the Jam of a sign-out"`.
- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run test/auth.test.ts test/auths.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npm run check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): a sign-in per Jam`.

### Task 3: each window's Jam; hubs, studio windows and the switcher

**Files:**
- Modify: `web/vscode/src/open.ts`:
  - `windowKind` returns `{ kind: "hub"; file: string }` (the workspace file's base name) for any `Jam Hub (….code-workspace` in the hub dir;
  - `HUB_FILE` is removed in favour of `hubFileFor`;
  - add `export function windowJam(k: WindowKind | undefined, list: string[], o: { studioJam(id: string): string | undefined; chosen: string | undefined }): string | undefined`, implementing the Global Constraints rule.
- Modify: `web/vscode/src/extension.ts`:
  - compute `list` and `windowJamUrl` at activation;
  - `jamUrl()` returns `windowJamUrl` (or an Error with today's copy when there is none);
  - `auth` is `auths.authFor(windowJamUrl)`;
  - `openDeps.signedIn` and `ssh` use it;
  - `ensureSsh` gets `app: appFor(jam, default)` for a non-default Jam;
  - `openSession` records `jam.studioJam.<id>` = the opening window's Jam (via an `OpenDeps.remember(id)` hook);
  - `ui.openHub(jam?)` writes and opens `hubFileFor(jam ?? windowJamUrl)`;
  - pending uses `pendingFileFor`;
  - set `jam.multiJam`;
  - register `jam.switchJam`, which stores `jam.windowJam` and runs `workbench.action.reloadWindow`.
- Modify: `web/vscode/src/ssh/setup.ts`: `SshDeps` gains `app?: string`, passed to `renderBlock`.
- Modify: `web/vscode/package.json`: the `jam.switchJam` command, with a `commandPalette` entry whose `when` is `jam.multiJam`.
- Tests: `test/open.test.ts` (`windowKind`, `windowJam`), `test/setup.test.ts` (`app`), `test/manifest.test.ts`, `test/extension.test.ts`.

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces: `windowJam`; `ui.openHub(jam?: string)`; `OpenDeps.remember`; `GoDeps` keyed by the window's Jam (Task 5 extends it).

- [ ] **Step 1: Write the failing tests.**
- `open.test.ts` `windowJam`:
  - a hub with `file` = `hubFileFor(B)` → B;
  - a hub for an unlisted Jam → `undefined`;
  - a studio window with `studioJam` B → B; with none → the default;
  - ordinary with `chosen` B (listed) → B; with B unlisted → the default *(Review Focus 3)*;
  - an empty list → `undefined`.
- `setup.test.ts`: `ensureSsh({ …, app: "vscode-b" })` writes a block containing `--app vscode-b`.
- `extension.test.ts`:
  - `"a studio window opened from Jam B uses B after a reload and in its ssh block"` *(Review Focus 2)*: `jam.urls = [B]`, a window with `jam.windowJam = B` opens a session. globalState gets `jam.studioJam.<id>` = B, and the ssh config block has `--app vscode-b.example`. Then activate a studio window for that id: its first API call goes to B.
  - `"Switch Jam stores the choice and reloads"`: the quick pick answer is B, workspaceState `jam.windowJam` = B, and `executeCommand("workbench.action.reloadWindow")` runs.
  - `"Switch Jam in a hub says the hub belongs to its Jam"`.
  - `"jam.multiJam is false with one Jam"` *(Review Focus 1)*.
  - `"each Jam's hub is its own workspace file"`: `jam.openHub` in a B window opens `…/hub/Jam Hub (b.example).code-workspace`.
- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run`. Expected: FAIL in the new cases.
- [ ] **Step 3: Implement** as listed under **Files**. Update every existing test that used `Jam Hub.code-workspace` to `hubFileFor(JAM)`.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npm run check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): each window's Jam, a hub per Jam, Switch Jam`.

### Task 4: adding a Jam, and launchers for other Jams

**Files:**
- Modify: `web/vscode/src/extension.ts`:
  - register `jam.addJam`: `showInputBox({ prompt: "A Jam's address", value: prefill, validateInput })` → append to `jam.urls` (global) → `auths.authFor(url).signIn(url)` with progress → offer *Open its hub*;
  - the `/hub?jam=` route follows the Global Constraints rule.
- Modify: `web/vscode/package.json`: the `jam.addJam` command.
- Tests: `test/manifest.test.ts`, `test/extension.test.ts`.

**Interfaces:**
- Consumes: Task 3's `ui.openHub(jam)`.

- [ ] **Step 1: Write the failing tests** (`extension.test.ts`):
  - `"Add a Jam appends it to jam.urls and signs in"`: with an input box answer of `https://b.example`, the `jam.urls` update is `["https://b.example"]` (global), and the consent URL opened is on B;
  - `"a launcher for a listed Jam opens its hub quietly"`;
  - `"a launcher for an unlisted Jam warns with Add it and opens nothing"` *(Review Focus 5)*: the warning is shown with `"Add it"`, there's no `openFolder`, and `jam.urls` is unchanged;
  - `"Add it runs Add a Jam prefilled"`.
- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run test/extension.test.ts test/manifest.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement** as listed under **Files**. Slice 4's "This Jam Hub launcher is for X, but jam.url is Y." warning and its tests are replaced.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npm run check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): Add a Jam; launchers open their Jam's hub`.

### Task 5: notifications and going to conversations across Jams

**Files:**
- Modify: `web/vscode/src/open.ts`:
  - `goTo(d, jam, channel, session)`;
  - `GoDeps` gains `windowJam: string | undefined`, and `writePending(jam, p)`, `openHub(jam)` and `isHub` become relative to `jam`: the hub tab opens only when `d.isHub && jam === d.windowJam`.
- Modify: `web/vscode/src/uri.ts`: the `go` route gains `jam: string` (`""` when absent; validated by the extension against the list).
- Modify: `web/vscode/src/notify/event.ts`: `targetLink(t, scheme, jam?)` appends `&jam=<encoded>` when given.
- Modify: `web/vscode/src/notify/notifier.ts`: the deps gain `jam: string` and `linkJam: string | undefined` (set for a non-default Jam). `goTo(channel, session)` keeps its shape: the extension binds the Jam.
- Modify: `web/vscode/src/extension.ts`:
  - `class OtherJams` (or a function), for each listed Jam other than the window's: when `auths.authFor(j).session(j)` exists, start a `LiveStream` and a `Notifier` (lock dir `notify/<slug>`) bound to `goTo(j, …)`. Stop it on that Jam's sign-out and restart it on sign-in;
  - the rail's `go` intent and the window's notifier use `goTo(windowJam, …)`;
  - the `/go` route uses `jam || windowJam`.
- Tests: `test/open.test.ts` (`goTo`), `test/uri.test.ts`, `test/notifyevent.test.ts`, `test/notifier.test.ts`, `test/extension.test.ts`.

**Interfaces:**
- Consumes: Tasks 1–3.

- [ ] **Step 1: Write the failing tests.**
- `open.test.ts` `goTo`:
  - from a Jam-A hub, Jam B's conversation → `writePending(B, …)`, then `openHub(B)`;
  - Jam A's conversation → `openTab`.
- `uri.test.ts`: `/go/<chn>?jam=https%3A%2F%2Fb.example` → `jam: "https://b.example"`.
- `notifyevent.test.ts`: `targetLink(t, "vscode", "https://b")` → `…/go/<chn>?session=<ses>&jam=https%3A%2F%2Fb`.
- `extension.test.ts`:
  - `"a signed-in second Jam gets its own events stream; signing out of it stops it"`: `jam.urls = [B]`, both have sessions, both `/app/user/events` are fetched, and after `auths.authFor(B).signOut()` B's stream aborts *(Review Focus 1: with one Jam, only one stream)*;
  - `"a notification from Jam B in a Jam-A window opens B's hub"` *(Review Focus 4)*: an event on B's stream, then toast Open → `hub/pending-b.example.json` is written, and `openFolder` runs on `Jam Hub (b.example).code-workspace`;
  - `"a /go link with jam= for an unlisted Jam warns"`.
- [ ] **Step 2: Run, and see them fail.** Run: `cd web/vscode && npx vitest run`. Expected: FAIL in the new cases.
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/vscode && npm run check && npm run build`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(vscode): notifications and links across Jams`.

### Task 6: docs

- `docs/usage/jam/vscode.md`:
  - *Settings*: `jam.urls`;
  - *Sign in and out*: one sign-in per Jam; the secrets keys and token files; *Jam: Add a Jam…*;
  - *What it writes*: `vscode-<slug>-app-token.json`, and the `--app` in a non-default Jam's block.
- `docs/usage/jam/vscode-hub.md`:
  - *Several Jams*: a hub and launcher per Jam; each window's Jam; *Jam: Switch Jam…*; a launcher for an unlisted Jam; notifications from every signed-in Jam; links carrying `jam=`;
  - change the launcher bullet "one hub per Jam comes later" to describe the new behaviour;
  - the hub file name is now `Jam Hub (<host>).code-workspace`.
- `docs/usage/jam/vscode-checklist.md`, steps for a second Jam:
  - add it, then sign in;
  - its hub and launcher;
  - Switch Jam;
  - a studio window from B reloads on B;
  - a notification from B in an A window opens B's hub;
  - remove B from `jam.urls`, and an ordinary window falls back to the default.
- [ ] **Step 1:** Make the edits.
- [ ] **Step 2:** Run `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep ERROR`. Expected: no new errors.
- [ ] **Step 3: Commit** `docs(vscode): several Jams`.
