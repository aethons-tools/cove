# Kit drafts — slice 3 (API, CLI, `kit draft sync`, publish) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each kit can have one **draft**, a mutable tree opened from a version. An operator edits its files in place through the admin API or `at-jam kit draft …`, mirrors a local directory into it (`kit draft sync`), sees its problems, and publishes it as the kit's next version. Every write is guarded by an ETag (the draft tree hash) and never merged silently.

**Architecture:**
- **Pure tree helpers stay in `internal/kittree`.** A relaxed builder (`BuildFiles`/`ValidateFiles`) runs the file rules without the `kit.yml`-at-root rule. `Tree.With` adds or replaces one entry. `IgnoreTree` applies a tree's own `.dockerignore`. `Build` and `Validate` keep their behaviour by calling the relaxed versions and then checking `kit.yml`.
- **The store holds one draft row per kit.** It is `kit_drafts` in Postgres (migration `0020`, `REFERENCES kits ON DELETE CASCADE`) and `memState.kitDrafts` in memory. The draft's tree and blobs live in the existing `kit_trees`/`kit_blobs`. `PutKitDraft(d, objs)` writes both atomically under the store lock, so GC can never land between them. GC counts draft trees as live. Drafts are never exported.
- **One jam layer, under `kitMu`.** `kit_draft.go` holds open, status, discard, the problems and the ETag check. `kit_draft_files.go` holds file get/put/delete and the whole-tree replace. `kit_draft_publish.go` holds publish, `--pin` and drift. Every mutation reads the draft, checks `If-Match`, builds the next tree, writes it and collects garbage while holding `kitMu`. Publish reuses `pushKitTreeLocked` with a new `origin` argument.
- **Routes live in `kit_draft_admin.go`** (`registerKitDrafts`), on the same admin mux and auth. The client is `adminclient/kitdraft.go`. The CLI is `cmd/at-jam/kitdraft.go`, dispatched from `cmdKit` before its flags are parsed.

**Tech Stack:** Go 1.26; `net/http` ServeMux wildcards (`{path...}`); `archive/tar`; the existing `jam` store (MemStore + Postgres via pgx/v5), `storetest`, `kittree`, `kittreetest`, `studio` (`ParseTree`, `SetBaseKit`, `TagSafeName`), `textesc`, `adminclient`, `internal/cli`.

**Spec:** [`docs/superpowers/specs/2026-10-24-kit-trees-design.md`](../specs/2026-10-24-kit-trees-design.md). Slice 3: §3 "The authoring loop" (Draft, API, CLI, Publish; the UI is slice 4, test build and draft studios are slice 5), §5 errors, §6 "Later slices: draft ETag conflicts, validation, publish". Slice 1 (trees) is merged (#479). Its plan, [`2026-10-24-kit-trees-slice1.md`](2026-10-24-kit-trees-slice1.md), is the model for this one.

## Spec deviations, interpretations and rulings

The first ten are the binding design rulings handed to this plan. The rest are rulings this plan makes where the spec and those rulings are silent.

1. **Storage.** At most one draft per kit. `kit_drafts {name PK → kits, base, tree, opened_at, updated_at, actor}`, mirrored by the MemStore map and covered by storetest conformance. Draft trees and blobs live in `kit_trees`/`kit_blobs`, and GC treats them as referenced. Removing a kit deletes its draft. Drafts are working state: they are not in `at-jam export`/`import` snapshots, and not in `config write` or plans.
2. **Validation.** Every draft write enforces `CheckPath`, the modes `0644`/`0755` and the limits (2000 entries, 64 MiB, 4 MiB a file). `kit.yml` may be missing or invalid while editing: the draft tree is built with `kittree.BuildFiles`, not a weakened `Build`. Status lists **problems** and publish refuses while any exist.
3. **Concurrency.** The ETag is the draft tree hash, quoted, in the `ETag` header. Every mutating draft request (PUT file, DELETE file, PUT tar, publish, discard) requires `If-Match`: without it the answer is 428, with a stale one 412 `draft <kit> changed since you loaded it (reload)`. Nothing is merged. Mutations run under `kitMu`.
4. **API.** These routes live on the admin mux, under the same auth:
   - `POST|GET|DELETE /admin/kits/{name}/draft`
   - `GET|PUT|DELETE /admin/kits/{name}/draft/files/{path...}`
   - `PUT /admin/kits/{name}/draft` (tar)
   - `POST /admin/kits/{name}/draft/publish`

   Bodies and statuses are in the tasks.
5. **Publish.** It runs full validation, then pushes the draft tree as the next version through `pushKitTreeLocked`, then closes the draft. A tree equal to current's is refused with `draft <kit> has no changes` and the draft is kept. For a config-managed kit, publish proceeds and answers `drift: true` with `this will drift from the config repo; config write it to keep the repo in sync`.
6. **CLI.** `at-jam kit draft open <kit> [--from vN] | status <kit> [--json] | get <kit> <path> [-o file] | put <kit> <path> <file|-> [--mode 755] | rm <kit> <path> | discard <kit> | publish <kit> [--pin] | sync <kit> <dir>`. A write fetches the status first and sends its ETag. `--etag` passes one explicitly.
7. **Audit.** Writes log `kit draft opened|saved|discarded|published` with the kit, the operator and the version or a 12-hex tree prefix, never content.
8. **Docs.** A new leaf `docs/usage/jam/kits-drafts.md` owns drafts. `kits.md` and `kits-directory.md` link to it.
9. **Unsafe paths are never echoed.** `CheckPath` runs before a path appears in any text. After it passes, CLI output still goes through `textesc.Escape`.
10. **Out of scope:** the Draft UI (slice 4), and test builds and draft studios (slice 5). The API is shaped for them: entries carry `sha256`, a write answers `changed` and `problems`, a file GET carries `X-Kit-File-Mode`, and `GET /admin/kits` marks kits with a draft.
11. **`Meta.Source = draft` becomes `KitVersionMeta.Origin = "draft"`.** `KitVersionMeta.Source` is a `*KitSource` (a git repo/ref/commit), not a label. So a new field `Origin string` (`json:"origin,omitempty"`) is added, with the constant `KitOriginDraft = "draft"`. Every other push leaves it `""`. `GET /admin/kits/{name}/history` gains `origin`, and `kit versions` prints it in the SOURCE column.
12. **`--pin` meaning.** No push pin exists today. `kit pin` rolls `Current` back, and `PinKitChecked` refuses while a kit follows. So `--pin` means: every kit that *follows* this kit (an unpinned `base.kit: <kit>`) stays on the version it follows now.
    - Each such follower first gets a new version whose `base.kit` is rewritten in place to `<kit>@v<current before publish>` (`studio.SetBaseKit`, the slice-1 in-place rewrite; every other byte is kept).
    - All rewrites are prepared before any is pushed. A follower whose `base.kit` cannot be rewritten refuses the publish with nothing written.
    - Without `--pin`, followers go stale as after any push (spec §3).
    - The answer lists `pinned: ["web v5 (app@v3)"]`. A pinned follower that is config-managed also sets `drift`.
13. **Base superseded.** A draft opened from v1 can be published after v2 landed. Its tree becomes v3 as it is (no merge, v2 stays in history). Status shows `base` and `current`, and the CLI prints `draft app: base v1, current v2` plus a line saying nothing is merged.
14. **`.dockerignore` on every draft write**, not only sync. A file PUT to a path the draft's `.dockerignore` excludes is refused with `draft <kit>: <path>: excluded by .dockerignore`. Saving a `.dockerignore` drops the files it now excludes, and they show as `removed` in the write's `changed`. This keeps the invariant "ignored files are never stored". It also means publish never has to filter.
15. **Problems.** The parsers stop at the first error, so a status lists one problem per stage. The texts are:
    - `kit.yml missing at the root` (no `kit.yml`)
    - else the `studio.ParseTree` error: YAML/schema errors, the `base.context…` refusals and note files
    - else the `validateKit` error: name, `Validate`, path parents, chain resolution, prompt and notes budgets

    Publish refuses with 400 `draft <kit> has problems: <p1>; <p2>`.
16. **Status codes and texts.**
    - 400 for an unsafe kit name (`kit name is not tag-safe (allowed: [A-Za-z0-9_.-])`), checked before any text names the kit. Also for an invalid mode (`draft <kit>: mode must be 0644 or 0755`), an unsafe path (`draft <kit>: a file has an unsafe path (…)`) and the file rules (`draft <kit>: <rule text>`).
    - 404 `no such kit`, `kit <kit> has no draft`, `kit <kit> has no version vN`, `draft <kit> has no file <path>`.
    - 409 `kit <kit> already has a draft` and `draft <kit> has no changes`.
    - 413 `draft <kit>: <path>: more than 4 MiB (limit 4 MiB per file)` (file body), and `kit upload over <N> bytes` (tar body, as push).
    - 415 `kit draft: Content-Type must be <type>`.
    - 400 `kit <kit> vN is not stored as a tree` when opening from a broken or pre-tree version.
17. **`If-Match` form.** The value must equal the quoted tree hash; the quotes are optional, so the CLI's `--etag` takes either. `*` and weak tags (`W/"…"`) are not accepted and answer 412.
18. **CSRF.** `POST …/draft` (open) and `POST …/draft/publish` take `application/json` only. A file PUT takes `application/octet-stream`, a tree PUT `application/x-tar`, otherwise 415, checked before any body is read. Every write but open also needs `If-Match`, which a browser no-cors request cannot send. A file and a tree PUT pre-check `If-Match` (404/428/412) before reading the body, and the jam call re-checks it under `kitMu`.
19. **GC after every draft write that changes the tree**, and after discard and publish. Blobs are cached in RAM (slice 1, deviation 4), so a long editing session would otherwise pile up dead blobs. `gcKitObjects` failures are logged and never fail the write.
20. **Reads take `kitMu` too.** Status and file GET read the draft row, its tree and blobs as one consistent snapshot, so a concurrent save plus GC cannot pull the tree out from under them.
21. **`actor`** is who last wrote the draft: the opener, then each saver.
22. **`kit draft sync` needs an open draft.** It does not open one: `kit app has no draft` tells the operator to `kit draft open app` first.
23. **Removing a kit with an open draft is allowed.** The draft goes with it: `RemoveKit` drops it in the cache, and Postgres cascades. A draft is not a removal guard.

## Global Constraints

- **Draft (spec §3, verbatim):** "At most one per kit: a mutable tree opened from `current` or a chosen version. Writes are validated (limits, paths); an invalid state may be saved while editing but blocks publish, with the problems listed. Concurrency uses the draft tree hash as an ETag: a save against a stale ETag is refused, never merged silently."
- **Publish (spec §3, verbatim):** "`kit draft publish [--pin]` makes the draft tree the next version (`Meta.Source = draft`) and closes the draft; followers go stale as usual. For a config-managed kit, publish carries the drift warning". The field is `Meta.Origin` (deviation 11).
- **Tree rules (spec §1, verbatim):** "`path`: clean, slash-separated, relative, valid UTF-8, no control or format characters, no `.`/`..` segments … `mode`: `0644` or `0755` only … Limits: ≤ 2000 entries, ≤ 64 MiB total, ≤ 4 MiB per file." A draft is exempt only from "`kit.yml` must exist at the root".
- **`.dockerignore` (spec §1, verbatim):** "Applied on the way in (push, git import, config apply, draft sync): ignored files are never stored. `kit.yml`, `Dockerfile` and `.dockerignore` are always kept."
- **Error texts (spec §5, exact):** `draft app changed since you loaded it (reload)`. Errors are fixed text, located by path, and never echo file contents or an unsafe name.
- **Fixed texts this plan adds:**
  - `kit app already has a draft`
  - `kit app has no draft`
  - `draft app has no changes`
  - `draft app has problems: …`
  - `draft app has no file <path>`
  - `draft app: If-Match is required (send the etag from the draft status)`
  - `draft app: <path>: excluded by .dockerignore`
  - `draft app: mode must be 0644 or 0755`
  - `kit app vN is not stored as a tree`
  - `kit draft: Content-Type must be application/json|application/octet-stream|application/x-tar`
  - `this will drift from the config repo; config write it to keep the repo in sync`
- **Status codes:** missing `If-Match` → 428; stale → 412; wrong Content-Type → 415; a file body over 4 MiB → 413; open with a draft present / publish with no changes → 409; problems → 400.
- **Lock order:** `configMu → kitMu → store mu` (`config_apply.go`). Every draft function takes `kitMu` once and never nests it. Nothing holds `kitMu` across network I/O.
- **Storage:** Postgres migration `internal/jam/migrations/0020_kit_drafts.sql` (the next number after `0019_kit_trees.sql`). MemStore and Postgres stay in parity through `storetest`. `TruncateAllForTest` truncates `kit_drafts`.
- **Not in config:** `ExportConfig`/`configSnapshot` never carry draft trees or blobs (they walk kit versions only; a conformance test pins this). `ImportConfig` resets the draft map.
- **Audit:** `log.Info("kit draft opened|saved|discarded|published", "operator", …, "kit", …, "tree"|"version", …)`. Never file content, never an unsafe path.
- Tests are hermetic: MemStore, `httptest`, `t.TempDir`. Postgres tests stay behind the `integration` tag and run in CI only.
- Use `GOPROXY=https://proxy.golang.org,direct` for every `go` command here.
- Every commit passes `just test`, `just lint` (check the exit code: `just lint; echo "lint exit=$?"`) and `go vet -tags integration ./...`. Run `gofmt -l` on the touched files: it must print nothing.
- In doc-edit steps, the form <link `target` as "text"> stands for the markdown link `[text](target)` to write. The plan spells it that way so its own link check stays clean.
- **Docs** change in the same task as the behaviour they describe. Edit the doc that owns the subject; no copies. `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1` must report no more than **741** errors: 739 before this plan, plus 2 for this plan file (no frontmatter and not in `INDEX.md`, by convention for plans). The new leaf adds none.

## Review Focus

1. **Two tabs edit one draft.** Both load ETag E. Tab A saves (E → E2). Tab B saves, removes or discards with E and gets 412 `draft app changed since you loaded it (reload)`; tab A's content stands. Without `If-Match` the answer is 428. After reloading, tab B saves fine. A stale tree replace or publish is refused the same way. *(Task 4: `TestDraftTwoTabsStaleETag`; Task 5: `TestReplaceKitDraft`; Task 6: `TestPublishRefusals`; Task 7: `TestKitDraftClient`; Task 8: `TestKitDraftVerbs` with `--etag`.)*
2. **The base version is superseded while the draft is open.** v2 is pushed after a draft was opened from v1. Status shows `base 1, current 2`, and the CLI prints `draft app: base v1, current v2` and that nothing is merged. Publish still works: v3 is exactly the draft tree (v2's file is not in it, v2 is kept). *(Task 6: `TestPublishAfterBaseSuperseded`; Task 8: `TestKitDraftStatusBaseSuperseded`.)*
3. **The kit is removed while a draft is open.** `kit rm` succeeds, because a draft is no guard. The draft and its tree are gone after GC, and a draft call answers 404 `no such kit`. Re-creating the kit shows no stale draft. A config apply that removes the kit drops the draft too. *(Task 2: conformance "removing the kit removes its draft" and "a config apply removing the kit…"; Task 3: `TestRemoveKitDropsDraft`.)*
4. **A path with unicode or control characters.** `docs/ünïcødé 名前.md` (percent-encoded on the wire) is accepted and round-trips. Each of the following is refused with the fixed unsafe-path text, the name never echoed, through the function, the file route and a hand-made tar:
   - a newline, an ANSI escape or U+202E
   - `../x` or `a/./b`
   - invalid UTF-8, or an absolute path

   *(Task 4: `TestDraftFileRefusals`, `TestDraftFileRoutes`; Task 5: `TestReplaceDraftRoute`.)*
5. **GC races a draft write.** Fifty saves run while another goroutine calls `GCKitObjects` in a loop (under `-race` in CI). Every entry of the final draft tree still has its blob. A replaced draft tree's unique blobs are collected; the current draft's are not. *(Task 2: conformance "GC keeps a draft's objects…"; Task 4: `TestDraftWritesSurviveConcurrentGC`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/kittree/tree.go` (modify) | `BuildFiles`, `ValidateFiles`, `With`, `FileTooLarge` (exported); `Build`/`Validate` become relaxed builder + `kit.yml` check |
| `internal/kittree/ingest.go` (modify) | `IgnoreTree`; callers of the renamed `FileTooLarge` |
| `internal/jam/kit_drafts.go` (create) | `KitDraft`; `memState` reads (`GetKitDraft`, `ListKitDrafts`), `checkKitDraft`, `applyPutKitDraft`, `applyRemoveKitDraft` |
| `internal/jam/kit_objects.go` (modify) | `checkObjects` (shared by version and draft writes); GC counts draft trees as live |
| `internal/jam/memstate.go`, `memstore.go`, `store.go`, `config_snapshot.go` (modify) | the `kitDrafts` map, MemStore writes, the interface, the import reset |
| `internal/jam/pgstore_kit_drafts.go` (create), `pgstore.go`, `pgstore_testhelpers.go` (modify), `migrations/0020_kit_drafts.sql` (create) | Postgres |
| `internal/jam/storetest/kits_draft.go` (create), `conformance.go` (modify) | conformance |
| `internal/jam/kit_draft.go` (create) | status/save/publish wire types, `DraftETag`, `draftOf`, `checkDraftETag`, `draftProblems`, `OpenKitDraft`, `KitDraftStatusOf`, `DiscardKitDraft`, `kitManaged` |
| `internal/jam/kit_draft_files.go` (create) | `KitDraftFile`, `PutKitDraftFile`, `RemoveKitDraftFile`, `ReplaceKitDraft`, `saveDraftLocked` |
| `internal/jam/kit_draft_publish.go` (create) | `PublishKitDraft`, `pinFollowersLocked`, `KitOriginDraft` |
| `internal/jam/kit_draft_admin.go` (create) | `registerKitDrafts`: every draft route |
| `internal/jam/kit_push.go`, `identity.go`, `kitsource_admin.go`, `admin.go` (modify) | `origin` on `pushKitTreeLocked`, `replaceKitYMLLocked`, `KitVersionMeta.Origin`, `KitVersionInfo.Origin`, `KitSummary.Draft`, route registration |
| `internal/jam/adminclient/adminclient.go` (modify), `adminclient/kitdraft.go` (create) | `sendHeaders`, raw bodies; the draft client |
| `cmd/at-jam/kitdraft.go` (create), `cmd/at-jam/main.go` (modify) | `at-jam kit draft …`; `kit versions` origin |
| `docs/usage/jam/kits-drafts.md` (create), `docs/usage/jam/INDEX.md`, `kits.md`, `kits-directory.md` (modify) | docs |

---

### Task 1: A relaxed tree builder (`internal/kittree`)

**Files:**
- Modify: `internal/kittree/tree.go` (`Build`, `Validate`, `fileTooLarge` → `FileTooLarge`; add `BuildFiles`, `ValidateFiles`, `With`, `requireKitFile`), `internal/kittree/ingest.go` (add `IgnoreTree`; rename the `fileTooLarge` call sites)
- Test: `internal/kittree/tree_test.go`, `internal/kittree/ingest_test.go`

**Interfaces:**
- Consumes: the existing `Tree`, `Entry`, `File`, `Blobs`, `MapBlobs`, `NewIgnorer`, `Ignorer.Ignored`, the test helpers `withLimits` and `hexSum` (`tree_test.go`).
- Produces:
```go
package kittree
func BuildFiles(files []File) (Tree, map[string][]byte, error) // Build minus the kit.yml rule
func (t Tree) ValidateFiles() error                              // Validate minus the kit.yml rule
func (t Tree) With(e Entry) Tree                                 // e added or replacing e.Path; sorted; t unchanged
func IgnoreTree(t Tree, blobs Blobs) (Tree, error)               // t less what its own .dockerignore excludes
func FileTooLarge(p string) error                                // "<p>: more than 4 MiB (limit 4 MiB per file)"
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/kittree/tree_test.go`:

```go
func TestBuildFilesNeedsNoKitYML(t *testing.T) {
	tr, blobs, err := BuildFiles([]File{{Path: "run.sh", Mode: 0o700, Data: []byte("echo\n")}})
	if err != nil || len(tr.Entries) != 1 || tr.Entries[0].Mode != 0o755 || len(blobs) != 1 {
		t.Fatalf("BuildFiles = %+v, %d blobs, %v", tr, len(blobs), err)
	}
	if err := tr.ValidateFiles(); err != nil {
		t.Fatalf("ValidateFiles = %v", err)
	}
	if err := tr.Validate(); err == nil || err.Error() != "kit.yml missing at the root" {
		t.Fatalf("Validate = %v, want the kit.yml refusal", err)
	}
	if _, _, err := Build([]File{{Path: "run.sh", Data: []byte("x")}}); err == nil || err.Error() != "kit.yml missing at the root" {
		t.Fatalf("Build = %v, want the kit.yml refusal", err)
	}
	empty, _, err := BuildFiles(nil)
	if err != nil || len(empty.Entries) != 0 || empty.ValidateFiles() != nil {
		t.Fatalf("an empty draft tree = %+v %v", empty, err)
	}
}

func TestBuildFilesKeepsTheFileRules(t *testing.T) {
	withLimits(t, 3, 6, 4)
	for name, tc := range map[string]struct {
		files []File
		want  string
	}{
		"unsafe path":      {[]File{{Path: "a\nb"}}, errUnsafePath.Error()},
		"too many":         {[]File{{Path: "a"}, {Path: "b"}, {Path: "c"}, {Path: "d"}}, "4 files (limit 3)"},
		"file too large":   {[]File{{Path: "a", Data: []byte("12345")}}, "a: more than 4 bytes (limit 4 bytes per file)"},
		"total too large":  {[]File{{Path: "a", Data: []byte("1234")}, {Path: "b", Data: []byte("1234")}}, "more than 6 bytes in total (limit 6 bytes)"},
		"twice":            {[]File{{Path: "a"}, {Path: "a"}}, "a: appears twice"},
		"file and dir":     {[]File{{Path: "a"}, {Path: "a/b"}}, "a: is both a file and a directory"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := BuildFiles(tc.files); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTreeWith(t *testing.T) {
	tr, _, err := BuildFiles([]File{{Path: "b", Data: []byte("1")}, {Path: "d", Data: []byte("2")}})
	if err != nil {
		t.Fatal(err)
	}
	got := tr.With(Entry{Path: "c", Mode: 0o644, SHA256: hexSum([]byte("3")), Size: 1})
	if paths := fmt.Sprint(entryPaths(got)); paths != "[b c d]" || len(tr.Entries) != 2 {
		t.Fatalf("With added = %s (original %d entries)", paths, len(tr.Entries))
	}
	repl := got.With(Entry{Path: "b", Mode: 0o755, SHA256: hexSum([]byte("x")), Size: 1})
	if e, _ := repl.Lookup("b"); e.Mode != 0o755 || len(repl.Entries) != 3 {
		t.Fatalf("With replaced = %+v", repl)
	}
	if e, _ := got.Lookup("b"); e.Mode != 0o644 {
		t.Fatal("With mutated its receiver")
	}
}

func entryPaths(t Tree) []string {
	var out []string
	for _, e := range t.Entries {
		out = append(out, e.Path)
	}
	return out
}
```

Append to `internal/kittree/ingest_test.go`:

```go
func TestIgnoreTree(t *testing.T) {
	tr, blobs, err := BuildFiles([]File{
		{Path: ".dockerignore", Data: []byte("*.log\nDockerfile\n")},
		{Path: "debug.log", Data: []byte("x")},
		{Path: "run.sh", Data: []byte("y")},
		{Path: "Dockerfile", Data: []byte("FROM x\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := IgnoreTree(tr, MapBlobs(blobs))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range got.Entries {
		paths = append(paths, e.Path)
	}
	if fmt.Sprint(paths) != "[.dockerignore Dockerfile run.sh]" {
		t.Fatalf("kept %v (Dockerfile and .dockerignore are always kept)", paths)
	}
	plain, _, _ := BuildFiles([]File{{Path: "a.log", Data: []byte("x")}})
	if kept, err := IgnoreTree(plain, MapBlobs(nil)); err != nil || len(kept.Entries) != 1 {
		t.Fatalf("no .dockerignore = %+v %v", kept, err)
	}
	if _, err := IgnoreTree(tr, MapBlobs(nil)); err == nil {
		t.Fatal("a .dockerignore whose content is missing must refuse")
	}
}
```

If `ingest_test.go` does not import `fmt` yet, add it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/kittree/ -run 'BuildFiles|TreeWith|IgnoreTree'`
Expected: FAIL to compile, `undefined: BuildFiles`.

- [ ] **Step 3: Implement**

In `internal/kittree/tree.go`:

1. Rename `fileTooLarge` to `FileTooLarge`, with the doc comment `// FileTooLarge is the refusal of file p over MaxFile.` Update its call sites: two in `tree.go`, and `ingest.go` lines ~376 and ~452 (`grep -n fileTooLarge internal/kittree/*.go` must print nothing afterwards).
2. Replace `Build` with `BuildFiles` plus a thin `Build`:

```go
// BuildFiles validates files as a tree under the file rules alone (safe
// paths, the two modes, the limits, no duplicate, no file where a directory
// is), normalizing modes, and returns it with its blobs by sha256. It does
// not require kit.yml: a kit draft may lack it while being edited.
func BuildFiles(files []File) (Tree, map[string][]byte, error) {
	if len(files) > MaxEntries {
		return Tree{}, nil, tooManyFiles(len(files))
	}
	t := Tree{Entries: make([]Entry, 0, len(files))}
	blobs := make(map[string][]byte, len(files))
	var total int64
	for _, f := range files {
		if err := CheckPath(f.Path); err != nil {
			return Tree{}, nil, err
		}
		n := int64(len(f.Data))
		if n > MaxFile {
			return Tree{}, nil, FileTooLarge(f.Path)
		}
		if total += n; total > MaxTotal {
			return Tree{}, nil, treeTooLarge()
		}
		sum := sha256.Sum256(f.Data)
		sha := hex.EncodeToString(sum[:])
		blobs[sha] = f.Data
		t.Entries = append(t.Entries, Entry{Path: f.Path, Mode: NormMode(f.Mode), SHA256: sha, Size: n})
	}
	slices.SortFunc(t.Entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	for i := 1; i < len(t.Entries); i++ {
		if t.Entries[i].Path == t.Entries[i-1].Path {
			return Tree{}, nil, fmt.Errorf("%s: appears twice", t.Entries[i].Path)
		}
	}
	if err := t.ValidateFiles(); err != nil {
		return Tree{}, nil, err
	}
	return t, blobs, nil
}

// Build validates files as one kit version's tree: BuildFiles, then kit.yml
// at the root. Identical content shares one blob.
func Build(files []File) (Tree, map[string][]byte, error) {
	t, blobs, err := BuildFiles(files)
	if err != nil {
		return Tree{}, nil, err
	}
	if err := t.requireKitFile(); err != nil {
		return Tree{}, nil, err
	}
	return t, blobs, nil
}

// With is t with e added, or replacing the entry at e.Path, kept in path
// order. t itself is not changed; the result is not validated.
func (t Tree) With(e Entry) Tree {
	out := slices.Clone(t.Entries)
	i, found := slices.BinarySearchFunc(out, e.Path, func(x Entry, p string) int { return strings.Compare(x.Path, p) })
	if found {
		out[i] = e
	} else {
		out = slices.Insert(out, i, e)
	}
	return Tree{Entries: out}
}

func (t Tree) requireKitFile() error {
	if _, ok := t.Lookup(KitFile); !ok {
		return errors.New("kit.yml missing at the root")
	}
	return nil
}
```

3. Split `Validate`. Rename its body to `ValidateFiles` without the final `kit.yml` lookup, and make `Validate` the composition:

```go
// Validate checks a tree read back from storage or a backup as a kit
// version: ValidateFiles, then kit.yml at the root.
func (t Tree) Validate() error {
	if err := t.ValidateFiles(); err != nil {
		return err
	}
	return t.requireKitFile()
}

// ValidateFiles checks a tree under the file rules alone: sorted unique safe
// paths, the two modes, lowercase hex sums, no file where another file's
// directory is, and the limits. A kit draft's tree must pass it.
func (t Tree) ValidateFiles() error {
	if len(t.Entries) > MaxEntries {
		return tooManyFiles(len(t.Entries))
	}
	var total int64
	for i, e := range t.Entries {
		if err := CheckPath(e.Path); err != nil {
			return err
		}
		if i > 0 && t.Entries[i-1].Path >= e.Path {
			return fmt.Errorf("%s: entries out of order or repeated", e.Path)
		}
		if e.Mode != 0o644 && e.Mode != 0o755 {
			return fmt.Errorf("%s: mode %o (want 644 or 755)", e.Path, e.Mode)
		}
		if !isSHA256Hex(e.SHA256) {
			return fmt.Errorf("%s: not a sha256", e.Path)
		}
		if e.Size < 0 || e.Size > MaxFile {
			return FileTooLarge(e.Path)
		}
		if total += e.Size; total > MaxTotal {
			return treeTooLarge()
		}
		for d := path.Dir(e.Path); d != "."; d = path.Dir(d) {
			if _, clash := t.Lookup(d); clash {
				return fmt.Errorf("%s: is both a file and a directory", d)
			}
		}
	}
	return nil
}
```

In `internal/kittree/ingest.go`, after `Ignore`:

```go
// IgnoreTree is t less the entries its own root .dockerignore excludes
// (kit.yml, Dockerfile and .dockerignore never are), the rules read through
// blobs: Ignore for a tree whose content is stored. A .dockerignore whose
// blob is missing refuses.
func IgnoreTree(t Tree, blobs Blobs) (Tree, error) {
	var rules []byte
	if e, ok := t.Lookup(dockerignoreFile); ok {
		b, ok := blobs(e.SHA256)
		if !ok {
			return Tree{}, errors.New(".dockerignore: has no stored content")
		}
		rules = b
	}
	ig, err := NewIgnorer(rules)
	if err != nil {
		return Tree{}, err
	}
	out := Tree{Entries: make([]Entry, 0, len(t.Entries))}
	for _, e := range t.Entries {
		ignored, err := ig.Ignored(e.Path)
		if err != nil {
			return Tree{}, err
		}
		if !ignored {
			out.Entries = append(out.Entries, e)
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/kittree/... ./internal/studio/... ./internal/jam/...`
Expected: PASS. `Build` and `Validate` behave exactly as before, and the slice-1 tests still pass.

- [ ] **Step 5: Lint and commit**

```bash
gofmt -l internal/kittree/
just lint; echo "lint exit=$?"
git add internal/kittree/
git commit -m "feat(kittree): relaxed tree builder for drafts (BuildFiles, ValidateFiles, With, IgnoreTree)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Drafts in the store (MemStore, Postgres, conformance)

**Files:**
- Create: `internal/jam/kit_drafts.go`, `internal/jam/pgstore_kit_drafts.go`, `internal/jam/migrations/0020_kit_drafts.sql`, `internal/jam/storetest/kits_draft.go`
- Modify: `internal/jam/kit_objects.go` (`checkObjects`; draft roots in `deadKitObjects`), `internal/jam/memstate.go` (field, init, `applyRemoveKit`), `internal/jam/memstore.go` (`PutKitDraft`, `RemoveKitDraft`), `internal/jam/store.go` (interface), `internal/jam/config_snapshot.go` (`applyImport` resets drafts), `internal/jam/pgstore.go` (`load` calls `loadKitDrafts`), `internal/jam/pgstore_testhelpers.go` (truncate and reset), `internal/jam/storetest/conformance.go` (run the new suite)
- Test: `internal/jam/storetest/kits_draft.go` (run by `TestMemStoreConformance` and, in CI, `TestPostgresStoreConformance`), `internal/jam/pgstore_integration_test.go` (`TestPostgresKitDraftSurvivesReopen`)

**Interfaces:**
- Consumes: `kittree.BuildFiles`, `kittree.Tree.ValidateFiles` (Task 1); `KitObjects`, `checkKitObjects`, `insertKitObjectsTx`, `registryTx`, `exec`, `kittreetest.Files`, storetest's `treeObjs`.
- Produces:
```go
// package jam
type KitDraft struct {
	Kit       string    `json:"kit"`
	Base      int       `json:"base"`
	Tree      string    `json:"tree"`
	OpenedAt  time.Time `json:"opened_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Actor     string    `json:"actor"`
}
// Store gains:
GetKitDraft(kit string) (KitDraft, bool)
ListKitDrafts() []KitDraft            // sorted by kit
PutKitDraft(d KitDraft, objs KitObjects) error
RemoveKitDraft(kit string) error      // wraps ErrNotFound without a draft
func checkObjects(m *memState, objs KitObjects, validate func(kittree.Tree) error) (map[string]bool, error)
```

- [ ] **Step 1: Write the failing conformance suite**

```go
// internal/jam/storetest/kits_draft.go
package storetest

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// draftObjs builds a draft tree (kit.yml not required) from path → content
// pairs and the objects that carry it.
func draftObjs(t *testing.T, kv ...string) (kittree.Tree, jam.KitObjects) {
	t.Helper()
	tr, blobs, err := kittree.BuildFiles(kittreetest.Files(kv...))
	if err != nil {
		t.Fatal(err)
	}
	return tr, jam.KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}
}

// sameDraft compares drafts with time.Equal: Postgres reads times back in UTC.
func sameDraft(a, b jam.KitDraft) bool {
	return a.Kit == b.Kit && a.Base == b.Base && a.Tree == b.Tree && a.Actor == b.Actor &&
		a.OpenedAt.Equal(b.OpenedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

// runKitDraftConformance exercises kit drafts: the one row per kit, its
// objects, GC roots, removal with the kit, and staying out of exports.
func runKitDraftConformance(t *testing.T, mk func(t *testing.T) jam.Store) {
	meta := jam.KitVersionMeta{PushedAt: time.Unix(10, 0).UTC()}
	opened, saved := time.Unix(20, 0).UTC(), time.Unix(30, 0).UTC()
	// withKit is a fresh store holding kit app v1 (kit.yml only).
	withKit := func(t *testing.T) jam.Store {
		t.Helper()
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n")
		if _, err := st.PushKitVersion("app", tr.Hash(), objs, meta, nil); err != nil {
			t.Fatal(err)
		}
		return st
	}
	draftOn := func(tree string) jam.KitDraft {
		return jam.KitDraft{Kit: "app", Base: 1, Tree: tree, OpenedAt: opened, UpdatedAt: saved, Actor: "local"}
	}
	t.Run("put a draft without kit.yml and read it back", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "run.sh*", "echo\n")
		d := draftOn(tr.Hash())
		if err := st.PutKitDraft(d, objs); err != nil {
			t.Fatal(err)
		}
		got, ok := st.GetKitDraft("app")
		if !ok || !sameDraft(got, d) {
			t.Fatalf("draft = %+v %v, want %+v", got, ok, d)
		}
		if list := st.ListKitDrafts(); len(list) != 1 || !sameDraft(list[0], d) {
			t.Fatalf("list = %+v", list)
		}
		if back, ok := st.KitTree(tr.Hash()); !ok || back.Hash() != tr.Hash() {
			t.Fatalf("tree = %+v %v", back, ok)
		}
		if _, ok := st.GetKitDraft("other"); ok {
			t.Fatal("a draft for a kit that has none")
		}
	})
	t.Run("refuses a draft that does not hold together and writes nothing", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "a.txt", "x")
		bad := kittree.Tree{Entries: slices.Clone(tr.Entries)}
		bad.Entries[0].Mode = fs.FileMode(0o600)
		for name, tc := range map[string]struct {
			d    jam.KitDraft
			objs jam.KitObjects
		}{
			"unknown kit":   {jam.KitDraft{Kit: "nope", Base: 1, Tree: tr.Hash()}, objs},
			"unknown base":  {jam.KitDraft{Kit: "app", Base: 9, Tree: tr.Hash()}, objs},
			"unstored tree": {draftOn(tr.Hash()), jam.KitObjects{}},
			"bad mode":      {draftOn(bad.Hash()), jam.KitObjects{Trees: []kittree.Tree{bad}, Blobs: objs.Blobs}},
		} {
			err := st.PutKitDraft(tc.d, tc.objs)
			if err == nil {
				t.Fatalf("%s: accepted", name)
			}
			if name == "unknown kit" && !errors.Is(err, jam.ErrNotFound) {
				t.Fatalf("unknown kit: %v, want ErrNotFound", err)
			}
		}
		if _, ok := st.GetKitDraft("app"); ok {
			t.Fatal("a refused draft was written")
		}
		if _, ok := st.KitTree(tr.Hash()); ok {
			t.Fatal("a refused draft's tree was stored")
		}
	})
	t.Run("an empty draft tree round-trips", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t)
		if err := st.PutKitDraft(draftOn(tr.Hash()), objs); err != nil {
			t.Fatal(err)
		}
		if back, ok := st.KitTree(tr.Hash()); !ok || len(back.Entries) != 0 {
			t.Fatalf("empty tree = %+v %v", back, ok)
		}
	})
	t.Run("GC keeps a draft's objects and drops a replaced draft's", func(t *testing.T) {
		st := withKit(t)
		t1, o1 := draftObjs(t, "kit.yml", "kind: studio\n", "a.txt", "one")
		t2, o2 := draftObjs(t, "kit.yml", "kind: studio\n", "a.txt", "two")
		if err := st.PutKitDraft(draftOn(t1.Hash()), o1); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{}) {
			t.Fatalf("gc = %+v %v (the draft references its tree)", gc, err)
		}
		if err := st.PutKitDraft(draftOn(t2.Hash()), o2); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{Trees: 1, Blobs: 1}) {
			t.Fatalf("gc = %+v %v (want the replaced draft tree and its a.txt)", gc, err)
		}
		if _, ok := st.KitTree(t1.Hash()); ok {
			t.Fatal("the replaced draft tree survived GC")
		}
		if err := st.RemoveKitDraft("app"); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{Trees: 1, Blobs: 1}) {
			t.Fatalf("gc after discard = %+v %v", gc, err)
		}
		if k, _ := st.GetKit("app"); k.Current != 1 {
			t.Fatal("discarding the draft touched the kit")
		}
		if err := st.RemoveKitDraft("app"); !errors.Is(err, jam.ErrNotFound) {
			t.Fatalf("second remove = %v, want ErrNotFound", err)
		}
	})
	t.Run("removing the kit removes its draft", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "draft-only.txt", "x")
		if err := st.PutKitDraft(draftOn(tr.Hash()), objs); err != nil {
			t.Fatal(err)
		}
		if err := st.RemoveKit("app"); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.GetKitDraft("app"); ok {
			t.Fatal("the draft outlived its kit")
		}
		if _, err := st.GCKitObjects(); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.KitTree(tr.Hash()); ok {
			t.Fatal("the removed draft's tree survived GC")
		}
	})
	t.Run("a config apply removing the kit removes its draft", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "draft-only.txt", "x")
		if err := st.PutKitDraft(draftOn(tr.Hash()), objs); err != nil {
			t.Fatal(err)
		}
		if err := st.ApplyConfig(jam.ConfigWrite{RemoveKits: []string{"app"}}); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.GetKitDraft("app"); ok {
			t.Fatal("the draft outlived its kit")
		}
	})
	t.Run("drafts are not exported", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "draft-only.txt", "x")
		if err := st.PutKitDraft(draftOn(tr.Hash()), objs); err != nil {
			t.Fatal(err)
		}
		snap := st.ExportConfig()
		for _, x := range snap.KitTrees {
			if x.Hash() == tr.Hash() {
				t.Fatal("a draft tree was exported")
			}
		}
		e, _ := tr.Lookup("draft-only.txt")
		if _, ok := snap.KitBlobs[e.SHA256]; ok {
			t.Fatal("a draft blob was exported")
		}
		dst := mk(t)
		if err := dst.ImportConfig(snap); err != nil {
			t.Fatal(err)
		}
		if len(dst.ListKitDrafts()) != 0 {
			t.Fatal("an import made a draft")
		}
	})
}
```

In `internal/jam/storetest/conformance.go`, after `runKitTreeConformance(t, newStore)`, add `runKitDraftConformance(t, newStore)`.

Append to `internal/jam/pgstore_integration_test.go`:

```go
// TestPostgresKitDraftSurvivesReopen: a draft row and its loose tree load
// back on open.
func TestPostgresKitDraftSurvivesReopen(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	ctx := context.Background()
	s, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.TruncateAllForTest(ctx); err != nil {
		t.Fatal(err)
	}
	tr, blobs := kittreetest.Tree(t, "kit.yml", "kind: studio\n")
	if _, err := s.PushKitVersion("app", tr.Hash(), jam.KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}, jam.KitVersionMeta{PushedAt: time.Unix(10, 0).UTC()}, nil); err != nil {
		t.Fatal(err)
	}
	dt, dblobs, err := kittree.BuildFiles(kittreetest.Files("notes.md", "draft\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := jam.KitDraft{Kit: "app", Base: 1, Tree: dt.Hash(), OpenedAt: time.Unix(20, 0).UTC(), UpdatedAt: time.Unix(30, 0).UTC(), Actor: "local"}
	if err := s.PutKitDraft(d, jam.KitObjects{Trees: []kittree.Tree{dt}, Blobs: dblobs}); err != nil {
		t.Fatal(err)
	}
	again, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	got, ok := again.GetKitDraft("app")
	if !ok || got.Tree != d.Tree || got.Base != 1 || got.Actor != "local" || !got.OpenedAt.Equal(d.OpenedAt) || !got.UpdatedAt.Equal(d.UpdatedAt) {
		t.Fatalf("reloaded draft = %+v %v", got, ok)
	}
	if _, ok := again.KitTree(dt.Hash()); !ok {
		t.Fatal("the draft tree was not loaded")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run TestMemStoreConformance`
Expected: FAIL to compile, `st.PutKitDraft undefined (type jam.Store has no field or method PutKitDraft)`.

- [ ] **Step 3: Implement the memory side**

```go
// internal/jam/kit_drafts.go
package jam

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
)

// KitDraft is a kit's one open draft (docs/usage/jam/kits-drafts.md): a
// mutable tree opened from version Base. Its tree and blobs live with the
// versions' (kit_trees, kit_blobs) and count as referenced for GC. A draft
// is working state: never in a config snapshot, an export or a config plan.
type KitDraft struct {
	Kit       string    `json:"kit"`
	Base      int       `json:"base"` // the version it was opened from
	Tree      string    `json:"tree"` // the draft tree's sha256: its ETag
	OpenedAt  time.Time `json:"opened_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Actor     string    `json:"actor"` // who last wrote it (opened or saved)
}

// GetKitDraft returns kit's open draft.
func (m *memState) GetKitDraft(kit string) (KitDraft, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.kitDrafts[kit]
	return d, ok
}

// ListKitDrafts lists the open drafts, by kit name.
func (m *memState) ListKitDrafts() []KitDraft {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]KitDraft, 0, len(m.kitDrafts))
	for _, d := range m.kitDrafts {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b KitDraft) int { return cmp.Compare(a.Kit, b.Kit) })
	return out
}

// checkKitDraft refuses a draft write that does not hold together: an
// unknown kit (ErrNotFound) or base version, objects failing the file rules
// (kittree.Tree.ValidateFiles: a draft may lack kit.yml), or a tree neither
// written nor stored. Caller holds mu.
func checkKitDraft(m *memState, d KitDraft, objs KitObjects) error {
	k, ok := m.kits[d.Kit]
	if !ok {
		return fmt.Errorf("kit %q: %w", d.Kit, ErrNotFound)
	}
	if !k.HasVersion(d.Base) {
		return fmt.Errorf("kit %s has no version %d", d.Kit, d.Base)
	}
	written, err := checkObjects(m, objs, kittree.Tree.ValidateFiles)
	if err != nil {
		return err
	}
	if _, stored := m.kitTrees[d.Tree]; !written[d.Tree] && !stored {
		return fmt.Errorf("kit %s draft: tree %.12s is not stored", d.Kit, d.Tree)
	}
	return nil
}

func (m *memState) applyPutKitDraft(d KitDraft) { m.kitDrafts[d.Kit] = d }

func (m *memState) applyRemoveKitDraft(kit string) bool {
	if _, ok := m.kitDrafts[kit]; !ok {
		return false
	}
	delete(m.kitDrafts, kit)
	return true
}
```

In `internal/jam/kit_objects.go`, split `checkKitObjects`. Its body moves into `checkObjects` and the doc check stays:

```go
// checkKitObjects refuses a kit write that does not hold together: objects
// failing checkObjects under the version rules (kittree.Tree.Validate), or a
// doc naming a tree that is neither written nor stored. Caller holds mu.
func checkKitObjects(m *memState, kits []Kit, objs KitObjects) error {
	written, err := checkObjects(m, objs, kittree.Tree.Validate)
	if err != nil {
		return err
	}
	for _, k := range kits {
		for _, v := range slices.Sorted(maps.Keys(k.Trees)) {
			if sha := k.Trees[v]; !written[sha] {
				if _, ok := m.kitTrees[sha]; !ok {
					return fmt.Errorf("kit %s v%d: tree %.12s is not stored", k.Name, v, sha)
				}
			}
		}
	}
	return nil
}

// checkObjects checks objs on their own: every blob is its sha256, every
// tree passes validate and names only blobs written or stored (of its
// size). It returns the written trees' hashes. Caller holds mu.
func checkObjects(m *memState, objs KitObjects, validate func(kittree.Tree) error) (map[string]bool, error) {
	for sha, b := range objs.Blobs {
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != sha {
			return nil, fmt.Errorf("kit blob %.12s: content does not match its sha256", sha)
		}
	}
	written := map[string]bool{}
	for _, t := range objs.Trees {
		if err := validate(t); err != nil {
			return nil, fmt.Errorf("kit tree: %w", err)
		}
		for _, e := range t.Entries {
			b, ok := objs.Blobs[e.SHA256]
			if !ok {
				b, ok = m.kitBlobs[e.SHA256]
			}
			if !ok || int64(len(b)) != e.Size {
				return nil, fmt.Errorf("kit tree %.12s: %s has no stored content", t.Hash(), e.Path)
			}
		}
		written[t.Hash()] = true
	}
	return written, nil
}
```

In `deadKitObjects`, right after the loop that marks every `k.Trees` hash live, add:

```go
	for _, d := range m.kitDrafts {
		live[d.Tree] = true // an open draft references its tree too
	}
```

Change its doc comment's first line to: `// deadKitObjects lists, sorted, the trees no kit version or draft references and`.

In `internal/jam/memstate.go`:
- Add the field after `kitBlobs`: `kitDrafts map[string]KitDraft // kit name → its open draft (kit_drafts.go)`.
- In `newMemState`, add `kitDrafts: map[string]KitDraft{},`.
- In `applyRemoveKit`, add `delete(m.kitDrafts, name) // a kit's draft goes with it` after `delete(m.kits, name)`.

In `internal/jam/config_snapshot.go` `applyImport`, add `m.kitDrafts = map[string]KitDraft{}` right after `m.kits = map[string]Kit{}`. An import fills an empty store, and drafts are never imported.

In `internal/jam/memstore.go`, after `GCKitObjects`:

```go
func (fs *MemStore) PutKitDraft(d KitDraft, objs KitObjects) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := checkKitDraft(fs.memState, d, objs); err != nil {
		return err
	}
	fs.applyKitObjects(objs)
	fs.applyPutKitDraft(d)
	return nil
}

func (fs *MemStore) RemoveKitDraft(kit string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.applyRemoveKitDraft(kit) {
		return fmt.Errorf("kit %s draft: %w", kit, ErrNotFound)
	}
	return nil
}
```

In `internal/jam/store.go`, after `GCKitObjects() (KitGC, error)`:

```go
	// GetKitDraft, ListKitDrafts, PutKitDraft and RemoveKitDraft hold the
	// kits' drafts, at most one per kit (kit_drafts.go). PutKitDraft upserts
	// d with objs in one atomic step: d.Kit must exist (ErrNotFound) with
	// version d.Base, objs must pass the file rules (a draft may lack
	// kit.yml), and d.Tree must be written or stored. RemoveKitDraft wraps
	// ErrNotFound when there is no draft; RemoveKit (and a config apply's
	// RemoveKits) drops the kit's draft. A draft's tree is referenced for
	// GCKitObjects. Drafts are never in ExportConfig.
	GetKitDraft(kit string) (KitDraft, bool)
	ListKitDrafts() []KitDraft
	PutKitDraft(d KitDraft, objs KitObjects) error
	RemoveKitDraft(kit string) error
```

Also update the `GCKitObjects` comment to: `// GCKitObjects deletes the trees no kit version or draft references and the blobs`.

- [ ] **Step 4: Implement the Postgres side**

```sql
-- internal/jam/migrations/0020_kit_drafts.sql
-- Kit drafts (docs/usage/jam/kits-drafts.md): at most one per kit, a mutable
-- tree whose objects live in kit_trees / kit_blobs. Working state: not in a
-- config snapshot. Removing the kit removes its draft.
CREATE TABLE kit_drafts (
    name       text        PRIMARY KEY REFERENCES kits (name) ON DELETE CASCADE,
    base       integer     NOT NULL,
    tree       text        NOT NULL,
    opened_at  timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    actor      text        NOT NULL
);
```

```go
// internal/jam/pgstore_kit_drafts.go
package jam

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PutKitDraft writes objs and the draft row in one transaction; the cache
// changes only after commit.
func (s *PostgresStore) PutKitDraft(d KitDraft, objs KitObjects) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkKitDraft(s.memState, d, objs); err != nil {
		return err
	}
	if err := s.registryTx("PutKitDraft", func(ctx context.Context, tx pgx.Tx) error {
		if err := insertKitObjectsTx(ctx, tx, objs); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO kit_drafts (name, base, tree, opened_at, updated_at, actor) VALUES ($1,$2,$3,$4,$5,$6)
			 ON CONFLICT (name) DO UPDATE SET base = EXCLUDED.base, tree = EXCLUDED.tree, opened_at = EXCLUDED.opened_at,
			 updated_at = EXCLUDED.updated_at, actor = EXCLUDED.actor`,
			d.Kit, d.Base, d.Tree, d.OpenedAt, d.UpdatedAt, d.Actor)
		return err
	}); err != nil {
		return err
	}
	s.applyKitObjects(objs)
	s.applyPutKitDraft(d)
	return nil
}

func (s *PostgresStore) RemoveKitDraft(kit string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kitDrafts[kit]; !ok {
		return fmt.Errorf("kit %s draft: %w", kit, ErrNotFound)
	}
	if err := s.exec("RemoveKitDraft", `DELETE FROM kit_drafts WHERE name = $1`, kit); err != nil {
		return err
	}
	s.applyRemoveKitDraft(kit)
	return nil
}

// loadKitDrafts reads every draft row into the cache (load, before the store
// is shared). Times come back in UTC.
func (s *PostgresStore) loadKitDrafts(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT name, base, tree, opened_at, updated_at, actor FROM kit_drafts`)
	if err != nil {
		return fmt.Errorf("pgstore: load kit_drafts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d KitDraft
		if err := rows.Scan(&d.Kit, &d.Base, &d.Tree, &d.OpenedAt, &d.UpdatedAt, &d.Actor); err != nil {
			return err
		}
		d.OpenedAt, d.UpdatedAt = d.OpenedAt.UTC(), d.UpdatedAt.UTC()
		s.kitDrafts[d.Kit] = d
	}
	return rows.Err()
}
```

In `internal/jam/pgstore.go` `load`, right after the `s.loadKitObjects(ctx)` call:

```go
	if err := s.loadKitDrafts(ctx); err != nil {
		return err
	}
```

In `internal/jam/pgstore_testhelpers.go`:
- Append `, kit_drafts` to the `TRUNCATE` list. It references `kits`, so the two must be truncated together.
- Add `s.kitDrafts = map[string]KitDraft{}` after `s.kitBlobs = map[string][]byte{}`.

A config apply's `RemoveKits` (`DELETE FROM kits`) and `RemoveKit` reach the row through `ON DELETE CASCADE`, and the cache through `applyRemoveKit`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... && GOPROXY=https://proxy.golang.org,direct go vet -tags integration ./...`
Expected: PASS, with vet clean (the Postgres test compiles; it runs in CI).

- [ ] **Step 6: Lint and commit**

```bash
gofmt -l internal/jam/
just lint; echo "lint exit=$?"
git add internal/jam/
git commit -m "feat(jam): kit drafts in the store (kit_drafts, GC roots, conformance)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Open, status and discard (jam layer, routes, the doc)

**Files:**
- Create: `internal/jam/kit_draft.go`, `internal/jam/kit_draft_admin.go`, `docs/usage/jam/kits-drafts.md`
- Modify: `internal/jam/admin.go` (call `registerKitDrafts` after `registerRooms`; `KitSummary.Draft`), `docs/usage/jam/INDEX.md` (row, `updated:`)
- Test: `internal/jam/kit_draft_test.go`, `internal/jam/kit_draft_admin_test.go`

**Interfaces:**
- Consumes: `Store.GetKitDraft/PutKitDraft/RemoveKitDraft` (Task 2); `kitMu`, `writeErr`, `WriteStatus`, `validateKit`, `gcKitObjects`, `RemoveKitChecked`, `writeJSON`, `OperatorID`, `ConfigKindKit`, `ConfigKey`, `studio.ParseTree`, `studio.TagSafeName`, `kittree.Changes`; the existing test helpers `newTestAdmin`, `dockerfile`.
- Produces:
```go
// package jam
type KitDraftStatus struct { Kit string; Base, Current int; Tree, ETag string; OpenedAt, UpdatedAt time.Time; Actor string; Entries []DraftEntry; Changes []DraftChange; Problems []string; Managed bool }
type DraftEntry struct { Path, Mode string; Size int64; SHA256 string }
type DraftChange struct { Path, Kind string }
type KitDraftOpenBody struct { From int `json:"from,omitempty"` }
const DraftDriftWarning = "this will drift from the config repo; config write it to keep the repo in sync"
func DraftETag(tree string) string
func OpenKitDraft(store Store, name string, from int, actor string, now time.Time) (KitDraft, error)
func KitDraftStatusOf(store Store, name string) (KitDraftStatus, error)
func DiscardKitDraft(store Store, log *slog.Logger, name, ifMatch string) (KitDraft, error)
func draftOf(store Store, name string) (KitDraft, error)
func checkDraftETag(d KitDraft, ifMatch string) error
func draftProblems(store Store, name string, tree kittree.Tree) (studio.StudioKit, []string)
func draftEntries(t kittree.Tree) []DraftEntry
func draftChanges(from, to kittree.Tree) []DraftChange
func kitManaged(store Store, name string) bool
func unsafeKitName() error
// kit_draft_admin.go
func registerKitDrafts(mux *http.ServeMux, store Store, log *slog.Logger)
func draftFail(w http.ResponseWriter, err error)
func requireContentType(w http.ResponseWriter, r *http.Request, want string) bool
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool
func shortTree(sha string) string
// KitSummary gains Draft bool `json:"draft,omitempty"`
// test helpers (package jam, used by Tasks 4–6):
var draftNow time.Time
func pushApp(t *testing.T, st Store, kv ...string)
func draftDo(t *testing.T, h http.Handler, method, path, ctype, ifMatch string, body []byte) *httptest.ResponseRecorder
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/jam/kit_draft_test.go
package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

var draftNow = time.Unix(100, 0).UTC()

// pushApp pushes path → content pairs ("*" suffix = 0755) as kit app's next version.
func pushApp(t *testing.T, st Store, kv ...string) {
	t.Helper()
	if _, _, err := PushKitFiles(st, "app", kittreetest.Files(kv...)); err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err error, text string, status int) {
	t.Helper()
	if err == nil || err.Error() != text || WriteStatus(err, 500) != status {
		t.Fatalf("err = %v (status %d), want %q (%d)", err, WriteStatus(err, 500), text, status)
	}
}

func TestOpenKitDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	pushApp(t, st, "kit.yml", "kind: studio\nprompt: two\n")
	d, err := OpenKitDraft(st, "app", 1, "local", draftNow)
	k, _ := st.GetKit("app")
	if err != nil || d.Base != 1 || d.Tree != k.Trees[1] || d.Actor != "local" || !d.OpenedAt.Equal(draftNow) || !d.UpdatedAt.Equal(draftNow) {
		t.Fatalf("open = %+v %v", d, err)
	}
	if got, ok := st.GetKitDraft("app"); !ok || got.Tree != d.Tree {
		t.Fatalf("stored draft = %+v %v", got, ok)
	}
	_, err = OpenKitDraft(st, "app", 0, "local", draftNow)
	wantErr(t, err, "kit app already has a draft", 409)
	_, err = OpenKitDraft(st, "app", 9, "local", draftNow)
	wantErr(t, err, "kit app has no version v9", 404)
	_, err = OpenKitDraft(st, "app", -1, "local", draftNow)
	wantErr(t, err, "from must be a version number", 400)
	_, err = OpenKitDraft(st, "nope", 0, "local", draftNow)
	wantErr(t, err, "no such kit", 404)
	_, err = OpenKitDraft(st, "a b\n", 0, "local", draftNow)
	wantErr(t, err, "kit name is not tag-safe (allowed: [A-Za-z0-9_.-])", 400)
	// A broken (migration) version has no tree to open.
	broken := Kit{Name: "old", Current: 1, Broken: map[int]string{1: "unreadable"}}
	if err := st.PutKitDocs([]Kit{broken}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	_, err = OpenKitDraft(st, "old", 0, "local", draftNow)
	wantErr(t, err, "kit old v1 is not stored as a tree", 400)
}

func TestKitDraftStatus(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
	d, err := OpenKitDraft(st, "app", 0, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	s, err := KitDraftStatusOf(st, "app")
	if err != nil || s.Kit != "app" || s.Base != 1 || s.Current != 1 || s.Tree != d.Tree || s.ETag != `"`+d.Tree+`"` || s.Actor != "local" || s.Managed {
		t.Fatalf("status = %+v %v", s, err)
	}
	if len(s.Entries) != 2 || s.Entries[1].Path != "run.sh" || s.Entries[1].Mode != "0755" || s.Entries[1].Size != 5 || len(s.Entries[1].SHA256) != 64 {
		t.Fatalf("entries = %+v", s.Entries)
	}
	if s.Changes == nil || len(s.Changes) != 0 || s.Problems == nil || len(s.Problems) != 0 {
		t.Fatalf("changes %#v problems %#v (empty lists, not null)", s.Changes, s.Problems)
	}
	_, err = KitDraftStatusOf(st, "nope")
	wantErr(t, err, "no such kit", 404)
	if _, _, err := PushKitFiles(st, "web", kittreetest.Files("kit.yml", "kind: studio\n")); err != nil {
		t.Fatal(err)
	}
	_, err = KitDraftStatusOf(st, "web")
	wantErr(t, err, "kit web has no draft", 404)
	if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "abc1234", Digest: "d", AppliedAt: time.Unix(1, 0).UTC()}}}); err != nil {
		t.Fatal(err)
	}
	if s, _ := KitDraftStatusOf(st, "app"); !s.Managed {
		t.Fatal("a config-managed kit's draft must say so")
	}
}

func TestDraftProblems(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	for name, tc := range map[string]struct {
		kv   []string
		want string
	}{
		"no kit.yml":     {[]string{"run.sh", "echo\n"}, "kit.yml missing at the root"},
		"invalid kind":   {[]string{"kit.yml", "kind: nope\n"}, `studio kit: kind must be "studio", got "nope"`},
		"context field":  {[]string{"kit.yml", "kind: studio\nbase:\n  context: H4sI\n"}, "base.context is no longer supported; put a Dockerfile next to kit.yml"},
		"missing parent": {[]string{"kit.yml", "kind: studio\nbase:\n  kit: ghost\n"}, "kit ghost (parent of app) is missing"},
		"missing note":   {[]string{"kit.yml", "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: a.md}\n"}, `note "a.md": file a.md is not in the kit`},
	} {
		t.Run(name, func(t *testing.T) {
			tr, blobs, err := kittree.BuildFiles(kittreetest.Files(tc.kv...))
			if err != nil {
				t.Fatal(err)
			}
			d := KitDraft{Kit: "app", Base: 1, Tree: tr.Hash(), OpenedAt: draftNow, UpdatedAt: draftNow, Actor: "local"}
			if err := st.PutKitDraft(d, KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}); err != nil {
				t.Fatal(err)
			}
			s, err := KitDraftStatusOf(st, "app")
			if err != nil || len(s.Problems) != 1 || s.Problems[0] != tc.want {
				t.Fatalf("problems = %q %v, want [%q]", s.Problems, err, tc.want)
			}
		})
	}
}

func TestDiscardKitDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	d, _ := OpenKitDraft(st, "app", 0, "local", draftNow)
	_, err := DiscardKitDraft(st, nil, "app", "")
	wantErr(t, err, "draft app: If-Match is required (send the etag from the draft status)", 428)
	_, err = DiscardKitDraft(st, nil, "app", `"`+strings.Repeat("0", 64)+`"`)
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	_, err = DiscardKitDraft(st, nil, "app", `W/"`+d.Tree+`"`)
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	if _, err := DiscardKitDraft(st, nil, "app", d.Tree); err != nil { // the quotes are optional
		t.Fatal(err)
	}
	if _, ok := st.GetKitDraft("app"); ok {
		t.Fatal("discard left the draft")
	}
	if k, _ := st.GetKit("app"); k.Current != 1 || len(k.VersionNumbers()) != 1 {
		t.Fatal("discard touched the kit")
	}
	_, err = DiscardKitDraft(st, nil, "app", d.Tree)
	wantErr(t, err, "kit app has no draft", 404)
}

// TestRemoveKitDropsDraft: kit rm is not blocked by a draft and takes it
// (and, after GC, its tree) with it (Review Focus 3).
func TestRemoveKitDropsDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	d, err := OpenKitDraft(st, "app", 0, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	tr, blobs, err := kittree.BuildFiles(kittreetest.Files("draft-only.txt", "x"))
	if err != nil {
		t.Fatal(err)
	}
	d.Tree = tr.Hash()
	if err := st.PutKitDraft(d, KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKitChecked(st, nil, "app"); err != nil {
		t.Fatalf("a draft must not block kit rm: %v", err)
	}
	if _, ok := st.GetKitDraft("app"); ok {
		t.Fatal("the draft outlived its kit")
	}
	if _, ok := st.KitTree(tr.Hash()); ok {
		t.Fatal("the draft's tree outlived its kit")
	}
	_, err = KitDraftStatusOf(st, "app")
	wantErr(t, err, "no such kit", 404)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	_, err = KitDraftStatusOf(st, "app")
	wantErr(t, err, "kit app has no draft", 404)
}
```

```go
// internal/jam/kit_draft_admin_test.go
package jam

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// draftDo issues one loopback admin request with an optional Content-Type
// and If-Match.
func draftDo(t *testing.T, h http.Handler, method, path, ctype, ifMatch string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5000"
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	if ifMatch != "" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func body(w *httptest.ResponseRecorder) string { return strings.TrimSpace(w.Body.String()) }

func TestDraftOpenStatusDiscardRoutes(t *testing.T) {
	var logs bytes.Buffer
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	// JSON only: a browser's no-cors text/plain POST (CSRF) opens nothing.
	for _, ctype := range []string{"text/plain", "", "application/x-www-form-urlencoded"} {
		if w := draftDo(t, h, "POST", "/admin/kits/app/draft", ctype, "", []byte(`{}`)); w.Code != http.StatusUnsupportedMediaType || body(w) != "kit draft: Content-Type must be application/json" {
			t.Fatalf("Content-Type %q = %d %s", ctype, w.Code, w.Body)
		}
	}
	if _, ok := st.GetKitDraft("app"); ok {
		t.Fatal("a refused open made a draft")
	}
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", []byte(`{"from":`)); w.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d", w.Code)
	}
	w := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil) // an empty body opens from current
	var s KitDraftStatus
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &s) != nil || s.Base != 1 || w.Header().Get("ETag") != s.ETag {
		t.Fatalf("open = %d %s (ETag %q)", w.Code, w.Body, w.Header().Get("ETag"))
	}
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", []byte(`{"from":1}`)); w.Code != http.StatusConflict || body(w) != "kit app already has a draft" {
		t.Fatalf("second open = %d %s", w.Code, w.Body)
	}
	g := draftDo(t, h, "GET", "/admin/kits/app/draft", "", "", nil)
	if g.Code != http.StatusOK || g.Header().Get("ETag") != s.ETag {
		t.Fatalf("status = %d %s", g.Code, g.Body)
	}
	var kits []KitSummary
	getJSON(t, h, "/admin/kits", &kits)
	if len(kits) != 1 || !kits[0].Draft {
		t.Fatalf("kits = %+v (app has a draft)", kits)
	}
	if w := draftDo(t, h, "DELETE", "/admin/kits/app/draft", "", "", nil); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("discard without If-Match = %d", w.Code)
	}
	if w := draftDo(t, h, "DELETE", "/admin/kits/app/draft", "", `"`+strings.Repeat("0", 64)+`"`, nil); w.Code != http.StatusPreconditionFailed || body(w) != "draft app changed since you loaded it (reload)" {
		t.Fatalf("stale discard = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "DELETE", "/admin/kits/app/draft", "", s.ETag, nil); w.Code != http.StatusNoContent {
		t.Fatalf("discard = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "GET", "/admin/kits/app/draft", "", "", nil); w.Code != http.StatusNotFound || body(w) != "kit app has no draft" {
		t.Fatalf("status after discard = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "GET", "/admin/kits/nope/draft", "", "", nil); w.Code != http.StatusNotFound || body(w) != "no such kit" {
		t.Fatalf("unknown kit = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil); w.Code != http.StatusCreated {
		t.Fatalf("reopen = %d", w.Code)
	}
	out := logs.String()
	for _, want := range []string{"kit draft opened", "kit draft discarded", "kit=app", "tree=" + s.Tree[:12], "operator=local"} {
		if !strings.Contains(out, want) {
			t.Fatalf("audit log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "kind: studio") {
		t.Fatal("the audit log carries file content")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'OpenKitDraft|KitDraftStatus|DraftProblems|DiscardKitDraft|RemoveKitDropsDraft|DraftOpenStatusDiscardRoutes'`
Expected: FAIL to compile, `undefined: OpenKitDraft`.

- [ ] **Step 3: Implement the jam layer**

```go
// internal/jam/kit_draft.go
package jam

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitDraftStatus is GET /admin/kits/{name}/draft: the draft, its files, its
// changes from its base version, and what blocks publishing it.
type KitDraftStatus struct {
	Kit       string        `json:"kit"`
	Base      int           `json:"base"`    // the version it was opened from
	Current   int           `json:"current"` // the kit's current version now; ≠ Base when one landed since
	Tree      string        `json:"tree"`
	ETag      string        `json:"etag"` // quoted tree hash: send it back as If-Match
	OpenedAt  time.Time     `json:"opened_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Actor     string        `json:"actor"` // who last wrote it
	Entries   []DraftEntry  `json:"entries"`
	Changes   []DraftChange `json:"changes"`           // vs Base
	Problems  []string      `json:"problems"`          // empty: publishable
	Managed   bool          `json:"managed,omitempty"` // config-managed: publishing drifts
}

// DraftEntry is one file of a draft.
type DraftEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"` // "0644" or "0755"
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// DraftChange is one file that differs: kind added, removed, modified or mode.
type DraftChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// KitDraftOpenBody is the POST /admin/kits/{name}/draft request; From 0 = current.
type KitDraftOpenBody struct {
	From int `json:"from,omitempty"`
}

// DraftDriftWarning is the fixed warning a publish of a config-managed kit
// answers with (KitDraftPublish.Warning), shown by the UI and the CLI.
const DraftDriftWarning = "this will drift from the config repo; config write it to keep the repo in sync"

// DraftETag is a draft's ETag: its tree hash, quoted.
func DraftETag(tree string) string { return `"` + tree + `"` }

func unsafeKitName() error {
	return writeErr(http.StatusBadRequest, "kit name is not tag-safe (allowed: [A-Za-z0-9_.-])")
}

// draftOf is kit name's open draft: 400 for a name that is not tag-safe
// (checked before any text names it), 404 for an unknown kit or no draft.
func draftOf(store Store, name string) (KitDraft, error) {
	if !studio.TagSafeName(name) {
		return KitDraft{}, unsafeKitName()
	}
	if _, ok := store.GetKit(name); !ok {
		return KitDraft{}, writeErr(http.StatusNotFound, "no such kit")
	}
	d, ok := store.GetKitDraft(name)
	if !ok {
		return KitDraft{}, writeErr(http.StatusNotFound, "kit %s has no draft", name)
	}
	return d, nil
}

// checkDraftETag refuses a write without If-Match (428) or against a tree
// that is no longer the draft's (412). The quotes around the hash are
// optional; a weak tag or * never matches.
func checkDraftETag(d KitDraft, ifMatch string) error {
	tag := strings.TrimSpace(ifMatch)
	if tag == "" {
		return writeErr(http.StatusPreconditionRequired, "draft %s: If-Match is required (send the etag from the draft status)", d.Kit)
	}
	if strings.Trim(tag, `"`) != d.Tree {
		return writeErr(http.StatusPreconditionFailed, "draft %s changed since you loaded it (reload)", d.Kit)
	}
	return nil
}

// draftProblems reads a draft tree as a push would and lists what blocks
// publishing it, in fixed text: kit.yml missing, else the parse (schema,
// packed-context fields, note files), else validateKit (name, chain,
// budgets). The parsers stop at their first error. sk is the parsed kit
// when there are none; problems is never nil.
func draftProblems(store Store, name string, tree kittree.Tree) (studio.StudioKit, []string) {
	if _, ok := tree.Lookup(kittree.KitFile); !ok {
		return studio.StudioKit{}, []string{"kit.yml missing at the root"}
	}
	sk, err := studio.ParseTree(tree, store.KitBlob)
	if err == nil {
		_, err = validateKit(store, name, sk)
	}
	if err != nil {
		return studio.StudioKit{}, []string{err.Error()}
	}
	return sk, []string{}
}

func draftEntries(t kittree.Tree) []DraftEntry {
	out := make([]DraftEntry, 0, len(t.Entries))
	for _, e := range t.Entries {
		out = append(out, DraftEntry{Path: e.Path, Mode: fmt.Sprintf("%04o", e.Mode), Size: e.Size, SHA256: e.SHA256})
	}
	return out
}

func draftChanges(from, to kittree.Tree) []DraftChange {
	cs, _ := kittree.Changes(from, to)
	out := make([]DraftChange, 0, len(cs))
	for _, c := range cs {
		out = append(out, DraftChange{Path: c.Path, Kind: c.Kind})
	}
	return out
}

// kitManaged reports whether the config repo manages kit name (a managed mark).
func kitManaged(store Store, name string) bool {
	key := ConfigKey{Kind: ConfigKindKit, Name: name}
	return slices.ContainsFunc(store.ListConfigMarks(), func(m ConfigMark) bool { return m.Key() == key })
}

// OpenKitDraft opens kit name's draft from version from (0 = current) for
// actor at now, under kitMu. 400 for an unsafe name, a negative from or a
// version with no stored tree (broken, pre-tree); 404 for an unknown kit or
// version; 409 when the kit already has a draft.
func OpenKitDraft(store Store, name string, from int, actor string, now time.Time) (KitDraft, error) {
	if !studio.TagSafeName(name) {
		return KitDraft{}, unsafeKitName()
	}
	if from < 0 {
		return KitDraft{}, writeErr(http.StatusBadRequest, "from must be a version number")
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	k, ok := store.GetKit(name)
	if !ok {
		return KitDraft{}, writeErr(http.StatusNotFound, "no such kit")
	}
	if from == 0 {
		from = k.Current
	}
	if !k.HasVersion(from) {
		return KitDraft{}, writeErr(http.StatusNotFound, "kit %s has no version v%d", name, from)
	}
	tree, ok := k.Trees[from]
	if _, stored := store.KitTree(tree); !ok || !stored {
		return KitDraft{}, writeErr(http.StatusBadRequest, "kit %s v%d is not stored as a tree", name, from)
	}
	if _, open := store.GetKitDraft(name); open {
		return KitDraft{}, writeErr(http.StatusConflict, "kit %s already has a draft", name)
	}
	d := KitDraft{Kit: name, Base: from, Tree: tree, OpenedAt: now, UpdatedAt: now, Actor: actor}
	if err := store.PutKitDraft(d, KitObjects{}); err != nil {
		return KitDraft{}, err
	}
	return d, nil
}

// KitDraftStatusOf is kit name's draft status, read under kitMu as one
// snapshot (a concurrent save and GC cannot drop its tree mid-read).
func KitDraftStatusOf(store Store, name string) (KitDraftStatus, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, err := draftOf(store, name)
	if err != nil {
		return KitDraftStatus{}, err
	}
	tree, ok := store.KitTree(d.Tree)
	if !ok {
		return KitDraftStatus{}, fmt.Errorf("kit %s draft: tree %.12s is not stored", name, d.Tree)
	}
	k, _ := store.GetKit(name)
	base, _ := store.KitTree(k.Trees[d.Base])
	_, problems := draftProblems(store, name, tree)
	return KitDraftStatus{
		Kit: name, Base: d.Base, Current: k.Current, Tree: d.Tree, ETag: DraftETag(d.Tree),
		OpenedAt: d.OpenedAt, UpdatedAt: d.UpdatedAt, Actor: d.Actor,
		Entries: draftEntries(tree), Changes: draftChanges(base, tree), Problems: problems,
		Managed: kitManaged(store, name),
	}, nil
}

// DiscardKitDraft drops kit name's draft (If-Match checked) under kitMu and
// collects what only it held. It returns the dropped draft (for the log).
func DiscardKitDraft(store Store, log *slog.Logger, name, ifMatch string) (KitDraft, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, err := draftOf(store, name)
	if err != nil {
		return KitDraft{}, err
	}
	if err := checkDraftETag(d, ifMatch); err != nil {
		return KitDraft{}, err
	}
	if err := store.RemoveKitDraft(name); err != nil {
		return KitDraft{}, err
	}
	gcKitObjects(store, log)
	return d, nil
}
```

- [ ] **Step 4: Implement the routes**

```go
// internal/jam/kit_draft_admin.go
package jam

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"
)

// registerKitDrafts mounts the kit draft API (docs/usage/jam/kits-drafts.md)
// on the admin mux, behind the same operator gate:
//
//	POST   /admin/kits/{name}/draft   {from?}  open (201, the status)
//	GET    /admin/kits/{name}/draft            status (ETag header)
//	DELETE /admin/kits/{name}/draft            discard (If-Match; 204)
//
// Every write but open needs If-Match (the draft's ETag: 428 without, 412
// stale). Open and publish take JSON only, a file write octet-stream only,
// a tree replace a tar only (415 otherwise, before any body is read), so no
// browser no-cors request can write. Audit logs carry the kit, the operator
// and tree prefixes, never content.
func registerKitDrafts(mux *http.ServeMux, store Store, log *slog.Logger) {
	mux.HandleFunc("POST /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		if !requireContentType(w, r, "application/json") {
			return
		}
		var b KitDraftOpenBody
		if !decodeOptional(w, r, &b) {
			return
		}
		name := r.PathValue("name")
		d, err := OpenKitDraft(store, name, b.From, OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFail(w, err)
			return
		}
		log.Info("kit draft opened", "operator", OperatorID(r), "kit", name, "base", d.Base, "tree", shortTree(d.Tree))
		writeDraftStatus(w, store, name, http.StatusCreated)
	})
	mux.HandleFunc("GET /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		writeDraftStatus(w, store, r.PathValue("name"), http.StatusOK)
	})
	mux.HandleFunc("DELETE /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		d, err := DiscardKitDraft(store, log, name, r.Header.Get("If-Match"))
		if err != nil {
			draftFail(w, err)
			return
		}
		log.Info("kit draft discarded", "operator", OperatorID(r), "kit", name, "tree", shortTree(d.Tree))
		w.WriteHeader(http.StatusNoContent)
	})
}

// writeDraftStatus answers kit name's draft status with its ETag.
func writeDraftStatus(w http.ResponseWriter, store Store, name string, code int) {
	st, err := KitDraftStatusOf(store, name)
	if err != nil {
		draftFail(w, err)
		return
	}
	w.Header().Set("ETag", st.ETag)
	writeJSON(w, code, st)
}

// draftFail answers err: a WriteError's own status and fixed text, else 500.
func draftFail(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), WriteStatus(err, http.StatusInternalServerError))
}

// requireContentType refuses (415) a request whose media type is not want,
// before its body is read.
func requireContentType(w http.ResponseWriter, r *http.Request, want string) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != want {
		http.Error(w, "kit draft: Content-Type must be "+want, http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// decodeOptional decodes a small JSON body into v; an empty body leaves v zero.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

// shortTree is a tree hash's 12-hex prefix, for logs.
func shortTree(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
```

In `internal/jam/admin.go`:
- After `registerRooms(mux, store, log)`, add `registerKitDrafts(mux, store, log)`.
- In `KitSummary`, add `Draft bool \`json:"draft,omitempty"\` // an open draft (kits-drafts.md)`.
- In `GET /admin/kits`, after `sum.Parent = …`, add `_, sum.Draft = store.GetKitDraft(k.Name)`.

- [ ] **Step 5: Write the doc**

Create `docs/usage/jam/kits-drafts.md` with this content. Write each <link …> as a real markdown link.

````markdown
---
summary: A kit's draft — one mutable tree per kit to edit in place, then publish as the next version; the checks and problems, the ETag/If-Match rule, publish and --pin, the draft API and the `kit draft` verbs.
read_when: You want to edit a kit's files in place before publishing a version (`kit draft open|status|get|put|rm|discard|publish|sync`), a draft write answered 412 or 428, or a publish was refused (problems, no changes) or warned about drift.
owns: kit drafts — opening one, the draft tree and its checks, problems, the ETag/If-Match rule, publish (origin, --pin, drift), the /admin/kits/{name}/draft routes, the `kit draft` verbs and their audit lines
prereqs: kits.md, kits-directory.md
tier: leaf
updated: 2026-10-25
---

# Kit drafts

## The draft

A kit has at most one **draft**: a mutable copy of one version's files,
opened from *current* or a version you choose (its **base**). You change
files in the draft, then publish it as the kit's next version, or discard it.

- `kit.yml` may be missing or invalid while you edit. The draft's status lists
  the **problems**: `kit.yml missing at the root`, a parse or validation
  error, a missing parent, a `base.context…` field. Publish refuses while any
  remain.
- A draft is working state: it is not in `at-jam export`, `config write` or a
  config plan, and it does not block `kit rm`. Removing the kit removes its
  draft.

## Concurrency

The draft's **ETag** is its tree hash, quoted (`"3f2a…"`). Every write must
send it back as `If-Match`: without it the answer is 428, and with a stale one
412 `draft <kit> changed since you loaded it (reload)`. Nothing is merged:
reload the status and make the edit again. `If-Match: *` and weak tags never
match.

## API

Admin only, the same operator gate as the rest of `/admin`.

| Route | Does |
|---|---|
| `POST /admin/kits/{name}/draft` | Open, from `{"from": N}` (default current); `application/json`, else 415. 201 with the status; 409 `kit <name> already has a draft`; 400 for a broken version. |
| `GET /admin/kits/{name}/draft` | The status: `base`, `current`, `tree`, `etag` (also the `ETag` header), `entries` (`path`, `mode`, `size`, `sha256`), `changes` from the base, `problems`, `managed`. |
| `DELETE /admin/kits/{name}/draft` | Discard (`If-Match`). 204. |

`GET /admin/kits` marks a kit with an open draft `"draft": true`.

Each write logs `kit draft opened|saved|discarded|published` with the kit, the
operator and the tree's first 12 hex digits (publish: the version), never
content.
````

In `docs/usage/jam/INDEX.md`, add this row right after the `kits-directory.md` row (write the <link> as a real link), and set `updated: 2026-10-25` in the frontmatter:

```markdown
| <link `kits-drafts.md` as "kits-drafts.md"> | You want to edit a kit's files in place and publish them as a version (`kit draft open\|status\|get\|put\|rm\|discard\|publish\|sync`), a draft write answered 412/428, or a publish was refused (problems, no changes) or warned about drift. |
```

- [ ] **Step 6: Run the tests and the docs audit**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS, and the audit reports no more than 741 errors.

- [ ] **Step 7: Lint and commit**

```bash
gofmt -l internal/jam/
just lint; echo "lint exit=$?"
git add internal/jam/ docs/usage/jam/
git commit -m "feat(jam): open, status and discard a kit draft

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Draft files: get, put, delete, under the ETag

**Files:**
- Create: `internal/jam/kit_draft_files.go`
- Modify: `internal/jam/kit_draft_admin.go` (three file routes, `draftPrecheck`, `draftMode`, `writeDraftSave`, `logDraftSaved`), `docs/usage/jam/kits-drafts.md`
- Test: `internal/jam/kit_draft_files_test.go`, `internal/jam/kit_draft_admin_test.go`

**Interfaces:**
- Consumes: Task 1 (`BuildFiles`, `With`, `IgnoreTree`, `ValidateFiles`, `FileTooLarge`); Task 3 (`draftOf`, `checkDraftETag`, `draftProblems`, `draftChanges`, `DraftETag`, `unsafeKitName`, `draftFail`, `requireContentType`, `shortTree`, `pushApp`, `wantErr`, `draftDo`, `body`, `draftNow`).
- Produces:
```go
// package jam
type KitDraftSave struct { Kit, Tree, ETag string; Changed []DraftChange; Problems []string }
func KitDraftFile(store Store, name, p string) ([]byte, kittree.Entry, string, error) // data, entry, the draft's ETag
func PutKitDraftFile(store Store, log *slog.Logger, name, p string, mode fs.FileMode, data []byte, ifMatch, actor string, now time.Time) (KitDraftSave, error) // mode 0 = keep, else 0644
func RemoveKitDraftFile(store Store, log *slog.Logger, name, p, ifMatch, actor string, now time.Time) (KitDraftSave, error)
func lockedDraftTree(store Store, name, ifMatch string) (KitDraft, kittree.Tree, error) // caller holds kitMu
func saveDraftLocked(store Store, log *slog.Logger, d KitDraft, prev, next kittree.Tree, objs KitObjects, actor string, now time.Time) (KitDraftSave, error)
func draftPathErr(name string, err error) error
func draftFileTooLarge(name, p string) error
// kit_draft_admin.go
func draftPrecheck(w http.ResponseWriter, r *http.Request, store Store, name string) bool
func draftMode(name, q string) (fs.FileMode, error)
func writeDraftSave(w http.ResponseWriter, s KitDraftSave)
func logDraftSaved(log *slog.Logger, r *http.Request, name string, s KitDraftSave)
// routes: GET|PUT|DELETE /admin/kits/{name}/draft/files/{path...}
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/jam/kit_draft_files_test.go
package jam

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
)

const unsafeDraftPath = "draft app: a file has an unsafe path (empty, absolute, a . or .. segment, invalid UTF-8, or a control or format character)"

// withDraftLimits shrinks the tree limits for one test.
func withDraftLimits(t *testing.T, entries int, total, file int64) {
	t.Helper()
	e, to, f := kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile
	kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile = entries, total, file
	t.Cleanup(func() { kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile = e, to, f })
}

func openApp(t *testing.T, st Store) string {
	t.Helper()
	d, err := OpenKitDraft(st, "app", 0, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	return DraftETag(d.Tree)
}

func TestPutKitDraftFile(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
	tag := openApp(t, st)
	later := draftNow.Add(time.Minute)
	s, err := PutKitDraftFile(st, nil, "app", "docs/notes.md", 0, []byte("hi\n"), tag, "alice", later)
	if err != nil || s.ETag == tag || s.ETag != DraftETag(s.Tree) || !slices.Equal(s.Changed, []DraftChange{{"docs/notes.md", "added"}}) || len(s.Problems) != 0 {
		t.Fatalf("put = %+v %v", s, err)
	}
	if d, _ := st.GetKitDraft("app"); d.Tree != s.Tree || d.Actor != "alice" || !d.UpdatedAt.Equal(later) || !d.OpenedAt.Equal(draftNow) {
		t.Fatalf("draft row = %+v", d)
	}
	// Mode 0 keeps the file's mode (0755 here); a new file gets 0644.
	s, err = PutKitDraftFile(st, nil, "app", "run.sh", 0, []byte("echo two\n"), s.ETag, "alice", later)
	data, e, etag, ferr := KitDraftFile(st, "app", "run.sh")
	if err != nil || ferr != nil || string(data) != "echo two\n" || e.Mode != 0o755 || etag != s.ETag {
		t.Fatalf("get = %q %+v %q %v %v", data, e, etag, err, ferr)
	}
	if _, e, _, _ := KitDraftFile(st, "app", "docs/notes.md"); e.Mode != 0o644 {
		t.Fatalf("a new file's mode = %o", e.Mode)
	}
	s, err = PutKitDraftFile(st, nil, "app", "run.sh", 0o644, []byte("echo two\n"), s.ETag, "alice", later)
	if err != nil || !slices.Equal(s.Changed, []DraftChange{{"run.sh", "mode"}}) {
		t.Fatalf("mode change = %+v %v", s, err)
	}
	// An identical write is a no-op with the same ETag.
	same, err := PutKitDraftFile(st, nil, "app", "run.sh", 0o644, []byte("echo two\n"), s.ETag, "bob", later)
	if err != nil || same.ETag != s.ETag || len(same.Changed) != 0 {
		t.Fatalf("no-op = %+v %v", same, err)
	}
	if d, _ := st.GetKitDraft("app"); d.Actor != "alice" {
		t.Fatal("a no-op write recorded its actor")
	}
	// kit.yml may go while editing; the problem is listed.
	s, err = RemoveKitDraftFile(st, nil, "app", "kit.yml", s.ETag, "alice", later)
	if err != nil || !slices.Equal(s.Problems, []string{"kit.yml missing at the root"}) || !slices.Equal(s.Changed, []DraftChange{{"kit.yml", "removed"}}) {
		t.Fatalf("rm kit.yml = %+v %v", s, err)
	}
	_, err = RemoveKitDraftFile(st, nil, "app", "nope.txt", s.ETag, "alice", later)
	wantErr(t, err, "draft app has no file nope.txt", 404)
	_, _, _, err = KitDraftFile(st, "app", "nope.txt")
	wantErr(t, err, "draft app has no file nope.txt", 404)
	_, err = PutKitDraftFile(st, nil, "app", "x", 0o600, []byte("x"), s.ETag, "alice", later)
	wantErr(t, err, "draft app: mode must be 0644 or 0755", 400)
	if v, _ := LoadKitVersion(st, "app", 0); v.Version != 1 || v.Err != nil {
		t.Fatal("draft writes touched the published version")
	}
}

// TestDraftFileRefusals: hostile paths are refused unechoed; graphic unicode
// is a fine name; the limits and .dockerignore hold (Review Focus 4).
func TestDraftFileRefusals(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", ".dockerignore", "*.log\n", "sub/x", "x")
	tag := openApp(t, st)
	for name, tc := range map[string]struct{ p, want string }{
		"newline":         {"a\nb", unsafeDraftPath},
		"ansi escape":     {"a\x1b[31mb", unsafeDraftPath},
		"bidi override":   {"\u202etxt.exe", unsafeDraftPath},
		"dot dot":         {"../x", unsafeDraftPath},
		"dot segment":     {"a/./b", unsafeDraftPath},
		"invalid utf-8":   {"a\xffb", unsafeDraftPath},
		"absolute":        {"/etc/passwd", unsafeDraftPath},
		"empty":           {"", unsafeDraftPath},
		"file over a dir": {"sub", "draft app: sub: is both a file and a directory"},
		"ignored":         {"debug.log", "draft app: debug.log: excluded by .dockerignore"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PutKitDraftFile(st, nil, "app", tc.p, 0, []byte("x"), tag, "local", draftNow)
			wantErr(t, err, tc.want, 400)
		})
	}
	_, err := RemoveKitDraftFile(st, nil, "app", "a\nb", tag, "local", draftNow)
	wantErr(t, err, unsafeDraftPath, 400)
	_, _, _, err = KitDraftFile(st, "app", "a\nb")
	wantErr(t, err, unsafeDraftPath, 400)
	if d, _ := st.GetKitDraft("app"); DraftETag(d.Tree) != tag {
		t.Fatal("a refused write changed the draft")
	}
	uni := "docs/ünïcødé 名前.md"
	s, err := PutKitDraftFile(st, nil, "app", uni, 0, []byte("ok\n"), tag, "local", draftNow)
	if err != nil || s.Changed[0].Path != uni {
		t.Fatalf("unicode put = %+v %v", s, err)
	}
	if b, _, _, err := KitDraftFile(st, "app", uni); err != nil || string(b) != "ok\n" {
		t.Fatalf("unicode get = %q %v", b, err)
	}
	// Limits: 4 entries now (kit.yml, .dockerignore, sub/x, the unicode file).
	withDraftLimits(t, 5, 1<<20, 16)
	_, err = PutKitDraftFile(st, nil, "app", "big.bin", 0, make([]byte, 17), s.ETag, "local", draftNow)
	wantErr(t, err, "draft app: big.bin: more than 16 bytes (limit 16 bytes per file)", 413)
	s, err = PutKitDraftFile(st, nil, "app", "five.txt", 0, []byte("5"), s.ETag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PutKitDraftFile(st, nil, "app", "six.txt", 0, []byte("6"), s.ETag, "local", draftNow)
	wantErr(t, err, "draft app: 6 files (limit 5)", 400)
}

func TestDraftDockerignoreDropsFiles(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "debug.log", "x", "run.sh", "y")
	tag := openApp(t, st)
	s, err := PutKitDraftFile(st, nil, "app", ".dockerignore", 0, []byte("*.log\n"), tag, "local", draftNow)
	want := []DraftChange{{".dockerignore", "added"}, {"debug.log", "removed"}}
	if err != nil || !slices.Equal(s.Changed, want) {
		t.Fatalf("save .dockerignore = %+v %v, want %+v", s.Changed, err, want)
	}
	if _, _, _, err := KitDraftFile(st, "app", "debug.log"); err == nil {
		t.Fatal("an ignored file stayed in the draft")
	}
}

// TestDraftTwoTabsStaleETag: two editors load one ETag; the second save is
// refused, never merged (Review Focus 1).
func TestDraftTwoTabsStaleETag(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "notes.md", "v1\n")
	loaded := openApp(t, st) // both tabs load the draft
	a, err := PutKitDraftFile(st, nil, "app", "notes.md", 0, []byte("tab A\n"), loaded, "alice", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PutKitDraftFile(st, nil, "app", "notes.md", 0, []byte("tab B\n"), loaded, "bob", draftNow)
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	if b, _, _, _ := KitDraftFile(st, "app", "notes.md"); string(b) != "tab A\n" {
		t.Fatalf("tab B's stale save got through: %q", b)
	}
	for name, call := range map[string]func(tag string) error{
		"put": func(tag string) error {
			_, err := PutKitDraftFile(st, nil, "app", "other.md", 0, []byte("b"), tag, "bob", draftNow)
			return err
		},
		"rm": func(tag string) error {
			_, err := RemoveKitDraftFile(st, nil, "app", "notes.md", tag, "bob", draftNow)
			return err
		},
		"discard": func(tag string) error {
			_, err := DiscardKitDraft(st, nil, "app", tag)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantErr(t, call(loaded), "draft app changed since you loaded it (reload)", 412)
			wantErr(t, call(""), "draft app: If-Match is required (send the etag from the draft status)", 428)
		})
	}
	if d, _ := st.GetKitDraft("app"); DraftETag(d.Tree) != a.ETag {
		t.Fatal("a refused write changed the draft")
	}
	// Tab B reloads (a.ETag) and saves.
	if _, err := PutKitDraftFile(st, nil, "app", "notes.md", 0, []byte("tab B\n"), a.ETag, "bob", draftNow); err != nil {
		t.Fatal(err)
	}
}

// TestDraftWritesSurviveConcurrentGC: GC running alongside saves never takes
// a blob the draft references (Review Focus 5; -race in CI).
func TestDraftWritesSurviveConcurrentGC(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := openApp(t, st)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := st.GCKitObjects(); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	for i := range 50 {
		s, err := PutKitDraftFile(st, nil, "app", fmt.Sprintf("f%02d.txt", i), 0, []byte(fmt.Sprintf("content %d\n", i)), tag, "local", draftNow)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatal(err)
		}
		tag = s.ETag
	}
	close(stop)
	wg.Wait()
	if _, err := st.GCKitObjects(); err != nil {
		t.Fatal(err)
	}
	d, _ := st.GetKitDraft("app")
	tree, ok := st.KitTree(d.Tree)
	if !ok || len(tree.Entries) != 51 {
		t.Fatalf("draft tree = %d entries, %v", len(tree.Entries), ok)
	}
	for _, e := range tree.Entries {
		if _, ok := st.KitBlob(e.SHA256); !ok {
			t.Fatalf("GC took %s's blob from the draft", e.Path)
		}
	}
}
```

Append to `internal/jam/kit_draft_admin_test.go`:

```go
func TestDraftFileRoutes(t *testing.T) {
	var logs bytes.Buffer
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil).Header().Get("ETag")
	put := func(path, ctype, etag string, b []byte) *httptest.ResponseRecorder {
		return draftDo(t, h, "PUT", "/admin/kits/app/draft/files/"+path, ctype, etag, b)
	}
	const octet = "application/octet-stream"
	for _, ctype := range []string{"text/plain", "", "application/x-www-form-urlencoded"} {
		if w := put("run.sh", ctype, tag, []byte("echo\n")); w.Code != http.StatusUnsupportedMediaType || body(w) != "kit draft: Content-Type must be application/octet-stream" {
			t.Fatalf("Content-Type %q = %d %s", ctype, w.Code, w.Body)
		}
	}
	if w := put("run.sh", octet, "", []byte("echo\n")); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match = %d", w.Code)
	}
	if w := put("run.sh", octet, `"`+strings.Repeat("0", 64)+`"`, []byte("echo\n")); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale = %d", w.Code)
	}
	if w := put("run.sh?mode=0700", octet, tag, []byte("echo\n")); w.Code != http.StatusBadRequest || body(w) != "draft app: mode must be 0644 or 0755" {
		t.Fatalf("bad mode = %d %s", w.Code, w.Body)
	}
	w := put("run.sh?mode=755", octet, tag, []byte("echo SECRET-CONTENT\n"))
	var s KitDraftSave
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &s) != nil || w.Header().Get("ETag") != s.ETag || len(s.Changed) != 1 || s.Changed[0] != (DraftChange{"run.sh", "added"}) {
		t.Fatalf("put = %d %s", w.Code, w.Body)
	}
	g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/run.sh", "", "", nil)
	if g.Code != http.StatusOK || g.Body.String() != "echo SECRET-CONTENT\n" || g.Header().Get("X-Kit-File-Mode") != "0755" ||
		g.Header().Get("ETag") != s.ETag || g.Header().Get("Content-Type") != octet || g.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("get = %d %q %v", g.Code, g.Body, g.Header())
	}
	// A percent-encoded unicode path round-trips.
	w = put("docs/%C3%BCn%C3%AF.md", octet, s.ETag, []byte("ok\n"))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &s) != nil || s.Changed[0].Path != "docs/ünï.md" {
		t.Fatalf("unicode put = %d %s", w.Code, w.Body)
	}
	if g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/docs/%C3%BCn%C3%AF.md", "", "", nil); g.Body.String() != "ok\n" {
		t.Fatalf("unicode get = %d %q", g.Code, g.Body)
	}
	// A control or format character is refused, the name not echoed (Review Focus 4).
	for _, bad := range []string{"a%0Ab", "a%1B%5B31mb", "%E2%80%AEtxt.exe", "a%FFb"} {
		w := put(bad, octet, s.ETag, []byte("x"))
		if w.Code != http.StatusBadRequest || body(w) != unsafeDraftPath {
			t.Fatalf("PUT %s = %d %q", bad, w.Code, w.Body)
		}
		if g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/"+bad, "", "", nil); g.Code != http.StatusBadRequest || body(g) != unsafeDraftPath {
			t.Fatalf("GET %s = %d %q", bad, g.Code, g.Body)
		}
	}
	d := draftDo(t, h, "DELETE", "/admin/kits/app/draft/files/run.sh", "", s.ETag, nil)
	if d.Code != http.StatusOK || json.Unmarshal(d.Body.Bytes(), &s) != nil || s.Changed[0] != (DraftChange{"run.sh", "removed"}) {
		t.Fatalf("delete = %d %s", d.Code, d.Body)
	}
	if g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/run.sh", "", "", nil); g.Code != http.StatusNotFound || body(g) != "draft app has no file run.sh" {
		t.Fatalf("get removed = %d %s", g.Code, g.Body)
	}
	// Over MaxFile: 413 from the body cap.
	withDraftLimits(t, kittree.MaxEntries, kittree.MaxTotal, 8)
	if w := put("big.bin", octet, s.ETag, make([]byte, 9)); w.Code != http.StatusRequestEntityTooLarge || body(w) != "draft app: big.bin: more than 8 bytes (limit 8 bytes per file)" {
		t.Fatalf("oversize = %d %s", w.Code, w.Body)
	}
	out := logs.String()
	if !strings.Contains(out, "kit draft saved") || !strings.Contains(out, "tree="+s.Tree[:12]) || strings.Contains(out, "SECRET-CONTENT") {
		t.Fatalf("audit log:\n%s", out)
	}
}
```

Add `"github.com/aethons-tools/cove/internal/kittree"` to that file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'PutKitDraftFile|DraftFileRefusals|DraftDockerignore|TwoTabs|ConcurrentGC|DraftFileRoutes'`
Expected: FAIL to compile, `undefined: PutKitDraftFile`.

- [ ] **Step 3: Implement the jam layer**

```go
// internal/jam/kit_draft_files.go
package jam

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitDraftSave answers a draft write: the draft's new tree and ETag, the
// files this write changed (vs the draft before it), and the problems that
// now block publishing.
type KitDraftSave struct {
	Kit      string        `json:"kit"`
	Tree     string        `json:"tree"`
	ETag     string        `json:"etag"`
	Changed  []DraftChange `json:"changed"`
	Problems []string      `json:"problems"`
}

// draftPathErr is CheckPath's refusal located at the draft; it never names
// the path.
func draftPathErr(name string, err error) error {
	return writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
}

// draftFileTooLarge is the 413 for a file body over kittree.MaxFile; p has
// passed CheckPath.
func draftFileTooLarge(name, p string) error {
	return writeErr(http.StatusRequestEntityTooLarge, "draft %s: %s", name, kittree.FileTooLarge(p))
}

// lockedDraftTree is kit name's draft, its ETag checked against ifMatch, and
// its tree. Caller holds kitMu.
func lockedDraftTree(store Store, name, ifMatch string) (KitDraft, kittree.Tree, error) {
	d, err := draftOf(store, name)
	if err != nil {
		return KitDraft{}, kittree.Tree{}, err
	}
	if err := checkDraftETag(d, ifMatch); err != nil {
		return KitDraft{}, kittree.Tree{}, err
	}
	t, ok := store.KitTree(d.Tree)
	if !ok {
		return KitDraft{}, kittree.Tree{}, fmt.Errorf("kit %s draft: tree %.12s is not stored", name, d.Tree)
	}
	return d, t, nil
}

// saveDraftLocked records next as d's tree (objs carrying it and its new
// blobs) for actor at now, collects what only the old tree held, and answers
// the save. An unchanged tree writes nothing. Caller holds kitMu and has
// checked d's ETag (lockedDraftTree); prev is d's current tree.
func saveDraftLocked(store Store, log *slog.Logger, d KitDraft, prev, next kittree.Tree, objs KitObjects, actor string, now time.Time) (KitDraftSave, error) {
	hash := next.Hash()
	if hash != d.Tree {
		d.Tree, d.UpdatedAt, d.Actor = hash, now, actor
		if err := store.PutKitDraft(d, objs); err != nil {
			return KitDraftSave{}, err
		}
		gcKitObjects(store, log)
	}
	_, problems := draftProblems(store, d.Kit, next)
	return KitDraftSave{Kit: d.Kit, Tree: hash, ETag: DraftETag(hash), Changed: draftChanges(prev, next), Problems: problems}, nil
}

// freshBlobs looks a blob up among a write's new blobs, then in the store.
func freshBlobs(store Store, fresh map[string][]byte) kittree.Blobs {
	return func(sha string) ([]byte, bool) {
		if b, ok := fresh[sha]; ok {
			return b, true
		}
		return store.KitBlob(sha)
	}
}

// KitDraftFile is file p of kit name's draft: its bytes, its entry and the
// draft's ETag, read under kitMu.
func KitDraftFile(store Store, name, p string) ([]byte, kittree.Entry, string, error) {
	if !studio.TagSafeName(name) {
		return nil, kittree.Entry{}, "", unsafeKitName()
	}
	if err := kittree.CheckPath(p); err != nil {
		return nil, kittree.Entry{}, "", draftPathErr(name, err)
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	d, err := draftOf(store, name)
	if err != nil {
		return nil, kittree.Entry{}, "", err
	}
	t, ok := store.KitTree(d.Tree)
	if !ok {
		return nil, kittree.Entry{}, "", fmt.Errorf("kit %s draft: tree %.12s is not stored", name, d.Tree)
	}
	e, ok := t.Lookup(p)
	if !ok {
		return nil, kittree.Entry{}, "", writeErr(http.StatusNotFound, "draft %s has no file %s", name, p)
	}
	b, ok := store.KitBlob(e.SHA256)
	if !ok {
		return nil, kittree.Entry{}, "", fmt.Errorf("kit %s draft: %s has no stored content", name, p)
	}
	return b, e, DraftETag(d.Tree), nil
}

// PutKitDraftFile writes file p (data, mode 0644 or 0755; 0 keeps the
// file's mode, else 0644) into kit name's draft, under kitMu and against
// ifMatch. The draft's .dockerignore applies: p excluded refuses, and a
// .dockerignore drops the files it newly excludes. The file rules hold
// (kittree.Tree.ValidateFiles); kit.yml may be missing or invalid.
func PutKitDraftFile(store Store, log *slog.Logger, name, p string, mode fs.FileMode, data []byte, ifMatch, actor string, now time.Time) (KitDraftSave, error) {
	if !studio.TagSafeName(name) {
		return KitDraftSave{}, unsafeKitName()
	}
	if err := kittree.CheckPath(p); err != nil {
		return KitDraftSave{}, draftPathErr(name, err)
	}
	if mode != 0 && mode != 0o644 && mode != 0o755 {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: mode must be 0644 or 0755", name)
	}
	if int64(len(data)) > kittree.MaxFile {
		return KitDraftSave{}, draftFileTooLarge(name, p)
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	d, cur, err := lockedDraftTree(store, name, ifMatch)
	if err != nil {
		return KitDraftSave{}, err
	}
	if mode == 0 {
		mode = 0o644
		if e, ok := cur.Lookup(p); ok {
			mode = e.Mode
		}
	}
	one, blobs, err := kittree.BuildFiles([]kittree.File{{Path: p, Mode: mode, Data: data}})
	if err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	next, err := kittree.IgnoreTree(cur.With(one.Entries[0]), freshBlobs(store, blobs))
	if err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	if _, kept := next.Lookup(p); !kept {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s: excluded by .dockerignore", name, p)
	}
	if err := next.ValidateFiles(); err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	return saveDraftLocked(store, log, d, cur, next, KitObjects{Trees: []kittree.Tree{next}, Blobs: blobs}, actor, now)
}

// RemoveKitDraftFile removes file p from kit name's draft, under kitMu and
// against ifMatch: 404 when the draft has no such file.
func RemoveKitDraftFile(store Store, log *slog.Logger, name, p, ifMatch, actor string, now time.Time) (KitDraftSave, error) {
	if !studio.TagSafeName(name) {
		return KitDraftSave{}, unsafeKitName()
	}
	if err := kittree.CheckPath(p); err != nil {
		return KitDraftSave{}, draftPathErr(name, err)
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	d, cur, err := lockedDraftTree(store, name, ifMatch)
	if err != nil {
		return KitDraftSave{}, err
	}
	if _, ok := cur.Lookup(p); !ok {
		return KitDraftSave{}, writeErr(http.StatusNotFound, "draft %s has no file %s", name, p)
	}
	next := cur.Without(p)
	return saveDraftLocked(store, log, d, cur, next, KitObjects{Trees: []kittree.Tree{next}}, actor, now)
}
```

- [ ] **Step 4: Implement the routes**

Append the three handlers inside `registerKitDrafts` (after the DELETE draft handler), and add the helpers. Add the imports `"fmt"`, `"io/fs"` and `"github.com/aethons-tools/cove/internal/kittree"` to `kit_draft_admin.go`.

```go
	// One file of the draft. {path...} is the file's path, percent-decoded;
	// it is CheckPath'd before any text names it.
	mux.HandleFunc("GET /admin/kits/{name}/draft/files/{path...}", func(w http.ResponseWriter, r *http.Request) {
		data, e, etag, err := KitDraftFile(store, r.PathValue("name"), r.PathValue("path"))
		if err != nil {
			draftFail(w, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("ETag", etag)
		h.Set("X-Kit-File-Mode", fmt.Sprintf("%04o", e.Mode))
		w.Write(data)
	})
	mux.HandleFunc("PUT /admin/kits/{name}/draft/files/{path...}", func(w http.ResponseWriter, r *http.Request) {
		name, p := r.PathValue("name"), r.PathValue("path")
		if !requireContentType(w, r, "application/octet-stream") || !draftPrecheck(w, r, store, name) {
			return
		}
		if err := kittree.CheckPath(p); err != nil {
			draftFail(w, draftPathErr(name, err))
			return
		}
		mode, err := draftMode(name, r.URL.Query().Get("mode"))
		if err != nil {
			draftFail(w, err)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, kittree.MaxFile))
		if err != nil {
			if _, big := errors.AsType[*http.MaxBytesError](err); big {
				draftFail(w, draftFileTooLarge(name, p))
				return
			}
			http.Error(w, "kit draft: the body could not be read", http.StatusBadRequest)
			return
		}
		s, err := PutKitDraftFile(store, log, name, p, mode, data, r.Header.Get("If-Match"), OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFail(w, err)
			return
		}
		logDraftSaved(log, r, name, s)
		writeDraftSave(w, s)
	})
	mux.HandleFunc("DELETE /admin/kits/{name}/draft/files/{path...}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		s, err := RemoveKitDraftFile(store, log, name, r.PathValue("path"), r.Header.Get("If-Match"), OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFail(w, err)
			return
		}
		logDraftSaved(log, r, name, s)
		writeDraftSave(w, s)
	})
```

```go
// draftPrecheck answers 400/404/428/412 before a write's body is read, so a
// stale upload is refused without reading it; the write re-checks under kitMu.
func draftPrecheck(w http.ResponseWriter, r *http.Request, store Store, name string) bool {
	d, err := draftOf(store, name)
	if err == nil {
		err = checkDraftETag(d, r.Header.Get("If-Match"))
	}
	if err != nil {
		draftFail(w, err)
		return false
	}
	return true
}

// draftMode reads ?mode=: "" (keep), 644/0644 or 755/0755.
func draftMode(name, q string) (fs.FileMode, error) {
	switch q {
	case "":
		return 0, nil
	case "644", "0644":
		return 0o644, nil
	case "755", "0755":
		return 0o755, nil
	}
	return 0, writeErr(http.StatusBadRequest, "draft %s: mode must be 0644 or 0755", name)
}

// writeDraftSave answers a draft write with its new ETag.
func writeDraftSave(w http.ResponseWriter, s KitDraftSave) {
	w.Header().Set("ETag", s.ETag)
	writeJSON(w, http.StatusOK, s)
}

// logDraftSaved is a draft write's audit line: never content or a path.
func logDraftSaved(log *slog.Logger, r *http.Request, name string, s KitDraftSave) {
	log.Info("kit draft saved", "operator", OperatorID(r), "kit", name, "tree", shortTree(s.Tree), "changed", len(s.Changed))
}
```

Update the route list in `registerKitDrafts`'s doc comment with:

```
//	GET    /admin/kits/{name}/draft/files/{path...}          one file's bytes
//	PUT    /admin/kits/{name}/draft/files/{path...}[?mode=]  write one file (octet-stream, If-Match)
//	DELETE /admin/kits/{name}/draft/files/{path...}          remove one file (If-Match)
```

- [ ] **Step 5: Document the writes**

In `docs/usage/jam/kits-drafts.md`, in "## The draft", insert this bullet before the `kit.yml may be missing` bullet (write the <link> as a real link):

```markdown
- Every write is checked like a push: safe paths, modes `0644`/`0755`, and
  the <link `kits-directory.md#limits` as "limits">. The draft's own
  `.dockerignore` applies too: a file it excludes is refused
  (`draft <kit>: <path>: excluded by .dockerignore`), and saving a
  `.dockerignore` drops the files it now excludes.
```

Append to the API table:

```markdown
| `GET /admin/kits/{name}/draft/files/{path}` | One file's bytes (`application/octet-stream`), with `ETag` (the draft's) and `X-Kit-File-Mode`. |
| `PUT /admin/kits/{name}/draft/files/{path}[?mode=755]` | Write one file (`If-Match`; `application/octet-stream`, else 415; over 4 MiB 413). Without `mode` the file keeps its mode, a new one gets `0644`. |
| `DELETE /admin/kits/{name}/draft/files/{path}` | Remove one file (`If-Match`). |
```

After the table, add: ``A write answers the new `etag` (also the `ETag` header), the files it `changed` and the `problems`. A path is percent-encoded per segment; one with a control or format character is refused without being echoed.``

- [ ] **Step 6: Run the tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test -race ./internal/jam/ -run 'Draft|KitDraft' && GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS, with no race reported and no more than 741 audit errors.

- [ ] **Step 7: Lint and commit**

```bash
gofmt -l internal/jam/
just lint; echo "lint exit=$?"
git add internal/jam/ docs/usage/jam/kits-drafts.md
git commit -m "feat(jam): read, write and remove kit draft files under the draft ETag

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Replace the whole draft tree (the sync upload)

**Files:**
- Modify: `internal/jam/kit_draft_files.go` (`ReplaceKitDraft`), `internal/jam/kit_draft_admin.go` (`PUT /admin/kits/{name}/draft`), `docs/usage/jam/kits-drafts.md`
- Test: `internal/jam/kit_draft_files_test.go`, `internal/jam/kit_draft_admin_test.go`

**Interfaces:**
- Consumes: `kittree.Ignore`, `kittree.BuildFiles`, `kittree.ReadTar`, `kittree.MaxUpload`, `kittree.WriteTar`; Task 4's `lockedDraftTree`, `saveDraftLocked`, `draftPrecheck`, `writeDraftSave`, `logDraftSaved`, `withDraftLimits`, `openApp`.
- Produces:
```go
func ReplaceKitDraft(store Store, log *slog.Logger, name string, files []kittree.File, ifMatch, actor string, now time.Time) (KitDraftSave, error)
// route: PUT /admin/kits/{name}/draft (application/x-tar, If-Match) → 200 KitDraftSave
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/jam/kit_draft_files_test.go`, adding `"github.com/aethons-tools/cove/internal/kittree/kittreetest"` to its imports:

```go
func TestReplaceKitDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "old.sh", "old\n")
	tag := openApp(t, st)
	files := kittreetest.Files("kit.yml", "kind: studio\nprompt: synced\n", "run.sh*", "echo\n", ".dockerignore", "*.log\n", "debug.log", "x")
	s, err := ReplaceKitDraft(st, nil, "app", files, tag, "local", draftNow)
	want := []DraftChange{{".dockerignore", "added"}, {"kit.yml", "modified"}, {"old.sh", "removed"}, {"run.sh", "added"}}
	if err != nil || !slices.Equal(s.Changed, want) || len(s.Problems) != 0 {
		t.Fatalf("replace = %+v %v, want %+v", s, err, want)
	}
	if _, _, _, err := KitDraftFile(st, "app", "debug.log"); err == nil {
		t.Fatal(".dockerignore must filter the sync")
	}
	same, err := ReplaceKitDraft(st, nil, "app", files, s.ETag, "local", draftNow)
	if err != nil || same.ETag != s.ETag || len(same.Changed) != 0 {
		t.Fatalf("same files = %+v %v (unchanged)", same, err)
	}
	// Stale or missing If-Match (Review Focus 1), nothing written.
	_, err = ReplaceKitDraft(st, nil, "app", kittreetest.Files("kit.yml", "kind: studio\n"), tag, "local", draftNow)
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	_, err = ReplaceKitDraft(st, nil, "app", kittreetest.Files("kit.yml", "kind: studio\n"), "", "local", draftNow)
	wantErr(t, err, "draft app: If-Match is required (send the etag from the draft status)", 428)
	// No kit.yml is a problem, not a refusal.
	s, err = ReplaceKitDraft(st, nil, "app", kittreetest.Files("notes.md", "n\n"), s.ETag, "local", draftNow)
	if err != nil || !slices.Equal(s.Problems, []string{"kit.yml missing at the root"}) {
		t.Fatalf("no kit.yml = %+v %v", s, err)
	}
	// The file rules hold.
	_, err = ReplaceKitDraft(st, nil, "app", []kittree.File{{Path: "a\nb"}}, s.ETag, "local", draftNow)
	wantErr(t, err, unsafeDraftPath, 400)
	withDraftLimits(t, 1, kittree.MaxTotal, kittree.MaxFile)
	_, err = ReplaceKitDraft(st, nil, "app", kittreetest.Files("a", "1", "b", "2"), s.ETag, "local", draftNow)
	wantErr(t, err, "draft app: 2 files (limit 1)", 400)
}
```

Append to `internal/jam/kit_draft_admin_test.go`, adding `"archive/tar"` to its imports:

```go
func TestReplaceDraftRoute(t *testing.T) {
	h, st := newTestAdmin(t)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil).Header().Get("ETag")
	const xtar = "application/x-tar"
	// A hand-made `tar -C dir -cf - .`: "./" prefixes and directory entries.
	var hand bytes.Buffer
	tw := tar.NewWriter(&hand)
	for _, e := range []struct {
		name string
		flag byte
		mode int64
		data string
	}{{"./", tar.TypeDir, 0o755, ""}, {"./kit.yml", tar.TypeReg, 0o644, "kind: studio\n"}, {"./bin/", tar.TypeDir, 0o755, ""}, {"./bin/run", tar.TypeReg, 0o700, "echo\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: e.flag, Mode: e.mode, Size: int64(len(e.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", "text/plain", tag, hand.Bytes()); w.Code != http.StatusUnsupportedMediaType || body(w) != "kit draft: Content-Type must be application/x-tar" {
		t.Fatalf("Content-Type = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, "", hand.Bytes()); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match = %d", w.Code)
	}
	w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, tag, hand.Bytes())
	var s KitDraftSave
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &s) != nil || w.Header().Get("ETag") != s.ETag || !slices.Equal(s.Changed, []DraftChange{{"bin/run", "added"}}) {
		t.Fatalf("replace = %d %s", w.Code, w.Body)
	}
	if g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/bin/run", "", "", nil); g.Header().Get("X-Kit-File-Mode") != "0755" {
		t.Fatalf("mode = %q", g.Header().Get("X-Kit-File-Mode"))
	}
	// Hostile tars (Review Focus 4): a symlink, an escape-character name, garbage.
	var link bytes.Buffer
	lw := tar.NewWriter(&link)
	if err := lw.WriteHeader(&tar.Header{Name: "leak", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}); err != nil {
		t.Fatal(err)
	}
	lw.Close()
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, s.ETag, link.Bytes()); w.Code != http.StatusBadRequest || body(w) != "draft app: leak: symlinks are not allowed" {
		t.Fatalf("symlink = %d %s", w.Code, w.Body)
	}
	var esc bytes.Buffer
	if err := kittree.WriteTar(&esc, []kittree.File{{Path: "a\x1b[31mb", Data: []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, s.ETag, esc.Bytes()); w.Code != http.StatusBadRequest || body(w) != unsafeDraftPath {
		t.Fatalf("escape name = %d %q", w.Code, w.Body)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, s.ETag, []byte("garbage, not a tar, garbage, not a tar")); w.Code != http.StatusBadRequest || body(w) != "draft app: not a readable tar" {
		t.Fatalf("garbage = %d %s", w.Code, w.Body)
	}
	// The body cap (MaxUpload) answers 413, as push does.
	withDraftLimits(t, 1, 1024, 8<<20)
	var big bytes.Buffer
	if err := kittree.WriteTar(&big, []kittree.File{{Path: "kit.yml", Data: make([]byte, 2<<20)}}); err != nil {
		t.Fatal(err)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, s.ETag, big.Bytes()); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d %s", w.Code, w.Body)
	}
	if d, _ := st.GetKitDraft("app"); DraftETag(d.Tree) != s.ETag {
		t.Fatal("a refused replace changed the draft")
	}
}
```

Add `"slices"` to that file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'ReplaceKitDraft|ReplaceDraftRoute'`
Expected: FAIL to compile, `undefined: ReplaceKitDraft`.

- [ ] **Step 3: Implement**

Append to `internal/jam/kit_draft_files.go`:

```go
// ReplaceKitDraft replaces kit name's draft tree with files (a sync): their
// root .dockerignore applied as on a push, the file rules held, kit.yml
// optional. The input is checked before kitMu is taken; the ETag under it.
func ReplaceKitDraft(store Store, log *slog.Logger, name string, files []kittree.File, ifMatch, actor string, now time.Time) (KitDraftSave, error) {
	if !studio.TagSafeName(name) {
		return KitDraftSave{}, unsafeKitName()
	}
	files, err := kittree.Ignore(files)
	if err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	next, blobs, err := kittree.BuildFiles(files)
	if err != nil {
		return KitDraftSave{}, writeErr(http.StatusBadRequest, "draft %s: %s", name, err)
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	d, cur, err := lockedDraftTree(store, name, ifMatch)
	if err != nil {
		return KitDraftSave{}, err
	}
	return saveDraftLocked(store, log, d, cur, next, KitObjects{Trees: []kittree.Tree{next}, Blobs: blobs}, actor, now)
}
```

In `registerKitDrafts`, after the DELETE draft handler:

```go
	// The whole tree, replaced by an uncompressed tar of files (kit draft
	// sync): read within the tree limits as it streams, the body capped at
	// kittree.MaxUpload (413 past it), as POST …/versions reads a push.
	mux.HandleFunc("PUT /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !requireContentType(w, r, "application/x-tar") || !draftPrecheck(w, r, store, name) {
			return
		}
		files, err := kittree.ReadTar(http.MaxBytesReader(w, r.Body, kittree.MaxUpload()))
		if err != nil {
			if _, big := errors.AsType[*http.MaxBytesError](err); big {
				http.Error(w, fmt.Sprintf("kit upload over %d bytes", kittree.MaxUpload()), http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, fmt.Sprintf("draft %s: %s", name, err), http.StatusBadRequest)
			return
		}
		s, err := ReplaceKitDraft(store, log, name, files, r.Header.Get("If-Match"), OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFail(w, err)
			return
		}
		logDraftSaved(log, r, name, s)
		writeDraftSave(w, s)
	})
```

Add `//	PUT    /admin/kits/{name}/draft   replace the tree (x-tar, If-Match): kit draft sync` to the route list in the doc comment.

- [ ] **Step 4: Document it**

Append to the API table in `docs/usage/jam/kits-drafts.md`:

```markdown
| `PUT /admin/kits/{name}/draft` | Replace the whole tree with an uncompressed tar (`If-Match`; `application/x-tar`, else 415), read as a push upload is (a leading `./` dropped, directories skipped, links refused, the body capped, 413) and filtered by its `.dockerignore`. `kit draft sync` sends it. |
```

- [ ] **Step 5: Run the tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS, with no more than 741 audit errors.

- [ ] **Step 6: Lint and commit**

```bash
gofmt -l internal/jam/
just lint; echo "lint exit=$?"
git add internal/jam/ docs/usage/jam/kits-drafts.md
git commit -m "feat(jam): replace a kit draft's whole tree from a tar (the sync upload)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Publish (origin, `--pin`, drift)

**Files:**
- Create: `internal/jam/kit_draft_publish.go`
- Modify: `internal/jam/identity.go` (`KitVersionMeta.Origin`), `internal/jam/kit_push.go` (`origin` parameter on `pushKitTreeLocked`; `replaceKitYMLLocked`), `internal/jam/kitsource_admin.go` (`KitVersionInfo.Origin`), `internal/jam/admin.go` (history sets `Origin`), `internal/jam/kit_draft_admin.go` (publish route), `docs/usage/jam/kits-drafts.md`
- Test: `internal/jam/kit_draft_publish_test.go`

**Interfaces:**
- Consumes: Task 3 (`lockedDraftTree` from Task 4, `draftProblems`, `kitManaged`, `DraftDriftWarning`, `requireContentType`, `decodeOptional`, `draftFail`); `pushKitTreeLocked`, `KitsReferencing`, `LoadKitVersion`, `studio.SetBaseKit`, `gcKitObjects`.
- Produces:
```go
const KitOriginDraft = "draft"
// KitVersionMeta gains Origin string `json:"origin,omitempty"`; KitVersionInfo gains Origin string `json:"origin,omitempty"`
type KitDraftPublishBody struct { Pin bool `json:"pin,omitempty"` }
type KitDraftPublish struct { Kit string; Version, Base int; Pinned []string; Drift bool; Warning string }
func PublishKitDraft(store Store, log *slog.Logger, name string, pin bool, ifMatch string) (KitDraftPublish, error)
func pinFollowersLocked(store Store, name string, version int) ([]KitRef, error)
func pushKitTreeLocked(store Store, name string, tree kittree.Tree, objs KitObjects, sk studio.StudioKit, origin string) (KitRef, bool, error) // signature change
func replaceKitYMLLocked(store Store, name, config string) (KitRef, bool, error)
// route: POST /admin/kits/{name}/draft/publish (JSON {pin}, If-Match) → 200 KitDraftPublish
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/jam/kit_draft_publish_test.go
package jam

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestPublishKitDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := openApp(t, st)
	s, err := PutKitDraftFile(st, nil, "app", "run.sh", 0o755, []byte("echo\n"), tag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	res, err := PublishKitDraft(st, nil, "app", false, s.ETag)
	if err != nil || res.Kit != "app" || res.Version != 2 || res.Base != 1 || res.Drift || res.Warning != "" || len(res.Pinned) != 0 {
		t.Fatalf("publish = %+v %v", res, err)
	}
	k, _ := st.GetKit("app")
	if k.Current != 2 || k.Trees[2] != s.Tree || k.Meta[2].Origin != KitOriginDraft || k.Meta[1].Origin != "" {
		t.Fatalf("kit = %+v", k)
	}
	if _, ok := st.GetKitDraft("app"); ok {
		t.Fatal("publish left the draft open")
	}
	if v, _ := LoadKitVersion(st, "app", 2); v.Err != nil {
		t.Fatalf("v2 = %v", v.Err)
	}
}

func TestPublishRefusals(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := openApp(t, st)
	_, err := PublishKitDraft(st, nil, "app", false, tag)
	wantErr(t, err, "draft app has no changes", 409)
	if _, ok := st.GetKitDraft("app"); !ok {
		t.Fatal("a refused publish closed the draft")
	}
	s, err := PutKitDraftFile(st, nil, "app", "kit.yml", 0, []byte("kind: nope\n"), tag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PublishKitDraft(st, nil, "app", false, s.ETag)
	wantErr(t, err, `draft app has problems: studio kit: kind must be "studio", got "nope"`, 400)
	_, err = PublishKitDraft(st, nil, "app", false, tag) // Review Focus 1: stale
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	_, err = PublishKitDraft(st, nil, "app", false, "")
	wantErr(t, err, "draft app: If-Match is required (send the etag from the draft status)", 428)
	if k, _ := st.GetKit("app"); k.Current != 1 || len(k.VersionNumbers()) != 1 {
		t.Fatal("a refused publish made a version")
	}
}

// TestPublishAfterBaseSuperseded: a push lands while the draft is open; the
// draft publishes as it is, nothing merged (Review Focus 2).
func TestPublishAfterBaseSuperseded(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := openApp(t, st)
	s, err := PutKitDraftFile(st, nil, "app", "draft.txt", 0, []byte("d\n"), tag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	pushApp(t, st, "kit.yml", "kind: studio\n", "pushed.txt", "p\n") // v2
	status, err := KitDraftStatusOf(st, "app")
	if err != nil || status.Base != 1 || status.Current != 2 || !slices.Equal(status.Changes, []DraftChange{{"draft.txt", "added"}}) {
		t.Fatalf("status = %+v %v (changes are vs the base)", status, err)
	}
	res, err := PublishKitDraft(st, nil, "app", false, s.ETag)
	if err != nil || res.Version != 3 || res.Base != 1 {
		t.Fatalf("publish = %+v %v", res, err)
	}
	v3, _ := LoadKitVersion(st, "app", 3)
	if _, ok := v3.Tree.Lookup("draft.txt"); !ok {
		t.Fatal("v3 lacks the draft's file")
	}
	if _, ok := v3.Tree.Lookup("pushed.txt"); ok {
		t.Fatal("publish merged v2's file")
	}
	if v2, _ := LoadKitVersion(st, "app", 2); v2.Err != nil {
		t.Fatal("v2 must stay in history")
	} else if _, ok := v2.Tree.Lookup("pushed.txt"); !ok {
		t.Fatal("v2 changed")
	}
}

func TestPublishPinsFollowers(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	for name, yml := range map[string]string{"web": "kind: studio\nbase:\n  kit: app # follows\n", "ops": "kind: studio\nbase:\n  kit: app@v1\n"} {
		if _, _, err := PushKitFiles(st, name, kittreetest.Files("kit.yml", yml)); err != nil {
			t.Fatal(err)
		}
	}
	tag := openApp(t, st)
	s, err := PutKitDraftFile(st, nil, "app", "kit.yml", 0, []byte("kind: studio\nprompt: new\n"), tag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	res, err := PublishKitDraft(st, nil, "app", true, s.ETag)
	if err != nil || res.Version != 2 || !slices.Equal(res.Pinned, []string{"web v2 (app@v1)"}) {
		t.Fatalf("publish --pin = %+v %v", res, err)
	}
	web, _ := LoadKitVersion(st, "web", 0)
	if web.Version != 2 || string(web.KitYML) != "kind: studio\nbase:\n  kit: app@v1 # follows\n" {
		t.Fatalf("web = v%d %q (base.kit rewritten in place)", web.Version, web.KitYML)
	}
	if ops, _ := st.GetKit("ops"); ops.Current != 1 {
		t.Fatal("an already pinned kit was rewritten")
	}
	follow, pinned := KitsReferencing(st, "app")
	if len(follow) != 0 || !slices.Equal(pinned, []string{"ops", "web"}) {
		t.Fatalf("follow %v pinned %v", follow, pinned)
	}
	// Without --pin a follower keeps following (and goes stale as usual).
	if _, _, err := PushKitFiles(st, "dev", kittreetest.Files("kit.yml", "kind: studio\nbase:\n  kit: app\n")); err != nil {
		t.Fatal(err)
	}
	tag = openApp(t, st)
	s, _ = PutKitDraftFile(st, nil, "app", "kit.yml", 0, []byte("kind: studio\nprompt: newer\n"), tag, "local", draftNow)
	if res, err := PublishKitDraft(st, nil, "app", false, s.ETag); err != nil || len(res.Pinned) != 0 {
		t.Fatalf("publish = %+v %v", res, err)
	}
	if follow, _ := KitsReferencing(st, "app"); !slices.Equal(follow, []string{"dev"}) {
		t.Fatalf("follow = %v", follow)
	}
}

func TestPublishManagedKitDrifts(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "abc1234", Digest: "d", AppliedAt: time.Unix(1, 0).UTC()}}}); err != nil {
		t.Fatal(err)
	}
	tag := openApp(t, st)
	s, _ := PutKitDraftFile(st, nil, "app", "notes.md", 0, []byte("n\n"), tag, "local", draftNow)
	res, err := PublishKitDraft(st, nil, "app", false, s.ETag)
	if err != nil || res.Version != 2 || !res.Drift || res.Warning != "this will drift from the config repo; config write it to keep the repo in sync" {
		t.Fatalf("publish = %+v %v", res, err)
	}
}

func TestPublishRoute(t *testing.T) {
	var logs bytes.Buffer
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	open := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil)
	put := draftDo(t, h, "PUT", "/admin/kits/app/draft/files/run.sh", "application/octet-stream", open.Header().Get("ETag"), []byte("echo SECRET\n"))
	tag := put.Header().Get("ETag")
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft/publish", "text/plain", tag, []byte(`{}`)); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d", w.Code)
	}
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft/publish", "application/json", "", []byte(`{}`)); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match = %d", w.Code)
	}
	w := draftDo(t, h, "POST", "/admin/kits/app/draft/publish", "application/json", tag, []byte(`{"pin":false}`))
	var res KitDraftPublish
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Version != 2 {
		t.Fatalf("publish = %d %s", w.Code, w.Body)
	}
	var hist []KitVersionInfo
	getJSON(t, h, "/admin/kits/app/history", &hist)
	if len(hist) != 2 || hist[0].Origin != "" || hist[1].Origin != "draft" {
		t.Fatalf("history = %+v", hist)
	}
	out := logs.String()
	if !strings.Contains(out, "kit draft published") || !strings.Contains(out, "version=2") || strings.Contains(out, "SECRET") {
		t.Fatalf("audit log:\n%s", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'Publish'`
Expected: FAIL to compile, `undefined: PublishKitDraft`.

- [ ] **Step 3: Thread `origin` through the push path**

In `internal/jam/identity.go`, add to `KitVersionMeta`:

```go
	Origin   string        `json:"origin,omitempty"` // "" = pushed or imported (Source says which); "draft" = published from a draft (KitOriginDraft)
```

In `internal/jam/kitsource_admin.go`, add to `KitVersionInfo` after `Source`: `Origin string \`json:"origin,omitempty"\` // "draft" for a version published from a draft`. In `admin.go`'s history handler, set `Origin: m.Origin` in the `KitVersionInfo` literal.

In `internal/jam/kit_push.go`:
- Give `pushKitTreeLocked` a trailing `origin string` parameter, and build the meta as `KitVersionMeta{PushedAt: time.Now().UTC(), Parent: chainParent(chain), Origin: origin}`. Its doc comment gains: `origin is the version's KitVersionMeta.Origin ("" for a push).`
- `PushKitFiles` passes `""`.
- Split `ReplaceKitYML`:

```go
func ReplaceKitYML(store Store, name, config string) (KitRef, bool, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	return replaceKitYMLLocked(store, name, config)
}

// replaceKitYMLLocked is ReplaceKitYML past the lock; the caller holds kitMu.
func replaceKitYMLLocked(store Store, name, config string) (KitRef, bool, error) {
	out := []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: []byte(config)}}
	if cur, ok := LoadKitVersion(store, name, 0); ok && cur.TreeHash != "" {
		for _, e := range cur.Tree.Entries {
			if e.Path == kittree.KitFile {
				out[0].Mode = e.Mode
				continue
			}
			b, ok := store.KitBlob(e.SHA256)
			if !ok {
				return KitRef{}, false, fmt.Errorf("kit %s: %s has no stored content", name, e.Path)
			}
			out = append(out, kittree.File{Path: e.Path, Mode: e.Mode, Data: b})
		}
	}
	tree, objs, sk, err := kitTreeOf(name, out)
	if err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	return pushKitTreeLocked(store, name, tree, objs, sk, "")
}
```

Run `grep -rn 'pushKitTreeLocked(' internal/` and check that every call passes the new argument.

- [ ] **Step 4: Implement publish**

```go
// internal/jam/kit_draft_publish.go
package jam

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitOriginDraft is the KitVersionMeta.Origin of a version published from a draft.
const KitOriginDraft = "draft"

// KitDraftPublishBody is the POST /admin/kits/{name}/draft/publish request.
type KitDraftPublishBody struct {
	Pin bool `json:"pin,omitempty"`
}

// KitDraftPublish answers a publish: the new version, the draft's base, the
// followers pinned ("web v5 (app@v3)"), and, for a config-managed kit (or a
// managed follower pinned), Drift with DraftDriftWarning.
type KitDraftPublish struct {
	Kit     string   `json:"kit"`
	Version int      `json:"version"`
	Base    int      `json:"base"`
	Pinned  []string `json:"pinned,omitempty"`
	Drift   bool     `json:"drift,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

// PublishKitDraft makes kit name's draft tree its next version (Origin
// "draft") and closes the draft, under kitMu. The draft's ETag (ifMatch) is
// checked; its tree must have no problems (400) and differ from current's
// (409, the draft kept). A draft based on an older version than current is
// published as it is: nothing is merged. With pin, every kit following name
// is first pinned to the version it follows now (pinFollowersLocked);
// without, followers go stale as after any push.
func PublishKitDraft(store Store, log *slog.Logger, name string, pin bool, ifMatch string) (KitDraftPublish, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	d, tree, err := lockedDraftTree(store, name, ifMatch)
	if err != nil {
		return KitDraftPublish{}, err
	}
	sk, problems := draftProblems(store, name, tree)
	if len(problems) > 0 {
		return KitDraftPublish{}, writeErr(http.StatusBadRequest, "draft %s has problems: %s", name, strings.Join(problems, "; "))
	}
	k, _ := store.GetKit(name)
	if k.Trees[k.Current] == d.Tree {
		return KitDraftPublish{}, writeErr(http.StatusConflict, "draft %s has no changes", name)
	}
	res := KitDraftPublish{Kit: name, Base: d.Base, Drift: kitManaged(store, name)}
	if pin {
		refs, err := pinFollowersLocked(store, name, k.Current)
		if err != nil {
			return KitDraftPublish{}, err
		}
		for _, r := range refs {
			res.Pinned = append(res.Pinned, fmt.Sprintf("%s v%d (%s@v%d)", r.ID, r.Version, name, k.Current))
			res.Drift = res.Drift || kitManaged(store, r.ID)
		}
	}
	ref, _, err := pushKitTreeLocked(store, name, tree, KitObjects{Trees: []kittree.Tree{tree}}, sk, KitOriginDraft)
	if err != nil {
		return KitDraftPublish{}, err
	}
	res.Version = ref.Version
	if err := store.RemoveKitDraft(name); err != nil {
		return res, fmt.Errorf("kit %s: published v%d, but the draft could not be closed: %w", name, ref.Version, err)
	}
	gcKitObjects(store, log)
	if res.Drift {
		res.Warning = DraftDriftWarning
	}
	return res, nil
}

// pinFollowersLocked gives each kit that follows name (an unpinned base.kit)
// a new version whose base.kit is rewritten in place to name@v<version>, the
// version it follows now (studio.SetBaseKit keeps every other byte). Every
// rewrite is prepared before any is pushed, so a follower whose base.kit
// cannot be rewritten refuses with nothing written. Caller holds kitMu.
func pinFollowersLocked(store Store, name string, version int) ([]KitRef, error) {
	follow, _ := KitsReferencing(store, name)
	ref := fmt.Sprintf("%s@v%d", name, version)
	ymls := make([]string, len(follow))
	for i, f := range follow {
		cur, ok := LoadKitVersion(store, f, 0)
		if !ok || cur.TreeHash == "" {
			return nil, writeErr(http.StatusConflict, "kit %s follows %s but is not stored as a tree; pin it by hand", f, name)
		}
		yml, err := studio.SetBaseKit(cur.KitYML, ref)
		if err != nil {
			return nil, writeErr(http.StatusConflict, "kit %s: cannot pin its base.kit to %s (%s)", f, ref, err)
		}
		ymls[i] = string(yml)
	}
	out := make([]KitRef, 0, len(follow))
	for i, f := range follow {
		r, _, err := replaceKitYMLLocked(store, f, ymls[i])
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}
```

In `registerKitDrafts`, add (and `//	POST   /admin/kits/{name}/draft/publish  {pin?}  publish (If-Match)` to its doc list):

```go
	mux.HandleFunc("POST /admin/kits/{name}/draft/publish", func(w http.ResponseWriter, r *http.Request) {
		if !requireContentType(w, r, "application/json") {
			return
		}
		var b KitDraftPublishBody
		if !decodeOptional(w, r, &b) {
			return
		}
		name := r.PathValue("name")
		res, err := PublishKitDraft(store, log, name, b.Pin, r.Header.Get("If-Match"))
		if err != nil {
			draftFail(w, err)
			return
		}
		log.Info("kit draft published", "operator", OperatorID(r), "kit", name, "version", res.Version, "base", res.Base, "pinned", len(res.Pinned), "drift", res.Drift)
		writeJSON(w, http.StatusOK, res)
	})
```

- [ ] **Step 5: Document publish**

In `docs/usage/jam/kits-drafts.md`, add this bullet at the end of "## The draft":

```markdown
- Another version may land while a draft is open: the status then shows
  `base: 1, current: 2`. Publishing still makes the draft's tree the next
  version, without the other version's changes (no merge; it stays in the
  history).
```

Insert this section between "## Concurrency" and "## API":

```markdown
## Publish

Publish checks the draft as a push would, records its tree as the kit's next
version (origin `draft`, the SOURCE column of `kit versions`) and closes the
draft.

- No change from *current*: 409 `draft <kit> has no changes`; the draft is kept.
- Problems: 400 `draft <kit> has problems: …`.
- Kits that follow this one go stale, as after any push. With `--pin`
  (`{"pin": true}`), each follower first gets a new version whose `base.kit`
  names the version it follows now (`app@v3`, rewritten in place), so it stays
  on it. The answer lists them in `pinned`.
- A config-managed kit publishes, and the answer carries `drift: true` and
  `warning`: `this will drift from the config repo; config write it to keep the repo in sync`.
```

Append to the API table:

```markdown
| `POST /admin/kits/{name}/draft/publish` | Publish (`If-Match`; `{"pin": bool}`, `application/json`). Answers `version`, `base`, `pinned`, `drift`, `warning`. |
```

- [ ] **Step 6: Run the tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS (the slice-1 push tests are unchanged), with no more than 741 audit errors.

- [ ] **Step 7: Lint and commit**

```bash
gofmt -l internal/jam/
just lint; echo "lint exit=$?"
git add internal/jam/ docs/usage/jam/kits-drafts.md
git commit -m "feat(jam): publish a kit draft as the next version (origin draft, --pin, drift)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: The admin client

**Files:**
- Create: `internal/jam/adminclient/kitdraft.go`
- Modify: `internal/jam/adminclient/adminclient.go` (`send` → `sendHeaders`; `finish` takes a raw `*[]byte`)
- Test: `internal/jam/adminclient/kitdraft_test.go`

**Interfaces:**
- Consumes: the Tasks 3–6 wire types (`jam.KitDraftStatus`, `jam.KitDraftSave`, `jam.KitDraftPublish`, `jam.KitDraftOpenBody`, `jam.KitDraftPublishBody`) and routes; `configGitTimeout`.
- Produces:
```go
// package adminclient
func (c *Client) sendHeaders(httpc *http.Client, method, path, contentType string, body []byte, hdr http.Header, out any) (http.Header, error)
func (c *Client) OpenKitDraft(name string, from int) (jam.KitDraftStatus, error)
func (c *Client) KitDraftStatus(name string) (jam.KitDraftStatus, error)
func (c *Client) DiscardKitDraft(name, etag string) error
func (c *Client) KitDraftFile(name, p string) ([]byte, error)
func (c *Client) PutKitDraftFile(name, p string, data []byte, mode, etag string) (jam.KitDraftSave, error) // mode "" | "0644" | "0755"
func (c *Client) RemoveKitDraftFile(name, p, etag string) (jam.KitDraftSave, error)
func (c *Client) ReplaceKitDraft(name string, tarBody []byte, etag string) (jam.KitDraftSave, error)
func (c *Client) PublishKitDraft(name string, pin bool, etag string) (jam.KitDraftPublish, error)
```

- [ ] **Step 1: Write the failing test**

```go
// internal/jam/adminclient/kitdraft_test.go
package adminclient

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestKitDraftClient(t *testing.T) {
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.DiscardHandler), nil))
	defer ts.Close()
	c := New(ts.URL, "")
	st, err := c.OpenKitDraft("app", 0)
	if err != nil || st.Base != 1 || st.ETag == "" {
		t.Fatalf("open = %+v %v", st, err)
	}
	uni := "docs/ünï dé.md"
	s, err := c.PutKitDraftFile("app", uni, []byte("hi\n"), "0755", st.ETag)
	if err != nil || len(s.Changed) != 1 || s.Changed[0].Path != uni {
		t.Fatalf("put = %+v %v", s, err)
	}
	if b, err := c.KitDraftFile("app", uni); err != nil || string(b) != "hi\n" {
		t.Fatalf("get = %q %v", b, err)
	}
	// A stale ETag is a 412 with the fixed text (Review Focus 1).
	_, err = c.PutKitDraftFile("app", "x", []byte("x"), "", st.ETag)
	se, ok := errors.AsType[*StatusError](err)
	if !ok || se.Code != 412 || se.Body != "draft app changed since you loaded it (reload)" {
		t.Fatalf("stale put = %v", err)
	}
	if s, err = c.RemoveKitDraftFile("app", uni, s.ETag); err != nil || s.Changed[0].Kind != "removed" {
		t.Fatalf("rm = %+v %v", s, err)
	}
	var body bytes.Buffer
	if err := kittree.WriteTar(&body, kittreetest.Files("kit.yml", "kind: studio\nprompt: synced\n")); err != nil {
		t.Fatal(err)
	}
	if s, err = c.ReplaceKitDraft("app", body.Bytes(), s.ETag); err != nil || len(s.Changed) != 1 || s.Changed[0].Kind != "modified" {
		t.Fatalf("replace = %+v %v", s, err)
	}
	if st, err = c.KitDraftStatus("app"); err != nil || st.ETag != s.ETag {
		t.Fatalf("status = %+v %v", st, err)
	}
	res, err := c.PublishKitDraft("app", false, s.ETag)
	if err != nil || res.Version != 2 {
		t.Fatalf("publish = %+v %v", res, err)
	}
	if _, err := c.KitDraftStatus("app"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("status after publish = %v, want ErrNotFound", err)
	}
	st, err = c.OpenKitDraft("app", 1)
	if err != nil || st.Base != 1 || st.Current != 2 {
		t.Fatalf("open --from 1 = %+v %v", st, err)
	}
	if err := c.DiscardKitDraft("app", st.ETag); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminclient/ -run KitDraftClient`
Expected: FAIL to compile, `c.OpenKitDraft undefined`.

- [ ] **Step 3: Implement**

In `internal/jam/adminclient/adminclient.go`, replace `send` with:

```go
// send issues one request; contentType "" sends no body.
func (c *Client) send(httpc *http.Client, method, path, contentType string, body []byte, out any) error {
	_, err := c.sendHeaders(httpc, method, path, contentType, body, nil, out)
	return err
}

// sendHeaders is send with extra request headers (hdr may be nil), returning
// the answer's headers. out may be a *[]byte to take a 2xx body raw.
func (c *Client) sendHeaders(httpc *http.Client, method, path, contentType string, body []byte, hdr http.Header, out any) (http.Header, error) {
	var r io.Reader
	if contentType != "" {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("admin API unreachable at %s (is `at-jam serve` running?): %w", c.base, err)
	}
	return resp.Header, finish(resp, method, path, out)
}
```

In `finish`, replace the final decode with:

```go
	if raw, ok := out.(*[]byte); ok {
		b, err := io.ReadAll(resp.Body)
		*raw = b
		return err
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
```

and extend its doc comment: `out may be a *[]byte, which takes the body raw.`

```go
// internal/jam/adminclient/kitdraft.go
package adminclient

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// Kit drafts (docs/usage/jam/kits-drafts.md). A write is sent against an
// etag (the draft's, from a status or the previous write): a stale one is a
// *StatusError with Code 412.

func draftPath(name string) string { return "/admin/kits/" + url.PathEscape(name) + "/draft" }

// draftFilePath escapes each segment of p, so a name with spaces or unicode
// travels intact and a '/' stays a separator.
func draftFilePath(name, p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return draftPath(name) + "/files/" + strings.Join(segs, "/")
}

func ifMatch(etag string) http.Header { return http.Header{"If-Match": {etag}} }

// OpenKitDraft opens kit name's draft from version from (0 = current).
func (c *Client) OpenKitDraft(name string, from int) (jam.KitDraftStatus, error) {
	var s jam.KitDraftStatus
	err := c.do("POST", draftPath(name), jam.KitDraftOpenBody{From: from}, &s)
	return s, err
}

// KitDraftStatus is kit name's draft status (ErrNotFound without a draft).
func (c *Client) KitDraftStatus(name string) (jam.KitDraftStatus, error) {
	var s jam.KitDraftStatus
	err := c.do("GET", draftPath(name), nil, &s)
	return s, err
}

// DiscardKitDraft drops kit name's draft.
func (c *Client) DiscardKitDraft(name, etag string) error {
	_, err := c.sendHeaders(c.httpc, "DELETE", draftPath(name), "", nil, ifMatch(etag), nil)
	return err
}

// KitDraftFile is file p of kit name's draft.
func (c *Client) KitDraftFile(name, p string) ([]byte, error) {
	var b []byte
	_, err := c.sendHeaders(c.httpc, "GET", draftFilePath(name, p), "", nil, nil, &b)
	return b, err
}

// PutKitDraftFile writes file p (mode "" keeps the file's, else "0644" or "0755").
func (c *Client) PutKitDraftFile(name, p string, data []byte, mode, etag string) (jam.KitDraftSave, error) {
	path := draftFilePath(name, p)
	if mode != "" {
		path += "?mode=" + url.QueryEscape(mode)
	}
	var s jam.KitDraftSave
	_, err := c.sendHeaders(c.httpc, "PUT", path, "application/octet-stream", data, ifMatch(etag), &s)
	return s, err
}

// RemoveKitDraftFile removes file p from kit name's draft.
func (c *Client) RemoveKitDraftFile(name, p, etag string) (jam.KitDraftSave, error) {
	var s jam.KitDraftSave
	_, err := c.sendHeaders(c.httpc, "DELETE", draftFilePath(name, p), "", nil, ifMatch(etag), &s)
	return s, err
}

// ReplaceKitDraft replaces kit name's draft tree with an uncompressed tar
// (kittree.WriteTar), under the raw-upload timeout.
func (c *Client) ReplaceKitDraft(name string, tarBody []byte, etag string) (jam.KitDraftSave, error) {
	hc := *c.httpc // keeps the Transport
	hc.Timeout = configGitTimeout
	var s jam.KitDraftSave
	_, err := c.sendHeaders(&hc, "PUT", draftPath(name), "application/x-tar", tarBody, ifMatch(etag), &s)
	return s, err
}

// PublishKitDraft publishes kit name's draft as its next version.
func (c *Client) PublishKitDraft(name string, pin bool, etag string) (jam.KitDraftPublish, error) {
	body, err := json.Marshal(jam.KitDraftPublishBody{Pin: pin})
	if err != nil {
		return jam.KitDraftPublish{}, err
	}
	var res jam.KitDraftPublish
	_, err = c.sendHeaders(c.httpc, "POST", draftPath(name)+"/publish", "application/json", body, ifMatch(etag), &res)
	return res, err
}
```

- [ ] **Step 4: Run the tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminclient/ ./cmd/at-jam/`
Expected: PASS. The existing callers of `send` and `finish` behave the same.

- [ ] **Step 5: Lint and commit**

```bash
gofmt -l internal/jam/adminclient/
just lint; echo "lint exit=$?"
git add internal/jam/adminclient/
git commit -m "feat(adminclient): kit draft calls with If-Match and raw file bodies

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: `at-jam kit draft open|status|get|put|rm|discard|publish`

**Files:**
- Create: `cmd/at-jam/kitdraft.go`
- Modify: `cmd/at-jam/main.go` (`cmdKit` dispatches `draft` before parsing its flags; usage and Brief list `draft`; `kit versions` shows `Origin`), `docs/usage/jam/kits-drafts.md` (the verbs), `docs/usage/jam/kits.md` (one pointer)
- Test: `cmd/at-jam/kitdraft_test.go`

**Interfaces:**
- Consumes: Task 7's client methods; `readConfig`, `validateApp`, `firstNonEmpty`, `loadSettings`, `defaultApp`, `defaultAdminURL`, `adminTokenEnv`, `resolveToken`, `cli.ParseFlags`, `textesc.Escape`, `kittree.CheckPath`.
- Produces:
```go
// package main
func cmdKitDraft(args []string, stdout, stderr io.Writer) int
var kitDraftVerbs map[string]struct{ args string; n int }
const kitDraftUsage = "at-jam kit draft: expected open|status|get|put|rm|discard|publish"
func parseDraftVersion(s string) (int, error)   // "", "3", "v3"
func draftModeFlag(s string) (string, error)    // "", 644/0644, 755/0755 → "", "0644", "0755"
func printDraftStatus(w io.Writer, st jam.KitDraftStatus)
func printDraftChanges(w io.Writer, cs []jam.DraftChange)
func printProblems(w io.Writer, ps []string)
// test helper: func draftCLI(t *testing.T) (run func(args ...string) (int, string, string), store *jam.MemStore)
```

- [ ] **Step 1: Write the failing tests**

```go
// cmd/at-jam/kitdraft_test.go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// draftCLI is a Jam holding kit app v1, a runner for `at-jam kit draft …`
// against it (args[0] is the verb), the store and the admin URL.
func draftCLI(t *testing.T) (func(args ...string) (int, string, string), *jam.MemStore, string) {
	t.Helper()
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	t.Cleanup(ts.Close)
	return func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		argv := append([]string{"kit", "draft", args[0], "--admin-url", ts.URL}, args[1:]...)
		code := run(argv, func(string) string { return "" }, &out, &errb)
		return code, out.String(), errb.String()
	}, store, ts.URL
}

func TestKitDraftVerbs(t *testing.T) {
	kit, _, _ := draftCLI(t)
	code, out, errs := kit("open", "app")
	if code != 0 || !strings.HasPrefix(out, `opened draft app from v1 (etag "`) {
		t.Fatalf("open: %d %q %q", code, out, errs)
	}
	script := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(script, []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs = kit("put", "app", "run.sh", script, "--mode", "755"); code != 0 || !strings.HasPrefix(out, `saved run.sh to draft app (etag "`) {
		t.Fatalf("put: %d %q %q", code, out, errs)
	}
	if code, out, _ = kit("get", "app", "run.sh"); code != 0 || out != "echo hi\n" {
		t.Fatalf("get: %d %q", code, out)
	}
	dst := filepath.Join(t.TempDir(), "got.sh")
	if code, _, _ = kit("get", "app", "run.sh", "-o", dst); code != 0 {
		t.Fatalf("get -o: %d", code)
	}
	if b, _ := os.ReadFile(dst); string(b) != "echo hi\n" {
		t.Fatalf("get -o wrote %q", b)
	}
	code, out, _ = kit("status", "app")
	for _, want := range []string{"draft app: base v1, current v1\n", "etag \"", "files (2):", "0755", "run.sh", "changes vs v1:\n  added    run.sh\n", "problems: none\n"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("status lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = kit("status", "app", "--json")
	var st jam.KitDraftStatus
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || len(st.Changes) != 1 || st.Base != 1 {
		t.Fatalf("status --json: %d %s", code, out)
	}
	// --etag writes against the given etag: a stale one fails, nothing written (Review Focus 1).
	if code, _, errs = kit("put", "app", "x.txt", script, "--etag", strings.Repeat("0", 64)); code != 1 || !strings.Contains(errs, "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale --etag: %d %q", code, errs)
	}
	if code, out, _ = kit("put", "app", "y.txt", script, "--etag", st.ETag); code != 0 {
		t.Fatalf("put --etag: %d %q", code, out)
	}
	if code, out, _ = kit("rm", "app", "y.txt"); code != 0 || !strings.HasPrefix(out, "removed y.txt from draft app (etag ") {
		t.Fatalf("rm: %d %q", code, out)
	}
	if code, out, _ = kit("publish", "app"); code != 0 || out != "published draft app as v2\n" {
		t.Fatalf("publish: %d %q", code, out)
	}
	if code, _, errs = kit("status", "app"); code != 1 || !strings.Contains(errs, "kit app has no draft") {
		t.Fatalf("status after publish: %d %q", code, errs)
	}
	// A draft of current with no edit has nothing to publish.
	if code, _, errs = kit("open", "app"); code != 0 {
		t.Fatal(errs)
	}
	if code, _, errs = kit("publish", "app"); code != 1 || !strings.Contains(errs, "draft app has no changes") {
		t.Fatalf("publish unchanged: %d %q", code, errs)
	}
	if code, out, _ = kit("discard", "app"); code != 0 || out != "discarded draft app\n" {
		t.Fatalf("discard: %d %q", code, out)
	}
	if code, out, _ = kit("open", "app", "--from", "v1"); code != 0 || !strings.HasPrefix(out, "opened draft app from v1 (etag ") {
		t.Fatalf("open --from v1: %d %q", code, out)
	}
	if code, out, _ = kit("discard", "app"); code != 0 {
		t.Fatalf("discard: %d %q", code, out)
	}
}

func TestKitVersionsShowsDraftOrigin(t *testing.T) {
	kit, _, url := draftCLI(t)
	f := filepath.Join(t.TempDir(), "n.md")
	if err := os.WriteFile(f, []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"open", "app"}, {"put", "app", "n.md", f}, {"publish", "app"}} {
		if code, _, errs := kit(args...); code != 0 {
			t.Fatalf("%v: %s", args, errs)
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "versions", "--admin-url", url, "app"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "v2") || !strings.Contains(lines[2], "draft") || strings.Contains(lines[1], "draft") {
		t.Fatalf("versions:\n%s", out.String())
	}
}

func TestKitDraftStatusBaseSuperseded(t *testing.T) {
	kit, store, _ := draftCLI(t)
	if code, _, errs := kit("open", "app"); code != 0 {
		t.Fatal(errs)
	}
	if _, err := store.PushKit("app", "kind: studio\nprompt: two\n"); err != nil {
		t.Fatal(err)
	}
	code, out, _ := kit("status", "app")
	if code != 0 || !strings.Contains(out, "draft app: base v1, current v2\n") || !strings.Contains(out, "v2 landed after the draft was opened; publishing replaces it with the draft (nothing is merged)") {
		t.Fatalf("status (Review Focus 2):\n%s", out)
	}
}

func TestKitDraftArgs(t *testing.T) {
	kit, _, _ := draftCLI(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "app"}, "at-jam kit draft get: expected <kit> <path> [-o file]"},
		{[]string{"put", "app", "x"}, "at-jam kit draft put: expected <kit> <path> <file|-> [--mode 755]"},
		{[]string{"open", "app", "--from", "x"}, "at-jam kit draft open: --from must be a version (vN)"},
		{[]string{"put", "app", "x", "f", "--mode", "700"}, "at-jam kit draft put: --mode must be 755 or 644"},
		{[]string{"get", "app", "a\nb"}, "at-jam kit draft get: a file has an unsafe path"},
	} {
		if code, _, errs := kit(tc.args...); code != 2 || !strings.Contains(errs, tc.want) {
			t.Fatalf("%q: %d %q, want %q", tc.args, code, errs, tc.want)
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "draft"}, func(string) string { return "" }, &out, &errb); code != 2 || !strings.Contains(errb.String(), "expected open|status|get|put|rm|discard|publish") {
		t.Fatalf("no verb: %d %q", code, errb.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ -run 'KitDraft|KitVersionsShowsDraftOrigin'`
Expected: FAIL. `kit draft` is not a `cmdKit` subcommand yet, so `cmdKit` rejects `draft`.

- [ ] **Step 3: Implement**

```go
// cmd/at-jam/kitdraft.go
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/textesc"
)

// kitDraftVerbs is each `kit draft` verb's arguments (for its usage line)
// and how many are positional.
var kitDraftVerbs = map[string]struct {
	args string
	n    int
}{
	"open":    {"<kit> [--from vN]", 1},
	"status":  {"<kit> [--json]", 1},
	"get":     {"<kit> <path> [-o file]", 2},
	"put":     {"<kit> <path> <file|-> [--mode 755]", 3},
	"rm":      {"<kit> <path>", 2},
	"discard": {"<kit>", 1},
	"publish": {"<kit> [--pin]", 1},
}

const kitDraftUsage = "at-jam kit draft: expected open|status|get|put|rm|discard|publish"

// cmdKitDraft is `at-jam kit draft <verb>` (docs/usage/jam/kits-drafts.md).
// A write sends the draft's etag as If-Match: --etag, else the one a status
// fetched just before it.
func cmdKitDraft(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, kitDraftUsage)
		return 2
	}
	verb, rest := args[0], args[1:]
	spec, known := kitDraftVerbs[verb]
	if !known {
		fmt.Fprintln(stderr, kitDraftUsage)
		return 2
	}
	fs := flag.NewFlagSet("kit draft "+verb, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	from := fs.String("from", "", "open: the version to open the draft from (vN; default: current)")
	asJSON := fs.Bool("json", false, "status: print the status as JSON")
	outFile := fs.String("o", "", "get: write the file here instead of stdout")
	mode := fs.String("mode", "", "put: the file's mode, 755 or 644 (default: the file's, else 644)")
	pin := fs.Bool("pin", false, "publish: pin the kits that follow this one to the version they follow now")
	etag := fs.String("etag", "", "write against this draft etag (default: fetch the draft's status first)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "at-jam kit draft %s: %s\n", verb, msg)
		return 2
	}
	if err := validateApp(*app); err != nil {
		return usage(err.Error())
	}
	if len(pos) != spec.n {
		return usage("expected " + spec.args)
	}
	if spec.n >= 2 {
		if err := kittree.CheckPath(pos[1]); err != nil && verb != "sync" {
			return usage(err.Error())
		}
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	kit, name := pos[0], textesc.Escape(pos[0], false)
	// etagFor is the etag a write is sent against.
	etagFor := func() (string, error) {
		if *etag != "" {
			return `"` + strings.Trim(*etag, `"`) + `"`, nil
		}
		st, err := c.KitDraftStatus(kit)
		return st.ETag, err
	}
	switch verb {
	case "open":
		v, err := parseDraftVersion(*from)
		if err != nil {
			return usage(err.Error())
		}
		st, err := c.OpenKitDraft(kit, v)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "opened draft %s from v%d (etag %s)\n", name, st.Base, st.ETag)
	case "status":
		st, err := c.KitDraftStatus(kit)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(st); err != nil {
				return fail(err)
			}
			break
		}
		printDraftStatus(stdout, st)
	case "get":
		b, err := c.KitDraftFile(kit, pos[1])
		if err != nil {
			return fail(err)
		}
		if *outFile == "" {
			_, err = stdout.Write(b)
		} else {
			err = os.WriteFile(*outFile, b, 0o644)
		}
		if err != nil {
			return fail(err)
		}
	case "put":
		m, err := draftModeFlag(*mode)
		if err != nil {
			return usage(err.Error())
		}
		data, err := readConfig(pos[2])
		if err != nil {
			return fail(err)
		}
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		s, err := c.PutKitDraftFile(kit, pos[1], data, m, tag)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "saved %s to draft %s (etag %s)\n", textesc.Escape(pos[1], false), name, s.ETag)
		printProblems(stdout, s.Problems)
	case "rm":
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		s, err := c.RemoveKitDraftFile(kit, pos[1], tag)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "removed %s from draft %s (etag %s)\n", textesc.Escape(pos[1], false), name, s.ETag)
		printProblems(stdout, s.Problems)
	case "discard":
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		if err := c.DiscardKitDraft(kit, tag); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "discarded draft %s\n", name)
	case "publish":
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		res, err := c.PublishKitDraft(kit, *pin, tag)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "published draft %s as v%d\n", name, res.Version)
		for _, p := range res.Pinned {
			fmt.Fprintf(stdout, "pinned %s\n", textesc.Escape(p, false))
		}
		if res.Drift {
			fmt.Fprintln(stderr, "warning:", res.Warning)
		}
	}
	return 0
}

// parseDraftVersion reads --from: "" (current), "3" or "v3".
func parseDraftVersion(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "v"))
	if err != nil || n < 1 {
		return 0, errors.New("--from must be a version (vN)")
	}
	return n, nil
}

// draftModeFlag reads --mode: "" (keep), 644/0644 or 755/0755.
func draftModeFlag(s string) (string, error) {
	switch s {
	case "":
		return "", nil
	case "644", "0644":
		return "0644", nil
	case "755", "0755":
		return "0755", nil
	}
	return "", errors.New("--mode must be 755 or 644")
}

func draftTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// printDraftStatus renders a draft's status; every name is escaped.
func printDraftStatus(w io.Writer, st jam.KitDraftStatus) {
	fmt.Fprintf(w, "draft %s: base v%d, current v%d\n", textesc.Escape(st.Kit, false), st.Base, st.Current)
	if st.Current != st.Base {
		fmt.Fprintf(w, "  v%d landed after the draft was opened; publishing replaces it with the draft (nothing is merged)\n", st.Current)
	}
	fmt.Fprintf(w, "etag %s\n", st.ETag)
	fmt.Fprintf(w, "saved %s by %s (opened %s)\n", draftTime(st.UpdatedAt), textesc.Escape(st.Actor, false), draftTime(st.OpenedAt))
	if st.Managed {
		fmt.Fprintln(w, "managed by the config repo: publishing will drift from it")
	}
	fmt.Fprintf(w, "files (%d):\n", len(st.Entries))
	for _, e := range st.Entries {
		fmt.Fprintf(w, "  %s %9d  %s\n", e.Mode, e.Size, textesc.Escape(e.Path, false))
	}
	if len(st.Changes) == 0 {
		fmt.Fprintf(w, "no changes vs v%d\n", st.Base)
	} else {
		fmt.Fprintf(w, "changes vs v%d:\n", st.Base)
		printDraftChanges(w, st.Changes)
	}
	if len(st.Problems) == 0 {
		fmt.Fprintln(w, "problems: none")
	} else {
		printProblems(w, st.Problems)
	}
}

// printDraftChanges prints one "  <kind> <path>" line per change.
func printDraftChanges(w io.Writer, cs []jam.DraftChange) {
	for _, c := range cs {
		fmt.Fprintf(w, "  %-8s %s\n", c.Kind, textesc.Escape(c.Path, false))
	}
}

// printProblems prints a draft's problems; nothing when there are none.
func printProblems(w io.Writer, ps []string) {
	if len(ps) == 0 {
		return
	}
	fmt.Fprintln(w, "problems:")
	for _, p := range ps {
		fmt.Fprintf(w, "  - %s\n", textesc.Escape(p, false))
	}
}
```

The `verb != "sync"` guard in the `CheckPath` block is there for Task 9, whose second argument is a local directory, not a draft path. Keep it as written.

In `cmd/at-jam/main.go`:
- In `cmdKit`, right after `sub, rest := args[0], args[1:]`, add:

```go
	if sub == "draft" { // its own verbs and flags (kitdraft.go)
		return cmdKitDraft(rest, stdout, stderr)
	}
```

- In `cmdKit`'s usage line and the `kit` command's `Brief`, change `…|untrack|sync` to `…|untrack|sync|draft`.
- In `kit versions`, after the `if h.Source != nil { … }` block, add:

```go
			if h.Source == nil && h.Origin != "" {
				srcText = h.Origin // "draft": published from a draft
			}
```

- [ ] **Step 4: Document the verbs**

Append to `docs/usage/jam/kits-drafts.md`, before the closing audit paragraph:

````markdown
## The `kit draft` verbs

```
at-jam kit draft open app [--from v2]                  # → opened draft app from v2 (etag "…")
at-jam kit draft status app [--json]                   # base and current, files, changes vs the base, problems
at-jam kit draft get app run.sh [-o run.sh]            # one file's bytes (stdout by default)
at-jam kit draft put app run.sh ./run.sh [--mode 755]  # write a file (- reads stdin)
at-jam kit draft rm app old.sh                         # remove a file
at-jam kit draft publish app [--pin]                   # → published draft app as v4
at-jam kit draft discard app                           # drop the draft
```

Each write first fetches the status and sends its etag. With `--etag <hash>`,
a script writes against the etag it already holds and fails with 412 instead
of overwriting another edit. `publish` prints a warning on stderr for a
config-managed kit. All verbs take the admin-client flags
(`--app`/`--admin-url`/`--token`).
````

In `docs/usage/jam/kits.md`, after the `kit rm` bullet in "## The `kit` verbs", add (write the <link> as a real link):

```markdown
- To change a kit's files in place before they become a version, open a
  **draft** (`kit draft open|status|get|put|rm|sync|publish|discard`); see
  <link `kits-drafts.md` as "kits-drafts.md">.
```

- [ ] **Step 5: Run the tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ ./internal/jam/... && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS, with no more than 741 audit errors.

- [ ] **Step 6: Lint and commit**

```bash
gofmt -l cmd/at-jam/
just lint; echo "lint exit=$?"
git add cmd/at-jam/ docs/usage/jam/
git commit -m "feat(at-jam): kit draft open|status|get|put|rm|discard|publish

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: `at-jam kit draft sync <kit> <dir>`

**Files:**
- Modify: `cmd/at-jam/kitdraft.go` (the `sync` verb), `docs/usage/jam/kits-drafts.md`, `docs/usage/jam/kits-directory.md`
- Test: `cmd/at-jam/kitdraft_test.go`

**Interfaces:**
- Consumes: `kittree.ReadDir`, `kittree.WriteTar`, `openDirError` (`kitpush.go`), `Client.ReplaceKitDraft` (Task 7), `printDraftChanges`/`printProblems` (Task 8), `draftCLI` (Task 8 test helper).
- Produces: the `sync` entry in `kitDraftVerbs` (`{"<kit> <dir>", 2}`); `kitDraftUsage` ends `…|publish|sync`.

- [ ] **Step 1: Write the failing test**

Append to `cmd/at-jam/kitdraft_test.go`:

```go
func TestKitDraftSync(t *testing.T) {
	kit, store, _ := draftCLI(t)
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{"kit.yml": "kind: studio\nprompt: synced\n", "bin/run": "echo\n", ".dockerignore": "*.log\n", "debug.log": "x"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "bin/run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := kit("sync", "app", dir); code != 1 || !strings.Contains(errs, "kit app has no draft") {
		t.Fatalf("sync without a draft: %d %q", code, errs)
	}
	if code, _, errs := kit("open", "app"); code != 0 {
		t.Fatal(errs)
	}
	code, out, errs := kit("sync", "app", dir)
	want := "  added    .dockerignore\n  added    bin/run\n  modified kit.yml\n"
	if code != 0 || !strings.HasPrefix(out, "synced "+dir+" to draft app (etag \"") || !strings.HasSuffix(out, want) {
		t.Fatalf("sync: %d %q %q", code, out, errs)
	}
	d, _ := store.GetKitDraft("app")
	tree, _ := store.KitTree(d.Tree)
	if _, ok := tree.Lookup("debug.log"); ok {
		t.Fatal(".dockerignore must filter the sync")
	}
	if e, _ := tree.Lookup("bin/run"); e.Mode != 0o755 {
		t.Fatal("the exec bit was lost")
	}
	if code, out, _ = kit("sync", "app", dir); code != 0 || !strings.HasPrefix(out, "draft app unchanged (etag ") {
		t.Fatalf("second sync: %d %q", code, out)
	}
	// A symlink in the directory is refused client-side; nothing is sent.
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "leak")); err != nil {
		t.Fatal(err)
	}
	if code, _, errs = kit("sync", "app", dir); code != 1 || !strings.Contains(errs, "leak: symlinks are not allowed") {
		t.Fatalf("symlink: %d %q", code, errs)
	}
	if again, _ := store.GetKitDraft("app"); again.Tree != d.Tree {
		t.Fatal("a refused sync changed the draft")
	}
	if code, _, errs = kit("sync", "app", filepath.Join(dir, "nope")); code != 1 || !strings.Contains(errs, "no such directory") {
		t.Fatalf("missing dir: %d %q", code, errs)
	}
	// A stale --etag is refused (Review Focus 1).
	if code, _, errs = kit("sync", "app", t.TempDir(), "--etag", strings.Repeat("0", 64)); code != 1 || !strings.Contains(errs, "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale sync: %d %q", code, errs)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ -run KitDraftSync`
Expected: FAIL: exit 2, `at-jam kit draft: expected open|status|get|put|rm|discard|publish`.

- [ ] **Step 3: Implement**

In `cmd/at-jam/kitdraft.go`:
- Add `"sync": {"<kit> <dir>", 2},` to `kitDraftVerbs`.
- Change `kitDraftUsage` to `"at-jam kit draft: expected open|status|get|put|rm|discard|publish|sync"`.
- Add `"bytes"` to the imports.
- Add this case to the `switch`:

```go
	case "sync":
		// The directory as `kit push` reads it (.dockerignore applied as the
		// walk goes, symlinks and irregular files refused), sent whole.
		dir := pos[1]
		root, err := os.OpenRoot(dir)
		if err != nil {
			return fail(fmt.Errorf("%s: %s", dir, openDirError(dir, err)))
		}
		defer root.Close()
		files, err := kittree.ReadDir(root, ".")
		if err != nil {
			return fail(fmt.Errorf("%s: %w", dir, err))
		}
		var body bytes.Buffer
		if err := kittree.WriteTar(&body, files); err != nil {
			return fail(err)
		}
		tag, err := etagFor()
		if err != nil {
			return fail(err)
		}
		s, err := c.ReplaceKitDraft(kit, body.Bytes(), tag)
		if err != nil {
			return fail(err)
		}
		if len(s.Changed) == 0 {
			fmt.Fprintf(stdout, "draft %s unchanged (etag %s)\n", name, s.ETag)
		} else {
			fmt.Fprintf(stdout, "synced %s to draft %s (etag %s)\n", textesc.Escape(dir, false), name, s.ETag)
			printDraftChanges(stdout, s.Changed)
		}
		printProblems(stdout, s.Problems)
```

Update `TestKitDraftArgs`'s no-verb expectation to `expected open|status|get|put|rm|discard|publish|sync`.

- [ ] **Step 4: Document sync**

In `docs/usage/jam/kits-drafts.md`'s verbs block, add after the `rm` line:

```
at-jam kit draft sync app ./app                        # mirror a directory into the draft (needs an open draft)
```

After the block, add: ``sync`` reads the directory as `kit push` does (`.dockerignore` applied, symlinks refused) and replaces the draft's tree with it. It prints the files added, removed, modified or mode-changed, or `unchanged`. It needs an open draft: run `kit draft open` first.

In `docs/usage/jam/kits-directory.md` "## `.dockerignore`", append this sentence to the paragraph (write the <link> as a real link): ``A kit draft applies it on every write too (<link `kits-drafts.md` as "kits-drafts.md">).``

In `docs/usage/jam/INDEX.md`, nothing changes: the row from Task 3 already names `sync`.

- [ ] **Step 5: Run every check**

Run:
```bash
GOPROXY=https://proxy.golang.org,direct just test
just lint; echo "lint exit=$?"
GOPROXY=https://proxy.golang.org,direct go vet -tags integration ./...
gofmt -l cmd/ internal/
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1
```
Expected: all pass, `lint exit=0`, `gofmt -l` prints nothing, and the audit reports no more than 741 errors.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-jam/ docs/usage/jam/
git commit -m "feat(at-jam): kit draft sync mirrors a directory into a kit's draft

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-review

**Spec coverage (§3, §5, §6):**
- Draft, at most one per kit, from current or a version: Tasks 2 and 3.
- Writes validated (limits, paths), with an invalid state saved while editing: Tasks 1, 4 and 5.
- Problems block publish: Tasks 3 and 6.
- ETag concurrency, never merged: Tasks 3–6, Review Focus 1.
- API: open/discard/status in Task 3, files in Task 4, publish in Task 6. The tar replace (Task 5) is the ruling's sync route.
- CLI: Tasks 8 and 9.
- Publish with `Meta.Source = draft` (as `Origin`), closing the draft, followers going stale, `--pin`, the drift warning: Task 6.
- Errors (fixed, path-located, unechoed): Tasks 4 and 5, Review Focus 4.
- §6 "draft ETag conflicts, validation, publish": Tasks 4–6.
- Not in this slice, by the spec's slicing: the UI (slice 4), test build and draft studios (slice 5). The API is shaped for them (ruling 10).

**Binding rulings:** storage, GC roots, removal and not exported (Task 2); relaxed builder (Task 1); problems (Task 3); 428/412 and `kitMu` (Tasks 3–6); every route and its Content-Type (Tasks 3–6); publish via `pushKitTreeLocked` with `has no changes` and drift (Task 6); `--pin` (deviation 12, Task 6); CLI and `--etag` (Tasks 8–9); sync via `ReadDir` + tar + If-Match with the change list (Task 9); audit lines (Tasks 3, 4, 6); the docs leaf, INDEX row and links (Tasks 3–9); migration `0020` (Task 2).

**Placeholder scan:** every code step shows the code; no TBD, no "similar to Task N". `draftCLI` returns `(run, store, url)` and every CLI test destructures three values.

**Type consistency:**
- `KitDraft{Kit, Base, Tree, OpenedAt, UpdatedAt, Actor}` (Task 2) is used by Tasks 3–6.
- `KitDraftStatus`, `DraftEntry`, `DraftChange`, `KitDraftOpenBody` (Task 3); `KitDraftSave` (Task 4); `KitDraftPublishBody`, `KitDraftPublish` (Task 6). The client (Task 7) and the CLI (Tasks 8–9) use these names.
- `pushKitTreeLocked(..., origin string)` and `replaceKitYMLLocked` (Task 6) are used by `PublishKitDraft` and `pinFollowersLocked`.
- `lockedDraftTree` and `saveDraftLocked` (Task 4) are used by Tasks 5 and 6.
- Test helpers: `pushApp`, `wantErr`, `draftNow` (Task 3, `kit_draft_test.go`); `draftDo`, `body` (Task 3, `kit_draft_admin_test.go`); `openApp`, `withDraftLimits`, `unsafeDraftPath` (Task 4); `draftCLI` (Task 8).

**Review Focus:** each of the five lines has its test in the owning task (named in the section).

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-10-25-kit-drafts-slice3.md`. Recommended: **subagent-driven**. Nine tasks build on each other's exact interfaces (store → jam → routes → client → CLI), so a fresh reviewer per task catches a signature drift before the next task consumes it.
