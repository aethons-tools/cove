---
summary: Migration note for the StudioKit rework — `runtime.launcher.install-manifest` was removed (no shim; the whole `runtime.launcher` block has since gone too) and the kit registry is studio-only.
read_when: Your serve config still sets `runtime.launcher` or `runtime.launcher.install-manifest`, Jam refuses to start over it, or an existing kit-registry row now fails `kit show` or role resolution.
owns: the breaking changes of the StudioKit rework and how to migrate off them
prereqs: kits.md for the StudioKit schema; studio-managers.md for the block that replaced runtime.launcher
tier: leaf
updated: 2026-11-01
---

# Migrating to StudioKits

Brokered studios now run a [StudioKit](kits.md#the-studiokit), and two things
were removed **with no compatibility shim**.

## `runtime.launcher.install-manifest` is removed

History: this key was dropped from the serve config, and `runtime.launcher` itself is now refused (see [Studio Managers](studio-managers.md#retiring-the-in-process-launcher)). The managed cove's base and
config now come from the studio-kit registry: the `default` kit is seeded
automatically at serve start; author others as `kits/<name>/` in the config
repo and bind them with a role's `kit:` ([kits.md](kits.md#role-to-kit-and-the-default-kit)).

## The registry is studio-only

Any pre-existing **full-kit** registry row (a whole `config.yml`, including the old
auto-registered `managed` / `managed-<name>` kits) now fails `kit show` and
resolution. Re-author it as a StudioKit, commit it to the config repo, then
point roles at the new name.
