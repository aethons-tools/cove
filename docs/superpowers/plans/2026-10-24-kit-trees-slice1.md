# Kits as directory trees — slice 1 (Trees) and slice 2 (Diff everywhere) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A kit version is a directory: `kit.yml` plus whatever files sit beside it, stored as a content-addressed tree in Jam's own database. A `Dockerfile` next to `kit.yml` makes the directory (minus `kit.yml`) the build context. Push, git import/track/sync, config write/apply, build, export/import, inspect and the version diff all work on that one shape. A one-time migration at serve start turns every stored version into a tree, and the packed base64 tar.gz (`base.context`, `context-files`, `context-dir`) and its machinery are deleted.

**Architecture:**
- **One pure package, `internal/kittree`.** The tree model (entries `{path, mode, sha256, size}`, the hash, the limits), reading a tree in from a directory (through `os.Root`, `.dockerignore` applied as the walk goes) or a push tar, writing the push tar and the deterministic build-context tar, a read-only go-billy view over a tree and a blob lookup, and the version diff. It has no store: blobs come in through `kittree.Blobs func(sha) ([]byte, bool)`.
- **The store holds objects beside docs.** `kit_blobs` and `kit_trees` (Postgres; maps in `memState`), cached like every other row. A kit doc gains `Trees[version] = tree sha256`. Objects ride in the same atomic write as the docs that reference them (`PushKitVersion`, `PutKitDocs(kits, objs)`, `ConfigWrite.KitObjects`); a GC drops what no version references.
- **One reader.** `jam.LoadKitVersion` returns a version's tree, its `kit.yml`, a billy view (`jam.KitFS`) and the parsed kit (note files read from the tree, `BuildContext` = the context's tree hash), cached by tree hash. The raise path, the chain, the launcher, the admin API, the UI, config and inspect all read through it.
- **One writer per entry point, all ending in a tree.** `jam.PushKitFiles` (tar API, JSON API, UI, default kit), the kit-set import (git import/track/sync, config apply) and the migration.
- **Order keeps the build green:** the tree goes in alongside the packed context (Tasks 1–12), the migration follows (Task 13), then the old code is deleted (Task 14). From Task 5 on, even the legacy `Store.PushKit` stores a one-file tree, so every reader sees trees early.

**Tech Stack:** Go 1.26; `archive/tar`; `os.Root`; `github.com/go-git/go-billy/v5` (already a dependency); `github.com/moby/patternmatcher` (already a dependency); `gopkg.in/yaml.v3` (`yaml.Node` for the in-place `base.kit` rewrite); the existing `jam` store (MemStore + Postgres via pgx), `storetest`, `linediff`, `condition`, `runner.Fake`, `adminclient`.

**Spec:** [`docs/superpowers/specs/2026-10-24-kit-trees-design.md`](../specs/2026-10-24-kit-trees-design.md): slices 1 and 2 (§1, §2, §4, §5, §6 minus the later-slice tests). §3 (drafts, the authoring loop) is out of scope.

## Spec deviations and interpretations

1. **Kit doc fields.** The spec says `Versions[n] = tree sha256`. Instead a new map `Kit.Trees map[int]string` (`json:"trees,omitempty"`) holds the tree of each version. `Kit.Versions` keeps its JSON name and its pre-tree meaning (config text), so an old doc is recognizable without guessing whether a value is text or a hash. Only `MigrateKitTrees` reads it after Task 14. Two more maps: `Kit.Broken map[int]string` (a version the migration could not turn into a tree, with a fixed reason) and `Kit.Legacy map[int]string` (every migrated version's pre-migration text, kept for one release). `Kit.HasVersion(v)` and `Kit.VersionNumbers()` cover all three.
2. **An entry also carries `size`.** It is not hashed. Diffs, limits and listings need it without reading the blob.
3. **Exact hash encoding.** The hash is sha256 over the entries sorted by path, each `path NUL mode NUL blob-sha256 LF`, where mode is the octal permission bits without a leading zero (`644` or `755`).
4. **Blobs are cached in memory.** The store caches them like every other Postgres row: reads never touch the database, and writes are commit-then-cache. RAM grows with the distinct blob bytes Jam holds. That is the cost the spec's "a bucket can come later" accepts. The `kit_blobs.bytes` column is `bytea`.
5. **Objects ride with their docs.** `PushKitVersion(name, tree, objs, meta, track)`, `PutKitDocs(kits, objs)` and `ConfigWrite.KitObjects` write the trees and blobs in the same transaction as the docs that name them. No dangling reference can be written. The store refuses a doc naming a tree that is neither written nor stored, and a blob whose bytes are not its hash.
6. **GC triggers.** Jam has no per-version delete, so "on version delete" means on kit removal. `GCKitObjects` runs after `RemoveKitChecked`, after a config apply that removes kits, and at serve start after the migration. A GC failure never fails the removal; serve start collects again.
7. **Digest formula.** `BuildDigest` and every `ChainDigest` link keep today's payload and add `"buildContext": <context tree hash>` (`omitempty`) right after `base`. A kit with no `Dockerfile` (the default kit, an `image:` kit, a plain parent follower) keeps its digest exactly. A context kit's digest changes once, at migration. The chain stage tag hashes `JSON(base)`, then `NUL` and the build-context hash when it is set. Note files of a `Dockerfile` kit live in its tree, so they are part of the build context (the spec defines the context as "the tree minus `kit.yml`"). Editing `prompt` or a note's inline `body` does not rebuild; editing a note file beside a `Dockerfile` does.
8. **Push upload format.**
   - `POST /admin/kits/{name}/versions` takes an uncompressed tar (`Content-Type: application/x-tar`) of regular files. Directory entries are skipped and a leading `./` is dropped, so `tar -C dir -cf - .` works. Links and irregular entries are refused.
   - The body is capped at `kittree.MaxUpload()` = 64 MiB + 2000 × 2 KiB + 1 MiB, and over that the answer is 413.
   - The server applies `.dockerignore` again (`kittree.Ignore`), so a hand-made tar cannot store ignored files.
   - The CLI builds the body in memory with `kittree.WriteTar` and sends it with `adminclient.PushKitTree` under the existing 5-minute raw-upload timeout.
   - `kit push <dir> [--name n]` takes its name from `--name`, else the legacy `name:`, else the directory's base name. `kit push --config <file|-> [--name n]` pushes a one-file tree and needs `--name` unless the file has a legacy `name:`.
   - `POST /admin/kits` (JSON `{name, config}`) stays, as a one-file push.
   - The UI's "push a new version" box edits only `kit.yml`: it keeps the current version's other files (`jam.ReplaceKitYML`). The "new kit" form is a one-file push.
9. **Migration placement.**
   - SQL migration `0019_kit_trees.sql` creates the two tables when the store opens.
   - The data migration is `jam.MigrateKitTrees(store, conds, log)`, a startup step in `cmd/at-jam` like `MigrateModelSpecs`. It runs after the conditions tracker loads (it raises `kit.migrate:<name>`) and before the drift check, the GC and the default-kit seeding.
   - It writes everything in one `store.ApplyConfig`: the docs, the objects, and the re-keyed config marks.
   - It also runs right after `POST /admin/config` imports a backup older than v5.
   - It re-keys a kit's managed mark when the mark recorded the current version's old canonical form, so an upgrade shows no drift.
   - It does not apply `.dockerignore` to an old context: it keeps what was built.
   - A stored config that does not parse once its context fields are taken out keeps its text verbatim as `kit.yml`, so it stays exactly as invalid as before.
   - The condition is open while a kit's *current* version is broken. Each serve start re-evaluates it, and the admin UI's attention list shows the same problem live.
10. **`Store.PushKit(name, kitYML)` survives as a store convenience.** From Task 5 it stores `kitYML` as a one-file tree, without checks. That keeps every existing reader and most test fixtures working through the transition. After Task 14 it is the seeding/test helper only, and every operator push goes through `jam.PushKitFiles`. `PushKitMeta`, `PushKitTracked` and `KitConfig` are removed in Task 14.
11. **`studio.ParseKitFS` stays lenient about the old context fields until Task 14.** Every ingest path calls `studio.RefuseContextFields` explicitly: push, git import, config apply. Task 14 moves the call into `ParseKitFS` when the fields leave the schema.
12. **Path-parent rewrite in place.** A kit set stores each `../web` reference as `web@vN` by editing the `base.kit` scalar in the stored `kit.yml` bytes (`studio.SetBaseKit`). Every other byte, comments included, is kept. `config write` rewrites `web@vN` back to `../web` the same way. `base.kit` must therefore be a one-line plain or quoted scalar without escapes, which every valid reference is.
13. **Config canonical form for a kit.** It is the tree's *listing*: the `kit.yml` text, then `# files:` and one line per entry (`mode size sha256 path`), then `# source: …` for a tracked kit. The listing identifies exactly the same tree as its hash would, and a plan's line diff of two listings is readable. `ConfigCanonDisplay` becomes the identity for kits.
14. **Config export carries kit directories.** `ConfigTreeExport.Kits map[string][]ConfigKitFile` (`kits/<n>` → files with base64 data and modes) replaces `binary`, `prune`, `inline` and `?contexts=files`.
    - A writer mirrors each directory: it writes the files and removes every other file there that the directory's `.dockerignore` does not exclude.
    - An empty list mirrors a directory to nothing: a kit now written by reference, or a managed kit gone from Jam.
    - There is no inline fallback any more. A kit too big for the 16 MiB config upload is written anyway, and `config plan --dir` then refuses the upload (`configtar.ErrTooLarge`). Use a git source.
    - An older `at-jam` writing against a new Jam writes no kit files. The release note says to upgrade the CLI.
15. **Snapshot v5.** `ConfigSnapshot` gains `kit_trees` and `kit_blobs` (base64 in JSON) and `ConfigSnapshotVersion` becomes 5, so an older Jam refuses a tree backup instead of losing files. Import still reads 1–4, and the import route migrates them right after.
16. **`KitFS` returns `(billy.Filesystem, bool)`.** It takes a `KitReader` (`GetKit`, `KitTree`, `KitBlob`), which a `Store`, a planner's `memState` and a kit set's overlay all satisfy.
17. **The build gets the files through `KitLink.Files`.** Each chain link carries the version's billy view. The launcher buffers the context tar (≤ 64 MiB) and hands it to the existing `ResolveKitBaseTar`/`ResolveKitBaseTarOn`. It is still in-process, and a remote launcher would ship the tree instead.
18. **A tree holds files only, the whole directory.** A subdirectory with its own `kit.yml` is still part of the parent directory's tree. Exclude it with `.dockerignore`.
19. **Unsafe names are never echoed.** A path with a control or format character, invalid UTF-8 or a `.`/`..` segment is refused as `<dir>: a file has an unsafe path (…)`, without the name.
20. **Slice 2 is folded in.** Task 11 puts the tree file list and diff on the kit page. Task 12 adds `GET /admin/kits/{name}/diff`, `at-jam kit diff` and a per-kit diff on config plan ops. The CLI, the API and plans render `kittree.DiffText`. The kit page keeps its HTML renderer (rows, links to `&file=`), fed from the two trees, with the same change kinds (added, removed, modified, mode).
21. **Inspect.** The `kit` section's provenance table gains a `FILES` column (`tree <12 hex>, N files`). The `seed` section reads the built image's `/home/agent/.init-agent-data`, which a kit tree does not hold, so it is unchanged.
22. **Parse cache.** `LoadKitVersion` caches successful tree parses by tree hash, unbounded: trees are immutable and few. Failed parses are not cached.
23. **GC keeps no draft roots in slice 1:** drafts do not exist yet.

## Global Constraints

- **Tree (spec §1, verbatim):** `path`: clean, slash-separated, relative, valid UTF-8, no control or format characters, no `.`/`..` segments; `kit.yml` must exist at the root. `mode`: `0644` or `0755` only; any executable bit on input becomes `0755`, otherwise `0644`. Regular files only: no symlinks, hardlinks, devices or directory entries. Limits: ≤ 2000 entries, ≤ 64 MiB total, ≤ 4 MiB per file.
- **Tree hash (verbatim):** "`sha256` over the entries sorted by path, each encoded as `path\0mode-octal\0blob-sha256\n`. Identical directories share one tree."
- **`.dockerignore` (verbatim):** "Applied on the way in (push, git import, config apply, draft sync): ignored files are never stored. `kit.yml`, `Dockerfile` and `.dockerignore` are always kept."
- **No `context` field (verbatim):** "A `Dockerfile` next to `kit.yml` makes the directory the build context. `context`, `context-files` and `context-dir` are refused on push."
- **Build tar (verbatim):** "deterministic: sorted, epoch mtime, fixed uid/gid", streamed to `docker build -`; a child still builds `FROM ${COVE_BASE_IMAGE}`, its parent's stage.
- **Error texts (spec §5, exact where the spec gives them):** `kits/app/run.sh: symlinks are not allowed`, `kit app: kit.yml missing at the root`, `kit app: 2001 files (limit 2000)`, `base.context is no longer supported; put a Dockerfile next to kit.yml` (and the same for `base.context-files` and `base.context-dir`). Errors are fixed text, located by path, and never echo file contents.
- **Other fixed texts this plan adds:** `<path>: hard links are not allowed`, `<path>: not a regular file`, `<path>: appears twice`, `<path>: is both a file and a directory`, `<path>: more than 4 MiB (limit 4 MiB per file)`, `more than 64 MiB in total (limit 64 MiB)`, `<dir>: a file has an unsafe path (empty, absolute, a . or .. segment, invalid UTF-8, or a control or format character)`, `<dir>: not a directory`, `not a readable tar`, `kit upload over <N> bytes`.
- **Migration (verbatim):** "At serve start, once, in one transaction, logged `kits migrated to trees`". A version whose context cannot be read is kept as a **broken** version (raising it fails closed) and raises the attention condition `kit.migrate:<name>`.
- Diff: the `kit.yml` line diff followed by a file list (added, removed, modified with line diff, mode); no base64 is shown anywhere.
- Tests are hermetic (`runner.Fake`, MemStore, `t.TempDir`). Postgres tests stay behind the `integration` tag. Use `GOPROXY=https://proxy.golang.org,direct` for `go` commands here.
- Every commit builds and passes `go test ./...` and `just lint`.
- In the doc-edit steps, the form <link `target` as "text"> stands for the markdown link `[text](target)` to write. The plan spells it that way so its own link check stays clean.
- **Docs** change in the same task as the behaviour (progressive disclosure: edit the doc that owns the subject, no copies). `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1` must report no more than **733** errors: 731 at the spec commit plus 2 for this plan file.

## Review Focus

1. **A hostile kit directory**, from `kit push`, a git checkout or a config repo, holds a symlink (to `/etc/passwd`, or to a file inside), a FIFO, a symlinked kit dir, or a file named with an escape character. It must be refused with a path-located message. Nothing is followed, read beyond the check, or echoed. *(Task 2: `TestReadDirRefusesSymlink`, `TestReadDirRefusesFIFO`, `TestReadDirRefusesSymlinkedKitDir`, `TestReadDirUnsafeNameNotEchoed`; Task 10: `TestLoadRefusesSymlinkInKitDir`.)*
2. **A hand-made push tar** (`tar -C dir -cf - .`) has `./` prefixes and directory entries. Others have a duplicate name, a hard link, a device, more than 2000 files, or a body over the cap. Each is accepted as files or refused with the fixed text, and an oversize body answers 413. *(Task 2: `TestReadTar*`; Task 9: `TestPushTarRoute`.)*
3. **The config round trip** is write → commit → clone → apply, and must be `unchanged`. That includes `0755` scripts, a `.dockerignore`d file present on disk, and a kit whose parent is a sibling in the tree (`../web` on disk, `web@vN` stored). *(Task 10: `TestConfigTreeRoundTripUnchanged`.)*
4. **Upgrading a populated Jam** must behave as follows:
   - The default kit gets no new version.
   - A context kit becomes a tree whose image goes stale once.
   - An unreadable context becomes a broken version: raising it fails with its reason, and `kit.migrate:<name>` is open.
   - A managed kit shows no drift.
   - A second start is a no-op.
   
   *(Task 13: `TestMigrateKitTrees*`.)*
5. **Editing only `prompt` in a `Dockerfile` kit's `kit.yml`** reuses the image. Editing its `Dockerfile` rebuilds. A chain child's own stage is cached per build context. *(Task 4: `TestBuildDigestBuildContext`; Task 8: `TestStageTagKeyedOnBuildContext`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/kittree/tree.go` (create) | `Entry`, `Tree`, `File`, `Blobs`, `MapBlobs`, `NormMode`, `CheckPath`, `Build`, `Hash`, `Lookup`, `Without`, `ContextHash`, `Validate`, limits |
| `internal/kittree/ingest.go` (create) | `DockerignoreMatcher`, `Ignorer`, `NewIgnorer`, `Ignore`, `ReadDir`, `ReadTar`, `WriteTar`, `MaxUpload` |
| `internal/kittree/fs.go` (create) | `NewFS`: the read-only billy view |
| `internal/kittree/context.go` (create) | `ContextTar`: the deterministic build-context tar |
| `internal/kittree/diff.go` (create) | `Change`, `Changes`, `DiffText`, `IsText` |
| `internal/studio/tree.go` (create) | `ParseKitFS`, `ParseTree`, `RefuseContextFields`, `RenderKitYAML`, `SetBaseKit` |
| `internal/studio/studiokit.go`, `digest.go`, `pack.go` (modify) | `StudioKit.BuildContext`; the digest; `DockerignoreMatcher` delegates |
| `internal/jam/kit_objects.go` (create) | `KitObjects`, `KitGC`, `Kit.HasVersion`/`VersionNumbers`, object checks/apply/GC, `KitTree`/`KitBlob` reads |
| `internal/jam/identity.go`, `store.go`, `memstate.go`, `memstore.go`, `pgstore.go`, `pgstore_config.go`, `pgstore_testhelpers.go`, `config_marks.go`, `config_snapshot.go` (modify); `migrations/0019_kit_trees.sql` (create) | the store |
| `internal/jam/storetest/kits_tree.go` (create), `conformance.go`, `kits_set.go` (modify) | conformance |
| `internal/jam/kit_version.go` (create) | `KitReader`, `KitFS`, `KitVersion`, `LoadKitVersion`, the parse cache |
| `internal/jam/kitchain.go`, `studiokit.go`, `supervisor.go`, `admin.go`, `setup_image_bodies.go`, `config_plan_delete.go` (modify) | readers |
| `internal/jam/launcher/prepare.go` (modify) | tree builds |
| `internal/jam/kit_push.go` (create) | `PushKitFiles`, `ReplaceKitYML`, `kitTreeOf` |
| `internal/jam/adminclient/adminclient.go`, `cmd/at-jam/kitpush.go` (create), `cmd/at-jam/main.go` (modify) | push, show and diff client and CLI |
| `internal/jam/gitsource/load.go`, `internal/jam/kitset.go`, `kitsource.go`, `config_tree.go`, `config_plan.go`, `config_files.go`, `config_export.go`, `config_admin.go`, `cmd/at-jam/configgit.go` (modify) | checkouts and config |
| `internal/jam/adminui/kit_files.go` (create), `kit_context.go`, `kit_detail.go`, `kits.go`, `attention.go`, `search.go`, `config_page.go`, `templates/kit.html`, `templates/config.html` (modify) | UI |
| `internal/jam/kit_diff.go` (create) | `GET /admin/kits/{name}/diff`, plan kit diffs |
| `internal/jam/kit_migrate.go`, `kit_migrate_legacy.go` (create); `internal/jam/testdata/kitmigrate/*.golden` (create) | migration |
| deleted in Task 14 | `internal/studio/{pack,scan,context_files,unpack}.go` and their tests, the packed-context parts of `base.go`, `jail.go`, `merge.go`, the launcher, the adminui, `config_export_context_test.go` |
| `docs/usage/jam/kits-directory.md` (create), `docs/usage/jam/INDEX.md` (row) | the kit directory, its limits, tree storage, the migration |
| docs (modify) | `kits.md`, `kits-inheritance.md`, `kits-git.md`, `kits-path-parents.md`, `config-git.md`, `ui-pages.md`, `inspect.md`, `backup.md`, `monitoring.md`, `coves.md` (one sentence) |

---
### Task 1: The tree model (`internal/kittree`)

**Files:**
- Create: `internal/kittree/tree.go`
- Test: `internal/kittree/tree_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
```go
package kittree
const KitFile = "kit.yml"
const Dockerfile = "Dockerfile"
var MaxEntries = 2000; var MaxTotal int64 = 64 << 20; var MaxFile int64 = 4 << 20
type Entry struct { Path string `json:"path"`; Mode fs.FileMode `json:"mode"`; SHA256 string `json:"sha256"`; Size int64 `json:"size"` }
type Tree struct { Entries []Entry `json:"entries"` }
type File struct { Path string; Mode fs.FileMode; Data []byte }
type Blobs func(sha string) ([]byte, bool)
func MapBlobs(m map[string][]byte) Blobs
func NormMode(m fs.FileMode) fs.FileMode
func CheckPath(p string) error
func Build(files []File) (Tree, map[string][]byte, error)
func (t Tree) Hash() string
func (t Tree) Lookup(p string) (Entry, bool)
func (t Tree) Without(p string) Tree
func (t Tree) ContextHash() string // "" without a root Dockerfile
func (t Tree) Size() int64
func (t Tree) Validate() error
func SizeText(n int64) string // "4 MiB", "16 bytes"
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/kittree/tree_test.go
package kittree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// withLimits shrinks the tree limits for one test.
func withLimits(t *testing.T, entries int, total, file int64) {
	t.Helper()
	e, to, f := MaxEntries, MaxTotal, MaxFile
	MaxEntries, MaxTotal, MaxFile = entries, total, file
	t.Cleanup(func() { MaxEntries, MaxTotal, MaxFile = e, to, f })
}

func kitFiles(extra ...File) []File {
	return append([]File{{Path: KitFile, Mode: 0o644, Data: []byte("kind: studio\n")}}, extra...)
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestBuildSortsNormalizesAndHashes(t *testing.T) {
	tr, blobs, err := Build([]File{
		{Path: "run.sh", Mode: 0o700, Data: []byte("echo hi\n")},
		{Path: KitFile, Mode: 0o600, Data: []byte("kind: studio\n")},
		{Path: "sub/data.txt", Mode: 0o664, Data: []byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Path: KitFile, Mode: 0o644, SHA256: hexSum([]byte("kind: studio\n")), Size: 13},
		{Path: "run.sh", Mode: 0o755, SHA256: hexSum([]byte("echo hi\n")), Size: 8},
		{Path: "sub/data.txt", Mode: 0o644, SHA256: hexSum([]byte("x")), Size: 1},
	}
	if fmt.Sprint(tr.Entries) != fmt.Sprint(want) {
		t.Fatalf("entries = %+v\nwant %+v", tr.Entries, want)
	}
	if len(blobs) != 3 || string(blobs[hexSum([]byte("x"))]) != "x" {
		t.Fatalf("blobs = %v", blobs)
	}
	if tr.Size() != 22 {
		t.Fatalf("size = %d", tr.Size())
	}
}

func TestHashEncoding(t *testing.T) {
	tr, _, err := Build([]File{{Path: KitFile, Data: []byte("a")}, {Path: "x.sh", Mode: 0o755, Data: []byte("b")}})
	if err != nil {
		t.Fatal(err)
	}
	enc := "kit.yml\x00644\x00" + hexSum([]byte("a")) + "\n" + "x.sh\x00755\x00" + hexSum([]byte("b")) + "\n"
	if got := tr.Hash(); got != hexSum([]byte(enc)) {
		t.Fatalf("hash = %s, want sha256 of %q", got, enc)
	}
	same, _, _ := Build([]File{{Path: "x.sh", Mode: 0o711, Data: []byte("b")}, {Path: KitFile, Mode: 0o600, Data: []byte("a")}})
	if same.Hash() != tr.Hash() {
		t.Fatal("identical directories must share one tree hash")
	}
	other, _, _ := Build([]File{{Path: KitFile, Data: []byte("a")}, {Path: "x.sh", Mode: 0o644, Data: []byte("b")}})
	if other.Hash() == tr.Hash() {
		t.Fatal("a mode change must change the hash")
	}
}

func TestBuildRefusals(t *testing.T) {
	big := make([]File, 0, 2001)
	big = append(big, kitFiles()...)
	for i := 1; i < 2001; i++ {
		big = append(big, File{Path: fmt.Sprintf("f%04d", i), Data: []byte("x")})
	}
	for name, tc := range map[string]struct {
		files []File
		want  string
	}{
		"no kit.yml":     {[]File{{Path: "Dockerfile", Data: []byte("FROM x")}}, "kit.yml missing at the root"},
		"nested kit.yml": {[]File{{Path: "a/kit.yml", Data: []byte("x")}}, "kit.yml missing at the root"},
		"duplicate":      {kitFiles(File{Path: "a", Data: []byte("1")}, File{Path: "a", Data: []byte("2")}), "a: appears twice"},
		"file and dir":   {kitFiles(File{Path: "a", Data: []byte("1")}, File{Path: "a/b", Data: []byte("2")}), "a: is both a file and a directory"},
		"2001 files":     {big, "2001 files (limit 2000)"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Build(tc.files); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBuildRefusesUnsafePathsWithoutEchoing(t *testing.T) {
	hostile := map[string]bool{"bad\x1bname": true, "rtl\u202e.sh": true, "\xff": true}
	for _, p := range []string{"", ".", "/abs", "a/../b", "a//b", "a/", "./a", "bad\x1bname", "rtl\u202e.sh", "\xff"} {
		_, _, err := Build(kitFiles(File{Path: p, Data: []byte("x")}))
		if err == nil || !strings.HasPrefix(err.Error(), "a file has an unsafe path") {
			t.Fatalf("%q: err = %v", p, err)
		}
		if hostile[p] && strings.Contains(err.Error(), p) {
			t.Fatalf("%q: the error echoes the name: %v", p, err)
		}
	}
	if _, _, err := Build(kitFiles(File{Path: "with space/ok.txt", Data: []byte("x")})); err != nil {
		t.Fatalf("spaces are allowed: %v", err)
	}
}

func TestBuildSizeLimits(t *testing.T) {
	withLimits(t, 2000, 32, 16)
	if _, _, err := Build(kitFiles(File{Path: "big", Data: make([]byte, 17)})); err == nil || err.Error() != "big: more than 16 bytes (limit 16 bytes per file)" {
		t.Fatalf("per file: %v", err)
	}
	if _, _, err := Build(kitFiles(File{Path: "a", Data: make([]byte, 16)}, File{Path: "b", Data: make([]byte, 16)})); err == nil || err.Error() != "more than 32 bytes in total (limit 32 bytes)" {
		t.Fatalf("total: %v", err)
	}
	if SizeText(4<<20) != "4 MiB" || SizeText(16) != "16 bytes" {
		t.Fatal(SizeText(4<<20), SizeText(16))
	}
}

func TestContextHash(t *testing.T) {
	plain, _, _ := Build(kitFiles())
	if plain.ContextHash() != "" {
		t.Fatal("no Dockerfile: no build context")
	}
	a, _, _ := Build(kitFiles(File{Path: Dockerfile, Data: []byte("FROM x\n")}))
	if a.ContextHash() != a.Without(KitFile).Hash() || a.ContextHash() == "" {
		t.Fatal("the context is the tree minus kit.yml")
	}
	b, _, _ := Build([]File{{Path: KitFile, Data: []byte("kind: studio\nprompt: other\n")}, {Path: Dockerfile, Data: []byte("FROM x\n")}})
	if b.ContextHash() != a.ContextHash() {
		t.Fatal("editing kit.yml must not change the context")
	}
	c, _, _ := Build(kitFiles(File{Path: Dockerfile, Data: []byte("FROM y\n")}))
	if c.ContextHash() == a.ContextHash() {
		t.Fatal("editing the Dockerfile must change the context")
	}
}

func TestValidateRejectsTamperedTrees(t *testing.T) {
	good, _, _ := Build(kitFiles(File{Path: "z", Data: []byte("1")}))
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	swap := Tree{Entries: []Entry{good.Entries[1], good.Entries[0]}}
	mode := Tree{Entries: append([]Entry(nil), good.Entries...)}
	mode.Entries[1].Mode = 0o600
	sum := Tree{Entries: append([]Entry(nil), good.Entries...)}
	sum.Entries[1].SHA256 = "zz"
	noKit := Tree{Entries: good.Entries[1:]}
	for name, tr := range map[string]Tree{"order": swap, "mode": mode, "sha": sum, "kit.yml": noKit} {
		if tr.Validate() == nil {
			t.Fatalf("%s: tampered tree validated", name)
		}
	}
	if e, ok := good.Lookup("z"); !ok || e.Size != 1 {
		t.Fatal("Lookup")
	}
	if _, ok := good.Lookup("y"); ok {
		t.Fatal("Lookup of a missing path")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/kittree/`
Expected: FAIL, package does not compile (`undefined: Build`, …).

- [ ] **Step 3: Implement**

```go
// internal/kittree/tree.go

// Package kittree is a kit version's files as a content-addressed tree: the
// model, its hash and limits, reading one in from a directory or a push tar,
// writing a push body and a build context, a read-only billy view, and the
// version diff. It holds no store: blobs come in through a Blobs lookup.
package kittree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// KitFile is the kit definition at a tree's root.
const KitFile = "kit.yml"

// Dockerfile, next to kit.yml, makes the tree a build context.
const Dockerfile = "Dockerfile"

// The limits of one tree (spec §1). Vars so tests can shrink them; treat them
// as constants.
var (
	MaxEntries       = 2000
	MaxTotal   int64 = 64 << 20
	MaxFile    int64 = 4 << 20
)

// Entry is one file of a tree. Size is not part of the hash.
type Entry struct {
	Path   string      `json:"path"`
	Mode   fs.FileMode `json:"mode"`   // 0o644 or 0o755
	SHA256 string      `json:"sha256"` // hex sha256 of the content
	Size   int64       `json:"size"`
}

// Tree is a version's files, sorted by path.
type Tree struct {
	Entries []Entry `json:"entries"`
}

// File is one file with its content, on its way into a tree.
type File struct {
	Path string
	Mode fs.FileMode
	Data []byte
}

// Blobs looks a blob's bytes up by its hex sha256.
type Blobs func(sha string) ([]byte, bool)

// MapBlobs is a Blobs over m.
func MapBlobs(m map[string][]byte) Blobs {
	return func(sha string) ([]byte, bool) {
		b, ok := m[sha]
		return b, ok
	}
}

// NormMode keeps the two modes git keeps: any executable bit makes 0755.
func NormMode(m fs.FileMode) fs.FileMode {
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

var errUnsafePath = errors.New("a file has an unsafe path (empty, absolute, a . or .. segment, invalid UTF-8, or a control or format character)")

// CheckPath reports whether p is a clean, slash-separated, relative path of
// valid UTF-8 with no control or format character and no . or .. segment.
// Its error never names p.
func CheckPath(p string) error {
	if p == "" || p == "." || !utf8.ValidString(p) || !fs.ValidPath(p) {
		return errUnsafePath
	}
	for _, r := range p {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return errUnsafePath
		}
	}
	return nil
}

// SizeText renders a limit: whole MiB as "N MiB", else "N bytes".
func SizeText(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return strconv.FormatInt(n>>20, 10) + " MiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

func tooManyFiles(n int) error { return fmt.Errorf("%d files (limit %d)", n, MaxEntries) }

func fileTooLarge(p string) error {
	return fmt.Errorf("%s: more than %s (limit %s per file)", p, SizeText(MaxFile), SizeText(MaxFile))
}

func treeTooLarge() error {
	return fmt.Errorf("more than %s in total (limit %s)", SizeText(MaxTotal), SizeText(MaxTotal))
}

// Build validates files as one tree, normalizing modes, and returns it with
// its blobs by sha256. Identical content shares one blob.
func Build(files []File) (Tree, map[string][]byte, error) {
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
			return Tree{}, nil, fileTooLarge(f.Path)
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
	if err := t.Validate(); err != nil {
		return Tree{}, nil, err
	}
	return t, blobs, nil
}

// Hash is the tree's identity: sha256 over the entries in path order, each
// "path NUL mode NUL blob-sha256 LF", mode in octal without a leading zero.
func (t Tree) Hash() string {
	h := sha256.New()
	for _, e := range t.Entries {
		h.Write([]byte(e.Path))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatUint(uint64(e.Mode.Perm()), 8)))
		h.Write([]byte{0})
		h.Write([]byte(e.SHA256))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Lookup finds the entry at p.
func (t Tree) Lookup(p string) (Entry, bool) {
	i, ok := slices.BinarySearchFunc(t.Entries, p, func(e Entry, p string) int { return strings.Compare(e.Path, p) })
	if !ok {
		return Entry{}, false
	}
	return t.Entries[i], true
}

// Without is t less the entry at p.
func (t Tree) Without(p string) Tree {
	return Tree{Entries: slices.DeleteFunc(slices.Clone(t.Entries), func(e Entry) bool { return e.Path == p })}
}

// ContextHash is the hash of the build context (the tree minus kit.yml) when
// a Dockerfile sits next to kit.yml; "" when none does.
func (t Tree) ContextHash() string {
	if _, ok := t.Lookup(Dockerfile); !ok {
		return ""
	}
	return t.Without(KitFile).Hash()
}

// Size is the total of the entries' sizes.
func (t Tree) Size() int64 {
	var n int64
	for _, e := range t.Entries {
		n += e.Size
	}
	return n
}

// Validate checks a tree read back from storage or a backup: sorted unique
// safe paths, the two modes, hex sums, no file where another file's
// directory is, kit.yml at the root, and the limits.
func (t Tree) Validate() error {
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
		if b, err := hex.DecodeString(e.SHA256); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("%s: not a sha256", e.Path)
		}
		if e.Size < 0 || e.Size > MaxFile {
			return fileTooLarge(e.Path)
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
	if _, ok := t.Lookup(KitFile); !ok {
		return errors.New("kit.yml missing at the root")
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/kittree/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kittree/tree.go internal/kittree/tree_test.go
git commit -m "kittree: the content-addressed kit tree model, hash and limits

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Reading a tree in, writing a push body (`internal/kittree`)

**Files:**
- Create: `internal/kittree/ingest.go`
- Modify: `internal/studio/pack.go` (`DockerignoreMatcher` delegates to kittree's)
- Test: `internal/kittree/ingest_test.go`, `internal/kittree/ingest_unix_test.go`

**Interfaces:**
- Consumes: Task 1's `File`, `CheckPath`, `NormMode`, limits, `KitFile`, `Dockerfile`.
- Produces:
```go
func DockerignoreMatcher(r io.Reader) (*patternmatcher.PatternMatcher, error)
type Ignorer struct{ /* unexported */ }
func NewIgnorer(rules []byte) (Ignorer, error)       // nil rules: ignores nothing
func (ig Ignorer) Ignored(rel string) (bool, error)  // kit.yml, Dockerfile, .dockerignore at the root: never
func (ig Ignorer) Prunes() bool                      // an ignored dir may be skipped whole
func Ignore(files []File) ([]File, error)
func ReadDir(root *os.Root, dir string) ([]File, error)
func ReadTar(r io.Reader) ([]File, error)
func WriteTar(w io.Writer, files []File) error
func MaxUpload() int64
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/kittree/ingest_test.go
package kittree

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTree creates files (path → content; a path ending in "*" is 0755) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, "*") {
			p, mode = strings.TrimSuffix(p, "*"), 0o755
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func paths(files []File) string {
	var ps []string
	for _, f := range files {
		ps = append(ps, f.Path+":"+f.Mode.String())
	}
	return strings.Join(ps, " ")
}

func TestReadDirReadsFilesWithModes(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "kind: studio\n", "kits/app/bin/run.sh*": "echo", "kits/app/data": "x", "kits/other/kit.yml": "no"})
	files, err := ReadDir(openRoot(t, root), "kits/app")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); got != "bin/run.sh:-rwxr-xr-x data:-rw-r--r-- kit.yml:-rw-r--r--" {
		t.Fatalf("files = %s", got)
	}
}

func TestReadDirAppliesDockerignore(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"kit.yml": "kind: studio\n", "Dockerfile": "FROM x\n", ".dockerignore": "*.log\nnode_modules\nkit.yml\nDockerfile\n.dockerignore\n!keep.log\n",
		"a.log": "1", "keep.log": "2", "node_modules/x/y.js": "3", "src/main.go": "4",
	})
	files, err := ReadDir(openRoot(t, root), ".")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); got != ".dockerignore:-rw-r--r-- Dockerfile:-rw-r--r-- keep.log:-rw-r--r-- kit.yml:-rw-r--r-- src/main.go:-rw-r--r--" {
		t.Fatalf("files = %s", got)
	}
}

func TestReadDirIgnoredFilesDoNotCount(t *testing.T) {
	withLimits(t, 2, 1<<20, 1<<20)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kit.yml": "k", ".dockerignore": "big/\n", "big/1": "1", "big/2": "2", "big/3": "3"})
	if _, err := ReadDir(openRoot(t, root), "."); err != nil {
		t.Fatalf("ignored files counted against the limit: %v", err)
	}
	writeTree(t, root, map[string]string{"c": "x"})
	if _, err := ReadDir(openRoot(t, root), "."); err == nil || err.Error() != "3 files (limit 2)" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDirRefusesSymlink(t *testing.T) {
	for _, target := range []string{"/etc/passwd", "kit.yml"} {
		root := t.TempDir()
		writeTree(t, root, map[string]string{"kits/app/kit.yml": "k"})
		if err := os.Symlink(target, filepath.Join(root, "kits/app/run.sh")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app/run.sh: symlinks are not allowed" {
			t.Fatalf("%s: err = %v", target, err)
		}
	}
}

func TestReadDirRefusesSymlinkedKitDir(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"real/kit.yml": "k"})
	if err := os.MkdirAll(filepath.Join(root, "kits"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../real", filepath.Join(root, "kits/app")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app: not a directory" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDirUnsafeNameNotEchoed(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k", "kits/app/bad\x1b[31mname": "x"})
	_, err := ReadDir(openRoot(t, root), "kits/app")
	if err == nil || !strings.HasPrefix(err.Error(), "kits/app: a file has an unsafe path") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("err = %q", err)
	}
}

func TestReadDirFileTooLarge(t *testing.T) {
	withLimits(t, 2000, 1<<20, 4)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kit.yml": "k", "big": "12345"})
	if _, err := ReadDir(openRoot(t, root), "."); err == nil || err.Error() != "big: more than 4 bytes (limit 4 bytes per file)" {
		t.Fatalf("err = %v", err)
	}
}

// tarOf builds a tar from headers; a header's Size is set from body.
func tarOf(t *testing.T, ents ...struct {
	hdr  tar.Header
	body string
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range ents {
		h := e.hdr
		h.Size = int64(len(e.body))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type ent = struct {
	hdr  tar.Header
	body string
}

func TestReadTarAcceptsTarOfADirectory(t *testing.T) {
	b := tarOf(t,
		ent{tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}, ""},
		ent{tar.Header{Name: "./kit.yml", Typeflag: tar.TypeReg, Mode: 0o600}, "kind: studio\n"},
		ent{tar.Header{Name: "./bin/", Typeflag: tar.TypeDir, Mode: 0o755}, ""},
		ent{tar.Header{Name: "./bin/run.sh", Typeflag: tar.TypeReg, Mode: 0o700}, "echo"},
	)
	files, err := ReadTar(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(files); got != "kit.yml:-rw-r--r-- bin/run.sh:-rwxr-xr-x" {
		t.Fatalf("files = %s", got)
	}
}

func TestReadTarRefusals(t *testing.T) {
	reg := func(n string) ent { return ent{tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644}, "x"} }
	for name, tc := range map[string]struct {
		b    []byte
		want string
	}{
		"duplicate": {tarOf(t, reg("a"), reg("./a")), "a: appears twice"},
		"symlink":   {tarOf(t, ent{tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, ""}), "l: symlinks are not allowed"},
		"hardlink":  {tarOf(t, ent{tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "a"}, ""}), "h: hard links are not allowed"},
		"device":    {tarOf(t, ent{tar.Header{Name: "d", Typeflag: tar.TypeChar}, ""}), "d: not a regular file"},
		"escape":    {tarOf(t, reg("../x")), "a file has an unsafe path (empty, absolute, a . or .. segment, invalid UTF-8, or a control or format character)"},
		"garbage":   {[]byte("not a tar at all, not a tar at all, not a tar at all"), "not a readable tar"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadTar(bytes.NewReader(tc.b)); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadTarKeepsTheBodyLimitError(t *testing.T) {
	b := tarOf(t, ent{tar.Header{Name: "kit.yml", Typeflag: tar.TypeReg}, strings.Repeat("x", 4096)})
	r := http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(b)), 1024)
	_, err := ReadTar(r)
	if _, ok := errors.AsType[*http.MaxBytesError](err); !ok || err.Error() != "not a readable tar" {
		t.Fatalf("err = %v (%T)", err, err)
	}
}

func TestWriteTarIsDeterministicAndReadsBack(t *testing.T) {
	files := []File{{Path: "z.sh", Mode: 0o700, Data: []byte("z")}, {Path: KitFile, Mode: 0o644, Data: []byte("k")}}
	var a, b bytes.Buffer
	if err := WriteTar(&a, files); err != nil {
		t.Fatal(err)
	}
	if err := WriteTar(&b, []File{files[1], files[0]}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("WriteTar is not deterministic")
	}
	tr := tar.NewReader(bytes.NewReader(a.Bytes()))
	hdr, err := tr.Next()
	if err != nil || hdr.Name != KitFile || !hdr.ModTime.Equal(time.Unix(0, 0)) || hdr.Uid != 0 || hdr.Uname != "" {
		t.Fatalf("first header %+v, %v", hdr, err)
	}
	got, err := ReadTar(bytes.NewReader(a.Bytes()))
	if err != nil || paths(got) != "kit.yml:-rw-r--r-- z.sh:-rwxr-xr-x" {
		t.Fatalf("read back %s, %v", paths(got), err)
	}
}

func TestIgnoreFiltersByTheRootDockerignore(t *testing.T) {
	files := []File{{Path: KitFile, Data: []byte("k")}, {Path: ".dockerignore", Data: []byte("*.tmp\n")}, {Path: "a.tmp"}, {Path: "b/c.tmp"}, {Path: "keep"}}
	got, err := Ignore(files)
	if err != nil || paths(got) != "kit.yml:---------- .dockerignore:---------- b/c.tmp:---------- keep:----------" {
		t.Fatalf("got %s, %v", paths(got), err)
	}
}
```

`*.tmp` matches only at the root, the way docker matches it, so `b/c.tmp` stays. The test pins that parity.

```go
// internal/kittree/ingest_unix_test.go
//go:build unix

package kittree

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadDirRefusesFIFO(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kits/app/kit.yml": "k"})
	if err := syscall.Mkfifo(filepath.Join(root, "kits/app/pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDir(openRoot(t, root), "kits/app"); err == nil || err.Error() != "kits/app/pipe: not a regular file" {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/kittree/`
Expected: FAIL, `undefined: ReadDir` (and the other new names).

- [ ] **Step 3: Implement**

```go
// internal/kittree/ingest.go
package kittree

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// dockerignoreFile is the ignore file at a tree's root.
const dockerignoreFile = ".dockerignore"

// DockerignoreMatcher parses .dockerignore rules exactly as docker build
// reads them.
func DockerignoreMatcher(r io.Reader) (*patternmatcher.PatternMatcher, error) {
	patterns, err := ignorefile.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return patternmatcher.New(patterns)
}

// Ignorer is a root .dockerignore.
type Ignorer struct{ pm *patternmatcher.PatternMatcher }

// NewIgnorer parses rules; nil rules ignore nothing.
func NewIgnorer(rules []byte) (Ignorer, error) {
	pm, err := DockerignoreMatcher(bytes.NewReader(rules))
	if err != nil {
		return Ignorer{}, errors.New(".dockerignore: cannot be parsed")
	}
	return Ignorer{pm: pm}, nil
}

// Ignored reports whether rel (slash, relative to the root) is excluded.
// kit.yml, Dockerfile and .dockerignore at the root never are.
func (ig Ignorer) Ignored(rel string) (bool, error) {
	if rel == KitFile || rel == Dockerfile || rel == dockerignoreFile {
		return false, nil
	}
	ok, err := ig.pm.MatchesOrParentMatches(rel)
	if err != nil {
		return false, errors.New(".dockerignore: cannot be applied")
	}
	return ok, nil
}

// Prunes reports whether an ignored directory can be skipped whole: no `!`
// rule could re-include something beneath it.
func (ig Ignorer) Prunes() bool { return !ig.pm.Exclusions() }

// Ignore drops the files the .dockerignore among files excludes.
func Ignore(files []File) ([]File, error) {
	var rules []byte
	for _, f := range files {
		if f.Path == dockerignoreFile {
			rules = f.Data
		}
	}
	ig, err := NewIgnorer(rules)
	if err != nil {
		return nil, err
	}
	out := make([]File, 0, len(files))
	for _, f := range files {
		ignored, err := ig.Ignored(f.Path)
		if err != nil {
			return nil, err
		}
		if !ignored {
			out = append(out, f)
		}
	}
	return out, nil
}

// ReadDir reads the kit directory dir (slash, relative to root; "." for the
// root) of an untrusted tree into files. Its .dockerignore is applied as the
// walk goes, so an ignored file is never read nor counted. Symlinks (the kit
// directory itself included) and irregular files are refused, modes are
// normalized and the tree limits hold. Errors name paths as dir/rel and never
// echo content or an unsafe name.
func ReadDir(root *os.Root, dir string) ([]File, error) {
	dir = path.Clean(dir)
	fi, err := root.Lstat(filepath.FromSlash(dir))
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s: not a directory", dir)
	}
	ig, err := readIgnorer(root, dir)
	if err != nil {
		return nil, err
	}
	var files []File
	var total int64
	err = fs.WalkDir(root.FS(), dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return fmt.Errorf("%s: cannot be read", dir)
		}
		if p == dir {
			return nil
		}
		rel := p
		if dir != "." {
			rel = strings.TrimPrefix(p, dir+"/")
		}
		if err := CheckPath(rel); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		ignored, err := ig.Ignored(rel)
		if err != nil {
			return fmt.Errorf("%s/%w", dir, err)
		}
		if ignored {
			if d.IsDir() && ig.Prunes() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s: symlinks are not allowed", p)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s: not a regular file", p)
		}
		if len(files) == MaxEntries {
			return tooManyFiles(MaxEntries + 1)
		}
		data, mode, err := readRegular(root, p)
		if err != nil {
			return err
		}
		if total += int64(len(data)); total > MaxTotal {
			return treeTooLarge()
		}
		files = append(files, File{Path: rel, Mode: NormMode(mode), Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// readIgnorer reads dir's .dockerignore (a regular file of at most MaxFile
// bytes; absent: nothing is ignored).
func readIgnorer(root *os.Root, dir string) (Ignorer, error) {
	p := path.Join(dir, dockerignoreFile)
	fi, err := root.Lstat(filepath.FromSlash(p))
	if errors.Is(err, fs.ErrNotExist) {
		return NewIgnorer(nil)
	}
	if err != nil || !fi.Mode().IsRegular() {
		return Ignorer{}, fmt.Errorf("%s: not a regular file", p)
	}
	data, _, err := readRegular(root, p)
	if err != nil {
		return Ignorer{}, err
	}
	ig, err := NewIgnorer(data)
	if err != nil {
		return Ignorer{}, fmt.Errorf("%s/%w", dir, err)
	}
	return ig, nil
}

// readRegular reads p through root, non-blocking (a FIFO swapped in cannot
// hang it), judging the opened file: anything but a regular file, or over
// MaxFile, is refused.
func readRegular(root *os.Root, p string) ([]byte, fs.FileMode, error) {
	f, err := root.OpenFile(filepath.FromSlash(p), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: cannot be read", p)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s: not a regular file", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if err != nil {
		return nil, 0, fmt.Errorf("%s: cannot be read", p)
	}
	if int64(len(data)) > MaxFile {
		return nil, 0, fileTooLarge(p)
	}
	return data, fi.Mode(), nil
}

// MaxUpload caps a push body: the files, a header block per file with room
// for PAX records, and the end blocks.
func MaxUpload() int64 { return MaxTotal + int64(MaxEntries)*2048 + 1<<20 }

// tarError is a push body that cannot be read as a tar. Its text is fixed;
// it unwraps to the cause (a *http.MaxBytesError, say).
type tarError struct{ err error }

func (e *tarError) Error() string { return "not a readable tar" }
func (e *tarError) Unwrap() error { return e.err }

// ReadTar reads a push body: an uncompressed tar of regular files. A leading
// "./" is dropped and directory entries are skipped; links, devices and
// unsafe names are refused; the tree limits hold. It does not apply
// .dockerignore (Ignore does).
func ReadTar(r io.Reader) ([]File, error) {
	tr := tar.NewReader(r)
	var files []File
	var total int64
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &tarError{err}
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if err := CheckPath(name); err != nil {
			return nil, err
		}
		switch hdr.Typeflag {
		case tar.TypeReg:
		case tar.TypeSymlink:
			return nil, fmt.Errorf("%s: symlinks are not allowed", name)
		case tar.TypeLink:
			return nil, fmt.Errorf("%s: hard links are not allowed", name)
		default:
			return nil, fmt.Errorf("%s: not a regular file", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: appears twice", name)
		}
		seen[name] = true
		if len(files) == MaxEntries {
			return nil, tooManyFiles(MaxEntries + 1)
		}
		data, err := io.ReadAll(io.LimitReader(tr, MaxFile+1))
		if err != nil {
			return nil, &tarError{err}
		}
		if int64(len(data)) > MaxFile {
			return nil, fileTooLarge(name)
		}
		if total += int64(len(data)); total > MaxTotal {
			return nil, treeTooLarge()
		}
		files = append(files, File{Path: name, Mode: NormMode(fs.FileMode(hdr.Mode)), Data: data})
	}
	return files, nil
}

// WriteTar writes files as a deterministic uncompressed tar: sorted by path,
// epoch mtime, uid/gid 0, no user or group names, normalized modes.
func WriteTar(w io.Writer, files []File) error {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	tw := tar.NewWriter(w)
	for _, f := range sorted {
		if err := writeEntry(tw, f.Path, NormMode(f.Mode), f.Data); err != nil {
			return err
		}
	}
	return tw.Close()
}

// epoch is the fixed mtime of every entry: USTAR-representable, leaks no host time.
var epoch = time.Unix(0, 0)

func writeEntry(tw *tar.Writer, name string, mode fs.FileMode, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: int64(mode.Perm()), Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: epoch}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
```

In `internal/studio/pack.go`, replace the body of `DockerignoreMatcher` with a delegation, so the packer and kittree cannot disagree until Task 14 deletes the packer:

```go
// DockerignoreMatcher parses a .dockerignore the way the packer does (kittree's).
func DockerignoreMatcher(r io.Reader) (*patternmatcher.PatternMatcher, error) {
	return kittree.DockerignoreMatcher(r)
}
```

Add the import `"github.com/aethons-tools/cove/internal/kittree"` to `pack.go`, and drop the `ignorefile` import if it is now unused.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/kittree/ ./internal/studio/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kittree/ingest.go internal/kittree/ingest_test.go internal/kittree/ingest_unix_test.go internal/studio/pack.go
git commit -m "kittree: read a kit dir or push tar in (.dockerignore, no links), write a push tar

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The billy view, the build-context tar and the version diff (`internal/kittree`)

**Files:**
- Create: `internal/kittree/fs.go`, `internal/kittree/context.go`, `internal/kittree/diff.go`
- Test: `internal/kittree/fs_test.go`, `internal/kittree/context_test.go`, `internal/kittree/diff_test.go`

**Interfaces:**
- Consumes: Tasks 1–2 (`Tree`, `Entry`, `Blobs`, `writeEntry`, `epoch`, `KitFile`); `internal/jam/linediff.Diff`.
- Produces:
```go
func NewFS(t Tree, blobs Blobs) billy.Filesystem // read-only: writes return billy.ErrReadOnly
func ContextTar(w io.Writer, fsys billy.Filesystem) error // every file but kit.yml, deterministic
const ChangeAdded, ChangeRemoved, ChangeModified, ChangeMode = "added", "removed", "modified", "mode"
type Change struct { Path, Kind string; From, To Entry }
func Changes(a, b Tree) (changes []Change, unchanged int)
const MaxDiffFile = 64 << 10
func IsText(b []byte) bool
func DiffText(a, b Tree, blobs Blobs) string
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/kittree/fs_test.go
package kittree

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
)

func sampleFS(t *testing.T) (Tree, billy.Filesystem, map[string][]byte) {
	t.Helper()
	tr, blobs, err := Build([]File{{Path: KitFile, Data: []byte("kind: studio\n")}, {Path: "bin/run.sh", Mode: 0o755, Data: []byte("echo")}, {Path: "Dockerfile", Data: []byte("FROM x\n")}})
	if err != nil {
		t.Fatal(err)
	}
	return tr, NewFS(tr, MapBlobs(blobs)), blobs
}

func TestFSReads(t *testing.T) {
	_, fsys, _ := sampleFS(t)
	for _, name := range []string{"bin/run.sh", "/bin/run.sh", "./bin/run.sh"} {
		f, err := fsys.Open(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		if string(b) != "echo" {
			t.Fatalf("%s = %q", name, b)
		}
	}
	fi, err := fsys.Stat("bin/run.sh")
	if err != nil || fi.Size() != 4 || fi.Mode() != 0o755 || fi.IsDir() || fi.Name() != "run.sh" {
		t.Fatalf("stat %v %v", fi, err)
	}
	if fi, err := fsys.Lstat("bin"); err != nil || !fi.IsDir() {
		t.Fatalf("lstat bin %v %v", fi, err)
	}
	ents, err := fsys.ReadDir("/")
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if err != nil || strings.Join(names, ",") != "Dockerfile,bin,kit.yml" {
		t.Fatalf("readdir / = %v %v", names, err)
	}
	if _, err := fsys.Stat("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat missing: %v", err)
	}
	if _, err := fsys.Open("bin"); err == nil {
		t.Fatal("opening a directory must fail")
	}
}

func TestFSIsReadOnly(t *testing.T) {
	_, fsys, _ := sampleFS(t)
	ro := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, billy.ErrReadOnly) {
			t.Fatalf("%s: err = %v, want billy.ErrReadOnly", what, err)
		}
	}
	_, err := fsys.Create("x")
	ro("create", err)
	_, err = fsys.OpenFile("kit.yml", os.O_WRONLY, 0)
	ro("openfile write", err)
	ro("remove", fsys.Remove("kit.yml"))
	ro("rename", fsys.Rename("kit.yml", "y"))
	ro("mkdirall", fsys.MkdirAll("d", 0o755))
	ro("symlink", fsys.Symlink("kit.yml", "l"))
	_, err = fsys.TempFile("", "x")
	ro("tempfile", err)
	f, _ := fsys.Open("kit.yml")
	_, err = f.Write([]byte("x"))
	ro("file write", err)
	ro("truncate", f.Truncate(0))
}

func TestFSMissingBlob(t *testing.T) {
	tr, _, _ := sampleFS(t)
	fsys := NewFS(tr, func(string) ([]byte, bool) { return nil, false })
	if _, err := fsys.Open("kit.yml"); err == nil || !strings.Contains(err.Error(), "content missing") {
		t.Fatalf("err = %v", err)
	}
}
```

```go
// internal/kittree/context_test.go
package kittree

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
	"time"
)

func TestContextTarIsTreeMinusKitYML(t *testing.T) {
	tr, fsys, _ := sampleFS(t)
	var a, b bytes.Buffer
	if err := ContextTar(&a, fsys); err != nil {
		t.Fatal(err)
	}
	if err := ContextTar(&b, fsys); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("not deterministic")
	}
	want := tr.Without(KitFile).Entries
	r := tar.NewReader(&a)
	for i := 0; ; i++ {
		hdr, err := r.Next()
		if err == io.EOF {
			if i != len(want) {
				t.Fatalf("%d entries, want %d", i, len(want))
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r)
		e := want[i]
		if hdr.Name != e.Path || hdr.Mode != int64(e.Mode) || hexSum(data) != e.SHA256 || !hdr.ModTime.Equal(time.Unix(0, 0)) || hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "" || hdr.Gname != "" {
			t.Fatalf("entry %d = %+v", i, hdr)
		}
	}
}
```

```go
// internal/kittree/diff_test.go
package kittree

import (
	"strings"
	"testing"
)

func build(t *testing.T, files ...File) (Tree, map[string][]byte) {
	t.Helper()
	tr, blobs, err := Build(files)
	if err != nil {
		t.Fatal(err)
	}
	return tr, blobs
}

func TestChanges(t *testing.T) {
	a, _ := build(t, File{Path: KitFile, Data: []byte("k")}, File{Path: "gone", Data: []byte("1")}, File{Path: "mod", Data: []byte("a")}, File{Path: "same", Data: []byte("s")}, File{Path: "x.sh", Data: []byte("x")})
	b, _ := build(t, File{Path: KitFile, Data: []byte("k")}, File{Path: "mod", Data: []byte("b")}, File{Path: "new", Data: []byte("2")}, File{Path: "same", Data: []byte("s")}, File{Path: "x.sh", Mode: 0o755, Data: []byte("x")})
	ch, unchanged := Changes(a, b)
	var got []string
	for _, c := range ch {
		got = append(got, c.Kind+" "+c.Path)
	}
	if strings.Join(got, ", ") != "removed gone, modified mod, added new, mode x.sh" || unchanged != 2 {
		t.Fatalf("changes = %v, unchanged = %d", got, unchanged)
	}
}

func TestDiffText(t *testing.T) {
	big := strings.Repeat("y\n", MaxDiffFile)
	a, ab := build(t,
		File{Path: KitFile, Data: []byte("kind: studio\nprompt: a\n")},
		File{Path: "Dockerfile", Data: []byte("FROM a\n")},
		File{Path: "bin", Data: []byte{0, 1}},
		File{Path: "big.txt", Data: []byte(big)},
		File{Path: "old.txt", Data: []byte("o")},
		File{Path: "t.sh", Data: []byte("t")})
	b, bb := build(t,
		File{Path: KitFile, Data: []byte("kind: studio\nprompt: b\n")},
		File{Path: "Dockerfile", Data: []byte("FROM b\n")},
		File{Path: "bin", Data: []byte{0, 2}},
		File{Path: "big.txt", Data: []byte(big + "z\n")},
		File{Path: "run.sh", Mode: 0o755, Data: []byte("echo\n")},
		File{Path: "t.sh", Mode: 0o755, Data: []byte("t")})
	blobs := func(sha string) ([]byte, bool) {
		if v, ok := ab[sha]; ok {
			return v, true
		}
		v, ok := bb[sha]
		return v, ok
	}
	want := `kit.yml:
   kind: studio
  -prompt: a
  +prompt: b
files: 6 changed, 0 unchanged
  modified  Dockerfile  0644  7 B → 7 B
     -FROM a
     +FROM b
  modified  big.txt  0644  131072 B → 131074 B  (too large to diff)
  modified  bin  0644  2 B → 2 B  (binary)
  removed   old.txt  0644  1 B
  added     run.sh  0755  5 B
  mode      t.sh  0644 → 0755
`
	if got := DiffText(a, b, blobs); got != want {
		t.Fatalf("DiffText =\n%s\nwant\n%s", got, want)
	}
	if got := DiffText(a, a, blobs); got != "no changes\n" {
		t.Fatalf("same = %q", got)
	}
}
```

In the expected text, `files: 6 changed` counts the six non-`kit.yml` changes, and `kit.yml` gets its own block. When an implementer adjusts the exact column spacing, update `want` in the same step and keep the shape: the `kit.yml` block first, then one row per changed file, then the indented line diff of a modified text file.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/kittree/`
Expected: FAIL, `undefined: NewFS`, `ContextTar`, `Changes`, `DiffText`.

- [ ] **Step 3: Implement**

```go
// internal/kittree/fs.go
package kittree

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"time"

	"github.com/go-git/go-billy/v5"
)

// NewFS is a read-only billy view of t. Open, OpenFile (read-only), Stat,
// Lstat, ReadDir and Join read; every write returns billy.ErrReadOnly.
// Directories are implied by the file paths. A file's bytes are fetched from
// blobs when it is opened.
func NewFS(t Tree, blobs Blobs) billy.Filesystem {
	v := &treeFS{t: t, blobs: blobs, dirs: map[string][]string{"": nil}}
	for _, e := range t.Entries {
		// Add each path element to its parent directory's children, up to the root.
		for p := e.Path; ; {
			key := path.Dir(p)
			if key == "." {
				key = ""
			}
			if name := path.Base(p); !slices.Contains(v.dirs[key], name) {
				v.dirs[key] = append(v.dirs[key], name)
			}
			if key == "" {
				break
			}
			p = key
		}
	}
	for k := range v.dirs {
		slices.Sort(v.dirs[k])
	}
	return v
}

type treeFS struct {
	t     Tree
	blobs Blobs
	dirs  map[string][]string // dir ("" = root) → sorted child names
}

// clean maps "/a", "./a" and "a" to "a", the root to "".
func clean(name string) string {
	p := path.Clean("/" + filepath.ToSlash(name))
	return p[1:]
}

func (v *treeFS) Open(name string) (billy.File, error) { return v.OpenFile(name, os.O_RDONLY, 0) }

func (v *treeFS) OpenFile(name string, flag int, _ os.FileMode) (billy.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC) != 0 {
		return nil, billy.ErrReadOnly
	}
	p := clean(name)
	e, ok := v.t.Lookup(p)
	if !ok {
		if _, isDir := v.dirs[p]; isDir {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	b, ok := v.blobs(e.SHA256)
	if !ok {
		return nil, fmt.Errorf("%s: content missing from the store", p)
	}
	return &treeFile{name: name, Reader: bytes.NewReader(b)}, nil
}

func (v *treeFS) Stat(name string) (os.FileInfo, error) {
	p := clean(name)
	if e, ok := v.t.Lookup(p); ok {
		return fileInfo{name: path.Base(p), size: e.Size, mode: e.Mode}, nil
	}
	if _, ok := v.dirs[p]; ok {
		return fileInfo{name: path.Base("/" + p), mode: fs.ModeDir | 0o755}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func (v *treeFS) Lstat(name string) (os.FileInfo, error) { return v.Stat(name) }

func (v *treeFS) ReadDir(name string) ([]os.FileInfo, error) {
	p := clean(name)
	kids, ok := v.dirs[p]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]os.FileInfo, 0, len(kids))
	for _, k := range kids {
		fi, err := v.Stat(path.Join(p, k))
		if err != nil {
			return nil, err
		}
		out = append(out, fi)
	}
	return out, nil
}

func (v *treeFS) Join(elem ...string) string                   { return path.Join(elem...) }
func (v *treeFS) Create(string) (billy.File, error)            { return nil, billy.ErrReadOnly }
func (v *treeFS) Rename(string, string) error                  { return billy.ErrReadOnly }
func (v *treeFS) Remove(string) error                          { return billy.ErrReadOnly }
func (v *treeFS) TempFile(string, string) (billy.File, error)  { return nil, billy.ErrReadOnly }
func (v *treeFS) MkdirAll(string, os.FileMode) error           { return billy.ErrReadOnly }
func (v *treeFS) Symlink(string, string) error                 { return billy.ErrReadOnly }
func (v *treeFS) Readlink(name string) (string, error)         { return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid} }
func (v *treeFS) Chroot(string) (billy.Filesystem, error)      { return nil, billy.ErrNotSupported }
func (v *treeFS) Root() string                                 { return "/" }
func (v *treeFS) Capabilities() billy.Capability               { return billy.ReadCapability | billy.SeekCapability }

type treeFile struct {
	name string
	*bytes.Reader
}

func (f *treeFile) Name() string              { return f.name }
func (f *treeFile) Write([]byte) (int, error) { return 0, billy.ErrReadOnly }
func (f *treeFile) Close() error              { return nil }
func (f *treeFile) Lock() error               { return nil }
func (f *treeFile) Unlock() error             { return nil }
func (f *treeFile) Truncate(int64) error      { return billy.ErrReadOnly }

type fileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return i.size }
func (i fileInfo) Mode() fs.FileMode  { return i.mode }
func (i fileInfo) ModTime() time.Time { return epoch }
func (i fileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fileInfo) Sys() any           { return nil }
```

`path.Base("/" + "")` is `"/"` for the root, so `Stat("")` names the root `/`.

```go
// internal/kittree/context.go
package kittree

import (
	"archive/tar"
	"fmt"
	"io"
	"path"
	"slices"

	"github.com/go-git/go-billy/v5"
)

// ContextTar writes the build context in fsys (a kit version's billy view):
// every regular file but the root kit.yml, as a deterministic uncompressed
// tar (sorted by path, epoch mtime, uid/gid 0, the stored mode), for `docker
// build -`.
func ContextTar(w io.Writer, fsys billy.Filesystem) error {
	var files []string
	var walk func(dir string) error
	walk = func(dir string) error {
		ents, err := fsys.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("build context: %s: cannot be read", dir)
		}
		for _, e := range ents {
			p := path.Join(dir, e.Name())
			switch {
			case e.IsDir():
				if err := walk(p); err != nil {
					return err
				}
			case p != KitFile:
				files = append(files, p)
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return err
	}
	slices.Sort(files)
	tw := tar.NewWriter(w)
	for _, p := range files {
		fi, err := fsys.Stat(p)
		if err != nil {
			return fmt.Errorf("build context: %s: %w", p, err)
		}
		f, err := fsys.Open(p)
		if err != nil {
			return fmt.Errorf("build context: %w", err)
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("build context: %s: cannot be read", p)
		}
		if err := writeEntry(tw, p, fi.Mode().Perm(), data); err != nil {
			return err
		}
	}
	return tw.Close()
}
```

`path.Join("", "x")` is `"x"`, so the root walk yields slash paths relative to the root.

```go
// internal/kittree/diff.go
package kittree

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/linediff"
)

// Change kinds.
const (
	ChangeAdded    = "added"
	ChangeRemoved  = "removed"
	ChangeModified = "modified"
	ChangeMode     = "mode"
)

// Change is one file that differs between two trees. From or To is the zero
// Entry on the side without the file.
type Change struct {
	Path string
	Kind string
	From Entry
	To   Entry
}

// Changes lists, in path order, the files that differ from a to b, and counts
// the rest.
func Changes(a, b Tree) (changes []Change, unchanged int) {
	i, j := 0, 0
	for i < len(a.Entries) || j < len(b.Entries) {
		switch {
		case j == len(b.Entries) || (i < len(a.Entries) && a.Entries[i].Path < b.Entries[j].Path):
			changes = append(changes, Change{Path: a.Entries[i].Path, Kind: ChangeRemoved, From: a.Entries[i]})
			i++
		case i == len(a.Entries) || b.Entries[j].Path < a.Entries[i].Path:
			changes = append(changes, Change{Path: b.Entries[j].Path, Kind: ChangeAdded, To: b.Entries[j]})
			j++
		default:
			x, y := a.Entries[i], b.Entries[j]
			switch {
			case x.SHA256 != y.SHA256:
				changes = append(changes, Change{Path: x.Path, Kind: ChangeModified, From: x, To: y})
			case x.Mode != y.Mode:
				changes = append(changes, Change{Path: x.Path, Kind: ChangeMode, From: x, To: y})
			default:
				unchanged++
			}
			i, j = i+1, j+1
		}
	}
	return changes, unchanged
}

// MaxDiffFile is the largest side a file's line diff reads.
const MaxDiffFile = 64 << 10

// IsText reports content a diff shows as lines: valid UTF-8 with no NUL.
func IsText(b []byte) bool { return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0 }

// DiffText renders the version diff a → b for a terminal or a plan: kit.yml's
// line diff, then one row per other changed file (added, removed, modified,
// mode). A modified text file of at most MaxDiffFile bytes a side is followed
// by its line diff. Paths are CheckPath-safe; no content is shown but text
// lines.
func DiffText(a, b Tree, blobs Blobs) string {
	var sb strings.Builder
	changes, unchanged := Changes(a, b)
	var rows []Change
	for _, c := range changes {
		if c.Path == KitFile {
			fmt.Fprintf(&sb, "kit.yml:%s\n", modeNote(c))
			if c.Kind == ChangeModified {
				writeLines(&sb, "  ", blobOf(blobs, c.From), blobOf(blobs, c.To))
			}
			continue
		}
		rows = append(rows, c)
	}
	if _, kitUnchanged := a.Lookup(KitFile); kitUnchanged && !hasPath(changes, KitFile) {
		unchanged--
	}
	if len(changes) == 0 {
		return "no changes\n"
	}
	if len(rows) == 0 {
		return sb.String()
	}
	fmt.Fprintf(&sb, "files: %d changed, %d unchanged\n", len(rows), unchanged)
	for _, c := range rows {
		switch c.Kind {
		case ChangeAdded:
			fmt.Fprintf(&sb, "  added     %s  %04o  %d B\n", c.Path, c.To.Mode, c.To.Size)
		case ChangeRemoved:
			fmt.Fprintf(&sb, "  removed   %s  %04o  %d B\n", c.Path, c.From.Mode, c.From.Size)
		case ChangeMode:
			fmt.Fprintf(&sb, "  mode      %s  %04o → %04o\n", c.Path, c.From.Mode, c.To.Mode)
		case ChangeModified:
			mode := fmt.Sprintf("%04o", c.To.Mode)
			if c.From.Mode != c.To.Mode {
				mode = fmt.Sprintf("%04o → %04o", c.From.Mode, c.To.Mode)
			}
			fmt.Fprintf(&sb, "  modified  %s  %s  %d B → %d B", c.Path, mode, c.From.Size, c.To.Size)
			from, to := blobOf(blobs, c.From), blobOf(blobs, c.To)
			switch {
			case c.From.Size > MaxDiffFile || c.To.Size > MaxDiffFile:
				sb.WriteString("  (too large to diff)\n")
			case !IsText(from) || !IsText(to):
				sb.WriteString("  (binary)\n")
			default:
				sb.WriteString("\n")
				writeLines(&sb, "     ", from, to)
			}
		}
	}
	return sb.String()
}

func hasPath(cs []Change, p string) bool {
	for _, c := range cs {
		if c.Path == p {
			return true
		}
	}
	return false
}

func modeNote(c Change) string {
	if c.Kind == ChangeModified && c.From.Mode == c.To.Mode || c.Kind == ChangeAdded || c.Kind == ChangeRemoved {
		return ""
	}
	return fmt.Sprintf(" %04o → %04o", c.From.Mode, c.To.Mode)
}

func blobOf(blobs Blobs, e Entry) []byte {
	if e.SHA256 == "" {
		return nil
	}
	b, _ := blobs(e.SHA256)
	return b
}

// writeLines writes linediff lines indented: "<op><text>", a collapsed run as "… <text>".
func writeLines(sb *strings.Builder, indent string, a, b []byte) {
	lines, tooLarge := linediff.Diff(string(a), string(b))
	if tooLarge {
		sb.WriteString(indent + "(too large to diff)\n")
		return
	}
	for _, l := range lines {
		if l.Op == "…" {
			fmt.Fprintf(sb, "%s… %s\n", indent, l.Text)
		} else {
			fmt.Fprintf(sb, "%s%s%s\n", indent, l.Op, l.Text)
		}
	}
}
```

The `unchanged` count excludes `kit.yml`, which always has its own block. Check `linediff.Diff`'s op for an unchanged line: it is `" "`, so an unchanged line renders `"   kind: studio"` (indent, then op, then text). The test pins that.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/kittree/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kittree/
git commit -m "kittree: read-only billy view, deterministic build-context tar, version diff

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Kits parsed from a tree, the in-place `base.kit` rewrite, the digest (`internal/studio`)

**Files:**
- Create: `internal/studio/tree.go`
- Modify: `internal/studio/studiokit.go` (field `BuildContext`), `internal/studio/digest.go` (`BuildDigest`, `ChainDigest`)
- Test: `internal/studio/tree_test.go`, `internal/studio/digest_test.go` (add `TestBuildDigestBuildContext`)

**Interfaces:**
- Consumes: `kittree.Tree`, `kittree.Blobs`, `kittree.NewFS`, `kittree.CheckPath`, `kittree.KitFile`, `kittree.Dockerfile` (Tasks 1, 3); `jail.go`'s `errTooLarge`, `maxRootFile`, `maxNoteFileBytes`.
- Produces:
```go
// StudioKit gains, never serialized:
BuildContext string `yaml:"-" json:"-"` // the build context's tree hash; "" = no Dockerfile beside kit.yml
func ParseKitFS(fsys billy.Filesystem) (StudioKit, error)
func ParseTree(t kittree.Tree, blobs kittree.Blobs) (StudioKit, error) // ParseKitFS + BuildContext
func RefuseContextFields(kitYML []byte) error
func RenderKitYAML(sk StudioKit) ([]byte, error)
func SetBaseKit(kitYML []byte, value string) ([]byte, error)
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/studio/tree_test.go
package studio

import (
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"

	"github.com/aethons-tools/cove/internal/kittree"
)

func treeOf(t *testing.T, files map[string]string) (kittree.Tree, kittree.Blobs) {
	t.Helper()
	var fs []kittree.File
	for p, b := range files {
		fs = append(fs, kittree.File{Path: p, Mode: 0o644, Data: []byte(b)})
	}
	tr, blobs, err := kittree.Build(fs)
	if err != nil {
		t.Fatal(err)
	}
	return tr, kittree.MapBlobs(blobs)
}

func TestParseTreeResolvesNoteFilesAndSetsBuildContext(t *testing.T) {
	tr, blobs := treeOf(t, map[string]string{
		"kit.yml":       "kind: studio\nnotes:\n  - name: release.md\n    read-when: releasing\n    file: ./docs/release.md\n",
		"docs/release.md": "# Release\n",
		"Dockerfile":    "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n",
	})
	sk, err := ParseTree(tr, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if sk.Notes[0].Body != "# Release\n" || sk.Notes[0].File != "" {
		t.Fatalf("note = %+v", sk.Notes[0])
	}
	if sk.BuildContext == "" || sk.BuildContext != tr.ContextHash() {
		t.Fatalf("BuildContext = %q", sk.BuildContext)
	}
}

func TestParseKitFSRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"missing kit.yml":   {map[string]string{"Dockerfile": "FROM x"}, "kit.yml missing at the root"},
		"note not in kit":   {map[string]string{"kit.yml": "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: a.md}\n"}, `note "a.md": file a.md is not in the kit`},
		"note escapes":      {map[string]string{"kit.yml": "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: ../a.md}\n"}, `note "a.md": file must be a relative path inside the kit directory`},
		"image + Dockerfile": {map[string]string{"kit.yml": "kind: studio\nbase: {image: ghcr.io/x@sha256:1}\n", "Dockerfile": "FROM x"}, "studio kit: base.image and a Dockerfile next to kit.yml are mutually exclusive"},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := memfs.New()
			for p, b := range tc.files {
				if err := util.WriteFile(fsys, p, []byte(b), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ParseKitFS(fsys); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRefuseContextFields(t *testing.T) {
	for key, want := range map[string]string{
		"context: H4sI":           "base.context is no longer supported; put a Dockerfile next to kit.yml",
		"context-files: {dockerfile: x}": "base.context-files is no longer supported; put a Dockerfile next to kit.yml",
		"contextFiles: {dockerfile: x}":  "base.context-files is no longer supported; put a Dockerfile next to kit.yml",
		"context-dir: ctx":        "base.context-dir is no longer supported; put a Dockerfile next to kit.yml",
	} {
		if err := RefuseContextFields([]byte("kind: studio\nbase:\n  " + key + "\n")); err == nil || err.Error() != want {
			t.Fatalf("%s: err = %v", key, err)
		}
	}
	if err := RefuseContextFields([]byte("kind: studio\nbase: {kit: web}\n")); err != nil {
		t.Fatal(err)
	}
}

func TestRenderKitYAMLParsesBack(t *testing.T) {
	sk := DefaultStudioKit()
	sk.LegacyName = "x"
	b, err := RenderKitYAML(sk)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseStudioKit(b)
	if err != nil {
		t.Fatal(err)
	}
	sk.LegacyName = ""
	if !reflect.DeepEqual(back, sk) {
		t.Fatalf("round trip:\n%+v\n%+v", back, sk)
	}
	if strings.Contains(string(b), "name:") {
		t.Fatalf("rendered a legacy name:\n%s", b)
	}
}

func TestSetBaseKitEditsOnlyTheScalar(t *testing.T) {
	for in, want := range map[string]string{
		"kind: studio\nbase:\n  kit: ../web # the parent\nprompt: hi\n": "kind: studio\nbase:\n  kit: web@v3 # the parent\nprompt: hi\n",
		"kind: studio\nbase:\n  kit: \"../web\"\n":                      "kind: studio\nbase:\n  kit: \"web@v3\"\n",
		"kind: studio\nbase:\n  kit: '../web'\n":                        "kind: studio\nbase:\n  kit: 'web@v3'\n",
		`{"kind":"studio","base":{"kit":"../web"}}`:                    `{"kind":"studio","base":{"kit":"web@v3"}}`,
		"prompt: \"é\"\nbase: {image: \"ü\", kit: ../web}\n":           "prompt: \"é\"\nbase: {image: \"ü\", kit: web@v3}\n",
	} {
		got, err := SetBaseKit([]byte(in), "web@v3")
		if err != nil || string(got) != want {
			t.Fatalf("SetBaseKit(%q) = %q, %v; want %q", in, got, err, want)
		}
		back, err := SetBaseKit(got, "../web")
		if err != nil || string(back) != in {
			t.Fatalf("not reversible: %q → %q", got, back)
		}
	}
	for _, in := range []string{
		"kind: studio\n",
		"kind: studio\nbase:\n  kit: |\n    ../web\n",
		"kind: studio\nbase: {kit: \"..\\/web\"}\n",
	} {
		if _, err := SetBaseKit([]byte(in), "web@v3"); err == nil {
			t.Fatalf("SetBaseKit(%q) must refuse", in)
		}
	}
}
```

Add to `internal/studio/digest_test.go`:

```go
// TestBuildDigestBuildContext pins the formula: today's payload with
// "buildContext" after "base", omitted when empty — so a kit without a
// Dockerfile keeps its digest exactly.
func TestBuildDigestBuildContext(t *testing.T) {
	h := harnessinstall.Default()
	sk := StudioKit{Kind: Kind, Egress: []string{"b.com", "a.com"}, Prompt: "p"}
	old := hashDigest(struct {
		Base       Base                     `json:"base"`
		Egress     []string                 `json:"egress"`
		BuildArgs  map[string]string        `json:"buildArgs"`
		MCPServers map[string]kit.MCPServer `json:"mcpServers,omitempty"`
		Harness    harnessinstall.Install   `json:"harness"`
	}{Base: sk.Base, Egress: normEgress(sk.Egress), BuildArgs: normBuildArgs(nil), Harness: normHarness(h)})
	if got := BuildDigest(sk, h); got != old {
		t.Fatalf("a kit without a build context changed digest: %s != %s", got, old)
	}
	withCtx := sk
	withCtx.BuildContext = "abc"
	if BuildDigest(withCtx, h) == old {
		t.Fatal("a build context must key the digest")
	}
	edited := withCtx
	edited.Prompt = "other"
	if BuildDigest(edited, h) != BuildDigest(withCtx, h) {
		t.Fatal("a prompt edit must keep the digest")
	}
	parent := StudioKit{Kind: Kind}
	child := StudioKit{Kind: Kind, Base: Base{Kit: "p"}, BuildContext: "c1"}
	child2 := child
	child2.BuildContext = "c2"
	if ChainDigest([]StudioKit{parent, child}, h) == ChainDigest([]StudioKit{parent, child2}, h) {
		t.Fatal("a link's build context must key the chain digest")
	}
}
```

(`digest_test.go` imports `harnessinstall` and `internal/kit` already. Add any that are missing.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/studio/ -run 'ParseTree|ParseKitFS|RefuseContext|RenderKitYAML|SetBaseKit|BuildDigestBuildContext'`
Expected: FAIL, `undefined: ParseTree` and `sk.BuildContext undefined`.

- [ ] **Step 3: Implement**

In `internal/studio/studiokit.go`, add the field to `StudioKit` after `MCPServers`:

```go
	// BuildContext is the tree hash of the kit's build context (its tree minus
	// kit.yml) when a Dockerfile sits next to kit.yml; "" otherwise. Set by
	// ParseTree, never serialized; build-affecting (BuildDigest).
	BuildContext string `yaml:"-" json:"-"`
```

In `internal/studio/digest.go`, add `BuildContext` to both payloads right after `Base`:

```go
func BuildDigest(sk StudioKit, harness harnessinstall.Install) string {
	return hashDigest(struct {
		Base         Base                     `json:"base"`
		BuildContext string                   `json:"buildContext,omitempty"`
		Egress       []string                 `json:"egress"`
		BuildArgs    map[string]string        `json:"buildArgs"`
		MCPServers   map[string]kit.MCPServer `json:"mcpServers,omitempty"`
		Harness      harnessinstall.Install   `json:"harness"`
	}{Base: sk.Base, BuildContext: sk.BuildContext, Egress: normEgress(sk.Egress), BuildArgs: normBuildArgs(sk.BuildArgs), MCPServers: sk.MCPServers, Harness: normHarness(harness)})
}
```

In `ChainDigest`, do the same for `link`: add `BuildContext string \`json:"buildContext,omitempty"\`` after `Base`, and set `BuildContext: sk.BuildContext` in the loop. Extend the doc comments: "base, the build context's tree hash (when a Dockerfile sits next to kit.yml), egress …".

```go
// internal/studio/tree.go
package studio

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"unicode/utf8"

	"github.com/go-git/go-billy/v5"
	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/kittree"
)

// ParseTree parses kit version tree t, its bytes from blobs: ParseKitFS over
// its billy view, with BuildContext set to t's context hash.
func ParseTree(t kittree.Tree, blobs kittree.Blobs) (StudioKit, error) {
	sk, err := ParseKitFS(kittree.NewFS(t, blobs))
	if err != nil {
		return StudioKit{}, err
	}
	sk.BuildContext = t.ContextHash()
	return sk, nil
}

// ParseKitFS parses the kit directory fsys: its kit.yml (ParseStudioKit),
// each note's file read from fsys into its body (relative to the root, inside
// it, at most maxNoteFileBytes in total), and the rule that base.image and a
// Dockerfile next to kit.yml exclude each other. It does not refuse the old
// packed-context fields; ingest calls RefuseContextFields.
func ParseKitFS(fsys billy.Filesystem) (StudioKit, error) {
	data, err := readKitFile(fsys, kittree.KitFile, maxRootFile)
	if errors.Is(err, fs.ErrNotExist) {
		return StudioKit{}, errors.New("kit.yml missing at the root")
	}
	if err != nil {
		return StudioKit{}, err
	}
	sk, err := ParseStudioKit(data)
	if err != nil {
		return StudioKit{}, err
	}
	budget := int64(maxNoteFileBytes)
	for i, n := range sk.Notes {
		if n.File == "" {
			continue
		}
		if n.Body != "" {
			return StudioKit{}, fmt.Errorf("note %q: set body or file, not both", n.Name)
		}
		p := path.Clean(filepath.ToSlash(n.File))
		if kittree.CheckPath(p) != nil {
			return StudioKit{}, fmt.Errorf("note %q: file must be a relative path inside the kit directory", n.Name)
		}
		b, err := readKitFile(fsys, p, budget)
		switch {
		case errors.Is(err, errTooLarge):
			return StudioKit{}, fmt.Errorf("notes: note files exceed the %d-byte total cap (at note %q)", maxNoteFileBytes, n.Name)
		case errors.Is(err, fs.ErrNotExist):
			return StudioKit{}, fmt.Errorf("note %q: file %s is not in the kit", n.Name, p)
		case err != nil:
			return StudioKit{}, err
		}
		budget -= int64(len(b))
		sk.Notes[i].Body, sk.Notes[i].File = string(b), ""
	}
	if _, err := fsys.Stat(kittree.Dockerfile); err == nil && sk.Base.Image != "" {
		return StudioKit{}, errors.New("studio kit: base.image and a Dockerfile next to kit.yml are mutually exclusive")
	}
	return sk, nil
}

// readKitFile reads p from fsys, errTooLarge past max bytes.
func readKitFile(fsys billy.Filesystem, p string, max int64) ([]byte, error) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("%s: cannot be read", p)
	}
	if int64(len(b)) > max {
		return nil, errTooLarge
	}
	return b, nil
}

// contextFields are the packed-context keys of base, as written and as stored.
var contextFields = []struct{ key, field string }{
	{"context", "base.context"},
	{"context-files", "base.context-files"},
	{"contextFiles", "base.context-files"},
	{"context-dir", "base.context-dir"},
}

// RefuseContextFields refuses a kit.yml that still uses the packed build
// context: base.context, base.context-files or base.context-dir. A kit.yml
// that is not YAML passes; ParseStudioKit reports it.
func RefuseContextFields(kitYML []byte) error {
	base := baseNode(kitYML)
	if base == nil || base.Kind != yaml.MappingNode {
		return nil
	}
	for _, c := range contextFields {
		if mapValue(base, c.key) != nil {
			return fmt.Errorf("%s is no longer supported; put a Dockerfile next to kit.yml", c.field)
		}
	}
	return nil
}

// RenderKitYAML is the kit.yml Jam writes for a definition it holds (the
// default kit, a JSON push's re-render, a migrated version): sk as YAML
// without a legacy name.
func RenderKitYAML(sk StudioKit) ([]byte, error) {
	sk.LegacyName = ""
	return yaml.Marshal(sk)
}

var (
	errNoBaseKit    = errors.New("kit.yml: no base.kit to rewrite")
	errBaseKitShape = errors.New("kit.yml: write base.kit as a one-line plain or quoted value, without escapes")
)

// SetBaseKit returns kitYML with its base.kit value replaced by value (a
// tag-safe name, name@vN, or a ../path: all plain-safe YAML), keeping every
// other byte and the value's quote style. base.kit must be a one-line plain,
// single- or double-quoted scalar without escapes.
func SetBaseKit(kitYML []byte, value string) ([]byte, error) {
	base := baseNode(kitYML)
	if base == nil {
		return nil, errNoBaseKit
	}
	kit := mapValue(base, "kit")
	if kit == nil || kit.Kind != yaml.ScalarNode {
		return nil, errNoBaseKit
	}
	var q string
	switch kit.Style {
	case 0:
	case yaml.DoubleQuotedStyle:
		q = `"`
	case yaml.SingleQuotedStyle:
		q = "'"
	default:
		return nil, errBaseKitShape
	}
	start, ok := byteOffset(kitYML, kit.Line, kit.Column)
	raw := q + kit.Value + q
	if !ok || !bytes.HasPrefix(kitYML[start:], []byte(raw)) {
		return nil, errBaseKitShape
	}
	out := make([]byte, 0, len(kitYML)-len(raw)+len(value)+2)
	out = append(out, kitYML[:start]...)
	out = append(out, q+value+q...)
	return append(out, kitYML[start+len(raw):]...), nil
}

// baseNode is the value node of the document's top-level base key, or nil.
func baseNode(kitYML []byte) *yaml.Node {
	var doc yaml.Node
	if yaml.Unmarshal(kitYML, &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	return mapValue(doc.Content[0], "base")
}

// mapValue is mapping m's value for key, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// byteOffset maps yaml.v3's 1-based line and column (the column counts
// characters) to a byte offset in b.
func byteOffset(b []byte, line, col int) (int, bool) {
	off := 0
	for l := 1; l < line; l++ {
		i := bytes.IndexByte(b[off:], '\n')
		if i < 0 {
			return 0, false
		}
		off += i + 1
	}
	for c := 1; c < col; c++ {
		if off >= len(b) || b[off] == '\n' {
			return 0, false
		}
		_, size := utf8.DecodeRune(b[off:])
		off += size
	}
	return off, true
}
```

`adminui/kit_context.go` has its own `mapValue`; the two live in different packages, so they do not clash.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/studio/ ./internal/jam/... ./internal/kittree/`
Expected: PASS. Every existing digest test still passes: no kit there has a `BuildContext`.

- [ ] **Step 5: Commit**

```bash
git add internal/studio/
git commit -m "studio: parse a kit from its tree, rewrite base.kit in place, key the digest on the build context

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Trees and blobs in the store

**Files:**
- Create: `internal/jam/kit_objects.go`, `internal/jam/migrations/0019_kit_trees.sql`, `internal/jam/storetest/kits_tree.go`
- Modify: `internal/jam/identity.go` (`Kit`), `internal/jam/store.go` (interface), `internal/jam/memstate.go`, `internal/jam/memstore.go`, `internal/jam/pgstore.go`, `internal/jam/pgstore_config.go`, `internal/jam/pgstore_testhelpers.go`, `internal/jam/config_marks.go` (`ConfigWrite.KitObjects`), `internal/jam/kitchain.go` (`RemoveKitChecked`, `PinKitChecked`), `internal/jam/kitsource.go` and `internal/jam/kitset.go` (callers of `PutKitDocs`), `internal/jam/config_apply.go` (GC after removals), `internal/jam/config_export.go` and `internal/jam/config_plan_delete.go` (read the current config through `kitConfigLocked`), `internal/jam/setup_image_bodies.go` (`HasVersion`), `internal/jam/admin.go` (version lists), `internal/jam/adminui/{kit_detail,attention,search}.go` (read through `KitConfig`), `internal/jam/storetest/conformance.go`, `internal/jam/storetest/kits_set.go`
- Test: `internal/jam/storetest/kits_tree.go` (runs for MemStore in `TestMemStoreConformance` and for Postgres under `integration`). Also update the tests that read `Kit.Versions` of a *pushed* kit (listed in Step 4).

**Interfaces:**
- Consumes: `kittree.Tree`, `kittree.Build`, `kittree.File`, `kittree.KitFile` (Task 1).
- Produces:
```go
// identity.go — Kit gains (Versions keeps its JSON name and pre-tree meaning)
Versions map[int]string `json:"versions,omitempty"` // pre-tree config text (a store not yet migrated)
Trees    map[int]string `json:"trees,omitempty"`    // version → tree sha256
Broken   map[int]string `json:"broken,omitempty"`   // version → why the migration could not make its tree
Legacy   map[int]string `json:"legacy,omitempty"`   // pre-migration config text, kept for one release
func (k Kit) HasVersion(v int) bool
func (k Kit) VersionNumbers() []int

// kit_objects.go
type KitObjects struct { Trees []kittree.Tree; Blobs map[string][]byte }
type KitGC struct { Trees, Blobs int }
func (m *memState) KitTree(sha string) (kittree.Tree, bool)
func (m *memState) KitBlob(sha string) ([]byte, bool)

// Store gains / changes
PushKitVersion(name, tree string, objs KitObjects, meta KitVersionMeta, track *KitSource) (int, error)
PutKitDocs(kits []Kit, objs KitObjects) error
KitTree(sha string) (kittree.Tree, bool)
KitBlob(sha string) ([]byte, bool)
GCKitObjects() (KitGC, error)
// PushKit / PushKitMeta / PushKitTracked now store config as a one-file tree (kit.yml).
// KitConfig returns a tree version's kit.yml text (a pre-tree version's stored text).

// config_marks.go
ConfigWrite.KitObjects KitObjects // trees and blobs the write's Kits reference; written in the same transaction
```

- [ ] **Step 1: Write the failing conformance tests**

```go
// internal/jam/storetest/kits_tree.go
package storetest

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// treeObjs builds a tree from path → content pairs (a "*" suffix on the path makes it 0755).
func treeObjs(t *testing.T, kv ...string) (kittree.Tree, jam.KitObjects) {
	t.Helper()
	var files []kittree.File
	for i := 0; i+1 < len(kv); i += 2 {
		p, mode := kv[i], 0o644
		if strings.HasSuffix(p, "*") {
			p, mode = strings.TrimSuffix(p, "*"), 0o755
		}
		files = append(files, kittree.File{Path: p, Mode: fsMode(mode), Data: []byte(kv[i+1])})
	}
	tr, blobs, err := kittree.Build(files)
	if err != nil {
		t.Fatal(err)
	}
	return tr, jam.KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}
}

// runKitTreeConformance exercises kit trees, blobs, their atomic writes and GC.
func runKitTreeConformance(t *testing.T, mk func(t *testing.T) jam.Store) {
	meta := jam.KitVersionMeta{PushedAt: time.Unix(10, 0).UTC()}
	t.Run("push a tree version and read it back", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
		v, err := st.PushKitVersion("app", tr.Hash(), objs, meta, nil)
		if err != nil || v != 1 {
			t.Fatalf("push = %d, %v", v, err)
		}
		k, _ := st.GetKit("app")
		if k.Current != 1 || k.Trees[1] != tr.Hash() || !k.HasVersion(1) || len(k.VersionNumbers()) != 1 {
			t.Fatalf("doc = %+v", k)
		}
		got, ok := st.KitTree(tr.Hash())
		if !ok || got.Hash() != tr.Hash() {
			t.Fatalf("tree = %+v %v", got, ok)
		}
		e, _ := got.Lookup("run.sh")
		b, ok := st.KitBlob(e.SHA256)
		if !ok || string(b) != "echo\n" || e.Mode != 0o755 {
			t.Fatalf("blob = %q %v", b, ok)
		}
		b[0] = 'X'
		if again, _ := st.KitBlob(e.SHA256); !bytes.Equal(again, []byte("echo\n")) {
			t.Fatal("KitBlob returned an aliased slice")
		}
		if cfg, ok := st.KitConfig("app", 1); !ok || cfg != "kind: studio\n" {
			t.Fatalf("KitConfig = %q %v", cfg, ok)
		}
	})
	t.Run("push kit stores a one-file tree", func(t *testing.T) {
		st := mk(t)
		if _, err := st.PushKit("web", `{"kind":"studio"}`); err != nil {
			t.Fatal(err)
		}
		k, _ := st.GetKit("web")
		tr, ok := st.KitTree(k.Trees[1])
		if !ok || len(tr.Entries) != 1 || tr.Entries[0].Path != kittree.KitFile || len(k.Versions) != 0 {
			t.Fatalf("doc %+v tree %+v", k, tr)
		}
	})
	t.Run("refuses a doc naming an unstored tree and writes nothing", func(t *testing.T) {
		st := mk(t)
		tr, _ := treeObjs(t, "kit.yml", "x")
		if _, err := st.PushKitVersion("app", tr.Hash(), jam.KitObjects{}, meta, nil); err == nil {
			t.Fatal("an unstored tree must be refused")
		}
		if _, ok := st.GetKit("app"); ok {
			t.Fatal("the doc was written")
		}
	})
	t.Run("refuses a blob that is not its hash", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "x")
		for sha := range objs.Blobs {
			objs.Blobs[sha] = []byte("tampered")
		}
		if _, err := st.PushKitVersion("app", tr.Hash(), objs, meta, nil); err == nil {
			t.Fatal("a tampered blob must be refused")
		}
		if _, ok := st.KitTree(tr.Hash()); ok {
			t.Fatal("the tree was stored")
		}
	})
	t.Run("identical trees share objects and GC keeps what is referenced", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n", "Dockerfile", "FROM x\n")
		for _, n := range []string{"a", "b"} {
			if _, err := st.PushKitVersion(n, tr.Hash(), objs, meta, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.RemoveKit("a"); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{}) {
			t.Fatalf("gc = %+v %v (b still references the tree)", gc, err)
		}
		if err := st.RemoveKit("b"); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{Trees: 1, Blobs: 2}) {
			t.Fatalf("gc = %+v %v", gc, err)
		}
		if _, ok := st.KitTree(tr.Hash()); ok {
			t.Fatal("an unreferenced tree survived GC")
		}
	})
	t.Run("put kit docs writes objects with the docs, all or nothing", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n")
		good := jam.Kit{Name: "a", Current: 1, Trees: map[int]string{1: tr.Hash()}, Meta: map[int]jam.KitVersionMeta{1: meta}}
		bad := jam.Kit{Name: "b", Current: 1, Trees: map[int]string{1: strings.Repeat("0", 64)}}
		if err := st.PutKitDocs([]jam.Kit{good, bad}, objs); err == nil {
			t.Fatal("a doc naming an unknown tree must refuse the call")
		}
		if _, ok := st.GetKit("a"); ok {
			t.Fatal("a was written although the call failed")
		}
		if err := st.PutKitDocs([]jam.Kit{good}, objs); err != nil {
			t.Fatal(err)
		}
		if cfg, ok := st.KitConfig("a", 0); !ok || cfg != "kind: studio\n" {
			t.Fatalf("KitConfig = %q %v", cfg, ok)
		}
	})
	t.Run("apply config writes kit objects", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n")
		w := jam.ConfigWrite{Kits: []jam.Kit{{Name: "a", Current: 1, Trees: map[int]string{1: tr.Hash()}}}, KitObjects: objs}
		if err := st.ApplyConfig(w); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.KitTree(tr.Hash()); !ok {
			t.Fatal("tree not stored")
		}
	})
	t.Run("pin and versions span text, tree and broken versions", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n")
		doc := jam.Kit{Name: "m", Current: 3, Versions: map[int]string{1: `{"kind":"studio"}`}, Broken: map[int]string{2: "unreadable"}, Trees: map[int]string{3: tr.Hash()}}
		if err := st.PutKitDocs([]jam.Kit{doc}, objs); err != nil {
			t.Fatal(err)
		}
		k, _ := st.GetKit("m")
		if got := k.VersionNumbers(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
			t.Fatalf("versions = %v", got)
		}
		if err := st.PinKit("m", 1); err != nil {
			t.Fatal(err)
		}
		if v, err := st.PushKit("m", `{"kind":"studio","prompt":"x"}`); err != nil || v != 4 {
			t.Fatalf("next version = %d %v", v, err)
		}
	})
}
```

Add a one-line helper to the same file: `func fsMode(m int) fs.FileMode { return fs.FileMode(m) }`, and import `io/fs`.

In `internal/jam/storetest/conformance.go`, call `runKitTreeConformance(t, newStore)` after `runKitSetConformance`. In the `kits_versions_pin_remove` subtest, change `len(k.Versions) != 2` to `len(k.VersionNumbers()) != 2`. In `storetest/kits_set.go`, change both `PutKitDocs(x)` calls to `PutKitDocs(x, jam.KitObjects{})`. Its docs keep `Versions` text, which stores still accept.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run TestMemStoreConformance`
Expected: FAIL to compile: `st.PushKitVersion undefined`, `jam.KitObjects undefined`, `PutKitDocs` arity.

- [ ] **Step 3: Implement**

`internal/jam/identity.go`, the `Kit` struct:

```go
type Kit struct {
	Name    string `json:"name"`
	Current int    `json:"current"`
	// Versions is a pre-tree store's config text by version; new versions
	// never use it, and MigrateKitTrees moves it into Legacy.
	Versions map[int]string         `json:"versions,omitempty"`
	Trees    map[int]string         `json:"trees,omitempty"`  // version → tree sha256 (immutable)
	Broken   map[int]string         `json:"broken,omitempty"` // version → why the migration could not make its tree (fixed text); raising it fails closed
	Legacy   map[int]string         `json:"legacy,omitempty"` // every migrated version's pre-migration config text, kept for one release
	Meta     map[int]KitVersionMeta `json:"meta,omitempty"`   // version number → provenance; absent for legacy versions
	Source   *KitSource             `json:"source,omitempty"` // tracked source; nil = not tracked
}
```

```go
// internal/jam/kit_objects.go
package jam

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"

	"github.com/aethons-tools/cove/internal/kittree"
)

// KitObjects are the content-addressed objects a kit write carries: trees
// and the blobs their entries name, by sha256. A store writes them in the
// same transaction as the docs that reference them.
type KitObjects struct {
	Trees []kittree.Tree
	Blobs map[string][]byte
}

func (o KitObjects) empty() bool { return len(o.Trees) == 0 && len(o.Blobs) == 0 }

// KitGC reports a GCKitObjects run: the trees and blobs it deleted.
type KitGC struct{ Trees, Blobs int }

// HasVersion reports whether the kit has version v: a tree, a pre-tree
// config or a broken one.
func (k Kit) HasVersion(v int) bool {
	_, text := k.Versions[v]
	_, tree := k.Trees[v]
	_, broken := k.Broken[v]
	return text || tree || broken
}

// VersionNumbers is every version of the kit, ascending.
func (k Kit) VersionNumbers() []int {
	set := map[int]bool{}
	for _, m := range []map[int]string{k.Versions, k.Trees, k.Broken} {
		for v := range m {
			set[v] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// oneFileKit is kitYML as a one-file tree (kit.yml, 0644) and its objects.
func oneFileKit(kitYML string) (kittree.Tree, KitObjects, error) {
	t, blobs, err := kittree.Build([]kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: []byte(kitYML)}})
	if err != nil {
		return kittree.Tree{}, KitObjects{}, err
	}
	return t, KitObjects{Trees: []kittree.Tree{t}, Blobs: blobs}, nil
}

// checkKitObjects refuses a kit write that does not hold together: a tree
// that does not validate, a blob whose bytes are not its sha256, a tree entry
// whose blob is neither written nor stored (or differs in size), or a doc
// naming a tree that is neither. Caller holds mu.
func checkKitObjects(m *memState, kits []Kit, objs KitObjects) error {
	for sha, b := range objs.Blobs {
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != sha {
			return fmt.Errorf("kit blob %.12s: content does not match its sha256", sha)
		}
	}
	written := map[string]bool{}
	for _, t := range objs.Trees {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("kit tree: %w", err)
		}
		for _, e := range t.Entries {
			b, ok := objs.Blobs[e.SHA256]
			if !ok {
				b, ok = m.kitBlobs[e.SHA256]
			}
			if !ok || int64(len(b)) != e.Size {
				return fmt.Errorf("kit tree %.12s: %s has no stored content", t.Hash(), e.Path)
			}
		}
		written[t.Hash()] = true
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

// applyKitObjects caches copies of objs. Caller holds mu and has checked them.
func (m *memState) applyKitObjects(objs KitObjects) {
	for _, t := range objs.Trees {
		m.kitTrees[t.Hash()] = kittree.Tree{Entries: slices.Clone(t.Entries)}
	}
	for sha, b := range objs.Blobs {
		m.kitBlobs[sha] = bytes.Clone(b)
	}
}

// deadKitObjects lists, sorted, the trees no kit version references and the
// blobs no remaining tree names. Caller holds mu.
func (m *memState) deadKitObjects() (trees, blobs []string) {
	live := map[string]bool{}
	for _, k := range m.kits {
		for _, sha := range k.Trees {
			live[sha] = true
		}
	}
	liveBlobs := map[string]bool{}
	for sha, t := range m.kitTrees {
		if !live[sha] {
			trees = append(trees, sha)
			continue
		}
		for _, e := range t.Entries {
			liveBlobs[e.SHA256] = true
		}
	}
	for sha := range m.kitBlobs {
		if !liveBlobs[sha] {
			blobs = append(blobs, sha)
		}
	}
	slices.Sort(trees)
	slices.Sort(blobs)
	return trees, blobs
}

// applyDropKitObjects deletes trees and blobs from the cache. Caller holds mu.
func (m *memState) applyDropKitObjects(trees, blobs []string) {
	for _, sha := range trees {
		delete(m.kitTrees, sha)
	}
	for _, sha := range blobs {
		delete(m.kitBlobs, sha)
	}
}

// setKitTree records tree as version ver of k with meta, current, and k's
// tracked source when track is non-nil.
func setKitTree(k *Kit, ver int, tree string, meta KitVersionMeta, track *KitSource) {
	if k.Trees == nil {
		k.Trees = map[int]string{}
	}
	if k.Meta == nil {
		k.Meta = map[int]KitVersionMeta{}
	}
	k.Trees[ver] = tree
	k.Meta[ver] = copyKitMeta(meta)
	k.Current = ver
	if track != nil {
		k.Source = copyKitSource(track)
	}
}

// KitTree returns a copy of stored tree sha.
func (m *memState) KitTree(sha string) (kittree.Tree, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.kitTrees[sha]
	return kittree.Tree{Entries: slices.Clone(t.Entries)}, ok
}

// KitBlob returns a copy of stored blob sha.
func (m *memState) KitBlob(sha string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.kitBlobs[sha]
	return bytes.Clone(b), ok
}

// kitConfigLocked is version's kit.yml text (a pre-tree version's stored
// config); version 0 is current. Caller holds mu, or owns m.
func (m *memState) kitConfigLocked(name string, version int) (string, bool) {
	k, ok := m.kits[name]
	if !ok {
		return "", false
	}
	if version == 0 {
		version = k.Current
	}
	if sha, ok := k.Trees[version]; ok {
		if e, ok := m.kitTrees[sha].Lookup(kittree.KitFile); ok {
			if b, ok := m.kitBlobs[e.SHA256]; ok {
				return string(b), true
			}
		}
		return "", false
	}
	cfg, ok := k.Versions[version]
	return cfg, ok
}
```

`internal/jam/memstate.go`:
- Add the fields `kitTrees map[string]kittree.Tree // kit tree sha256 → tree (kit_objects.go)` and `kitBlobs map[string][]byte // blob sha256 → bytes`. Initialize both in `newMemState`.
- `KitConfig` becomes `m.mu.RLock(); defer m.mu.RUnlock(); return m.kitConfigLocked(name, version)`.
- `nextKitVersion` takes the max over `k.VersionNumbers()`.
- `copyKit` also copies `Trees`, `Broken` and `Legacy` (`maps.Clone` each, keeping nil as nil).
- Delete `applyPushKitMeta`. `setKitTree` replaces it.

`internal/jam/memstore.go`:

```go
func (fs *MemStore) PushKit(name, config string) (int, error) {
	return fs.PushKitMeta(name, config, KitVersionMeta{PushedAt: time.Now().UTC()})
}

func (fs *MemStore) PushKitMeta(name, config string, meta KitVersionMeta) (int, error) {
	return fs.PushKitTracked(name, config, meta, nil)
}

// PushKitTracked stores config as a one-file tree (kit.yml) version.
func (fs *MemStore) PushKitTracked(name, config string, meta KitVersionMeta, track *KitSource) (int, error) {
	if name == "" || config == "" {
		return 0, fmt.Errorf("kit name and config are required")
	}
	t, objs, err := oneFileKit(config)
	if err != nil {
		return 0, fmt.Errorf("kit %s: %w", name, err)
	}
	return fs.PushKitVersion(name, t.Hash(), objs, meta, track)
}

func (fs *MemStore) PushKitVersion(name, tree string, objs KitObjects, meta KitVersionMeta, track *KitSource) (int, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if name == "" || tree == "" {
		return 0, fmt.Errorf("kit name and tree are required")
	}
	if err := checkKitObjects(fs.memState, []Kit{{Name: name, Trees: map[int]string{0: tree}}}, objs); err != nil {
		return 0, err
	}
	fs.applyKitObjects(objs)
	k := copyKit(fs.kits[name])
	k.Name = name
	ver := nextKitVersion(k)
	setKitTree(&k, ver, tree, meta, track)
	fs.kits[name] = k
	return ver, nil
}

func (fs *MemStore) PutKitDocs(kits []Kit, objs KitObjects) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := checkKitDocs(kits); err != nil {
		return err
	}
	if err := checkKitObjects(fs.memState, kits, objs); err != nil {
		return err
	}
	fs.applyKitObjects(objs)
	for _, k := range kits {
		fs.kits[k.Name] = copyKit(k)
	}
	return nil
}

func (fs *MemStore) GCKitObjects() (KitGC, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	trees, blobs := fs.deadKitObjects()
	fs.applyDropKitObjects(trees, blobs)
	return KitGC{Trees: len(trees), Blobs: len(blobs)}, nil
}
```

`copyKit(Kit{})` of an absent kit yields a zero `Kit`. In `MemStore.PinKit`, replace `k.Versions[version]` with `k.HasVersion(version)`.

`internal/jam/store.go`: add `PushKitVersion`, `KitTree`, `KitBlob` and `GCKitObjects` to the kit block with doc comments, and change `PutKitDocs` to `PutKitDocs(kits []Kit, objs KitObjects) error`. Reword `PushKit`'s comment: "PushKit records config as the next version: a one-file tree (kit.yml), without the push-time checks — seeding and tests; operator pushes go through jam.PushKitFiles". Reword `KitConfig`'s: "KitConfig is version's kit.yml text (a pre-tree version's stored config); 0 = current".

`internal/jam/config_marks.go`: add the field `KitObjects KitObjects // the trees and blobs Kits reference, written in the same transaction` after `RemoveKits`. Count it in `Empty()` (`&& w.KitObjects.empty()`). In `checkConfigWrite`, after `checkKitDocs`, add `if err := checkKitObjects(m, w.Kits, w.KitObjects); err != nil { return fmt.Errorf("config write: %w", err) }`. In `applyConfigWrite`, call `m.applyKitObjects(w.KitObjects)` before the kits loop.

`internal/jam/migrations/0019_kit_trees.sql`:

```sql
-- Kit trees (docs/usage/jam/kits-directory.md): every kit version is
-- a content-addressed tree of blobs, shared by every kit and version. Config:
-- exported with the kits (ConfigSnapshot kit_trees / kit_blobs).
CREATE TABLE kit_blobs (
    sha256 text   PRIMARY KEY,
    size   bigint NOT NULL,
    bytes  bytea  NOT NULL
);
CREATE TABLE kit_trees (
    sha256  text  PRIMARY KEY,
    entries jsonb NOT NULL
);
```

`internal/jam/pgstore.go`:

```go
// in load(), right after the kits loadDocs:
	if err := s.loadKitObjects(ctx); err != nil {
		return err
	}

// loadKitObjects reads every kit tree and blob into the cache.
func (s *PostgresStore) loadKitObjects(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT sha256, entries FROM kit_trees`)
	if err != nil {
		return fmt.Errorf("pgstore: load kit_trees: %w", err)
	}
	for rows.Next() {
		var sha string
		var entries []byte
		if err := rows.Scan(&sha, &entries); err != nil {
			rows.Close()
			return err
		}
		var t kittree.Tree
		if err := json.Unmarshal(entries, &t.Entries); err != nil {
			rows.Close()
			return fmt.Errorf("pgstore: decode kit_trees row: %w", err)
		}
		s.kitTrees[sha] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.pool.Query(ctx, `SELECT sha256, bytes FROM kit_blobs`)
	if err != nil {
		return fmt.Errorf("pgstore: load kit_blobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sha string
		var b []byte
		if err := rows.Scan(&sha, &b); err != nil {
			return err
		}
		s.kitBlobs[sha] = b
	}
	return rows.Err()
}

// insertKitObjectsTx writes objs, skipping what is already stored (content-addressed).
func insertKitObjectsTx(ctx context.Context, tx pgx.Tx, objs KitObjects) error {
	for _, t := range objs.Trees {
		entries, err := json.Marshal(t.Entries)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO kit_trees (sha256, entries) VALUES ($1,$2) ON CONFLICT (sha256) DO NOTHING`, t.Hash(), entries); err != nil {
			return err
		}
	}
	for _, sha := range slices.Sorted(maps.Keys(objs.Blobs)) {
		b := objs.Blobs[sha]
		if _, err := tx.Exec(ctx, `INSERT INTO kit_blobs (sha256, size, bytes) VALUES ($1,$2,$3) ON CONFLICT (sha256) DO NOTHING`, sha, len(b), b); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) PushKitTracked(name, config string, meta KitVersionMeta, track *KitSource) (int, error) {
	if name == "" || config == "" {
		return 0, fmt.Errorf("kit name and config are required")
	}
	t, objs, err := oneFileKit(config)
	if err != nil {
		return 0, fmt.Errorf("kit %s: %w", name, err)
	}
	return s.PushKitVersion(name, t.Hash(), objs, meta, track)
}

func (s *PostgresStore) PushKitVersion(name, tree string, objs KitObjects, meta KitVersionMeta, track *KitSource) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" || tree == "" {
		return 0, fmt.Errorf("kit name and tree are required")
	}
	if err := checkKitObjects(s.memState, []Kit{{Name: name, Trees: map[int]string{0: tree}}}, objs); err != nil {
		return 0, err
	}
	k := copyKit(s.kits[name]) // don't mutate the cached kit before commit
	k.Name = name
	ver := nextKitVersion(k)
	setKitTree(&k, ver, tree, meta, track)
	doc, err := json.Marshal(k)
	if err != nil {
		return 0, err
	}
	if err := s.registryTx("PushKitVersion", func(ctx context.Context, tx pgx.Tx) error {
		if err := insertKitObjectsTx(ctx, tx, objs); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO kits (name, doc) VALUES ($1,$2)
			 ON CONFLICT (name) DO UPDATE SET doc = EXCLUDED.doc, version = kits.version + 1, updated_at = now()`, name, doc)
		return err
	}); err != nil {
		return 0, err
	}
	s.applyKitObjects(objs)
	s.kits[name] = k
	return ver, nil
}

func (s *PostgresStore) GCKitObjects() (KitGC, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	trees, blobs := s.deadKitObjects()
	if len(trees)+len(blobs) == 0 {
		return KitGC{}, nil
	}
	if err := s.registryTx("GCKitObjects", func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM kit_trees WHERE sha256 = ANY($1)`, trees); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM kit_blobs WHERE sha256 = ANY($1)`, blobs)
		return err
	}); err != nil {
		return KitGC{}, err
	}
	s.applyDropKitObjects(trees, blobs)
	return KitGC{Trees: len(trees), Blobs: len(blobs)}, nil
}
```

`PushKit` and `PushKitMeta` keep delegating, as now. In `PutKitDocs(kits, objs)`:
- Add `checkKitObjects(s.memState, kits, objs)` after `checkKitDocs`.
- Return early only when `len(kits) == 0 && objs.empty()`.
- Call `insertKitObjectsTx(ctx, tx, objs)` first inside the transaction.
- Call `s.applyKitObjects(objs)` before caching the docs.

In `PinKit`, use `k.HasVersion(version)`. `pgstore_config.go`'s `ApplyConfig` transaction calls `insertKitObjectsTx(ctx, tx, w.KitObjects)` just before its `for _, k := range w.Kits` loop; `checkConfigWrite` and `applyConfigWrite` already cover the cache. In `pgstore_testhelpers.go`, add `kit_trees, kit_blobs` to the `TRUNCATE` list and reset `s.kitTrees` and `s.kitBlobs` to empty maps.

Callers:
- `kitsource.go` `importKit`: `store.PutKitDocs(docs, KitObjects{})`. Task 10 passes real objects.
- `kitchain.go` `RemoveKitChecked`: after `store.RemoveKit(name)` succeeds, run `_, _ = store.GCKitObjects() // a failure leaves unreferenced objects for the next serve start`, then `return nil`. Keep the removal error as is.
- `kitchain.go` `PinKitChecked`: replace `store.KitConfig(name, version)` with `k, ok := store.GetKit(name); if !ok || !k.HasVersion(version)`.
- `config_apply.go` `planAndWriteConfig`: after a successful `ApplyConfig` whose write has `len(w.RemoveKits) > 0`, call `_, _ = store.GCKitObjects()`. Find the write: it is `plan.write`.
- `config_export.go`: `exportObjects` uses `cfg, ok := m.kitConfigLocked(n, k.Current)` instead of `k.Versions[k.Current]`, and `renderInlineKitFile` uses `cfg, _ := m.kitConfigLocked(name, 0)`.
- `config_plan_delete.go` `kitDepth`: `cfg, _ := p.m.kitConfigLocked(name, 0)`, then `kitParentSpec(cfg)`.
- `setup_image_bodies.go`: `} else if k.HasVersion(r.Version) {`.
- `admin.go`: the `GET /admin/kits` count is `len(k.VersionNumbers())`; `/versions` and `/history` iterate `k.VersionNumbers()` (already sorted; drop the `sort.Ints`).
- `adminui/kit_detail.go`: `kitListRows` counts with `len(k.VersionNumbers())` and parses `cfg, _ := store.KitConfig(k.Name, k.Current)`. `buildKitDetail` replaces `k.Versions[n]` with `cfg, _ := store.KitConfig(name, n)`, iterates `k.VersionNumbers()`, tests membership with `k.HasVersion(view)` and `k.HasVersion(diff)`, and reads `from, _ := store.KitConfig(name, diff)` and `cur, _ := store.KitConfig(name, view)`.
- `adminui/attention.go` and `adminui/search.go`: `cfg, _ := store.KitConfig(k.Name, k.Current)`, then `studio.ParseStudioKit([]byte(cfg))`.

- [ ] **Step 4: Fix the tests that read a pushed kit's `Versions`, and run everything**

These tests push through `PushKit` or a push API and then read `k.Versions[...]`, so they must read through `store.KitConfig(name, v)` or `len(k.VersionNumbers())` instead:
- `internal/jam/{memstore_test,kitset_test,kitsource_test,config_adopt_test,config_apply_test,config_plan_test,config_snapshot_test,config_source_test,config_export_context_test}.go`
- `internal/jam/adminui/{kits_page_test,kits_source_test}.go`
- `cmd/at-jam/configgit_write_test.go`

A test that builds a doc with `Versions:` text and writes it through `PutKitDocs(…, jam.KitObjects{})` stays as it is: pre-tree docs are still valid. `pgstore_integration_test.go` changes the same way and runs only under `integration`.

Run: `GOPROXY=https://proxy.golang.org,direct go test ./... && just lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A internal/jam cmd/at-jam
git commit -m "jam: store kit trees and blobs beside the docs, atomically, with GC

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Trees and blobs in config backups and planner state

**Files:**
- Modify: `internal/jam/config_snapshot.go` (`ConfigSnapshot`, `ConfigSnapshotVersion`, `ExportConfig`, `checkImport`, `applyImport`), `internal/jam/pgstore.go` (`ImportConfig`), `docs/usage/jam/backup.md`
- Test: `internal/jam/storetest/kits_tree.go` (add a subtest), `internal/jam/config_snapshot_test.go` (add `TestImportRefusesKitWithoutTree`)

**Interfaces:**
- Consumes: Task 5's `KitObjects`, `checkKitObjects`, `applyKitObjects`, `memState.kitTrees`/`kitBlobs`.
- Produces:
```go
const ConfigSnapshotVersion = 5
// ConfigSnapshot gains
KitTrees []kittree.Tree     `json:"kit_trees,omitempty"` // every tree a kit version references, by hash order
KitBlobs map[string][]byte  `json:"kit_blobs,omitempty"` // every blob those trees name (base64 in JSON)
```
`stateFromSnapshot` (planner, exporter) gets the trees and blobs through `applyImport`.

- [ ] **Step 1: Write the failing tests**

Add to `runKitTreeConformance`:

```go
	t.Run("export and import carry trees and blobs", func(t *testing.T) {
		src := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
		if _, err := src.PushKitVersion("app", tr.Hash(), objs, meta, nil); err != nil {
			t.Fatal(err)
		}
		snap := src.ExportConfig()
		if snap.Version != jam.ConfigSnapshotVersion || len(snap.KitTrees) != 1 || len(snap.KitBlobs) != 2 {
			t.Fatalf("snapshot v%d trees %d blobs %d", snap.Version, len(snap.KitTrees), len(snap.KitBlobs))
		}
		dst := mk(t)
		if err := dst.ImportConfig(snap); err != nil {
			t.Fatal(err)
		}
		e, _ := tr.Lookup("run.sh")
		if b, ok := dst.KitBlob(e.SHA256); !ok || string(b) != "echo\n" {
			t.Fatalf("blob after import = %q %v", b, ok)
		}
	})
```

In `internal/jam/config_snapshot_test.go`:

```go
func TestImportRefusesKitWithoutTree(t *testing.T) {
	snap := ConfigSnapshot{Version: ConfigSnapshotVersion, Kits: []Kit{{Name: "a", Current: 1, Trees: map[int]string{1: strings.Repeat("0", 64)}}}}
	if err := NewMemStore().ImportConfig(snap); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestMemStoreConformance|TestImportRefusesKitWithoutTree'`
Expected: FAIL to compile, `snap.KitTrees undefined`.

- [ ] **Step 3: Implement**

`config_snapshot.go`:
- Bump `ConfigSnapshotVersion` to 5. Extend its comment: "5 carries kit trees and blobs (kit_trees, kit_blobs); import also reads 1–4, whose kits are config text that the import route migrates (MigrateKitTrees)."
- Add the two fields after `Kits`.
- In `ExportConfig`, after the kits loop:

```go
	live := map[string]bool{}
	for _, k := range m.kits {
		for _, sha := range k.Trees {
			live[sha] = true
		}
	}
	for _, sha := range slices.Sorted(maps.Keys(live)) {
		t := m.kitTrees[sha]
		snap.KitTrees = append(snap.KitTrees, kittree.Tree{Entries: slices.Clone(t.Entries)})
		for _, e := range t.Entries {
			if snap.KitBlobs == nil {
				snap.KitBlobs = map[string][]byte{}
			}
			snap.KitBlobs[e.SHA256] = bytes.Clone(m.kitBlobs[e.SHA256])
		}
	}
```

- In `checkImport`, before `validateSnapshotContext`:

```go
	if err := checkKitObjects(m, s.Kits, KitObjects{Trees: s.KitTrees, Blobs: s.KitBlobs}); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
```

  The target is empty, so every tree must come with the snapshot.
- In `applyImport`, after the kits loop:

```go
	m.kitTrees, m.kitBlobs = map[string]kittree.Tree{}, map[string][]byte{}
	m.applyKitObjects(KitObjects{Trees: s.KitTrees, Blobs: s.KitBlobs})
```

`pgstore.go` `ImportConfig`: in its transaction, before the kits insert, call `insertKitObjectsTx(ctx, tx, KitObjects{Trees: snap.KitTrees, Blobs: snap.KitBlobs})`. The cache follows through `applyImport`, as now.

`docs/usage/jam/backup.md`: in the list of what a backup holds, change the kits line to "**kits** (every version: its files, as content-addressed trees and blobs, with the current pin and provenance)". Bump its `updated:`. The sentence about importing a pre-tree backup (version ≤ 4) is added in Task 13, together with the migration it describes.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./... && just lint`
Expected: PASS. Fix any existing test that asserts `ConfigSnapshotVersion == 4` by using the constant.

- [ ] **Step 5: Commit**

```bash
git add -A internal/jam docs/usage/jam/backup.md
git commit -m "jam: config backups carry kit trees and blobs (snapshot v5)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: One reader for a stored kit version, on the raise path

**Files:**
- Create: `internal/jam/kit_version.go`
- Modify: `internal/jam/kitchain.go` (`KitLink.Files`, `ResolveKitChain`, `KitsReferencing`, `recordedChainKit`, `chainCacheKey`), `internal/jam/studiokit.go` (`studioKitCurrent`, `StudioKitDefinition`, `ResolveKitDefinition`, `kitDefinitionFromConfig` → `kitDefinitionFromVersion`, `ensureKit`'s unchanged check), `internal/jam/supervisor.go` (`kitSource`, `kitSourceFor`, `currentKitRef`, `kitRefKey`), `internal/jam/studiokit_test.go` (the one `kitDefinitionFromConfig` call)
- Test: `internal/jam/kit_version_test.go`

**Interfaces:**
- Consumes: Task 5's `Store.KitTree`/`KitBlob`, `Kit.Trees`/`Broken`/`Versions`; Task 4's `studio.ParseTree`; Task 3's `kittree.NewFS`.
- Produces:
```go
type KitReader interface {
	GetKit(name string) (Kit, bool)
	KitTree(sha string) (kittree.Tree, bool)
	KitBlob(sha string) ([]byte, bool)
}
func KitFS(r KitReader, sha string) (billy.Filesystem, bool)
type KitVersion struct {
	Name     string
	Version  int
	TreeHash string           // "" for a pre-tree version
	Tree     kittree.Tree     // zero for a pre-tree version
	KitYML   []byte           // kit.yml (a pre-tree version: its stored config text)
	Files    billy.Filesystem // the version's files; nil for a pre-tree version
	Kit      studio.StudioKit // parsed; zero when Err is set
	Err      error            // the version does not parse, or is broken
}
func LoadKitVersion(r KitReader, name string, version int) (KitVersion, bool) // version 0 = current
func (v KitVersion) Key() string // "tree:<sha>" or "text:<sha256 of KitYML>"
// kitchain.go
type KitLink struct { Ref KitRef; Kit studio.StudioKit; Follow bool; Files billy.Filesystem }
func ResolveKitChain(store KitReader, self string, sk studio.StudioKit) ([]KitLink, error)
func recordedChainKit(store KitReader, chain []KitRef) (studio.StudioKit, error)
func chainCacheKey(store KitReader, sk studio.StudioKit) string
// studiokit.go
func studioKitCurrent(store Store, name string) (KitRef, KitVersion, error)
func kitDefinitionFromVersion(store Store, ref KitRef, v KitVersion, h harnessinstall.Install) (KitDefinition, error)
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/jam/kit_version_test.go
package jam

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kittree"
)

// pushTree pushes path → content files (a "*" suffix makes 0755) as kit name's next version.
func pushTree(t *testing.T, st Store, name string, kv ...string) kittree.Tree {
	t.Helper()
	var files []kittree.File
	for i := 0; i+1 < len(kv); i += 2 {
		p, mode := kv[i], 0o644
		if strings.HasSuffix(p, "*") {
			p, mode = strings.TrimSuffix(p, "*"), 0o755
		}
		files = append(files, kittree.File{Path: p, Mode: fsMode(mode), Data: []byte(kv[i+1])})
	}
	tr, blobs, err := kittree.Build(files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PushKitVersion(name, tr.Hash(), KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}, KitVersionMeta{PushedAt: time.Unix(1, 0).UTC()}, nil); err != nil {
		t.Fatal(err)
	}
	return tr
}

const dockerfile = "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n"

func TestLoadKitVersionTree(t *testing.T) {
	st := NewMemStore()
	tr := pushTree(t, st, "app",
		"kit.yml", "kind: studio\nnotes:\n  - {name: r.md, read-when: releasing, file: docs/r.md}\n",
		"docs/r.md", "# R\n", "Dockerfile", dockerfile, "run.sh*", "echo\n")
	v, ok := LoadKitVersion(st, "app", 0)
	if !ok || v.Err != nil {
		t.Fatalf("load: %v %v", ok, v.Err)
	}
	if v.Version != 1 || v.TreeHash != tr.Hash() || v.Kit.Notes[0].Body != "# R\n" || v.Kit.BuildContext != tr.ContextHash() {
		t.Fatalf("version = %+v", v)
	}
	if !strings.HasPrefix(string(v.KitYML), "kind: studio\n") || v.Key() != "tree:"+tr.Hash() {
		t.Fatalf("kit.yml %q key %q", v.KitYML, v.Key())
	}
	f, err := v.Files.Open("run.sh")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	if string(b) != "echo\n" {
		t.Fatalf("run.sh = %q", b)
	}
	if fsys, ok := KitFS(st, tr.Hash()); !ok || fsys == nil {
		t.Fatal("KitFS of a stored tree")
	}
	if _, ok := KitFS(st, strings.Repeat("0", 64)); ok {
		t.Fatal("KitFS of an unknown tree")
	}
}

func TestLoadKitVersionTextAndBroken(t *testing.T) {
	st := NewMemStore()
	doc := Kit{Name: "old", Current: 1, Versions: map[int]string{1: `{"kind":"studio","prompt":"p"}`}, Broken: map[int]string{2: "its base.context is not a readable tar.gz"}}
	if err := st.PutKitDocs([]Kit{doc}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	v, ok := LoadKitVersion(st, "old", 1)
	if !ok || v.Err != nil || v.Kit.Prompt != "p" || v.Files != nil || !strings.HasPrefix(v.Key(), "text:") {
		t.Fatalf("text version = %+v", v)
	}
	b, ok := LoadKitVersion(st, "old", 2)
	if !ok || b.Err == nil || b.Err.Error() != "kit old@v2 could not be migrated to a tree: its base.context is not a readable tar.gz" {
		t.Fatalf("broken version = %+v", b)
	}
	if _, ok := LoadKitVersion(st, "old", 3); ok {
		t.Fatal("a missing version must be ok=false")
	}
}

func TestLoadKitVersionReturnsCopies(t *testing.T) {
	st := NewMemStore()
	pushTree(t, st, "app", "kit.yml", "kind: studio\negress: [a.com]\n")
	v, _ := LoadKitVersion(st, "app", 0)
	v.Kit.Egress[0] = "mutated"
	if again, _ := LoadKitVersion(st, "app", 0); again.Kit.Egress[0] != "a.com" {
		t.Fatal("the parse cache handed out a shared slice")
	}
}

func TestResolvedChainCarriesFiles(t *testing.T) {
	st := NewMemStore()
	parent := pushTree(t, st, "web", "kit.yml", "kind: studio\n", "Dockerfile", dockerfile)
	pushTree(t, st, "app", "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", dockerfile+"RUN true\n")
	def, err := StudioKitDefinition(st, "app", harnessinstall.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Chain) != 2 || def.Chain[0].Files == nil || def.Chain[1].Files == nil {
		t.Fatalf("chain = %+v", def.Chain)
	}
	if def.Chain[0].Kit.BuildContext != parent.ContextHash() || def.Chain[1].Kit.BuildContext == "" {
		t.Fatalf("build contexts %q %q", def.Chain[0].Kit.BuildContext, def.Chain[1].Kit.BuildContext)
	}
	if _, err := def.Chain[1].Files.Stat("Dockerfile"); err != nil {
		t.Fatal(err)
	}
}
```

Add `func fsMode(m int) fs.FileMode { return fs.FileMode(m) }` in the test file (import `io/fs`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'LoadKitVersion|ResolvedChainCarriesFiles'`
Expected: FAIL, `undefined: LoadKitVersion`.

- [ ] **Step 3: Implement**

```go
// internal/jam/kit_version.go
package jam

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/go-git/go-billy/v5"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitReader is what reading a stored kit version needs: a Store, a
// planner's or exporter's memState, a kit set's overlay.
type KitReader interface {
	GetKit(name string) (Kit, bool)
	KitTree(sha string) (kittree.Tree, bool)
	KitBlob(sha string) ([]byte, bool)
}

// KitFS is the read-only view of stored tree sha; ok=false for an unknown tree.
func KitFS(r KitReader, sha string) (billy.Filesystem, bool) {
	t, ok := r.KitTree(sha)
	if !ok {
		return nil, false
	}
	return kittree.NewFS(t, r.KitBlob), true
}

// KitVersion is one stored kit version as every reader sees it.
type KitVersion struct {
	Name     string
	Version  int
	TreeHash string           // "" for a pre-tree version
	Tree     kittree.Tree     // zero for a pre-tree version
	KitYML   []byte           // kit.yml; a pre-tree version's stored config text
	Files    billy.Filesystem // the version's files; nil for a pre-tree version
	Kit      studio.StudioKit // parsed (note files read, BuildContext set); zero when Err is set
	Err      error            // the version does not parse, or is broken
}

// Key is the version's content identity, for caches and "unchanged" checks:
// "tree:<hash>", or "text:<sha256>" of a pre-tree version's config.
func (v KitVersion) Key() string {
	if v.TreeHash != "" {
		return "tree:" + v.TreeHash
	}
	sum := sha256.Sum256(v.KitYML)
	return "text:" + hex.EncodeToString(sum[:])
}

// LoadKitVersion reads kit name's version (0 = current). ok=false: no such
// kit or version. A broken version, an unstored tree or a kit.yml that does
// not parse is returned with Err set (KitYML still set when it was read).
func LoadKitVersion(r KitReader, name string, version int) (KitVersion, bool) {
	k, ok := r.GetKit(name)
	if !ok {
		return KitVersion{}, false
	}
	if version == 0 {
		version = k.Current
	}
	v := KitVersion{Name: name, Version: version}
	if reason, broken := k.Broken[version]; broken {
		v.Err = fmt.Errorf("kit %s@v%d could not be migrated to a tree: %s", name, version, reason)
		return v, true
	}
	if sha, ok := k.Trees[version]; ok {
		v.TreeHash = sha
		t, ok := r.KitTree(sha)
		if !ok {
			v.Err = fmt.Errorf("kit %s@v%d: tree %.12s is not stored", name, version, sha)
			return v, true
		}
		blobs := kittree.Blobs(r.KitBlob)
		v.Tree, v.Files = t, kittree.NewFS(t, blobs)
		if e, ok := t.Lookup(kittree.KitFile); ok {
			v.KitYML, _ = blobs(e.SHA256)
		}
		v.Kit, v.Err = parseTreeCached(sha, t, blobs)
		return v, true
	}
	if text, ok := k.Versions[version]; ok {
		v.KitYML = []byte(text)
		v.Kit, v.Err = studio.ParseStudioKit([]byte(text))
		return v, true
	}
	return KitVersion{}, false
}

// kitParses caches successful tree parses by tree hash: trees are immutable,
// so a hash always parses the same. Failures are not cached.
var kitParses sync.Map // tree sha256 → studio.StudioKit

func parseTreeCached(sha string, t kittree.Tree, blobs kittree.Blobs) (studio.StudioKit, error) {
	if sk, ok := kitParses.Load(sha); ok {
		return cloneKit(sk.(studio.StudioKit)), nil
	}
	sk, err := studio.ParseTree(t, blobs)
	if err != nil {
		return studio.StudioKit{}, err
	}
	kitParses.Store(sha, sk)
	return cloneKit(sk), nil
}

// cloneKit copies sk's slices and maps, so a caller cannot reach the cache.
func cloneKit(sk studio.StudioKit) studio.StudioKit {
	sk.Egress = slices.Clone(sk.Egress)
	sk.BuildArgs = maps.Clone(sk.BuildArgs)
	sk.Secrets = maps.Clone(sk.Secrets)
	sk.Notes = slices.Clone(sk.Notes)
	sk.MCPServers = maps.Clone(sk.MCPServers)
	sk.Base.ContextFiles = maps.Clone(sk.Base.ContextFiles)
	return sk
}
```

`kitchain.go`:

```go
// KitLink is one resolved kit in a chain: root first, the kit itself last.
type KitLink struct {
	Ref    KitRef // ID, Version (0 for a kit not yet pushed), Digest = the link's own BuildDigest
	Kit    studio.StudioKit
	Follow bool             // reached by a following (unpinned) reference
	Files  billy.Filesystem // the version's files (its build context, when Kit.BuildContext is set); nil for a kit not yet pushed or a pre-tree version
}
```

In `ResolveKitChain(store KitReader, …)`, replace the `KitConfig` + `ParseStudioKit` pair:

```go
		pv, ok := LoadKitVersion(store, name, version)
		if !ok {
			return nil, chainErr("kit %s has no version v%d (parent of %s)", name, version, curName)
		}
		if pv.Err != nil {
			return nil, chainErr("kit %s@v%d (parent of %s) is invalid: %v", name, version, curName, pv.Err)
		}
		parent := pv.Kit
		rev = append(rev, KitLink{Ref: KitRef{ID: name, Version: version, Digest: studio.BuildDigest(parent, h)}, Kit: parent, Follow: pin == 0, Files: pv.Files})
```

The other readers in `kitchain.go`:
- `KitsReferencing` uses `cur, ok := LoadKitVersion(store, k.Name, k.Current); if !ok || cur.Err != nil || !cur.Kit.Base.HasParent() { continue }`.
- `recordedChainKit(store KitReader, …)` uses `LoadKitVersion(store, r.ID, r.Version)`, mapping `!ok` to "is missing" and `Err` to "is invalid: %w" with the same texts as now.
- `chainCacheKey` becomes:

```go
func chainCacheKey(store KitReader, sk studio.StudioKit) string {
	var b strings.Builder
	for depth := 1; sk.Base.HasParent(); depth++ {
		name, pin, err := sk.Base.ParentRef()
		if err != nil || depth >= MaxKitChain {
			return chainUnresolved
		}
		pv, ok := LoadKitVersion(store, name, pin)
		if !ok || pv.Err != nil {
			return chainUnresolved
		}
		fmt.Fprintf(&b, "%s@v%d:%s;", name, pv.Version, pv.Key())
		sk = pv.Kit
	}
	return b.String()
}
```

Pin `0` makes `LoadKitVersion` read the parent's current version, exactly as before. Drop the `crypto/sha256` import when nothing else in the file uses it.

`studiokit.go`:

```go
func studioKitCurrent(store Store, name string) (KitRef, KitVersion, error) {
	if !studio.TagSafeName(name) {
		return KitRef{}, KitVersion{}, fmt.Errorf("kit name %q is not tag-safe", name)
	}
	v, ok := LoadKitVersion(store, name, 0)
	if !ok || v.Version == 0 {
		return KitRef{}, KitVersion{}, fmt.Errorf("studio kit %q not in registry", name)
	}
	return KitRef{ID: name, Version: v.Version}, v, nil
}

func StudioKitDefinition(store Store, name string, h harnessinstall.Install) (KitDefinition, error) {
	ref, v, err := studioKitCurrent(store, name)
	if err != nil {
		return KitDefinition{}, err
	}
	def, err := kitDefinitionFromVersion(store, ref, v, h)
	if err != nil {
		return KitDefinition{}, fmt.Errorf("studio kit %q: %w", name, err)
	}
	return def, nil
}

func ResolveKitDefinition(store Store, ref KitRef, h harnessinstall.Install) (KitDefinition, bool, error) {
	v, ok := LoadKitVersion(store, ref.ID, ref.Version)
	if !ok {
		return KitDefinition{}, false, nil
	}
	def, err := kitDefinitionFromVersion(store, ref, v, h)
	return def, err == nil, err
}

// kitDefinitionFromVersion is the effective definition of stored version v
// (ref): its chain resolved from store, Kit = the merged chain, the last link
// carrying v's files, Ref.Digest = studio.ChainDigest(chain, h). A version
// that does not parse, or a missing or invalid parent, fails closed.
func kitDefinitionFromVersion(store Store, ref KitRef, v KitVersion, h harnessinstall.Install) (KitDefinition, error) {
	if v.Err != nil {
		return KitDefinition{}, fmt.Errorf("resolve kit %s: %w", ref, v.Err)
	}
	sk := v.Kit
	chain, err := ResolveKitChain(store, ref.ID, sk)
	if err != nil {
		return KitDefinition{}, fmt.Errorf("resolve kit %s: %w", ref, err)
	}
	self := &chain[len(chain)-1]
	self.Ref.Version, self.Files = ref.Version, v.Files
	kits := chainKits(chain)
	ref.Digest = studio.ChainDigest(kits, h)
	merged := sk
	if len(chain) > 1 {
		merged = studio.Merge(kits)
	}
	return KitDefinition{Ref: ref, Kit: merged, Chain: chain, Harness: h}, nil
}
```

In `ensureKit`, the unchanged check becomes `if cur, ok := LoadKitVersion(store, name, 0); ok && cur.Version != 0 && string(cur.KitYML) == text {`. Since Task 5, a `PushKit` of `text` stores exactly `text` as `kit.yml`, so this is the same comparison as before. In `studiokit_test.go`, the test that called `kitDefinitionFromConfig(NewMemStore(), ref, text, h)` now calls `kitDefinitionFromVersion(NewMemStore(), ref, KitVersion{Version: 1, KitYML: []byte(text), Kit: mustParse(t, text)}, h)`; for an invalid `text`, set `Err` from `studio.ParseStudioKit` instead. The test file defines `mustParse`.

`supervisor.go`:

```go
type kitSource struct {
	ref   KitRef
	ver   KitVersion
	named bool // role.Kit names it (vs the wiring default) — error wording only
}

func (k kitSource) definition(store Store, h harnessinstall.Install) (KitDefinition, error) {
	def, err := kitDefinitionFromVersion(store, k.ref, k.ver, h)
	…unchanged…
}
```

In `kitSourceFor`:
- Named: `ref, ver, err := studioKitCurrent(s.store, role.Kit)` → `kitSource{ref: ref, ver: ver, named: true}`.
- Default: `ver, ok := LoadKitVersion(s.store, s.defaultStudioKit.ID, s.defaultStudioKit.Version)` → `kitSource{ref: …, ver: ver}`.

In `currentKitRef`:
- Compute the chain key as `chain := chainUnresolved; if src.ver.Err == nil { chain = chainCacheKey(s.store, src.ver.Kit) }`.
- Key with `content: src.ver.Key()`.
- In `kitRefKey`, rename `config string` to `content string // the version's content identity (KitVersion.Key): a removed-and-re-pushed kit can reuse a version number`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./... && just lint`
Expected: PASS. The supervisor's `kitRefs.misses` tests still hold: an unchanged version has an unchanged `Key()`.

- [ ] **Step 5: Commit**

```bash
git add -A internal/jam
git commit -m "jam: LoadKitVersion, the one reader of a stored kit version; chains carry the files

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: The launcher builds a kit's own stage from its tree

**Files:**
- Modify: `internal/jam/launcher/prepare.go` (`resolveStudioBase`, `resolveOwnBase`, `resolveChainStage`, `kitStageTag`, new `contextReader`), `internal/jam/launcher/prepare_chain_test.go` (`mustStage`, `TestKitStageTagFormula` take a `studio.StudioKit`), `docs/usage/jam/kits.md` (the build-digest paragraph), `docs/usage/jam/kits-inheritance.md` (a child's own stage), `docs/usage/jam/coves.md` (one sentence)
- Test: `internal/jam/launcher/prepare_tree_test.go`

**Interfaces:**
- Consumes: Task 7's `jam.KitLink.Files`; Task 4's `StudioKit.BuildContext` and `studio.ParseTree`; Task 3's `kittree.ContextTar`.
- Produces:
```go
func kitStageTag(asm, parentRef string, sk studio.StudioKit) (string, error) // JSON(sk.Base), then NUL + sk.BuildContext when set
func contextReader(link jam.KitLink) (io.Reader, error)
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/jam/launcher/prepare_tree_test.go
package launcher

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// treeLink is chain link name@vN read from a tree of path → content files.
func treeLink(t *testing.T, name string, version int, kv ...string) jam.KitLink {
	t.Helper()
	var files []kittree.File
	for i := 0; i+1 < len(kv); i += 2 {
		files = append(files, kittree.File{Path: kv[i], Mode: 0o644, Data: []byte(kv[i+1])})
	}
	tr, blobs, err := kittree.Build(files)
	if err != nil {
		t.Fatal(err)
	}
	sk, err := studio.ParseTree(tr, kittree.MapBlobs(blobs))
	if err != nil {
		t.Fatal(err)
	}
	return jam.KitLink{Ref: KitRef{ID: name, Version: version, Digest: studio.BuildDigest(sk, harnessinstall.Default())}, Kit: sk, Files: kittree.NewFS(tr, kittree.MapBlobs(blobs))}
}

func tarNames(t *testing.T, b []byte) string {
	t.Helper()
	var names []string
	r := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := r.Next()
		if err == io.EOF {
			return strings.Join(names, ",")
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

const df = "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n"

func TestTreeKitStreamsItsDirectoryMinusKitYML(t *testing.T) {
	ops := &fakeOps{}
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	link := treeLink(t, "app", 1, "kit.yml", "kind: studio\n", "Dockerfile", df, "bin/run.sh", "echo")
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	if _, err := l.PrepareKit(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	if got := tarNames(t, ops.resolvedTar); got != "Dockerfile,bin/run.sh" {
		t.Fatalf("context tar = %s", got)
	}
}

func TestTreeKitWithoutFilesFailsClosed(t *testing.T) {
	l := newPrepareLauncher(&fakeOps{}, &fakeInv{}, noAsm)
	link := treeLink(t, "app", 1, "kit.yml", "kind: studio\n", "Dockerfile", df)
	link.Files = nil
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	if _, err := l.PrepareKit(context.Background(), def); err == nil || !strings.Contains(err.Error(), "files are not loaded") {
		t.Fatalf("err = %v", err)
	}
}

func TestChainTreeStageBuildsOnItsParent(t *testing.T) {
	ops := &fakeOps{}
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	root := treeLink(t, "web", 1, "kit.yml", "kind: studio\n")
	child := treeLink(t, "app", 2, "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", df+"RUN true\n")
	if _, err := l.PrepareKit(context.Background(), chainDef(nil, root, child)); err != nil {
		t.Fatal(err)
	}
	if len(ops.stages) != 1 || ops.stages[0].parent != "blessed-default" || tarNames(t, ops.stages[0].tar) != "Dockerfile" {
		t.Fatalf("stages = %+v", ops.stages)
	}
}

func TestStageTagKeyedOnBuildContext(t *testing.T) {
	a := treeLink(t, "app", 1, "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", df)
	b := treeLink(t, "app", 2, "kit.yml", "kind: studio\nbase: {kit: web}\nprompt: edited\n", "Dockerfile", df)
	c := treeLink(t, "app", 3, "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", df+"RUN x\n")
	ta, _ := kitStageTag("asm", "p", a.Kit)
	tb, _ := kitStageTag("asm", "p", b.Kit)
	tc, _ := kitStageTag("asm", "p", c.Kit)
	if ta != tb {
		t.Fatal("a kit.yml-only edit must reuse the stage")
	}
	if ta == tc {
		t.Fatal("a Dockerfile edit must make a new stage")
	}
}
```

`fakeOps.ResolveKitBase` answers `"blessed-default"` when `resolvedBase` is unset (`launcher_test.go`), so the plain root resolves to it and the child's stage builds on it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/launcher/ -run 'Tree|StageTagKeyed'`
Expected: FAIL. The tree kit has no `ContextFiles`/`Context`, so it resolves through `ResolveKitBase` and `resolvedTar` stays empty; `kitStageTag` does not take a `StudioKit`.

- [ ] **Step 3: Implement**

In `prepare.go`:

```go
func (l *Launcher) resolveStudioBase(def KitDefinition) (string, error) {
	if len(def.Chain) <= 1 {
		// Guard on the chain's own link, not def.Kit.Base: Merge clears
		// Base.Kit, so the merged kit cannot show a named parent.
		own := jam.KitLink{Ref: def.Ref, Kit: def.Kit}
		if len(def.Chain) == 1 {
			own = def.Chain[0]
		}
		return l.resolveOwnBase(own)
	}
	root := def.Chain[0]
	ref, err := l.resolveOwnBase(root)
	if err != nil {
		return "", fmt.Errorf("kit %s: %w", root.Ref, err)
	}
	for _, link := range def.Chain[1:] {
		if ref, err = l.resolveChainStage(link, ref); err != nil {
			return "", fmt.Errorf("kit %s: %w", link.Ref, err)
		}
	}
	return ref, nil
}

// resolveOwnBase resolves a parentless link's base. A kit whose tree holds a
// Dockerfile streams its directory (minus kit.yml) to the backend's `docker
// build -` FROM the blessed base, gated. A packed context (a pre-tree
// version) is pre-flighted by studio.ScanContextTar and streamed the same
// way. Otherwise the declared Base.Image is resolved and gated ("" → the
// blessed default). A base naming a parent kit is refused (fail closed).
func (l *Launcher) resolveOwnBase(link jam.KitLink) (string, error) {
	b := link.Kit.Base
	if b.HasParent() {
		return "", fmt.Errorf("base names parent kit %q, which the kit chain does not resolve", b.Kit)
	}
	if link.Kit.BuildContext != "" {
		r, err := contextReader(link)
		if err != nil {
			return "", err
		}
		return l.cfg.Ops.ResolveKitBaseTar(r)
	}
	kind, err := b.Kind()
	if err != nil {
		return "", err
	}
	switch kind {
	case studio.BaseContextFiles, studio.BaseContextZip:
		tarBytes, err := scannedContextTar(b)
		if err != nil {
			return "", err
		}
		return l.cfg.Ops.ResolveKitBaseTar(bytes.NewReader(tarBytes))
	default:
		return l.cfg.Ops.ResolveKitBase(b.Image)
	}
}

// resolveChainStage resolves one non-root chain link on parentRef. A link
// with no own context is the parent's stage as is. Otherwise its stage is
// cached under kitStageTag: a present tag was applied only after the stage's
// gate passed, so it is reused; a missing one is built by streaming the
// link's context (its tree minus kit.yml, or a pre-tree packed context) FROM
// parentRef, gated, and tagged.
func (l *Launcher) resolveChainStage(link jam.KitLink, parentRef string) (string, error) {
	b := link.Kit.Base
	if !b.HasParent() {
		return "", fmt.Errorf("a non-root chain link must name its parent kit")
	}
	tree := link.Kit.BuildContext != ""
	if !tree {
		kind, err := b.Kind()
		if err != nil {
			return "", err
		}
		switch kind {
		case studio.BaseKit:
			return parentRef, nil
		case studio.BaseContextFiles, studio.BaseContextZip:
		default:
			return "", fmt.Errorf("unsupported base for a chain link")
		}
	}
	stage, err := kitStageTag(l.asm, parentRef, link.Kit)
	if err != nil {
		return "", err
	}
	unlock := l.lockKey(stage)
	defer unlock()
	if ok, err := l.cfg.Ops.HasKitBase(stage); err != nil {
		return "", fmt.Errorf("stage %s: %w", stage, err)
	} else if ok {
		return stage, nil
	}
	var r io.Reader
	if tree {
		if r, err = contextReader(link); err != nil {
			return "", err
		}
	} else {
		tarBytes, err := scannedContextTar(b)
		if err != nil {
			return "", err
		}
		r = bytes.NewReader(tarBytes)
	}
	return l.cfg.Ops.ResolveKitBaseTarOn(r, parentRef, stage)
}

// contextReader is link's build context, its tree minus kit.yml, as a
// deterministic tar (kittree.ContextTar), buffered (a tree is at most 64 MiB).
func contextReader(link jam.KitLink) (io.Reader, error) {
	if link.Files == nil {
		return nil, fmt.Errorf("kit %s has a Dockerfile but its files are not loaded", link.Ref)
	}
	var buf bytes.Buffer
	if err := kittree.ContextTar(&buf, link.Files); err != nil {
		return nil, err
	}
	return &buf, nil
}

// kitStageTag is the local tag a chain stage is cached under:
// cove-kit-base:<first 32 hex of sha256(asm, NUL, parentRef, NUL, JSON(own
// base)[, NUL, build-context hash])>. The build-context hash is appended only
// when the kit has one, so a pre-tree packed-context stage keeps its tag. …
// (keep the rest of the existing comment)
func kitStageTag(asm, parentRef string, sk studio.StudioKit) (string, error) {
	js, err := json.Marshal(sk.Base)
	if err != nil {
		return "", fmt.Errorf("stage digest: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(asm))
	h.Write([]byte{0})
	h.Write([]byte(parentRef))
	h.Write([]byte{0})
	h.Write(js)
	if sk.BuildContext != "" {
		h.Write([]byte{0})
		h.Write([]byte(sk.BuildContext))
	}
	return "cove-kit-base:" + hex.EncodeToString(h.Sum(nil))[:32], nil
}
```

Add the imports `"io"` and `"github.com/aethons-tools/cove/internal/kittree"`. In `prepare_chain_test.go`:
- `mustStage(t, parentRef, b)` calls `kitStageTag(…, studio.StudioKit{Base: b})`.
- `TestKitStageTagFormula` passes `studio.StudioKit{Base: b}` (the formula for a kit without a build context is unchanged, so its expected value stands).
- Update every other `kitStageTag(…, base)` call the same way.

Docs:
- `docs/usage/jam/kits.md`, the build-digest paragraph under "Building on another kit": `base` + `egress` + … becomes `base` + **the build context's tree hash** + `egress` + `build-args` + `mcp-servers` + the harness. Add one sentence: "The build context is the kit's directory minus `kit.yml`, so an edit to `kit.yml` alone (prompt, notes with an inline body, secrets) reuses the image, while an edit to any other file of a `Dockerfile` kit rebuilds it."
- `docs/usage/jam/kits-inheritance.md`, the paragraph saying a child's own stage is "`context-files`, `context` or `context-dir`": replace it with "a `Dockerfile` next to the child's `kit.yml`; its directory (minus `kit.yml`) is the stage's build context, built `FROM ${COVE_BASE_IMAGE}`, the parent's stage". Keep the paragraph's other rules (the stage cache, the gate).
- `docs/usage/jam/coves.md`, the sentence "A Dockerfile-context base is not yet buildable (deferred).": it becomes "A kit's own stage is built from its directory, minus `kit.yml` (<link `kits.md` as "kits.md">)." The doc does not grow.

Bump `updated:` on each doc touched.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/launcher/ ./internal/jam/... && just lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A internal/jam/launcher docs/usage/jam/kits.md docs/usage/jam/kits-inheritance.md docs/usage/jam/coves.md
git commit -m "launcher: build a kit's own stage from its directory (tree minus kit.yml)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Push a kit directory

**Files:**
- Create: `internal/jam/kit_push.go`, `cmd/at-jam/kitpush.go`
- Modify: `internal/jam/studiokit.go` (`validateKit` returns only the chain; `EnsureStudioKit` and `PushStudioKit` go through `PushKitFiles`; delete `ensureStudioKit`/`ensureKit`), `internal/jam/kitset.go` (`resolveKitEntries` takes the canonical text from `ToJSON` itself), `internal/jam/admin.go` (`POST /admin/kits/{name}/versions`; `KitConfigResult.Tree`/`Files`; `GET /admin/kits/{name}` through `LoadKitVersion`), `internal/jam/adminclient/adminclient.go` (`PushKitTree`), `internal/jam/adminui/kits.go` (new version → `ReplaceKitYML`), `cmd/at-jam/main.go` (`kit push`, `kit show`, the `kit` usage line), `docs/usage/jam/kits.md`, `docs/usage/jam/INDEX.md`; create `docs/usage/jam/kits-directory.md`
- Test: `internal/jam/kit_push_test.go`, `internal/jam/admin_kitpush_test.go`, `cmd/at-jam/kitpush_test.go`. Fixtures that push a *packed context* through `PushStudioKit`/`EnsureStudioKit` switch to `store.PushKit` (Step 4).

**Interfaces:**
- Consumes: `kittree.Ignore`, `kittree.Build`, `kittree.ReadDir`, `kittree.ReadTar`, `kittree.WriteTar`, `kittree.MaxUpload` (Tasks 1–2); `studio.RefuseContextFields`, `studio.ParseTree`, `studio.RenderKitYAML` (Task 4); `Store.PushKitVersion`, `KitObjects` (Task 5); `LoadKitVersion` (Task 7).
- Produces:
```go
// package jam
func PushKitFiles(store Store, name string, files []kittree.File) (KitRef, bool, error) // the one push path
func ReplaceKitYML(store Store, name, config string) (KitRef, bool, error)            // current files, kit.yml replaced
func PushStudioKit(store Store, name, config string) (version int, unchanged bool, err error) // a one-file push (unchanged signature)
func EnsureStudioKit(store Store, name string, sk studio.StudioKit) (KitRef, error)   // unchanged signature
func kitTreeOf(name string, files []kittree.File) (kittree.Tree, KitObjects, studio.StudioKit, error)
func validateKit(store KitReader, name string, sk studio.StudioKit) ([]KitLink, error)
type KitFileInfo struct { Path string `json:"path"`; Mode string `json:"mode"`; Size int64 `json:"size"` }
// KitConfigResult gains: Tree string `json:"tree,omitempty"`; Files []KitFileInfo `json:"files,omitempty"`
// route: POST /admin/kits/{name}/versions  (application/x-tar) → 201 KitResult
// package adminclient
func (c *Client) PushKitTree(name string, tarBody []byte) (jam.KitResult, error)
// package main (cmd/at-jam)
func kitPushFiles(pos []string, config, name string) ([]kittree.File, string, error)
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/jam/kit_push_test.go
package jam

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

func files(kv ...string) []kittree.File {
	var out []kittree.File
	for i := 0; i+1 < len(kv); i += 2 {
		p, mode := kv[i], 0o644
		if strings.HasSuffix(p, "*") {
			p, mode = strings.TrimSuffix(p, "*"), 0o755
		}
		out = append(out, kittree.File{Path: p, Mode: fsMode(mode), Data: []byte(kv[i+1])})
	}
	return out
}

func TestPushKitFilesStoresTheDirectory(t *testing.T) {
	st := NewMemStore()
	ref, unchanged, err := PushKitFiles(st, "app", files("kit.yml", "kind: studio\n# a comment\n", "Dockerfile", dockerfile, "run.sh*", "echo\n", ".dockerignore", "*.log\n", "debug.log", "x"))
	if err != nil || unchanged || ref.Version != 1 {
		t.Fatalf("push = %+v %v %v", ref, unchanged, err)
	}
	v, _ := LoadKitVersion(st, "app", 0)
	if string(v.KitYML) != "kind: studio\n# a comment\n" {
		t.Fatalf("kit.yml not stored verbatim: %q", v.KitYML)
	}
	if _, ok := v.Tree.Lookup("debug.log"); ok {
		t.Fatal(".dockerignore must filter the push")
	}
	if e, _ := v.Tree.Lookup("run.sh"); e.Mode != 0o755 {
		t.Fatal("exec bit lost")
	}
	if ref.Digest != studio.ChainDigest([]studio.StudioKit{v.Kit}, harnessDefault()) || v.Kit.BuildContext == "" {
		t.Fatalf("digest %s, build context %q", ref.Digest, v.Kit.BuildContext)
	}
	again, unchanged, err := PushKitFiles(st, "app", files("kit.yml", "kind: studio\n# a comment\n", "Dockerfile", dockerfile, "run.sh*", "echo\n", ".dockerignore", "*.log\n"))
	if err != nil || !unchanged || again.Version != 1 {
		t.Fatalf("same tree = %+v %v %v", again, unchanged, err)
	}
}

func TestPushKitFilesRefusals(t *testing.T) {
	st := NewMemStore()
	for name, tc := range map[string]struct {
		files []kittree.File
		want  string
	}{
		"no kit.yml":   {files("Dockerfile", "FROM x"), "kit app: kit.yml missing at the root"},
		"context":      {files("kit.yml", "kind: studio\nbase:\n  context: H4sI\n"), "base.context is no longer supported; put a Dockerfile next to kit.yml"},
		"context-dir":  {files("kit.yml", "kind: studio\nbase:\n  context-dir: ctx\n"), "base.context-dir is no longer supported; put a Dockerfile next to kit.yml"},
		"path parent":  {files("kit.yml", "kind: studio\nbase: {kit: ../web}\n"), "path parents resolve only in a git import (kit import/track --git)"},
		"invalid kit":  {files("kit.yml", "kind: nope\n"), `studio kit: kind must be "studio", got "nope"`},
		"missing note": {files("kit.yml", "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: a.md}\n"), `note "a.md": file a.md is not in the kit`},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := PushKitFiles(st, "app", tc.files)
			if err == nil || err.Error() != tc.want || WriteStatus(err, 500) != 400 {
				t.Fatalf("err = %v (status %d), want %q", err, WriteStatus(err, 500), tc.want)
			}
		})
	}
}

func TestReplaceKitYMLKeepsTheOtherFiles(t *testing.T) {
	st := NewMemStore()
	if _, _, err := PushKitFiles(st, "app", files("kit.yml", "kind: studio\n", "run.sh*", "echo\n")); err != nil {
		t.Fatal(err)
	}
	ref, _, err := ReplaceKitYML(st, "app", "kind: studio\nprompt: hi\n")
	if err != nil || ref.Version != 2 {
		t.Fatalf("replace = %+v %v", ref, err)
	}
	v, _ := LoadKitVersion(st, "app", 0)
	if e, ok := v.Tree.Lookup("run.sh"); !ok || e.Mode != 0o755 || v.Kit.Prompt != "hi" {
		t.Fatalf("tree = %+v", v.Tree)
	}
}

func TestEnsureDefaultStudioKitIsAOneFileTree(t *testing.T) {
	st := NewMemStore()
	ref, err := EnsureDefaultStudioKit(st)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := LoadKitVersion(st, studio.DefaultStudioKitID, ref.Version)
	want, _ := studio.RenderKitYAML(studio.DefaultStudioKit())
	if len(v.Tree.Entries) != 1 || string(v.KitYML) != string(want) {
		t.Fatalf("default kit tree %+v kit.yml %q", v.Tree, v.KitYML)
	}
	if again, _ := EnsureDefaultStudioKit(st); again.Version != ref.Version {
		t.Fatal("seeding the default kit twice made a new version")
	}
}
```

Add `func harnessDefault() harnessinstall.Install { return harnessinstall.Default() }` to the test file, with the import.

```go
// internal/jam/admin_kitpush_test.go
package jam

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kittree"
)

func TestPushTarRoute(t *testing.T) {
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	post := func(name string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/admin/kits/"+name+"/versions", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/x-tar")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	var body bytes.Buffer
	if err := kittree.WriteTar(&body, files("kit.yml", "kind: studio\n", "Dockerfile", dockerfile)); err != nil {
		t.Fatal(err)
	}
	w := post("app", body.Bytes())
	var res KitResult
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Version != 1 {
		t.Fatalf("push = %d %s", w.Code, w.Body)
	}
	g := httptest.NewRecorder()
	h.ServeHTTP(g, httptest.NewRequest("GET", "/admin/kits/app", nil))
	var got KitConfigResult
	if json.Unmarshal(g.Body.Bytes(), &got) != nil || got.Tree == "" || len(got.Files) != 2 || got.Files[0].Path != "Dockerfile" || got.Files[0].Mode != "0644" {
		t.Fatalf("show = %s", g.Body)
	}
	if w := post("app", []byte("garbage, not a tar, garbage, not a tar")); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not a readable tar") {
		t.Fatalf("garbage = %d %s", w.Code, w.Body)
	}
	if w := post("bad%20name", body.Bytes()); w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe name = %d", w.Code)
	}
	// Shrink the cap (MaxUpload = MaxTotal + MaxEntries×2 KiB + 1 MiB) under one
	// big file, and lift the per-file limit, so the body cap trips first.
	e, to, f := kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile
	kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile = 1, 1024, 8<<20
	t.Cleanup(func() { kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile = e, to, f })
	var big bytes.Buffer
	if err := kittree.WriteTar(&big, []kittree.File{{Path: "kit.yml", Data: make([]byte, 2<<20)}}); err != nil {
		t.Fatal(err)
	}
	if w := post("app", big.Bytes()); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d %s", w.Code, w.Body)
	}
}
```

```go
// cmd/at-jam/kitpush_test.go
package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestKitPushDirectory(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	dir := filepath.Join(t.TempDir(), "web")
	for p, body := range map[string]string{"kit.yml": "kind: studio\n", "Dockerfile": "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n", "run.sh": "echo\n"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	getenv := func(string) string { return "" }
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, dir}, getenv, &out, &errb); code != 0 || out.String() != "pushed web v1\n" {
		t.Fatalf("push: exit=%d out=%q err=%q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, dir}, getenv, &out, &errb); code != 0 || out.String() != "web unchanged (current v1)\n" {
		t.Fatalf("repush: %q %q", out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"kit", "show", "--admin-url", ts.URL, "--own", "web"}, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "#   0755") || !strings.Contains(out.String(), "run.sh") {
		t.Fatalf("show:\n%s", out.String())
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "leak")); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, dir}, getenv, &out, &errb); code == 0 || !strings.Contains(errb.String(), "leak: symlinks are not allowed") {
		t.Fatalf("symlink: exit=%d err=%q", code, errb.String())
	}
}

func TestKitPushArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := cmdKit([]string{"push"}, cliGlobals(), &out, &errb); code != 2 || !strings.Contains(errb.String(), "expected <dir> or --config <file>") {
		t.Fatalf("no args: %d %q", code, errb.String())
	}
	if code := cmdKit([]string{"push", "--config", "-"}, cliGlobals(), &out, &errb); code != 2 || !strings.Contains(errb.String(), "--name is required with --config") {
		t.Fatalf("stdin without name: %d %q", code, errb.String())
	}
}
```

`cliGlobals()` stands for `cli.Globals{}` (import `internal/cli`), as `TestKitPushRejectsMalformedConfig` uses it. `--config -` with no name fails before reading stdin, because no legacy name can be known without reading it. The CLI checks the flags first.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ ./cmd/at-jam/ -run 'PushKitFiles|ReplaceKitYML|EnsureDefaultStudioKitIsAOneFileTree|PushTarRoute|KitPushDirectory|KitPushArgs'`
Expected: FAIL, `undefined: PushKitFiles`.

- [ ] **Step 3: Implement**

```go
// internal/jam/kit_push.go
package jam

import (
	"fmt"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// kitTreeOf reads files as kit name's version, the way every push does: the
// root .dockerignore applied (ignored files are never stored), the tree
// limits, no packed-context field, kit.yml parsed with its note files read
// from the tree. Errors are the operator's input (callers map them to 400).
func kitTreeOf(name string, files []kittree.File) (kittree.Tree, KitObjects, studio.StudioKit, error) {
	files, err := kittree.Ignore(files)
	if err != nil {
		return kittree.Tree{}, KitObjects{}, studio.StudioKit{}, fmt.Errorf("kit %s: %w", name, err)
	}
	tree, blobs, err := kittree.Build(files)
	if err != nil {
		return kittree.Tree{}, KitObjects{}, studio.StudioKit{}, fmt.Errorf("kit %s: %w", name, err)
	}
	e, _ := tree.Lookup(kittree.KitFile)
	if err := studio.RefuseContextFields(blobs[e.SHA256]); err != nil {
		return kittree.Tree{}, KitObjects{}, studio.StudioKit{}, err
	}
	sk, err := studio.ParseTree(tree, kittree.MapBlobs(blobs))
	if err != nil {
		return kittree.Tree{}, KitObjects{}, studio.StudioKit{}, err
	}
	return tree, KitObjects{Trees: []kittree.Tree{tree}, Blobs: blobs}, sk, nil
}

// PushKitFiles records files as kit name's next version: the one push path
// of the tar API, the JSON API, the UI and the default kit. The kit is
// validated against its parent chain under kitMu (validateKit). A tree equal
// to the current version's makes no version (unchanged=true). Every refusal
// is a 400 WriteError.
func PushKitFiles(store Store, name string, files []kittree.File) (KitRef, bool, error) {
	tree, objs, sk, err := kitTreeOf(name, files)
	if err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	chain, err := validateKit(store, name, sk)
	if err != nil {
		return KitRef{}, false, err
	}
	digest := studio.ChainDigest(chainKits(chain), harnessinstall.Default())
	hash := tree.Hash()
	if k, ok := store.GetKit(name); ok && k.Current != 0 && k.Trees[k.Current] == hash {
		return KitRef{ID: name, Version: k.Current, Digest: digest}, true, nil
	}
	v, err := store.PushKitVersion(name, hash, objs, KitVersionMeta{PushedAt: time.Now().UTC(), Parent: chainParent(chain)}, nil)
	if err != nil {
		return KitRef{}, false, fmt.Errorf("studio kit %q: push: %w", name, err)
	}
	return KitRef{ID: name, Version: v, Digest: digest}, false, nil
}

// ReplaceKitYML pushes kit name's current files with kit.yml replaced by
// config: the UI's "push a new version", which edits kit.yml only. A kit
// with no tree yet is a one-file push.
func ReplaceKitYML(store Store, name, config string) (KitRef, bool, error) {
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
	return PushKitFiles(store, name, out)
}
```

`studiokit.go`:

```go
// EnsureStudioKit records sk under name as a one-file tree (kit.yml =
// studio.RenderKitYAML(sk)) and returns the ref a raise carries … (keep the
// rest of the doc). Idempotent: an unchanged definition renders the same
// kit.yml, so the same tree, and reuses the current version.
func EnsureStudioKit(store Store, name string, sk studio.StudioKit) (KitRef, error) {
	yml, err := studio.RenderKitYAML(sk)
	if err != nil {
		return KitRef{}, fmt.Errorf("studio kit %q: %w", name, err)
	}
	ref, _, err := PushKitFiles(store, name, []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: yml}})
	return ref, err
}

// validateKit is the push-time check of sk as kit name … (keep the doc,
// drop "the canonical JSON"): the name, the kit's own validity, no path
// parent, the parent chain resolved from store, and the merged kit's prompt
// and notes budgets. It returns the chain.
func validateKit(store KitReader, name string, sk studio.StudioKit) ([]KitLink, error) {
	if err := sk.CheckName(name); err != nil {
		return nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := sk.Validate(); err != nil {
		return nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if _, isPath := sk.Base.ParentPath(); isPath {
		return nil, writeErr(http.StatusBadRequest, "path parents resolve only in a git import (kit import/track --git)")
	}
	chain, err := ResolveKitChain(store, name, sk)
	if err != nil {
		return nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	merged := studio.Merge(chainKits(chain))
	if err := merged.CheckPrompt(); err != nil {
		return nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := merged.CheckNotes(); err != nil {
		return nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	return chain, nil
}

// PushStudioKit records config as kit name's next version, a one-file tree:
// the JSON admin API's push and the UI's new kit.
func PushStudioKit(store Store, name, config string) (version int, unchanged bool, err error) {
	ref, unchanged, err := PushKitFiles(store, name, []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: []byte(config)}})
	return ref.Version, unchanged, err
}
```

Delete `ensureStudioKit` and `ensureKit`. In `kitset.go` `resolveKitEntries`, replace `text, chain, err := validateKit(ov, e.Name, e.Kit)` with:

```go
		chain, err := validateKit(ov, e.Name, e.Kit)
		if err != nil {
			return nil, err
		}
		text, err := e.Kit.ToJSON()
		if err != nil {
			return nil, writeErr(http.StatusBadRequest, "studio kit %q: %s", e.Name, err.Error())
		}
```

Task 10 replaces it with the tree.

`admin.go`:

```go
// KitFileInfo is one file of a kit version, as GET /admin/kits/{name} lists it.
type KitFileInfo struct {
	Path string `json:"path"`
	Mode string `json:"mode"` // "0644" | "0755"
	Size int64  `json:"size"`
}
```

Add to `KitConfigResult`, after `Config`:
```go
	// Tree is the version's tree hash; "" for a pre-tree version.
	Tree string `json:"tree,omitempty"`
	// Files lists the version's files, kit.yml included, by path.
	Files []KitFileInfo `json:"files,omitempty"`
```

Change `GET /admin/kits/{name}` to read `v, ok := LoadKitVersion(store, name, version)`:
- `!ok` → 404 as now.
- `Config: string(v.KitYML)`, `Version: v.Version`, `Tree: v.TreeHash`, and one `KitFileInfo{Path, Mode: fmt.Sprintf("%04o", e.Mode), Size}` per `v.Tree.Entries`.
- The chain view stays `KitChainView(store, name, res.Config)`; Task 11 moves it to the parsed kit.

New route, next to `POST /admin/kits`:

```go
	mux.HandleFunc("POST /admin/kits/{name}/versions", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !studio.TagSafeName(name) {
			http.Error(w, "kit name is not tag-safe (allowed: [A-Za-z0-9_.-])", http.StatusBadRequest)
			return
		}
		files, err := kittree.ReadTar(http.MaxBytesReader(w, r.Body, kittree.MaxUpload()))
		if err != nil {
			if _, big := errors.AsType[*http.MaxBytesError](err); big {
				http.Error(w, fmt.Sprintf("kit upload over %d bytes", kittree.MaxUpload()), http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, fmt.Sprintf("kit %s: %s", name, err), http.StatusBadRequest)
			return
		}
		ref, unchanged, err := PushKitFiles(store, name, files)
		if err != nil {
			http.Error(w, err.Error(), WriteStatus(err, http.StatusInternalServerError))
			return
		}
		log.Info("admin kit pushed", "operator", OperatorID(r), "kit", name, "version", ref.Version, "unchanged", unchanged, "files", len(files))
		writeJSON(w, http.StatusCreated, KitResult{Name: name, Version: ref.Version, Unchanged: unchanged})
	})
```

`adminclient.go`:

```go
// PushKitTree pushes a kit directory, an uncompressed tar of its files
// (kittree.WriteTar), as kit name's next version.
func (c *Client) PushKitTree(name string, tarBody []byte) (jam.KitResult, error) {
	var res jam.KitResult
	err := c.doRaw("POST", "/admin/kits/"+url.PathEscape(name)+"/versions", "application/x-tar", tarBody, &res)
	return res, err
}
```

`adminui/kits.go`: `POST /app/admin/kits/{name}/versions` calls `jam.ReplaceKitYML(store, name, r.FormValue("config"))` and uses `ref.Version`. The new-kit form keeps `jam.PushStudioKit`.

```go
// cmd/at-jam/kitpush.go
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

var errKitPushUsage = errors.New("expected <dir> or --config <file> (- for stdin)")

// kitPushFiles reads what `kit push` sends and the name it goes under: the
// files of <dir> (its .dockerignore applied, symlinks refused) named --name,
// else kit.yml's legacy name:, else the dir's base name; or, with --config,
// the one file kit.yml, named --name or its legacy name:.
func kitPushFiles(pos []string, config, name string) ([]kittree.File, string, error) {
	switch {
	case len(pos) == 1 && config == "":
		root, err := os.OpenRoot(pos[0])
		if err != nil {
			return nil, "", fmt.Errorf("%s: not a directory", pos[0])
		}
		defer root.Close()
		files, err := kittree.ReadDir(root, ".")
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", pos[0], err)
		}
		if name == "" {
			abs, _ := filepath.Abs(pos[0])
			name = filepath.Base(abs)
		}
		return files, name, nil
	case len(pos) == 0 && config != "":
		data, err := readConfig(config)
		if err != nil {
			return nil, "", err
		}
		return []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: data}}, name, nil
	}
	return nil, "", errKitPushUsage
}

// checkKitPush validates files client-side as push would: the tree, no packed
// context, kit.yml parsed with its note files, the name and the authoring
// budgets. The legacy name: wins over a defaulted dir name.
func checkKitPush(files []kittree.File, name string, nameGiven bool) (string, error) {
	files, err := kittree.Ignore(files)
	if err != nil {
		return "", err
	}
	tree, blobs, err := kittree.Build(files)
	if err != nil {
		return "", err
	}
	e, _ := tree.Lookup(kittree.KitFile)
	if err := studio.RefuseContextFields(blobs[e.SHA256]); err != nil {
		return "", err
	}
	sk, err := studio.ParseTree(tree, kittree.MapBlobs(blobs))
	if err != nil {
		return "", fmt.Errorf("invalid studio kit config: %w", err)
	}
	if !nameGiven && sk.LegacyName != "" {
		name = sk.LegacyName
	}
	if name == "" {
		return "", errors.New("--name is required with --config")
	}
	for _, check := range []func() error{func() error { return sk.CheckName(name) }, sk.CheckPrompt, sk.CheckNotes} {
		if err := check(); err != nil {
			return "", err
		}
	}
	return name, nil
}
```

`cmd/at-jam/main.go`, `cmdKit`:
- Change the `config` flag help to `"push: a kit.yml to push alone (or - for stdin); otherwise push <dir>"`.
- Replace the `case "push":` body:

```go
	case "push":
		if *config == "-" && *name == "" {
			fmt.Fprintln(stderr, "at-jam kit push: --name is required with --config")
			return 2
		}
		files, kitName, err := kitPushFiles(pos, *config, *name)
		if errors.Is(err, errKitPushUsage) {
			fmt.Fprintln(stderr, "at-jam kit push:", err)
			return 2
		}
		if err != nil {
			fmt.Fprintln(stderr, "at-jam kit push:", err)
			return 1
		}
		if kitName, err = checkKitPush(files, kitName, *name != ""); err != nil {
			fmt.Fprintln(stderr, "at-jam kit push:", err)
			return 1
		}
		var body bytes.Buffer
		if err := kittree.WriteTar(&body, files); err != nil {
			fmt.Fprintln(stderr, "at-jam kit push:", err)
			return 1
		}
		res, err := c.PushKitTree(kitName, body.Bytes())
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		if res.Unchanged {
			fmt.Fprintf(stdout, "%s unchanged (current v%d)\n", kitName, res.Version)
		} else {
			fmt.Fprintf(stdout, "pushed %s v%d\n", kitName, res.Version)
		}
```

In `case "show":`, after `fmt.Fprint(stdout, string(y))`, add:

```go
		if len(res.Files) > 0 {
			fmt.Fprintf(stdout, "# files (tree %s):\n", shortCommit(res.Tree))
			for _, f := range res.Files {
				fmt.Fprintf(stdout, "#   %s  %8d  %s\n", f.Mode, f.Size, f.Path)
			}
		}
```

The `--config` flag test `TestKitPushRejectsMalformedConfig` (`--name x --config bad`) keeps its expectations: the failure surfaces as "invalid studio kit config". Update `TestKitPushPacksContextDir`: it now expects `base.context-dir is no longer supported; put a Dockerfile next to kit.yml` and a non-zero exit. Rename it `TestKitPushRefusesContextDir`.

`docs/usage/jam/kits.md`:
1. In the schema example, `base:` loses `context-files | context | context-dir` from its comment (`# OPTIONAL — one of: image | kit | omitted; a Dockerfile next to kit.yml is the build context`). The notes example's `file:` comment becomes `# relative to the kit directory, read from it at push; or body: |`.
2. Rewrite the `base` row: `kit:` a parent kit; `image:` a gated prebuilt ref (not with a `Dockerfile`); omitted, the blessed default. The build context is the **kit directory** (next section). Keep the provenance-gate and injected-build-arg sentences. Drop everything about `context-files`, `context` and `context-dir`, and state: "`base.context`, `base.context-files` and `base.context-dir` are refused at push: `base.context is no longer supported; put a Dockerfile next to kit.yml`."
3. Create the leaf `docs/usage/jam/kits-directory.md`. `kits.md` is at its 200-line budget, so the subject gets its own doc. Give it frontmatter in the house schema:
   - `summary: A kit version is a directory — kit.yml plus its files; a Dockerfile beside kit.yml makes it the build context; the limits, .dockerignore, how Jam stores versions as content-addressed trees.`
   - `read_when: You are laying out a kit directory (Dockerfile, scripts, note files), a push or import refused a file (symlink, limit, unsafe name, packed-context field), or you want to know what a version stores.`
   - `owns: the kit directory — files, modes, limits, .dockerignore, build context, tree storage (and, from Task 13, the one-time migration)`
   - `prereqs: kits.md`, `tier: leaf`, `updated: <today>`.

   Add its row to `docs/usage/jam/INDEX.md` right after the `kits.md` row: `| <link `kits-directory.md` as "kits-directory.md"> | You are laying out a kit directory (Dockerfile, scripts, note files), a push or import refused a file (symlink, limit, unsafe name, \`base.context…\` field), or want to know what a kit version stores. |`. In `kits.md`, after the field table, add one line: "A kit version is a **directory**: `kit.yml` plus its files; a `Dockerfile` beside `kit.yml` is the build context — the files, limits and `.dockerignore` are in <link `kits-directory.md` as "kits-directory.md">." The leaf's body has two sections, `## The directory` and `## Limits`:
   - A kit version is a directory: `kit.yml` plus its files.
   - A `Dockerfile` next to `kit.yml` makes the directory, minus `kit.yml`, the build context, streamed to `docker build -`, modes kept, so a script keeps its `+x`.
   - Limits: regular files only, no symlinks; modes `0644`/`0755` (any exec bit → `0755`); ≤ 2000 files, ≤ 64 MiB, ≤ 4 MiB a file; paths with no control character or `.`/`..` segment.
   - `.dockerignore` filters what is stored; `kit.yml`, `Dockerfile` and `.dockerignore` are always kept.
   - Note files are read from the directory.
   - Jam stores each version as a content-addressed tree; identical directories share it.
   - The error texts: the spec §5 list.
4. In "The model", replace "On `push` the YAML is parsed and validated as a studio kit, then stored as canonical JSON …" with: "On `push` the directory is read, filtered by its `.dockerignore`, checked, and its `kit.yml` parsed and validated as a studio kit. The version stores the files, `kit.yml` byte for byte. A push whose files equal *current*'s makes no version."
5. In the verbs block:

```
at-jam kit push ./web                                       # → "pushed web v3" (name: --name, else kit.yml's name:, else the dir's)
at-jam kit push ./web --name web-dev                        # push the directory under another name
at-jam kit push --config ./kit.yml --name web               # push kit.yml alone (a one-file version)
at-jam kit push --config - --name web                       # read kit.yml from stdin
```

   And `kit show` lists the version's files under `# files (tree <12 hex>):`.
6. Note in the admin-UI paragraph that the "push a new version" box edits `kit.yml` and keeps the other files.

Bump `updated:`. Check that `summary`/`read_when` still hold, and add "pushing a kit directory" to `read_when`.

- [ ] **Step 4: Convert the packed-context fixtures, then run everything**

Tests that push a kit carrying `base.context`/`context-files` through `PushStudioKit`, `EnsureStudioKit` or the HTTP push now get the refusal. They test packed-context behaviour that still reads until Task 14, so they seed the kit with the store's unchecked writer instead:

```go
// seedKit stores sk under name the way a pre-tree Jam would have: unchecked,
// as kit.yml text (Store.PushKit). For fixtures of packed-context kits.
func seedKit(t *testing.T, st Store, name string, sk studio.StudioKit) {
	t.Helper()
	text, err := sk.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PushKit(name, string(text)); err != nil {
		t.Fatal(err)
	}
}
```

Put `seedKit` in `internal/jam/kit_push_test.go`. Put `seedKit(t, st jam.Store, …)` equivalents in `internal/jam/adminui/kit_context_test.go` and `cmd/at-jam/configgit_write_test.go`. Run the suite, and convert each failure whose kit carries a packed context:
- `config_export_context_test.go`
- `config_adopt_test.go`
- `adminui/kit_context_test.go`
- `cmd/at-jam/configgit_write_test.go`
- `cmd/at-jam/main_test.go`
- any other failure whose kit carries a packed context, e.g. in `supervisor_test.go`, `kitchain_raise_test.go` or `image_status_test.go`

Run: `GOPROXY=https://proxy.golang.org,direct go test ./... && just lint && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS, and the audit at most 733 errors (the new leaf is linked from `INDEX.md` and has its frontmatter, so it adds none).

- [ ] **Step 5: Commit**

```bash
git add -A internal/jam cmd/at-jam docs/usage/jam/kits.md docs/usage/jam/kits-directory.md docs/usage/jam/INDEX.md
git commit -m "kits: push a kit directory as a tree (tar API, CLI push <dir>, one-file --config)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Checkouts: git import/track/sync, config apply and config write on trees

**Files:**
- Modify:
  - `internal/jam/gitsource/load.go`: `Load` and `LoadDir` return the files.
  - `internal/jam/kitset.go`: entries carry the files and the tree; path refs are rewritten in `kit.yml`; the overlay serves trees; docs carry `Trees`.
  - `internal/jam/kitsource.go`: `PutKitDocs` with the set's objects.
  - `internal/jam/config_tree.go`: `locate`.
  - `internal/jam/config_plan.go`: kit canon, `KitObjects` in the write; delete `ConfigCanonDisplay`.
  - `internal/jam/config_files.go`: `configObject.KitFiles`; delete `Binary`, `inlineContext`, `kitContextDir`, `kitContextPath`.
  - `internal/jam/config_export.go`: `kitCanon`, `renderKitDir`, `treeKitYML`, `ConfigTreeExport.Kits`, `ConfigKitFile`; delete `renderKitFile`, `renderInlineKitFile`, `otherKitForms`, `treeBudget`, `ConfigFileBlob`, `ConfigExportOptions`, `ExportConfigTreeWith`.
  - `internal/jam/config_admin.go`: the export-tree route ignores `contexts`.
  - `internal/jam/adminclient/adminclient.go`: `ConfigExportTree` drops `?contexts=files`.
  - `internal/jam/adminui/config_page.go`.
  - `cmd/at-jam/configgit.go`: `writeConfigTree` mirrors kit dirs; `mirrorKitDir` replaces `pruneTreeDir`; `isKitDir` replaces `isKitContextDir`; the plan diff uses `op.Before`/`op.After`.
  - Docs: `docs/usage/jam/kits-git.md`, `kits-path-parents.md`, `config-git.md`.
- Delete: `internal/jam/config_export_context_test.go`. `config_export_kits_test.go` replaces it.
- Test:
  - `internal/jam/gitsource/load_test.go`: add three tests and adapt the rest to the new signature.
  - `internal/jam/kitset_tree_test.go`.
  - `internal/jam/config_export_kits_test.go`.
  - `internal/jam/config_roundtrip_test.go`: its writer helper now writes kit dirs too.
  - `cmd/at-jam/configgit_write_test.go`: rewritten from `Binary`/`Prune` to `Kits`.

**Interfaces:**
- Consumes: `kittree.ReadDir`, `kittree.Build`, `kittree.MapBlobs`, `kittree.NewIgnorer`, `kittree.CheckPath` (Tasks 1–2); `studio.ParseTree`, `studio.RefuseContextFields`, `studio.SetBaseKit` (Task 4); `KitObjects`, `PutKitDocs(kits, objs)`, `ConfigWrite.KitObjects`, `memState.applyKitObjects` (Task 5); `LoadKitVersion`, `KitReader` (Task 7); `validateKit` (Task 9).
- Produces:
```go
// gitsource
func Load(root, dir string) (studio.StudioKit, []kittree.File, string, error)
func LoadDir(root, dir string) (studio.StudioKit, []kittree.File, string, error)
// jam
type kitSetEntry struct { …; files []kittree.File; tree kittree.Tree; blobs map[string][]byte; parent *KitParentRef } // `text` is gone
func kitSetObjects(entries []kitSetEntry) KitObjects
func kitCanon(t kittree.Tree, blobs kittree.Blobs, tracked *KitSource) []byte
type ConfigKitFile struct { Path string `json:"path"`; Data []byte `json:"data"`; Mode uint32 `json:"mode"` }
type ConfigTreeExport struct {
	Files  map[string]string          `json:"files"`
	Remove []string                   `json:"remove,omitempty"`
	Kits   map[string][]ConfigKitFile `json:"kits,omitempty"` // kits/<n> → its files; empty = mirror the dir to nothing
}
func ExportConfigTree(snap ConfigSnapshot, marks []ConfigMark, serve map[string]any, all bool) (ConfigTreeExport, error)
func exportObjects(m *memState, inTree func(kit string) bool) (map[ConfigKey]configObject, error)
// cmd/at-jam
func mirrorKitDir(root *os.Root, p string, keep map[string]bool, ignore *[]byte) error
func isKitDir(p string) bool
```

- [ ] **Step 1: Write the failing tests**

Add to `internal/jam/gitsource/load_test.go`:

```go
func TestLoadReadsTheKitDirectory(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "app", "notes:\n  - {name: r.md, read-when: x, file: docs/r.md}\n")
	writeFile(t, root, "app/docs/r.md", "# R\n")
	writeFile(t, root, "app/Dockerfile", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n")
	writeFile(t, root, "app/.dockerignore", "*.log\n")
	writeFile(t, root, "app/x.log", "noise")
	if err := os.WriteFile(filepath.Join(root, "app/run.sh"), []byte("echo"), 0o755); err != nil {
		t.Fatal(err)
	}
	sk, files, hint, err := Load(root, "app")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	if strings.Join(got, ",") != ".dockerignore,Dockerfile,docs/r.md,kit.yml,run.sh" || hint != "app" || sk.Notes[0].Body != "# R\n" || sk.BuildContext == "" {
		t.Fatalf("files %v hint %q kit %+v", got, hint, sk)
	}
}

func TestLoadRefusesSymlinkInKitDir(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "kits/app", "")
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "kits/app/run.sh")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Load(root, "kits/app"); err == nil || err.Error() != "kits/app/run.sh: symlinks are not allowed" {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRefusesPackedContextFields(t *testing.T) {
	root := t.TempDir()
	writeKit(t, root, "app", "base:\n  context-dir: ctx\n")
	if _, _, _, err := Load(root, "app"); err == nil || err.Error() != "base.context-dir is no longer supported; put a Dockerfile next to kit.yml" {
		t.Fatalf("err = %v", err)
	}
}
```

Adapt the existing tests in that file:
- `Load` and `LoadDir` have four results.
- The tests about `base.context-dir` packing and escapes become `TestLoadRefusesPackedContextFields`.
- The note-file escape tests now expect `note "…": file must be a relative path inside the kit directory`, or `note "…": file x is not in the kit` for a symlinked note. ReadDir refuses the symlink first: `app/n.md: symlinks are not allowed`.

```go
// internal/jam/kitset_tree_test.go
package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

func TestKitSetStoresTreesWithTheParentRefRewrittenInPlace(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root+"/kits/web/kit.yml", "kind: studio\n", 0o644)
	mustWrite(t, root+"/kits/web/Dockerfile", dockerfile, 0o644)
	mustWrite(t, root+"/kits/app/kit.yml", "kind: studio\n# built on web\nbase:\n  kit: ../web   # sibling\nprompt: hi\n", 0o644)
	st := NewMemStore()
	src := gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "kits/app", Ref: "main"}
	set, err := loadKitSet(root, src, "")
	if err != nil {
		t.Fatal(err)
	}
	kitMu.Lock()
	entries, err := resolveKitSet(st, set, src.Repo)
	kitMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutKitDocs(buildKitDocs(st, entries, src, "c1", time.Unix(100, 0).UTC(), false), kitSetObjects(entries)); err != nil {
		t.Fatal(err)
	}
	app, _ := LoadKitVersion(st, "app", 0)
	if string(app.KitYML) != "kind: studio\n# built on web\nbase:\n  kit: web@v1   # sibling\nprompt: hi\n" {
		t.Fatalf("app kit.yml = %q", app.KitYML)
	}
	web, _ := LoadKitVersion(st, "web", 0)
	if _, ok := web.Tree.Lookup("Dockerfile"); !ok || web.Kit.BuildContext == "" {
		t.Fatalf("web tree = %+v", web.Tree)
	}
	kitMu.Lock()
	again, err := resolveKitSet(st, set, src.Repo)
	kitMu.Unlock()
	if err != nil || !again[0].Unchanged || !again[1].Unchanged {
		t.Fatalf("re-import: %+v %v", again, err)
	}
	if strings.Contains(string(app.KitYML), "../web") {
		t.Fatal("the stored kit.yml still has the path ref")
	}
}
```

The set names the top kit `app` and its parent `web` from their directories.

```go
// internal/jam/config_export_kits_test.go
package jam

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeExport writes an export as a writer would: Files at 0644, each kit dir's files at their
// modes. It replaces the helper of the same name in the deleted config_export_context_test.go.
func writeExport(t *testing.T, dir string, exp ConfigTreeExport) {
	t.Helper()
	for p, body := range exp.Files {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(p)), body, 0o644)
	}
	for d, files := range exp.Kits {
		for _, f := range files {
			mustWrite(t, filepath.Join(dir, filepath.FromSlash(d+"/"+f.Path)), string(f.Data), fs.FileMode(f.Mode))
		}
	}
}

func kitsJam(t *testing.T) *MemStore {
	t.Helper()
	st := NewMemStore()
	if _, _, err := PushKitFiles(st, "web", files("kit.yml", "kind: studio\n# web\n", "Dockerfile", dockerfile, "run.sh*", "echo\n", ".dockerignore", "*.log\n")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PushKitFiles(st, "app", files("kit.yml", "kind: studio\nbase: {kit: web@v1}\n")); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestExportWritesKitDirectories(t *testing.T) {
	exp, err := ExportConfigTree(kitsJam(t).ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	web := exp.Kits["kits/web"]
	var got []string
	for _, f := range web {
		got = append(got, f.Path+":"+fs.FileMode(f.Mode).String())
	}
	if strings.Join(got, " ") != ".dockerignore:-rw-r--r-- Dockerfile:-rw-r--r-- kit.yml:-rw-r--r-- run.sh:-rwxr-xr-x" {
		t.Fatalf("kits/web = %v", got)
	}
	app := exp.Kits["kits/app"]
	if len(app) != 1 || string(app[0].Data) != "kind: studio\nbase: {kit: ../web}\n" {
		t.Fatalf("kits/app = %+v", app)
	}
	if _, ok := exp.Files["kits/app/kit.yml"]; ok {
		t.Fatal("a kit dir's files belong in Kits, not Files")
	}
}

func TestExportMirrorsAGoneManagedKitToNothing(t *testing.T) {
	st := kitsJam(t)
	marks := []ConfigMark{{Kind: ConfigKindKit, Name: "old", Commit: "c"}}
	exp, err := ExportConfigTree(st.ExportConfig(), marks, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if files, ok := exp.Kits["kits/old"]; !ok || len(files) != 0 {
		t.Fatalf("kits/old = %v, %v", files, ok)
	}
}

// TestConfigTreeRoundTripUnchanged: write → (commit, clone: a copy on disk)
// → apply adopts every kit with no new version — modes, a .dockerignored
// file on disk and an in-tree parent included — and a second apply is a no-op.
func TestConfigTreeRoundTripUnchanged(t *testing.T) {
	st := kitsJam(t)
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeExport(t, dir, exp)
	mustWrite(t, filepath.Join(dir, "kits/web/debug.log"), "local noise", 0o644)
	if fi, err := os.Stat(filepath.Join(dir, "kits/web/run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh mode on disk: %v %v", fi, err)
	}
	tr, err := LoadConfigTree(dir)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("%v %v", err, tr.Refusals)
	}
	res, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"web", "app"} {
		if op := opOf(res.Plan, ConfigKindKit, n); op.Action != ConfigAdopt {
			t.Fatalf("%s: %+v (the repo copy must equal Jam's tree)", n, op)
		}
		if k, _ := st.GetKit(n); k.Current != 1 {
			t.Fatalf("%s got a new version: v%d", n, k.Current)
		}
	}
	again, err := ApplyConfigTree(context.Background(), st, nil, tr, allCreds, dirOpts(), nil, "local")
	if err != nil || again.Plan.Counts()[ConfigUnchanged] != len(again.Plan.Ops) {
		t.Fatalf("second apply is not a no-op: %v %+v", err, again.Plan.Ops)
	}
}

func TestKitCanonIsTheTreeListing(t *testing.T) {
	st := kitsJam(t)
	v, _ := LoadKitVersion(st, "web", 0)
	got := string(kitCanon(v.Tree, st.KitBlob, &KitSource{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main", Commit: "c"}))
	if !strings.HasPrefix(got, "kind: studio\n# web\n# files:\n#   0644 6 ") || !strings.Contains(got, " run.sh\n") ||
		!strings.HasSuffix(got, "# source: https://github.com/acme/kits//web@main\n") {
		t.Fatalf("canon =\n%s", got)
	}
}
```

In `config_roundtrip_test.go`, replace the `for p, body := range exp.Files { mustWrite(…) }` loop with `writeExport(t, dir, exp)`. Its replica comparison then also compares `got.Kits` with `exp.Kits` (`reflect.DeepEqual`).

In `cmd/at-jam/configgit_write_test.go`, rewrite the `Binary`/`Prune` cases as `Kits` cases and add:

```go
func TestWriteConfigTreeMirrorsKitDirs(t *testing.T) {
	dir := t.TempDir()
	for p, body := range map[string]string{"kits/app/stale.txt": "old", "kits/app/debug.log": "ignored", "kits/gone/kit.yml": "kind: studio\n", "kits/gone/x": "x"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "kits/app/leak")); err != nil {
		t.Fatal(err)
	}
	exp := jam.ConfigTreeExport{Files: map[string]string{}, Kits: map[string][]jam.ConfigKitFile{
		"kits/app": {{Path: "kit.yml", Data: []byte("kind: studio\n"), Mode: 0o644}, {Path: "run.sh", Data: []byte("echo"), Mode: 0o755}, {Path: ".dockerignore", Data: []byte("*.log\n"), Mode: 0o644}},
		"kits/gone": {},
	}}
	if err := writeConfigTree(dir, exp); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{"kits/app/kit.yml": true, "kits/app/run.sh": true, "kits/app/debug.log": true, "kits/app/stale.txt": false, "kits/app/leak": false, "kits/gone": false} {
		if _, err := os.Lstat(filepath.Join(dir, p)); (err == nil) != want {
			t.Fatalf("%s present=%v, want %v", p, err == nil, want)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "kits/app/run.sh")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh mode %v", fi.Mode())
	}
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Fatal("the symlink target must never be touched")
	}
	for _, bad := range []jam.ConfigTreeExport{
		{Kits: map[string][]jam.ConfigKitFile{"kits/../x": {}}},
		{Kits: map[string][]jam.ConfigKitFile{"kits/a": {{Path: "../x", Mode: 0o644}}}},
		{Kits: map[string][]jam.ConfigKitFile{"kits/a": {{Path: "x", Mode: 0o777}}}},
	} {
		if err := writeConfigTree(dir, bad); err == nil {
			t.Fatalf("writeConfigTree(%+v) must refuse", bad.Kits)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... ./cmd/at-jam/`
Expected: FAIL to compile (`Load` result count, `exp.Kits`, `kitSetObjects`, `kitCanon` signature).

- [ ] **Step 3: Implement the checkout reader**

`internal/jam/gitsource/load.go`:

```go
// Load reads the kit directory dir (slash, relative to root; "." for the
// root) of the checkout at root into a pushable kit: its files (kittree.ReadDir:
// .dockerignore applied; symlinks, irregular files and unsafe names refused),
// no packed-context field, kit.yml parsed with its note files read from the
// directory, and the push-time authoring checks. The name hint is the kit's
// name: field, else the base of dir, else "".
func Load(root, dir string) (studio.StudioKit, []kittree.File, string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return studio.StudioKit{}, nil, "", errors.New("kit root cannot be opened")
	}
	defer r.Close()
	files, err := kittree.ReadDir(r, dir)
	if err != nil {
		return studio.StudioKit{}, nil, "", err
	}
	tree, blobs, err := kittree.Build(files)
	if err != nil {
		return studio.StudioKit{}, nil, "", fmt.Errorf("%s: %w", path.Clean(dir), err)
	}
	e, _ := tree.Lookup(KitFile)
	if err := studio.RefuseContextFields(blobs[e.SHA256]); err != nil {
		return studio.StudioKit{}, nil, "", err
	}
	sk, err := studio.ParseTree(tree, kittree.MapBlobs(blobs))
	if err != nil {
		return studio.StudioKit{}, nil, "", err
	}
	if err := sk.CheckPrompt(); err != nil {
		return studio.StudioKit{}, nil, "", err
	}
	if err := sk.CheckNotes(); err != nil {
		return studio.StudioKit{}, nil, "", err
	}
	return sk, files, nameHint(sk, dir), nil
}
```

`LoadDir` keeps its checks and returns `Load(root, dir)`'s four results.

`internal/jam/kitset.go`:
- `kitSetEntry` drops `text` and gains `files []kittree.File // as loaded; kit.yml rewritten when its path ref is`, `tree kittree.Tree` and `blobs map[string][]byte`.
- `loadKitSet` takes `sk, files, hint, err := gitsource.Load(…)` and `psk, pfiles, phint, err := gitsource.LoadDir(…)`, and sets `files:` on each entry.

Then:

```go
// kitYMLOf is files' kit.yml content.
func kitYMLOf(files []kittree.File) []byte {
	for _, f := range files {
		if f.Path == kittree.KitFile {
			return f.Data
		}
	}
	return nil
}

// withKitYML is files with kit.yml's content replaced (a copy; files is not modified).
func withKitYML(files []kittree.File, yml []byte) []kittree.File {
	out := slices.Clone(files)
	for i := range out {
		if out[i].Path == kittree.KitFile {
			out[i].Data = yml
		}
	}
	return out
}

func resolveKitEntries(store Store, set []kitSetEntry, check func(i int, e kitSetEntry) error) ([]kitSetEntry, error) {
	out := slices.Clone(set)
	ov := &overlayStore{Store: store, pending: map[string]kitSetEntry{}}
	for i := range out {
		e := &out[i]
		if p, isPath := e.Kit.Base.ParentPath(); isPath {
			want := path.Join(checkoutDir(e.Dir), p)
			j := slices.IndexFunc(out[:i], func(o kitSetEntry) bool { return o.Group == e.Group && checkoutDir(o.Dir) == want })
			if j < 0 {
				return nil, writeErr(http.StatusBadRequest, "parent path %s (of %s) is not in this set", p, e.Name)
			}
			ref := fmt.Sprintf("%s@v%d", out[j].Name, out[j].Version)
			yml, err := studio.SetBaseKit(kitYMLOf(e.files), ref)
			if err != nil {
				return nil, writeErr(http.StatusBadRequest, "%s/kit.yml: %s", checkoutDir(e.Dir), err)
			}
			e.Kit.Base.Kit, e.files = ref, withKitYML(e.files, yml)
		}
		if check != nil {
			if err := check(i, *e); err != nil {
				return nil, err
			}
		}
		chain, err := validateKit(ov, e.Name, e.Kit)
		if err != nil {
			return nil, err
		}
		tree, blobs, err := kittree.Build(e.files)
		if err != nil {
			return nil, writeErr(http.StatusBadRequest, "kit %s: %s", e.Name, err)
		}
		e.tree, e.blobs, e.parent = tree, blobs, chainParent(chain)
		k, _ := store.GetKit(e.Name)
		if k.Current != 0 && k.Trees[k.Current] == tree.Hash() {
			e.Version, e.Unchanged = k.Current, true
		} else {
			e.Version, e.Unchanged = nextKitVersion(k), false
		}
		ov.pending[e.Name] = *e
	}
	return out, nil
}

// kitSetObjects are the trees and blobs of the entries that get a new version.
func kitSetObjects(entries []kitSetEntry) KitObjects {
	objs := KitObjects{Blobs: map[string][]byte{}}
	for _, e := range entries {
		if e.Unchanged {
			continue
		}
		objs.Trees = append(objs.Trees, e.tree)
		maps.Copy(objs.Blobs, e.blobs)
	}
	return objs
}
```

`checkoutDir("kits/app")` is `"kits/app"`. The overlay serves the pending trees:

```go
func (o *overlayStore) GetKit(name string) (Kit, bool) {
	k, ok := o.Store.GetKit(name)
	e, has := o.pending[name]
	if !has {
		return k, ok
	}
	k = copyKit(k)
	k.Name, k.Current = name, e.Version
	if k.Trees == nil {
		k.Trees = map[int]string{}
	}
	k.Trees[e.Version] = e.tree.Hash()
	return k, true
}

func (o *overlayStore) KitTree(sha string) (kittree.Tree, bool) {
	for _, e := range o.pending {
		if e.tree.Hash() == sha {
			return e.tree, true
		}
	}
	return o.Store.KitTree(sha)
}

func (o *overlayStore) KitBlob(sha string) ([]byte, bool) {
	for _, e := range o.pending {
		if b, ok := e.blobs[sha]; ok {
			return b, true
		}
	}
	return o.Store.KitBlob(sha)
}
```

Delete `overlayStore.KitConfig`. In `addKitVersion`, replace `doc.Versions[e.Version] = e.text` with `if doc.Trees == nil { doc.Trees = map[int]string{} }; doc.Trees[e.Version] = e.tree.Hash()`. In `checkSetAncestor`, iterate `k.VersionNumbers()` instead of `k.Versions`. In `kitsource.go` `importKit`, call `store.PutKitDocs(docs, kitSetObjects(entries))`.

`internal/jam/config_tree.go` `loadKits`: replace `l.refuse("%s/kit.yml: %s", rel, err)` with `l.refuse("%s", locate(rel, err))`:

```go
// locate is err as a refusal of the kit at rel (kits/<n>): kept when it
// already names a path under rel (a file the read refused), else located at
// rel/kit.yml.
func locate(rel string, err error) string {
	if msg := err.Error(); strings.HasPrefix(msg, rel+"/") || strings.HasPrefix(msg, rel+":") {
		return msg
	}
	return rel + "/kit.yml: " + err.Error()
}
```

- [ ] **Step 4: Implement the config side**

`internal/jam/config_export.go`:

```go
// kitCanon is a kit's canonical form for plans and marks: its kit.yml, then
// "# files:" and one line per entry of its tree (mode, size, sha256, path),
// then, for a kit that tracks a source, that source (not its commit: a sync
// that only moves the commit is not a change). The listing identifies the
// tree exactly as its hash does, and a plan's line diff of two is readable.
func kitCanon(t kittree.Tree, blobs kittree.Blobs, tracked *KitSource) []byte {
	var b bytes.Buffer
	if e, ok := t.Lookup(kittree.KitFile); ok {
		yml, _ := blobs(e.SHA256)
		b.Write(yml)
		if len(yml) > 0 && yml[len(yml)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	b.WriteString("# files:\n")
	for _, e := range t.Entries {
		fmt.Fprintf(&b, "#   %04o %d %s %s\n", e.Mode, e.Size, e.SHA256, e.Path)
	}
	if tracked != nil {
		src := gitsource.Source{Repo: tracked.Repo, Dir: tracked.Dir, Ref: tracked.Ref}
		b.WriteString("# source: " + src.String() + "\n")
	}
	return b.Bytes()
}

// ConfigKitFile is one file of a kit directory a config write writes.
type ConfigKitFile struct {
	Path string `json:"path"` // relative to the kit directory
	Data []byte `json:"data"` // base64 in JSON
	Mode uint32 `json:"mode"` // 0644 or 0755
}

// ConfigTreeExport is a rendered config tree.
type ConfigTreeExport struct {
	Files  map[string]string `json:"files"`            // repo path → content
	Remove []string          `json:"remove,omitempty"` // managed paths Jam no longer has
	// Kits holds each kit directory the write owns, by its repo dir
	// (kits/<n>): its files at their modes, kit.yml included. A writer
	// mirrors the dir: it writes the files and removes every other file there
	// that the dir's .dockerignore (the exported one, else the dir's own)
	// does not exclude. An empty list mirrors the dir to nothing: a kit now
	// written by reference, or a managed kit gone from Jam.
	Kits map[string][]ConfigKitFile `json:"kits,omitempty"`
}

// kitDir is the repo dir of kit name.
func kitDir(name string) string { return "kits/" + name }
```

In `exportObjects(m *memState, inTree func(kit string) bool)` (the `contextFiles` parameter is gone), the kit loop becomes:

```go
	for _, n := range slices.Sorted(maps.Keys(m.kits)) {
		if inTree != nil && !inTree(n) {
			continue
		}
		k := m.kits[n]
		v, ok := LoadKitVersion(m, n, 0)
		if !ok {
			continue
		}
		key := ConfigKey{ConfigKindKit, n}
		at = key
		if v.TreeHash == "" || v.Tree.Entries == nil {
			return nil, fmt.Errorf("kit %s: v%d is not stored as a tree", n, v.Version)
		}
		blobs := kittree.MapBlobs(m.kitBlobs)
		if k.Source != nil { // a kit following its own source is written by reference
			out[key] = configObject{Key: key, Files: map[string][]byte{sourceKitPath(n): renderKitSourceFile(*k.Source)}, Canon: kitCanon(v.Tree, blobs, k.Source)}
			continue
		}
		files, err := renderKitDir(m, n, v, inTree)
		if errors.Is(err, ErrConfigKitPinBehind) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("kit %s: %w", n, err)
		}
		out[key] = configObject{Key: key, KitFiles: files, Canon: kitCanon(v.Tree, blobs, nil)}
	}
```

`m` is a private copy, so `LoadKitVersion` reads it through its `RLock`ed methods.

```go
// renderKitDir is kit name's current version v as its kits/<n>/ files: every
// file as stored, kit.yml through treeKitYML.
func renderKitDir(m *memState, name string, v KitVersion, inTree func(string) bool) ([]ConfigKitFile, error) {
	out := make([]ConfigKitFile, 0, len(v.Tree.Entries))
	for _, e := range v.Tree.Entries {
		data, ok := m.kitBlobs[e.SHA256]
		if !ok {
			return nil, fmt.Errorf("%s has no stored content", e.Path)
		}
		if e.Path == kittree.KitFile {
			var err error
			if data, err = treeKitYML(m, name, data, inTree); err != nil {
				return nil, err
			}
		}
		out = append(out, ConfigKitFile{Path: e.Path, Data: bytes.Clone(data), Mode: uint32(e.Mode)})
	}
	return out, nil
}

// treeKitYML is kit.yml as a config tree holds it. A name@vN parent in the
// tree becomes the path parent ../<name>, which apply re-pins to the
// parent's current version, so only a pin equal to it can be written: an
// older pin is ErrConfigKitPinBehind, a pin of a parent written by reference
// too. Only the base.kit value changes (studio.SetBaseKit). A kit.yml that
// does not parse is written as it is.
func treeKitYML(m *memState, name string, yml []byte, inTree func(string) bool) ([]byte, error) {
	sk, err := studio.ParseStudioKit(yml)
	if err != nil {
		return yml, nil
	}
	parent, v, err := sk.Base.ParentRef()
	if err != nil || v == 0 || inTree == nil || !inTree(parent) {
		return yml, nil
	}
	if m.kits[parent].Source != nil {
		return nil, &kitPinSourceError{kit: name, parent: parent, pin: v}
	}
	if cur := m.kits[parent].Current; v != cur {
		return nil, &kitPinBehindError{kit: name, parent: parent, pin: v, cur: cur}
	}
	return studio.SetBaseKit(yml, "../"+parent)
}

// ExportConfigTree renders the managed objects (every live one with all) and,
// when serve is non-nil, jam.yml.
func ExportConfigTree(snap ConfigSnapshot, marks []ConfigMark, serve map[string]any, all bool) (ConfigTreeExport, error) {
	m := stateFromSnapshot(snap, nil)
	managed := map[ConfigKey]bool{}
	for _, mk := range marks {
		managed[mk.Key()] = true
	}
	want := func(k ConfigKey) bool { return all || managed[k] }
	inTree := func(kit string) bool { return want(ConfigKey{ConfigKindKit, kit}) && m.kitExists(kit) }
	objs, err := exportObjects(m, inTree)
	if err != nil {
		return ConfigTreeExport{}, err
	}
	exp := ConfigTreeExport{Files: map[string]string{}, Kits: map[string][]ConfigKitFile{}}
	remove := map[string]bool{}
	for k, o := range objs {
		if !want(k) {
			continue
		}
		for p, b := range o.Files {
			exp.Files[p] = string(b)
		}
		if k.Kind == ConfigKindKit {
			if o.KitFiles != nil {
				exp.Kits[kitDir(k.Name)] = o.KitFiles
				remove[sourceKitPath(k.Name)] = true
			} else {
				exp.Kits[kitDir(k.Name)] = []ConfigKitFile{} // written by reference: its directory goes
			}
		}
	}
	if serve != nil {
		if exp.Files["jam.yml"], err = renderServeYAML(serve); err != nil {
			return ConfigTreeExport{}, err
		}
	}
	for name, p := range m.projects {
		if want(ConfigKey{ConfigKindProject, name}) && p.Context.Empty() {
			remove["projects/"+name+"/context.md"] = true
		}
	}
	for k := range managed {
		if _, ok := objs[k]; ok {
			continue
		}
		switch k.Kind {
		case ConfigKindKit:
			remove[sourceKitPath(k.Name)] = true
			exp.Kits[kitDir(k.Name)] = []ConfigKitFile{}
		case ConfigKindProject:
			remove[k.Path()] = true
			remove["projects/"+k.Name+"/context.md"] = true
		default:
			remove[k.Path()] = true
		}
	}
	for p := range remove {
		if _, ok := exp.Files[p]; !ok {
			exp.Remove = append(exp.Remove, p)
		}
	}
	slices.Sort(exp.Remove)
	if len(exp.Kits) == 0 {
		exp.Kits = nil
	}
	return exp, nil
}
```

Remove the dead pieces:
- From `config_export.go`: `renderKitFile`, `renderInlineKitFile`, `otherKitForms`, `treeBudget`, `newTreeBudget`, `configTreeHeadroom*`, `ConfigFileBlob`, `ConfigExportOptions`, `ExportConfigTreeWith`, and the old `kitCanon(config string, …)`.
- From `config_files.go`: `configObject.Binary`, `inlineContext`, `kitContextDir`, `kitContextPath`. Add `KitFiles []ConfigKitFile // a kit written as a directory` to `configObject`.
- `PlanConfig` calls `exportObjects(m, nil)`. So does `ConfigMarkStates` in `config_drift.go`; update that caller too.
- `config_admin.go`'s export-tree route calls `ExportConfigTree(store.ExportConfig(), store.ListConfigMarks(), env.Serve, q.Get("all") == "1")`, and its route comment drops `&contexts=files`.

`internal/jam/config_plan.go` `planKits`:
- `repo := configObject{Key: key, Canon: kitCanon(e.tree, kittree.MapBlobs(e.blobs), tracked)}`.
- After the loop, before `for _, d := range docs`: `objs := kitSetObjects(entries); p.m.applyKitObjects(objs); p.w.KitObjects = objs`.
- Delete `ConfigCanonDisplay`. Its two callers use `op.Before`/`op.After` directly: `cmd/at-jam/configgit.go` `printConfigPlan` and `internal/jam/adminui/config_page.go`.

`adminclient.ConfigExportTree` uses `path := "/admin/config/export-tree"`, plus `"?all=1"` when `all`. Update its doc comment.

`cmd/at-jam/configgit.go`:

```go
// writeConfigTree writes exp.Files, then each exp.Kits dir's files at their
// modes, removes exp.Remove, and mirrors each kit dir (mirrorKitDir). Every
// path is checked before anything is written.
func writeConfigTree(dir string, exp jam.ConfigTreeExport) error {
	for p := range exp.Files {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("refusing to write %q outside %s", p, dir)
		}
	}
	for d, files := range exp.Kits {
		if !isKitDir(d) {
			return fmt.Errorf("refusing to write kit dir %q: not kits/<name>", d)
		}
		for _, f := range files {
			if kittree.CheckPath(f.Path) != nil {
				return fmt.Errorf("refusing to write an unsafe path under %s", d)
			}
			if f.Mode != 0o644 && f.Mode != 0o755 {
				return fmt.Errorf("refusing to write %s/%s with mode %o", d, f.Path, f.Mode)
			}
			if _, dup := exp.Files[d+"/"+f.Path]; dup {
				return fmt.Errorf("refusing to write %q twice", d+"/"+f.Path)
			}
		}
	}
	for _, p := range exp.Remove {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("refusing to remove %q outside %s", p, dir)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for p, content := range exp.Files {
		if err := writeTreeFile(root, p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	for d, files := range exp.Kits {
		for _, f := range files {
			if err := writeTreeFile(root, d+"/"+f.Path, f.Data, os.FileMode(f.Mode)); err != nil {
				return err
			}
		}
	}
	for _, p := range exp.Remove {
		if err := root.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, d := range slices.Sorted(maps.Keys(exp.Kits)) {
		files := exp.Kits[d]
		keep := map[string]bool{}
		var ignore *[]byte // nil: nothing exported here, read the dir's own .dockerignore
		if len(files) > 0 {
			empty := []byte{}
			ignore = &empty // exported without a .dockerignore: nothing on disk is excluded
		}
		for _, f := range files {
			keep[d+"/"+f.Path] = true
			if f.Path == ".dockerignore" {
				data := f.Data
				ignore = &data
			}
		}
		if err := mirrorKitDir(root, d, keep, ignore); err != nil {
			return err
		}
	}
	return nil
}

// isKitDir reports whether p is kits/<name>, name a tag-safe kit name.
func isKitDir(p string) bool {
	segs := strings.Split(p, "/")
	return len(segs) == 2 && segs[0] == "kits" && studio.TagSafeName(segs[1])
}
```

`mirrorKitDir` is the old `pruneTreeDir` with these changes. Rename it and keep its doc, reworded for a kit dir:
- The directory check loop is `for _, d := range []string{"kits", p}`.
- The matcher is `ig, err := kittree.NewIgnorer(rules)`. In the walk, `ignored, merr := ig.Ignored(strings.TrimPrefix(q, p+"/"))`, and the prune test is `ignored && d.IsDir() && ig.Prunes()`.
- Drop the trailing `_ = root.Remove(path.Dir(p))`. The walk's `dirs` already includes `p` itself, so an emptied kit dir goes and `kits/` stays.

`kit.yml`, `Dockerfile` and `.dockerignore` are never "ignored" (`Ignorer.Ignored`), so a mirror removes them unless kept. That is how a kit now written by reference loses its `kits/<n>/kit.yml`. Delete `isKitContextDir`. In `config write`'s output, drop the `exp.Inline` loop and count `len(exp.Files)` plus every kit dir's file count. `printConfigPlan`'s diff uses `linediff.Diff(op.Before, op.After)`.

- [ ] **Step 5: Update the docs**

- `docs/usage/jam/kits-git.md`:
  - The paragraph "paths it names — `base.context-dir` and each note's `file` — are relative to `<dir>` … `context-dir` is packed into `context`" becomes: "Jam reads the **whole directory** `<dir>` into the version, filtered by its `.dockerignore` (<link `kits-directory.md` as "kits-directory.md">): a symlink anywhere in it, or an irregular file, is refused (`<dir>/<path>: symlinks are not allowed`), and note files are read from it."
  - In the limits paragraph, "the packed context then meets the usual `context` caps" becomes "the directory then meets the <link `kits-directory.md#limits` as "kit directory limits">".
  - In the errors table, replace the `base.context-dir "…"` row with `<dir>/<path>: symlinks are not allowed` → "a symlink in the kit directory: commit the file itself", and add `base.context… is no longer supported; put a Dockerfile next to kit.yml`.
- `docs/usage/jam/kits-path-parents.md`:
  - "**Pinning:** each path reference is **stored** as `<name>@v<N>`" gains "rewritten in place in the stored `kit.yml`: only that value changes, comments and layout stay. `base.kit` must be a one-line plain or quoted value."
  - "**Unchanged:** … when its own stored config is identical" becomes "when its directory (after the rewrite) is identical".
- `docs/usage/jam/config-git.md`:
  - In the layout block, replace the `kits/<n>/context/…` line with `kits/<n>/…                       the rest of its directory (Dockerfile, scripts…; modes 0644/0755)`.
  - Rewrite the context paragraph (the "A packed `base.context` is written as files in `kits/<n>/context/` …" text through "… and back."): "A kit is written as its directory: every file of its current version at its mode, with a parent that is also in the tree written `../<parent>` in `kit.yml` (apply re-pins it). A write mirrors `kits/<n>/`: it removes every other file there that the directory's `.dockerignore` does not exclude. A kit written as `kits/<n>.source.yml` loses its `kits/<n>/`, and back. A kit too big for the config upload limits is still written; apply it from a git source."
  - Drop the sentence about `context/` being left alone, and keep the umask sentence.
  - Replace the `export-tree` API row with: "Render the tree: `files`, `remove`, and `kits` (each `kits/<n>` dir's files: base64 `data`, `mode`; an empty list removes the dir). A kit pinned behind its in-tree parent is 409."
  - Add one sentence where plans are described: "A kit's plan entry compares its `kit.yml` and its file list (mode, size, sha256, path)."

Bump `updated:` on each.

- [ ] **Step 6: Run everything**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./... && just lint && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS, and the audit at most 733 errors. Fix the remaining fallout:
- Plan tests that compared a kit op's `Before`/`After` with canonical JSON now compare the listing; use `kitCanon` in the expectation.
- `adminclient/config_test.go` expects the path without `contexts=files`.
- `kitset_test.go` and `kitsource_test.go` read `e.text` or `k.Versions[…]`; read `LoadKitVersion(…).KitYML` instead.

- [ ] **Step 7: Commit**

```bash
git add -A internal/jam cmd/at-jam docs/usage/jam
git rm internal/jam/config_export_context_test.go
git commit -m "kits: git import, config apply and config write move kit directories as trees

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: The kit page lists and diffs a version's files; inspect shows each link's tree

**Files:**
- Create: `internal/jam/adminui/kit_files.go`
- Modify:
  - `internal/jam/adminui/kit_context.go`: `ctxRead.files` becomes `[]ctxEntry`; `readContext` converts.
  - `internal/jam/adminui/kit_detail.go`: `viewKit` takes a `jam.KitVersion`; `baseLabel` takes the kit; `buildKitDetail`, `kitListRows` and `chainOf` read through `LoadKitVersion`.
  - `internal/jam/adminui/attention.go`, `internal/jam/adminui/search.go`: `LoadKitVersion`.
  - `internal/jam/adminui/templates/kit.html`: the file cards' headings.
  - `internal/jam/kitchain.go`: `KitChainView` takes the parsed kit.
  - `internal/jam/admin.go`: `GET /admin/kits/{name}` passes `v.Kit`.
  - `internal/jam/setup_image_bodies.go`: `kitDetailBody` gains a `FILES` column.
  - Goldens: `internal/jam/testdata/setup/{role,studio}.golden`.
  - Docs: `docs/usage/jam/ui-pages.md`, `docs/usage/jam/kits.md` (the UI paragraph), `docs/usage/jam/inspect.md`.
- Test: `internal/jam/adminui/kit_files_test.go`, `internal/jam/setup_image_bodies_test.go` (`TestKitDetailBody`)

**Interfaces:**
- Consumes: `jam.LoadKitVersion`, `jam.KitVersion` (Task 7); `jam.PushKitFiles` (Task 9, fixtures); `kittree.IsText`, `kittree.MaxDiffFile` (Task 3).
- Produces:
```go
// package jam
func KitChainView(store Store, name string, sk studio.StudioKit) (links []KitChainLink, effective string, err error)
// package adminui (unexported)
type ctxEntry struct { Path string; Mode fs.FileMode; Size int64; Sum string; Data []byte }
func treeRead(v jam.KitVersion, want string, unpack bool) *ctxRead
func viewKit(n int, v jam.KitVersion, unpack bool, want string) kitView
func baseLabel(sk studio.StudioKit) string
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/jam/adminui/kit_files_test.go
package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

func file(p, body string, mode int) kittree.File {
	return kittree.File{Path: p, Mode: fsMode(mode), Data: []byte(body)}
}

// seedTreeKit pushes kit "t": v1 and v2 differ in their files and prompt.
func seedTreeKit(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	df := file("Dockerfile", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n", 0o644)
	for _, fs := range [][]kittree.File{
		{file("kit.yml", "kind: studio\nprompt: one\n", 0o644), df, file("keep.txt", "same\n", 0o644), file("mod.txt", "a\n", 0o644), file("gone.txt", "bye\n", 0o644), file("run.sh", "echo\n", 0o644), file("blob.bin", "\x00\x01", 0o644), file("big.txt", bigText, 0o644)},
		{file("kit.yml", "kind: studio\nprompt: two\n", 0o644), df, file("keep.txt", "same\n", 0o644), file("mod.txt", "a\nbb\n", 0o644), file("new.txt", "hello\n", 0o644), file("run.sh", "echo\n", 0o755), file("blob.bin", "\x00\x02", 0o644), file("big.txt", bigText+"more\n", 0o644)},
	} {
		if _, _, err := jam.PushKitFiles(store, "t", fs); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestKitPageListsFiles(t *testing.T) {
	body := get(t, kitsHandler(seedTreeKit(t)), "/app/admin/kits/t?v=2").Body.String()
	files := section(body, "ctx-files")
	for _, want := range []string{"<h2>Files</h2>", `file=run.sh"`, "0755", `file=kit.yml"`, `file=Dockerfile"`} {
		if !strings.Contains(files, want) {
			t.Errorf("files card missing %q:\n%s", want, files)
		}
	}
	yaml := section(body, "kit-yaml")
	if !strings.Contains(body, "prompt: two") || strings.Contains(yaml, "H4sI") {
		t.Errorf("kit.yml card:\n%s", yaml)
	}
	if !strings.Contains(body, "Dockerfile") || !strings.Contains(body, "Base") {
		t.Errorf("base label missing")
	}
}

func TestKitPageDiffsFiles(t *testing.T) {
	h := kitsHandler(seedTreeKit(t))
	body := get(t, h, "/app/admin/kits/t?v=2&diff=1").Body.String()
	if d := section(body, "kit-diff"); !strings.Contains(d, `<span class="d-del">- prompt: one</span>`) || !strings.Contains(d, `<span class="d-add">+ prompt: two</span>`) {
		t.Errorf("kit.yml diff:\n%s", d)
	}
	tbl := section(body, "ctx-diff")
	for _, want := range []string{
		"<h2>Files v1 → v2</h2>",
		`data-change="added"`, `file=new.txt"`,
		`data-change="removed"`, `file=gone.txt"`,
		`data-change="modified"`, `file=mod.txt"`, "2 B → 5 B",
		`data-change="mode"`, `file=run.sh"`, "0644 → 0755",
		"2 unchanged",
	} {
		if !strings.Contains(tbl, want) {
			t.Errorf("files diff missing %q:\n%s", want, tbl)
		}
	}
	f := section(get(t, h, "/app/admin/kits/t?v=2&diff=1&file=mod.txt").Body.String(), "ctx-file")
	if !strings.Contains(f, `<span class="d-add">+ bb</span>`) {
		t.Errorf("file diff:\n%s", f)
	}
	f = section(get(t, h, "/app/admin/kits/t?v=2&diff=1&file=blob.bin").Body.String(), "ctx-file")
	if !strings.Contains(f, "binary") {
		t.Errorf("binary file:\n%s", f)
	}
}
```

`2 unchanged` counts `Dockerfile` and `keep.txt`; `kit.yml` changed. Add `func fsMode(m int) fs.FileMode { return fs.FileMode(m) }` with the `io/fs` import.

Add to `internal/jam/setup_image_bodies_test.go`, in or beside `TestKitDetailBody`:

```go
func TestKitDetailBodyShowsTrees(t *testing.T) {
	st := NewMemStore()
	tr := pushTree(t, st, "app", "kit.yml", "kind: studio\n", "Dockerfile", dockerfile)
	body := kitDetailBody(st, []KitRef{{ID: "app", Version: 1}}, nil)
	if !strings.Contains(body, "FILES") || !strings.Contains(body, "tree "+tr.Hash()[:12]+", 2 files") {
		t.Fatalf("body:\n%s", body)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ ./internal/jam/ -run 'KitPage|KitDetailBodyShowsTrees'`
Expected: FAIL. The page has no `ctx-files` card for a tree version, and the body has no `FILES` column.

- [ ] **Step 3: Implement**

```go
// internal/jam/adminui/kit_files.go
package adminui

import (
	"io"
	"io/fs"

	"github.com/aethons-tools/cove/internal/jam"
)

// ctxEntry is one file of a version as the kit page lists it: from a tree,
// or from a pre-tree packed context.
type ctxEntry struct {
	Path string
	Mode fs.FileMode
	Size int64
	Sum  string // hex sha256 of the content
	Data []byte // the first maxFileShow+1 bytes of the wanted file; nil for every other
}

// treeRead lists tree version v's files, kit.yml included. With unpack, the
// file named want has its first maxFileShow+1 bytes read through v.Files.
func treeRead(v jam.KitVersion, want string, unpack bool) *ctxRead {
	c := &ctxRead{tree: true, size: sizeLabel(int(v.Tree.Size())), read: true, byPath: map[string]int{}}
	for i, e := range v.Tree.Entries {
		ent := ctxEntry{Path: e.Path, Mode: e.Mode, Size: e.Size, Sum: e.SHA256}
		if unpack && e.Path == want && v.Files != nil {
			if f, err := v.Files.Open(e.Path); err == nil {
				ent.Data, _ = io.ReadAll(io.LimitReader(f, maxFileShow+1))
				f.Close()
			}
		}
		c.files = append(c.files, ent)
		c.byPath[e.Path] = i
	}
	return c
}
```

In `kit_context.go`:
- `ctxRead` gains `tree bool // the files are the version's tree (not a packed base.context)` and its `files` becomes `[]ctxEntry`.
- `readContext` converts each `studio.ContextEntry` to `ctxEntry{Path, Mode, Size, Sum: hex.EncodeToString(e.Sum[:]), Data}`.
- `get` returns a `ctxEntry`. `modeText`, `sideSize` and `fileView` take a `ctxEntry`. `ctxDiff` already compares `a.Sum == b.Sum` and `a.Mode == b.Mode`, which works with the string sum.
- `ctxListView` and `ctxDiffView` gain `Tree bool`, set from the read in `ctxList` and `ctxDiff` (`d.Tree = from.ctx != nil && from.ctx.tree || to.ctx != nil && to.ctx.tree`).
- For a tree read, `placeholder()` is never called: tree versions show `kit.yml`, not a placeholder.

`kit_detail.go`:

```go
// baseLabel names a kit's base: its form, and for a chained kit the parent
// it is built on ("kit: web@v2", plus " + Dockerfile" for its own stage).
func baseLabel(sk studio.StudioKit) string {
	b := sk.Base
	own := "blessed default"
	if sk.BuildContext != "" {
		own = "Dockerfile"
	}
	k, err := b.Kind()
	switch {
	case err != nil:
		return "invalid"
	case k == studio.BaseKit && sk.BuildContext == "":
		return "kit: " + b.Kit
	case b.HasParent():
		if sk.BuildContext == "" {
			own = ownBaseLabel(k)
		}
		return "kit: " + b.Kit + " + " + own
	case sk.BuildContext != "":
		return own
	}
	return ownBaseLabel(k)
}

// viewKit renders stored version v (number n). A tree version shows its
// kit.yml as stored and lists its files; a pre-tree version keeps the
// packed-context rendering (viewStored).
func viewKit(n int, v jam.KitVersion, unpack bool, want string) kitView {
	if v.TreeHash == "" {
		return viewStored(n, string(v.KitYML), unpack, want)
	}
	kv := kitView{N: n, ctx: treeRead(v, want, unpack)}
	text := string(v.KitYML)
	if v.Err != nil {
		kv.ParseErr, kv.Raw, kv.diffText = v.Err.Error(), text, text
		return kv
	}
	kv.YAML, kv.PushYAML, kv.diffText = text, text, text
	fillKitView(&kv, v.Kit)
	return kv
}
```

Then:
- Rename the old `viewKit(n, stored, unpack, want)` to `viewStored`, and move its field-filling tail (from `v.Base, v.Image, v.Digest = …` through `v.Prompt = sk.Prompt`) into `fillKitView(v *kitView, sk studio.StudioKit)`, which both paths call. `viewStored` keeps setting `Files` and `ContextSize` for `context-files`/`context`.
- In `buildKitDetail`, load each version with `ver, _ := jam.LoadKitVersion(store, name, n)`. The rail digest is `shortDigest(studio.BuildDigest(ver.Kit, …))` when `ver.Err == nil`. The view is `viewKit(view, viewVer, true, file)` and the diff side is `viewKit(diff, fromVer, true, file)`.
- `chainOf(store, name string, sk studio.StudioKit)` calls `jam.KitChainView(store, name, sk)`. The effective view is `viewStored(0, y, false, "")`, the merged YAML, as now.
- `kitListRows` reads `cur, _ := jam.LoadKitVersion(store, k.Name, 0)`: `cur.Err != nil` is `Invalid`; otherwise `row.Base, row.Egress = baseLabel(cur.Kit), len(cur.Kit.Egress)`.
- `attention.go` and `search.go` use `cur, ok := jam.LoadKitVersion(store, k.Name, 0)` with `ok && cur.Err != nil` for the "does not parse" item and `cur.Kit` for search.

`templates/kit.html`:
- In the `ctx-diff` card, the heading becomes `{{if .Tree}}Files v{{.From}} → v{{.To}}{{else}}Context files v{{.From}} → v{{.To}}{{end}}`, and the sub becomes `{{if .Tree}}the kit directory, file by file{{else}}the packed <span class="mono">base.context</span>, file by file{{end}}`.
- In the `ctx-files` card, the heading becomes `{{if .Tree}}Files{{else}}Context files{{end}}`, with the sub `{{if .Tree}}v{{.N}}'s directory ({{.Size}}), kit.yml included{{else}}v{{.N}}'s packed <span class="mono">base.context</span> ({{.Size}} encoded){{end}}`.
- The YAML card's sub reads `{{if $.Ctx}}{{if $.Ctx.Tree}}v{{.N}}'s kit.yml, as stored{{else}}v{{.N}}, rendered from the stored kit{{end}}{{else}}v{{.N}}, rendered from the stored kit{{end}}`.

`kitchain.go`: `KitChainView(store Store, name string, sk studio.StudioKit)` drops its own `ParseStudioKit`. `admin.go`'s `GET /admin/kits/{name}` calls it with `v.Kit`, and sets `ChainError` to `v.Err.Error()` when `v.Err != nil`.

`setup_image_bodies.go` `kitDetailBody`: rows get a third cell, and the header becomes `[]string{"  KIT", "SOURCE", "FILES"}`:

```go
		files := "unknown"
		if v, ok := LoadKitVersion(store, r.ID, r.Version); ok {
			switch {
			case v.TreeHash != "":
				files = fmt.Sprintf("tree %s, %d files", shortDigest(v.TreeHash), len(v.Tree.Entries))
			case v.Err != nil:
				files = "not loadable"
			default:
				files = "config text (not yet a tree)"
			}
		}
```

The `v.Err != nil` case covers a broken version. `shortDigest` (in `setup.go`) cuts at 12 bytes, and a hex hash is ASCII. Regenerate the goldens: `go test ./internal/jam/ -run TestSetupGolden -update-setup` (the flag is `-update-setup`; check the golden test's name with `grep -n updateSetupGolden internal/jam/setup_render_test.go`). Review the golden diff: only the new column changes.

Docs:
- `docs/usage/jam/kits.md`, the admin-UI paragraph after the verbs: "Its YAML and diffs show a packed `context` as `«tar.gz…»` …" becomes: "A version's page shows its `kit.yml` as stored and a **Files** table (path, mode, size, `kit.yml` included). A diff shows the `kit.yml` line diff, then the files added, removed, modified (sizes) or mode-changed, with the unchanged counted. `&file=<path>` shows one file or its line diff (binary or over 64 KiB: sizes only)."
- `docs/usage/jam/ui-pages.md` "Kit pages": the sentence on the viewed version's base ("image ref, context-files paths, or a packed `context`'s files…") becomes "the base (image ref, `Dockerfile`, or the blessed default; `kit: <parent>` for a chained kit), the kit's files and its `kit.yml` (<link `kits.md#the-kit-verbs` as "kits.md"> has the table, the diff and `&file=`)". Adjust the YAML-card sentence: "its `kit.yml` as stored, with **Copy**".
- `docs/usage/jam/inspect.md`, the `kit` row of the sections table: add "each link's tree (`tree <12 hex>, N files`)".

Bump `updated:` on each.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./... && just lint && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS, and the audit at most 733.

- [ ] **Step 5: Commit**

```bash
git add -A internal/jam docs/usage/jam
git commit -m "adminui, inspect: a kit version's files and their diff, from its tree

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Diff everywhere: `kit diff` in the API and CLI, kit diffs in config plans (slice 2)

**Files:**
- Create: `internal/jam/kit_diff.go`
- Modify:
  - `internal/jam/admin.go`: the route.
  - `internal/jam/adminclient/adminclient.go`: `KitDiff`.
  - `cmd/at-jam/main.go`: `kit diff`, the flags, the usage line.
  - `internal/jam/config_plan.go`: `ConfigOp.Diff`, set for kit updates.
  - `cmd/at-jam/configgit.go`: `printConfigPlan` prints a kit's `Diff`.
  - `internal/jam/adminui/config_page.go` and `templates/config.html`: show a kit op's `Diff`.
  - Docs: `docs/usage/jam/kits.md`, `docs/usage/jam/config-git.md`.
- Test: `internal/jam/kit_diff_test.go`, `cmd/at-jam/kitpush_test.go` (add `TestKitDiffCLI`), `internal/jam/config_plan_test.go` (add `TestPlanKitUpdateCarriesItsDiff`)

**Interfaces:**
- Consumes: `kittree.DiffText` (Task 3); `LoadKitVersion` (Task 7); `kitSetEntry.tree`/`blobs`, `p.m` (Task 10).
- Produces:
```go
type KitDiffResult struct { Name string `json:"name"`; From int `json:"from"`; To int `json:"to"`; Text string `json:"text"` }
func KitVersionDiff(store Store, name string, from, to int) (KitDiffResult, error) // to 0 = current; from 0 = the version before to
// route: GET /admin/kits/{name}/diff?from=N&to=M
func (c *Client) KitDiff(name string, from, to int) (jam.KitDiffResult, error)
// ConfigOp gains: Diff string `json:"diff,omitempty"` // a kit update's version diff (Jam → repo), kittree.DiffText
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/jam/kit_diff_test.go
package jam

import (
	"strings"
	"testing"
)

func TestKitVersionDiff(t *testing.T) {
	st := NewMemStore()
	for _, fs := range [][]string{{"kit.yml", "kind: studio\nprompt: a\n", "run.sh", "echo\n"}, {"kit.yml", "kind: studio\nprompt: b\n", "run.sh*", "echo\n"}} {
		if _, _, err := PushKitFiles(st, "app", files(fs...)); err != nil {
			t.Fatal(err)
		}
	}
	d, err := KitVersionDiff(st, "app", 0, 0)
	if err != nil || d.From != 1 || d.To != 2 {
		t.Fatalf("diff = %+v %v", d, err)
	}
	if !strings.Contains(d.Text, "  -prompt: a\n  +prompt: b\n") || !strings.Contains(d.Text, "  mode      run.sh  0644 → 0755\n") {
		t.Fatalf("text:\n%s", d.Text)
	}
	if _, err := KitVersionDiff(st, "app", 1, 9); err == nil || WriteStatus(err, 500) != 404 {
		t.Fatalf("unknown version: %v", err)
	}
	if _, err := KitVersionDiff(st, "app", 1, 1); err == nil || WriteStatus(err, 500) != 400 {
		t.Fatalf("same version: %v", err)
	}
}
```

```go
// cmd/at-jam/kitpush_test.go — add
func TestKitDiffCLI(t *testing.T) {
	store := jam.NewMemStore()
	for _, p := range []string{"prompt: a\n", "prompt: b\n"} {
		if _, err := store.PushKit("app", "kind: studio\n"+p); err != nil {
			t.Fatal(err)
		}
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "diff", "--admin-url", ts.URL, "app"}, func(string) string { return "" }, &out, &errb); code != 0 || !strings.Contains(out.String(), "+prompt: b") || !strings.HasPrefix(out.String(), "app v1 → v2\n") {
		t.Fatalf("exit=%d out=%q err=%q", code, out.String(), errb.String())
	}
}
```

Add to `internal/jam/config_plan_test.go`:

```go
func TestPlanKitUpdateCarriesItsDiff(t *testing.T) {
	st := NewMemStore()
	if _, _, err := PushKitFiles(st, "app", files("kit.yml", "kind: studio\nprompt: a\n")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mustWrite(t, dir+"/kits/app/kit.yml", "kind: studio\nprompt: b\n", 0o644)
	tr, err := LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	marks := []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "c", Digest: configObject{Canon: liveCanon(t, st, "app")}.Digest()}}
	plan := PlanConfig(ConfigPlanInput{Tree: tr, Snapshot: st.ExportConfig(), Marks: marks, Env: allCreds, Source: gitsource.Source{Repo: ConfigDirRepo}, Commit: ConfigDirCommit, Now: time.Unix(1, 0)})
	op := opOf(plan, ConfigKindKit, "app")
	if op.Action != ConfigUpdate || !strings.Contains(op.Diff, "  -prompt: a\n  +prompt: b\n") {
		t.Fatalf("op = %+v", op)
	}
}

// liveCanon is kit name's canonical form in st (its current tree's listing).
func liveCanon(t *testing.T, st Store, name string) []byte {
	t.Helper()
	v, _ := LoadKitVersion(st, name, 0)
	return kitCanon(v.Tree, st.KitBlob, nil)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ ./cmd/at-jam/ -run 'KitVersionDiff|KitDiffCLI|PlanKitUpdateCarriesItsDiff'`
Expected: FAIL, `undefined: KitVersionDiff` and `op.Diff undefined`.

- [ ] **Step 3: Implement**

```go
// internal/jam/kit_diff.go
package jam

import (
	"net/http"

	"github.com/aethons-tools/cove/internal/kittree"
)

// KitDiffResult is GET /admin/kits/{name}/diff: version From → To of kit
// Name, as kittree.DiffText renders it.
type KitDiffResult struct {
	Name string `json:"name"`
	From int    `json:"from"`
	To   int    `json:"to"`
	Text string `json:"text"`
}

// KitVersionDiff diffs kit name's version from → to (to 0 = current; from 0
// = the version before to). An unknown kit or version is 404; from == to, no
// earlier version, or a version not stored as a tree is 400.
func KitVersionDiff(store Store, name string, from, to int) (KitDiffResult, error) {
	k, ok := store.GetKit(name)
	if !ok {
		return KitDiffResult{}, writeErr(http.StatusNotFound, "no such kit")
	}
	if to == 0 {
		to = k.Current
	}
	if from == 0 {
		for _, v := range k.VersionNumbers() {
			if v < to {
				from = v
			}
		}
	}
	if from == 0 || from == to {
		return KitDiffResult{}, writeErr(http.StatusBadRequest, "kit %s: pick two different versions (--from, --to)", name)
	}
	a, okA := LoadKitVersion(store, name, from)
	b, okB := LoadKitVersion(store, name, to)
	if !okA || !okB {
		missing := from
		if okA {
			missing = to
		}
		return KitDiffResult{}, writeErr(http.StatusNotFound, "kit %s has no version v%d", name, missing)
	}
	for _, v := range []KitVersion{a, b} {
		if v.TreeHash == "" {
			return KitDiffResult{}, writeErr(http.StatusBadRequest, "kit %s v%d is not stored as a tree", name, v.Version)
		}
	}
	return KitDiffResult{Name: name, From: from, To: to, Text: kittree.DiffText(a.Tree, b.Tree, store.KitBlob)}, nil
}
```

`admin.go`, next to `/history`:

```go
	mux.HandleFunc("GET /admin/kits/{name}/diff", func(w http.ResponseWriter, r *http.Request) {
		var from, to int
		for key, dst := range map[string]*int{"from": &from, "to": &to} {
			if q := r.URL.Query().Get(key); q != "" {
				n, err := strconv.Atoi(q)
				if err != nil || n < 1 {
					http.Error(w, key+" must be a version number", http.StatusBadRequest)
					return
				}
				*dst = n
			}
		}
		d, err := KitVersionDiff(store, r.PathValue("name"), from, to)
		if err != nil {
			http.Error(w, err.Error(), WriteStatus(err, http.StatusInternalServerError))
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
```

`adminclient.go`:

```go
// KitDiff fetches kit name's version diff from → to (0 = the defaults: the
// version before to, and current).
func (c *Client) KitDiff(name string, from, to int) (jam.KitDiffResult, error) {
	q := url.Values{}
	if from > 0 {
		q.Set("from", strconv.Itoa(from))
	}
	if to > 0 {
		q.Set("to", strconv.Itoa(to))
	}
	path := "/admin/kits/" + url.PathEscape(name) + "/diff"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var d jam.KitDiffResult
	err := c.do("GET", path, nil, &d)
	return d, err
}
```

`cmd/at-jam/main.go`, `cmdKit`:
- Add the flags `from := fs.Int("from", 0, "diff: the older version (default: the one before --to)")` and `to := fs.Int("to", 0, "diff: the newer version (default: current)")`.
- Add `diff` to the usage lines of `cmdKit` and the `kit` command's `Brief`.
- Add the case:

```go
	case "diff":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam kit diff: expected one kit name")
			return 2
		}
		d, err := c.KitDiff(pos[0], *from, *to)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s v%d → v%d\n%s", d.Name, d.From, d.To, d.Text)
```

`config_plan.go`:
- `ConfigOp` gains `Diff string \`json:"diff,omitempty"\` // a kit update's version diff, Jam → repo (kittree.DiffText)`.
- In `planKits`, compute each entry's diff before `classify`, and set it on the op `classify` or `record` appends:

```go
		jamTree := kittree.Tree{}
		if k, ok := p.m.kits[e.Name]; ok {
			jamTree = p.m.kitTrees[k.Trees[k.Current]]
		}
		blobs := func(sha string) ([]byte, bool) {
			if b, ok := e.blobs[sha]; ok {
				return b, true
			}
			b, ok := p.m.kitBlobs[sha]
			return b, ok
		}
		diff := ""
		if len(jamTree.Entries) > 0 {
			diff = kittree.DiffText(jamTree, e.tree, blobs)
		}
		n := len(p.plan.Ops)
		action, drift, ok := p.classify(key, repo)
		if !ok {
			p.setKitDiff(n, diff)
			continue
		}
		…existing configKitDoc / record…
		p.setKitDiff(n, diff)
```

  with:

```go
// setKitDiff sets diff on the kit op appended at or after index n, when it is an update.
func (p *planner) setKitDiff(n int, diff string) {
	for i := n; i < len(p.plan.Ops); i++ {
		if p.plan.Ops[i].Action == ConfigUpdate {
			p.plan.Ops[i].Diff = diff
		}
	}
}
```

  `planDigest` hashes the plan's ops. `Diff` is derived from the same trees as `Before`/`After`, so including it changes no plan identity; leave `planDigest` as is.
- `cmd/at-jam/configgit.go` `printConfigPlan`: right after the `fmt.Fprintf(w, "%s  %s %s%s\n", …)` line, add `if op.Kind == jam.ConfigKindKit && op.Diff != "" { fmt.Fprint(w, indent(op.Diff)); continue }`. Every kit update now shows its file-level diff, not only a drift one. `indent` exists in the file.
- `internal/jam/adminui/templates/config.html`: `planOpView` embeds `jam.ConfigOp`, so the op's card can render `{{with .Diff}}<pre class="diff">{{.}}</pre>{{else}}…the existing line diff…{{end}}`. `config_page.go` needs no new field.

Docs:
- `docs/usage/jam/kits.md` verbs block: add `at-jam kit diff web [--from 2] [--to 3]                    # kit.yml's line diff, then the files added/removed/modified/mode (default: current vs the one before)`.
- `docs/usage/jam/config-git.md`: in the plan output description, add "A kit update prints its version diff (`kit.yml` lines, then the files) under its line."

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./... && just lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A internal/jam cmd/at-jam docs/usage/jam
git commit -m "kits: version diff in the API, at-jam kit diff, and config plans

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: The one-time migration to trees

**Files:**
- Create: `internal/jam/kit_migrate.go`, `internal/jam/kit_migrate_legacy.go`, `internal/jam/testdata/kitmigrate/{context,context-files,plain}.golden`
- Modify:
  - `cmd/at-jam/main.go`: run the migration and the GC at serve start.
  - `internal/jam/admin.go`: `POST /admin/config` migrates after an import.
  - `internal/jam/adminui/attention.go`: a broken current version's text.
  - Docs: `docs/usage/jam/kits-directory.md` (section `## Stored as trees`), `docs/usage/jam/monitoring.md` (the condition row), `docs/usage/jam/backup.md` (pre-tree backups).
- Test: `internal/jam/kit_migrate_test.go`, `internal/jam/config_admin_test.go` (add `TestConfigImportMigratesAPreTreeBackup`)

**Interfaces:**
- Consumes: `kittree.Build`, `kittree.NormMode`, limits (Task 1); `studio.RenderKitYAML`, `studio.ParseStudioKit` (Task 4); `KitObjects`, `ConfigWrite.KitObjects`, `Kit.Legacy`/`Broken` (Tasks 5–6); `LoadKitVersion` (Task 7); `kitCanon` (Task 10); `configMu` (`config_apply.go`), `kitMu` (`studiokit.go`); `condition.Tracker`.
- Produces:
```go
const KitMigrateKind = "kit.migrate"
type KitTreeMigration struct { Migrated []string; Broken []string } // kit names; name@vN
func MigrateKitTrees(store Store, t *condition.Tracker, log *slog.Logger) (KitTreeMigration, error)
func legacyKitFiles(text string) ([]kittree.File, error)
func legacyKitTree(text string) (kittree.Tree, map[string][]byte, error)
func legacyKitCanonDigest(config string, tracked *KitSource) string
```

- [ ] **Step 1: Write the failing tests**

The fixtures are the stored JSON text of a pre-tree Jam, written out literally. Nothing here depends on the `Base` context fields that Task 14 deletes.

```go
// internal/jam/kit_migrate_test.go
package jam

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/studio"
)

var updateKitMigrateGolden = flag.Bool("update-kit-migrate", false, "rewrite internal/jam/testdata/kitmigrate/*.golden")

// tgz packs name, mode, body triples as a pre-tree context: a tar.gz with
// "./" names and directory entries, as `tar czf - .` makes, in base64.
func tgz(t *testing.T, ents ...string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755})
	for i := 0; i+2 < len(ents); i += 3 {
		var mode int64
		fmt.Sscanf(ents[i+1], "%o", &mode)
		if err := tw.WriteHeader(&tar.Header{Name: "./" + ents[i], Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(ents[i+2]))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(ents[i+2]))
	}
	_ = tw.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// listing renders a version's files for a golden: each path and mode, then its content.
func listing(t *testing.T, st Store, name string) string {
	t.Helper()
	v, ok := LoadKitVersion(st, name, 0)
	if !ok || v.TreeHash == "" {
		t.Fatalf("%s is not a tree: %+v", name, v)
	}
	var b strings.Builder
	for _, e := range v.Tree.Entries {
		data, _ := st.KitBlob(e.SHA256)
		fmt.Fprintf(&b, "== %s %04o\n%s\n", e.Path, e.Mode, data)
	}
	return b.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "kitmigrate", name+".golden")
	if *updateKitMigrateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != got {
		t.Fatalf("%s (rerun with -update-kit-migrate to accept):\n%s", name, got)
	}
}

func legacyDoc(name string, texts ...string) Kit {
	k := Kit{Name: name, Current: len(texts), Versions: map[int]string{}}
	for i, s := range texts {
		k.Versions[i+1] = s
	}
	return k
}

func TestMigrateKitTreesGoldens(t *testing.T) {
	st := NewMemStore()
	ctx := tgz(t, "Dockerfile", "644", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\nCOPY run.sh /usr/local/bin/\n", "run.sh", "755", "echo hi\n")
	docs := []Kit{
		legacyDoc("context", `{"kind":"studio","base":{"context":"`+ctx+`"},"egress":["github.com"],"prompt":"p"}`),
		legacyDoc("context-files", `{"kind":"studio","base":{"contextFiles":{"dockerfile":"FROM x\n","sub":{"a.txt":"hi\n"}}}}`),
		legacyDoc("plain", `{"kind":"studio","notes":[{"name":"r.md","read-when":"releasing","body":"# R\n"}]}`),
	}
	if err := st.PutKitDocs(docs, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateKitTrees(st, nil, nil)
	if err != nil || len(rep.Migrated) != 3 || len(rep.Broken) != 0 {
		t.Fatalf("report %+v %v", rep, err)
	}
	for _, d := range docs {
		golden(t, d.Name, listing(t, st, d.Name))
		k, _ := st.GetKit(d.Name)
		if len(k.Versions) != 0 || k.Legacy[1] != d.Versions[1] {
			t.Fatalf("%s: legacy text not kept: %+v", d.Name, k)
		}
	}
	v, _ := LoadKitVersion(st, "context", 0)
	if e, _ := v.Tree.Lookup("run.sh"); e.Mode != 0o755 || v.Kit.BuildContext == "" {
		t.Fatalf("context kit: %+v", v.Tree)
	}
	before := st.ExportConfig()
	if rep, err := MigrateKitTrees(st, nil, nil); err != nil || len(rep.Migrated) != 0 {
		t.Fatalf("second run: %+v %v", rep, err)
	}
	if !reflect.DeepEqual(st.ExportConfig(), before) {
		t.Fatal("a second run changed the store")
	}
}

func TestMigrateKitTreesKeepsABrokenVersion(t *testing.T) {
	st := NewMemStore()
	if err := st.PutKitDocs([]Kit{legacyDoc("app", `{"kind":"studio"}`, `{"kind":"studio","base":{"context":"!!not base64"}}`)}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	tr := condition.New(condition.Options{})
	rep, err := MigrateKitTrees(st, tr, nil)
	if err != nil || len(rep.Broken) != 1 || rep.Broken[0] != "app@v2" {
		t.Fatalf("report %+v %v", rep, err)
	}
	if _, err := StudioKitDefinition(st, "app", harnessinstall.Default()); err == nil || !strings.Contains(err.Error(), "could not be migrated to a tree: its base.context is not valid base64") {
		t.Fatalf("raise must fail closed: %v", err)
	}
	if c, ok := tr.Get(condition.Key(KitMigrateKind, "app")); !ok || c.Severity != condition.Warning {
		t.Fatalf("condition = %+v %v", c, ok)
	}
	if v, ok := LoadKitVersion(st, "app", 1); !ok || v.Err != nil {
		t.Fatalf("v1 must have migrated: %+v", v)
	}
	if err := PinKitChecked(st, "app", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateKitTrees(st, tr, nil); err != nil {
		t.Fatal(err)
	}
	if tr.IsOpen(condition.Key(KitMigrateKind, "app")) {
		t.Fatal("the condition must clear once current is not broken")
	}
}

func TestMigrateKitTreesDefaultKitNotReversioned(t *testing.T) {
	st := NewMemStore()
	text, err := json.Marshal(studio.DefaultStudioKit())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutKitDocs([]Kit{legacyDoc(studio.DefaultStudioKitID, string(text))}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateKitTrees(st, nil, nil); err != nil {
		t.Fatal(err)
	}
	ref, err := EnsureDefaultStudioKit(st)
	if err != nil || ref.Version != 1 {
		t.Fatalf("the default kit got a new version: %+v %v", ref, err)
	}
	if ref.Digest != studio.BuildDigest(studio.DefaultStudioKit(), harnessinstall.Default()) {
		t.Fatal("a kit without a Dockerfile must keep its digest")
	}
}

func TestMigrateKitTreesRekeysTheMark(t *testing.T) {
	st := NewMemStore()
	text := `{"kind":"studio","prompt":"p"}`
	if err := st.PutKitDocs([]Kit{legacyDoc("app", text)}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "c", Digest: legacyKitCanonDigest(text, nil)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateKitTrees(st, nil, nil); err != nil {
		t.Fatal(err)
	}
	states, err := ConfigMarkStates(st)
	if err != nil || len(states) != 1 || states[0].State != ConfigInSync {
		t.Fatalf("states = %+v %v", states, err)
	}
}
```


```go
// internal/jam/config_admin_test.go — add
func TestConfigImportMigratesAPreTreeBackup(t *testing.T) {
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	snap := ConfigSnapshot{Version: 4, Kits: []Kit{{Name: "app", Current: 1, Versions: map[int]string{1: `{"kind":"studio"}`}}}}
	body, _ := json.Marshal(snap)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/admin/config", bytes.NewReader(body)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("import = %d %s", w.Code, w.Body)
	}
	if k, _ := st.GetKit("app"); k.Trees[1] == "" || len(k.Versions) != 0 {
		t.Fatalf("the imported kit was not migrated: %+v", k)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'MigrateKitTrees|ConfigImportMigratesAPreTreeBackup'`
Expected: FAIL, `undefined: MigrateKitTrees`.

- [ ] **Step 3: Implement the legacy reader**

```go
// internal/jam/kit_migrate_legacy.go
package jam

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// The fixed reasons a pre-tree version is kept broken (Kit.Broken). They
// name no path and quote no content.
var (
	errLegacyBase64 = errors.New("its base.context is not valid base64")
	errLegacyTar    = errors.New("its base.context is not a readable tar.gz")
	errLegacyLink   = errors.New("its base.context holds a link or an irregular file")
	errLegacyUnsafe = errors.New("its base.context holds an unsafe path")
	errLegacyTree   = errors.New("its base.context-files is not a tree of files")
	errLegacyKitYML = errors.New("its context holds a kit.yml at the root")
	errLegacyLimits = errors.New("its context does not fit a kit tree (a safe path for every file; at most 2000 files, 64 MiB, 4 MiB a file)")
)

// legacyKitFiles is a pre-tree version's files. kit.yml is its stored config
// with the packed-context fields taken out, re-rendered (studio.RenderKitYAML).
// Beside it go the files of its base.context (a base64 tar.gz) or
// base.contextFiles (an inline tree; the reserved dockerfile key is the
// Dockerfile, every file 0644). A config that does not parse once those
// fields are out is kept verbatim as kit.yml, so it stays exactly as invalid
// as it was. A context that cannot be read is one of the errLegacy reasons.
func legacyKitFiles(text string) ([]kittree.File, error) {
	verbatim := []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: []byte(text)}}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text), &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return verbatim, nil
	}
	top := doc.Content[0]
	var b64 string
	var inline *yaml.Node
	if base := takeKey(top, "base", false); base != nil && base.Kind == yaml.MappingNode {
		if n := takeKey(base, "context", true); n != nil {
			b64 = n.Value
		}
		for _, k := range []string{"contextFiles", "context-files"} {
			if n := takeKey(base, k, true); n != nil {
				inline = n
			}
		}
		takeKey(base, "context-dir", true)
		if len(base.Content) == 0 {
			takeKey(top, "base", true)
		}
	}
	stripped, err := yaml.Marshal(&doc)
	if err != nil {
		return verbatim, nil
	}
	sk, err := studio.ParseStudioKit(stripped)
	if err != nil {
		return verbatim, nil
	}
	yml, err := studio.RenderKitYAML(sk)
	if err != nil {
		return verbatim, nil
	}
	files := []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: yml}}
	var ctx []kittree.File
	switch {
	case b64 != "":
		ctx, err = legacyContextTar(b64)
	case inline != nil:
		ctx, err = legacyContextTree(inline, "")
	}
	if err != nil {
		return nil, err
	}
	for _, f := range ctx {
		if f.Path == kittree.KitFile {
			return nil, errLegacyKitYML
		}
	}
	return append(files, ctx...), nil
}

// takeKey returns mapping m's value for key; with remove it also deletes the pair.
func takeKey(m *yaml.Node, key string, remove bool) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			if remove {
				m.Content = append(m.Content[:i], m.Content[i+2:]...)
			}
			return v
		}
	}
	return nil
}

// legacyContextTar reads a packed context with the old pre-flight's safety
// rules: no absolute or ".." names, no links or irregular entries, the tree
// limits; directory entries and "./" prefixes are dropped, and a later
// duplicate replaces an earlier one (extraction keeps the last).
func legacyContextTar(b64 string) ([]kittree.File, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errLegacyBase64
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, errLegacyTar
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var out []kittree.File
	at := map[string]int{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, errLegacyTar
		}
		if path.IsAbs(hdr.Name) || strings.Contains("/"+hdr.Name+"/", "/../") {
			return nil, errLegacyUnsafe
		}
		name := path.Clean(hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, errLegacyLink
		}
		if kittree.CheckPath(name) != nil {
			return nil, errLegacyUnsafe
		}
		data, err := io.ReadAll(io.LimitReader(tr, kittree.MaxFile+1))
		if err != nil {
			return nil, errLegacyTar
		}
		if int64(len(data)) > kittree.MaxFile {
			return nil, errLegacyLimits
		}
		f := kittree.File{Path: name, Mode: kittree.NormMode(fs.FileMode(hdr.Mode)), Data: data}
		if i, dup := at[name]; dup {
			out[i] = f
			continue
		}
		if len(out) == kittree.MaxEntries {
			return nil, errLegacyLimits
		}
		at[name] = len(out)
		out = append(out, f)
	}
}

// legacyContextTree flattens an inline context-files tree.
func legacyContextTree(n *yaml.Node, prefix string) ([]kittree.File, error) {
	if n.Kind != yaml.MappingNode {
		return nil, errLegacyTree
	}
	var out []kittree.File
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		p := path.Join(prefix, k)
		if prefix == "" && k == "dockerfile" {
			p = kittree.Dockerfile
		}
		switch v.Kind {
		case yaml.ScalarNode:
			out = append(out, kittree.File{Path: p, Mode: 0o644, Data: []byte(v.Value)})
		case yaml.MappingNode:
			sub, err := legacyContextTree(v, p)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		default:
			return nil, errLegacyTree
		}
	}
	return out, nil
}

// legacyKitTree is legacyKitFiles as a tree. Files that do not fit a tree
// are errLegacyLimits.
func legacyKitTree(text string) (kittree.Tree, map[string][]byte, error) {
	files, err := legacyKitFiles(text)
	if err != nil {
		return kittree.Tree{}, nil, err
	}
	tree, blobs, err := kittree.Build(files)
	if err != nil {
		return kittree.Tree{}, nil, errLegacyLimits
	}
	return tree, blobs, nil
}
```


- [ ] **Step 4: Implement the migration**

```go
// internal/jam/kit_migrate.go
package jam

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/kittree"
)

// KitMigrateKind is the condition kind of a kit whose current version the
// migration kept broken (kit.migrate:<name>).
const KitMigrateKind = "kit.migrate"

// KitTreeMigration reports a MigrateKitTrees run.
type KitTreeMigration struct {
	Migrated []string // kits whose pre-tree versions became trees
	Broken   []string // name@vN of each version kept broken
}

// MigrateKitTrees is the one-time kit migration (spec §4). It runs at every
// serve start and after a config import. Every kit still holding pre-tree
// config text (Kit.Versions) gets each version as a tree (legacyKitTree), or,
// for one whose context cannot be read, a Broken reason; the text moves to
// Kit.Legacy.
//
// A managed kit's config mark that recorded its current version's old
// canonical form is re-keyed to the tree's, so the upgrade shows no drift.
// Everything goes in one store.ApplyConfig, logged "kits migrated to trees";
// a store with nothing to migrate is not written.
//
// Then the kit.migrate conditions are re-evaluated (t may be nil): open for
// each kit whose current version is broken, cleared for every other.
func MigrateKitTrees(store Store, t *condition.Tracker, log *slog.Logger) (KitTreeMigration, error) {
	configMu.Lock()
	defer configMu.Unlock()
	kitMu.Lock()
	defer kitMu.Unlock()
	var rep KitTreeMigration
	w := ConfigWrite{KitObjects: KitObjects{Blobs: map[string][]byte{}}}
	trees := map[string]kittree.Tree{}
	marks := map[ConfigKey]ConfigMark{}
	for _, mk := range store.ListConfigMarks() {
		marks[mk.Key()] = mk
	}
	for _, k := range store.ListKits() {
		if len(k.Versions) == 0 {
			continue
		}
		doc := copyKit(k)
		if doc.Trees == nil {
			doc.Trees = map[int]string{}
		}
		doc.Legacy = maps.Clone(k.Versions)
		for _, v := range slices.Sorted(maps.Keys(k.Versions)) {
			tree, blobs, err := legacyKitTree(k.Versions[v])
			if err != nil {
				if doc.Broken == nil {
					doc.Broken = map[int]string{}
				}
				doc.Broken[v] = err.Error()
				rep.Broken = append(rep.Broken, fmt.Sprintf("%s@v%d", k.Name, v))
				continue
			}
			doc.Trees[v] = tree.Hash()
			trees[tree.Hash()] = tree
			maps.Copy(w.KitObjects.Blobs, blobs)
		}
		doc.Versions = nil
		w.Kits = append(w.Kits, doc)
		rep.Migrated = append(rep.Migrated, k.Name)
		mk, managed := marks[ConfigKey{ConfigKindKit, k.Name}]
		cur, hasCur := k.Versions[k.Current]
		tree, isTree := trees[doc.Trees[k.Current]]
		if managed && hasCur && isTree && mk.Digest == legacyKitCanonDigest(cur, k.Source) {
			mk.Digest = configObject{Canon: kitCanon(tree, kittree.MapBlobs(w.KitObjects.Blobs), k.Source)}.Digest()
			w.Marks = append(w.Marks, mk)
		}
	}
	for _, sha := range slices.Sorted(maps.Keys(trees)) {
		w.KitObjects.Trees = append(w.KitObjects.Trees, trees[sha])
	}
	if len(w.Kits) > 0 {
		if err := store.ApplyConfig(w); err != nil {
			return rep, fmt.Errorf("migrate kits to trees: %w", err)
		}
		if log != nil {
			log.Info("kits migrated to trees", "kits", len(rep.Migrated), "broken", rep.Broken)
		}
	}
	kitMigrateConditions(store, t)
	return rep, nil
}

// legacyKitCanonDigest is the digest a config mark recorded for a pre-tree
// kit: sha256 of its stored config, then for a tracked kit
// "\n# source: <repo>[//dir]@<ref>\n".
func legacyKitCanonDigest(config string, tracked *KitSource) string {
	b := config
	if tracked != nil {
		b += "\n# source: " + gitsource.Source{Repo: tracked.Repo, Dir: tracked.Dir, Ref: tracked.Ref}.String() + "\n"
	}
	sum := sha256.Sum256([]byte(b))
	return hex.EncodeToString(sum[:])
}

// kitMigrateConditions opens kit.migrate:<name> for each kit whose current
// version is broken and clears every other open one.
func kitMigrateConditions(store Store, t *condition.Tracker) {
	open := map[string]bool{}
	for _, k := range store.ListKits() {
		reason, broken := k.Broken[k.Current]
		if !broken {
			continue
		}
		key := condition.Key(KitMigrateKind, k.Name)
		open[key] = true
		t.Raise(condition.Condition{Key: key, Severity: condition.Warning,
			Summary: fmt.Sprintf("kit %s v%d could not be migrated to a tree", k.Name, k.Current),
			Detail:  reason,
			Fix:     "push the kit as a directory (a Dockerfile next to kit.yml), or pin a version that migrated (at-jam kit pin)"})
	}
	for _, key := range t.OpenKeys(KitMigrateKind) {
		if !open[key] {
			t.Clear(key)
		}
	}
}
```

A nil `*condition.Tracker` is a valid no-op (`Raise`, `Clear` and `OpenKeys` all check for it), so no nil guard is needed. The store also has the kits' new trees; `kitCanon` only needs this write's blobs, which `legacyKitTree` returned whole for each tree.

`cmd/at-jam/main.go`, right after `clearStaleCredConditions(conds, cfg.credNames())` (the migration needs the tracker), before the drift check starts:

```go
	// Kits as trees (docs/usage/jam/kits-directory.md#stored-as-trees): every
	// pre-tree version becomes a tree, once; then unreferenced kit objects go.
	// A failed migration fails closed: a half-read registry would raise kits
	// from text the build no longer reads.
	if mig, err := jam.MigrateKitTrees(st, conds, log); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	} else if len(mig.Broken) > 0 {
		log.Warn("kit versions kept broken (their packed context could not be read)", "versions", mig.Broken)
	}
	if gc, err := st.GCKitObjects(); err != nil {
		log.Warn("kit object collection failed (retried next start)", "reason", err.Error())
	} else if gc.Trees+gc.Blobs > 0 {
		log.Info("kit objects collected", "trees", gc.Trees, "blobs", gc.Blobs)
	}
```

`jam.EnsureDefaultStudioKit` runs later in serve, so the default kit's migrated tree is already there when it is seeded. `admin.go` `POST /admin/config`, after a successful `store.ImportConfig(snap)`:

```go
		// A pre-tree backup's kits are config text: migrate them now.
		if _, err := MigrateKitTrees(store, nil, log); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
```

`adminui/attention.go`: in the kits loop, a kit whose `k.Broken[k.Current]` is set gets `Why: fmt.Sprintf("kit %s v%d could not be migrated to a tree", k.Name, k.Current)`. Any other load error keeps "does not parse".

Docs:
- `docs/usage/jam/kits-directory.md`: add the section `## Stored as trees`, and extend `owns` with "the one-time migration from packed contexts":
  - Jam stores each version as a content-addressed tree in its own database (`kit_trees`, `kit_blobs`), shared by every kit and version, and drops unreferenced ones when a kit is removed and at serve start.
  - **Upgrading.** The first serve start after the upgrade migrates every kit once, in one transaction, and logs `kits migrated to trees`:
    - A packed `base.context` becomes files at the root.
    - `context-files` become files, the `dockerfile` key becoming `Dockerfile`, each `0644`.
    - `kit.yml` becomes the stored definition re-rendered without the context fields.
  - Build digests of kits with a context change once, so running studios show `image=stale` once and rebuild on their next raise. Kits without one keep their image.
  - A version whose context cannot be read is kept **broken**: raising it fails with the reason, and the attention condition `kit.migrate:<name>` is open while it is current. Push the kit as a directory, or pin a version that migrated.
  - The pre-migration text is kept in the kit's `legacy` field for one release.
  - **Rolling back to an older Jam after the migration is not supported.**
- `docs/usage/jam/monitoring.md`: add the row `| \`kit.migrate:<kit>\` | warning | the kit's current version could not be migrated to a tree at the upgrade (<link `kits-directory.md#stored-as-trees` as "kits-directory.md">) | a serve start finds its current version not broken (a new push, or a pin) |`.
- `docs/usage/jam/backup.md`: "A backup from before kit trees (version ≤ 4) still imports. Its kits are migrated to trees right after (<link `kits-directory.md#stored-as-trees` as "kits-directory.md">)."

Bump `updated:` on each.

- [ ] **Step 5: Run the tests, accept the goldens, run everything**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run MigrateKitTreesGoldens -update-kit-migrate`, then read the three golden files. Check each:
- `context`: a `kit.yml` without `base:`, then `Dockerfile 0644` and `run.sh 0755`.
- `context-files`: a `Dockerfile 0644` and `sub/a.txt 0644`.
- `plain`: `kit.yml` only, with the note's `body`.

Then:
Run: `GOPROXY=https://proxy.golang.org,direct go test ./... && just lint && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS, and the audit at most 733.

- [ ] **Step 6: Commit**

```bash
git add -A internal/jam cmd/at-jam docs/usage/jam
git commit -m "jam: migrate every stored kit version to a tree at serve start, once

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 14: Delete the packed-context code

**Files:**
- Delete: `internal/studio/pack.go`, `pack_test.go`, `scan.go`, `context_tar_test.go`, `context_files.go`, `context_files_test.go`, `unpack.go`, `unpack_test.go`, `stored_roundtrip_test.go`, and the packed-context cases of `internal/studio/jail_test.go`
- Modify:
  - `internal/studio/base.go`: `Base` keeps `Kit` and `Image`; `BaseKind` keeps `BaseDefault`, `BaseImage`, `BaseKit`; delete `ContextTree`, `ContextNode`, `ContextTar`, `tarFromTree`, `buildTarGz`, `tarEntry`, `validateContextKeys`, `validateContextCheap` and the caps vars.
  - `internal/studio/studiokit.go`: `ParseStudioKit` loses the `StoredContextFiles` fold; `ToJSON` loses the `context-dir` check.
  - `internal/studio/jail.go`: delete `ResolveInRoot` and `packInRoot`; keep `ReadFileInRoot`, `resolveInRoot`, `within`, `readRegular`, `errTooLarge`, `maxRootFile`, `maxNoteFileBytes`.
  - `internal/studio/notes.go`: delete `ResolveNoteFiles`.
  - `internal/studio/merge.go`: drop the `ContextFiles` clone.
  - `internal/studio/tree.go`: `ParseKitFS` calls `RefuseContextFields` first.
  - `internal/jam/kit_version.go`: the pre-tree branch reports "not migrated".
  - `internal/jam/launcher/prepare.go`: delete the packed-context branches and `scannedContextTar`.
  - `internal/jam/adminui/kit_context.go`, `kit_detail.go`: delete `readContext`, `storedContextNode`, `placeholder`, `viewStored`'s packed-context parts, `displayKit` and `treePaths`; `ownBaseLabel` keeps image/default.
  - `internal/jam/store.go`, `memstate.go`, `memstore.go`, `pgstore.go`: delete `KitConfig`, `PushKitMeta`, `PushKitTracked`.
  - `internal/jam/storetest/kits_source.go`, `conformance.go`: use `PushKitVersion` / `LoadKitVersion`.
  - `internal/jam/kit_version.go` and `cloneKit`: drop `Base.ContextFiles`.
  - The tests that seeded packed contexts: `internal/jam/launcher/{prepare_test,prepare_chain_test,launcher_test,chain_integration_test}.go`, `internal/jam/{kitchain_raise_test,kitsource_test,config_adopt_test,config_snapshot_test,supervisor_test}.go`, `internal/jam/adminui/kit_context_test.go`, `internal/studio/{base_test,digest_test,merge_test,studiokit_test}.go`, `internal/jam/gitsource/load_test.go`, `cmd/at-jam/{main_test,configgit_write_test,config_test}.go`, `internal/agentrun/workload_test.go`, `internal/assemble/*_test.go`, `internal/harnessinstall/harnessinstall_test.go`.
  - Docs: `docs/usage/jam/kits.md` (the last packed-context mentions), `docs/usage/jam/ui-pages.md`.
- Test: the suite. Add `internal/studio/tree_test.go` `TestParseKitFSRefusesPackedContext`.

**Interfaces:**
- Consumes: everything above.
- Produces: no new names. After this task, `rg -n 'ContextFiles|ContextDir|PackContextDir|ContextDirFiles|ScanContextTar|walkContextTar|BaseContextZip|BaseContextFiles|BaseContextDir|ResolveContextDir|ResolveInRoot|KitConfig\(|PushKitMeta|PushKitTracked|contexts=files' --type go` finds no match outside `kit_migrate_legacy.go`'s field-name strings.

- [ ] **Step 1: Write the failing test**

```go
// internal/studio/tree_test.go — add
func TestParseKitFSRefusesPackedContext(t *testing.T) {
	tr, blobs := treeOf(t, map[string]string{"kit.yml": "kind: studio\nbase:\n  context: H4sI\n"})
	if _, err := ParseTree(tr, blobs); err == nil || err.Error() != "base.context is no longer supported; put a Dockerfile next to kit.yml" {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/studio/ -run TestParseKitFSRefusesPackedContext`
Expected: FAIL. The lenient `ParseKitFS` still accepts `base.context`.

- [ ] **Step 3: Delete, in this order, building after each group**

1. **`studio`.**
   - `ParseKitFS`'s first statement after reading `data`: `if err := RefuseContextFields(data); err != nil { return StudioKit{}, err }`.
   - Cut `Base` to:

```go
// Base names the image a kit builds FROM. Kit names a parent kit ("name"
// follows its current version, "name@v<N>" pins N); Image is a prebuilt,
// provenance-gated ref; both empty is the blessed default. A Dockerfile next
// to kit.yml is the kit's own build context (StudioKit.BuildContext), built
// FROM ${COVE_BASE_IMAGE}: the blessed base, or the parent's stage.
type Base struct {
	Kit   string `yaml:"kit,omitempty" json:"kit,omitempty"`
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
}

const (
	BaseDefault BaseKind = iota // blessed default
	BaseImage                   // a prebuilt gated ref
	BaseKit                     // a parent kit's stage (plus the kit's own Dockerfile stage, if any)
)

// Kind reports which base form is set.
func (b Base) Kind() (BaseKind, error) {
	switch {
	case b.Kit != "" && b.Image != "":
		return BaseDefault, fmt.Errorf("base: kit and image are mutually exclusive")
	case b.Kit != "":
		return BaseKit, nil
	case b.Image != "":
		return BaseImage, nil
	}
	return BaseDefault, nil
}
```

   - `validate` keeps the parent checks only. Delete the listed functions and files, and `ResolveInRoot`, `packInRoot`, `ResolveNoteFiles`.
   - `ParseStudioKit` drops the `StoredContextFiles` fold.
   - `Merge` drops `out.Base.ContextFiles = maps.Clone(…)` and the `maps` import if unused.
   - `ToJSON`: delete the `context-dir` check. If `rg -n '\.ToJSON\(' --type go` then shows no caller outside tests, delete `ToJSON` and its tests too.

   Run: `go build ./internal/studio/ && go test ./internal/studio/`. `digest_test.go`, `base_test.go`, `merge_test.go` and `studiokit_test.go` lose their packed-context cases; the `Base` cases keep `kit`/`image`/default.
2. **The launcher.**
   - `resolveOwnBase`'s non-tree branch becomes `return l.cfg.Ops.ResolveKitBase(b.Image)` after the `HasParent` check.
   - `resolveChainStage`'s non-tree branch becomes `if link.Kit.BuildContext == "" { return parentRef, nil }`.
   - Delete `scannedContextTar`. Drop the `kind` switch and the `bytes`/`studio.Base…` uses that remain.
   - The launcher tests that built `ctxBase` stages switch to `treeLink` (Task 8's helper). In `prepare_chain_test.go`, `ctxBase(parent, marker)` becomes `treeLink(t, name, v, "kit.yml", "kind: studio\nbase: {kit: "+parent+"}\n", "Dockerfile", df+"RUN echo "+marker+"\n")`. `TestKitStageTagFormula` keeps the formula for `BuildContext: ""` and adds the `NUL` + context case.

   Run: `go test ./internal/jam/launcher/`.
3. **jam reading.**
   - In `LoadKitVersion`, the `k.Versions` branch becomes `v.KitYML = []byte(text); v.Err = fmt.Errorf("kit %s@v%d is not stored as a tree yet (Jam migrates kits at serve start)", name, version); return v, true`.
   - Delete `KitConfig` from the `Store` interface, `memState`, and the overlay (it went in Task 10). Every remaining caller reads `LoadKitVersion(…).KitYML`: `adminui` (`kitListRows`, `buildKitDetail`, `attention`, `search`, all moved in Task 11) and the `storetest` asserts. `kitConfigLocked` stays for `config_plan_delete.go`; rename it `kitYMLLocked`.
   - Delete `PushKitMeta`/`PushKitTracked`. `storetest/kits_source.go` uses `PushKitVersion(name, tree, objs, meta, track)` with a one-file tree from its `treeObjs` helper. Keep `PushKit(name, kitYML)`, the documented seeding/test helper.
4. **adminui.**
   - `viewKit` keeps only the tree path. A pre-tree version (only reachable on a store that failed to migrate) shows its `Err` as `ParseErr`.
   - Delete `viewStored`'s packed-context parts. The effective (merged) view keeps a small `viewYAML(n, y string) kitView` that parses the merged YAML and fills the view (`fillKitView`).
   - Delete `readContext`, `storedContextNode`, `placeholder`, `displayKit`, `treePaths`, `kitView.Files` and `kitView.ContextSize`. `ctxRead` loses `sum`/`err` fields only a packed context set. The template drops the `ContextSize` and `Files` (context-files) bits and the `{{else}}Context files…` branches.
   - `kit_context_test.go`'s packed cases go; Task 11's `kit_files_test.go` covers the page. Rename `kit_context.go` to `kit_files_view.go` (`git mv`) so its name matches its content.
5. **Tests elsewhere.** `rg -n 'ContextFiles|Context:|context-files|context-dir|base\.context' --type go` over `*_test.go`. Rewrite each fixture that still needs a build context as a tree (`PushKitFiles` with a `Dockerfile`, or `treeLink`). Drop the cases that only tested packing.

Docs: `docs/usage/jam/kits.md` must not mention `context-files`, `context-dir` or a packed `context`, except the one refusal sentence. Likewise `docs/usage/jam/ui-pages.md`, except the "`«tar.gz…»` placeholder", which goes. `rg -n 'context-files|context-dir|base\.context|contexts=files' docs/usage` must show only the refusal texts and `kits-directory.md`'s migration section. Bump `updated:`.

- [ ] **Step 4: Run everything**

Run: `GOPROXY=https://proxy.golang.org,direct go vet ./... && GOPROXY=https://proxy.golang.org,direct go test ./... && just lint && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: PASS, and the audit at most 733.

Run: `rg -n 'ContextFiles|ContextDir|PackContextDir|ContextDirFiles|ScanContextTar|walkContextTar|BaseContextZip|BaseContextFiles|BaseContextDir|ResolveContextDir|ResolveInRoot|KitConfig\(|PushKitMeta|PushKitTracked|contexts=files' --type go`
Expected: no match outside `internal/jam/kit_migrate_legacy.go` (whose YAML key strings `"contextFiles"`/`"context-files"` are data, not code names).

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "studio, jam: delete the packed build context (base.context, context-files, context-dir)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-review

**Spec coverage.**

| Spec | Where |
|---|---|
| §1 tree entries, paths, modes, regular files only, limits | Task 1 (`CheckPath`, `NormMode`, `Build`, `Validate`); Task 2 (`ReadDir`, `ReadTar`) |
| §1 tree hash | Task 1 `TestHashEncoding` |
| §1 tables, Mem/Postgres, storetest | Task 5 |
| §1 kit doc (`Current`, `Meta`, versions → trees); `kit.yml` parsed from the tree and cached | Task 5 (`Kit.Trees`, deviation 1); Task 7 (`LoadKitVersion`, the parse cache) |
| §1 GC on delete and at serve start | Task 5 (`GCKitObjects`, `RemoveKitChecked`, config removals); Task 13 (serve start) |
| §1 billy view, used by every reader | Task 3 `NewFS`; Task 7 `KitFS`/`KitVersion.Files`. Readers: parse (Task 4/7), note files (Task 4), build tar (Task 8), diffs (Tasks 3, 11, 12), config write (Task 10), inspect (Task 11) |
| §1 `.dockerignore` on the way in | Task 2 (`ReadDir`, `Ignore`); push (Task 9), git and config (Task 10) |
| §1 build context and digest | Task 4 (`BuildContext`, digest); Task 8 (stage tag) |
| §2 push (tar to `POST /admin/kits/{name}/versions`, `unchanged`, `--config` alone) | Task 9 |
| §2 git import/track/sync, path parents, kit sets | Task 10 |
| §2 config in git (`kits/<name>/` is the dir; write copies files with modes; apply copies in; canon = tree + source; `.source.yml` unchanged; `context/`, `?contexts=files`, `binary`/`prune`, repack-exactness removed) | Task 10 (deviations 13–14); Task 14 (code deletion) |
| §2 build tar stream | Task 3 `ContextTar`; Task 8 |
| §2 diff in the kit page, CLI and plans | Task 3 `DiffText`/`Changes`; Task 11 (page); Task 12 (API, CLI, plans) |
| §2 backups | Task 6 |
| §2 inspect | Task 11 (deviation 21) |
| §4 migration (once, one transaction, log, shapes, broken + condition, stale once, legacy field, no rollback) | Task 13 |
| §5 errors | Global Constraints; tests in Tasks 1, 2, 4, 9, 10 |
| §6 tests (hashing, dedup, GC, parity, billy, round trip, migration goldens, build tar, tree diff incl. binary/large/hostile) | Tasks 1–3, 5, 10, 13. Hostile paths cannot enter a tree (Task 1–2 refusals), so the diff test covers binary and large. |
| §3 drafts, authoring loop | out of scope (slices 3–5) |

**Placeholder scan.** Every code step carries code. Where a step edits many call sites mechanically (Task 5 callers, Task 14 deletion), it lists each site and the replacement expression. Two steps tell the implementer to confirm one fact against the code before relying on it, because the plan cannot pin it without running: Task 3's exact `linediff` rendering of an unchanged line (the test pins it), and Task 11's golden test name (a `grep` is given).

**Type consistency.** `KitObjects`, `PushKitVersion(name, tree, objs, meta, track)`, `PutKitDocs(kits, objs)`, `LoadKitVersion(r, name, version) (KitVersion, bool)`, `KitVersion{TreeHash, Tree, KitYML, Files, Kit, Err}`, `kitCanon(tree, blobs, tracked)`, `kitStageTag(asm, parentRef, sk)`, `KitChainView(store, name, sk)` and `ConfigTreeExport.Kits`/`ConfigKitFile` are used with the same signatures in every task that names them.

**Review Focus.** Each of the five lines names its pinning tests; each test is in its owning task's Step 1.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-10-24-kit-trees-slice1.md`. Please review it.

Which execution approach do you want?
- **Subagent-driven:** a fresh subagent implements each task and a fresh reviewer checks it before the next starts, then a whole-branch review. This is the most thorough, at the cost of a fresh context per task and per review.
- **Native:** I implement every task in this session, then one fresh reviewer on the most capable model checks the whole branch.

I recommend **subagent-driven**. The 14 tasks hand each other exact interfaces (`KitObjects`, `LoadKitVersion`, `kitCanon`, `ConfigTreeExport.Kits`), and a mistake shipped in the store or the migration corrupts every kit on upgrade, so a gate per task earns its cost. Does the plan capture what you want, and which approach should we use?

