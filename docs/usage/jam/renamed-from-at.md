---
summary: The at-/cove → jam rename — every old name (binaries, the Studio Manager, images, tags, labels, containers and volumes, env vars, build args, in-image paths, directories, release assets, Go packages) and its new name, the upgrade steps (studios are not migrated, only re-raised), and what Jam migrates by itself.
read_when: You are upgrading a Jam host or a workstation from a release whose binaries were `at-jam`/`at-mint`/`at-studio-manager`, or something you own (a kit, script, service unit, env var, path or ssh config) still says `at-jam`, `at-mint`, `cove-master`, `cove-*` or `harbor.cove`, and you need the new name or the upgrade steps.
owns: the at-/cove → jam old→new name table, the jam-rename upgrade runbook, what the jam-rename migrations do, and how to remove what the old release left
prereqs: serve.md for running serve
tier: leaf
updated: 2026-10-27
---

# Renamed from `at-*` and `cove-*` to `jam`

Everything this repo ships is now named for **Jam**: `jam`, and
`jam-studio-manager` on remote Studio Manager hosts. There are **no aliases** — an old name is simply not read. Jam carries over its own
config and state directories and rewrites stored kits, but **studios are not
migrated**: every studio is redefined and re-raised under the new names, and a
standing session starts fresh. You install the binaries, point your services
at them, and re-raise. Every studio image and every kit rebuilds once (the
studio assembly changed).

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
   rename removed; …`. Drop any `runtime.launcher` block: serve refuses it
   ([retiring it](studio-managers.md#retiring-the-in-process-launcher)).
4. **Start serve.** Its migrations run first (next section); if one cannot
   write the store, serve exits with the reason — fix it and start again (each
   migration is safe to rerun).
5. **Upgrade every [Studio Manager host](#remote-studio-managers)**, then
   **redefine and re-raise every studio** you need. Nothing carries a studio
   across: an old studio's agent cannot reach the new Jam, and the new manager
   does not see its old container, so Jam marks it lost and tears it down
   (one the old `runtime.launcher` ran is reaped without a teardown).
   Standing sessions come back by themselves, but **fresh** — their old state
   stays in the old volumes until you
   [remove them](#remove-what-the-old-release-left).
6. **Run `jam ssh-config --write <studio>`** (or `jam ssh-config --prune`) on
   each workstation: `--write` replaces that studio's old `# >>> at-jam` block
   with a `# >>> jam` one; `--prune` removes every old block.
7. **Rename what you keep outside Jam**:
   - a kit built `FROM` the old published `ghcr.io/aethons-tools/cove-image`
     inherits its `ENV COVE_SSHENV`, which nothing reads now (its Go env stops
     reaching sessions): rebuild it on `ghcr.io/aethons-tools/jam-studio`,
     which sets `JAM_SSHENV`;
   - the release workflow's `atcove` job is now `binaries`: update the
     required-check names in branch protection.

Hosts enrolled with a saved enroll snippet: fetch it again — it now exports
`JAM_IDENTITY_TOKEN`.

## What Jam migrates by itself

- **Directories** (every `jam` command, not only serve): `~/.config/at-jam` →
  `~/.config/jam` and `~/.local/state/at-jam` → `~/.local/state/jam`. Each
  file not already in the new directory is copied
  (modes kept; one already there is never overwritten); a marker file written
  last marks the copy done, so an interrupted one is finished by the next run.
  The old directories stay.
- **Kits** (at serve start): a kit whose current `kit.yml` or Dockerfile still uses an old
  build-arg or ENV name, or names the old base image repository (also in a
  `FROM` line, digest kept), gets a new version with the new names (history
  shows it as a *migration* version); a kit pinned to such a parent version is
  re-pinned to the migrated one. A kit that can't be rewritten, or that pins
  an old parent version that wasn't migrated, keeps its version and shows a
  `kit.migrate:<name>` condition. Kits kept in git (config-as-code or a
  tracked source) show drift until you rename there too; a push, a config
  apply or a draft publish that still uses an old name is refused with the
  new name in the message. Kit drafts are not rewritten.
- **Destinations** (at serve start): a stored destination env key now under `JAM_` (Jam's own
  namespace) is dropped from every connector, and the destination shows a
  `destination.env:<name>` condition, `env key <K> is reserved (JAM_*); rename
  it`.

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
   down, which removes the old container. Standing sessions pinned to it start
   fresh (Jam warns `pinned standing session's state is missing on its
   manager`). Remove what is left on the host as in
   [the next section](#remove-what-the-old-release-left).

Its `authorization: Bearer` token, Jam's cove-facing :443 address and its env
(it reads none) are unchanged; the names it changes (binary, directory, gRPC
stream, labels) are in the tables below.

## Jam in a container

The [image](container.md) keeps its name, `ghcr.io/aethons-tools/jam`; only
its binary changed. Pull the new tag and recreate the container; keep its
`/var/lib/jam` volume. It now holds `/usr/local/bin/jam` only (`ENTRYPOINT
["jam"]`): a `docker exec … at-jam <verb>` becomes `… jam <verb>`, and a
`command:` credential running `at-mint` becomes `["jam", "mint", …]`. The
local tags `cove-jam:dev` / `cove-jam:ci` are `jam:dev` / `jam:ci`. The
directory move runs inside the container (`HOME` is `/var/lib/jam`), so a
`.config/at-jam` or `.local/state/at-jam` in that volume is copied across on
the first start.

## Remove what the old release left

Nothing reads these any more. On each Colima host (a manager host, or a Jam
host that ran the old `runtime.launcher`), once its studios are re-raised;
`<docker-context>` is the manager's `colima.docker-context` (or the old
launcher's `docker-context`), default `colima`:

- **Containers** — the old launcher's `atcove-cove-<id>` and a manager's
  `atcove.<manager>.cove-<id>`, all labelled `harbor.cove`:
  `docker --context <docker-context> ps -aq --filter label=harbor.cove | xargs -r docker --context <docker-context> rm -f`.
  On a daemon shared with another, not yet upgraded Jam or manager, narrow it
  (`--filter label=harbor.cove.manager=<name>`, or `--filter name=^atcove-cove-`
  for the old launcher's).
- **State volumes** (`<old>-agent-data`, `-workspace`, `-docker`) hold the old
  standing sessions' state; it is not copied anywhere. Keep them while you may
  want a file back:
  `docker --context <docker-context> volume ls -q --filter name=^atcove` (both old forms),
  then `xargs -r docker --context <docker-context> volume rm`.
- **Images** `cove-kit:*` (and `cove-kit-base:*`, `cove-kit-draft:*`):
  `docker --context <docker-context> image ls --filter 'reference=cove-kit*' -q | xargs -r docker --context <docker-context> image rm`.
- **The old launcher's key**, `~/.config/at-cove/{id_ed25519,id_ed25519.pub,known_hosts.d}`
  on a Jam host: Jam has no launcher now, so it is not carried over; delete it.

## Old → new

| Old | New |
|---|---|
| `at-jam` | `jam` |
| Jam image `ghcr.io/aethons-tools/jam` with `at-jam` and `at-mint`; local tag `cove-jam:dev` | the same image with `jam` only (`jam mint`); `jam:dev` |
| `at-studio-manager` (remote manager hosts), `~/.config/at-studio-manager` | `jam-studio-manager`, `~/.config/jam-studio-manager` |
| `at-mint github\|anthropic` | `jam mint github\|anthropic` |
| `cove-master` (in studios) | `jam-studio-agent` |
| `ghcr.io/aethons-tools/cove-base-image`, `…/cove-image` | `ghcr.io/aethons-tools/jam-studio-base`, `…/jam-studio` (the blessed bases were copied, digests unchanged) |
| tags `cove-kit:`, `cove-kit-base:`, `cove-kit-draft:`; `$TMP/cove-kit-builds` | `jam-kit:`, `jam-kit-base:`, `jam-kit-draft:`; `$TMP/jam-kit-builds` |
| labels `harbor.cove`, `harbor.cove.state`, `harbor.cove.jam`, `harbor.cove.actor`, `harbor.cove.manager` | `jam.studio`, `jam.studio.state`, `jam.studio.jam`, `jam.studio.actor`, `jam.studio.manager` |
| containers `atcove-cove-<id>`, volumes `atcove-cove-<id>-{agent-data,workspace,docker}` | `jam-studio-<id>`, `jam-studio-<id>-…` |
| a remote manager's containers `atcove.<manager>.cove-<id>` (and their volumes) | `jam-studio.<manager>.<id>`, `jam-studio.<manager>.<id>-…` |
| build args `COVE_BASE_IMAGE`, `AT_JAM_STUDIO_BASE_IMAGE`, `AT_JAM_STUDIO_TARGET_ARCH`; kit `ENV COVE_SSHENV` | `JAM_STUDIO_BASE_IMAGE`, `JAM_STUDIO_TARGET_ARCH`; `ENV JAM_SSHENV` |
| `/usr/local/lib/cove/`, `cove-egress.service` (and its `cove-egress.conf` drop-ins), `/etc/ssh/sshd_config.d/cove.conf`, `cove-git-credential.sh`, `/etc/cove/mcp-servers.json`, `/dev/shm/cove-*`, `/agent-data/cove-master.log` | `/usr/local/lib/jam/`, `jam-egress.service` (`jam-egress.conf`), `/etc/ssh/sshd_config.d/00-jam.conf`, `jam-git-credential.sh`, `/etc/jam/mcp-servers.json`, `/dev/shm/jam-*` (except `/dev/shm/cove-master-env` → `/dev/shm/jam-studio-agent-env`), `/agent-data/jam-studio-agent.log` |
| `~/.config/at-jam`, `~/.local/state/at-jam` | `~/.config/jam`, `~/.local/state/jam` |
| `~/.ssh/config` `# >>> at-jam <studio> >>>` | `# >>> jam <studio> >>>` |
| release `cove_<ver>_<os>_<arch>.tar.gz` (at-cove, at-mint, at-jam, at-studio-manager) | `jam_<ver>_<os>_<arch>.tar.gz` (`jam`, `jam-studio-manager`) |
| attach gRPC `harbor.attach.v1.Runtime`, `x-harbor-launch-secret`; manager gRPC `cove.manager.v1.StudioManager` | `jam.attach.v1.Runtime`, `x-jam-launch-secret`; `jam.manager.v1.StudioManager` |
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
migration lock key, the `cove-ic` kit's name, and the files a standing session
keeps in `/agent-data` (`.cove-conversation`). The earlier Harbor → Jam names
are in [renamed-from-harbor.md](renamed-from-harbor.md).
