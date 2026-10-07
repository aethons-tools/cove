---
summary: Back up and restore a Jam's control-plane CONFIG (actors, roles, kits, destinations, model-specs, projects, Jam-wide session context) with `at-jam export` / `at-jam import` — a single file, excluding studio and intercom state.
read_when: You are snapshotting a Jam's config for backup, or restoring it onto a fresh/rebuilt Jam.
owns: the `at-jam export` / `at-jam import` command surface and the backup file's scope + semantics
prereqs: operators.md for signing in (`--app`/`--token`); roster.md and kits.md for what the aggregates are
tier: leaf
updated: 2026-10-06
---

# Backing up and restoring Jam config

`at-jam export` and `at-jam import` snapshot and restore the Jam **config** — the
control-plane aggregates:

- **actors** (identities, their **token hashes**, and grants)
- **roles** (scope incl. egress, allocation incl. standing sessions, session context)
- **kits** (all versions **and** the pin)
- **destinations**
- **model-specs** ([model-specs.md](model-specs.md); omitted from the file when there are none)
- **projects** (escalation policy, chat service, session context and resources)
- the **identity registry** — users (removed ones too: their ids back history),
  connections, accounts, project memberships and the legacy human aliases — with
  their ids, so a restore keeps every reference valid
- the **channel registry**: rooms (a project's channels, archived ones too)
  with their bindings and ids. Channel membership is runtime state and is not
  exported.
- the **Jam-wide session context**

The format is `version: 3`; an older Jam can't read it. Older backups still
import: a `version: 1` backup's per-project humans are merged into users the way an
upgrade does ([comms-addressing.md](comms-addressing.md#project-members-and-rooms)),
and a `version: 1` or `2` backup's project channels become rooms.

Import requires an empty target, registry included — except connections, which
a starting serve creates: a backup's connection of the same name takes over —
and checks the registry is consistent (unique live names and identities,
references that resolve). It
applies the admin API's authoring rules to session context (layer budgets,
leaf names, resources), destination notes and
[header specs](header-specs.md), and model-spec structure (credential
names are not checked — they are serve-config, not backup, state); a snapshot
that breaks them is refused with 400 and nothing is written. Older Jams didn't
check `identity_in`/`apply`, so a backup may hold a value this Jam doesn't
know: the error names the destination and field — fix it in the file
(a preset, or `custom` plus a spec) and re-import.

They deliberately **exclude** runtime/studio state (raised instances), the
intercom's log, read cursors and channel memberships, and allocator events. A backup restores
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
events in the old files are not migrated. The relay cursor/marker/receipt files
must also be moved into `state-dir`; see
[serve.md](serve.md#upgrading-relay-state) for where they were and what is lost
if you skip it.
