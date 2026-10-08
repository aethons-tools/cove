---
summary: Migration note for the StudioKit rework — `runtime.launcher.install-manifest` is removed (no shim) and the kit registry is studio-only.
read_when: Your serve config still sets `runtime.launcher.install-manifest`, Jam refuses to start over it, or an existing kit-registry row now fails `kit show` or role resolution.
owns: the breaking changes of the StudioKit rework and how to migrate off them
prereqs: kits.md for the StudioKit schema; serve.md for runtime.launcher
tier: leaf
updated: 2026-09-30
---

# Migrating to StudioKits

Brokered studios now run a [StudioKit](kits.md#the-studiokit), and two things
were removed **with no compatibility shim**.

## `runtime.launcher.install-manifest` is removed

Drop the key from the serve config. `runtime.launcher` now needs only
`runtime-addr` and `jam-host` (plus optional identity/DNS/docker settings — see
[serve.md](serve.md#the-launcher-runtimelauncher)). The managed cove's base and
config now come from the studio-kit registry: the `default` kit is seeded
automatically at serve start; author others with `at-jam kit push` and bind them
with `role add --kit` ([kits.md](kits.md#role-to-kit-and-the-default-kit)).
`at-cove install` and its manifest are unaffected for interactive kits.

## The registry is studio-only

Any pre-existing **full-kit** registry row (a whole `config.yml`, including the old
auto-registered `managed` / `managed-<name>` kits) now fails `kit show` and
resolution. Re-author it as a StudioKit and `kit push` it, then point roles at the
new name.
