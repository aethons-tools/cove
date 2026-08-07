# Refresh image-owned defaults in /agent-data on every boot

**Status:** design approved (brainstorm), pending implementation
**Date:** 2026-08-07
**Related:** the hardening entrypoint seed (`internal/assemble/hardening/image-files/usr/local/bin/entrypoint.sh`); the persistent `/agent-data` volume (`docs/OVERVIEW.md`).

## Problem

The hardening image ships agent-facing defaults under
`/home/agent/.init-agent-data` — the `skills/` tree, the `reference/` docs, and the
CLAUDE doc tree (`CLAUDE.md`, `PROGRESSIVE_DISCLOSURE.md`, `SANDBOX.md`) — plus
runtime-owned starting files. The entrypoint seeds this into the persistent
`/agent-data` volume **once**, guarded by a `.seeded` marker:

```sh
if [ ! -e /agent-data/.seeded ]; then
  cp -a /home/agent/.init-agent-data/. /agent-data/
  touch /agent-data/.seeded
fi
```

Because `/agent-data` persists across `at-cove recreate` (it holds the OAuth login,
history, settings), the marker survives, so a **rebuilt image's updated skills and
docs never reach an existing sandbox**. Concretely: shipping a new `board-attend`
skill via the image left it invisible in every already-seeded sandbox — the file
sat in the image at `/home/agent/.init-agent-data/skills/board-attend` while
`/agent-data/skills` kept the stale set. This is a papercut for exactly the
dogfooding loop (build → recreate → test the new payload).

The `.seeded` guard is intentionally broad because several seeded entries become
critical **runtime state** that must not be clobbered (see the partition below), so
the fix cannot simply drop the guard or overlay the whole tree.

## The partition

`.init-agent-data` entries fall into three classes:

| Class | Entries | Refresh policy |
|---|---|---|
| **Static defaults** (agent reads, nothing writes back) | `skills/`, `reference/`, `CLAUDE.md`, `PROGRESSIVE_DISCLOSURE.md`, `SANDBOX.md` | **Refresh every boot** |
| **Seeded-then-runtime-owned** | `.claude.json` (MCP/project state), `settings.json`, `plugins/` (manifest + downloaded cache), `COLLABORATOR.md` (at-cove injects it per-connect) | Seed once; never refresh |
| **Pure user/runtime state** (not in the image) | `.credentials.json`, `history.jsonl`, `sessions/`, `projects/`, `file-history/`, `tasks/`, `cache/`, … | Never touched by seed logic |

Only the **static defaults** (the "reference set") refresh. This solves the reported
problem (skills + docs not propagating) with zero risk to runtime state.

## Design

### Behavior

On every boot, after the existing first-boot full seed, the entrypoint re-mirrors
the reference set from the image into `/agent-data`:

- **Mirror semantics** (image is authoritative): added/changed files copied in,
  **removed/renamed entries pruned**, so an old skill name can't shadow a rename.
- **Cadence: every boot**, not version-gated. The image owns these files, so a local
  edit not surviving a reboot is the intended "change the kit, not the running copy"
  behavior — and it avoids a digest-marker mechanism.
- The three doc files are named **explicitly** (not "all top-level files"), because
  `.init-agent-data`'s top level mixes reference-set files with runtime-owned ones
  (`.claude.json`, `settings.json`, `COLLABORATOR.md`).

### Mechanism (entrypoint.sh)

```sh
SEED=/home/agent/.init-agent-data
if [ ! -e /agent-data/.seeded ]; then          # first boot: seed everything
  mkdir -p /agent-data
  cp -a "$SEED/." /agent-data/
  touch /agent-data/.seeded
fi
# Every boot: re-mirror the image-owned reference set so a rebuilt image's updates
# reach existing sandboxes. These subtrees hold no user state; runtime-owned seed
# files (.claude.json, settings.json, plugins/, COLLABORATOR.md) are NOT touched.
for d in skills reference; do
  [ -d "$SEED/$d" ] && rm -rf "/agent-data/$d" && cp -a "$SEED/$d" "/agent-data/$d"
done
for f in CLAUDE.md PROGRESSIVE_DISCLOSURE.md SANDBOX.md; do
  cp -a "$SEED/$f" "/agent-data/$f"
done
chown -R agent:agent /agent-data
```

`rm -rf` + `cp` yields the mirror (prune) semantics on deterministic, constant paths
and needs no `rsync`. It runs before sshd/the agent start, so nothing is mid-read.

### Security

Runs as root in the entrypoint, before dropping to the agent and starting sshd — the
same trust context as today's seed. The copy direction is image → volume only (a
malicious `/agent-data` cannot inject upward); paths are constants (no injection);
and the **sealed security files (nftables, squid, sshd) are not in `.init-agent-data`**
— they are applied separately — so this refresh never touches the egress/hardening
boundary. `chown agent:agent` runs after, as today. No weakening of hardening.

### Not touched (and why)

`.claude.json`, `settings.json`, `plugins/`, `COLLABORATOR.md`, and all user/runtime
state keep their first-boot seed and are owned by runtime thereafter. Making
`settings.json` or the plugin manifest a *managed* image default (refreshed) is a
plausible future extension but is deliberately out of scope here — it carries a
clobber risk and is not needed to fix the reported problem.

## Testing

- A hermetic Go test in the assemble package, following the embedded-script
  assertion pattern (e.g. `TestEntrypointStartsSSHD`): assert the entrypoint mirrors
  `skills` and `reference` and overwrites the three doc files, **and** that it does
  not refresh `.claude.json` / `settings.json` / `plugins` / `COLLABORATOR.md`.
- The new shell must be `shellcheck`-clean (CI runs `scripts/lint.sh` under STRICT).
- `just test` + `just lint` green.

## Docs

Update the seed paragraph in `docs/OVERVIEW.md`: it currently states the volume is
"seeded once (guarded by a `.seeded` marker)", which now holds only for the
runtime-owned set — the reference set (skills, reference docs, CLAUDE tree) refreshes
every boot.

## Scope

One PR: `entrypoint.sh` + one assemble test + the `docs/OVERVIEW.md` paragraph. A
single `class:implementor` unit.
