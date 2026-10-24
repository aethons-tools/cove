---
summary: A kit version is a directory — kit.yml plus its files; a Dockerfile beside kit.yml makes it the build context; the limits, .dockerignore, how Jam stores versions as content-addressed trees.
read_when: You are laying out a kit directory (Dockerfile, scripts, note files), a push or import refused a file (symlink, limit, unsafe name, packed-context field), or you want to know what a version stores.
owns: the kit directory — files, modes, limits, .dockerignore, build context, tree storage, the push upload and its refusals
prereqs: kits.md
tier: leaf
updated: 2026-10-24
---

# The kit directory

## The directory

A kit version is a directory: `kit.yml` plus its files.

A `Dockerfile` next to `kit.yml` makes the directory, **minus `kit.yml`**, the
build context. It is streamed to `docker build -` as a deterministic tar
(sorted, epoch mtime, fixed uid/gid), modes kept, so a script keeps its `+x`.
The Dockerfile builds `FROM ${COVE_BASE_IMAGE}` (the blessed base, or the
parent's stage for a child kit).

Because `kit.yml` is outside the build context, an edit to `kit.yml` alone
(prompt, notes with an inline body, secrets) reuses the image, while an edit to
any other file of a `Dockerfile` kit rebuilds it.

Note files (`notes[].file`) are read from the directory, relative to its root
and inside it. `base.image` and a `Dockerfile` exclude each other.

`at-jam kit push <dir>` sends the directory as an uncompressed tar to
`POST /admin/kits/{name}/versions` (admin only, `Content-Type: application/x-tar`, else 415); `kit push --config <file>`
sends `kit.yml` alone, a one-file version. A hand-made `tar -C dir -cf - .`
works: a leading `./` is dropped and directory entries are skipped.

## `.dockerignore`

A root `.dockerignore` (docker's syntax) filters what is **stored**: ignored
files are never read, sent or kept, and Jam applies it again to an upload.
`kit.yml`, `Dockerfile` and `.dockerignore` are always kept.

## Limits

- Regular files only: no symlinks, hard links, devices or FIFOs.
- Modes are `0644` or `0755`: any executable bit becomes `0755`.
- At most 2000 files, 64 MiB in total and 4 MiB a file. An upload body over
  the cap (64 MiB plus room for the tar headers) is answered 413.
- A path is relative, valid UTF-8, with no control or format character and
  no `.` or `..` segment.

## Storage

Jam stores each version as a **content-addressed tree**: one blob per distinct
file content, and a tree listing each file's path, mode and blob hash. The
tree's hash identifies the version's files; identical directories share one
tree and their blobs. A push whose tree equals *current*'s makes no version
(`unchanged`). `kit show` lists the files under `# files (tree <12 hex>):`.

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
| `<dir>: no such directory` | `kit push <dir>` cannot find it (`not a directory`, `cannot open the directory` alike). |
| `not a readable tar` | The upload is not an uncompressed tar. |
| `kit upload over <N> bytes` | The upload body is over the cap (413). |
| `base.context is no longer supported; put a Dockerfile next to kit.yml` | The old packed context; `base.context-files` and `base.context-dir` alike. |
| `studio kit: base.image and a Dockerfile next to kit.yml are mutually exclusive` | Both an `image:` base and a `Dockerfile`. |
| `note "x.md": file x.md is not in the kit` | A note's `file:` is not in the directory. |
