---
summary: The at-/cove → jam rename — every old name (binaries, images, tags, labels, containers and volumes, env vars, build args, in-image paths, directories, release assets, Go packages) and its new name, the upgrade steps, and what Jam migrates by itself at serve start.
read_when: You are upgrading a Jam host or a workstation from a release whose binaries were `at-jam`/`at-mint`, or something you own (a kit, script, service unit, env var, path or ssh config) still says `at-jam`, `at-mint`, `cove-master`, `cove-*` or `harbor.cove`, and you need the new name or the upgrade steps.
owns: the at-/cove → jam old→new name table, the jam-rename upgrade runbook, and what the jam-rename serve-start migrations do
prereqs: serve.md for running serve
tier: leaf
updated: 2026-10-27
---

# Renamed from `at-*` and `cove-*` to `jam`

Everything this repo ships is now named for **Jam**: one binary, `jam`. There
are **no aliases** — an old name is simply not read. Jam migrates its own state
the first time the new `jam serve` starts; you install the binary, point your
service at it, and re-raise your studios once. Every studio image and every kit
rebuilds once (the studio assembly changed).

## Upgrade, step by step

1. **Stop serve** (your service manager, e.g. `systemctl stop <unit>` for the
   unit that runs `at-jam serve`).
2. **Install `jam`** with the one-command installer — an old `at-jam update`
   cannot fetch the new release (it asks for a `cove_*` archive that no longer
   exists, fails, and changes nothing):
   `gh api -H "Accept: application/vnd.github.raw" /repos/aethons-tools/cove/contents/install.sh | bash`
   It notices any `at-jam`, `at-mint` or `at-cove` left in the install
   directory; delete them after step 3.
3. **Point the service and your PATH at `jam`**: the unit's `ExecStart`
   becomes `jam serve --config …`; scripts call `jam …`; `AT_JAM_ADMIN_TOKEN`
   becomes `JAM_ADMIN_TOKEN` (tables below). In the
   [credentials file](credentials.md), a `command:` that runs `at-mint …` (or
   `at-jam mint …`) becomes `["jam", "mint", …]`, and its environment
   `AT_MINT_GITHUB_APP_KEY` / `AT_MINT_AUTH0_CLIENT_SECRET` becomes
   `JAM_MINT_GITHUB_APP_KEY` / `JAM_MINT_AUTH0_CLIENT_SECRET`
   ([jam-mint.md](../jam-mint.md)). Serve start warns about any credential
   command still running `at-mint` or `at-jam`, naming the credentials only:
   `jam rename: credential commands still call at-mint or at-jam, which the
   rename removed; …`.
4. **Start serve** — with docker reachable (colima started) for this first
   start. Its migrations run first (next section); if one fails, serve exits
   with the reason and changes nothing more — fix it and start again (each
   migration is safe to rerun). Until the studio migration has completed once,
   every start needs docker and fails closed without it:
   `jam rename: the studio migration must complete once before serve starts
   (docs/usage/jam/renamed-from-at.md): …`. Once it completes, serve records
   that in `~/.local/state/jam/rename-migrated` (the serve config's
   `state-dir`, per docker context) and later starts skip it.
5. **Re-raise every studio** that was running: its old container was removed
   (its agent could not reach the new Jam). Standing sessions come back by
   themselves, on their own state.
6. **Run `jam ssh-config --write <studio>`** (or `jam ssh-config --prune`) on
   each workstation: `--write` replaces that studio's old `# >>> at-jam` block
   with a `# >>> jam` one; `--prune` removes every old block.
7. **Rename what you keep outside Jam**:
   - a kit built `FROM` the old published `ghcr.io/aethons-tools/cove-image`
     inherits its `ENV COVE_SSHENV`, which nothing reads now (its Go env stops
     reaching sessions): rebuild it on `ghcr.io/aethons-tools/jam-studio`,
     which sets `JAM_SSHENV`;
   - a [kit source](kits-git.md) tracking `.at-jam/cove-ic` in this repo must
     track `.jam/cove-ic`;
   - the release workflow's `atcove` job is now `binaries`: update the
     required-check names in branch protection.

Hosts enrolled with a saved enroll snippet: fetch it again — it now exports
`JAM_IDENTITY_TOKEN`.

## What serve migrates by itself

- **Directories** (every `jam` command, not only serve): `~/.config/at-jam` →
  `~/.config/jam`, `~/.local/state/at-jam` → `~/.local/state/jam`, and the
  launcher's `~/.config/at-cove/{id_ed25519,id_ed25519.pub,known_hosts.d}` →
  `~/.config/jam/`. Each file not already in the new directory is copied
  (modes kept; one already there is never overwritten); a marker file written
  last marks the copy done, so an interrupted one is finished by the next run.
  The old directories stay.
- **Studios** (the `local` manager's): each pre-rename container of this Jam
  (`atcove-cove-<id>`, or labelled `harbor.cove`) is removed with its docker
  cache and pinned host key, and its instance is marked lost, so Jam finishes
  its teardown. A pre-rename container that isn't this Jam's (another Jam on a
  shared docker host, or a studio this Jam no longer tracks) is **left
  running** and named in one warning, `migration: pre-rename containers not
  owned by this Jam left running: …` — remove the ones that are yours by hand
  (`docker --context <docker-context> rm -f <name>`).
- **Standing sessions' state**: each `atcove-cove-<id>-agent-data` and
  `-workspace` volume is copied to `jam-studio-<id>-…` (files, owners and
  modes kept, then verified); the old volume is removed only after its copy
  verified. Any failure stops serve, and your data is still in the old volume.
  A failed volume stops it with
  `migration: volume <old> could not be copied to <new>; left in place` (the
  warning just before it says why):
  - **the copy failed** — free space or fix docker and start again (a partial
    copy is recognised and redone);
  - **the copy verified, but the old volume could not be removed** — every
    start stops here until you remove it: check the new volume, then
    `docker --context <docker-context> volume rm atcove-cove-<id>-agent-data`
    (or `-workspace`, as the message names). The same applies after a
    rollback recreated an old volume beside a completed copy.

  **No image to copy with** stops it with `migration: no image on this host
  can copy the state volumes (nothing was moved); pull the jam base — docker
  --context <docker-context> pull ghcr.io/aethons-tools/jam-studio-base@sha256:… —
  and start jam serve again`.
  The copy runs on an image already on the docker host, never a pulled one,
  tried in order: a studio image retired this start, a blessed jam base
  (newest first), then the pre-rename `cove-base-image` at the same blessed
  digests. Run the command the message names and start again.

  `<docker-context>` is the launcher's `docker-context` (default `colima`).
- **Other managers**: studios and standing sessions on a manager other than
  `local` are not migrated — re-raise them once their manager runs the new
  release.
- **Kits**: a kit whose current `kit.yml` or Dockerfile still uses an old
  build-arg or ENV name, or names the old base image repository (also in a
  `FROM` line, digest kept), gets a new version with the new names (history
  shows it as a *migration* version); a kit pinned to such a parent version is
  re-pinned to the migrated one. A kit that can't be rewritten, or that pins
  an old parent version that wasn't migrated, keeps its version and shows a
  `kit.migrate:<name>` condition. Kits kept in git (config-as-code or a
  tracked source) show drift until you rename there too; a push, a config
  apply or a draft publish that still uses an old name is refused with the
  new name in the message. Kit drafts are not rewritten.
- **Destinations**: a stored destination env key now under `JAM_` (Jam's own
  namespace) is dropped from every connector, and the destination shows a
  `destination.env:<name>` condition, `env key <K> is reserved (JAM_*); rename
  it`.

Old images (`cove-kit:*`) are not deleted; remove them when you like:
`docker --context <docker-context> image ls --filter 'reference=cove-kit*' -q | xargs -r docker --context <docker-context> image rm`.

## Old → new

| Old | New |
|---|---|
| `at-jam` | `jam` |
| `at-mint github\|anthropic` | `jam mint github\|anthropic` |
| `cove-master` (in studios) | `jam-studio-agent` |
| `ghcr.io/aethons-tools/cove-base-image`, `…/cove-image` | `ghcr.io/aethons-tools/jam-studio-base`, `…/jam-studio` (the blessed bases were copied, digests unchanged) |
| tags `cove-kit:`, `cove-kit-base:`, `cove-kit-draft:`; `$TMP/cove-kit-builds` | `jam-kit:`, `jam-kit-base:`, `jam-kit-draft:`; `$TMP/jam-kit-builds` |
| labels `harbor.cove`, `harbor.cove.state`, `harbor.cove.jam` | `jam.studio`, `jam.studio.state`, `jam.studio.jam` |
| containers `atcove-cove-<id>`, volumes `atcove-cove-<id>-{agent-data,workspace,docker}` | `jam-studio-<id>`, `jam-studio-<id>-…` |
| build args `COVE_BASE_IMAGE`, `AT_JAM_STUDIO_BASE_IMAGE`, `AT_JAM_STUDIO_TARGET_ARCH`; kit `ENV COVE_SSHENV` | `JAM_STUDIO_BASE_IMAGE`, `JAM_STUDIO_TARGET_ARCH`; `ENV JAM_SSHENV` |
| `/usr/local/lib/cove/`, `cove-egress.service` (and its `cove-egress.conf` drop-ins), `/etc/ssh/sshd_config.d/cove.conf`, `cove-git-credential.sh`, `/etc/cove/mcp-servers.json`, `/dev/shm/cove-*`, `/agent-data/cove-master.log` | `/usr/local/lib/jam/`, `jam-egress.service` (`jam-egress.conf`), `/etc/ssh/sshd_config.d/00-jam.conf`, `jam-git-credential.sh`, `/etc/jam/mcp-servers.json`, `/dev/shm/jam-*` (except `/dev/shm/cove-master-env` → `/dev/shm/jam-studio-agent-env`), `/agent-data/jam-studio-agent.log` |
| `~/.config/at-jam`, `~/.local/state/at-jam`, `~/.config/at-cove` (launcher key) | `~/.config/jam`, `~/.local/state/jam`, `~/.config/jam` |
| `~/.ssh/config` `# >>> at-jam <studio> >>>` | `# >>> jam <studio> >>>` |
| release `cove_<ver>_<os>_<arch>.tar.gz` (at-cove, at-mint, at-jam) | `jam_<ver>_<os>_<arch>.tar.gz` (`jam`) |
| attach gRPC `harbor.attach.v1.Runtime`, `x-harbor-launch-secret` | `jam.attach.v1.Runtime`, `x-jam-launch-secret` |
| `.at-jam/<kit>/` (this repo's dev kit) | `.jam/<kit>/` |
| repo dirs `cmd/at-jam`, `cmd/at-mint`, `cmd/cove-master`, `images/cove-base-image`, `images/cove-image`; `docs/usage/at-mint.md` | `cmd/jam`, `internal/mintcmd`, `cmd/jam-studio-agent`, `images/jam-studio-base`, `images/jam-studio`; `docs/usage/jam-mint.md` |
| Go `internal/covemaster`, `internal/covemasterbin`, `scripts/stage-covemaster.sh` (staged `covemaster/cove-master-linux-<arch>`) | `internal/studioagent`, `internal/studioagentbin`, `scripts/stage-studio-agent.sh` (`studioagent/jam-studio-agent-linux-<arch>`) |
| Go `AtCoveIdentity`, `LaunchCoveMaster`, `CoveMasterOptions`, `CoveMasterLogVMPath`, `naming.CoveContainer` | `AssemblyIdentity`, `LaunchStudioAgent`, `StudioAgentOptions`, `StudioAgentLogVMPath`, `naming.StudioContainer` |

### Environment variables

| Old | New |
|---|---|
| `AT_JAM_ADMIN_TOKEN` | `JAM_ADMIN_TOKEN` |
| `AT_LOG_MODE`, `AT_LOG_LEVEL` | `JAM_LOG_MODE`, `JAM_LOG_LEVEL` |
| `AT_MINT_GITHUB_APP_KEY`, `AT_MINT_AUTH0_CLIENT_SECRET`, `COVE_RUN_TIMEOUT` | `JAM_MINT_GITHUB_APP_KEY`, `JAM_MINT_AUTH0_CLIENT_SECRET`, `JAM_RUN_TIMEOUT` |
| `COVE_VERSION`, `COVE_SYSTEM`, `COVE_SYSTEM_BINDIR`, `COVE_REPO`, `COVE_INSTALL_LIB` (installer) | `JAM_VERSION`, `JAM_SYSTEM`, `JAM_SYSTEM_BINDIR`, `JAM_REPO`, `JAM_INSTALL_LIB` |
| in studios: `AT_JAM_RUNTIME_ADDR`, `AT_JAM_IDENTITY_TOKEN`, `AT_JAM_LAUNCH_SECRET`, `AT_JAM_BASE_URL`, `AT_JAM_CONNECTOR` | `JAM_RUNTIME_ADDR`, `JAM_IDENTITY_TOKEN`, `JAM_LAUNCH_SECRET`, `JAM_BASE_URL`, `JAM_CONNECTOR` |
| in studios: `AT_COVE_WORKDIR`, `AT_COVE_AGENT_PROMPT_FILE`, `AT_COVE_AGENT_CONTEXT_FILE`, `AT_COVE_RESIDENT`, `AT_COVE_SESSION_KIND` | `JAM_WORKDIR`, `JAM_AGENT_PROMPT_FILE`, `JAM_AGENT_CONTEXT_FILE`, `JAM_RESIDENT`, `JAM_SESSION_KIND` |
| in images: `COVE_DOCKER`, `COVE_SHADOW_DIRS`, `COVE_*_FILE`, `COVE_PLUGIN_*` | `JAM_DOCKER`, `JAM_SHADOW_DIRS`, `JAM_*_FILE`, `JAM_PLUGIN_*` |
| `AT_HARBOR_*` (every one) | removed — use the `JAM_*` name |

Operator-authored env (a model-spec's `provider-env`, a destination's env) may
not set `JAM_*`: that namespace is Jam's.

## Not renamed

The admin API and UI routes (`/admin/coves…`), JSON fields and ids that say
"cove", the Go module and repository (`aethons-tools/cove`), the Postgres
migration lock key, the `cove-ic` dev kit, and the files a standing session
keeps in `/agent-data` (`.cove-conversation`). The earlier Harbor → Jam names
are in [renamed-from-harbor.md](renamed-from-harbor.md).
