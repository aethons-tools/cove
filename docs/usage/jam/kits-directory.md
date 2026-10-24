---
summary: A kit version is a directory — kit.yml plus its files; a Dockerfile beside kit.yml makes it the build context; the limits, .dockerignore, how Jam stores versions as content-addressed trees.
read_when: You are laying out a kit directory (Dockerfile, scripts, note files), a push or import refused a file (symlink, limit, unsafe name, packed-context field), or you want to know what a version stores.
owns: the kit directory — files, modes, limits, .dockerignore, build context, tree storage
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
