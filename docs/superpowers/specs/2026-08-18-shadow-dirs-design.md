---
kind: design-spec
subject: shadow-dirs — overmount transient work dirs on a shared-repo collaborator workspace
status: approved
date: 2026-08-18
read-when: implementing or reviewing the shadow-dirs feature (VM-local overmounts of .venv/node_modules/etc. on a share-repo-dir collaborator class)
---

# `shadow-dirs`: VM-local overmounts for shared-repo collaborator sandboxes

## Problem

A collaborator class with `share-repo-dir: true` realizes the workspace as a Docker
**host bind-mount** — `internal/backend/colima/colima.go` emits
`<hostPath>:/home/agent/workspace`. Host and sandbox therefore share one inode tree.

Transient work directories with **absolute-path or platform-specific content** corrupt
each other across that boundary: `.venv` (interpreter shebangs + compiled wheels),
`node_modules` (native addons, platform binaries), Rust/Go `target`/build trees. The
host built them for its own paths/arch; the sandbox writes over them for
`/home/agent/workspace`; both break.

## Solution

A per-class `shadow-dirs` list. Each named subpath of the shared workspace is overmounted
with a **persistent per-sandbox named volume**, so the sandbox gets its own VM-local copy
of that directory. The copy:

- is invisible to the host (the host's copy is shadowed, never touched);
- survives `recreate` (it is a named volume kept like `/agent-data` and the workspace);
- is purged only by a real `destroy`.

The overmount is composed at the **`docker run` layer** (an extra `-v` per dir), not via a
`mount(2)` inside the VM. That needs no capability grant — the hardening deliberately
withholds `CAP_SYS_ADMIN` and bans `--privileged`/`--security-opt` — and it fits the
existing plan→execute→teardown split. tmpfs/named-volume overmounts are strictly *more*
isolating (they hide host state from the sandbox), so they do not weaken the security
boundary.

### Scope

**In scope:** the `shadow-dirs` config field, its validation, the overmount mechanism, and
teardown.

**Deferred (follow-up ticket):** an `at-cove doctor` command that reads `.gitignore` +
build manifests and *recommends* a `shadow-dirs` block. Until then authors hand-write the
list; the config doc shows the common set.

**Explicitly rejected:** driving the shadow set automatically from `.gitignore`.
`.gitignore` answers "what git shouldn't track," a **superset** of "what breaks when shared
and should be VM-local." Wired directly it would (a) hide files the collaborator wants —
`.env`, `.envrc`, `secrets/`, local `.claude/` config are commonly gitignored, and
overmounting them with an empty volume makes the host's copy invisible in the sandbox;
(b) require reimplementing `git check-ignore` pathspec/negation/nested semantics to turn
patterns and file-globs into concrete directory paths; (c) over-shadow, since many
gitignored dirs (text logs, path-independent caches) share fine. `.gitignore` is instead
the ideal *input to `doctor`'s suggestion*, with a human as the gate.

## Config surface

A sibling of `share-repo-dir` on the `Collaborator` struct (`internal/kit/config.go`):

```yaml
collaborators:
  human:
    share-repo-dir: true
    shadow-dirs: [.venv, node_modules]
```

### Validation (`internal/kit/config.go`, TDD-first)

- **Rejected on `<common>`** — like `share-repo-dir` (extends the existing base-class check
  that rejects `prompt`/`default`/`share-repo-dir` on `<common>`).
- **Rejected unless the class has `share-repo-dir: true`** — a shadow with no shared bind is
  meaningless.
- **Each entry is a clean relative path under the workspace:** non-empty, not `.`, no
  leading `/` (not absolute), no `..` escape. This parse-time check is *also* the security
  guard for the entrypoint chown (below).
- **Reject duplicates** and any two entries that **sanitize to the same volume name** (see
  naming).
- **Not unioned from `<common>`** — own-list only, declared alongside `share-repo-dir` on
  each class.

## Architecture & data flow

The list threads through the existing plan→execute→teardown path; each new piece sits
beside a field that already works this way.

1. **Types grow one field each (`internal/backend/backend.go`):**
   - `WorkspaceMount` gains `ShadowDirs []string`, meaningful only when `Mode == Shared`
     (mirrors `HostPath`, which is set iff Shared).
   - `VolumeSet` gains `Shadow []string`, the shadow volume names, alongside
     `State`/`Workspace`/`Docker` so teardown already knows about them.

2. **Populate (`cmd/at-cove` create path):** when building the Shared `WorkspaceMount`,
   copy the resolved class's `shadow-dirs` into `ShadowDirs`.

3. **Naming (`internal/naming/naming.go`):** add
   `ShadowVolume(container, dir) -> atcove-{kit}-{class}-shadow-{sanitized}` (path `/`→`-`,
   strip leading `.`), following the `WorkspaceVolume`/`DockerVolume` pattern. Config
   validation guarantees no two entries collide here.

4. **Emit (`internal/backend/colima/colima.go` `Create`):** for each `ShadowDirs` entry,
   guarded by `Mode == Shared`:
   - append `-v {ShadowVolume(name)}:/home/agent/workspace/{dir}` to the run argv — Docker
     orders mounts parent-before-child, so the bind is established before each overmount;
   - record the name in `vols.Shadow`;
   - pass the list to the container via one `-e COVE_SHADOW_DIRS="{space-joined dirs}"`
     (same signalling idiom as `COVE_DOCKER=1`).

5. **Teardown (`Destroy`):** append `inst.Volumes.Shadow...` to the `docker volume rm -f`
   argv (best-effort, like the other volumes). `recreate` passes `keepVolumes=true`, so
   shadow volumes **persist** across recreate; only a real `destroy` purges them.

### The ownership gotcha (why the entrypoint must change)

A fresh named volume mounts **empty and `root:root`**. Docker seeds an empty volume's
ownership from whatever exists at the container path in the image — but
`/home/agent/workspace/.venv` does not exist in the image, so the volume stays root-owned
and the non-root `agent` cannot write it → `uv`/`npm` fail on first boot.

Fix, in the **hardening entrypoint** (`internal/assemble/hardening/image-files/usr/local/bin/entrypoint.sh`),
in the common prologue **before** the `COVE_DOCKER` branch (so both the systemd and sshd
paths get it), beside the existing `chown -R agent:agent /agent-data`:

```bash
for d in ${COVE_SHADOW_DIRS:-}; do
  # defense-in-depth: skip anything that escapes the workspace (config already
  # rejects '..'/absolute at parse time; this is a boundary-file backstop).
  case "$d" in */../*|../*|*/..|..) continue ;; esac
  chown agent:agent "/home/agent/workspace/$d"   # non-recursive: mountpoint only
done
```

Non-recursive is enough and cheap: on first boot the dir is empty; on later boots its
contents were written by `agent`. Unset `COVE_SHADOW_DIRS` → zero iterations.

**Security-boundary note (per AGENTS.md):** this edits a sealed hardening file, so it must
stay safe. It adds no capability, touches no nftables/squid/sshd, and the overmounts hide
host state from the sandbox (strictly more isolation). The chown targets are constrained to
`/home/agent/workspace/$d` where `$d` is a kit-authored, parse-time-validated relative path;
the loop additionally skips any path that escapes the workspace.

## Testing (hermetic, TDD-first)

- **`config_test.go`** — `shadow-dirs` on `<common>` rejected; set without
  `share-repo-dir:true` rejected; bad entries rejected (`..`, absolute, `.`, empty,
  duplicate, collide-on-sanitize); a valid list survives `ResolvedCollaborator` and is
  **not** merged from `<common>`.
- **`naming_test.go`** — `ShadowVolume` sanitization
  (`node_modules`→`…-shadow-node_modules`, `.venv`→`…-shadow-venv`,
  `foo/bar`→`…-shadow-foo-bar`) and uniqueness.
- **`colima_test.go`** — a Shared mount with `ShadowDirs` emits one
  `-v …:/home/agent/workspace/<dir>` per entry at the right target, one
  `-e COVE_SHADOW_DIRS=…`, and records `VolumeSet.Shadow`; a Shared mount with no
  shadow-dirs is byte-for-byte unchanged; `Destroy` includes the shadow volumes in
  `volume rm`; a defensive check that `ShadowDirs` on a non-Shared mount emits nothing.
- **`embed_test.go`** — assert the embedded `entrypoint.sh` contains the
  `COVE_SHADOW_DIRS` chown loop (this file already string-asserts entrypoint content).

## Docs (same change, per AGENTS.md; route via docs-author)

- `docs/usage/at-cove-config.md` owns the `config.yml` schema including `collaborators`; add
  the `shadow-dirs` field (rules + the common example list). Its `INDEX.md` row already
  covers "every field," so no INDEX churn.
- A one-line pointer where `docs/OVERVIEW.md` describes the shared-workspace tradeoff,
  linking to the config field — no duplicated prose.

## Edge cases (stated, not surprises)

- Removing a dir from `shadow-dirs` later stops emitting its `-v` on next `recreate` (the
  host dir shows through again); the orphaned volume lingers until the next real `destroy`.
  Acceptable, documented.
- `shadow-dirs` is a collaborator/`share-repo-dir` concept only — Isolated workers already
  get a private workspace volume, so it is N/A there (enforced by validation).
- Orthogonal to `docker:true`/Sysbox — just more `-v`/`-e` flags; the chown sits in the
  shared prologue that runs before the systemd handoff.

## Follow-up ticket (not implemented here)

`at-cove doctor` — recommend `shadow-dirs` from `.gitignore` + build manifests. Reads the
repo's `.gitignore` and detects `pyproject.toml`→`.venv` / `package.json`→`node_modules` /
`Cargo.toml`→`target`, filters to dirs that exist with platform-specific content, and prints
a paste-ready `shadow-dirs` block for the author to review. `.gitignore` informs the
suggestion; the human stays the gate.
