# Admin SPA slice 6b (a kit's Draft) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A kit's **Draft** becomes a section of its page in the admin app, at parity with the old Draft tab. It covers:
- open (from a version) and discard;
- the file tree, marked against the base;
- the editor (save with the etag, the stale-save refusal that keeps your text, the unsaved-edits guard), download, mode, rename and delete;
- add and replace (uploads);
- the changes table, the kit.yml diff, and a file's diff vs the base;
- publish, with Pin followers and the drift confirmations, and the "Published vN" card with its config-repo files.

**Architecture:** `adminui/api_kit_draft.go` holds the JSON twins of the `kit_draft*.go` routes, through the same `jam` draft functions with the same etag (`If-Match`) semantics:
- JSON writes take the draft's `etag` in the body, as the forms do.
- Uploads (add, replace) stay `multipart/form-data` with today's size cap, through the same `draftForm` preamble (its origin guard is a no-op; the API's `sameOriginAPI` already ran).
- A stale save answers **412 `stale`**. The app keeps the typed text, which `EditForm` already does, so the `staleEditor` re-render trick isn't needed.
- `draftRow` gains its raw path.

The app's `KitPage` gains a `DraftSection`. Its selectors are the search params `dfile` and `ddiff=1`, distinct from the overview's `v`/`diff`/`file`.

**Tech Stack:** as slice 1.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§3: "kit: overview …, then **Draft** as a section (the editor, uploads, publish)"; §4: "The kit draft's `etag` becomes `If-Match`…; uploads (draft add and replace) stay multipart with today's size cap…; a stale etag is `412` with a code the editor turns into 'reload or overwrite'"). **Ruling:** the etag travels in the JSON body (as the forms send it) rather than as an `If-Match` header, so the multipart uploads and the JSON writes share one shape. The semantics are the same: `jam` compares it as If-Match. The editor offers **Reload** (refetch, losing the text after a confirm). "Overwrite" isn't offered. Today's page offers none either, and an overwrite would discard another writer's change without them seeing it.

## Global Constraints

- **Parity texts:**
  - "Discard the draft of <k>? Its changes are lost."
  - "Remove <path> from the draft?"
  - "Publish the draft as the next version of <k>?" (+ " <DriftWarning>" when managed)
  - the drift box "I understand this drifts from the config repo", required when managed, and required together with Pin followers when pinning drifts managed followers;
  - the pin hint "kits that follow <k> stay on the version they follow now (v<current>)";
  - the superseded banner;
  - "No problems: the draft can be published.";
  - "Publishing is blocked until these are fixed:", with each problem as `SetupDisplayText`'d;
  - the editor's not-editable notes (`draftEditorFor`);
  - "This file holds invisible or bidirectional characters …";
  - the unsaved-edits prompt "You have unsaved changes in the editor. Discard them?".
- **Messages** (green): "opened a draft of <k> from vN", "discarded the draft of <k>", `savedMsg`, "added <p>", "renamed <a> to <b>", "removed <p>", "<p> is now 0755", "published <k> vN".
- **Size caps:** JSON writes are capped at `draftFormLimit()`, uploads at `draftUploadLimit()`. Over the cap is **413 `too_large`** with `draftTooLarge`'s text.
- **No packed context ever reaches the browser:**
  - the editor refuses a masked kit.yml (`maskedKitYML`);
  - diffs and file views go through the same builders as the old tab (`draftDiff`, `maskedKitData`);
  - the download route is the old one's twin, `application/octet-stream`, `nosniff`, attachment.
- **Audit lines are the old routes'.** `logDraftSave` logs only when the tree changed, never content or a path.

## Review Focus

1. **A stale save** (another tab saved since load) answers 412 with the "…your text is still in the editor — copy it before reloading" text. The editor keeps the typed text and offers Reload. Nothing is saved. Tested in Task 1 and Task 3.
2. **The unsaved-edits guard.** With typed edits, navigating away (another tree node, another file, the back button) asks first. Saving clears it. Tested in Task 3.
3. **A kit.yml with a packed context in the draft.** The editor shows the note, not the text. Its diff and changes table show the placeholder. The JSON never carries the base64. Tested in Task 1.
4. **An upload over the per-file limit** is refused with 413 and the limit text, inline. Tested in Task 1 and Task 4.
5. **Publish on a managed kit without the drift box** is 400 with the old text. With the box ticked, the "Published vN" card lists the config-repo files to write. Tested in Task 1 and Task 4.

---

### Task 1: Draft API

**Files:**
- Create: `internal/jam/adminui/api_kit_draft.go`, `internal/jam/adminui/api_kit_draft_test.go`
- Modify: `internal/jam/adminui/apitypes.go`, `internal/jam/adminui/kit_draft_view.go` (`draftRow.Raw`, `draftRow.Depth`), and `internal/jam/adminui/api.go` (registration)

**Interfaces:**
- Produces (types, camelCase json, lists never nil):
  ```go
  type DraftRow struct { Depth int; Name string; Dir bool; Path, Label, Mode string; Size int64; Change string } // Path raw ("" for a dir), Label shown
  type DraftEditor struct { Path, Label, Mode string; Exec bool; Size int64; ETag string; Editable bool; Text string; Invisible bool; Note string }
  type DraftYAMLDiff struct { From int; Lines []DiffLine; TooLarge, Hidden bool; Note string }
  type ConfigFileRow struct { Path, Change string }
  type Published struct { Version, Base int; Pinned []string; Warning string; Drift bool; ConfigFiles []ConfigFileRow }
  type DraftView struct {
      Base, Current int; Superseded bool; ETag, Actor, Updated string; Managed bool; DriftWarning, DriftConfirm string
      PinDrift []string; PinDriftNote, MaxFile string; Problems []string; Size string; Tree, Removed []DraftRow
      Editor *DraftEditor `tstype:"DraftEditor | null"`; FileMissing bool; Changes *FilesDiff `tstype:"FilesDiff | null"`
      YAMLDiff *DraftYAMLDiff `tstype:"DraftYAMLDiff | null"`; FileDiff *KitFile `tstype:"KitFile | null"`
  }
  type DraftPage struct { Name string; Current int; Versions []int; Draft *DraftView `tstype:"DraftView | null"`; Published *Published `tstype:"Published | null"` }
  type DraftResult struct { Page DraftPage; Message string }
  ```
- Routes (all under `api/kits/{name}/draft`):
  - `GET ?file=&diff=1` → `DraftPage`. It's 404 when the kit is missing. A file that's missing is a 200 with `fileMissing`, or with `fileDiff.missing`.
  - `POST {from}` (open), `POST /discard {etag}`, `POST /save {etag, path, content, executable}`, `POST /rename {etag, from, to}`, `POST /delete {etag, path}`, `POST /mode {etag, path, executable}` and `POST /publish {etag, pin, drift}` each answer `DraftResult`.
  - `POST /add` and `POST /replace` take multipart (`etag`, `path`, `executable`, `file`) and answer `DraftResult`.
  - `GET /download?path=` → the file's bytes, as the old route does.

  `save` and `add` answer the page opened on that file (`file=` the saved path). `delete` answers the bare page. `publish` sets `published`.

  Errors are `draftUIFail`'s mapping, as JSON: a `WriteError`'s status and its fixed text, otherwise 500 "internal error", logged. A stale save is 412 `stale` with `stale.Error()+": "+editorKeptText`.
- [ ] **Step 1: Write the failing tests** in `api_kit_draft_test.go`. Reuse `kit_draft_test.go`'s fixtures.
  - `TestDraftAPI`:
    - open from v1 → message;
    - GET shows the tree with raw paths;
    - save `run.sh` (the editor opens it; message "saved run.sh");
    - a save with the old etag → 412 `stale` and the kept-text message;
    - rename → message;
    - mode → "run2.sh is now 0755";
    - delete → message;
    - add (multipart, with no file) → an empty text file;
    - replace (multipart) → saved;
    - publish → `published.version`;
    - download → the bytes with `Content-Disposition: attachment`.
  - `TestDraftAPIPackedContext`: a base kit.yml with a packed context (`packCtx`) opened in a draft. GET `?file=kit.yml` → the editor isn't editable, with the packed-context note. GET `?file=kit.yml&diff=1` → no `H4sI` anywhere in the body.
  - `TestDraftAPILimits`: a JSON save over `draftFormLimit()` → 413 `too_large`; an upload over `draftUploadLimit()` → 413.
  - `TestDraftAPIPublishManaged`: publishing a config-managed kit without `drift` → 400 with the old text. With it → `published.drift` and `configFiles` listed.
  - The existing draft HTML tests keep passing.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run DraftAPI` → FAIL.
- [ ] **Step 3:** Implement, then regenerate types.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): kit draft API`.

### Task 2: The Draft section: status, tree, open, discard, add and changes

**Files:**
- Create: `web/app/src/admin/kits/DraftSection.tsx`, `web/app/src/admin/kits/useDraft.ts`, `web/app/src/admin/kits/draft.test.tsx`
- Modify: `web/app/src/admin/kits/KitPage.tsx` (render `DraftSection` after the cards; the header's draft chip links `?dfile=` to open it)

**Interfaces:**
- Produces:
  - `useDraft(kit, file, diff)`: the query `["admin","draft",kit,file,diff]`. `useDraftWrite(kit)` is a mutation that takes JSON or `FormData`. On success it sets the query for the answer's file (`dfile` = the path for save/add/replace/rename, else none), shows the message, and refreshes `["admin","kit",kit]` and the kits list (a draft chip and publish change them).
  - `apiAt(...).postForm(path, FormData)` in `client.ts`, sent without a JSON content type.
  - **DraftSection** has these cards:
    - **No draft:** "No draft" with its sub, an "Open from" select (versions, current selected and labelled "(current)"), and "Open draft".
    - **Draft status:** the base, the current, "last saved by … at …", Discard (`ConfirmAction`), and the superseded banner.
    - **Files:** a tree with depth indentation, directories with a trailing "/", each file a link `?dfile=<path>`, Mode, Size, and a "vs vN" change badge. Removed rows link `?dfile=<path>&ddiff=1`.
    - **Add file…:** Path, an optional file input, and Executable; sent as multipart.
    - **Changes vs vN:** the `FilesDiffCard` from 6a, its rows linking `?dfile=…&ddiff=1`.
    - **kit.yml vs vN:** `DiffView`, the note, or too-large.
    - **A file's diff:** `FileCard`.
    - "No such file in the draft." when `fileMissing`.
- [ ] **Step 1: Write the failing tests** in `draft.test.tsx`:
  - no draft → Open from v2 (current) posts `{from: 2}` and shows the status;
  - Discard confirms with the parity text;
  - the tree indents and links `?dfile=`;
  - Add posts `FormData` with the path and the executable flag;
  - the changes rows link the file diff;
  - the superseded banner shows.
- [ ] **Step 2:** `npx vitest run src/admin/kits/draft` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin kit draft section`.

### Task 3: The editor

**Files:**
- Create: `web/app/src/admin/kits/DraftEditor.tsx`, `web/app/src/admin/kits/useUnsavedGuard.ts`, `web/app/src/admin/kits/editor.test.tsx`
- Modify: `web/app/src/admin/kits/DraftSection.tsx`

**Interfaces:**
- Produces:
  - **DraftEditor**, for `draft.editor`, has:
    - a header: label, mode and size, "diff vs vBase" (`?dfile=<path>&ddiff=1`), and Download (an anchor to `/app/admin/api/kits/<k>/draft/download?path=`).
    - when editable: a monospace `Textarea` (no wrap, `spellCheck` off), the invisible-characters hint, an Executable checkbox, and Save, posting `{etag, path, content, executable}`.
    - on a 412 `stale`: the error inline (`EditForm`), the text kept, and a **Reload** button. Reload confirms with the unsaved prompt, then refetches.
    - when not editable: the note, plus a "Set mode" form.
    - "Replace with an upload…": multipart with the hint "up to <maxFile>; the uploaded file's own name is ignored".
    - Rename (a To field prefilled with the path).
    - Delete (`ConfirmAction` "Remove <label> from the draft?").
  - `useUnsavedGuard(dirty: boolean)` uses React Router `useBlocker(dirty)` with a confirm modal ("You have unsaved changes in the editor. Discard them?"), plus a `beforeunload` listener while dirty. The editor is dirty once typed in, and stays dirty after a refused stale save. A successful save clears it.
- [ ] **Step 1: Write the failing tests** in `editor.test.tsx`:
  - save posts the etag and the text;
  - a 412 keeps the typed text and shows Reload;
  - navigating to another file while dirty asks first, and Cancel stays;
  - after a save, navigating doesn't ask;
  - a non-editable file shows its note and "Set mode";
  - Rename posts `{from, to}`;
  - Delete confirms with the parity text.
- [ ] **Step 2:** `npx vitest run src/admin/kits/editor` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin kit draft editor`.

### Task 4: Publish

**Files:**
- Create: `web/app/src/admin/kits/DraftPublish.tsx`, `web/app/src/admin/kits/publish.test.tsx`
- Modify: `web/app/src/admin/kits/DraftSection.tsx`

**Interfaces:**
- Produces:
  - **DraftPublish** has:
    - the problems banner, or "No problems: the draft can be published.";
    - the drift banner when managed;
    - a "Pin followers" checkbox with the hint, plus the pin-drift banner;
    - the drift box: required when managed; when `pinDrift`, required only with Pin followers;
    - Publish, disabled with the title "Fix the problems first" while there are problems, confirming with the parity text.
  - **PublishedCard** shows "Published vN" with "from the draft (base vB)", a "view vN" link (`?v=N`), the pinned lines, the warning, and, when drifting, "Write to config repo" with the instructions and the files list.
- [ ] **Step 1: Write the failing tests** in `publish.test.tsx`:
  - problems disable Publish;
  - a managed kit needs the drift box (Publish stays disabled or the box is required until ticked) and confirms with the drift warning;
  - the pin drift box is required only with Pin;
  - publish posts `{etag, pin, drift}` and shows the Published card with the config files;
  - an upload over the limit (413) shows its text inline in Add.
- [ ] **Step 2:** `npx vitest run src/admin/kits/publish` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` and `scripts/app.sh` (both size budgets) → PASS. If the admin budget passes 300 KB, lazy-load the kits pages with `React.lazy` and record the ruling.
- [ ] **Step 5: Commit** `feat(app): admin kit draft publish`.

### Task 5: Browser test and docs

**Files:**
- Modify: `internal/jam/adminui/app_browser_test.go` (`TestBrowserAdminKitDraft`), `docs/web-app.md`, `docs/usage/jam/kits-drafts-ui.md` (the preview's Draft section), `docs/usage/jam/ui-console.md`, and the spec's status line.

- [ ] **Step 1:** Write `TestBrowserAdminKitDraft`:
  - kit `k` v1;
  - open `config/kits/k`, then "Open draft";
  - open `kit.yml`, change its prompt (`browsertest.SetInput` on the editor's textarea), and Save;
  - Publish;
  - wait for "Published v2";
  - assert the store's current is 2 and the prompt changed;
  - assert there are no page problems.
- [ ] **Step 2:** `just app && just test-browser` → PASS.
- [ ] **Step 3:** Update the docs, then run the docs audit.
- [ ] **Step 4:** `just test && just lint && (cd web/app && npm run check)` → PASS.
- [ ] **Step 5: Commit** `test(admin): browser test for a kit draft; docs`.
