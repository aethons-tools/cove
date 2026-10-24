# Kits as directory trees, and the kit authoring loop

**Status:** design approved in brainstorming (2026-10-24); spec, pre-plan.
**Owner:** Jam service (admin-spider).
**Replaces:** the packed build context (`base.context`, `context-files`,
`context-dir`) of [`2026-09-30-studio-kit-standalone-design.md`](2026-09-30-studio-kit-standalone-design.md)
and the context handling in the kit git, kit inheritance and config-in-git
specs. Builds on [`kits.md`](../../usage/jam/kits.md).

## Summary

A kit version is a **directory**: `kit.yml` plus whatever files sit next to
it. If there is a `Dockerfile` beside `kit.yml`, the directory is the build
context. Jam stores each version as a content-addressed tree in its own
database and reads it through a read-only go-billy filesystem. Push, git
import, config in git, build, diff, export and inspect all work on that one
shape. The packed base64 tar.gz and the machinery around it (repack-exactness,
`context/` subdirs, base64 diffs) go away. On top of trees, a per-kit
**draft** gives an authoring loop: edit files in place, test-build, try in a
studio, publish a version.

## Decisions (brainstorming record)

1. **Goal: both.** Simplify the internals first, built so the authoring loop
   follows.
2. **Storage: Postgres** (and MemStore), content-addressed. A bucket can come
   later if contexts outgrow it.
3. **No `context` field.** A `Dockerfile` next to `kit.yml` makes the
   directory the build context. `context`, `context-files` and `context-dir`
   are refused on push.
4. **A version is the whole directory**, `kit.yml` included.
5. **Migration: one-time, at serve start**; digests change once.
6. **Scope: specify the full loop now**; build it in slices.
7. **`.dockerignore` filters the upload**: ignored files are never stored.

## 1. Model and storage

**Tree.** A version's tree is a set of entries `{path, mode, sha256}`:
- `path`: clean, slash-separated, relative, valid UTF-8, no control or format
  characters, no `.`/`..` segments; `kit.yml` must exist at the root.
- `mode`: `0644` or `0755` only (the two git keeps); any executable bit on
  input becomes `0755`, otherwise `0644`.
- Regular files only: no symlinks, hardlinks, devices or directory entries.
- Limits: ≤ 2000 entries, ≤ 64 MiB total, ≤ 4 MiB per file.

**Tree hash.** `sha256` over the entries sorted by path, each encoded as
`path\0mode-octal\0blob-sha256\n`. Identical directories share one tree.

**Tables** (Postgres; MemStore maps; storetest conformance):
- `kit_blobs (sha256 PK, size, bytes)`: shared by every kit and version.
- `kit_trees (sha256 PK, entries jsonb)`.
- The kit doc keeps `Current`, `Meta` (pushed at, source, parent) and
  `Versions[n] = tree sha256` in place of YAML text. The parsed definition
  (`kit.yml`) is derived from the tree and cached in memory.
- Blob/tree GC: a blob or tree no live version or draft references is
  deleted on version delete and at serve start.

**Billy view.** `jam.KitFS(store, treeSHA) billy.Filesystem`: read-only
(`Open`, `Stat`, `Lstat`, `ReadDir`; writes return `billy.ErrReadOnly`).
Every reader of kit files uses it: the `kit.yml` parse, `file:` notes, the
build tar stream, diffs, `config write`, setup inspect.

**`.dockerignore`.** Applied on the way in (push, git import, config apply,
draft sync): ignored files are never stored. `kit.yml`, `Dockerfile` and
`.dockerignore` are always kept. Comparing a checkout with a stored version
means "the checkout after `.dockerignore`".

**Build context and digest.** The build context is the tree minus `kit.yml`.
`BuildDigest` covers the tree hash of that context, the image-affecting
definition fields (build-args, the parent chain's stages) and the harness, so
editing only `prompt` or notes in `kit.yml` does not rebuild the image.

## 2. Flows on trees

- **Push.** `at-jam kit push <dir> [--name n]` reads `<dir>/kit.yml`, applies
  `.dockerignore`, and uploads the files (a tar body to
  `POST /admin/kits/{name}/versions`; the server unpacks it into blobs and a
  tree under the limits above). A tree equal to `current` answers
  `unchanged`. `kit push --config kit.yml` alone pushes a one-file tree.
- **Git import / track / sync.** The fetched checkout's kit directory is
  copied (after `.dockerignore`) into a tree. Path parents and kit sets work
  as before, each kit being its own directory.
- **Config in git.** `kits/<name>/` is the kit directory itself.
  `config write` copies the tree's files out with their modes; apply copies the
  checkout's directory in. A kit's canonical form for plans is its tree hash
  plus its tracked source. `.source.yml` references are unchanged. The
  `context/` subdir, `?contexts=files`, `binary`/`prune` and the
  repack-exactness check are removed.
- **Build.** The launcher streams a tar of the build context (deterministic:
  sorted, epoch mtime, fixed uid/gid) to `docker build -`; a child still builds
  `FROM ${COVE_BASE_IMAGE}`, its parent's stage.
- **Diff.** A version diff is the `kit.yml` line diff followed by a file list
  (added, removed, modified with line diff, mode). The kit page, the CLI and
  config plans all use it; no base64 is shown anywhere.
- **Backups.** `at-jam export` carries trees and blobs (base64 in the JSON);
  `import` restores them.
- **Inspect.** The `kit` and `seed` sections read through the billy view.

## 3. The authoring loop (later slices)

- **Draft.** At most one per kit: a mutable tree opened from `current` or a
  chosen version. Writes are validated (limits, paths); an invalid state may
  be saved while editing but blocks publish, with the problems listed.
  Concurrency uses the draft tree hash as an ETag: a save against a stale ETag
  is refused, never merged silently.
- **API.** `POST /admin/kits/{name}/draft` (open, from a version),
  `DELETE …/draft` (discard), `GET …/draft` (status, entries),
  `GET|PUT|DELETE …/draft/files/{path}`, `POST …/draft/publish`.
- **CLI.** `at-jam kit draft open|status|get|put|rm|discard|publish` and
  `kit draft sync <dir>` (mirror a local directory into the draft).
- **UI.** A **Draft** tab on the kit page: file tree; text editor for UTF-8
  files ≤ 1 MiB, upload/replace otherwise; add, rename, delete; diff vs the
  base version.
- **Test build.** `kit draft build` / **Test build**: build the draft's image
  with the real chain, harness and hardening; stream the log; report the
  result and digest; tag `cove-kit-draft:<kit>-<tree[:12]>`, never `current`.
- **Draft studio.** `at-jam studio raise --kit-draft <kit>` (admin only,
  ephemeral manual studio) runs the draft without publishing; its setup
  inspect shows `kit (draft @<tree[:7]>)`.
- **Publish.** `kit draft publish [--pin]` makes the draft tree the next
  version (`Meta.Source = draft`) and closes the draft; followers go stale as
  usual. For a config-managed kit, publish carries the drift warning and the
  UI offers "write to config repo" (the files to commit) instead.

## 4. Migration

At serve start, once, in one transaction, logged `kits migrated to trees`:
- `base.context` → its files at the tree root (tar read with the existing
  safety checks); `context-files` → files; `kit.yml` = the stored definition
  re-rendered without context fields; a kit with no context → `kit.yml` only.
- A version whose context cannot be read is kept as a **broken** version
  (raising it fails closed) and raises the attention condition
  `kit.migrate:<name>`; the rest of the migration proceeds.
- Build digests change, so running studios show `image=stale` once and
  rebuild on upgrade.
- The pre-migration doc is kept in a `legacy` field for one release, then
  dropped. Rolling back to an older Jam after migration is not supported; the
  release notes say so.

## 5. Errors

Fixed text, path-located, never echoing file contents, e.g.
`kits/app/run.sh: symlinks are not allowed`,
`kit app: kit.yml missing at the root`,
`kit app: 2001 files (limit 2000)`,
`base.context is no longer supported; put a Dockerfile next to kit.yml`,
`draft app changed since you loaded it (reload)`.

## 6. Testing

- Tree hashing, dedup and GC; Mem/Postgres parity (storetest).
- Billy view conformance (read paths, read-only writes).
- Push/git/config round trip: write → commit → clone → apply is `unchanged`,
  including modes and `.dockerignore`.
- Migration goldens from the three old shapes and a broken context.
- Build tar stream equals the tree minus `kit.yml`, deterministic.
- Tree diff (each change kind, binary and large files, hostile paths).
- Later slices: draft ETag conflicts, validation, publish, test build
  (`runner.Fake`), draft studio raise.

## Slices

1. **Trees.** Storage, billy view, push, git import/track/sync, config
   write/apply, build, export/import, the migration; delete the packed-context
   code.
2. **Diff everywhere.** Tree diff in the kit page, CLI and config plans
   (may fold into slice 1).
3. **Drafts.** API, CLI, `kit draft sync`, publish.
4. **Draft UI.** File tree, editor, diff vs base.
5. **Test build and draft studios.**

## Out of scope

- An object store for blobs (later, if needed).
- Multi-user draft merging (ETags refuse instead).
- Kit history beyond versions (no per-file history in a draft).
