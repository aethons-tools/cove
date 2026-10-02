---
summary: Back up and restore a Jam's control-plane CONFIG (actors, roles, kits, destinations, projects) with `at-jam export` / `at-jam import` — a single file, excluding studio and intercom state.
read_when: You are snapshotting a Jam's config for backup, or restoring it onto a fresh/rebuilt Jam.
owns: the `at-jam export` / `at-jam import` command surface and the backup file's scope + semantics
prereqs: operators.md for signing in (`--app`/`--token`); roster.md and kits.md for what the aggregates are
tier: leaf
updated: 2026-10-02
---

# Backing up and restoring Jam config

`at-jam export` and `at-jam import` snapshot and restore the Jam **config** — the
five control-plane aggregates:

- **actors** (identities, their **token hashes**, and grants)
- **roles** (scope incl. egress, allocation incl. standing sessions)
- **kits** (all versions **and** the pin)
- **destinations**
- **projects** (roster, escalation policy, chat service)

They deliberately **exclude** runtime/studio state (raised instances), intercom
unread cursors, the intercom squawk log, and allocator events. A backup restores
*who can reach what*, not *what is currently running*.

## Export

```
at-jam export [--format json|yaml] [FILE|-]
```

Writes the snapshot to `FILE` (or stdout when `FILE` is omitted or `-`). Default
format is **JSON**; `--format yaml` emits YAML.

```
at-jam export backup.json
at-jam export --format yaml - > backup.yaml
```

> **The backup contains token hashes** — it is authentication material. A restored
> Jam accepts every actor's existing token unchanged, which is the point, but it
> also means the file must be protected like a credential store. `export` writes
> files with `0600` permissions; keep them that way.

## Import

```
at-jam import [--format json|yaml] <FILE|->
```

Restores a snapshot into a **fresh** Jam. Import is **fail-closed**: if the target
already holds any config, it refuses and writes nothing (so it can never
half-merge or clobber a running server). Format is sniffed from the content when
`--format` is omitted.

```
at-jam import backup.json
```

A backup taken before projects were first-class may have roles or grants that
name a project it has no record for. Import adds an empty record for each one
(see [projects.md](projects.md#upgrading-an-existing-jam)).

Because existing tokens keep working after a restore, there is nothing to
re-hand-out: point the studios at the restored Jam and they authenticate as
before.

## Why server-side

Only the Jam server can read and write the stored token hashes, so export/import
are bulk admin endpoints (`GET`/`POST /admin/config`) that the CLI streams to and
from — not a client-side replay over the per-aggregate endpoints (which would
re-mint every token). The wire is JSON; YAML exists only at the file boundary.

## Upgrading a file-store Jam

Jam is Postgres-only. For a Jam that ran on the old file store, export/import is
the **only** upgrade path: run `at-jam export` with the old version, then
`at-jam import` into a Jam running on `store-postgres`
([serve.md](serve.md#postgres-store-store-postgres)). Squawk history and session
events in the old files are not migrated, and the relay cursor/marker/receipt
files are handled via `state-dir` ([serve.md](serve.md#the-serve-config)).
