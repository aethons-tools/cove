---
summary: What a model-spec builds into a cove's image — the harness layer (exact CLI version + plugins, seeded and enabled), its place in the image identity — and the one-time COV-242 migration of specs stored before the version split.
read_when: You are changing a model-spec's version or plugins and want to know when coves get the new CLI/plugins, a plugin is missing or unexpectedly enabled in a cove, or Jam logged a model-spec migration warning after an upgrade.
owns: the model-spec → image harness layer (install, plugin seed and enablement, rebuild timing) and the one-time model-spec store migration (marker, rules, warnings)
prereqs: model-specs.md for the spec schema and validation
tier: leaf
updated: 2026-10-05
---

# Model-spec harness layer and migration

A spec's `type`, `version` and `claude.plugins` are **build inputs**, not
per-episode settings: they decide the image's harness layer.

## The harness layer

The image's harness layer, between the kit base and the sealed hardening
([OVERVIEW](../../OVERVIEW.md#how-the-build-context-is-assembled)), installs the
CLI with the native installer at exactly `version` and pre-seeds the plugins
(marketplace added, plugins installed at build, folded into `/agent-data`). The
raise resolves the role's spec **before** the kit image, whose build-digest
includes them ([kits.md](kits.md)) — so changing `version` or `plugins` builds a
new image at each role's next raise, and a running cove keeps its image. No
spec delivered = claude-default's install. **Enablement follows the spec, too:**
the seed enables exactly the installed plugins in the first-boot user settings
(for interactive sessions), and each episode's `--settings` enables the spec's
plugins ([model-specs.md](model-specs.md#what-a-cove-applies)); the sealed managed settings enable none. `plugins: []`
installs and enables nothing, so nothing is auto-installed at runtime.

## The one-time migration (COV-242)

A one-time store migration, recorded by a marker
(`jam_settings` key `model_spec_schema`) so it never re-runs, rewrites **every**
stored spec at the first `at-jam serve` startup after the upgrade — and on
[import](backup.md) of a backup taken before it:

- a non-exact `version` becomes `DefaultClaudeVersion`; the old range is kept as
  `version-constraint` when it admits the pin (`>=2.0.0`, `2.x`, `*`), so coves
  on images built before the upgrade keep passing their check. A range that
  doesn't (`2.0.x`, `>=3.0.0`) becomes the pin, with a WARN naming the spec;
- plugin ids that are malformed or name an unknown marketplace are dropped
  (WARN naming the spec and plugin), as are `enabledPlugins` /
  `extraKnownMarketplaces` in `claude.settings`;
- a spec left with no plugins gets claude-default's — every image carried them
  before. After the marker, an explicit `plugins: []` is kept as written.
