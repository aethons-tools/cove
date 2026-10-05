---
summary: What a model-spec builds into a cove's image — the harness layer (exact CLI version + plugins, seeded and enabled, plus Claude Code's managed settings), which Claude settings are sandbox policy vs. claude-default preferences, its place in the image identity — and the one-time model-spec store migration (COV-242 version split, COV-245 claude-default preferences, COV-241 destination oauth_beta → pool header rule).
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
spec delivered = claude-default's install. A plain at-cove kit's
[`model-spec:`](../at-cove-config.md#model-spec) block drives the same layer at
`at-cove install` (none = claude-default's install). **Enablement follows the spec, too:**
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
`showTurnDuration`, `spinnerTipsEnabled: false`, `theme: dark`
(`modelspec.DefaultClaudeSettings`, the one source) — are now a **baseline**:
the claude stage renders them at build and merges them *under* the first-boot
seed `settings.json` (`merge-baseline-settings.sh`, after the plugin seed), so
every session starts from them — interactive or headless, with any spec or
none, on any kit base. claude-default also carries them as its
`claude.settings`, explicitly.

### Where a Claude setting comes from (highest wins)

1. **Managed settings** — the sandbox policy above; nothing overrides it.
2. **Session flags** — a Jam episode's permission flags and `--settings` file:
   the spec's `claude.settings` plus its plugin enablement.
3. Project settings in the repo being worked on (Claude Code's own rules).
4. **User settings** (`/agent-data/settings.json`, seeded on a fresh state
   volume): the base image's `settings.json` and the plugin seed's enablement,
   over the **baseline preferences**, which are lowest.

Stage order is CLI → plugin seed → baseline → managed settings, each its own
`COPY`/`RUN` layer: the seed's build-time `claude plugin` commands never run
under the managed update controls, and editing one layer's input never re-runs
the network-bound seed.

## The one-time migration

A one-time store migration, recorded by a schema marker
(`jam_settings` key `model_spec_schema`, now `3`), runs at the first
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

**Step 2 (COV-245, settings)** rewrites `claude.settings`:

- every spec drops the managed sandbox-policy keys Jam now refuses
  (`autoUpdates`, `disableRemoteControl`, `remoteControlAtStartup`,
  `skipDangerousModePermissionPrompt`, `bypassPermissionsModeAccepted`,
  `disableAutoMode`), each with a WARN naming the spec and key — so stored
  specs stay updatable and old backups importable;
- a stored `claude-default` gains each
  [baseline preference](#managed-settings-vs-preferences-cov-245) key it
  lacks; a value the operator already set (say `theme: light`) is kept.

**Step 3 (COV-241, the oauth beta)** — when any destination still carries the
removed `oauth_beta` flag (stored rows and backups keep loading; the field is
read only here): every spec whose principal is `pool`, or the flagged
destination's own credential, gains the
[pool oauth-beta rule](model-spec-headers.md#the-pool-oauth-beta) — appended
after its existing rules (the flag used to apply last), never duplicated; a spec
already at 16 rules gets a WARN instead — and the flag is cleared on each
destination. The serve log names the destinations. A pool `claude-default`
seeded later carries the rule from the start.

**Plain at-cove kits** run no migration: a kit's
[`model-spec:`](../at-cove-config.md#model-spec) block is read as written, and
the removed `model-provider:` block is a load error with its replacement.
