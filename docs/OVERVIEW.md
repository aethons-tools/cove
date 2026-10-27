---
summary: What this repo builds and how it fits together — the studio image Jam assembles (harness + sealed hardening), its egress tiers and base gate, the package map, and how to build/test/run.
read_when: You need what this repo builds, the studio image's security model, the package map, or how to build/test/run.
owns: the project overview: studio image assembly, security model, package map
prereqs: none
tier: leaf
updated: 2026-10-26
---

# cove — Project Overview

> **Interim (jam-only slice 2).** The standalone `at-cove` CLI, the in-sandbox
> `at-task` worker and the `at-switchboard` Discord conductor are deleted; Jam
> (`at-jam`) is the only way this repo runs agents. This overview keeps what is
> still true — the image every studio is built from, its hardening and egress
> tiers, the package map, building and testing — and still uses some
> at-cove-era words (a kit's `config.yml`, `image.allowed-domains`) where a
> studio kit now has `kit.yml` and `egress:`. Slice 4 of the
> [jam-only design](superpowers/specs/2026-10-26-jam-only-design.md) rewrites it.

This repo builds Jam and the **hardened Claude Code studios** it runs: Jam's
launcher assembles each studio image (a kit base, the harness layer, then the
sealed hardening layer), raises it in Colima, brokers its credentials, and
starts `cove-master`, the in-studio agent supervisor.

- **Module:** `github.com/aethons-tools/cove`
- **Binaries:** `at-jam` (the service and its CLI), `at-mint` (host-side token minter), `at-studio-manager` (remote Studio Manager host process), `cove-master` (embedded in every studio image); dev tools `adopt-base`, `gen-blessed`, `gen-icons`.
- **Dependencies:** see `go.mod` — no CLI framework.

## Why it exists

Running an autonomous coding agent against your real machine is risky:
it can exfiltrate secrets,
reach arbitrary network destinations,
or touch files it shouldn't.
The launcher puts the agent inside a sandbox with three properties baked in:

1. **Egress is locked down** —
   the VM can only reach an allow-listed set of domains (Anthropic, GitHub, PyPI, …)
   through an in-VM Squid proxy,
   with `nftables` dropping everything else.
   The allow-list is **additive** — a sealed base, an always-on infra list and the
   kit's list, which a Jam role's policy may replace within the kit's ceiling (see
   [Egress: four additive allow-lists and a ceiling](#egress-four-additive-allow-lists-and-a-ceiling)) —
   so a kit can *widen* egress without ever weakening the sealed base. A user's own kit files can never weaken this.
2. **Secrets never touch disk or the host process table** —
   Jam's broker holds the real credentials and a studio holds only a scoped identity
   token ([connector](usage/jam/connector.md)); what a raise stages into the studio
   (cove-master's env and prompt) goes over SSH stdin into tmpfs, never argv or disk.
3. **The hardening is foolproof** —
   the security-critical files (egress rules, sshd config, entrypoint) ship embedded in the binary
   and are layered on *last*,
   so they always win over anything the user supplies.
   Agent docs and skills are not security-critical:
   they ship in the kit base (`cove-base-image`), so a kit can override them.

The launcher reaches every studio over SSH (`internal/sshargs`) with a key it
bakes into the image.

## How the build context is assembled

Jam's launcher assembles each studio kit's context (`internal/assemble.AssembleContext`) when the kit's image is not yet built. The context
is **the harness layer and the sealed layer** plus a few generated files — there
is no kit overlay. The image is layered
**`FROM ${BASE}` → harness → hardening**:

0. **Harness layer** (generated, `internal/harnessinstall`) — a `FROM ${BASE} AS harness`
   stage that installs the agent CLI at the model-spec's **exact version** (claude:
   the native installer `curl -fsSL https://claude.ai/install.sh | bash -s X.Y.Z`
   as `agent`, the `/usr/local/bin/claude` symlink, `ENV DISABLE_AUTOUPDATER=1`),
   then seeds the spec's `claude.plugins` (below), merges the baseline Claude
   preferences under the first-boot user `settings.json`, and last installs
   Claude Code's managed settings `/etc/claude-code/managed-settings.json` —
   sandbox-wide policy only, Claude-specific so not a hardening file; see
   [model-spec-harness.md](usage/jam/model-spec-harness.md#managed-settings-vs-preferences-cov-245). It is **model-spec-mediated, not
   a hardening concern**: a studio gets its raising role's spec's version and plugins
   ([model-spec-harness.md](usage/jam/model-spec-harness.md)), or with none the
   default install — the pinned `modelspec.DefaultClaudeVersion` (bumped by Renovate) and its plugins.
   Sitting after the kit base, a CLI bump rebuilds only from this stage on (Docker
   layer cache). It needs no secret — the install is unauthenticated.
1. **Non-overridable hardening** (embedded; `FROM harness`) —
   `nftables.conf`, `squid.conf` (its four additive allow-list ACLs — base, infra, kit, session — and the empty session egress file the session ACL reads), sshd hardening, the entrypoint and its `/agent-data` seeding *mechanism* (`seed-agent-data.sh` — not the seeded content), `sshd` `AcceptEnv` config, the git credential helper, the managed Claude settings, the sealed env (`CLAUDE_CONFIG_DIR=/agent-data`, the proxy vars), the version-locked `cove-master` binary, and — for the opt-in `docker:true` boot path — the systemd egress unit (`cove-egress.service`) plus the `docker`/`ssh` ordering drop-ins and the inner-dockerd `daemon.json` (see the [Sysbox docker-in-sandbox design](superpowers/specs/2026-08-08-sysbox-docker-in-sandbox-design.md#d-init-model--daemon-lifecycle)).
2. **Generated** — the kit's egress list (baked into `allowed_domains.kit.txt` and the `egress_ceiling.txt` bound), the Jam host into `allowed_domains.infra.txt`, the kit's MCP servers, and the managed public key (see [Egress: four additive allow-lists and a ceiling](#egress-four-additive-allow-lists-and-a-ceiling)).

The kit's **`image/`** is *not* overlaid here — it is the Docker **build context**
for the kit's `image/Dockerfile`, which selects/builds the base the launcher hardens
(see the base-image section below). The **overridable agent defaults** — the
startup settings (`settings.json`, `.claude.json`) and the agent docs and skills
(`CLAUDE.md` and its imports, `reference/`, `skills/`) — ship in `cove-base-image`,
so a kit's Dockerfile overrides them the normal way (`COPY` over
`/home/agent/.init-agent-data`) and the sealed layer stays purely sealed (COV-246).

The hardening extracting last is the **security boundary**:
nothing a kit — or a model-spec's harness layer — provides can weaken the egress
lock or sshd hardening; the harness stage only installs a CLI, plugins and Claude settings on the
open-network builder, before any of the sealed steps run. Its managed settings
sit above the kit base too, so a kit cannot override them either. The harness and its
plugins are part of the image's identity: the assembly identity hashes only
the harness layer's payload (its scripts and the managed settings' policy) and
its rendered baseline preferences, while the install itself (type,
version, plugins) is in the studio kit's build-digest — not in the Jam launcher's assembly fingerprint, so a bump
doesn't retag every studio image.
The hardening layer ships inside the binary via Go `embed.FS`,
so it cannot be misplaced or forgotten.
After it,
the assembler writes the managed public key into the context's `authorized_keys` —
an explicit assembly step,
keeping overlay precedence pure.

### The base image and the provenance gate

The assembled `Dockerfile` starts `FROM ${BASE}` (the harness stage; hardening then builds `FROM harness`) — a build arg the backend
resolves when it builds the image (`ResolveKitBase*`, which the launcher calls before every kit build):

1. a `Dockerfile` next to the studio kit's `kit.yml` (the launcher builds it; the built image is the base),
2. else the kit's `base:` — `kit:` a parent kit's image or `image:` a prebuilt ref (not with a `Dockerfile`; [kits.md](usage/jam/kits.md)),
3. else the default: the newest **blessed** `cove-base-image`, pinned by digest.

A **kit-chosen** base (1 or 2) must pass a **provenance gate**: the launcher reads the
resolved image's OCI rootfs `diff_ids` and asserts some blessed
`cove-base-image`'s layers are their exact prefix — i.e. it was really built
*FROM* a blessed base, unforgeably (matching the prefix means those bottom layers
*are* that image, byte-for-byte). The blessed digests are embedded
(`internal/basedigest`, a rolling set); the default base (the head) is blessed by
construction, so it skips the gate. A base that descends from no blessed image is
**rejected**, — there is no override. This lets hardening *trust* its prerequisites (the egress stack, the
`agent` user, the expected layout) rather than probe for them. The gate lives in
`internal/baseimage` (pure prefix logic) with the docker execution behind the
backend seam; the full model is in
[the design spec](superpowers/specs/2026-07-16-kit-selectable-base-image-design.md).

**How the blessed set is maintained — low-watermark + registry snapshot.** The
repo commits exactly one digest in `internal/basedigest/blessed/watermark.txt`:
the **low-watermark**, the oldest `cove-base-image` still trusted. Before `go
build`, `cmd/gen-blessed` lists the published `cove-base-image` digests from the
registry and walks newest→oldest **through** the watermark, writing that list to
the sibling `generated.txt` (gitignored) for `go:embed` (`internal/blessgen` holds
the pure walk; the GHCR query sits behind a seam). `basedigest.Blessed` prefers
`generated.txt` and falls back to the committed watermark, so a routine base
republish is trusted automatically with no commit-back loop, a breaking base
change is a one-line watermark bump that drops everything older, and a fresh clone
or offline build (no `GITHUB_TOKEN`) still compiles and trusts the watermark
alone. A watermark absent from the registry **fails the build loudly**. The full
model is in [the release-pipeline spec](superpowers/specs/2026-07-17-monolithic-release-pipeline-design.md#4-blessing-the-low-watermark--the-registry).

Because hardening trusts the base, `cove-base-image` carries the overridable
defaults every sandbox needs in `/home/agent/.init-agent-data`: the startup
settings (`settings.json`, `.claude.json`), the agent docs (`CLAUDE.md`,
`PROGRESSIVE_DISCLOSURE.md`, `SANDBOX.md`, `COLLABORATOR.md`, `reference/`), the
generic board/docs `skills/`, and the `.refresh` manifest. It also installs `code-server` (a pinned, SHA-256-verified release `.deb`) for the built-in Studio IDE. The sealed layer then, last: installs the
embedded version-locked `cove-master`; populates `/etc/environment` (so `pam_env`
exposes it to every SSH session) via `apply-sshenv.sh`; and re-asserts the
egress/sshd hardening.

**Session env — `COVE_SSHENV`.** SSH sessions read `/etc/environment`, which a
bare `docker run` / CI container does *not* — so the toolchain an image sets via
`ENV` (e.g. `PATH` with `/usr/local/go/bin`) wouldn't reach sessions without help.
`apply-sshenv.sh` bridges the gap: it copies the image's live `PATH` (intrinsic)
plus every variable named in the image's `COVE_SSHENV` (colon-separated) into
`/etc/environment`. So one `ENV` statement feeds both `docker run`/CI and SSH
sessions — no separate fragment to keep in sync. The egress proxy vars and
`CLAUDE_CONFIG_DIR` are the exception: the sealed layer writes them itself (never
image `ENV`, which would poison the build), last, so they always win.

### Egress: four additive allow-lists and a ceiling

The sealed `squid.conf` is default-deny with **four additive allow-list ACLs** — a
request passes if it matches **any**, so a kit can only *widen* egress, never bypass
the sealed base or the `nftables` lock:

| file | source | when | scope |
|---|---|---|---|
| `allowed_domains.txt` | sealed hardening | baked | base, unconditional |
| `allowed_domains.infra.txt` | the Jam host (the launcher's `jam-host`) | baked at build | every studio; a role can't remove it |
| `allowed_domains.kit.txt` | the **active policy list**: the studio kit's `egress:` by default | baked at build; replaced at raise for a Jam role with a policy | every studio |
| `allowed_domains.session.txt` | baked **empty** (its per-session tier went with at-cove) | baked | — |

A fifth file, **`egress_ceiling.txt`**, is an immutable baked copy of
the kit's egress list. Squid never reads it; it is the bound a Jam role's list
must fit inside (below). Every list and the ceiling are root-owned `0644`.

So a studio's effective egress is **`base ∪ infra ∪ kit`** (the kit list being
the role's policy when it has one). The session file stays empty, so its ACL never
dangles.

**A Jam role's egress replaces the policy list, within the ceiling, at raise.**
When Jam raises a cove for a role with an egress policy
([`at-jam egress set`](usage/jam/roster.md#role-egress)), the launcher runs the
sealed, root-only `apply-role-egress.sh` via host `docker exec -u root` after sshd
answers and **before cove-master (and so the agent) starts**. It reads the role's
domains on stdin, refuses — changing nothing — any domain the ceiling doesn't cover
(a leading-dot ceiling entry covers its apex and subdomains; an exact entry covers
only itself), then overwrites `allowed_domains.kit.txt`, clears the session file, and
runs `squid -k reconfigure`. The base and infra lists stay on. A role with no policy
keeps the kit default; a failed apply fails the raise
([coves](usage/jam/coves.md#raising-a-real-managed-studio)). When the role's policy
later changes, Jam re-applies it to the role's running coves the same way
(`apply-role-egress.sh --kit-default` restores the ceiling's list when the policy is
cleared; that mode takes no domains) —
see [egress drift](usage/jam/coves.md#egress-drift).

**Nested-container egress is contained too.** The `nftables` `output` chain locks the
agent's *direct* egress (only the `proxy` user reaches the network); a `forward` chain
extends the same lock to *forwarded* traffic. This matters for the opt-in
[`docker`](usage/docker-in-sandbox.md) capability (nested containers run via Sysbox): a
nested container's traffic is masqueraded and forwarded out the uplink, so without the
`forward` drop it would sidestep the `output`-chain lock. The chain default-drops
forwarded packets and accepts only established/related, so a nested container reaches
the outside only through squid — same allow-list, no bypass. It is **always-on** and
harmless to non-docker sandboxes (no forwarded traffic) and to same-network
container-to-container (L2-bridged, never traverses the `forward` hook).
The `output` chain also accepts traffic onto the sandbox's own docker bridges
(`docker0`, `br-*`), so host-run code reaches a nested container directly or via a
published port; that traffic stays inside the sandbox.

## Architecture

A pure **plan** (what to do) is separated from **execution** (doing it),
so the interesting logic is unit-testable without Docker, SSH, or a live VM.
A `Runner` interface abstracts process execution:
the OS implementation streams stdio and propagates exit codes;
a `Fake` records calls for tests.

```
cmd/at-jam/                    at-jam: `serve` (broker + control plane + launcher + Requisitioner) and the admin-API client verbs
cmd/at-mint/                   host-side token minter (github / anthropic)
cmd/at-studio-manager/        remote Studio Manager: dials Jam, hosts studios on a Colima host (logic in internal/studiomanager/; colima verbs in internal/colimacmd/)
cmd/cove-master/               the in-studio agent supervisor (embedded in every studio image)
cmd/{adopt-base,gen-blessed,gen-icons}/  dev tools: raise the blessed watermark, snapshot blessed bases, regenerate app icons
internal/jam/                  broker + control plane (store, roster, intercom, admin API/UI); internal/jam/launcher raises studios
internal/dispatcher/           the Requisitioner's poll → claim → raise loop
internal/dispatch/{linear,githubissues}/  Jam's trackers (Linear GraphQL, GitHub Issues REST) behind internal/tracker.Tracker
internal/dispatch/scheduler/   tracker vocabulary: Role, Issue, Comment, ClassifyLabels, AssembleBrief
internal/assemble/             the studio build context: harness stage + embedded sealed hardening layer + egress lists + key
internal/harnessinstall/       the harness layer: a model-spec's CLI install (exact version) + plugin seed
internal/install/              the assembly identity (AtCoveIdentity) that keys studio image tags
internal/covemasterbin/        the embedded linux cove-master binaries
internal/backend/{,colima/}    the docker surfaces the launcher drives (run/dial/pause/remove, volumes, egress, SSH CA, kit builds) and their Colima implementation
internal/{baseimage,basedigest}/  base resolution + the blessed-prefix provenance gate
internal/connect/              LaunchCoveMaster: stage cove-master's env/prompt into tmpfs over ssh and start it
internal/kit/                  shared kit types: secret demands, MCP servers, tracker blocks
internal/studio/               the StudioKit definition
internal/discord/              Discord REST client for Jam's relay
internal/keys/                 the launcher's SSH keypair (generated on first use)
internal/naming/               studio container and volume names
internal/{usersecret,mint,secret}/  Jam's credentials file, mint: expansion, host-side resolution
internal/sshargs/              pure argv builders for the ssh client
internal/runner/               Runner interface (OS impl + Fake)
```

Jam itself — `serve`, the broker, the roster, kits, studios, the Requisitioner,
the intercom — is documented under [usage/jam/](usage/jam/INDEX.md);
[renamed-from-harbor.md](usage/jam/renamed-from-harbor.md) lists its Harbor-era names.

## Building, testing, running

### Installing the binaries

The one-command installer ([`install.sh`](../install.sh) at the repo root) is the
fastest way to get `at-jam`, `at-mint` and `at-studio-manager`: it pulls the prebuilt archive from the
latest release the [release pipeline](DEVELOPMENT.md#ci-the-release-pipeline) cuts
on every push to `main`, verifies its SHA-256 against the release `checksums.txt`,
and installs all three (the archive also holds an `at-cove` tombstone, kept only so an
older `at-jam update` can install it; this installer skips it). `cove-master`
ships **embedded** in `at-jam`.

The repo is **private** today, so the installer authenticates through your GitHub
CLI login (`gh auth login`):

```bash
gh api -H "Accept: application/vnd.github.raw" \
  /repos/aethons-tools/cove/contents/install.sh | bash
```

When the repo goes public this collapses to the classic anonymous form — the same
script, unchanged:

```bash
curl -fsSL https://raw.githubusercontent.com/aethons-tools/cove/main/install.sh | bash
```

The installer picks the `gh` path when `gh` is installed **and** authenticated, and
otherwise falls back to anonymous `curl` (which only succeeds once the repo is
public). That single fork is what lets one script serve both phases with no code
change. A checksum mismatch aborts before anything is written.

Optional knobs:

| Env var | Effect |
|---------|--------|
| `COVE_VERSION=<N>-<MMDD>` | Install a specific release instead of the latest. |
| `BINDIR=<dir>` | Install into `<dir>` (wins over the other two). |
| `COVE_SYSTEM=1` | Install into `/usr/local/bin` (uses `sudo` if the dir is not writable). Default is `~/.local/bin`. |

**Updating in place.** Once installed, `at-jam update` upgrades both binaries to the latest release without re-running the `curl | bash` line: it
drives a copy of this same `install.sh` **embedded** in the binary (so it needs no
repo checkout and never re-fetches the script over the network), reusing its
resolve → download → verify → replace flow — including the `checksums.txt`
verification — untouched. It reads the same `COVE_VERSION`/`BINDIR`/`COVE_SYSTEM`/`COVE_REPO`
knobs, no-ops when already current, and takes `--version TAG` (pin) and `--dry-run`.

**Upgrading from a release that shipped at-cove.** An `at-cove` already on your
PATH can be deleted. An older `at-jam update` (or the old at-cove's own
self-update) run against a current release replaces it with the tombstone,
which does nothing but say at-cove was removed.

To **build from source instead** (the contributor path), use `just install` below,
which compiles the binaries and installs them onto your PATH.

Logic lives in `scripts/` so CI never needs `just` installed.
Common tasks (`just` to list them all):

```
just build           # build at-mint + at-jam + at-studio-manager into dist/<os>-<arch>/ (needs Node 22 + npm: the Jam app)
just build-all       # cross-compile every supported target
just jam <args>      # run the last-built at-jam with <args>
just install         # install the host binaries onto your PATH (no sudo by default)
just test            # hermetic unit tests — no docker/network/ssh
just integration     # real-ssh tests of the ssh argv builders (needs ssh/sshd/ssh-keygen)
just integration-jam # jam's integration-tagged tests (real TLS, sshd, git)
just lint            # go vet + gofmt check + shellcheck/hadolint (if present)
just setup           # install dev/test tooling (podman, shellcheck, hadolint, jq)
```

Default `go test ./...` is **hermetic** —
every test drives the `Fake` runner,
so no Docker, network, or live VM is required.
The real-ssh suite (build tag `integration`) boots a throwaway `sshd` on loopback
and exercises the ssh argv builders and TOFU end-to-end without Docker.

The sandbox images are built from a shared, pinned, multi-arch **image tree**
(`cove-base-image` → `cove-image`) published to GHCR and consumed by both CI and
the sandboxes — see [DEVELOPMENT.md](DEVELOPMENT.md#the-image-tree). The images,
the blessed-base snapshot, and the host binaries are all built by **one
monolithic continuous-delivery pipeline** on every push to `main`, which decides
from the repo diff what to rebuild and tags everything `<N>-<MMDD>` — see
[the release pipeline](DEVELOPMENT.md#ci-the-release-pipeline).

## Further reading

- [`usage/INDEX.md`](usage/INDEX.md) — running the binaries (Jam, `at-mint`, docker-in-sandbox).
- [`DEVELOPMENT.md`](DEVELOPMENT.md) — the dev sandbox, CI and the release pipeline.
- `superpowers/` — the design history (dated specs and plans); the
  [jam-only design](superpowers/specs/2026-10-26-jam-only-design.md) is the current direction.
