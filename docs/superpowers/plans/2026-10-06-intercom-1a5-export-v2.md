# intercom 1a-5: config export/import v2

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Test-first throughout.

**Goal (spec §9):** `ConfigSnapshotVersion = 2`. A backup carries the identity registry with its ids, so a restore keeps every stored reference valid:
- users, including removed ones;
- connections;
- accounts;
- memberships;
- legacy human aliases.

A v1 backup still imports, running the registry migration over it.

## Decisions
- **Export** writes projects as stored docs. People travel in the registry, not as roster humans, and the chat service as its connection id. All registry entities are sorted by id.
- **Import** (`importPlan`, shared by both stores):
  1. `checkImport` accepts version 1 or 2, and requires an empty target, registry included.
  2. `withReferencedProjects` runs.
  3. `planSnapshotRegistry` replays the registry into a scratch state through the store's own rules: live-name, login, OIDC and identity uniqueness, and references that resolve. Tombstones are kept as is. An inconsistency is `ErrInvalidConfig`.
  4. The registry migration runs over the snapshot's config, so v1 humans and kind-named chat services migrate.
- An older Jam can't read v2. This is documented: backup.md already makes the newest exporter the upgrade path.

## Tasks
1. Conformance: a v2 round trip keeps user ids, tombstones, aliases, the roster view and the chat service; a populated registry is refused; an inconsistent registry is refused.
2. `ConfigSnapshot` registry fields; `exportRegistry`; `importPlan` / `planSnapshotRegistry`; both stores' `ImportConfig` on top of them.
3. Docs: backup.md.
