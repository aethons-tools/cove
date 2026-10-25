# Kit drafts — slice 4 (the Draft tab) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The kit page gets a **Draft** tab. On it an operator opens a kit's draft, sees its files as a tree marked against the base version, edits text files in a `<textarea>`, uploads, replaces and downloads other files, adds, renames and deletes files, toggles the executable bit, reads the diff against the base, and publishes the draft. Publishing a config-managed kit shows the drift warning first. Afterwards it lists the files to commit and the `at-jam config write <repo>` command. Every form carries the draft ETag, and a stale save keeps the typed text and never saves it.

**Architecture:**
- **Server-rendered, like the rest of `internal/jam/adminui`.** The tab is the sub-path `GET /app/admin/kits/{name}/draft`. It is a page set of its own (`"kit-draft"`: `kit_shared.html` + `kit_draft.html`) with the `nav.subtabs` Overview | Draft pattern of the role and agent Setup tabs.
- **Writes are htmx form POSTs.** They go to `/app/admin/kits/{name}/draft[/…]`, each through `guardWrite` (`originGuard`). They call the slice-3 jam functions directly (`OpenKitDraft`, `PutKitDraftFile`, `RenameKitDraftFile` (new), `RemoveKitDraftFile`, `DiscardKitDraft`, `PublishKitDraft`). The form's hidden `etag` is passed as `ifMatch`. Each write answers the re-rendered `#draft` body plus an out-of-band `#flash`.
- **One new jam function.** `RenameKitDraftFile` in `internal/jam/kit_draft_files.go` does remove + put in one `saveDraftLocked` under `kitMu`.
- **The diff reuses the slice-1 kit-page rendering.** `ctxDiff` gains a link-parametrised core (`ctxDiffLinked`), the kit.yml line diff becomes `yamlDiff`, and `fileView` and the `ctx-file`/`diff-lines` templates are shared. The draft side is read into the same `ctxRead` shape (`draftRead`).

**Tech Stack:** Go 1.26, `net/http` ServeMux patterns, `html/template`, the vendored htmx 2 (`hx-post`, `hx-encoding="multipart/form-data"`, `hx-confirm`, `HX-Redirect`, `hx-swap-oob`), `mime/multipart`, `mime`, the `jam` store (MemStore in tests), `kittree`, `kittreetest`, `httptest`.

**Spec:** [`docs/superpowers/specs/2026-10-24-kit-trees-design.md`](../specs/2026-10-24-kit-trees-design.md), §3 "The authoring loop": the **UI** bullet ("A **Draft** tab on the kit page: file tree; text editor for UTF-8 files ≤ 1 MiB, upload/replace otherwise; add, rename, delete; diff vs the base version") and the **Publish** bullet's UI half ("For a config-managed kit, publish carries the drift warning and the UI offers 'write to config repo' (the files to commit) instead"). Also §5 errors. Slice 3 (drafts: API, CLI, sync, publish) is merged (#484). Its plan, [`2026-10-25-kit-drafts-slice3.md`](2026-10-25-kit-drafts-slice3.md), owns every jam draft rule this plan calls. Test builds and draft studios are slice 5.

## Spec deviations, interpretations and rulings

The first twelve are the binding design rulings handed to this plan. The rest are rulings this plan makes where the spec and those rulings are silent.

1. **Rendering.** Server-rendered `html/template` with the htmx already vendored. No new JS library, no CDN asset, no new `<script>` element, no new `hx-on` handler. The admin UI sets no CSP header today; this keeps it working under the strictest one the layout already tolerates. The editor is a plain monospace `<textarea>`.
2. **Routes.** The Draft tab is `/app/admin/kits/{name}/draft`, a sub-path, which is the page family's existing tab pattern (`…/setup` on role and agent pages, `nav.subtabs`). Every POST goes through `guardWrite` (`originGuard`). The UI calls jam functions directly, never the HTTP admin API. The admin role is the mount's `appui.RequireAdmin` around `/app/admin/*`, as for every adminui write: no extra in-handler role check (ruling 14).
3. **ETag in the UI.** Each form carries the draft ETag in a hidden `etag` field and passes it as `ifMatch`. A stale save re-renders the editor with the user's submitted text preserved. It adds the fixed message `draft <kit> changed since you loaded it (reload)` and a reload link. Typed work is never lost and never saved.
4. **Editability.** A file is edited as text only when it is valid text (`kittree.IsText`) and ≤ `maxDraftEditBytes = 1 << 20` (a const in adminui). Any other file shows name, size and mode, with **Download** and **Replace**. Download is served `Content-Disposition: attachment`, `application/octet-stream`, `nosniff`, `no-store`, as the API serves it. Replace is a multipart upload capped at `kittree.MaxFile`, with a friendly 413. A mode toggle (an executable checkbox) sets 0755 or 0644.
5. **Add, rename, delete.** Add takes a path input and an empty text or an upload. Rename is one jam function, `RenameKitDraftFile`, doing remove + put in one `saveDraftLocked` under `kitMu`. Delete asks for confirmation.
6. **File tree.** It is nested by directory from the sorted entries, each with mode and size. Changed-vs-base markers (added / modified / mode / removed) come from the draft status's `Changes` (`kittree.Changes`).
7. **Diff vs base.** It reuses the slice-1 kit-page file-diff rendering (`ctxDiff`, `fileView`, the `ctx-file` and `diff-lines` templates) for the draft tree against its base version, plus the kit.yml line diff.
8. **Problems and publish.** A problems panel lists `draftProblems`. The **Publish** button is disabled while any exist, and the server refuses anyway. The publish form has a **Pin followers** checkbox. The result banner shows the new version, the pinned followers and the warning(s). For a config-managed kit, `DraftDriftWarning` is shown before publishing. After publishing comes a "write to config repo" panel: the `at-jam config write <repo>` command and the changed files, paths only.
9. **Open and discard.** The kit page has an **Open draft from v<N>** button (N = the viewed version). The tab's own form opens from any version, current by default. A superseded draft shows a "base vN, current vM" notice. **Discard** asks for confirmation. The kits list marks a kit with a draft.
10. **No markup injection.** Draft file contents are never rendered as HTML: everything goes through `html/template` contexts. Paths are shown escaped (`pathText`). Uploaded file names are ignored, and the path always comes from the form.
11. **Docs.** `docs/usage/jam/ui-pages.md` (kit page section) owns the Draft tab. `kits-drafts.md` links to it. One fact lives in one place. The docs audit gains no errors beyond the new plan file's own.
12. **Repo rules.** `GOPROXY=https://proxy.golang.org,direct`. Checks are `just test`, `just lint` (exit code) and `go vet -tags integration ./...`. Tests are hermetic and code is gofmt'd.
13. **`RenameKitDraftFile` takes `now`.** Its signature is `(store Store, log *slog.Logger, name, from, to, ifMatch, actor string, now time.Time) (KitDraftSave, error)`, like every other draft write.
    - A taken target answers 409 `draft <kit> already has a file <to>`.
    - Renaming a file to its own path is a no-op save.
    - The draft's `.dockerignore` applies to the result as on a put: a target it excludes is refused, and a file renamed *to* `.dockerignore` drops what it newly excludes.
    - No admin API route or CLI verb is added: the API and CLI can already `rm` + `put`, and a route is a slice of its own.
14. **The admin gate.** `/app/admin/*` is mounted in `cmd/at-jam/main.go` as `d.Gate.Wrap(RequireAdmin(…))` (`internal/jam/appui/mux.go`). adminui handlers add `originGuard` and log `jam.OperatorID(r)`. Tests drive `adminui.Handler` directly, as every existing adminui test does.
15. **Paths travel in form fields and the query**, never in the URL path: `?file=<p>`, `/draft/download?path=<p>`, and hidden `path`/`from` fields. This avoids ServeMux cleaning and redirects, and the API's `draftFilePathGuard` concern. A `?file=` that is unsafe or absent renders "No such file in the draft." with a 404 and never echoes the value.
16. **Line breaks (the CRLF/BOM ruling).**
    - HTML form submission sends a textarea's line breaks as CRLF. The server turns every CRLF, and any lone CR, into LF (`editorText`).
    - So a file that contains any `\r` is never offered in the editor. It is shown read-only with the note "contains carriage returns (CRLF line ends): the browser editor would rewrite them as LF — Download it, edit it locally, and Replace it".
    - The template writes one newline right after the `<textarea …>` start tag. The HTML parser drops exactly one there, so a file that begins with a newline keeps it.
    - A BOM and other format characters (U+202E, …) are kept byte-exact. They get a note: `jam.SetupDisplayText(text) != text` → "This file holds invisible or bidirectional characters…".
    - Proven by a GET → browser-parse → CRLF-submit → byte-compare test.
17. **The stale save answers 200.**
    - htmx 2 swaps only 2xx bodies (the layout's `htmx:responseError` handler shows 4xx as plain text). So a 412 from `PutKitDraftFile` re-renders `#draft` with status 200.
    - The editor holds the submitted text, keeps the form's own stale ETag so Save stays refused, has Save disabled, and shows the fixed message with a reload link (`?file=<p>`).
    - Every other stale write (discard, replace, mode, add, rename, delete, publish) answers 412 with the fixed text through the flash banner. htmx then leaves that form as typed.
    - If the draft is gone by the time the stale page is built, the 412 itself is answered, and the typed text stays in the browser.
18. **The editor's forms carry the ETag the file was read at** (`KitDraftFile`'s third result), not the status's. A save between the status read and the file read can then never be overwritten by older text. The other forms carry the status ETag.
19. **Save size.** The editor saves up to `kittree.MaxFile`. Its urlencoded body is capped at `3*kittree.MaxFile + 64 KiB` (percent-encoding is at most 3 bytes a byte). A larger text is refused by jam's own 413 `draft <kit>: <path>: more than 4 MiB (limit 4 MiB per file)`. The editor re-opens a file only while it is ≤ 1 MiB.
20. **Upload size.**
    - The multipart body is capped at `kittree.MaxFile + 1 MiB`. It is checked against `Content-Length` first and by `http.MaxBytesReader` as it reads. Past the cap the answer is 413 `draft <kit>: the file is over 4 MiB (limit 4 MiB per file)`.
    - The `file` part is read up to `MaxFile+1`, so an oversize file within the body cap meets jam's own 413.
    - `ParseMultipartForm`'s memory bound equals the cap, so nothing spills to disk, and `RemoveAll` runs anyway.
21. **Add never overwrites.**
    - An add first reads the path with `KitDraftFile`. A file already there, at the ETag the form carries, is refused 409 `draft <kit> already has a file <path>`.
    - At another ETag the following put is stale (412).
    - An unsafe path is refused by jam's `CheckPath` text, unechoed.
    - The add and rename-to path inputs are trimmed of surrounding whitespace.
22. **Mode toggle for a non-editable file** is `KitDraftFile` then `PutKitDraftFile` with the form's ETag, so a concurrent save makes it stale. An editable file's mode rides the save form's checkbox.
23. **Download names the file** by its last segment through `mime.FormatMediaType("attachment", {"filename": …})` (RFC 2231 for non-ASCII). If that cannot encode the name, the header is a bare `attachment`.
24. **The tree is not capped.** `kittree.MaxEntries` (2000) bounds it. Removed files are listed after the tree, struck through, linking their diff.
25. **Masking.** In the diffs, the draft's `kit.yml` goes through `maskKitYML`, as the kit page treats a stored kit.yml. The editor shows the raw text, since it is what is being edited.
26. **The config-repo panel.** It lists `kits/<kit>/<path>` with its change kind for each file that differs between the version current before the publish and the published one, only when the kit itself is config-managed. It adds `kits/<f>/kit.yml` for each managed follower pinned. The command is literally `at-jam config write <repo>`. `config write` renders the files; the list says which to expect in `git diff`.
27. **Audit.** UI writes log `ui kit draft opened|saved|discarded|published` with the operator, the kit, the base or version, a 12-hex tree prefix and the count `changed`. They never log content or a path. A store fault answers a fixed 500 `internal error` and logs `ui kit draft: store error`.
28. **Problems are shown through `displayText`**, so control and format characters are visible, on top of html escaping.
29. **Consistency.** A page reads the status, the editor's file and kit.yml as separate `kitMu` snapshots. Only display may mix across a concurrent save, and every write is ETag-checked.
30. **Docs size.** `ui-pages.md` is already over the 200-line leaf budget (a pre-existing WARN). The Draft-tab subsection keeps it a WARN, never an error. Splitting the page is a follow-up and out of scope here.

## Global Constraints

- **UI (spec §3, verbatim):** "A **Draft** tab on the kit page: file tree; text editor for UTF-8 files ≤ 1 MiB, upload/replace otherwise; add, rename, delete; diff vs the base version."
- **Publish (spec §3, verbatim):** "For a config-managed kit, publish carries the drift warning and the UI offers 'write to config repo' (the files to commit) instead."
- **Error text (spec §5, exact):** `draft app changed since you loaded it (reload)`. Errors are fixed text, located by path, and never echo file contents or an unsafe name.
- **Fixed texts this plan adds:**
  - `draft app already has a file <path>` (409: rename and add)
  - `draft app: the file is over 4 MiB (limit 4 MiB per file)` (413: a form body past its cap)
  - `choose a file to upload`
  - `the upload could not be read`
  - `No such file in the draft.`
  - `internal error` (500)
- **Constants:**
  - `maxDraftEditBytes = 1 << 20` (adminui).
  - The form body cap is `3*kittree.MaxFile + 64<<10`.
  - The upload body cap is `kittree.MaxFile + 1<<20`.
  - `kittree.MaxFile` is a var (tests shrink it) and is read at request time.
- **Security:**
  - Every Draft-tab POST calls `guardWrite` before it reads anything. A cross-origin POST answers 403 and changes nothing.
  - Paths are `CheckPath`'d by jam before any text names them.
  - Uploaded file names are never read.
  - File content reaches HTML only inside `<textarea>` (RCDATA, escaped) or `diff-lines`/`ctx-file` (escaped after `SetupDisplayText`).
- **Assets:** no new JS, CDN, `<script>` or `hx-on`; the `<style>` stays in templates as today.
- **Repo:** `GOPROXY=https://proxy.golang.org,direct`; `gofmt -l .` empty; `just test`, `just lint` (exit 0), `go vet -tags integration ./...`; hermetic tests (MemStore, `httptest`, no network).
- **Docs:** `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` reports no more errors than the baseline after this plan's own commit (741 before it, so 743 with it).

## Review Focus

1. **Two browser tabs on one file.** Tab 2 saves first, then tab 1 saves. Tab 1 must get the fixed stale message, a reload link, its own typed text in the textarea (CRLF already folded), Save disabled and its stale ETag kept. The stored file must be tab 2's. Test: `TestDraftStaleSaveKeepsText` (Task 3).
2. **Byte-exact round trip through the textarea.** A BOM, a leading blank line, a missing final newline, tabs, a bidi override and markup must all round-trip GET → browser parse → CRLF-submit → stored bytes unchanged ("unchanged", ETag kept). A CRLF file is offered read-only. Test: `TestDraftEditorRoundTripsBytes` (Task 3).
3. **A 1 MiB + 1 byte text file** is not editable: it shows the note, Download and Replace. Exactly 1 MiB is still editable. A binary file is not editable. Test: `TestDraftEditorNotEditable` (Task 3).
4. **Hostile paths.** A path with a U+202E override, `..`, an absolute path, an escape sequence or an empty segment is refused by `CheckPath` on add and on rename, never echoed, the draft unchanged. A graphic name with markup (`docs/<b>名前</b>.md`) is accepted and shown escaped. Test: `TestDraftHostilePaths` (Task 5).
5. **Publish with problems.** The button is `disabled`, the problems are listed, and a crafted POST is still refused (400 `draft app has problems: …`) with nothing published and the draft kept. Test: `TestDraftPublishBlockedByProblems` (Task 7).

(CSRF via a cross-origin form POST is a Global Constraint, pinned by `TestDraftWritesRefuseCrossOrigin` in Task 2. Every later task appends its routes to its table.)

---

## File structure

| File | Responsibility | Task |
|---|---|---|
| `internal/jam/kit_draft_files.go` | + `RenameKitDraftFile` | 1 |
| `internal/jam/kit_draft_files_test.go` | + `TestRenameKitDraftFile` | 1 |
| `internal/jam/adminui/kit_draft_view.go` | Draft tab view model: `draftURL`, `draftFileHref`, `draftQuery`, `draftPage`, `draftView`, `draftRow`, `draftTree`, `newDraftView`, `buildDraftPage` | 2 (3, 6, 7 extend) |
| `internal/jam/adminui/kit_draft.go` | `registerKitDraft` (GET tab, open, discard), `draftForm`, `draftUIFail`, `loadDraftPage`, `writeDraftBody`, `renderDraftBody`, body caps | 2 |
| `internal/jam/adminui/kit_draft_editor.go` | editor model, save (stale preservation), download | 3 |
| `internal/jam/adminui/kit_draft_upload.go` | `uploadedFile`, replace, mode | 4 |
| `internal/jam/adminui/kit_draft_paths.go` | add, rename, delete | 5 |
| `internal/jam/adminui/kit_draft_diff.go` | `draftRead`, `draftDiff` | 6 |
| `internal/jam/adminui/kit_draft_publish.go` | publish, `publishView`, `configFiles` | 7 |
| `internal/jam/adminui/templates/kit_shared.html` | `kit-style`, `kit-masked-note`, `kit-hidden-note`, `ctx-file` (moved from kit.html), `ctx-diff-rows` (6) | 2, 6 |
| `internal/jam/adminui/templates/kit_draft.html` | the Draft tab (`content`, `draft-body`) | 2–7 |
| `internal/jam/adminui/templates/kit.html` | subtabs, draft chip, Open draft button; uses shared templates | 2, 6 |
| `internal/jam/adminui/templates/kits.html` | draft marker | 2 |
| `internal/jam/adminui/{adminui,kits,kit_detail,kit_files_view}.go` | page set, funcs, registration, `writeFlash`, `kitDetail.Draft`, `kitListRow.Draft`, `fileModeText`, `ctxDiffLinked`, `yamlDiff` | 2, 6 |
| `internal/jam/adminui/kit_draft_*_test.go`, `kit_draft_internal_test.go` | tests | 2–7 |
| `docs/usage/jam/ui-pages.md`, `kits-drafts.md`, `INDEX.md` | docs | 7 |

---

### Task 1: `RenameKitDraftFile`

**Files:**
- Modify: `internal/jam/kit_draft_files.go` (append after `RemoveKitDraftFile`)
- Test: `internal/jam/kit_draft_files_test.go` (append)

**Interfaces:**
- Consumes (slice 3, unexported, same package): `checkDraftFileArgs(name, p string) error`, `draftPathErr(name string, err error) error`, `lockedDraftTree(store Store, name, ifMatch string) (KitDraft, kittree.Tree, error)`, `saveDraftLocked(store Store, log *slog.Logger, d KitDraft, prev, next kittree.Tree, objs KitObjects, actor string, now time.Time) (KitDraftSave, error)`, `freshBlobs(store Store, fresh map[string][]byte) kittree.Blobs`, `writeErr`, `kitMu`; `kittree.CheckPath`, `kittree.IgnoreTree`, `Tree.Lookup/With/Without`.
- Produces: `func RenameKitDraftFile(store Store, log *slog.Logger, name, from, to, ifMatch, actor string, now time.Time) (KitDraftSave, error)`. 400 for an unsafe kit name or path (unechoed), a target its `.dockerignore` excludes (`draft <kit>: <to>: excluded by .dockerignore`), or the file rules (`draft <kit>: <rule>`). 404 `draft <kit> has no file <from>`. 409 `draft <kit> already has a file <to>`. 412/428 from the ETag. `from == to` is a no-op save.

- [ ] **Step 1: Write the failing test**

Append to `internal/jam/kit_draft_files_test.go`:

```go
// TestRenameKitDraftFile: a rename moves the content and mode in one save,
// refuses a missing source, a taken, unsafe, ignored or file-over-directory
// target (unechoed) and a stale ETag, leaves a rename to itself a no-op, and
// applies a file renamed to .dockerignore.
func TestRenameKitDraftFile(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "run.sh*", "echo\n", "sub/x", "x", ".dockerignore", "*.log\n", "rules", "sub\n")
	tag := openApp(t, st)
	later := draftNow.Add(time.Minute)
	s, err := RenameKitDraftFile(st, nil, "app", "run.sh", "bin/run.sh", tag, "alice", later)
	if err != nil || !slices.Equal(s.Changed, []DraftChange{{"bin/run.sh", "added"}, {"run.sh", "removed"}}) || len(s.Problems) != 0 {
		t.Fatalf("rename = %+v %v", s, err)
	}
	data, e, etag, err := KitDraftFile(st, "app", "bin/run.sh")
	if err != nil || string(data) != "echo\n" || e.Mode != 0o755 || etag != s.ETag {
		t.Fatalf("moved file = %q %+v %q %v", data, e, etag, err)
	}
	_, _, _, err = KitDraftFile(st, "app", "run.sh")
	wantErr(t, err, "draft app has no file run.sh", 404)
	if d, _ := st.GetKitDraft("app"); d.Actor != "alice" || !d.UpdatedAt.Equal(later) {
		t.Fatalf("draft row = %+v", d)
	}
	same, err := RenameKitDraftFile(st, nil, "app", "bin/run.sh", "bin/run.sh", s.ETag, "bob", later)
	if err != nil || same.ETag != s.ETag || len(same.Changed) != 0 {
		t.Fatalf("rename to itself = %+v %v", same, err)
	}
	if d, _ := st.GetKitDraft("app"); d.Actor != "alice" {
		t.Fatal("a no-op rename recorded its actor")
	}
	for name, tc := range map[string]struct {
		from, to, want string
		status         int
	}{
		"no such file":    {"nope", "x2", "draft app has no file nope", 404},
		"taken":           {"bin/run.sh", "kit.yml", "draft app already has a file kit.yml", 409},
		"bidi target":     {"bin/run.sh", "\u202etxt.exe", unsafeDraftPath, 400},
		"dot dot target":  {"bin/run.sh", "../x", unsafeDraftPath, 400},
		"unsafe source":   {"a\nb", "x", unsafeDraftPath, 400},
		"file over a dir": {"bin/run.sh", "sub", "draft app: sub: is both a file and a directory", 400},
		"ignored":         {"bin/run.sh", "run.log", "draft app: run.log: excluded by .dockerignore", 400},
		"stale":           {"bin/run.sh", "b.sh", "draft app changed since you loaded it (reload)", 412},
	} {
		t.Run(name, func(t *testing.T) {
			ifMatch := s.ETag
			if name == "stale" {
				ifMatch = tag
			}
			_, err := RenameKitDraftFile(st, nil, "app", tc.from, tc.to, ifMatch, "alice", later)
			wantErr(t, err, tc.want, tc.status)
		})
	}
	if d, _ := st.GetKitDraft("app"); DraftETag(d.Tree) != s.ETag {
		t.Fatal("a refused rename changed the draft")
	}
	// A file renamed to .dockerignore applies it: "sub" now drops sub/x.
	s, err = RemoveKitDraftFile(st, nil, "app", ".dockerignore", s.ETag, "alice", later)
	if err != nil {
		t.Fatal(err)
	}
	s, err = RenameKitDraftFile(st, nil, "app", "rules", ".dockerignore", s.ETag, "alice", later)
	if err != nil || !slices.Equal(s.Changed, []DraftChange{{".dockerignore", "added"}, {"rules", "removed"}, {"sub/x", "removed"}}) {
		t.Fatalf("rename to .dockerignore = %+v %v", s, err)
	}
	// kit.yml renamed away is a problem, not a refusal.
	s, err = RenameKitDraftFile(st, nil, "app", "kit.yml", "old.yml", s.ETag, "alice", later)
	if err != nil || !slices.Equal(s.Problems, []string{"kit app: kit.yml missing at the root"}) {
		t.Fatalf("rename kit.yml = %+v %v", s, err)
	}
	if v, _ := LoadKitVersion(st, "app", 0); v.Version != 1 || v.Err != nil {
		t.Fatal("renames touched the published version")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/agent/workspace/cove && GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run TestRenameKitDraftFile -v`
Expected: FAIL to build with `undefined: RenameKitDraftFile`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/jam/kit_draft_files.go`:

```go
// RenameKitDraftFile moves file from to path to in kit name's draft, under
// kitMu and against ifMatch, as one save: its content and mode move with it.
// 404 when the draft has no file from, 409 when it already has one at to; a
// rename to its own path changes nothing. The draft's .dockerignore applies
// to the result as on a put: to excluded refuses, and a file renamed to
// .dockerignore drops the files it newly excludes. The file rules hold
// (saveDraftLocked); kit.yml may be renamed away (a problem, not a refusal).
func RenameKitDraftFile(store Store, log *slog.Logger, name, from, to, ifMatch, actor string, now time.Time) (KitDraftSave, error) {
	if err := checkDraftFileArgs(name, from); err != nil {
		return KitDraftSave{}, err
	}
	if err := kittree.CheckPath(to); err != nil {
		return KitDraftSave{}, draftPathErr(name, err)
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	d, cur, err := lockedDraftTree(store, name, ifMatch)
	if err != nil {
		return KitDraftSave{}, err
	}
	e, ok := cur.Lookup(from)
	if !ok {
		return KitDraftSave{}, writeErr(http.StatusNotFound, "draft %s has no file %s", name, from)
	}
	if from == to {
		return saveDraftLocked(store, log, d, cur, cur, KitObjects{}, actor, now)
	}
	if _, taken := cur.Lookup(to); taken {
		return KitDraftSave{}, writeErr(http.StatusConflict, "draft %s already has a file %s", name, to)
	}
	moved := e
	moved.Path = to
	next, err := kittree.IgnoreTree(cur.Without(from).With(moved), freshBlobs(store, nil))
	if err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	if _, kept := next.Lookup(to); !kept {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s: excluded by .dockerignore", name, to)
	}
	return saveDraftLocked(store, log, d, cur, next, KitObjects{Trees: []kittree.Tree{next}}, actor, now)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/jam/ -run 'TestRenameKitDraftFile|TestPutKitDraftFile|TestDraftFileRefusals' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal/jam
git add internal/jam/kit_draft_files.go internal/jam/kit_draft_files_test.go
git commit -m "feat(jam): RenameKitDraftFile — move a draft file in one save" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: The Draft tab — page, file tree, problems, base notice, open and discard, markers

**Files:**
- Create: `internal/jam/adminui/kit_draft_view.go`, `internal/jam/adminui/kit_draft.go`, `internal/jam/adminui/templates/kit_shared.html`, `internal/jam/adminui/templates/kit_draft.html`
- Modify: `internal/jam/adminui/adminui.go` (pages map, funcs, registration), `internal/jam/adminui/kits.go` (`writeFlash`), `internal/jam/adminui/kit_detail.go` (`kitDetail.Draft`, `kitListRow.Draft`), `internal/jam/adminui/kit_files_view.go` (`fileModeText`), `internal/jam/adminui/templates/kit.html`, `internal/jam/adminui/templates/kits.html`
- Test: `internal/jam/adminui/kit_draft_test.go`, `internal/jam/adminui/kit_draft_internal_test.go`

**Interfaces:**
- Consumes: `jam.KitDraftStatusOf(store, name) (jam.KitDraftStatus, error)`, `jam.OpenKitDraft(store, name string, from int, actor string, now time.Time) (jam.KitDraft, error)`, `jam.DiscardKitDraft(store, log, name, ifMatch string) (jam.KitDraft, error)`, `jam.LoadKitVersion`, `jam.WriteStatus`, `*jam.WriteError{Status, Msg}`, `jam.DraftDriftWarning`, `store.GetKitDraft`; adminui `kitURL`, `pathText`, `sizeLabel`, `shortDigest`, `render`, `renderStatus`, `renderFragment`, `renderError`.
- Produces (used by Tasks 3–7):
  - `func draftURL(name string) string` (also template func `draftURL`)
  - `func draftFileHref(kit, p string, diff bool) string`
  - `type draftQuery struct{ File string; Diff bool }`
  - `type draftPage struct{ Title, Name string; Current int; Versions []int; Draft *draftView; NotFound bool; NotFoundFor string }`
  - `type draftView struct{ Base, Current int; Superseded bool; ETag, Actor, Updated string; Managed bool; DriftWarning string; Problems []string; Size string; Tree, Removed []draftRow }`
  - `type draftRow struct{ Pad, Name string; Dir bool; Path, Href, Mode string; Size int64; Change string }`
  - `func buildDraftPage(store jam.Store, name string, q draftQuery) (draftPage, bool, error)`
  - `func (p draftPage) status() int`
  - `func fileModeText(m fs.FileMode) string`
  - `func draftForm(w http.ResponseWriter, r *http.Request, store jam.Store, guardWrite func(http.ResponseWriter, *http.Request) bool, multipart bool) (string, bool)`
  - `func draftUIFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error)`
  - `func loadDraftPage(w http.ResponseWriter, r *http.Request, store jam.Store, log *slog.Logger, name string, q draftQuery) (draftPage, bool)`
  - `func writeDraftBody(w http.ResponseWriter, r *http.Request, p draftPage, msg string)`
  - `func renderDraftBody(w http.ResponseWriter, r *http.Request, store jam.Store, log *slog.Logger, name string, q draftQuery, msg string)`
  - `func writeFlash(w http.ResponseWriter, msg string)`
  - Template anchors in `kit_draft.html`, each replaced by a later task: `{{/* draft-published */}}` (7), `{{/* draft-publish-form */}}` (7), `{{/* draft-editor */}}` (3), `{{/* draft-file-diff */}}` (6), `{{/* draft-add */}}` (5), `{{/* draft-diff */}}` (6).
  - Test helpers (package `adminui_test`): `draftFixture(t) (jam.Store, http.Handler)`, `draftETag(t, store) string`, `putDraft(t, store, p string, mode fs.FileMode, data string)`, `hiddenETag(t, s string) string`, `var draftWriteRoutes []string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/adminui/kit_draft_internal_test.go`:

```go
package adminui

import (
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// draftTree nests sorted entries by directory: a row for each directory the
// first time a file under it appears, indented by depth.
func TestDraftTreeNests(t *testing.T) {
	entries := []jam.DraftEntry{
		{Path: "a.txt", Mode: "0644", Size: 1},
		{Path: "a/b.txt", Mode: "0644", Size: 2},
		{Path: "a/b/c.sh", Mode: "0755", Size: 3},
		{Path: "a/z", Mode: "0644", Size: 4},
		{Path: "kit.yml", Mode: "0644", Size: 5},
	}
	rows := draftTree("app", entries, map[string]string{"a/b/c.sh": "added"})
	var got []string
	for _, r := range rows {
		l := r.Pad + r.Name
		if r.Dir {
			l += "/"
		} else {
			l += " " + r.Mode + " " + r.Change
		}
		got = append(got, l)
	}
	want := []string{"a.txt 0644 ", "a/", "  b.txt 0644 ", "  b/", "    c.sh 0755 added", "  z 0644 ", "kit.yml 0644 "}
	if !slices.Equal(got, want) {
		t.Fatalf("tree =\n%q\nwant\n%q", got, want)
	}
	if rows[4].Href != "/app/admin/kits/app/draft?file=a%2Fb%2Fc.sh" || rows[4].Path != "a/b/c.sh" || rows[1].Href != "" {
		t.Fatalf("rows = %+v", rows)
	}
}
```

Create `internal/jam/adminui/kit_draft_test.go`:

```go
package adminui_test

import (
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// draftFixture pushes kit app (kit.yml, run.sh 0755, docs/a.md, gone.txt) as
// v1, opens its draft from v1 and returns the store and the UI.
func draftFixture(t *testing.T) (jam.Store, http.Handler) {
	t.Helper()
	store := newStore(t)
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\n", "run.sh*", "echo\n", "docs/a.md", "# a\n", "gone.txt", "bye\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.OpenKitDraft(store, "app", 0, "local", time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	return store, uiHandler(t, store)
}

// draftETag is kit app's draft ETag now.
func draftETag(t *testing.T, store jam.Store) string {
	t.Helper()
	st, err := jam.KitDraftStatusOf(store, "app")
	if err != nil {
		t.Fatal(err)
	}
	return st.ETag
}

// putDraft writes file p into app's draft (another tab, the CLI).
func putDraft(t *testing.T, store jam.Store, p string, mode fs.FileMode, data string) {
	t.Helper()
	if _, err := jam.PutKitDraftFile(store, nil, "app", p, mode, []byte(data), draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
}

// hiddenETag is the first hidden etag field's value in s, unescaped.
func hiddenETag(t *testing.T, s string) string {
	t.Helper()
	const k = `name="etag" value="`
	i := strings.Index(s, k)
	if i < 0 {
		t.Fatalf("no etag field in:\n%s", s)
	}
	s = s[i+len(k):]
	return html.UnescapeString(s[:strings.Index(s, `"`)])
}

// draftWriteRoutes are the Draft tab's writes, under /app/admin/kits/app;
// each later task appends its own.
var draftWriteRoutes = []string{"/draft", "/draft/discard"}

// Every Draft-tab write refuses a cross-origin form POST (403) before it
// reads the form, and changes nothing.
func TestDraftWritesRefuseCrossOrigin(t *testing.T) {
	store, h := draftFixture(t)
	before := draftETag(t, store)
	form := url.Values{"etag": {before}, "path": {"run.sh"}, "from": {"run.sh"}, "to": {"x.sh"}, "content": {"pwned"}, "pin": {"1"}}.Encode()
	for _, route := range draftWriteRoutes {
		req := httptest.NewRequest(http.MethodPost, "/app/admin/kits/app"+route, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("cross-origin POST %s = %d, want 403", route, rec.Code)
		}
	}
	if draftETag(t, store) != before {
		t.Fatal("a cross-origin write changed the draft")
	}
}

func TestDraftTabNoDraft(t *testing.T) {
	store := newStore(t)
	for _, y := range []string{"kind: studio\n", "kind: studio\nprompt: two\n"} {
		if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", y)); err != nil {
			t.Fatal(err)
		}
	}
	rec := get(t, uiHandler(t, store), "/app/admin/kits/app/draft")
	none := section(rec.Body.String(), "draft-none")
	for _, want := range []string{`<option value="1">v1</option>`, `<option value="2" selected>v2 (current)</option>`, `hx-post="/app/admin/kits/app/draft"`} {
		if rec.Code != http.StatusOK || !strings.Contains(none, want) {
			t.Errorf("no-draft card (%d) missing %q:\n%s", rec.Code, want, none)
		}
	}
	if !strings.Contains(rec.Body.String(), `<a href="/app/admin/kits/app/draft" aria-current="page">Draft</a>`) {
		t.Error("the Draft sub-tab is not current")
	}
}

func TestDraftTabUnknownKit(t *testing.T) {
	rec := get(t, uiHandler(t, newStore(t)), "/app/admin/kits/nope/draft")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Kit not found") {
		t.Fatalf("unknown kit = %d", rec.Code)
	}
}

// The tree nests by directory, gives each file its mode and size, and marks
// it against the base; removed files follow it.
func TestDraftTabTree(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "docs/a.md", 0, "# changed\n")
	putDraft(t, store, "docs/new.md", 0, "new\n")
	if _, err := jam.RemoveKitDraftFile(store, nil, "app", "gone.txt", draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/app/admin/kits/app/draft").Body.String()
	tree := section(body, "draft-tree")
	for _, want := range []string{
		`<tr class="dir"><td class="name mono">docs/</td>`,
		`<tr data-path="docs/a.md" data-change="modified"><td class="name mono">  <a href=`,
		`<tr data-path="docs/new.md" data-change="added">`,
		`<tr data-path="run.sh"><td class="name mono"><a href="/app/admin/kits/app/draft?file=run.sh">run.sh</a></td><td class="mono">0755</td><td>5 B</td>`,
		`<tr data-path="gone.txt" data-change="removed"><td class="name mono"><a href="/app/admin/kits/app/draft?file=gone.txt&amp;diff=1">gone.txt</a></td><td class="mono">0644</td><td>4 B</td>`,
	} {
		if !strings.Contains(tree, want) {
			t.Errorf("tree missing %q:\n%s", want, tree)
		}
	}
	if i, j := strings.Index(tree, "docs/</td>"), strings.Index(tree, `data-path="kit.yml"`); i < 0 || j < i {
		t.Error("the docs directory should come before kit.yml")
	}
	if strings.Contains(body, `id="draft-superseded"`) || !strings.Contains(body, `id="draft-no-problems"`) {
		t.Error("a fresh, valid draft shows neither the superseded notice nor problems")
	}
	if got := hiddenETag(t, section(body, "draft-status")); got != draftETag(t, store) {
		t.Fatalf("discard form etag = %q", got)
	}
}

// A draft opened before another version landed says so; a problem is listed
// escaped and through displayText.
func TestDraftTabSupersededAndProblems(t *testing.T) {
	store, h := draftFixture(t)
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\nprompt: v2\n")); err != nil {
		t.Fatal(err)
	}
	putDraft(t, store, "kit.yml", 0, "kind: \"<x>\"\n")
	body := get(t, h, "/app/admin/kits/app/draft").Body.String()
	if s := section(body, "draft-status"); !strings.Contains(s, `id="draft-superseded">base v1, current v2:`) {
		t.Errorf("superseded notice:\n%s", s)
	}
	pub := section(body, "draft-publish")
	if !strings.Contains(pub, `id="draft-problems"`) || !strings.Contains(pub, `got &#34;&lt;x&gt;&#34;`) || strings.Contains(pub, "<x>") {
		t.Errorf("problems panel:\n%s", pub)
	}
}

// The kit page opens a draft from the viewed version; the page, its header
// and the kits list then mark the kit.
func TestOpenDraftFromKitPage(t *testing.T) {
	store := newStore(t)
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\n")); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	page := get(t, h, "/app/admin/kits/app").Body.String()
	for _, want := range []string{`id="kit-open-draft">Open draft from v1</button>`, `<input type="hidden" name="from" value="1">`, `<a href="/app/admin/kits/app/draft">Draft</a>`} {
		if !strings.Contains(page, want) {
			t.Errorf("kit page missing %q", want)
		}
	}
	rec := post(t, h, "/app/admin/kits/app/draft", url.Values{"from": {"1"}})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/app/admin/kits/app/draft" {
		t.Fatalf("open = %d %q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body)
	}
	if d, ok := store.GetKitDraft("app"); !ok || d.Base != 1 {
		t.Fatalf("draft = %+v %v", d, ok)
	}
	page = get(t, h, "/app/admin/kits/app").Body.String()
	if !strings.Contains(page, `id="kit-draft-chip" href="/app/admin/kits/app/draft">draft · base v1</a>`) || strings.Contains(page, `id="kit-open-draft"`) {
		t.Error("kit page should link the open draft instead of offering a new one")
	}
	if list := get(t, h, "/app/admin/kits").Body.String(); !strings.Contains(list, `<a class="chip" href="/app/admin/kits/app/draft" title="An open draft">draft</a>`) {
		t.Error("kits list should mark the kit with a draft")
	}
}

func TestOpenDraftRefusals(t *testing.T) {
	store, h := draftFixture(t)
	if rec := post(t, h, "/app/admin/kits/app/draft", url.Values{}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "kit app already has a draft") {
		t.Fatalf("second open = %d %s", rec.Code, rec.Body)
	}
	if _, err := jam.DiscardKitDraft(store, nil, "app", draftETag(t, store)); err != nil {
		t.Fatal(err)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft", url.Values{"from": {"abc"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("from=abc = %d", rec.Code)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft", url.Values{"from": {"9"}}); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "kit app has no version v9") {
		t.Fatalf("from=9 = %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/app/admin/kits/nope/draft", url.Values{}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown kit = %d", rec.Code)
	}
}

func TestDiscardDraft(t *testing.T) {
	store, h := draftFixture(t)
	tag := hiddenETag(t, section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-status"))
	putDraft(t, store, "x.txt", 0, "x") // another tab saves meanwhile
	rec := post(t, h, "/app/admin/kits/app/draft/discard", url.Values{"etag": {tag}})
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale discard = %d %s", rec.Code, rec.Body)
	}
	if _, open := store.GetKitDraft("app"); !open {
		t.Fatal("a stale discard dropped the draft")
	}
	rec = post(t, h, "/app/admin/kits/app/draft/discard", url.Values{"etag": {draftETag(t, store)}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "discarded the draft of app") || !strings.Contains(rec.Body.String(), `id="draft-none"`) {
		t.Fatalf("discard = %d %s", rec.Code, rec.Body)
	}
	if _, open := store.GetKitDraft("app"); open {
		t.Fatal("discard left the draft open")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jam/adminui/ -run 'TestDraft|TestOpenDraft|TestDiscardDraft' -v`
Expected: FAIL to build (`undefined: draftTree`).

- [ ] **Step 3: Move the kit page's shared templates**

Create `internal/jam/adminui/templates/kit_shared.html`. It holds the kit page's `<style>` (moved from `kit.html`, plus the Draft-tab rules) and the three defines moved verbatim from the end of `kit.html`:

```html
{{/* kit-style is the kit pages' styles: the kit page and its Draft tab. */}}
{{define "kit-style"}}
<style>
  .crumbs{font-size:12.5px;color:var(--faint);margin-bottom:4px}
  .crumbs a{color:var(--muted);text-decoration:none}
  .page-head .spacer{flex:1}
  .page-head form{display:inline}
  .kit-layout{display:grid;grid-template-columns:220px 1fr;gap:16px;align-items:start}
  .rail{padding:6px}
  .ver{display:flex;flex-direction:column;gap:2px;padding:8px 10px;border-radius:8px;border:1px solid transparent}
  .ver.sel{background:var(--accent-soft);border-color:color-mix(in srgb,var(--accent) 30%,transparent)}
  .ver .top{display:flex;align-items:center;gap:6px}
  .ver .top a{font-weight:600;text-decoration:none;color:var(--ink)}
  .ver .links{display:flex;gap:8px;flex-wrap:wrap;font-size:11.5px}
  .ver .links a{color:var(--muted)}
  .ver .dig{font:11px "IBM Plex Mono",monospace;color:var(--faint)}
  .ver form{display:inline}
  .stack{display:flex;flex-direction:column;gap:16px;min-width:0}
  section.card{overflow:visible}
  section.card>header{display:flex;align-items:baseline;gap:10px;padding:12px 16px;border-bottom:1px solid var(--border);flex-wrap:wrap}
  section.card>header h2{margin:0}
  section.card>header .sub{font-size:12px}
  section.card>header .spacer{flex:1}
  section.card>.body{padding:12px 16px}
  dl.kv{display:grid;grid-template-columns:max-content 1fr;gap:6px 18px;margin:0}
  dl.kv dt{color:var(--muted);font-size:13px}
  dl.kv dd{margin:0;word-break:break-word}
  .unset{color:var(--faint)}
  pre.yaml,pre.diff{margin:0;padding:12px 16px;overflow-x:auto;font:12.5px/1.55 "IBM Plex Mono",ui-monospace,monospace;background:var(--surface-2);border-radius:0 0 12px 12px}
  .prompt{white-space:pre-wrap}
  details.edit>summary{cursor:pointer;list-style:none;padding:9px 16px;font-size:12.5px;font-weight:600;color:var(--accent);user-select:none}
  details.edit>summary::-webkit-details-marker{display:none}
  details.edit form{padding:12px 16px 16px;background:var(--surface-2);border-radius:0 0 12px 12px}
  .dtree td.name{white-space:pre}
  .dtree tr.dir td{color:var(--muted)}
  .dtree tr[data-change="removed"] td.name{text-decoration:line-through;color:var(--faint)}
  #draft label.check{display:flex;flex-direction:row;align-items:center;gap:8px;margin:8px 0;font-size:13px}
  #draft label.check input{width:auto}
  textarea.editor{min-height:28em;white-space:pre;tab-size:4}
  @media (max-width:860px){.kit-layout{grid-template-columns:1fr}}
</style>
{{end}}

{{/* kit-masked-note says a shown kit.yml differs from the stored one: base's packed context is masked. */}}
{{define "kit-masked-note"}}A packed context in this text is shown masked («packed context, not shown»); remove it before pushing.{{end}}

{{/* kit-hidden-note says how to get at a kit.yml not shown at all; the dot is the kit's name. */}}
{{define "kit-hidden-note"}}This kit.yml is not shown: it may hold a packed context. Write it out with <code>at-jam config write &lt;dir&gt; --all</code> (it is <code>kits/{{.}}/kit.yml</code>), fix it, and push that directory with <code>at-jam kit push</code>.{{end}}

{{/* ctx-file is one file of a version's tree (ctxFileView): its content, its line diff, or why neither. */}}
{{define "ctx-file"}}
<section class="card" id="ctx-file">
  {{if .Missing}}
  <header><h2>File</h2></header>
  <div class="body"><div class="banner error">No such file in {{if .Diff}}either version{{else}}this version{{end}}'s files.</div></div>
  {{else}}
  <header><h2 class="mono">{{.Path}}</h2><span class="sub">{{if .Diff}}v{{.From}} → v{{.To}}{{else}}content{{end}}</span></header>
  {{if .Note}}<div class="body unset">{{.Note}}</div>
  {{else if .Diff}}{{if .TooLarge}}<div class="body unset">Too large to diff here.</div>{{else}}{{template "diff-lines" .Lines}}{{end}}
  {{else}}{{if .Hidden}}<div class="body"><p class="hint">{{template "kit-hidden-note" "<kit>"}}</p></div>{{else if .Masked}}<div class="body"><p class="hint">{{template "kit-masked-note"}}</p></div>{{end}}<pre class="yaml">{{.Content}}</pre>{{end}}
  {{end}}
</section>
{{end}}
```

In `internal/jam/adminui/templates/kit.html`:
- Replace the whole `<style>` … `</style>` block inside `{{define "content"}}` with the single line `{{template "kit-style"}}`.
- Delete everything from the line `{{/* kit-masked-note says a shown kit.yml differs …` to the end of the file. Those defines now live in `kit_shared.html`.

Then add the sub-tabs, the draft chip and the Open-draft button. Replace

```html
  <span class="sub">{{len .Versions}} version(s)</span>
  <span class="spacer"></span>
```

with

```html
  <span class="sub">{{len .Versions}} version(s)</span>
  {{with .Draft}}<a class="chip" id="kit-draft-chip" href="{{draftURL $name}}">draft · base v{{.Base}}</a>{{end}}
  <span class="spacer"></span>
  {{if not .Draft}}<form hx-post="/app/admin/kits/{{.Name}}/draft" hx-swap="none"><input type="hidden" name="from" value="{{.View.N}}"><button type="submit" id="kit-open-draft">Open draft from v{{.View.N}}</button></form>{{end}}
```

and replace

```html
</div>

<div class="kit-layout">
```

with

```html
</div>
<nav class="subtabs" aria-label="Kit views"><a href="{{kitURL .Name}}" aria-current="page">Overview</a><a href="{{draftURL .Name}}">Draft</a></nav>

<div class="kit-layout">
```

In `internal/jam/adminui/templates/kits.html`, replace

```html
      <td><a class="mono" href="{{kitURL .Name}}"><b>{{.Name}}</b></a>{{template "attn-flag" (attn "specs" (printf "kit:%s" .Name))}}</td>
```

with

```html
      <td><a class="mono" href="{{kitURL .Name}}"><b>{{.Name}}</b></a>{{template "attn-flag" (attn "specs" (printf "kit:%s" .Name))}}{{if .Draft}} <a class="chip" href="{{draftURL .Name}}" title="An open draft">draft</a>{{end}}</td>
```

- [ ] **Step 4: Write the Draft tab template**

Create `internal/jam/adminui/templates/kit_draft.html`:

```html
{{define "content"}}
{{if .NotFound}}
<div class="page-head"><h1>Kit not found</h1></div>
<div class="banner">No kit <span class="mono">{{.NotFoundFor}}</span>. <a href="/app/admin/kits">Back to kits</a></div>
{{else}}
{{template "kit-style"}}
<div class="crumbs"><a href="/app/admin/kits">Kits</a></div>
{{template "draft-body" .}}
{{end}}
{{end}}

{{/* draft-body is the Draft tab's swappable body (#draft): every write answers it. */}}
{{define "draft-body"}}
{{$name := .Name}}
<div id="draft">
<div class="page-head">
  <h1>{{.Name}}</h1>{{template "managed-badge" (managed "kit" .Name)}}
  <span class="chip accent">current v{{.Current}}</span>
  {{with .Draft}}<span class="chip">draft · base v{{.Base}}</span>{{end}}
</div>
<nav class="subtabs" aria-label="Kit views"><a href="{{kitURL .Name}}">Overview</a><a href="{{draftURL .Name}}" aria-current="page">Draft</a></nav>
<div class="stack">
{{/* draft-published */}}
{{with .Draft}}{{$etag := .ETag}}
<section class="card" id="draft-status">
  <header><h2>Draft</h2><span class="sub">base v{{.Base}}, current v{{.Current}} · last saved by {{.Actor}} at {{.Updated}}</span><span class="spacer"></span>
    <form hx-post="/app/admin/kits/{{$name}}/draft/discard" hx-target="#draft" hx-swap="outerHTML" hx-confirm="Discard the draft of {{$name}}? Its changes are lost.">
      <input type="hidden" name="etag" value="{{$etag}}"><button class="small danger" type="submit">Discard</button></form></header>
  {{if .Superseded}}<div class="body"><div class="banner" id="draft-superseded">base v{{.Base}}, current v{{.Current}}: publishing makes the draft's files the next version as they are; v{{.Current}}'s changes are not merged.</div></div>{{end}}
</section>
<section class="card" id="draft-publish">
  <header><h2>Publish</h2><span class="sub">makes the draft the next version of {{$name}} and closes it</span></header>
  <div class="body">
    {{if .Problems}}<div class="banner error" id="draft-problems"><div>Publishing is blocked until these are fixed:<ul>{{range .Problems}}<li>{{displayText .}}</li>{{end}}</ul></div></div>{{else}}<p class="unset" id="draft-no-problems">No problems: the draft can be published.</p>{{end}}
    {{if .Managed}}<div class="banner" id="draft-drift">{{.DriftWarning}}</div>{{end}}
    {{/* draft-publish-form */}}
  </div>
</section>
{{/* draft-editor */}}
{{/* draft-file-diff */}}
<section class="card" id="draft-tree">
  <header><h2>Files</h2><span class="sub">{{.Size}} · marked against v{{.Base}}</span></header>
  <table class="dtree">
    <thead><tr><th>Path</th><th>Mode</th><th>Size</th><th>vs v{{.Base}}</th></tr></thead>
    <tbody>
    {{range .Tree}}{{if .Dir}}<tr class="dir"><td class="name mono">{{.Pad}}{{.Name}}/</td><td></td><td></td><td></td></tr>
    {{else}}<tr data-path="{{.Path}}"{{with .Change}} data-change="{{.}}"{{end}}><td class="name mono">{{.Pad}}<a href="{{.Href}}">{{.Name}}</a></td><td class="mono">{{.Mode}}</td><td>{{.Size}} B</td><td>{{with .Change}}<span class="chip">{{.}}</span>{{end}}</td></tr>
    {{end}}{{end}}
    {{range .Removed}}<tr data-path="{{.Path}}" data-change="removed"><td class="name mono"><a href="{{.Href}}">{{.Name}}</a></td><td class="mono">{{.Mode}}</td><td>{{.Size}} B</td><td><span class="chip">removed</span></td></tr>
    {{end}}
    </tbody>
  </table>
</section>
{{/* draft-add */}}
{{/* draft-diff */}}
{{else}}
<section class="card" id="draft-none">
  <header><h2>No draft</h2><span class="sub">a draft is a copy of one version's files: edit it here, then publish it as the next version</span></header>
  <form class="body" hx-post="/app/admin/kits/{{$name}}/draft" hx-swap="none">
    <label>Open from <select name="from">{{range $.Versions}}<option value="{{.}}"{{if eq . $.Current}} selected{{end}}>v{{.}}{{if eq . $.Current}} (current){{end}}</option>{{end}}</select></label>
    <div class="form-actions"><button type="submit" class="primary" id="draft-open">Open draft</button></div>
  </form>
</section>
{{end}}
</div>
</div>
{{end}}
```

- [ ] **Step 5: Write the view model**

Create `internal/jam/adminui/kit_draft_view.go`:

```go
package adminui

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// draftURL is the path of a kit's Draft tab.
func draftURL(name string) string { return kitURL(name) + "/draft" }

// draftFileHref opens file p in the Draft tab: in the editor, or with diff
// as its diff against the draft's base.
func draftFileHref(kit, p string, diff bool) string {
	h := draftURL(kit) + "?file=" + url.QueryEscape(p)
	if diff {
		h += "&diff=1"
	}
	return h
}

// draftQuery is what the Draft tab shows besides the draft: one file
// (?file=), in the editor or, with Diff (?diff=1), as its diff against the base.
type draftQuery struct {
	File string
	Diff bool
}

// draftPage is the Draft tab payload.
type draftPage struct {
	Title       string
	Name        string
	Current     int
	Versions    []int      // the open form's choices
	Draft       *draftView // nil: the kit has no draft
	NotFound    bool
	NotFoundFor string
}

// draftView is the kit's open draft, ready to render.
type draftView struct {
	Base, Current int
	Superseded    bool   // the kit's current version is not the draft's base
	ETag          string // the draft's ETag at this read: the forms send it back
	Actor         string
	Updated       string
	Managed       bool   // config-managed: publishing drifts
	DriftWarning  string // jam.DraftDriftWarning, shown when Managed
	Problems      []string
	Size          string     // the draft's total size
	Tree          []draftRow // its files, nested by directory
	Removed       []draftRow // files of the base the draft no longer has
}

// draftRow is one row of the Draft tab's file tree: a directory or a file.
type draftRow struct {
	Pad    string // two spaces per level
	Name   string // the last segment (display text)
	Dir    bool
	Path   string // the whole path (display text)
	Href   string
	Mode   string
	Size   int64
	Change string // against the base: added, modified, mode, removed; "" unchanged
}

// draftTree nests sorted entries by directory: a row for each directory the
// first time a file under it appears, then the file, each indented by its
// depth. Sorted paths keep a directory's files together.
func draftTree(kit string, entries []jam.DraftEntry, kinds map[string]string) []draftRow {
	var out []draftRow
	var prev []string
	for _, e := range entries {
		segs := strings.Split(e.Path, "/")
		dirs := segs[:len(segs)-1]
		same := 0
		for same < len(prev) && same < len(dirs) && prev[same] == dirs[same] {
			same++
		}
		for i := same; i < len(dirs); i++ {
			out = append(out, draftRow{Pad: strings.Repeat("  ", i), Name: pathText(dirs[i]), Dir: true, Path: pathText(strings.Join(dirs[:i+1], "/"))})
		}
		out = append(out, draftRow{
			Pad: strings.Repeat("  ", len(dirs)), Name: pathText(segs[len(segs)-1]), Path: pathText(e.Path),
			Href: draftFileHref(kit, e.Path, false), Mode: e.Mode, Size: e.Size, Change: kinds[e.Path],
		})
		prev = dirs
	}
	return out
}

// newDraftView shapes draft status st of kit for display; base is the tree
// of its base version (where removed files are read from).
func newDraftView(kit string, st jam.KitDraftStatus, base kittree.Tree) *draftView {
	v := &draftView{
		Base: st.Base, Current: st.Current, Superseded: st.Base != st.Current,
		ETag: st.ETag, Actor: st.Actor, Updated: st.UpdatedAt.UTC().Format("2006-01-02 15:04"),
		Managed: st.Managed, DriftWarning: jam.DraftDriftWarning, Problems: st.Problems,
	}
	var total int64
	for _, e := range st.Entries {
		total += e.Size
	}
	v.Size = sizeLabel(int(total))
	kinds := make(map[string]string, len(st.Changes))
	for _, c := range st.Changes {
		kinds[c.Path] = c.Kind
	}
	v.Tree = draftTree(kit, st.Entries, kinds)
	for _, c := range st.Changes {
		if c.Kind != kittree.ChangeRemoved {
			continue
		}
		e, _ := base.Lookup(c.Path)
		v.Removed = append(v.Removed, draftRow{
			Name: pathText(c.Path), Path: pathText(c.Path), Href: draftFileHref(kit, c.Path, true),
			Mode: fileModeText(e.Mode), Size: e.Size, Change: kittree.ChangeRemoved,
		})
	}
	return v
}

// buildDraftPage gathers kit name's Draft tab showing q. ok is false when the
// kit does not exist; a kit with no draft has Draft nil. err is a store fault.
func buildDraftPage(store jam.Store, name string, q draftQuery) (draftPage, bool, error) {
	k, ok := store.GetKit(name)
	if !ok {
		return draftPage{}, false, nil
	}
	p := draftPage{Title: "Kits", Name: name, Current: k.Current, Versions: k.VersionNumbers()}
	st, err := jam.KitDraftStatusOf(store, name)
	if err != nil {
		if jam.WriteStatus(err, http.StatusInternalServerError) == http.StatusNotFound {
			return p, true, nil // no draft (or the kit went since)
		}
		return p, true, err
	}
	base, _ := jam.LoadKitVersion(store, name, st.Base)
	p.Draft = newDraftView(name, st, base.Tree)
	return p, true, nil
}

// status is the GET answer's code for p.
func (p draftPage) status() int { return http.StatusOK }
```

In `internal/jam/adminui/kit_files_view.go`, add `"io/fs"` to the imports and replace

```go
func modeText(e ctxEntry) string { return fmt.Sprintf("%04o", uint32(e.Mode)) }
```

with

```go
func modeText(e ctxEntry) string { return fileModeText(e.Mode) }

// fileModeText is a file mode as the kit pages show it: "0644", "0755".
func fileModeText(m fs.FileMode) string { return fmt.Sprintf("%04o", uint32(m)) }
```

- [ ] **Step 6: Write the routes and helpers**

Create `internal/jam/adminui/kit_draft.go`:

```go
package adminui

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// draftFormLimit caps a Draft-tab form body: the editor's text up to the
// per-file limit, percent-encoded (at most 3 bytes a byte), plus the fields.
func draftFormLimit() int64 { return 3*kittree.MaxFile + 64<<10 }

// draftUploadLimit caps a Draft-tab upload body: one file up to the per-file
// limit plus the multipart framing and the fields.
func draftUploadLimit() int64 { return kittree.MaxFile + 1<<20 }

// draftTooLarge is the 413 text for a Draft-tab body past its cap; name is
// a stored kit's (tag-safe).
func draftTooLarge(name string) string {
	return fmt.Sprintf("draft %s: the file is over %s (limit %s per file)", name, kittree.SizeText(kittree.MaxFile), kittree.SizeText(kittree.MaxFile))
}

// registerKitDraft mounts the kit page's Draft tab
// (docs/usage/jam/ui-pages.md#the-draft-tab) and its open and discard
// writes. Every write is a form POST through guardWrite that calls the jam
// draft functions directly, sending its form's etag as If-Match.
func registerKitDraft(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("GET /app/admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		q := draftQuery{File: r.URL.Query().Get("file"), Diff: r.URL.Query().Get("diff") == "1"}
		p, ok, err := buildDraftPage(store, name, q)
		switch {
		case !ok:
			renderStatus(w, r, http.StatusNotFound, "kit-draft", draftPage{Title: "Kits", NotFound: true, NotFoundFor: name})
		case err != nil:
			draftUIFail(w, r, log, err)
		default:
			renderStatus(w, r, p.status(), "kit-draft", p)
		}
	})

	// Open, from the form's version (empty: current). The answer redirects
	// the page to the Draft tab.
	mux.HandleFunc("POST /app/admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		from := 0
		if s := strings.TrimSpace(r.FormValue("from")); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				renderError(w, http.StatusBadRequest, "from must be a version number")
				return
			}
			from = n
		}
		d, err := jam.OpenKitDraft(store, name, from, jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		log.Info("ui kit draft opened", "operator", jam.OperatorID(r), "kit", name, "base", d.Base, "tree", shortDigest(d.Tree))
		w.Header().Set("HX-Redirect", draftURL(name))
		renderDraftBody(w, r, store, log, name, draftQuery{}, fmt.Sprintf("opened a draft of %s from v%d", name, d.Base))
	})

	mux.HandleFunc("POST /app/admin/kits/{name}/draft/discard", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		d, err := jam.DiscardKitDraft(store, log, name, r.FormValue("etag"))
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		log.Info("ui kit draft discarded", "operator", jam.OperatorID(r), "kit", name, "tree", shortDigest(d.Tree))
		renderDraftBody(w, r, store, log, name, draftQuery{}, "discarded the draft of "+name)
	})
}

// draftForm runs a Draft-tab write's preamble: the origin guard, the kit's
// existence (404, before the body is read), the body cap (413, from
// Content-Length, else as it reads) and the form parse (multipart for an
// upload). It returns the kit name, or false once it has answered.
func draftForm(w http.ResponseWriter, r *http.Request, store jam.Store, guardWrite func(http.ResponseWriter, *http.Request) bool, multipart bool) (string, bool) {
	if !guardWrite(w, r) {
		return "", false
	}
	name := r.PathValue("name")
	if _, ok := store.GetKit(name); !ok {
		renderError(w, http.StatusNotFound, fmt.Sprintf("kit %q does not exist", name))
		return "", false
	}
	limit := draftFormLimit()
	if multipart {
		limit = draftUploadLimit()
	}
	if r.ContentLength > limit {
		renderError(w, http.StatusRequestEntityTooLarge, draftTooLarge(name))
		return "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	var err error
	if multipart {
		err = r.ParseMultipartForm(limit) // in memory: the body is capped at limit
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		if _, big := errors.AsType[*http.MaxBytesError](err); big {
			renderError(w, http.StatusRequestEntityTooLarge, draftTooLarge(name))
			return "", false
		}
		renderError(w, http.StatusBadRequest, "invalid form")
		return "", false
	}
	return name, true
}

// draftUIFail answers a draft call's error: a jam.WriteError's own status and
// fixed text as an error fragment, else a fixed 500, the error logged (it
// names a kit or a tree prefix, never content).
func draftUIFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if we, ok := errors.AsType[*jam.WriteError](err); ok {
		renderError(w, we.Status, we.Msg)
		return
	}
	log.Error("ui kit draft: store error", "operator", jam.OperatorID(r), "method", r.Method, "route", r.Pattern, "err", err.Error())
	renderError(w, http.StatusInternalServerError, "internal error")
}

// loadDraftPage is buildDraftPage for a write's answer: false once it has
// answered a kit gone meanwhile (404) or a store fault.
func loadDraftPage(w http.ResponseWriter, r *http.Request, store jam.Store, log *slog.Logger, name string, q draftQuery) (draftPage, bool) {
	p, ok, err := buildDraftPage(store, name, q)
	if !ok {
		renderError(w, http.StatusNotFound, "kit no longer exists")
		return draftPage{}, false
	}
	if err != nil {
		draftUIFail(w, r, log, err)
		return draftPage{}, false
	}
	return p, true
}

// writeDraftBody renders p's #draft body, plus msg as the #flash message.
func writeDraftBody(w http.ResponseWriter, r *http.Request, p draftPage, msg string) {
	renderFragment(w, r, "kit-draft", "draft-body", p)
	writeFlash(w, msg)
}

// renderDraftBody answers a Draft-tab write: the #draft body showing q, and msg.
func renderDraftBody(w http.ResponseWriter, r *http.Request, store jam.Store, log *slog.Logger, name string, q draftQuery, msg string) {
	if p, ok := loadDraftPage(w, r, store, log, name, q); ok {
		writeDraftBody(w, r, p, msg)
	}
}
```

In `internal/jam/adminui/kits.go`, replace the flash write inside `renderKitBody`:

```go
	renderFragment(w, r, "kit", "kit-body", d)
	if msg != "" {
		_, _ = w.Write([]byte(`<div id="flash" hx-swap-oob="innerHTML"><p class="ok">` + template.HTMLEscapeString(msg) + `</p></div>`))
	}
}
```

with

```go
	renderFragment(w, r, "kit", "kit-body", d)
	writeFlash(w, msg)
}

// writeFlash appends msg as an out-of-band #flash success message, escaped;
// "" writes nothing.
func writeFlash(w http.ResponseWriter, msg string) {
	if msg != "" {
		_, _ = w.Write([]byte(`<div id="flash" hx-swap-oob="innerHTML"><p class="ok">` + template.HTMLEscapeString(msg) + `</p></div>`))
	}
}
```

In `internal/jam/adminui/kit_detail.go`:
- Add the field `Draft *jam.KitDraft // the kit's open draft; nil = none` to `kitDetail` (after `CanSource`).
- Add `Draft bool // an open draft` to `kitListRow` (after `Invalid`).
- In `kitListRows`, after `row := kitListRow{…}`, add `_, row.Draft = store.GetKitDraft(k.Name)`.
- In `buildKitDetail`, right after the `d := kitDetail{…}` line, add:

```go
	if dr, open := store.GetKitDraft(name); open {
		d.Draft = &dr
	}
```

In `internal/jam/adminui/adminui.go`:
- In `pages`, change the kit entry and add the tab:

```go
	"kit":          mustParse(specsPage("/app/admin/kits"), "kit_shared.html", "kit.html"),
	"kit-draft":    mustParse(specsPage("/app/admin/kits"), "kit_shared.html", "kit_draft.html"),
```

- In `funcs`, after `"kitURL":     kitURL,` add `"draftURL":   draftURL,`.
- In `Handler`, after `registerKits(mux, store, o.kitFetcher, log, guardWrite)` add `registerKitDraft(mux, store, log, guardWrite)`.

- [ ] **Step 7: Run tests to verify they pass**

Run: `gofmt -l internal/jam/adminui && go test ./internal/jam/adminui/ -v -run 'TestDraft|TestOpenDraft|TestDiscardDraft|TestKit|TestDiffLinesPartialOnEveryPage'`
Expected: PASS, with the kit-page tests unchanged (the templates moved, the markup is the same).

- [ ] **Step 8: Commit**

```bash
git add internal/jam/adminui
git commit -m "feat(adminui): kit Draft tab — tree, problems, open and discard" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The editor — save with ETag preservation, editability, download

**Files:**
- Create: `internal/jam/adminui/kit_draft_editor.go`
- Modify: `internal/jam/adminui/kit_draft_view.go` (`draftView.Editor`, `FileMissing`, `buildDraftPage`, `status`), `internal/jam/adminui/templates/kit_draft.html` (`{{/* draft-editor */}}`), `internal/jam/adminui/adminui.go` (registration)
- Test: `internal/jam/adminui/kit_draft_editor_test.go`; `kit_draft_test.go` (`draftWriteRoutes`)

**Interfaces:**
- Consumes: Task 2's `draftForm`, `draftUIFail`, `loadDraftPage`, `writeDraftBody`, `renderDraftBody`, `draftURL`, `draftFileHref`, `fileModeText`, `buildDraftPage`; `jam.KitDraftFile(store, name, p) ([]byte, kittree.Entry, string, error)`, `jam.PutKitDraftFile(store, log, name, p string, mode fs.FileMode, data []byte, ifMatch, actor string, now time.Time) (jam.KitDraftSave, error)`, `jam.SetupDisplayText`, `kittree.IsText`.
- Produces:
  - `const maxDraftEditBytes = 1 << 20`
  - `type draftEditor struct{ Path, RawPath, Mode string; Exec bool; Size int64; ETag string; Editable bool; Text string; Invisible bool; Note string; Stale bool; StaleMsg, ReloadHref, DownloadHref, DiffHref string }`
  - `func draftEditorFor(store jam.Store, kit, p string) (*draftEditor, error)`
  - `func openDraftFile(store jam.Store, kit, p string, v *draftView) error`
  - `func staleEditor(kit, p, text, etag string, mode fs.FileMode, msg string) *draftEditor`
  - `func editorText(s string) string`
  - `func formMode(r *http.Request) fs.FileMode`
  - `func savedMsg(p string, s jam.KitDraftSave) string`
  - `func logDraftSave(log *slog.Logger, r *http.Request, name string, s jam.KitDraftSave)`
  - `func draftDownloadHref(kit, p string) string`
  - `func registerDraftEditor(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool)`
  - Fields `draftView.Editor *draftEditor` and `draftView.FileMissing bool`.
  - Template anchors inside the editor card: `{{/* draft-file-upload */}}` (Task 4) and `{{/* draft-file-actions */}}` (Task 4).
  - Test helper: `editorValue(t, body string) string`.

- [ ] **Step 1: Write the failing tests**

In `internal/jam/adminui/kit_draft_test.go`, change the routes table to

```go
var draftWriteRoutes = []string{"/draft", "/draft/discard", "/draft/save"}
```

Create `internal/jam/adminui/kit_draft_editor_test.go`:

```go
package adminui_test

import (
	"html"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// editorValue is the editor textarea's value as a browser holds it: the
// markup unescaped, less the one newline the HTML parser drops after the
// start tag.
func editorValue(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `name="content"`)
	if i < 0 {
		t.Fatalf("no editor in:\n%s", body)
	}
	s := body[i:]
	s = s[strings.Index(s, ">")+1:]
	end := strings.Index(s, "</textarea>")
	if end < 0 {
		t.Fatal("unterminated textarea")
	}
	return strings.TrimPrefix(html.UnescapeString(s[:end]), "\n")
}

func TestDraftEditorSaves(t *testing.T) {
	store, h := draftFixture(t)
	body := get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String()
	ed := section(body, "draft-editor")
	if editorValue(t, body) != "echo\n" || !strings.Contains(ed, `name="executable" value="1" checked`) || !strings.Contains(ed, `href="/app/admin/kits/app/draft/download?path=run.sh"`) {
		t.Fatalf("editor:\n%s", ed)
	}
	// The browser sends CRLF; unchecking Executable makes it 0644.
	rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {hiddenETag(t, ed)}, "path": {"run.sh"}, "content": {"echo two\r\n"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "saved run.sh") {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	data, e, _, _ := jam.KitDraftFile(store, "app", "run.sh")
	if string(data) != "echo two\n" || e.Mode != 0o644 {
		t.Fatalf("stored %q %o", data, e.Mode)
	}
	// The answer re-opens the file at the draft's new ETag.
	if got := hiddenETag(t, section(rec.Body.String(), "draft-editor")); got != draftETag(t, store) {
		t.Fatalf("re-rendered editor etag = %q", got)
	}
	// An invalid kit.yml is saved; its problem shows.
	rec = post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {draftETag(t, store)}, "path": {"kit.yml"}, "content": {"kind: nope\n"}})
	if rec.Code != http.StatusOK || !strings.Contains(section(rec.Body.String(), "draft-publish"), "kind must be &#34;studio&#34;") {
		t.Fatalf("save kit.yml = %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"path": {"run.sh"}, "content": {"x"}}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no etag = %d", rec.Code)
	}
}

// Review Focus 1: two tabs. The second save is refused, keeps its typed
// text with the fixed message and a reload link, keeps its stale ETag and
// disables Save; the first tab's save stands.
func TestDraftStaleSaveKeepsText(t *testing.T) {
	store, h := draftFixture(t)
	tab1 := hiddenETag(t, section(get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String(), "draft-editor"))
	tab2 := hiddenETag(t, section(get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String(), "draft-editor"))
	if rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {tab2}, "path": {"run.sh"}, "content": {"echo tab2\n"}, "executable": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("tab 2 save = %d %s", rec.Code, rec.Body)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {tab1}, "path": {"run.sh"}, "content": {"echo <tab1> typed\r\n"}, "executable": {"1"}})
	body := rec.Body.String()
	ed := section(body, "draft-editor")
	if rec.Code != http.StatusOK || !strings.Contains(ed, `draft app changed since you loaded it (reload) <a href="/app/admin/kits/app/draft?file=run.sh">reload</a>`) {
		t.Fatalf("stale save = %d:\n%s", rec.Code, ed)
	}
	if got := editorValue(t, body); got != "echo <tab1> typed\n" {
		t.Fatalf("typed text = %q", got)
	}
	if hiddenETag(t, ed) != tab1 || !strings.Contains(ed, `<button type="submit" class="primary" disabled>Save</button>`) {
		t.Fatalf("a stale editor must keep its etag and disable Save:\n%s", ed)
	}
	if data, _, _, _ := jam.KitDraftFile(store, "app", "run.sh"); string(data) != "echo tab2\n" {
		t.Fatalf("stored %q: a stale save overwrote tab 2", data)
	}
}

// Review Focus 2: what the editor shows, a browser submits back byte for byte.
func TestDraftEditorRoundTripsBytes(t *testing.T) {
	store, h := draftFixture(t)
	for p, content := range map[string]string{
		"bom.txt":     "\ufeffkey: value\n\tindented\n\nno final newline",
		"lead.txt":    "\n\nlead\n",
		"markup.html": "<script>alert(1)</script>\n&amp; </textarea>\n",
		"bidi.md":     "名前 ✓ \u202eevil\n",
	} {
		t.Run(p, func(t *testing.T) {
			putDraft(t, store, p, 0, content)
			body := get(t, h, "/app/admin/kits/app/draft?file="+url.QueryEscape(p)).Body.String()
			ed := section(body, "draft-editor")
			if strings.Contains(ed, "<script>alert") {
				t.Fatal("file content rendered as markup")
			}
			got := editorValue(t, body)
			if got != content {
				t.Fatalf("editor holds %q, want %q", got, content)
			}
			if invisible := strings.Contains(ed, `id="draft-invisible"`); invisible != (p == "bom.txt" || p == "bidi.md") {
				t.Errorf("invisible-characters note = %v", invisible)
			}
			rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {hiddenETag(t, ed)}, "path": {p}, "content": {strings.ReplaceAll(got, "\n", "\r\n")}})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), p+" unchanged") {
				t.Fatalf("save = %d %s", rec.Code, rec.Body)
			}
			if data, _, _, _ := jam.KitDraftFile(store, "app", p); string(data) != content {
				t.Fatalf("stored %q, want %q", data, content)
			}
		})
	}
	if body := get(t, h, "/app/admin/kits/app/draft?file=lead.txt").Body.String(); !strings.Contains(body, "wrap=\"off\">\n\n\nlead\n</textarea>") {
		t.Error("the template must write one newline after <textarea> so a leading newline survives")
	}
}

// Review Focus 3: only text of at most 1 MiB without CR is editable; any
// other file offers Download and Replace instead.
func TestDraftEditorNotEditable(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "max.txt", 0, strings.Repeat("a", 1<<20))
	putDraft(t, store, "over.txt", 0, strings.Repeat("a", 1<<20+1))
	putDraft(t, store, "win.txt", 0, "a\r\nb\r\n")
	putDraft(t, store, "blob.bin", 0, "\x00\x01")
	if ed := section(get(t, h, "/app/admin/kits/app/draft?file=max.txt").Body.String(), "draft-editor"); !strings.Contains(ed, `name="content"`) {
		t.Fatal("a 1 MiB text file should be editable")
	}
	for p, note := range map[string]string{"over.txt": "over 1 MiB", "win.txt": "carriage returns", "blob.bin": "binary file"} {
		ed := section(get(t, h, "/app/admin/kits/app/draft?file="+p).Body.String(), "draft-editor")
		if strings.Contains(ed, `name="content"`) || !strings.Contains(ed, `id="draft-noedit"`) || !strings.Contains(ed, note) || !strings.Contains(ed, `href="/app/admin/kits/app/draft/download?path=`+p+`"`) {
			t.Errorf("%s:\n%.600s", p, ed)
		}
	}
}

// An absent or unsafe ?file= is a 404 that never names it.
func TestDraftFileMissing(t *testing.T) {
	_, h := draftFixture(t)
	for _, p := range []string{"nope.txt", "a\u202eb", "../x"} {
		rec := get(t, h, "/app/admin/kits/app/draft?file="+url.QueryEscape(p))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No such file in the draft.") || strings.Contains(rec.Body.String(), "\u202e") || strings.Contains(rec.Body.String(), `name="content"`) {
			t.Errorf("%q = %d", p, rec.Code)
		}
	}
}

func TestDraftDownload(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "docs/名前.bin", 0, "\x00\x01<b>")
	rec := get(t, h, "/app/admin/kits/app/draft/download?path="+url.QueryEscape("docs/名前.bin"))
	hd := rec.Header()
	if rec.Code != http.StatusOK || rec.Body.String() != "\x00\x01<b>" || hd.Get("Content-Type") != "application/octet-stream" || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Cache-Control") != "no-store" {
		t.Fatalf("download = %d %v %q", rec.Code, hd, rec.Body)
	}
	if disp, params, err := mime.ParseMediaType(hd.Get("Content-Disposition")); err != nil || disp != "attachment" || params["filename"] != "名前.bin" {
		t.Fatalf("Content-Disposition = %q", hd.Get("Content-Disposition"))
	}
	if rec := get(t, h, "/app/admin/kits/app/draft/download?path=nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", rec.Code)
	}
	if rec := get(t, h, "/app/admin/kits/app/draft/download?path="+url.QueryEscape("a\u202eb")); rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "\u202e") {
		t.Fatalf("unsafe = %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jam/adminui/ -run 'TestDraftEditor|TestDraftStale|TestDraftFileMissing|TestDraftDownload|TestDraftWritesRefuseCrossOrigin' -v`
Expected: FAIL (no editor in the page; `/draft/save` and `/draft/download` 404/405).

- [ ] **Step 3: Write the editor**

Create `internal/jam/adminui/kit_draft_editor.go`:

```go
package adminui

import (
	"bytes"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// maxDraftEditBytes is the largest file the Draft tab edits as text (spec
// §3: "text editor for UTF-8 files ≤ 1 MiB"); a larger one is downloaded
// and replaced.
const maxDraftEditBytes = 1 << 20

// draftEditor is one draft file, opened: its text in the editor, or why not.
type draftEditor struct {
	Path         string // display text
	RawPath      string // the path itself, for the forms' hidden fields
	Mode         string
	Exec         bool
	Size         int64
	ETag         string // the draft's ETag the file was read at: its forms send it back
	Editable     bool
	Text         string // the file's exact text (Editable)
	Invisible    bool   // Text holds control or format characters (a BOM, a bidi override)
	Note         string // why the file is not editable
	Stale        bool   // a refused stale save: Text is the submitted text, unsaved
	StaleMsg     string
	ReloadHref   string
	DownloadHref string
	DiffHref     string
}

func draftDownloadHref(kit, p string) string {
	return draftURL(kit) + "/download?path=" + url.QueryEscape(p)
}

// draftEditorFor reads file p of kit's draft for the editor. Text is edited
// only when it is valid text (kittree.IsText) of at most maxDraftEditBytes
// with no CR: a browser submits a textarea's line breaks as CRLF, so a CR
// could not survive the round trip.
func draftEditorFor(store jam.Store, kit, p string) (*draftEditor, error) {
	data, e, etag, err := jam.KitDraftFile(store, kit, p)
	if err != nil {
		return nil, err
	}
	ed := &draftEditor{
		Path: pathText(p), RawPath: p, Mode: fileModeText(e.Mode), Exec: e.Mode == 0o755, Size: e.Size, ETag: etag,
		DownloadHref: draftDownloadHref(kit, p), DiffHref: draftFileHref(kit, p, true),
	}
	switch {
	case len(data) > maxDraftEditBytes:
		ed.Note = "over 1 MiB (" + bytesText(e.Size) + "): too large to edit here — Download it, or Replace it with an upload"
	case !kittree.IsText(data):
		ed.Note = "binary file (" + bytesText(e.Size) + "): not editable here — Download it, or Replace it with an upload"
	case bytes.IndexByte(data, '\r') >= 0:
		ed.Note = "contains carriage returns (CRLF line ends): the browser editor would rewrite them as LF — Download it, edit it locally, and Replace it"
	default:
		ed.Editable, ed.Text = true, string(data)
		ed.Invisible = jam.SetupDisplayText(ed.Text) != ed.Text
	}
	return ed, nil
}

// openDraftFile sets v's editor to file p, or FileMissing when the draft has
// no such file or p is unsafe (never named); another error is a store fault.
func openDraftFile(store jam.Store, kit, p string, v *draftView) error {
	ed, err := draftEditorFor(store, kit, p)
	switch status := jam.WriteStatus(err, http.StatusInternalServerError); {
	case err == nil:
		v.Editor = ed
	case status == http.StatusNotFound || status == http.StatusBadRequest:
		v.FileMissing = true
	default:
		return err
	}
	return nil
}

// staleEditor is the editor after a refused stale save of p: the submitted
// text kept, the form's own stale etag kept (so Save stays refused), and the
// fixed message with a reload link. Nothing was saved.
func staleEditor(kit, p, text, etag string, mode fs.FileMode, msg string) *draftEditor {
	return &draftEditor{
		Path: pathText(p), RawPath: p, Mode: fileModeText(mode), Exec: mode == 0o755, Size: int64(len(text)), ETag: etag,
		Editable: true, Text: text, Invisible: jam.SetupDisplayText(text) != text,
		Stale: true, StaleMsg: msg, ReloadHref: draftFileHref(kit, p, false),
		DownloadHref: draftDownloadHref(kit, p), DiffHref: draftFileHref(kit, p, true),
	}
}

// editorText is a submitted textarea's text as the file's bytes: HTML form
// submission sends its line breaks as CRLF, so each CRLF, and any lone CR,
// becomes LF.
func editorText(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// formMode is a form's Executable checkbox as a file mode.
func formMode(r *http.Request) fs.FileMode {
	if r.FormValue("executable") != "" {
		return 0o755
	}
	return 0o644
}

// savedMsg is a file write's flash message.
func savedMsg(p string, s jam.KitDraftSave) string {
	if len(s.Changed) == 0 {
		return pathText(p) + " unchanged"
	}
	return "saved " + pathText(p)
}

// logDraftSave is a UI draft write's audit line, only when it changed the
// tree: never content or a path.
func logDraftSave(log *slog.Logger, r *http.Request, name string, s jam.KitDraftSave) {
	if len(s.Changed) == 0 {
		return
	}
	log.Info("ui kit draft saved", "operator", jam.OperatorID(r), "kit", name, "tree", shortDigest(s.Tree), "changed", len(s.Changed))
}

// registerDraftEditor mounts the editor's save and the file download.
func registerDraftEditor(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/save", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		p, etag, mode := r.FormValue("path"), r.FormValue("etag"), formMode(r)
		text := editorText(r.FormValue("content"))
		s, err := jam.PutKitDraftFile(store, log, name, p, mode, []byte(text), etag, jam.OperatorID(r), time.Now().UTC())
		if err != nil && jam.WriteStatus(err, http.StatusInternalServerError) == http.StatusPreconditionFailed {
			renderStaleSave(w, r, store, log, name, p, text, etag, mode, err)
			return
		}
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{File: p}, savedMsg(p, s))
	})

	mux.HandleFunc("GET /app/admin/kits/{name}/draft/download", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		data, _, _, err := jam.KitDraftFile(store, r.PathValue("name"), p)
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		disp := mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(p)})
		if disp == "" {
			disp = "attachment"
		}
		h.Set("Content-Disposition", disp)
		_, _ = w.Write(data)
	})
}

// renderStaleSave answers a save refused as stale (412) with the #draft body
// as it is now, its editor holding the submitted text unsaved (staleEditor).
// It answers 200, since htmx swaps only 2xx. Should the draft be gone by
// now, the 412 itself is answered, and htmx leaves the typed text in place.
func renderStaleSave(w http.ResponseWriter, r *http.Request, store jam.Store, log *slog.Logger, name, p, text, etag string, mode fs.FileMode, stale error) {
	pg, ok := loadDraftPage(w, r, store, log, name, draftQuery{})
	if !ok {
		return
	}
	if pg.Draft == nil {
		draftUIFail(w, r, log, stale)
		return
	}
	pg.Draft.Editor = staleEditor(name, p, text, etag, mode, stale.Error())
	writeDraftBody(w, r, pg, "")
}
```

In `internal/jam/adminui/kit_draft_view.go`:
- Add two fields to `draftView` (after `Removed`):

```go
	Editor      *draftEditor // the file asked for (?file=), opened; nil = none
	FileMissing bool         // ?file= named no file of the draft (or an unsafe path)
```

- In `buildDraftPage`, replace

```go
	p.Draft = newDraftView(name, st, base.Tree)
	return p, true, nil
}
```

  with

```go
	p.Draft = newDraftView(name, st, base.Tree)
	if q.File != "" && !q.Diff {
		if err := openDraftFile(store, name, q.File, p.Draft); err != nil {
			return p, true, err
		}
	}
	return p, true, nil
}
```

- Replace `func (p draftPage) status() int { return http.StatusOK }` with

```go
// status is the GET answer's code for p: 404 when the file asked for is not
// in the draft.
func (p draftPage) status() int {
	if d := p.Draft; d != nil && d.FileMissing {
		return http.StatusNotFound
	}
	return http.StatusOK
}
```

In `internal/jam/adminui/adminui.go` `Handler`, after `registerKitDraft(mux, store, log, guardWrite)` add `registerDraftEditor(mux, store, log, guardWrite)`.

- [ ] **Step 4: Write the editor template**

In `internal/jam/adminui/templates/kit_draft.html`, replace the line `{{/* draft-editor */}}` with the block below. The newline right after the `<textarea …>` start tag is required (ruling 16):

```html
{{if .FileMissing}}<section class="card" id="draft-missing"><div class="body"><div class="banner error">No such file in the draft.</div></div></section>{{end}}
{{with .Editor}}
<section class="card" id="draft-editor">
  <header><h2 class="mono">{{.Path}}</h2><span class="sub">{{.Mode}} · {{.Size}} B</span><span class="spacer"></span>
    <a class="sub" href="{{.DiffHref}}">diff vs v{{$.Draft.Base}}</a> <a class="sub" href="{{.DownloadHref}}">Download</a></header>
  {{if .Stale}}<div class="body" id="draft-stale"><div class="banner error">{{.StaleMsg}} <a href="{{.ReloadHref}}">reload</a></div>
    <p class="hint">Your text below was not saved. Copy it, reload, and make the edit again.</p></div>{{end}}
  {{if .Editable}}
  <form class="body" hx-post="/app/admin/kits/{{$name}}/draft/save" hx-target="#draft" hx-swap="outerHTML">
    <input type="hidden" name="etag" value="{{.ETag}}"><input type="hidden" name="path" value="{{.RawPath}}">
    {{if .Invisible}}<p class="hint" id="draft-invisible">This file holds invisible or bidirectional characters (a byte-order mark, a direction override); the editor keeps them as they are, and diffs show them escaped.</p>{{end}}
    <textarea class="editor" name="content" spellcheck="false" autocomplete="off" wrap="off">
{{.Text}}</textarea>
    <label class="check"><input type="checkbox" name="executable" value="1"{{if .Exec}} checked{{end}}> Executable (0755)</label>
    <div class="form-actions"><button type="submit" class="primary"{{if .Stale}} disabled{{end}}>Save</button></div>
  </form>
  {{else}}
  <div class="body"><p class="unset" id="draft-noedit">{{.Note}}</p></div>
  {{/* draft-file-upload */}}
  {{end}}
  {{/* draft-file-actions */}}
</section>
{{end}}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `gofmt -l internal/jam/adminui && go test ./internal/jam/adminui/ -run 'TestDraft' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/jam/adminui
git commit -m "feat(adminui): Draft tab editor — ETag-safe save, CRLF ruling, download" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Replace by upload, and the mode toggle

**Files:**
- Create: `internal/jam/adminui/kit_draft_upload.go`
- Modify: `internal/jam/adminui/templates/kit_draft.html` (`{{/* draft-file-upload */}}`, `{{/* draft-file-actions */}}`), `internal/jam/adminui/adminui.go`
- Test: `internal/jam/adminui/kit_draft_upload_test.go`; `kit_draft_test.go` (`draftWriteRoutes`)

**Interfaces:**
- Consumes: `draftForm(…, true)`, `draftUIFail`, `renderDraftBody`, `formMode`, `savedMsg`, `logDraftSave`, `fileModeText`, `pathText`; `jam.KitDraftFile`, `jam.PutKitDraftFile`; `kittree.MaxFile`.
- Produces:
  - `func uploadedFile(r *http.Request) ([]byte, bool, error)` (used by Task 5's add)
  - `func registerDraftUploads(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool)`
  - routes `POST /app/admin/kits/{name}/draft/replace` (multipart: `etag`, `path`, `executable`, `file`) and `POST …/draft/mode` (`etag`, `path`, `executable`)
  - template anchor `{{/* draft-file-paths */}}` (Task 5)
  - test helper `postMultipart(t, h, path string, fields map[string]string, file []byte) *httptest.ResponseRecorder`

- [ ] **Step 1: Write the failing tests**

In `kit_draft_test.go`, set

```go
var draftWriteRoutes = []string{"/draft", "/draft/discard", "/draft/save", "/draft/replace", "/draft/mode"}
```

Create `internal/jam/adminui/kit_draft_upload_test.go`:

```go
package adminui_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// postMultipart posts fields and, when file is non-nil, a "file" part whose
// own name ("../evil<script>.txt") must be ignored, same-origin.
func postMultipart(t *testing.T, h http.Handler, path string, fields map[string]string, file []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if file != nil {
		fw, err := mw.CreateFormFile("file", "../evil<script>.txt")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(file)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDraftReplaceUpload(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "blob.bin", 0, "\x00\x01")
	ed := section(get(t, h, "/app/admin/kits/app/draft?file=blob.bin").Body.String(), "draft-editor")
	if !strings.Contains(ed, `id="draft-replace"`) || !strings.Contains(ed, `hx-encoding="multipart/form-data"`) {
		t.Fatalf("no replace form:\n%s", ed)
	}
	rec := postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": hiddenETag(t, ed), "path": "blob.bin", "executable": "1"}, []byte("\x00\x02\x03"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "saved blob.bin") || strings.Contains(rec.Body.String(), "evil") {
		t.Fatalf("replace = %d %s", rec.Code, rec.Body)
	}
	if data, e, _, _ := jam.KitDraftFile(store, "app", "blob.bin"); string(data) != "\x00\x02\x03" || e.Mode != 0o755 {
		t.Fatalf("stored %q %o", data, e.Mode)
	}
	if st, _ := jam.KitDraftStatusOf(store, "app"); len(st.Entries) != 5 {
		t.Fatalf("the upload's own name must be ignored: entries %+v", st.Entries)
	}
	if rec := postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": draftETag(t, store), "path": "blob.bin"}, nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "choose a file to upload") {
		t.Fatalf("no file = %d %s", rec.Code, rec.Body)
	}
	if rec := postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": hiddenETag(t, ed), "path": "blob.bin"}, []byte("x")); rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale replace = %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/replace", url.Values{"etag": {draftETag(t, store)}, "path": {"blob.bin"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a non-multipart replace = %d", rec.Code)
	}
}

// Over the per-file limit: jam's own 413 within the body cap; past the cap,
// a fixed 413 before the body is read. Nothing changes either way.
func TestDraftUploadOverLimit(t *testing.T) {
	store, h := draftFixture(t)
	old := kittree.MaxFile
	kittree.MaxFile = 1024
	t.Cleanup(func() { kittree.MaxFile = old })
	tag := draftETag(t, store)
	rec := postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": tag, "path": "run.sh"}, bytes.Repeat([]byte("a"), 2000))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "draft app: run.sh: more than 1024 bytes (limit 1024 bytes per file)") {
		t.Fatalf("over the file limit = %d %s", rec.Code, rec.Body)
	}
	rec = postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": tag, "path": "run.sh"}, bytes.Repeat([]byte("a"), 2<<20))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "draft app: the file is over 1024 bytes (limit 1024 bytes per file)") {
		t.Fatalf("past the body cap = %d %s", rec.Code, rec.Body)
	}
	rec = post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {tag}, "path": {"run.sh"}, "content": {strings.Repeat("b", 2000)}})
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "draft app: run.sh: more than 1024 bytes") {
		t.Fatalf("an editor text over the limit = %d %s", rec.Code, rec.Body)
	}
	if draftETag(t, store) != tag {
		t.Fatal("a refused upload changed the draft")
	}
}

func TestDraftModeToggle(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "blob.bin", 0, "\x00\x01")
	ed := section(get(t, h, "/app/admin/kits/app/draft?file=blob.bin").Body.String(), "draft-editor")
	if !strings.Contains(ed, `id="draft-mode"`) || strings.Contains(ed, `name="content"`) {
		t.Fatalf("mode form:\n%s", ed)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/mode", url.Values{"etag": {hiddenETag(t, ed)}, "path": {"blob.bin"}, "executable": {"1"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "blob.bin is now 0755") {
		t.Fatalf("mode = %d %s", rec.Code, rec.Body)
	}
	if data, e, _, _ := jam.KitDraftFile(store, "app", "blob.bin"); string(data) != "\x00\x01" || e.Mode != 0o755 {
		t.Fatalf("stored %q %o", data, e.Mode)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/mode", url.Values{"etag": {hiddenETag(t, ed)}, "path": {"blob.bin"}}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale mode = %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jam/adminui/ -run 'TestDraftReplace|TestDraftUploadOverLimit|TestDraftModeToggle|TestDraftWritesRefuseCrossOrigin' -v`
Expected: FAIL (no replace/mode forms, routes 404/405).

- [ ] **Step 3: Write the routes**

Create `internal/jam/adminui/kit_draft_upload.go`:

```go
package adminui

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// uploadedFile is the multipart form's "file" part: its bytes, read up to
// kittree.MaxFile+1 so an oversize file meets jam's own 413, and whether one
// was sent. Its file name is never read: the path comes from the form.
func uploadedFile(r *http.Request) ([]byte, bool, error) {
	f, _, err := r.FormFile("file")
	if errors.Is(err, http.ErrMissingFile) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, kittree.MaxFile+1))
	return b, true, err
}

// registerDraftUploads mounts Replace (a multipart upload over one draft
// file) and the Executable toggle of a file the editor does not open.
func registerDraftUploads(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/replace", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, true)
		if !ok {
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		data, sent, err := uploadedFile(r)
		switch {
		case err != nil:
			renderError(w, http.StatusBadRequest, "the upload could not be read")
			return
		case !sent:
			renderError(w, http.StatusBadRequest, "choose a file to upload")
			return
		}
		p := r.FormValue("path")
		s, err := jam.PutKitDraftFile(store, log, name, p, formMode(r), data, r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{File: p}, savedMsg(p, s))
	})

	// The file is re-put with its own bytes at the new mode, against the
	// form's etag: a save since the form was loaded makes it stale.
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/mode", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		p, mode := r.FormValue("path"), formMode(r)
		data, _, _, err := jam.KitDraftFile(store, name, p)
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		s, err := jam.PutKitDraftFile(store, log, name, p, mode, data, r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		msg := pathText(p) + " is now " + fileModeText(mode)
		if len(s.Changed) == 0 {
			msg = savedMsg(p, s)
		}
		renderDraftBody(w, r, store, log, name, draftQuery{File: p}, msg)
	})
}
```

In `adminui.go` `Handler`, after `registerDraftEditor(mux, store, log, guardWrite)` add `registerDraftUploads(mux, store, log, guardWrite)`.

- [ ] **Step 4: Write the templates**

In `kit_draft.html`, replace `  {{/* draft-file-upload */}}` with

```html
  <form class="body" id="draft-mode" hx-post="/app/admin/kits/{{$name}}/draft/mode" hx-target="#draft" hx-swap="outerHTML">
    <input type="hidden" name="etag" value="{{.ETag}}"><input type="hidden" name="path" value="{{.RawPath}}">
    <label class="check"><input type="checkbox" name="executable" value="1"{{if .Exec}} checked{{end}}> Executable (0755)</label>
    <div class="form-actions"><button type="submit" class="small">Set mode</button></div>
  </form>
```

and replace `  {{/* draft-file-actions */}}` with

```html
  <details class="edit" id="draft-replace"><summary>Replace with an upload…</summary>
    <form hx-post="/app/admin/kits/{{$name}}/draft/replace" hx-encoding="multipart/form-data" hx-target="#draft" hx-swap="outerHTML">
      <input type="hidden" name="etag" value="{{.ETag}}"><input type="hidden" name="path" value="{{.RawPath}}">
      <label>New content <span class="hint">up to 4 MiB; the uploaded file's own name is ignored</span><input type="file" name="file" required></label>
      <label class="check"><input type="checkbox" name="executable" value="1"{{if .Exec}} checked{{end}}> Executable (0755)</label>
      <div class="form-actions"><button type="submit" class="primary">Replace</button></div>
    </form>
  </details>
  {{/* draft-file-paths */}}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `gofmt -l internal/jam/adminui && go test ./internal/jam/adminui/ -run 'TestDraft' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/jam/adminui
git commit -m "feat(adminui): Draft tab replace upload and mode toggle" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Add, rename, delete

**Files:**
- Create: `internal/jam/adminui/kit_draft_paths.go`
- Modify: `internal/jam/adminui/templates/kit_draft.html` (`{{/* draft-add */}}`, `{{/* draft-file-paths */}}`), `internal/jam/adminui/adminui.go`
- Test: `internal/jam/adminui/kit_draft_paths_test.go`; `kit_draft_test.go` (`draftWriteRoutes`)

**Interfaces:**
- Consumes: Task 1 `jam.RenameKitDraftFile(store, log, name, from, to, ifMatch, actor string, now time.Time) (jam.KitDraftSave, error)`; `jam.RemoveKitDraftFile(store, log, name, p, ifMatch, actor string, now time.Time) (jam.KitDraftSave, error)`; `jam.KitDraftFile`, `jam.PutKitDraftFile`; Task 4 `uploadedFile`; Task 3 `formMode`, `logDraftSave`; Task 2 `draftForm`, `draftUIFail`, `renderDraftBody`.
- Produces: `func registerDraftPaths(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool)`; routes `POST …/draft/add` (multipart: `etag`, `path`, `executable`, optional `file`), `POST …/draft/rename` (`etag`, `from`, `to`), `POST …/draft/delete` (`etag`, `path`).

- [ ] **Step 1: Write the failing tests**

In `kit_draft_test.go`, set

```go
var draftWriteRoutes = []string{"/draft", "/draft/discard", "/draft/save", "/draft/replace", "/draft/mode", "/draft/add", "/draft/rename", "/draft/delete"}
```

Create `internal/jam/adminui/kit_draft_paths_test.go`:

```go
package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestDraftAddFile(t *testing.T) {
	store, h := draftFixture(t)
	if add := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-add"); !strings.Contains(add, `hx-post="/app/admin/kits/app/draft/add"`) {
		t.Fatalf("no add form:\n%s", add)
	}
	rec := postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": draftETag(t, store), "path": " scripts/new.sh ", "executable": "1"}, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "added scripts/new.sh") || editorValue(t, body) != "" {
		t.Fatalf("add empty = %d %s", rec.Code, body)
	}
	if data, e, _, err := jam.KitDraftFile(store, "app", "scripts/new.sh"); err != nil || len(data) != 0 || e.Mode != 0o755 {
		t.Fatalf("added %q %o %v", data, e.Mode, err)
	}
	rec = postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": draftETag(t, store), "path": "data/blob.bin"}, []byte{0, 1, 2})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "evil") {
		t.Fatalf("add upload = %d %s", rec.Code, rec.Body)
	}
	// An add never overwrites.
	rec = postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": draftETag(t, store), "path": "run.sh"}, []byte("clobber"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "draft app already has a file run.sh") {
		t.Fatalf("add over a file = %d %s", rec.Code, rec.Body)
	}
	if data, _, _, _ := jam.KitDraftFile(store, "app", "run.sh"); string(data) != "echo\n" {
		t.Fatalf("run.sh = %q", data)
	}
}

// Review Focus 4: hostile paths are refused by CheckPath on add and rename,
// never echoed; a graphic name with markup is fine and shown escaped.
func TestDraftHostilePaths(t *testing.T) {
	store, h := draftFixture(t)
	tag := draftETag(t, store)
	for _, p := range []string{"\u202etxt.exe", "a/../b", "/etc/passwd", "a\x1b[31mb", "docs//x"} {
		for _, rec := range []*httptest.ResponseRecorder{
			postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": tag, "path": p}, nil),
			post(t, h, "/app/admin/kits/app/draft/rename", url.Values{"etag": {tag}, "from": {"run.sh"}, "to": {p}}),
		} {
			body := rec.Body.String()
			if rec.Code != http.StatusBadRequest || !strings.Contains(body, "unsafe path") || strings.Contains(body, "\u202e") || strings.Contains(body, "txt.exe") || strings.Contains(body, "passwd") || strings.Contains(body, "\x1b") {
				t.Errorf("%q = %d %s", p, rec.Code, body)
			}
		}
	}
	if draftETag(t, store) != tag {
		t.Fatal("a hostile path changed the draft")
	}
	rec := postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": tag, "path": "docs/<b>名前</b>.md"}, []byte("x\n"))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "<b>名前") || !strings.Contains(section(body, "draft-tree"), "&lt;b&gt;名前&lt;/b&gt;.md") {
		t.Fatalf("markup name = %d %s", rec.Code, body)
	}
}

func TestDraftRenameAndDelete(t *testing.T) {
	store, h := draftFixture(t)
	ed := section(get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String(), "draft-editor")
	if !strings.Contains(ed, `hx-confirm="Remove run.sh from the draft?"`) || !strings.Contains(ed, `name="to" required value="run.sh"`) {
		t.Fatalf("path actions:\n%s", ed)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/rename", url.Values{"etag": {hiddenETag(t, ed)}, "from": {"run.sh"}, "to": {"bin/run.sh"}})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "renamed run.sh to bin/run.sh") || editorValue(t, body) != "echo\n" {
		t.Fatalf("rename = %d %s", rec.Code, body)
	}
	tree := section(body, "draft-tree")
	if !strings.Contains(tree, `data-path="bin/run.sh" data-change="added"`) || !strings.Contains(tree, `data-path="run.sh" data-change="removed"`) {
		t.Fatalf("tree after rename:\n%s", tree)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/rename", url.Values{"etag": {draftETag(t, store)}, "from": {"bin/run.sh"}, "to": {"kit.yml"}}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "draft app already has a file kit.yml") {
		t.Fatalf("rename onto a file = %d %s", rec.Code, rec.Body)
	}
	rec = post(t, h, "/app/admin/kits/app/draft/delete", url.Values{"etag": {draftETag(t, store)}, "path": {"docs/a.md"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "removed docs/a.md") {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if _, _, _, err := jam.KitDraftFile(store, "app", "docs/a.md"); jam.WriteStatus(err, 500) != http.StatusNotFound {
		t.Fatalf("docs/a.md still there: %v", err)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/delete", url.Values{"etag": {hiddenETag(t, ed)}, "path": {"kit.yml"}}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale delete = %d", rec.Code)
	}
	if _, _, _, err := jam.KitDraftFile(store, "app", "kit.yml"); err != nil {
		t.Fatal("a stale delete removed kit.yml")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jam/adminui/ -run 'TestDraftAddFile|TestDraftHostilePaths|TestDraftRenameAndDelete|TestDraftWritesRefuseCrossOrigin' -v`
Expected: FAIL (no add form; routes 404/405).

- [ ] **Step 3: Write the routes**

Create `internal/jam/adminui/kit_draft_paths.go`:

```go
package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// registerDraftPaths mounts Add file, Rename and Delete.
func registerDraftPaths(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	// Add: the path from the form (trimmed), an empty text file or the
	// upload. It never overwrites: a file already at the path, at the etag
	// this form was loaded with, is refused; at another etag the put is stale.
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/add", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, true)
		if !ok {
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		p, etag := strings.TrimSpace(r.FormValue("path")), r.FormValue("etag")
		data, _, err := uploadedFile(r)
		if err != nil {
			renderError(w, http.StatusBadRequest, "the upload could not be read")
			return
		}
		_, _, at, err := jam.KitDraftFile(store, name, p)
		switch status := jam.WriteStatus(err, http.StatusInternalServerError); {
		case err == nil && strings.Trim(at, `"`) == strings.Trim(etag, `"`):
			renderError(w, http.StatusConflict, fmt.Sprintf("draft %s already has a file %s", name, pathText(p)))
			return
		case err != nil && status != http.StatusNotFound:
			draftUIFail(w, r, log, err) // an unsafe path: jam's fixed text, unechoed
			return
		}
		s, err := jam.PutKitDraftFile(store, log, name, p, formMode(r), data, etag, jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{File: p}, "added "+pathText(p))
	})

	mux.HandleFunc("POST /app/admin/kits/{name}/draft/rename", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		from, to := r.FormValue("from"), strings.TrimSpace(r.FormValue("to"))
		s, err := jam.RenameKitDraftFile(store, log, name, from, to, r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{File: to}, "renamed "+pathText(from)+" to "+pathText(to))
	})

	mux.HandleFunc("POST /app/admin/kits/{name}/draft/delete", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		p := r.FormValue("path")
		s, err := jam.RemoveKitDraftFile(store, log, name, p, r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{}, "removed "+pathText(p))
	})
}
```

In `adminui.go` `Handler`, after `registerDraftUploads(mux, store, log, guardWrite)` add `registerDraftPaths(mux, store, log, guardWrite)`.

- [ ] **Step 4: Write the templates**

In `kit_draft.html`, replace `{{/* draft-add */}}` with

```html
<section class="card" id="draft-add">
  <details class="edit"><summary>Add file…</summary>
  <form hx-post="/app/admin/kits/{{$name}}/draft/add" hx-encoding="multipart/form-data" hx-target="#draft" hx-swap="outerHTML">
    <input type="hidden" name="etag" value="{{$etag}}">
    <label>Path <span class="hint">slash-separated, inside the kit directory, e.g. <code>scripts/setup.sh</code></span><input name="path" required autocomplete="off" spellcheck="false"></label>
    <label>Content <span class="hint">optional: upload a file (up to 4 MiB; its own name is ignored), or leave empty for an empty text file to edit</span><input type="file" name="file"></label>
    <label class="check"><input type="checkbox" name="executable" value="1"> Executable (0755)</label>
    <div class="form-actions"><button type="submit" class="primary">Add</button></div>
  </form>
  </details>
</section>
```

and replace `  {{/* draft-file-paths */}}` with

```html
  <form class="body" id="draft-rename" hx-post="/app/admin/kits/{{$name}}/draft/rename" hx-target="#draft" hx-swap="outerHTML">
    <input type="hidden" name="etag" value="{{.ETag}}"><input type="hidden" name="from" value="{{.RawPath}}">
    <label>Rename to <input name="to" required value="{{.RawPath}}" autocomplete="off" spellcheck="false"></label>
    <div class="form-actions"><button type="submit" class="small">Rename</button></div>
  </form>
  <form class="body" id="draft-delete" hx-post="/app/admin/kits/{{$name}}/draft/delete" hx-target="#draft" hx-swap="outerHTML" hx-confirm="Remove {{.Path}} from the draft?">
    <input type="hidden" name="etag" value="{{.ETag}}"><input type="hidden" name="path" value="{{.RawPath}}">
    <div class="form-actions"><button type="submit" class="small danger">Delete</button></div>
  </form>
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `gofmt -l internal/jam/adminui && go test ./internal/jam/adminui/ -run 'TestDraft' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/jam/adminui
git commit -m "feat(adminui): Draft tab add, rename and delete" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Diff against the base version

**Files:**
- Create: `internal/jam/adminui/kit_draft_diff.go`
- Modify: `internal/jam/adminui/kit_files_view.go` (`ctxDiff` → `ctxDiffLinked`), `internal/jam/adminui/kit_detail.go` (`yamlDiff`), `internal/jam/adminui/kit_draft_view.go` (fields, `buildDraftPage`, `status`), `internal/jam/adminui/templates/kit_shared.html` (`ctx-diff-rows`, `ctx-file` header), `internal/jam/adminui/templates/kit.html` (use `ctx-diff-rows`), `internal/jam/adminui/templates/kit_draft.html` (`{{/* draft-file-diff */}}`, `{{/* draft-diff */}}`)
- Test: `internal/jam/adminui/kit_draft_diff_test.go`

**Interfaces:**
- Consumes: slice-1 `viewKit(n int, v jam.KitVersion, unpack bool, want string) kitView`, `kitView.ctx *ctxRead`, `kitView.diffText`, `ctxRead{size, files, byPath}`, `ctxEntry`, `fileView(p string, from, to *ctxRead, diff bool) *ctxFileView`, `maskKitYML(text string) (string, maskKind)`, `lineDiff`, `kitDiff`, `maxFileShow`; `jam.KitDraftFile`, `jam.KitDraftStatus`, `jam.DraftChange`.
- Produces:
  - `func ctxDiffLinked(from, to kitView, href func(p string) string) *ctxDiffView` (`ctxDiff` keeps its signature and calls it)
  - `func yamlDiff(from, to string) ([]diffLine, bool)`
  - `func draftRead(st jam.KitDraftStatus, want string, data []byte) *ctxRead`
  - `func draftDiff(store jam.Store, kit string, st jam.KitDraftStatus, base jam.KitVersion, q draftQuery, v *draftView)`
  - fields `draftView.Changes *ctxDiffView`, `YAMLDiff *kitDiff`, `FileDiff *ctxFileView`
  - template `ctx-diff-rows`

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/adminui/kit_draft_diff_test.go`:

```go
package adminui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestDraftDiffVsBase(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "run.sh", 0, "echo two\n")
	putDraft(t, store, "kit.yml", 0, "kind: studio\nprompt: <hi>\n")
	if _, err := jam.RemoveKitDraftFile(store, nil, "app", "gone.txt", draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/app/admin/kits/app/draft").Body.String()
	ch := section(body, "draft-changes")
	for _, want := range []string{
		"<h2>Changes vs v1</h2>",
		`<tr data-change="modified"><td>modified</td><td class="mono"><a href="/app/admin/kits/app/draft?file=run.sh&amp;diff=1">run.sh</a></td><td class="mono">0755</td><td>5 B → 9 B</td></tr>`,
		`<tr data-change="removed"><td>removed</td><td class="mono"><a href="/app/admin/kits/app/draft?file=gone.txt&amp;diff=1">gone.txt</a></td>`,
		"1 unchanged",
	} {
		if !strings.Contains(ch, want) {
			t.Errorf("changes missing %q:\n%s", want, ch)
		}
	}
	if y := section(body, "draft-yaml-diff"); !strings.Contains(y, `<span class="d-add">+ prompt: &lt;hi&gt;</span>`) {
		t.Errorf("kit.yml diff:\n%s", y)
	}
	f := get(t, h, "/app/admin/kits/app/draft?file=run.sh&diff=1").Body.String()
	if cf := section(f, "ctx-file"); !strings.Contains(cf, "v1 → draft") || !strings.Contains(cf, `<span class="d-del">- echo</span>`) || !strings.Contains(cf, `<span class="d-add">+ echo two</span>`) || strings.Contains(f, `name="content"`) {
		t.Errorf("file diff:\n%s", cf)
	}
	if cf := section(get(t, h, "/app/admin/kits/app/draft?file=gone.txt&diff=1").Body.String(), "ctx-file"); !strings.Contains(cf, `<span class="d-del">- bye</span>`) {
		t.Errorf("removed file diff:\n%s", cf)
	}
	rec := get(t, h, "/app/admin/kits/app/draft?diff=1&file="+url.QueryEscape("x\u202ey"))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "\u202e") || !strings.Contains(rec.Body.String(), "No such file in either version") {
		t.Errorf("hostile diff file = %d", rec.Code)
	}
}

// A draft equal to its base lists no change and shows no kit.yml diff.
func TestDraftDiffUnchanged(t *testing.T) {
	_, h := draftFixture(t)
	body := get(t, h, "/app/admin/kits/app/draft").Body.String()
	if ch := section(body, "draft-changes"); !strings.Contains(ch, "No file changed; 4 unchanged") || strings.Contains(body, `id="draft-yaml-diff"`) {
		t.Errorf("unchanged draft:\n%s", ch)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jam/adminui/ -run 'TestDraftDiff' -v`
Expected: FAIL (no `draft-changes` section).

- [ ] **Step 3: Parametrise the slice-1 diff and extract the YAML diff**

In `internal/jam/adminui/kit_files_view.go`, replace the whole `ctxDiff` function with:

```go
// ctxDiff compares from's and to's trees; nil when neither has one. A
// version without one compares as empty. Each row links its version diff.
func ctxDiff(kit string, from, to kitView) *ctxDiffView {
	return ctxDiffLinked(from, to, func(p string) string { return fileHref(kit, to.N, from.N, p) })
}

// ctxDiffLinked is ctxDiff with each row linked by href (the Draft tab links
// its own file diff).
func ctxDiffLinked(from, to kitView, href func(p string) string) *ctxDiffView {
	if from.ctx == nil && to.ctx == nil {
		return nil
	}
	d := &ctxDiffView{From: from.N, To: to.N}
	var paths []string
	for _, v := range []kitView{from, to} {
		if v.ctx != nil {
			for _, f := range v.ctx.files {
				paths = append(paths, f.Path)
			}
		}
	}
	slices.Sort(paths)
	for _, p := range slices.Compact(paths) {
		a, inA := from.ctx.get(p)
		b, inB := to.ctx.get(p)
		row := ctxChange{Path: pathText(p), Href: href(p)}
		switch {
		case !inA:
			row.Change, row.Mode, row.Size = "added", modeText(b), bytesText(b.Size)
		case !inB:
			row.Change, row.Mode, row.Size = "removed", modeText(a), bytesText(a.Size)
		case a.Sum == b.Sum && a.Mode == b.Mode:
			d.Unchanged++
			continue
		default:
			row.Change, row.Mode, row.Size = "modified", modeText(b), bytesText(b.Size)
			if a.Mode != b.Mode {
				row.Mode = modeText(a) + " → " + modeText(b)
				if a.Sum == b.Sum {
					row.Change = "mode"
				}
			}
			if a.Sum != b.Sum {
				row.Size = bytesText(a.Size) + " → " + bytesText(b.Size)
			}
		}
		if len(d.Rows) == maxCtxEntries {
			d.More++
			continue
		}
		d.Rows = append(d.Rows, row)
	}
	return d
}
```

In `internal/jam/adminui/kit_detail.go`, inside `buildKitDetail`, replace

```go
	lines, tooLarge := lineDiff(fv.diffText, d.View.diffText)
	for i := range lines { // a tree's kit.yml is shown as stored: make its control and format characters visible
		lines[i].Text = jam.SetupDisplayText(lines[i].Text)
	}
```

with

```go
	lines, tooLarge := yamlDiff(fv.diffText, d.View.diffText)
```

and append to the file:

```go
// yamlDiff is the line diff of two kit.yml texts as the pages show it: a
// tree's kit.yml is shown as stored, so each line's control and format
// characters are made visible.
func yamlDiff(from, to string) ([]diffLine, bool) {
	lines, tooLarge := lineDiff(from, to)
	for i := range lines {
		lines[i].Text = jam.SetupDisplayText(lines[i].Text)
	}
	return lines, tooLarge
}
```

In `internal/jam/adminui/templates/kit_shared.html`, change the `ctx-file` header's sub from `{{if .Diff}}v{{.From}} → v{{.To}}{{else}}content{{end}}` to `{{if .Diff}}v{{.From}} → {{if .To}}v{{.To}}{{else}}draft{{end}}{{else}}content{{end}}`. Then append:

```html
{{/* ctx-diff-rows is a tree diff's table and counts (ctxDiffView): the kit page's and the Draft tab's. */}}
{{define "ctx-diff-rows"}}{{if .Rows}}<table>
        <thead><tr><th>Change</th><th>Path</th><th>Mode</th><th>Size</th></tr></thead>
        <tbody>{{range .Rows}}<tr data-change="{{.Change}}"><td>{{.Change}}</td><td class="mono"><a href="{{.Href}}">{{.Path}}</a></td><td class="mono">{{.Mode}}</td><td>{{.Size}}</td></tr>{{end}}</tbody>
      </table>{{end}}
      <div class="body unset">{{with .More}}… {{.}} more changed, not listed; {{end}}{{if not .Rows}}No file changed; {{end}}{{.Unchanged}} unchanged</div>{{end}}
```

In `internal/jam/adminui/templates/kit.html`, inside `<section class="card" id="ctx-diff">`, replace everything from `{{if .Rows}}<table>` through the `<div class="body unset">…unchanged</div>` line with `      {{template "ctx-diff-rows" .}}`.

- [ ] **Step 4: Write the draft diff**

Create `internal/jam/adminui/kit_draft_diff.go`:

```go
package adminui

import (
	"io/fs"
	"slices"
	"strconv"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// draftRead is a draft's files in the shape the kit page reads a version's
// (ctxRead), from its status entries. File want carries data's first
// maxFileShow+1 bytes; a kit.yml is masked (maskKitYML), as the kit page
// shows a stored one.
func draftRead(st jam.KitDraftStatus, want string, data []byte) *ctxRead {
	c := &ctxRead{byPath: make(map[string]int, len(st.Entries))}
	var total int64
	for i, e := range st.Entries {
		mode, _ := strconv.ParseUint(e.Mode, 8, 32)
		ent := ctxEntry{Path: e.Path, Mode: fs.FileMode(mode), Size: e.Size, Sum: e.SHA256}
		if e.Path == want && data != nil {
			shown := data
			if e.Path == kittree.KitFile {
				text, how := maskKitYML(string(data))
				shown, ent.Mask = []byte(text), how
			}
			ent.Data = shown[:min(len(shown), maxFileShow+1)]
		}
		total += e.Size
		c.files = append(c.files, ent)
		c.byPath[e.Path] = i
	}
	c.size = sizeLabel(int(total))
	return c
}

// draftDiff fills v's diff against the draft's base version: the changed
// files (each row linking its file diff), kit.yml's line diff when it
// changed, and with q.Diff, file q.File's diff. A file the draft or the base
// lacks compares as absent; one neither has is Missing (never named).
func draftDiff(store jam.Store, kit string, st jam.KitDraftStatus, base jam.KitVersion, q draftQuery, v *draftView) {
	want := ""
	if q.Diff {
		want = q.File
	}
	baseView := viewKit(st.Base, base, true, want)
	var data []byte
	if want != "" {
		data, _, _, _ = jam.KitDraftFile(store, kit, want)
	}
	draft := kitView{ctx: draftRead(st, want, data)}
	v.Changes = ctxDiffLinked(baseView, draft, func(p string) string { return draftFileHref(kit, p, true) })
	if slices.ContainsFunc(st.Changes, func(c jam.DraftChange) bool { return c.Path == kittree.KitFile }) {
		yml, _, _, _ := jam.KitDraftFile(store, kit, kittree.KitFile)
		shown, _ := maskKitYML(string(yml))
		lines, tooLarge := yamlDiff(baseView.diffText, shown)
		v.YAMLDiff = &kitDiff{From: st.Base, Lines: lines, TooLarge: tooLarge}
	}
	if want != "" {
		v.FileDiff = fileView(want, baseView.ctx, draft.ctx, true)
		v.FileDiff.From = st.Base
	}
}
```

In `internal/jam/adminui/kit_draft_view.go`:
- Add to `draftView` (after `FileMissing`):

```go
	Changes  *ctxDiffView // the files against the base
	YAMLDiff *kitDiff     // kit.yml's line diff; nil when kit.yml is unchanged
	FileDiff *ctxFileView // ?file=…&diff=1: that file against the base
```

- In `buildDraftPage`, replace

```go
	return p, true, nil
}

// status is
```

  with

```go
	draftDiff(store, name, st, base, q, p.Draft)
	return p, true, nil
}

// status is
```

- In `status`, change the condition to `if d := p.Draft; d != nil && (d.FileMissing || (d.FileDiff != nil && d.FileDiff.Missing)) {`.

- [ ] **Step 5: Write the templates**

In `kit_draft.html`, replace `{{/* draft-file-diff */}}` with `{{with .FileDiff}}{{template "ctx-file" .}}{{end}}`. Then replace `{{/* draft-diff */}}` with

```html
{{with .Changes}}
<section class="card" id="draft-changes">
  <header><h2>Changes vs v{{.From}}</h2><span class="sub">the draft's files against its base, file by file</span></header>
  {{template "ctx-diff-rows" .}}
</section>
{{end}}
{{with .YAMLDiff}}
<section class="card" id="draft-yaml-diff">
  <header><h2>kit.yml vs v{{.From}}</h2></header>
  {{if .TooLarge}}<div class="body unset">Too large to diff here.</div>{{else}}{{template "diff-lines" .Lines}}{{end}}
</section>
{{end}}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `gofmt -l internal/jam/adminui && go test ./internal/jam/adminui/ -v`
Expected: PASS: the new draft diff tests and every existing kit-page test (`TestKitPageDiffsFiles`, `TestKitVersionDiff`, `TestKitPageKitYMLEscaped`, `TestDiffLinesPartialOnEveryPage`).

- [ ] **Step 7: Commit**

```bash
git add internal/jam/adminui
git commit -m "feat(adminui): Draft tab diff vs base, reusing the kit page's file diff" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Publish, the config-repo panel, and the docs

**Files:**
- Create: `internal/jam/adminui/kit_draft_publish.go`
- Modify: `internal/jam/adminui/kit_draft_view.go` (`draftPage.Published`), `internal/jam/adminui/templates/kit_draft.html` (`{{/* draft-published */}}`, `{{/* draft-publish-form */}}`), `internal/jam/adminui/adminui.go`; docs `docs/usage/jam/ui-pages.md`, `docs/usage/jam/kits-drafts.md`, `docs/usage/jam/INDEX.md`
- Test: `internal/jam/adminui/kit_draft_publish_test.go`; `kit_draft_test.go` (`draftWriteRoutes`)

**Interfaces:**
- Consumes: `jam.PublishKitDraft(store, log, name string, pin bool, ifMatch string) (jam.KitDraftPublish, error)` (`Kit, Version, Tree, Base, Pinned []string ("web v2 (app@v1)"), Drift, Warning`); `store.ListConfigMarks() []jam.ConfigMark`, `jam.ConfigKindKit`, `store.KitTree(sha) (kittree.Tree, bool)`, `kittree.Changes`; Task 2 `draftForm`, `draftUIFail`, `loadDraftPage`, `writeDraftBody`.
- Produces: `type publishView struct{ Version, Base int; Pinned []string; Warning string; Drift bool; ConfigFiles []configFile }`, `type configFile struct{ Path, Change string }`, `func newPublishView(store jam.Store, kit string, prev int, res jam.KitDraftPublish) *publishView`, `func configFiles(store jam.Store, kit string, prev int, res jam.KitDraftPublish) []configFile`, `func registerDraftPublish(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool)`, field `draftPage.Published *publishView`, route `POST …/draft/publish` (`etag`, `pin`).

- [ ] **Step 1: Write the failing tests**

In `kit_draft_test.go`, set

```go
var draftWriteRoutes = []string{"/draft", "/draft/discard", "/draft/save", "/draft/replace", "/draft/mode", "/draft/add", "/draft/rename", "/draft/delete", "/draft/publish"}
```

Create `internal/jam/adminui/kit_draft_publish_test.go`:

```go
package adminui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestDraftPublish(t *testing.T) {
	store, h := draftFixture(t)
	if _, _, err := jam.PushKitFiles(store, "web", kittreetest.Files("kit.yml", "kind: studio\nbase:\n  kit: app\n")); err != nil {
		t.Fatal(err)
	}
	putDraft(t, store, "run.sh", 0, "echo two\n")
	pub := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-publish")
	if !strings.Contains(pub, `id="draft-publish-button">Publish</button>`) || strings.Contains(pub, `id="draft-drift"`) {
		t.Fatalf("publish card:\n%s", pub)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}, "pin": {"1"}})
	body := rec.Body.String()
	done := section(body, "draft-published")
	if rec.Code != http.StatusOK || !strings.Contains(done, "<h2>Published v2</h2>") || !strings.Contains(done, "<p>pinned web v2 (app@v1)</p>") || strings.Contains(done, `id="config-write"`) {
		t.Fatalf("publish = %d:\n%s", rec.Code, done)
	}
	if k, _ := store.GetKit("app"); k.Current != 2 {
		t.Fatalf("current = v%d", k.Current)
	}
	if _, open := store.GetKitDraft("app"); open || !strings.Contains(body, `id="draft-none"`) {
		t.Fatal("publish should close the draft and offer a new one")
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}}); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "kit app has no draft") {
		t.Fatalf("publish again = %d %s", rec.Code, rec.Body)
	}
}

// Review Focus 5: a draft with problems cannot be published from the page,
// and a crafted POST is refused anyway.
func TestDraftPublishBlockedByProblems(t *testing.T) {
	store, h := draftFixture(t)
	if _, err := jam.RemoveKitDraftFile(store, nil, "app", "kit.yml", draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	pub := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-publish")
	if !strings.Contains(pub, `id="draft-publish-button" disabled`) || !strings.Contains(pub, "kit app: kit.yml missing at the root") {
		t.Fatalf("publish card:\n%s", pub)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {draftETag(t, store)}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "draft app has problems: kit app: kit.yml missing at the root") {
		t.Fatalf("crafted publish = %d %s", rec.Code, rec.Body)
	}
	if k, _ := store.GetKit("app"); k.Current != 1 {
		t.Fatal("a draft with problems was published")
	}
	if _, open := store.GetKitDraft("app"); !open {
		t.Fatal("a refused publish closed the draft")
	}
}

// A config-managed kit shows the drift warning before publishing, then the
// files to commit and the config write command: paths only, no content.
func TestDraftPublishManagedOffersConfigWrite(t *testing.T) {
	store, h := draftFixture(t)
	if err := store.ApplyConfig(jam.ConfigWrite{Marks: []jam.ConfigMark{{Kind: jam.ConfigKindKit, Name: "app", Commit: "abc1234", Digest: "d", AppliedAt: time.Unix(1, 0).UTC()}}}); err != nil {
		t.Fatal(err)
	}
	putDraft(t, store, "run.sh", 0, "echo SECRET-CONTENT\n")
	putDraft(t, store, "docs/new.md", 0, "n\n")
	pub := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-publish")
	if !strings.Contains(pub, `id="draft-drift">`+jam.DraftDriftWarning) || !strings.Contains(pub, `hx-confirm="Publish the draft as the next version of app? `+jam.DraftDriftWarning+`"`) {
		t.Fatalf("drift warning:\n%s", pub)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/publish", url.Values{"etag": {hiddenETag(t, pub)}})
	done := section(rec.Body.String(), "draft-published")
	for _, want := range []string{
		`id="config-write"`, "<code>at-jam config write &lt;repo&gt;</code>",
		`<li data-change="added">kits/app/docs/new.md`, `<li data-change="modified">kits/app/run.sh`,
		jam.DraftDriftWarning,
	} {
		if rec.Code != http.StatusOK || !strings.Contains(done, want) {
			t.Errorf("published panel (%d) missing %q:\n%s", rec.Code, want, done)
		}
	}
	if strings.Contains(done, "SECRET-CONTENT") || strings.Contains(done, "kits/app/kit.yml") {
		t.Error("the panel must list changed paths only, never content")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jam/adminui/ -run 'TestDraftPublish|TestDraftWritesRefuseCrossOrigin' -v`
Expected: FAIL (no publish button; `/draft/publish` 404/405).

- [ ] **Step 3: Write the publish route**

Create `internal/jam/adminui/kit_draft_publish.go`:

```go
package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// publishView is a publish's result banner: the new version, the followers
// pinned, the warning(s), and for a drifting publish the files to commit.
type publishView struct {
	Version, Base int
	Pinned        []string
	Warning       string
	Drift         bool
	ConfigFiles   []configFile
}

// configFile is one config-repo path a publish changed, and how.
type configFile struct{ Path, Change string }

func newPublishView(store jam.Store, kit string, prev int, res jam.KitDraftPublish) *publishView {
	v := &publishView{Version: res.Version, Base: res.Base, Pinned: res.Pinned, Warning: res.Warning, Drift: res.Drift}
	if res.Drift {
		v.ConfigFiles = configFiles(store, kit, prev, res)
	}
	return v
}

// configFiles lists the config-repo paths a drifting publish changed, paths
// only: when kit is managed, kits/<kit>/<path> for each file that differs
// from version prev (current before the publish); and kits/<f>/kit.yml for
// each managed follower pinned. `at-jam config write <repo>` renders them.
func configFiles(store jam.Store, kit string, prev int, res jam.KitDraftPublish) []configFile {
	marks := store.ListConfigMarks()
	managed := func(name string) bool {
		return slices.ContainsFunc(marks, func(m jam.ConfigMark) bool { return m.Kind == jam.ConfigKindKit && m.Name == name })
	}
	var out []configFile
	if managed(kit) {
		k, _ := store.GetKit(kit)
		before, _ := store.KitTree(k.Trees[prev])
		after, _ := store.KitTree(res.Tree)
		changes, _ := kittree.Changes(before, after)
		for _, c := range changes {
			out = append(out, configFile{Path: pathText("kits/" + kit + "/" + c.Path), Change: c.Kind})
		}
	}
	for _, pin := range res.Pinned {
		if f, _, _ := strings.Cut(pin, " "); managed(f) {
			out = append(out, configFile{Path: "kits/" + f + "/kit.yml", Change: kittree.ChangeModified})
		}
	}
	return out
}

// registerDraftPublish mounts Publish: jam.PublishKitDraft with the form's
// etag and "pin followers" box. It refuses a draft with problems (400) even
// though the page disables the button.
func registerDraftPublish(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/publish", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		k, _ := store.GetKit(name)
		prev := k.Current
		res, err := jam.PublishKitDraft(store, log, name, r.FormValue("pin") != "", r.FormValue("etag"))
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		log.Info("ui kit draft published", "operator", jam.OperatorID(r), "kit", name, "version", res.Version, "base", res.Base, "tree", shortDigest(res.Tree), "pinned", len(res.Pinned), "drift", res.Drift)
		p, ok := loadDraftPage(w, r, store, log, name, draftQuery{})
		if !ok {
			return
		}
		p.Published = newPublishView(store, name, prev, res)
		writeDraftBody(w, r, p, fmt.Sprintf("published %s v%d", name, res.Version))
	})
}
```

In `kit_draft_view.go`, add `Published *publishView // set right after a publish` to `draftPage` (after `Draft`). In `adminui.go` `Handler`, after `registerDraftPaths(mux, store, log, guardWrite)` add `registerDraftPublish(mux, store, log, guardWrite)`.

- [ ] **Step 4: Write the templates**

In `kit_draft.html`, replace `{{/* draft-publish-form */}}` with

```html
    <form hx-post="/app/admin/kits/{{$name}}/draft/publish" hx-target="#draft" hx-swap="outerHTML" hx-disabled-elt="find button[type=submit]" hx-confirm="Publish the draft as the next version of {{$name}}?{{if .Managed}} {{.DriftWarning}}{{end}}">
      <input type="hidden" name="etag" value="{{$etag}}">
      <label class="check"><input type="checkbox" name="pin" value="1"> Pin followers <span class="hint">kits that follow {{$name}} stay on the version they follow now (v{{.Current}})</span></label>
      <div class="form-actions"><button type="submit" class="primary" id="draft-publish-button"{{if .Problems}} disabled title="Fix the problems first"{{end}}>Publish</button></div>
    </form>
```

and replace `{{/* draft-published */}}` with

```html
{{with .Published}}
<section class="card" id="draft-published">
  <header><h2>Published v{{.Version}}</h2><span class="sub">from the draft (base v{{.Base}})</span><span class="spacer"></span><a class="sub" href="{{kitURL $name}}?v={{.Version}}">view v{{.Version}}</a></header>
  <div class="body">
    {{range .Pinned}}<p>pinned {{.}}</p>{{end}}
    {{with .Warning}}<div class="banner" id="draft-published-warning">{{.}}</div>{{end}}
  </div>
  {{if .Drift}}
  <div class="body" id="config-write">
    <h3>Write to config repo</h3>
    <p>In your config repo checkout run <code>at-jam config write &lt;repo&gt;</code>, check <code>git diff</code>, and commit. Expect these files:</p>
    <ul class="mono">{{range .ConfigFiles}}<li data-change="{{.Change}}">{{.Path}} <span class="unset">{{.Change}}</span></li>{{end}}</ul>
  </div>
  {{end}}
</section>
{{end}}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `gofmt -l internal/jam/adminui && go test ./internal/jam/adminui/ -v`
Expected: PASS.

- [ ] **Step 6: Document the Draft tab**

In `docs/usage/jam/ui-pages.md`, append at the end of the `## Kit pages` section (after the "**Push new version** …" paragraph):

```markdown
### The Draft tab

A kit's page offers **Open draft from v<N>** (the version viewed); the
Draft tab (`/app/admin/kits/<name>/draft`) opens one from any version,
current by default. The page header and the Kits table mark a kit that
has a [draft](kits-drafts.md). The tab shows:

- the base and current versions, with a banner when another version landed
  since (publishing does not merge it), who saved last, and **Discard**;
- the problems that block publishing, and for a config-managed kit the drift
  warning;
- the files as a tree with mode, size and the change against the base
  (added, modified, mode, removed), **Changes** with a diff per file, and
  `kit.yml`'s line diff;
- **Add file**: a path, then an empty text file or an upload (its own name
  is ignored).

Opening a file shows a text editor for valid UTF-8 text of at most 1 MiB
with no carriage returns, and an **Executable** box. Any other file
offers **Download** and an **Executable** toggle. Every file can be
**Replace**d by an upload, renamed or deleted.

Each form carries the draft's ETag ([concurrency](kits-drafts.md#concurrency)).
A save refused as stale keeps your text in the editor, unsaved, with a
reload link. **Publish** ([rules](kits-drafts.md#publish)) is disabled
while there are problems. **Pin followers** pins the following kits. The
result shows the new version, the pinned followers and any warning. For a
config-managed kit it lists the `kits/<name>/…` files changed and the
command that renders them for you to commit: `at-jam config write <repo>`.
```

In its frontmatter:
- In `summary`, change `and a kit's page (/app/admin/kits/<name>)` to `and a kit's page (/app/admin/kits/<name>, with its Draft tab)`.
- In `read_when`, change `a kit's versions, diffs or pinning` to `a kit's versions, diffs, pinning or draft (the Draft tab: edit, upload, rename, publish)`.
- In `owns`, change `kit version rail/diff/push)` to `kit version rail/diff/push, the kit Draft tab)`.
- Keep `updated: 2026-10-25`.

In `docs/usage/jam/kits-drafts.md`, append to the first paragraph under `## The draft` (after "…then publish it as the kit's next version, or discard it."):

```markdown
In the admin UI, the kit page's [Draft tab](ui-pages.md#the-draft-tab)
does the same.
```

In `docs/usage/jam/INDEX.md`:
- In the `kits-drafts.md` row, change `or over the admin API (`/admin/kits/{name}/draft`)` to `, over the admin API (`/admin/kits/{name}/draft`) or the kit page's Draft tab`.
- In the `ui-pages.md` row, change `kit versions/diffs/pinning` to `kit versions/diffs/pinning and its Draft tab`.

- [ ] **Step 7: Run every check**

```bash
cd /home/agent/workspace/cove
export GOPROXY=https://proxy.golang.org,direct
gofmt -l . | grep -v '^vendor/' ; test -z "$(gofmt -l internal cmd)"
just test
just lint; echo "lint exit $?"
go vet -tags integration ./...
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1
```

Expected:
- `just test` passes.
- `lint exit 0`.
- `go vet` is silent.
- The audit's last line shows `743 error(s)` (the baseline 741 plus this plan file's own orphan and frontmatter errors) and no new `ui-pages.md`/`kits-drafts.md`/`INDEX.md` error. `ui-pages.md` stays a WARN for size (ruling 30).

- [ ] **Step 8: Commit**

```bash
git add internal/jam/adminui docs/usage/jam/ui-pages.md docs/usage/jam/kits-drafts.md docs/usage/jam/INDEX.md
git commit -m "feat(adminui): Draft tab publish with the config-repo panel; docs" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-review

**1. Spec coverage**

| Spec / ruling | Task |
|---|---|
| Draft tab on the kit page (§3 UI; rulings 2, 9) | 2 (route, subtabs, open/discard, markers) |
| File tree (§3; ruling 6) | 2 (`draftTree`, markers, removed rows) |
| Text editor for UTF-8 ≤ 1 MiB (§3; rulings 4, 16, 18, 19) | 3 |
| Upload/replace otherwise; download; mode (§3; rulings 4, 20, 22, 23) | 4 |
| Add, rename, delete (§3; rulings 5, 13, 21) | 1 (jam rename), 5 |
| Diff vs the base version (§3; ruling 7) | 6 |
| Publish, drift warning, "write to config repo" (§3 Publish; rulings 8, 26) | 7 |
| Problems panel, button disabled, server refuses (ruling 8) | 2 (panel), 7 (button, refusal) |
| ETag in forms; stale save keeps text (ruling 3, §5 text) | 2 (discard), 3 (save), 4–5, 7 |
| CSRF / originGuard (ruling 2) | 2 (`TestDraftWritesRefuseCrossOrigin`, extended in 3–5 and 7) |
| Escaping, no HTML content, upload names ignored (ruling 10) | 3 (round trip, markup), 4–5 (upload name), 5 (path markup) |
| Docs (ruling 11) | 7 |
| Repo checks (ruling 12) | every task's run step; 7 runs them all |

No gaps are left. Test builds and draft studios are slice 5.

**2. Placeholder scan.** There is no "TBD", "TODO" or "similar to" left. The `{{/* draft-… */}}` template comments are real anchors that later tasks replace. Each replacement gives the exact block.

**3. Type consistency.** These names and signatures were checked across tasks:
- `RenameKitDraftFile(store, log, name, from, to, ifMatch, actor, now)` (1, 5).
- `draftForm(w, r, store, guardWrite, multipart bool) (string, bool)` (2–5, 7).
- `buildDraftPage(store, name, q) (draftPage, bool, error)` (2, 3, 6).
- `loadDraftPage`/`writeDraftBody`/`renderDraftBody` (2, 3–5, 7).
- `draftEditor` fields (3, 4, 5 templates).
- `uploadedFile(r) ([]byte, bool, error)` (4, 5).
- `formMode`, `savedMsg` and `logDraftSave` (3–5).
- `fileModeText` (2–4).
- `ctxDiffLinked(from, to, href)` and `yamlDiff(from, to)` (6).
- `publishView`, `configFiles(store, kit, prev, res)` (7).

They also match slice 3's real code: `KitDraftStatus` fields, `DraftEntry.Mode` as a string, `KitDraftFile` returning `([]byte, kittree.Entry, string, error)`, `PutKitDraftFile`'s `now`, `DiscardKitDraft(store, log, name, ifMatch)`, `PublishKitDraft(store, log, name, pin, ifMatch)`, and `DraftDriftWarning`. Test helpers come from earlier tasks: `draftFixture`, `draftETag`, `putDraft`, `hiddenETag` (2); `editorValue` (3); `postMultipart` (4).

**4. Review Focus.** Each line has its test in its owning task:
1. → Task 3 `TestDraftStaleSaveKeepsText`
2. → Task 3 `TestDraftEditorRoundTripsBytes`
3. → Task 3 `TestDraftEditorNotEditable`
4. → Task 5 `TestDraftHostilePaths`
5. → Task 7 `TestDraftPublishBlockedByProblems`

CSRF is a Global Constraint, with its test in Task 2.
