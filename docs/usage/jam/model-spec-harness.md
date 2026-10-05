---
summary: What a model-spec builds into a cove's image — the harness layer (exact CLI version + plugins, seeded and enabled, plus Claude Code's managed settings), which Claude settings are sandbox policy vs. claude-default preferences, its place in the image identity — and the one-time model-spec store migration (COV-242 version split, COV-245 claude-default preferences).
read_when: You are changing a model-spec's version or plugins and want to know when coves get the new CLI/plugins, a plugin is missing or unexpectedly enabled in a cove, you need to know where a Claude setting (theme, remote control, permissions default, …) comes from in a cove, or Jam logged a model-spec migration warning after an upgrade.
owns: the model-spec → image harness layer (install, plugin seed and enablement, managed settings, rebuild timing), the managed-settings vs. preference classification, and the one-time model-spec store migration (marker, steps, rules, warnings)
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
plugins ([model-specs.md](model-specs.md#what-a-cove-applies)); the managed settings enable none. `plugins: []`
installs and enables nothing, so nothing is auto-installed at runtime.

## Managed settings vs. preferences (COV-245)

Claude Code's **managed settings** (`/etc/claude-code/managed-settings.json`,
root-owned `0644`; payload `internal/harnessinstall/payload/claude/`) are
installed by the harness layer's claude stage, not the sealed hardening layer:
they are Claude-specific. A kit still cannot override them — the harness stage
builds on top of the kit base and only hardening follows. They hold **only
sandbox-wide policy**, which outranks any `--settings` or user setting:

| Key | Why it is policy |
|-----|------------------|
| `autoUpdates: false`, `env.DISABLE_AUTOUPDATER`/`DISABLE_UPDATES` | A CLI change is a spec `version` edit + rebuild, never a self-update. |
| `disableRemoteControl: false`, `remoteControlAtStartup: true` | How sessions are reachable — a sandbox decision. |
| `skipDangerousModePermissionPrompt`, `bypassPermissionsModeAccepted` | The one-time bypass acceptance, so no session stalls on the prompt. |
| `disableAutoMode: "disable"` | Which permission modes exist is permission policy (`policy.mode` never offers auto). |
| `permissions.defaultMode: bypassPermissions` | For **interactive** sessions (`at-cove connect`/`chat` run `claude` without the harness argv). A Jam episode's `--dangerously-skip-permissions` / `--permission-mode` flag outranks it ([model-spec-policy.md](model-spec-policy.md)). |

Model-specs may not set these keys ([validation](model-specs.md#validation)).
The **preferences** the managed settings used to force — `agentPushNotifEnabled`,
`alwaysThinkingEnabled`, `disableAgentView` (it only hides a UI view),
`inputNeededNotifEnabled`, `prefersReducedMotion`, `showThinkingSummaries`,
`showTurnDuration`, `spinnerTipsEnabled: false`, `theme: dark` — are
claude-default's `claude.settings` (`modelspec.DefaultClaudeSettings`), applied
per episode via `--settings`, so a cove under claude-default runs with the same
values. Interactive sessions get the same values from the base image's seeded
user `settings.json` (a kit's own base may differ — they are preferences).

## The one-time migration

A one-time store migration, recorded by a schema marker
(`jam_settings` key `model_spec_schema`, now `2`), runs at the first
`at-jam serve` startup after an upgrade — and on [import](backup.md) of a
backup taken before it — applying each step the marker has not recorded, so no
step ever re-runs (an operator's later edits are never undone).

**Step 1 (COV-242, the version split)** rewrites **every** stored spec:

- a non-exact `version` becomes `DefaultClaudeVersion`; the old range is kept as
  `version-constraint` when it admits the pin (`>=2.0.0`, `2.x`, `*`), so coves
  on images built before the upgrade keep passing their check. A range that
  doesn't (`2.0.x`, `>=3.0.0`) becomes the pin, with a WARN naming the spec;
- plugin ids that are malformed or name an unknown marketplace are dropped
  (WARN naming the spec and plugin), as are `enabledPlugins` /
  `extraKnownMarketplaces` in `claude.settings`;
- a spec left with no plugins gets claude-default's — every image carried them
  before. After the marker, an explicit `plugins: []` is kept as written.

**Step 2 (COV-245, preferences)** touches only a stored `claude-default`: it
gains each [claude-default preference](#managed-settings-vs-preferences-cov-245)
key its `claude.settings` lacks; a value the operator already set (say
`theme: light`) is kept, and every other spec is untouched.
