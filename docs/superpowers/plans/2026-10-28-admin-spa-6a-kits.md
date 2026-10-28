# Admin SPA slice 6a (Kits, without the Draft) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In the admin app, the Config tree's **Kits** node and kit leaves work at parity with the old kit pages, except the Draft:
- the list, with New kit, Import from git, Delete, and the draft chip;
- a kit's page:
  - the version rail with Pin and the diff links;
  - the version view, or diff, with the file list, the file diff and one file;
  - Built on / Effective, Used by, Source (Track / Sync / Untrack), Versions, Push new version, Delete.

The Draft (the editor, uploads, publish) is slice **6b**.

**Architecture:** `adminui/api_kits.go` holds the JSON twins of `kits.go`, `kit_detail.go` and `kit_files_view.go`, calling the same `jam` functions. The version, diff and file selectors are the page URL's `?v=&diff=&file=`, as today. The app keeps them as search params, so they're in-page selectors, not navigation, and links and the back button still work. `ctxRow` gains the raw path so the app can build the `file=` param.

**Tech Stack:** as slice 1.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§3: a kit's page is its overview, with selectors in-page; the Draft becomes a section in 6b). **Ruling:** slice 6 is split into 6a (this) and 6b (Draft), the way the user-area slice 6 was split.

## Global Constraints

- **Parity confirms** (drift note appended as in `kit.html` / `kits.html`):
  - "Delete kit <k> and all its versions?"
  - "Make v<N> the current version of <k>? Roles bound to it raise v<N> from now on."
  - "Stop tracking the source of <k>? Its versions are kept."
  - "<drift> Sync anyway?", "<drift> Track anyway?" and "<drift> Push anyway?" on a managed kit.
- **Delete is disabled** with the title "The built-in default kit is re-seeded at serve start" (default) or "Roles or kits use this kit — rebind them first". In the list: "Roles use this kit — rebind them first".
- **Messages:** the writes' success messages (`pushedMsg`, `sourceMsg`, "<k> current is now v<N>", "<k> no longer tracks a source") come back as `message` and show green.
- **Masking rules stay server-side:**
  - an unparsable kit.yml is shown masked (`maskKitYML`), or hidden with the "kit-hidden-note" text;
  - the push box prefills `pushRaw` (never a hidden text);
  - all shown text is `SetupDisplayText`'d where the template did it (`RawShown`) or the view builder already did (YAML, diff lines, file content).
- **Import, Track and Sync can take seconds** (a git fetch). Their buttons show progress and send once.
- `?file=` naming a file in neither version answers 200 with `file.missing`, and the page says "No such file in either version's files." / "…this version's files.". **Ruling:** the HTML answered 404 with the page. A 200 with a flag keeps the rest of the page.

## Review Focus

1. **A kit whose current version doesn't parse.** The list shows "invalid". The page shows the parse error, the masked text and the masked/hidden note. The push box is prefilled with `pushRaw` only. Tested in Task 1 and Task 3.
2. **A diff between two versions with file changes.** It shows the YAML diff (or "Too large to diff here."), the digest note, the per-file change table (with "… N more changed" and the unchanged count), and a changed file's line diff when chosen. Tested in Task 1 and Task 3.
3. **Pin from the rail on a managed kit** confirms with the drift note. The rail then marks the new current. Tested in Task 3.
4. **A kit file path with spaces or `%`.** The `file=` param round-trips, and the file shows. Tested in Task 1 and Task 3.
5. **A source write without a fetcher** is 404 `not_found`, and the page offers no Track, Sync or Untrack (`canSource` false). Tested in Task 1 and Task 3.

---

### Task 1: Kits API

**Files:**
- Create: `internal/jam/adminui/api_kits.go`, `internal/jam/adminui/api_kits_test.go`
- Modify: `internal/jam/adminui/apitypes.go`, `internal/jam/adminui/kit_files_view.go` (`ctxRow.Raw`, `ctxChange.Raw`: the raw path), and `internal/jam/adminui/api.go` (`registerApp` takes `kitFetcher`; it already has `f`)

**Interfaces:**
- Produces (types, camelCase json, lists never nil):
  ```go
  type KitRow struct { Name string; Current, Count int; Base string; Egress, UsedBy int; Invalid, Draft bool; Managed *Managed `tstype:"Managed | null"`; Attn *AttnFlag `tstype:"AttnFlag | null"` }
  type KitsResponse struct { Kits []KitRow; CanSource bool }
  type SourceInfo struct { Repo, RepoLink, Dir, Ref, Commit, CommitLink string }
  type KitVersionRow struct { N, Prev int; Current bool; Digest, Pushed string; Source *SourceInfo `tstype:"SourceInfo | null"` }
  type KV struct { Key, Value string }
  type KitView struct { N int; ParseErr, RawShown string; Masked, Hidden bool; PushText, YAML, Base, Image string; Egress, Ceiling, Excluded []string; BuildArgs, Secrets, MCPServers, Notes []KV; Prompt, Digest string } // PushText: the push box's prefill
  type KitDiff struct { From, To int; Lines []DiffLine; DigestChanged, TooLarge bool }
  type KitUse struct { Project, Role string; Implicit bool }
  type KitParent struct { Name string; Version int; Follow bool }
  type KitChild struct { Name string; Pinned bool }
  type FileRow struct { Path, Label, Mode string; Size int64 }               // Path raw (the file= param), Label shown
  type FileList struct { N int; Size string; Rows []FileRow; Hidden int }
  type FileChange struct { Change, Path, Label, Mode, Size string }
  type FilesDiff struct { From, To int; Rows []FileChange; More, Unchanged int }
  type KitFile struct { Path string; Diff bool; From, To int; Lines []DiffLine; TooLarge bool; Content, Note string; Missing, Masked, Hidden bool }
  type DraftInfo struct { Base int }
  type KitDetail struct {
      Name string; Managed *Managed `tstype:"Managed | null"`; Current int; Versions []KitVersionRow; View KitView
      Diff *KitDiff `tstype:"KitDiff | null"`; Uses []KitUse; Chain []KitParent; ChainErr string; Effective *KitView `tstype:"KitView | null"`
      UsedByKits []KitChild; IsDefault bool; Source *SourceInfo `tstype:"SourceInfo | null"`; SetWith string; CanSource bool
      Draft *DraftInfo `tstype:"DraftInfo | null"`; Files *FileList `tstype:"FileList | null"`; FilesDiff *FilesDiff `tstype:"FilesDiff | null"`; File *KitFile `tstype:"KitFile | null"`
      Attn *AttnFlag `tstype:"AttnFlag | null"`
  }
  type KitResult struct { Kit KitDetail; Message string }
  ```
- Routes (log lines as the HTML):
  - `GET api/kits`.
  - `POST api/kits {name, config}` → 201 `Created`. Refusals are "name and config are required" (400) and "kit %q already exists; push a new version on its page" (409).
  - `POST api/kits/import {source, name, track}` → 201 `Created` plus `message`. **Ruling:** the response is `{id, path, message}`; `Created` gains an optional `message`, unused elsewhere.
  - `GET api/kits/{name}?v=&diff=&file=` → `KitDetail`, or 404.
  - `POST api/kits/{name}/versions {config}`, `POST …/pin {version}`, `POST|DELETE …/source`, `POST …/sync` → `KitResult`.
  - `DELETE api/kits/{name}` → 204.

  The source routes answer 404 "git actions are not available on this serve" without a fetcher. Errors follow `kitSourceFail`'s mapping (`jam.KitSourceStatus`), as JSON.
- [ ] **Step 1: Write the failing tests** in `api_kits_test.go`. Reuse `kit_detail_test.go` / `kit_files_test.go` fixtures, such as `jam.PushKitFiles` with `kittreetest.Files`.
  - `TestKitsAPI`:
    - create → 201;
    - repeat → 409 with the message;
    - push a new version (message "pushed k v2") and an unchanged one ("k unchanged (current v2)");
    - pin v1 → message;
    - GET `?v=2&diff=1` → `diff` and `filesDiff` set;
    - GET `?file=run.sh` → `file.content`;
    - a file named `a b%.txt` round-trips through `file=`;
    - delete → 204.
  - `TestKitsAPIInvalid`: a kit with a broken current version (as in `attention_internal_test.go`) → the list shows `invalid`; the detail shows `view.parseErr` plus `rawShown`, and `pushText` isn't the hidden placeholder.
  - `TestKitsAPISources`: with `cfgTreeFetcher`-like fake kit fetchers (see `kits_source_test.go`), import with `track` → 201 plus a message, then sync → message, then untrack. Without a fetcher → 404 on each.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run 'KitsAPI'` → FAIL.
- [ ] **Step 3:** Implement, then regenerate types.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): kits API`.

### Task 2: Kits list

**Files:**
- Create: `web/app/src/admin/kits/KitsPage.tsx`, `web/app/src/admin/kits/kits.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (`config/kits`)

**Interfaces:**
- Produces: **KitsPage**, with:
  - the heading "Kits" and its sub;
  - "New kit" (Kit name with `pattern`, Studio kit YAML in a monospace textarea);
  - "Import from git" when `canSource` (Source, Kit name, a "Track this source" checkbox that defaults on), which shows the server's message on success and navigates;
  - a `DataTable` with these columns:
    - Kit: a link, `AttnFlag`, and a "draft" badge;
    - Current: "vN of M";
    - Base: or the "invalid" badge;
    - Egress: "N domain(s)";
    - Roles;
    - Delete: a `ConfirmAction`, or a disabled button with its title.
- [ ] **Step 1: Write the failing tests** in `kits.test.tsx`:
  - rows, including invalid and draft;
  - create navigates;
  - a 409 inline;
  - import posts `{source, name, track: true}`, shows the message and navigates;
  - no Import without `canSource`;
  - Delete is disabled when used.
- [ ] **Step 2:** `npx vitest run src/admin/kits` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin kits list`.

### Task 3: The kit page

**Files:**
- Create: `web/app/src/admin/kits/KitPage.tsx`, `VersionRail.tsx`, `KitViewCard.tsx`, `KitFiles.tsx`, `web/app/src/admin/kits/kit.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (`config/kits/:kit`)

**Interfaces:**
- Produces:
  - **KitPage** reads `v`, `diff` and `file` from the URL's search params. `useKit(name, v, diff, file)` is the query `["admin","kit",name,v,diff,file]`; writes set it from `KitResult` and show the message. The page has:
    - a header: name, `ManagedBadge`, "current vN", "N version(s)", the draft chip (text "draft · base vN"; the Draft section arrives in 6b), and Delete (or disabled);
    - a two-column layout: **VersionRail** on the left, the cards on the right.
  - **VersionRail**, one entry per version:
    - a link `?v=N`;
    - a "current" badge, or Pin (`ConfirmAction` with the parity text);
    - links "diff vPrev" (`?v=N&diff=Prev`) and "diff current" (`?v=N&diff=cur`);
    - the digest (title "Build digest under claude-default's harness: …"), or "invalid";
    - the selected entry is highlighted.
  - **The cards, in order:**
    1. Diff ("Diff vA → vB", the digest note, "close" → `?v=To`), `DiffView`, or "Too large to diff here.".
    2. Files vA → vB: a change table whose rows link `?v=To&diff=From&file=<path>`, plus the counts line.
    3. The diffed file.
    4. The version card. On a parse error it shows the error, the "Raises from this version fail closed…" line, the masked/hidden note and the raw `<pre>`. Otherwise it shows:
       - the chain error banner;
       - `Facts`: Base (+ image), Built on (the chain with follows/pinned, "→ this kit"), Egress, Egress ceiling, Excluded (+ the COV-208 note), Build args, Secret demands, Prompt;
       - Effective, when chained;
       - YAML with a Copy button (`navigator.clipboard`).
    5. Files: path links `?v=N&file=<path>`, Mode, Size, and "N more file(s) not listed".
    6. One file (content, or note, or masked/hidden notes).
    7. Used by: kits (follows/pinned) and roles (with "no kit set").
    8. Source: Repo, Dir, Ref, Last synced; Sync and Untrack when `canSource`; "Not tracked — pushed by hand, or imported once."; the SetWith hint; and "Track…" (Source input) when `canSource` and untracked.
    9. Versions: a table (Version link plus current, Pushed, Source with dir and ref, Commit).
    10. "Push new version": a textarea prefilled with `view.pushText`, plus the hidden/masked notes.
- [ ] **Step 1: Write the failing tests** in `kit.test.tsx`:
  - the rail's links and Pin confirm (with drift on a managed kit);
  - diff mode shows the YAML diff, the files table and its counts;
  - choosing a changed file shows its line diff;
  - a file with a space in its name opens;
  - an invalid current version shows the error and prefills `pushText`;
  - push shows "pushed k v3";
  - with no `canSource`, there's no Track, Sync or Untrack;
  - Track posts the source and shows the message;
  - Delete disabled for the default kit;
  - not found.
- [ ] **Step 2:** `npx vitest run src/admin/kits/kit` → FAIL.
- [ ] **Step 3:** Implement. Lazy-load the admin pages with `React.lazy` if the admin size budget passes 300 KB.
- [ ] **Step 4:** `npm run check` and `scripts/app.sh` → PASS, both budgets within 350 KB.
- [ ] **Step 5: Commit** `feat(app): admin kit page`.

### Task 4: Browser test and docs

**Files:**
- Modify: `internal/jam/adminui/app_browser_test.go` (`TestBrowserAdminKitPin`), `docs/web-app.md`, `docs/usage/jam/ui-console.md`, and the spec's status line.

- [ ] **Step 1:** Write `TestBrowserAdminKitPin`:
  - push two versions of `k`;
  - open `config/kits/k`;
  - click Pin on v1, then confirm;
  - wait for the rail's v1 to show "current";
  - assert the store's current is 1 and there are no page problems.
- [ ] **Step 2:** `just app && just test-browser` → PASS.
- [ ] **Step 3:** Update the docs, then run the docs audit.
- [ ] **Step 4:** `just test && just lint && (cd web/app && npm run check)` → PASS.
- [ ] **Step 5: Commit** `test(admin): browser test for a kit pin; docs`.
