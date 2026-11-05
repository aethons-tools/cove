# jam-colima: rename, VM profile, host health, disk recovery, kit-prepare progress

Status: approved by the user 2026-11-05 (build cache moved last in the cleanup order).

## Goal

The Studio Manager host binary `jam-studio-manager` becomes `jam-colima`, the
Colima implementation of a Studio Manager. Every manager reports its host's
memory and disk to Jam, recovers disk space by itself, and reports progress
while it prepares kit images. Everything except the Colima primitives is
generic: the wire ops, Jam's handling, the cleanup policy and the progress
tracker apply to any manager implementation.

"Studio Manager" stays the concept: `jam manager …`, the
`jam.studio.manager` labels, container and volume names and the gRPC service
are unchanged.

## 1. Rename

- `cmd/jam-studio-manager` → `cmd/jam-colima`; release archive, `install.sh`,
  `jam update`, justfile and goreleaser ship `jam-colima`.
- `install.sh` and `jam update` warn about a leftover `jam-studio-manager` in
  the install directory (like the `at-*` notice).
- Config dir `$XDG_CONFIG_HOME/jam-colima` (else `~/.config/jam-colima`). At
  `run`, if the new dir is missing and `…/jam-studio-manager` exists, it is
  renamed to the new dir (one log line). The identity key moves with it, so
  image fingerprints and state are kept.
- The package `internal/studiomanager` keeps its name: it is the generic
  manager host runtime; Colima wiring lives beside it.
- Docs: `docs/usage/studio-manager.md` → `docs/usage/jam-colima.md`, with an
  "Upgrading from jam-studio-manager" section; old superpowers plans/specs are
  history and are not rewritten. `renamed_test.go` guards the old name.

## 2. VM profile

- `colima.profile` (default `default`) names the Colima VM; it replaces
  `colima.docker-context`. The docker context is derived: `colima` for
  `default`, else `colima-<profile>`. A config with `docker-context` is
  refused: `colima.docker-context is replaced by colima.profile (the VM name;
  context colima-<profile>)`.
- `jam-colima colima setup-docker|check-docker` take `--config <path>` (read
  the profile from it; default config path if it exists) or `--profile`, and
  edit/check that VM's `<colima home>/<profile>/colima.yaml` and context.

## 3. Host health (generic)

- New unsolicited manager→Jam frame `OpStatus` (frame id 0), JSON
  `wire.StatusReport{Host *jam.HostStats, Activities []jam.ManagerActivity}`; no proto
  change. Sent after Welcome, every 30s, and (throttled to ≤1/s) whenever the
  activity list changes.
- `jam.HostStats{At, MemTotal, MemUsed, DiskTotal, DiskUsed, Reclaimable
  {Images, BuildCache, Volumes}, LastCleanup *CleanupRecord{At, Freed, Reason,
  Err}}` (bytes). Optional launcher-side interface `HostReporter`
  (`HostStats(ctx)`); a manager without it reports no host.
- Jam keeps the last report in `jam.ManagerStatus` (`Host`, `Activities`).
  Shown on the admin Managers panel (memory, disk with a bar, reclaimable,
  last cleanup), in `jam manager list` (`mem=… disk=…`), and on `/metrics`
  (`jam_manager_memory_bytes{manager,kind=used|total}`,
  `jam_manager_disk_bytes{…}`, `jam_manager_reclaimable_bytes{manager,kind}`).
- Attention condition `manager.disk:<name>`, warning at ≥90% disk, critical at
  ≥95%; cleared below 88% (hysteresis) or when the manager is revoked.
- Colima: `colima ssh --profile <p> -- sh -c 'df -kP /var/lib/docker;
  cat /proc/meminfo'` (MemTotal − MemAvailable = used) plus
  `docker system df --format json` for reclaimable.

## 4. Disk recovery (generic policy, Colima primitives)

- Config `cleanup: {high: 85, low: 70, critical: 95, daily: true}` (percent of
  VM disk). Checked with every stats sample.
- Over `high`: run the steps in order, re-measuring after each, stopping once
  under `low`:
  1. dangling images;
  2. unused `jam-kit*` images (no container uses them, not mid-build), oldest
     first, one at a time;
  3. anonymous unused volumes;
  4. the build cache.
- Daily light pass (when `daily`): steps 1 and 3 only.
- Never removed: named volumes (studio state), images or containers in use,
  non-Jam named images. Dangling images, anonymous volumes and build cache are
  VM-wide garbage by definition; on a shared VM removing an unused kit image
  may make another manager rebuild it (time, never data).
- At ≥`critical`, raises are declined (`disk full (<n>%)`) so Jam places them
  elsewhere, and a cleanup runs at once.
- Each run is recorded in `HostStats.LastCleanup` and logged.
- Optional interface `HostCleaner` (`PruneDangling`, `KitImages`,
  `RemoveImage`, `PruneVolumes`, `PruneBuildCache`).

## 5. Kit-prepare progress (generic)

- `jam.ManagerActivity{ID, Kind ("prepare-kit"|"build-kit-draft"|"cleanup"),
  Kit KitRef, Step, Detail, Started}` (`jam.Activity` was taken); a
  manager-side tracker the launcher reports into. PrepareKit reports
  `assembling`, `resolving base`, `building stage i/n` for chains, and
  `building` / `step k/m` parsed from `--progress=plain` output (`#N [k/m] …`).
- Jam stores activities per manager (from `OpStatus`). Shown:
  - session line: a raising/recovering session whose manager has a
    prepare-kit activity for its kit reads `is preparing its kit image —
    <step>` (card pill `preparing kit`);
  - standing-session upgrade state `preparing: <step>`;
  - the Managers panel "Activity" column.

## Delivery

Four stacked PRs: rename+profile → host stats → cleanup → progress. Each is
reviewed before its PR; none merged without the user's OK.
