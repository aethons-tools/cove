# Session colors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** every session gets a durable color from a 24-color palette at setup. The color shows in its VS Code studio window's title bar, as a border on its chat bubbles and the composer, and in its status card's icon box.

**Architecture:** a pure Go palette and picker (`internal/jam/sessioncolor.go`), and a durable `session_colors` store table. `Supervisor.Raise` assigns the color. The user API carries `color` on `SessionHead`, `SessionCard`, `Conversation` and `MessageRow`. The web app draws the borders and the icon fill. The VS Code extension opens studio windows from a local `.code-workspace` file carrying `workbench.colorCustomizations`.

**Tech Stack:** Go (store: MemStore and PostgresStore over a shared memState), tygo, React/Mantine and vitest (web/app), TypeScript and vitest (web/vscode).

**Spec:** `docs/superpowers/specs/2026-10-31-session-colors-design.md`

## Global Constraints

- Palette, in order: `#aa3f4e #ab4235 #a84811 #9a5500 #8d5e05 #816501 #736c01 #607204 #427a17 #0a7e3a #057c59 #027a6b #047879 #007785 #027493 #0470a5 #2769b7 #4a61b9 #6359b5 #7652ac #874c9f #94468f #9e427b #a63f66`.
- Every palette color has a contrast of at least 4.5:1 against `#ffffff` (WCAG relative luminance).
- Colors are stored and sent as lowercase `#rrggbb`; `""` means no color.
- A color is never changed once assigned to a session id. `session_colors` rows are never deleted (no removal method exists).
- Assignment is best effort: a store error logs a warning and the session raises without a color.
- Nothing is written into a studio's filesystem.
- Phase, needs-you and card-state colors are untouched.
- Docs are updated in the same change (AGENTS.md rule).

## Review Focus

1. **A store error on assignment.** The raise still succeeds, with no color (Task 3 test `raise survives a color store error`).
2. **More than 24 live sessions.** The picker still returns a palette color and never panics (Task 1 test `full palette repeats farthest`).
3. **A live session with a malformed or empty color in the store** (e.g. a hand-edited row). The picker ignores it (Task 1 test `ignores invalid live colors`).
4. **A message from a session with no color, or from a person.** No border, and no `undefined`/empty style attribute (Task 5 test).
5. **A studio opened before this change.** `focusStudio` with no workspace file falls back to the remote folder URI instead of failing (Task 6 test).

---

### Task 1: Palette and picker (pure Go)

**Files:**
- Create: `internal/jam/sessioncolor.go`
- Test: `internal/jam/sessioncolor_test.go`

**Interfaces:**
- Produces: `var SessionPalette = [...]string{…24 hex…}` and `func PickSessionColor(live []string) string`.

- [ ] **Step 1: Write the failing tests** (package `jam`):
  - `TestSessionPaletteContrast`: `len(SessionPalette) == 24`. Every entry matches `^#[0-9a-f]{6}$`, all are distinct, and each has a WCAG contrast of at least 4.5 against white. The test has its own small luminance helper.
  - `TestPickSessionColor` (table):
    - `nil` → `SessionPalette[0]`.
    - `[]string{SessionPalette[0]}` → the palette color with the largest OKLab ΔE from it; assert it is not `[0]` and that it equals a brute-force max computed in the test using `oklabOf`.
    - **ignores invalid live colors:** `[]string{"", "nope", "#12"}` → `SessionPalette[0]`.
    - **full palette repeats farthest:** live = all 24 palette entries; the result is in `SessionPalette`, and it's deterministic (two calls are equal).
    - **tie broken by palette order:** live = `{SessionPalette[0], SessionPalette[0]}`; the same result as for a single `[0]`.
- [ ] **Step 2:** `go test ./internal/jam/ -run 'SessionPalette|PickSessionColor'` should fail to compile (undefined).
- [ ] **Step 3: Implement**:
  - `oklabOf(hex string) (lab [3]float64, ok bool)`: sRGB decode, then linear, then OKLab with Björn Ottosson's matrices.
  - `PickSessionColor`: for each palette color in order, take the smallest ΔE to the valid live colors (+Inf when there are none). Return the first with the largest such minimum.
  - A doc comment points at the spec.
- [ ] **Step 4:** the same command passes.
- [ ] **Step 5:** commit `feat(jam): the session color palette and picker`.

### Task 2: The `session_colors` store table

**Files:**
- Create: `internal/jam/migrations/0024_session_colors.sql`, `internal/jam/session_colors.go` (interface and MemStore), `internal/jam/pgstore_session_colors.go`, `internal/jam/storetest/sessioncolors.go`
- Modify: `internal/jam/store.go` (embed `SessionColorStore` in `Store`), `internal/jam/memstate.go` (add the `sessionColors map[string]string` field and init it), `internal/jam/pgstore_registry.go` (call `loadSessionColors` next to `loadAppTokens`), `internal/jam/pgstore_testhelpers.go` (add `session_colors` to the TRUNCATE and reset the map), `internal/jam/storetest/conformance.go` (call `runSessionColorConformance`)

**Interfaces:**
- Produces:
  ```go
  // SessionColorStore is the Store's session_colors table: a session id's
  // color, kept after the session ends (rows are never deleted).
  type SessionColorStore interface {
      SessionColor(actorID string) (string, bool)
      // PutSessionColor records actorID's color unless it already has one,
      // and returns the color it has after the call.
      PutSessionColor(actorID, color string) (string, error)
  }
  ```
- Migration: `CREATE TABLE session_colors (actor_id text PRIMARY KEY, color text NOT NULL);` with a header comment in 0023's style.
- Postgres: `INSERT … ON CONFLICT (actor_id) DO NOTHING` inside `registryTx`, then apply to the memState. Load all rows at startup.

- [ ] **Step 1: Conformance tests** in `storetest/sessioncolors.go`, `runSessionColorConformance`:
  - `session_color_absent`: `SessionColor("ses_x")` → `"", false`.
  - `session_color_put_get`: Put `#aa3f4e` → returns `#aa3f4e`; `SessionColor` → `#aa3f4e, true`.
  - `session_color_first_wins`: Put `#aa3f4e`, then Put `#ab4235` → returns `#aa3f4e`; Get gives `#aa3f4e`.
  - `session_color_survives_instance_removal`: `PutInstance(Instance{ActorID: "ses_x"})`, Put a color, `RemoveInstance("ses_x")`; the color is still there.
- [ ] **Step 2:** `go test ./internal/jam/ -run TestMemStoreConformance` (or the name in `store_conformance_test.go`) should fail to compile.
- [ ] **Step 3:** implement the interface, MemStore (`fs.mu` lock and memState map), the migration and PostgresStore, following `app_tokens` (`pgstore_app_tokens.go`).
- [ ] **Step 4:** the hermetic conformance passes. `go vet ./internal/jam/...` is clean. `go test -tags integration ./internal/jam/ -run TestPostgresStoreConformance` compiles (`go vet -tags integration ./internal/jam/`). It skips without a DSN.
- [ ] **Step 5:** commit `feat(jam): session_colors store table`.

### Task 3: Assign the color in `Supervisor.Raise`

**Files:**
- Modify: `internal/jam/supervisor.go` (after the successful `PutInstance`, near line 526)
- Test: the supervisor test file that covers `Raise`. Find it with `grep -ln "func TestRaise\|\.Raise(ctx" internal/jam/*_test.go` and reuse its fake setup.

**Interfaces:**
- Consumes: `PickSessionColor`, `SessionColorStore`.
- Produces: `func (s *Supervisor) assignColor(actorID string)`. It does nothing when the id already has a color. Otherwise the live colors come from `ListInstances()` (phase not `PhaseGone`, excluding actorID) via `SessionColor`, and the pick goes through `PutSessionColor`. Errors only log at Warn.

- [ ] **Step 1: Failing tests:**
  - `raise assigns distinct colors`: raise two sessions; each `SessionColor` is set, and they differ.
  - `re-raise keeps the color`: raise `ses_a`, tear it down, raise `ses_a` again; same color.
  - `raise survives a color store error`: wrap the store so `PutSessionColor` returns an error; `Raise` returns no error and the instance exists.
- [ ] **Step 2:** run them; they fail.
- [ ] **Step 3:** implement `assignColor` and call it after `PutInstance` succeeds.
- [ ] **Step 4:** `go test ./internal/jam/` passes.
- [ ] **Step 5:** commit `feat(jam): a session gets its color when raised`.

### Task 4: `color` in the user API

**Files:**
- Modify: `internal/jam/userui/apitypes.go`:
  - `SessionHead.Color string \`json:"color"\``
  - `SessionCard.Color string \`json:"color"\``
  - `Conversation.Color string \`json:"color"\`` (the home session's)
  - `MessageRow.Color string \`json:"color"\`` (the sender session's)
  - Each with a one-line comment: `"" = none`.
- Modify: `userui/session.go` (`sessionHead`), `userui/card.go` (the caller at line 78 sets `c.Color` from `h.d.Store`/deps), `userui/api_conversation.go:78` (set `conv.Color` beside `conv.Session`), `userui/conversation.go` (`conversation()`: per sender with `m.From.Kind() == ident.Session`, look up once per sender in a local map)
- Regenerate: `go generate ./internal/jam/userui/` → `web/app/src/api/types.ts`
- Tests: the existing userui tests for these handlers (`grep -ln "sessionHead\|apiConversation\|cardOf" internal/jam/userui/*_test.go`)

- [ ] **Step 1: Failing tests:**
  - The session head JSON has `"color":"#aa3f4e"` when the store has it.
  - The rail card has it.
  - A conversation's `color` is the home session's.
  - A session's message row has its color, while a person's message row has `""`.
  - A session with no stored color has `""`.
- [ ] **Step 2:** fail. **Step 3:** implement, then regenerate the types. **Step 4:** `go test ./internal/jam/userui/` passes and `scripts/app-types-check.sh` is clean.
- [ ] **Step 5:** commit `feat(userui): sessions' colors in the user API`.

### Task 5: The web app: bubbles, composer, status card

**Files:**
- Modify: `web/app/src/conversation/Message.tsx` (bubble `Paper`: `style={m.color ? { borderLeft: \`3px solid ${m.color}\` } : undefined}`)
- Modify: `web/app/src/composer/Composer.tsx` (new prop `accent?: string`; the same 3px left border on the composer's outer wrapper when set)
- Modify: `web/app/src/conversation/ConversationView.tsx` (pass `accent={c.color || undefined}`)
- Modify: `web/app/src/session/card/SessionCard.tsx` and `card.css`:
  - The icon box gets `background: card.color` when set.
  - `.sc-ctx` moves out of `.sc-icon`. It becomes a sibling right after it, a flex item: `position: relative; align-self: stretch; width: 6px`. It no longer uses absolute insets.
  - The icon's radius becomes `var(--sc-icon-radius, 8px)`.
- Modify: `web/app/src/vscode/pane.css` (`.sc { --sc-icon-radius: 2px }`)
- Tests: `conversation/Message.test.tsx` (or the existing message test), `composer/Composer.test.tsx`, `session/card/SessionCard.test.tsx`

- [ ] **Step 1: Failing tests:**
  - A message with `color: "#aa3f4e"` renders a bubble whose `style.borderLeft` contains `3px solid`. One with `color: ""` has no `border-left` in its style attribute.
  - The Composer with `accent="#aa3f4e"` has the border; without `accent`, none.
  - SessionCard: `.sc-icon` has the background `#aa3f4e` when `card.color` is set. `.sc-ctx` is not inside `.sc-icon` (`icon.querySelector(".sc-ctx") === null`) but is in the card.
- [ ] **Step 2:** fail. **Step 3:** implement. **Step 4:** `npm run check` in `web/app` passes.
- [ ] **Step 5:** commit `feat(app): session colors on bubbles, composer and the card's icon`.

### Task 6: The VS Code studio window title bar

**Files:**
- Modify: `web/vscode/src/open.ts`
- Modify: `web/vscode/src/extension.ts` (Ui wiring: `studioDir = joinPath(globalStorageUri, "studios")`; `focusStudio`)
- Test: `web/vscode/test/open.test.ts`

**Interfaces:**
- Produces, in `open.ts`:
  - `export function studioWorkspace(id: string, color: string): string`: the JSON text (2-space indent, trailing newline) of `{ folders: [{ uri: remoteUri(id) }], remoteAuthority: "ssh-remote+jam-<id>", settings? }`.
    - `settings` is present only when `color` matches `^#[0-9a-f]{6}$`.
    - Its value is `{ "workbench.colorCustomizations": { "titleBar.activeBackground": color, "titleBar.activeForeground": "#ffffff", "titleBar.inactiveBackground": color + "99", "titleBar.inactiveForeground": "#ffffffaa" } }`.
  - `export const studioFileName = (id: string) => \`jam-${id}.code-workspace\``.
  - `Ui` gains `writeStudioFile(name: string, text: string): Promise<string>` (it writes under the studios dir and returns the file URI string) and `studioFile(name: string): Promise<string | undefined>` (the URI when the file exists).
- `openSession` writes `studioWorkspace(id, s.head.color)` and opens the returned URI in place of `remoteUri(id)`.
- `focusStudio(id)` opens `studioFile(...)` when it exists, else `remoteUri(id)`.
- `windowKind` needs no change: the folder's authority still names the studio. A test proves it.

- [ ] **Step 1: Failing tests:**
  - `studioWorkspace` with a color: the parsed JSON's folder is `remoteUri(id)`, its `remoteAuthority` is right, and its colors are exactly the values above. With `""`: no `settings` key.
  - `openSession` writes the file and calls `openFolder` with the URI that `writeStudioFile` returned.
  - `windowKind` for `{workspaceFile: file:///…/studios/jam-<id>.code-workspace, workspaceFolders: [remoteUri(id)]}` → `{kind: "session", id}`.
  - The focusStudio fallback: there's no file, so `openFolder(remoteUri(id))`. This lives in whichever test covers the goTo deps. If focusStudio stays inline in extension.ts, extract `focusStudioUri(ui, id): Promise<string>` into open.ts and test that.
- [ ] **Step 2:** fail. **Step 3:** implement. **Step 4:** `npx tsc --noEmit -p .` and `npx vitest run` in `web/vscode` pass.
- [ ] **Step 5:** commit `feat(vscode): a studio window's title bar is its session's color`.

### Task 7: Docs

**Files:**
- Create: `docs/usage/jam/session-colors.md`, a leaf. Its frontmatter is in the style of `session-card.md`. It owns the palette, the assignment rule, durability (`session_colors`), where the color shows, and the one-time second window for studios opened before the change.
- Modify:
  - `docs/usage/jam/INDEX.md`: add the row.
  - `docs/usage/jam/session-card.md`: the icon is filled with the session's color (link to the leaf), and the context bar sits right of the icon.
  - `docs/usage/jam/vscode.md`: a studio window opens from `…/studios/jam-<id>.code-workspace`, its title bar colored; link to the leaf.
  - `docs/usage/jam/vscode-checklist.md`: rows for the title bar color, and for the bubble and composer border.
  - The spec's Status line: built (PR #…).
- [ ] Run the docs-audit checker (docs-audit skill) and fix what it reports.
- [ ] Commit `docs: session colors`.
