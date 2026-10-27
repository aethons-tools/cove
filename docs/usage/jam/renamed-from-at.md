---
summary: The at-/cove → jam rename — every old name (binaries, the Studio Manager, images, tags, labels, containers and volumes, env vars, build args, in-image paths, directories, release assets, Go packages) and its new name, the upgrade steps, and what Jam migrates by itself at serve start.
read_when: You are upgrading a Jam host or a workstation from a release whose binaries were `at-jam`/`at-mint`/`at-studio-manager`, or something you own (a kit, script, service unit, env var, path or ssh config) still says `at-jam`, `at-mint`, `cove-master`, `cove-*` or `harbor.cove`, and you need the new name or the upgrade steps.
owns: the at-/cove → jam old→new name table, the jam-rename upgrade runbook, and what the jam-rename serve-start migrations do
prereqs: serve.md for running serve
tier: leaf
updated: 2026-10-27
---

# Renamed from `at-*` and `cove-*` to `jam`

Everything this repo ships is now named for **Jam**: `jam`, and
`jam-studio-manager` on remote Studio Manager hosts. There are **no aliases** — an old name is simply not read. Jam migrates its own state
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
   It installs `jam` and `jam-studio-manager`, and notices any `at-jam`,
   `at-mint`, `at-cove` or `at-studio-manager` left in the install directory;
   delete them after step 3.
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

Then upgrade every [remote Studio Manager host](#remote-studio-managers).

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
  `local` are not migrated, even when that manager shares the local docker
  host (its volumes carry its own Jam label, `<runtime-addr>#<name>`) — see
  [Remote Studio Managers](#remote-studio-managers).
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

## Remote Studio Managers

Nothing on a manager host migrates by itself, and a manager must run Jam's
exact release (an old one is refused: its stream also uses the old gRPC name).
On each host, after Jam is upgraded:

1. **Stop `at-studio-manager run`** (its service unit).
2. **Install the release** with the one-command installer above (an old
   `at-jam update` there fails the same way). Delete `at-studio-manager` (and
   any `at-jam`, `at-mint`) from the install directory.
3. **Move its config directory**: `mv ~/.config/at-studio-manager
   ~/.config/jam-studio-manager` (under `$XDG_CONFIG_HOME` when set), and
   rewrite any `token-file`, `ca-file`, `identity-file` or `known-hosts-dir`
   in `config.yml` that names the old directory. The token stays valid.
4. **Point the service at `jam-studio-manager run`** (`--config` if you pass
   one) and start it.
5. **Re-raise its studios.** The new manager does not see its pre-rename
   containers (old labels), so on reconnect Jam marks them lost and tears each
   down, which removes the old container. Remove anything left over:
   `docker --context <docker-context> ps -a --filter label=harbor.cove.manager=<name>`.
6. **Standing sessions pinned to it start fresh** (Jam warns `pinned standing
   session's state is missing on its manager`): their state stays in the old
   `atcove-cove-<id>-agent-data` and `-workspace` volumes on that host. To keep
   it, before step 4 create each new volume and copy into it, as root, on an
   image already on the host (for example the old studio's, `docker --context <docker-context> inspect
   --format '{{.Config.Image}}' atcove-cove-<id>`):
   `docker --context <docker-context> volume create --label jam.studio.state=<id> --label jam.studio.jam=<runtime-addr>#<name> --label jam.studio.manager=<name> jam-studio-<id>-agent-data`,
   then `docker --context <docker-context> run --rm --network none --user 0:0 --entrypoint /bin/sh -v atcove-cove-<id>-agent-data:/from:ro -v jam-studio-<id>-agent-data:/to <image> -c 'cp -a /from/. /to/'`
   (and the same for `-workspace`); remove the old volumes once you have
   checked the new ones. `<runtime-addr>` and `<docker-context>` are the manager's
   `colima.runtime-addr` and `colima.docker-context` (default `colima`).

Its `authorization: Bearer` token, Jam's cove-facing :443 address and its env
(it reads none) are unchanged; the names it changes (binary, directory, gRPC
stream, labels) are in the tables below.

## Old → new

| Old | New |
|---|---|
| `at-jam` | `jam` |
| `at-studio-manager` (remote manager hosts), `~/.config/at-studio-manager` | `jam-studio-manager`, `~/.config/jam-studio-manager` |
| `at-mint github\|anthropic` | `jam mint github\|anthropic` |
| `cove-master` (in studios) | `jam-studio-agent` |
| `ghcr.io/aethons-tools/cove-base-image`, `…/cove-image` | `ghcr.io/aethons-tools/jam-studio-base`, `…/jam-studio` (the blessed bases were copied, digests unchanged) |
| tags `cove-kit:`, `cove-kit-base:`, `cove-kit-draft:`; `$TMP/cove-kit-builds` | `jam-kit:`, `jam-kit-base:`, `jam-kit-draft:`; `$TMP/jam-kit-builds` |
| labels `harbor.cove`, `harbor.cove.state`, `harbor.cove.jam`, `harbor.cove.actor`, `harbor.cove.manager` | `jam.studio`, `jam.studio.state`, `jam.studio.jam`, `jam.studio.actor`, `jam.studio.manager` |
| containers `atcove-cove-<id>`, volumes `atcove-cove-<id>-{agent-data,workspace,docker}` | `jam-studio-<id>`, `jam-studio-<id>-…` |
| build args `COVE_BASE_IMAGE`, `AT_JAM_STUDIO_BASE_IMAGE`, `AT_JAM_STUDIO_TARGET_ARCH`; kit `ENV COVE_SSHENV` | `JAM_STUDIO_BASE_IMAGE`, `JAM_STUDIO_TARGET_ARCH`; `ENV JAM_SSHENV` |
| `/usr/local/lib/cove/`, `cove-egress.service` (and its `cove-egress.conf` drop-ins), `/etc/ssh/sshd_config.d/cove.conf`, `cove-git-credential.sh`, `/etc/cove/mcp-servers.json`, `/dev/shm/cove-*`, `/agent-data/cove-master.log` | `/usr/local/lib/jam/`, `jam-egress.service` (`jam-egress.conf`), `/etc/ssh/sshd_config.d/00-jam.conf`, `jam-git-credential.sh`, `/etc/jam/mcp-servers.json`, `/dev/shm/jam-*` (except `/dev/shm/cove-master-env` → `/dev/shm/jam-studio-agent-env`), `/agent-data/jam-studio-agent.log` |
| `~/.config/at-jam`, `~/.local/state/at-jam`, `~/.config/at-cove` (launcher key) | `~/.config/jam`, `~/.local/state/jam`, `~/.config/jam` |
| `~/.ssh/config` `# >>> at-jam <studio> >>>` | `# >>> jam <studio> >>>` |
| release `cove_<ver>_<os>_<arch>.tar.gz` (at-cove, at-mint, at-jam, at-studio-manager) | `jam_<ver>_<os>_<arch>.tar.gz` (`jam`, `jam-studio-manager`) |
| attach gRPC `harbor.attach.v1.Runtime`, `x-harbor-launch-secret`; manager gRPC `cove.manager.v1.StudioManager` | `jam.attach.v1.Runtime`, `x-jam-launch-secret`; `jam.manager.v1.StudioManager` |
| `.at-jam/<kit>/` (this repo's dev kit) | `.jam/<kit>/` |
| repo dirs `cmd/at-jam`, `cmd/at-mint`, `cmd/at-studio-manager`, `cmd/cove-master`, `images/cove-base-image`, `images/cove-image`; `docs/usage/at-mint.md` | `cmd/jam`, `internal/mintcmd`, `cmd/jam-studio-manager`, `cmd/jam-studio-agent`, `images/jam-studio-base`, `images/jam-studio`; `docs/usage/jam-mint.md` |
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
