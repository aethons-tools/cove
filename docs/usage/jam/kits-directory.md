---
summary: A kit version is a directory — kit.yml plus its files; a Dockerfile beside kit.yml makes it the build context; the limits, .dockerignore, how Jam stores versions as content-addressed trees.
read_when: You are laying out a kit directory (Dockerfile, scripts, note files) in the config repo, a config plan, draft or import refused a file (symlink, limit, unsafe name, packed-context field), or you want to know what a version stores.
owns: the kit directory — files, modes, limits, .dockerignore, build context, tree storage, the one-time migration from packed contexts, and its refusals
prereqs: kits.md
tier: leaf
updated: 2026-11-01
---

# The kit directory

## The directory

A kit version is a directory: `kit.yml` plus its files. In the config repo it
is `kits/<name>/` ([config-git.md](config-git.md#layout)); a commit that
changes it makes the next version ([kits.md](kits.md#the-model)).

A `Dockerfile` next to `kit.yml` makes the directory, **minus `kit.yml`**, the
build context. It is streamed to `docker build -` as a deterministic tar
(sorted, epoch mtime, fixed uid/gid), modes kept, so a script keeps its `+x`.
The Dockerfile must start `ARG JAM_STUDIO_BASE_IMAGE` then `FROM ${JAM_STUDIO_BASE_IMAGE}`
(docker substitutes a build arg in `FROM` only when it is declared first), so
it builds on the blessed base, or on the parent's stage for a child kit.

Because `kit.yml` is outside the build context, an edit to `kit.yml` alone
(prompt, notes with an inline body, secrets) reuses the image, while an edit to
any other file of a `Dockerfile` kit rebuilds it.

Note files (`notes[].file`) are read from the directory, relative to its root
and inside it. `base.image` and a `Dockerfile` exclude each other.

## `.dockerignore`

A root `.dockerignore` (docker's syntax) filters what is **stored**: ignored
files are never read, sent or kept.
`kit.yml`, `Dockerfile` and `.dockerignore` are always kept. A kit draft applies it on
every write too ([kits-drafts.md](kits-drafts.md)).

## Limits

- Regular files only: no symlinks, hard links, devices or FIFOs.
- Modes are `0644` or `0755`: any executable bit becomes `0755`.
- At most 2000 files, 64 MiB in total and 4 MiB a file.
- A path is relative, valid UTF-8, with no control or format character and
  no `.` or `..` segment.

## Storage

Jam stores each version as a **content-addressed tree**: one blob per distinct
file content, and a tree listing each file's path, mode and blob hash. The
tree's hash identifies the version's files; identical directories share one
tree and their blobs. A new version whose tree equals *current*'s is not made
(`unchanged`). `kit show` lists the files under `# files (tree <12 hex>):`.

## Stored as trees

Jam keeps the trees and blobs in its own database (`kit_trees`, `kit_blobs`),
shared by every kit and version. Unreferenced ones are dropped when a kit is
removed and at serve start.

**Upgrading.** The first serve start after the upgrade migrates every kit
once, in one transaction, and logs `kits migrated to trees`:

- A packed `base.context` becomes files at the root, modes kept.
- `context-files` become files, the `dockerfile` key becoming `Dockerfile`,
  each `0644`.
- `kit.yml` becomes the stored definition re-rendered without the context
  fields. The context's own `.dockerignore` applies, as it did to the build.

The migration holds every distinct file of every old context in memory at once,
until its transaction commits: allow for their unpacked size.

Build digests of kits with a context change once, so running studios show
`image=stale` once and rebuild on their next raise. Kits without one, the
default kit included, keep their version and image. A config-managed kit's
mark moves with it, so the next plan shows it unchanged.

A version whose context cannot be read (not base64, not a tar.gz, a link or
unsafe path, over the limits) is kept **broken**: raising it fails with the
reason, and the attention condition `kit.migrate:<name>` is open while it is
current ([when it clears](monitoring.md)). Push the kit as a directory, or pin a version that migrated. A stored
definition that no longer parses keeps its text, minus the context fields, as
`kit.yml`.

**A config repo written by an older Jam** still holds `kits/<n>/kit.yml`
with `base.context-dir` and the files under `kits/<n>/context/`. `config
diff`, `apply` and `sync` refuse it (the context-field error below, followed by
`; a config repo written by an older Jam: run jam config write <repo> and
commit`). After the upgrade, run `jam config write <repo>` once and commit:
it rewrites each `kit.yml` without the field and mirrors the migrated files to
the kit's root, removing `context/`.

The pre-migration text is kept in the kit's `legacy` field for one release.
**Rolling back to an older Jam after the migration is not supported.**

## Refusals

Errors are fixed text, located by path, and never echo file contents or an
unsafe name:

| Error | Cause |
|---|---|
| `<path>: symlinks are not allowed` | A symlink in the directory, e.g. `kits/app/run.sh` (`hard links are not allowed` and `not a regular file` alike). |
| `kit app: kit.yml missing at the root` | No `kit.yml` at the directory's root. |
| `kit app: 2001 files (limit 2000)` | Too many files after `.dockerignore` (from the CLI, `<dir>: …`). |
| `<path>: more than 4 MiB (limit 4 MiB per file)` | One file is too big. |
| `more than 64 MiB in total (limit 64 MiB)` | The files together are too big. |
| `<dir>: a file has an unsafe path (…)` | A name with a control or format character, invalid UTF-8, or a `.`/`..` segment; the name is not shown. |
| `<path>: appears twice` | A tar names one file twice. |
| `base.context is no longer supported; put a Dockerfile next to kit.yml` | The old packed context (refused by a config plan, a draft and a git import); `base.context-files` and `base.context-dir` alike. In a config repo, [rewrite it](#stored-as-trees). |
| `studio kit: base.image and a Dockerfile next to kit.yml are mutually exclusive` | Both an `image:` base and a `Dockerfile`. |
| `note "x.md": file x.md is not in the kit` | A note's `file:` is not in the directory. |
