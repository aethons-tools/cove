# Jam only — slice 3 "Rename and migrate in one cut" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename everything that remains after slice 2 to the `jam` names of spec §3 in one cut (binaries, images, tags, labels, containers and volumes, env vars, build args, in-image paths, directories, ssh-config markers, release assets, attach wire names, Go identifiers), with no aliases. Serve-start migrations carry a running Jam's own state across. A short runbook covers the upgrade.

**Architecture:** Ten tasks, each ending green (`go build`, `just test`, `just lint`, `go vet -tags integration ./...`, `bash install.test.sh`):

1. A permanent guard test, `renamed_test.go`, starts in Task 1 with one token group. Each later task first adds its own group and watches it fail, then does the rename (mostly through one throwaway helper, `/tmp/jamrename.py`, plus `git mv`) and watches it pass.
2. Old names that migrations and refusals must still recognise live in exactly one package, `internal/legacynames`. That package is on the guard's allow-list.
3. Directory copies run where the existing `migrateConfigDir` already runs: every invocation, before any command.
4. Studio container/volume and kit-tree migrations run at serve start, before the supervisor and the standing reconciler. Both fail closed, so nothing raises onto an empty volume while its pre-rename one still exists.
5. Docs are renamed last (Task 9), and only then does the guard cover them. OVERVIEW/AGENTS and the in-image agent docs get name changes only; slice 4 rewrites them.

**Tech Stack:** Go 1.26 module `github.com/aethons-tools/cove` (unchanged), `just`, bash (`install.sh`, `install.test.sh`, `scripts/*.sh`), goreleaser v2, GitHub Actions, buf (attach proto), Docker/Colima, Postgres (CI parity), Markdown docs checked by `docs_audit.py`.

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-only-design.md`](../specs/2026-10-26-jam-only-design.md) — §3 "Rename and migrate in one cut (slice 3)", §5 Errors, §6 Testing, Decisions 5–7. Precedent: [`docs/usage/jam/renamed-from-harbor.md`](../../usage/jam/renamed-from-harbor.md). Prior slices: [slice 1](2026-10-26-jam-only-slice1-github-issues.md) (#500), [slice 2](2026-10-26-jam-only-slice2-delete.md) (#513).

## Global Constraints

**What gets renamed, and what doesn't**

- **One cut, no aliases** (Decision 7). Nothing reads or writes an old name except:
  - the migrations, which read old names to copy them across;
  - two refusals, which name the old name so the user knows what to change: kit push and the provenance gate;
  - the `install.sh` notice that names stale old binaries.

  The Harbor-era aliases whose names are in the §3 table are removed too:
  - the `at-harbor` binary copy;
  - `AT_HARBOR_*` (env vars, including the dual writes);
  - the `~/.config/at-harbor` copy.

  The serve-config aliases (`runtime.launcher.harbor-host`, `runtime.dispatcher`) and the `studio`←`cove` verb alias are not in the table and stay as they are.
- **Kept on purpose** (spec §3):
  - the admin API and UI routes `/admin/coves…` and `/app/admin/coves…`;
  - the Go module path `github.com/aethons-tools/cove`;
  - the repo name;
  - the Postgres migration advisory-lock key;
  - "cove" as the wire/API word for a studio's sandbox: JSON fields, ids, the `/admin/coves` paths.

  Also kept, because they are persisted inside standing-session volumes or are internal scratch:
  - `/agent-data/.cove-conversation` and `/agent-data/.cove-clear-pending` (renaming them would silently drop resumable conversations);
  - the build-time `/.cove` scratch dir;
  - the `cove-ic` kit name (it names the cove repo).

  The guard's tokens are chosen so none of these match.
- **The rename table.** Spec §3, with the env and staging details this plan fixes:

| Old | New |
|---|---|
| `at-jam` (`cmd/at-jam`) | `jam` (`cmd/jam`) |
| `at-mint github\|anthropic` (`cmd/at-mint`) | `jam mint github\|anthropic` (`internal/mintcmd`; `cmd/at-mint` deleted) |
| `cove-master` (`cmd/cove-master`, `internal/covemaster`, `internal/covemasterbin`, `scripts/stage-covemaster.sh`, staged `covemaster/cove-master-linux-<arch>`) | `jam-studio-agent` (`cmd/jam-studio-agent`, `internal/studioagent`, `internal/studioagentbin`, `scripts/stage-studio-agent.sh`, staged `studioagent/jam-studio-agent-linux-<arch>`) |
| images `ghcr.io/aethons-tools/cove-base-image`, `…/cove-image`; dirs `images/cove-base-image`, `images/cove-image` | `ghcr.io/aethons-tools/jam-studio-base`, `…/jam-studio`; `images/jam-studio-base`, `images/jam-studio` |
| tags `cove-kit:`, `cove-kit-base:`, `cove-kit-draft:`; `$TMP/cove-kit-builds` | `jam-kit:`, `jam-kit-base:`, `jam-kit-draft:`; `$TMP/jam-kit-builds` |
| labels `harbor.cove`, `harbor.cove.state`, `harbor.cove.jam` | `jam.studio`, `jam.studio.state`, `jam.studio.jam` (+ new `jam.studio.migrated-from`, Task 8) |
| containers `atcove-cove-<id>`; volumes `<container>-agent-data`, `-workspace`, `-docker` | `jam-studio-<id>`; same suffixes |
| host env: `AT_JAM_ADMIN_TOKEN`, `AT_LOG_MODE`, `AT_LOG_LEVEL`, `AT_MINT_GITHUB_APP_KEY`, `AT_MINT_AUTH0_CLIENT_SECRET`, `COVE_RUN_TIMEOUT` | `JAM_ADMIN_TOKEN`, `JAM_LOG_MODE`, `JAM_LOG_LEVEL`, `JAM_MINT_GITHUB_APP_KEY`, `JAM_MINT_AUTH0_CLIENT_SECRET`, `JAM_RUN_TIMEOUT` |
| installer env: `COVE_VERSION`, `COVE_SYSTEM`, `COVE_SYSTEM_BINDIR`, `COVE_REPO`, `COVE_INSTALL_LIB` | `JAM_VERSION`, `JAM_SYSTEM`, `JAM_SYSTEM_BINDIR`, `JAM_REPO`, `JAM_INSTALL_LIB` |
| studio env: `AT_JAM_RUNTIME_ADDR`, `AT_JAM_IDENTITY_TOKEN`, `AT_JAM_LAUNCH_SECRET`, `AT_JAM_BASE_URL`, `AT_JAM_CONNECTOR`, `AT_COVE_WORKDIR`, `AT_COVE_AGENT_PROMPT_FILE`, `AT_COVE_AGENT_CONTEXT_FILE`, `AT_COVE_RESIDENT`, `AT_COVE_SESSION_KIND` | `JAM_RUNTIME_ADDR`, `JAM_IDENTITY_TOKEN`, `JAM_LAUNCH_SECRET`, `JAM_BASE_URL`, `JAM_CONNECTOR`, `JAM_WORKDIR`, `JAM_AGENT_PROMPT_FILE`, `JAM_AGENT_CONTEXT_FILE`, `JAM_RESIDENT`, `JAM_SESSION_KIND` |
| `AT_HARBOR_IDENTITY_TOKEN`, `AT_HARBOR_LAUNCH_SECRET`, `AT_HARBOR_RUNTIME_ADDR`, `AT_HARBOR_ADMIN_TOKEN` | dropped (no dual write, no fallback) |
| in-image script env: `COVE_DOCKER`, `COVE_SHADOW_DIRS`, `COVE_KIT_DOMAINS_FILE`, `COVE_SESSION_DOMAINS_FILE`, `COVE_EGRESS_CEILING_FILE`, `COVE_ENV_FILE`, `COVE_SSH_USER_CA_FILE`, `COVE_PLUGIN_{BUILD_CFG,SEED,RUNTIME_CFG,RUN_AS}` | `JAM_` + same suffix |
| test knobs: `AT_JAM_SHUTDOWN_{CHILD,MARKER}`, `AT_JAM_SIGNAL_CHILD`, `COVE_BROWSER`, `COVE_DOCKER_CONTEXT`, `COVE_BOARD_SCREENSHOT`, `COVE_RUNNER_TEST_HELPER`, `COVE_RUN_REPO` | `JAM_` + same suffix |
| build args `COVE_BASE_IMAGE`, `AT_JAM_STUDIO_BASE_IMAGE`, `AT_JAM_STUDIO_TARGET_ARCH`; kit `ENV COVE_SSHENV` | `JAM_STUDIO_BASE_IMAGE` (passed once), `JAM_STUDIO_TARGET_ARCH`; `JAM_SSHENV` |
| in-image `/usr/local/lib/cove/`, `cove-egress.service` (+ the three `*.service.d/cove-egress.conf` drop-ins), `sshd_config.d/cove.conf`, `/usr/local/bin/cove-git-credential.sh`, `/dev/shm/cove-*`, `/agent-data/cove-master.log` | `/usr/local/lib/jam/`, `jam-egress.service` (+ `jam-egress.conf` drop-ins), **`sshd_config.d/00-jam.conf`** (see the next row), `/usr/local/bin/jam-git-credential.sh`, `/dev/shm/jam-*` (`cove-master-env` → `jam-studio-agent-env`), `/agent-data/jam-studio-agent.log` |
| `~/.config/at-jam`, `~/.local/state/at-jam`, launcher `~/.config/at-cove/{id_ed25519,id_ed25519.pub,known_hosts.d}` | `~/.config/jam`, `~/.local/state/jam`, `~/.config/jam/{id_ed25519,id_ed25519.pub,known_hosts.d}` |
| `~/.ssh/config` markers `# >>> at-jam <s> >>>` / `# <<< at-jam <s> <<<` | `# >>> jam <s> >>>` / `# <<< jam <s> <<<` |
| release archive `cove_<ver>_<os>_<arch>.tar.gz` (at-cove tombstone, at-mint, at-jam); goreleaser `project_name: cove`; release.yml job/outputs `atcove`, `cove_image`, env `COVE_IMAGE` | `jam_<ver>_<os>_<arch>.tar.gz` holding exactly `jam`; `project_name: jam`; `binaries`, `studio_image`, `STUDIO_IMAGE` |
| attach gRPC `harbor.attach.v1.Runtime`, metadata `x-harbor-launch-secret` | `jam.attach.v1.Runtime`, `x-jam-launch-secret` |
| Go `AtCoveIdentity`, `LaunchCoveMaster`, `CoveMasterOptions`, `CoveMasterLogVMPath`, `writeCoveMaster`, `coveMaster*`, `naming.CoveContainer` | `AssemblyIdentity`, `LaunchStudioAgent`, `StudioAgentOptions`, `StudioAgentLogVMPath`, `writeStudioAgent`, `studioAgent*`, `naming.StudioContainer` (`AtCoveBuildIdentity` does not exist; nothing to rename) |
| repo dev kit `.at-jam/cove-ic/` | `.jam/cove-ic/` |
| doc `docs/usage/at-mint.md` | `docs/usage/jam-mint.md` |

**Safety rules**

- **The hardening layer, the sealed egress base and the egress ceiling are not weakened.**
  - Paths and units are renamed only.
  - `TestAssembleContextStagesOnlyCoveMaster` (renamed `TestAssembleContextStagesOnlyStudioAgent`) and `TestAssembleContextHarnessStageBetweenBaseAndHardening` keep their ordered-steps assertions, with the new names.
  - Every other `internal/assemble/` test keeps passing with only names changed.
  - The sshd drop-in becomes `00-jam.conf`, not `jam.conf`. sshd keeps the first value it reads for `TrustedUserCAKeys`, reading drop-ins in lexical order. `jam.conf` would sort after `cove.conf`, so a kit-supplied drop-in named `d*.conf`…`i*.conf` would newly take precedence. `00-jam.conf` sorts before both, so precedence never loosens.
- **Secrets never in logs or argv.**
  - Secrets travel the way they do today: identity token, launch secret and mint keys go in the tmpfs env file or child env, never in argv.
  - The migrations log names and paths, never file contents or env values.
  - Volume copies pass only volume names and a fixed script in argv.
- **Assembly identity changes once more**, so every studio image tag changes and every kit rebuilds on its next raise. Expected; documented in the runbook.

**Process**

- **Docs.**
  - `docs/usage/jam/renamed-from-at.md` is new, a leaf of ≤ 200 lines beside `renamed-from-harbor.md`, with a row in `docs/usage/jam/INDEX.md`.
  - Names are changed throughout `docs/usage`, `docs/{INDEX,OVERVIEW,DEVELOPMENT}.md`, `README.md` and `AGENTS.md`, with no rewriting (slice 4 does that).
  - `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` must report no new `ERROR` outside `superpowers/` versus the baseline captured in Task 9 Step 1.
- **Toolchain:** `export GOPROXY=https://proxy.golang.org,direct` before any `go` command. Run `gofmt -w` on every Go file you touch. Postgres tests skip locally (no `JAM_TEST_POSTGRES_DSN`) and run in CI.
- **Gates per task** (all must exit 0):
  - `go build ./... && go vet ./... && go vet -tags integration ./...`
  - `just test`
  - `just lint` (check the exit code)
  - `bash install.test.sh`
  - plus the task's own checks.
- **Commits:** one per task (more is fine). The message ends with a blank line, then `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Branch `feat/jam-rename`; do not push until the user asks.
- **The branch is releasable only as a whole** (after Task 10). Between Tasks 5 and 8, a dev Jam built from the branch would not find pre-rename containers. Don't run a real Jam host from a mid-branch build.

## Review Focus

1. **A standing session's state survives the upgrade.**
   - *Setup:* an operator with standing sessions, some live and some idle with only volumes, upgrades and starts the new `jam serve`.
   - *Expected:* each `atcove-cove-<id>-{agent-data,workspace}` is copied byte- and ownership-intact to `jam-studio-<id>-…`. The old volume is removed only after its copy succeeded.
   - *If the copy fails:* serve exits with `migration: volume <old> could not be copied to <new>; left in place`. Nothing is raised onto an empty new volume. The next start retries, and a partial copy is recognised by its `jam.studio.migrated-from` label.
   - *Pinned by Task 8:*
     - `TestMigrateStateVolumesCopiesThenRemovesOld`
     - `TestMigrateStateVolumesFailedCopyLeavesOldAndFails`
     - `TestMigrateStateVolumesRetriesAPartialCopy`
     - `TestMigrateStateVolumesRefusesAForeignNewVolume`
     - `TestMigrateStateVolumesFindsUnlabelledStandingVolumes`
     - the conformance case on MemStore and Postgres
     - `TestCopyVolumeRealDocker` (integration; skips without docker)
2. **An operator runs their old `at-jam update` against the first `jam` release.**
   - That binary embeds the pre-rename `install.sh`. It asks for `cove_<ver>_<os>_<arch>.tar.gz` and `place`s `at-cove`, `at-mint` and `at-jam`.
   - *Expected:* it fails before touching anything. The asset doesn't exist; old `at-jam`/`at-mint` stay byte-identical and the running service keeps working. The runbook's one-command install then works.
   - *Pinned by Task 7:* `install.test.sh` "compat" cases drive the frozen `testdata/install-legacy.sh` against the new release and, as a control, against an old-format release.
3. **A kit written against the old names rebuilds after the upgrade.**
   - *Kits covered:* `ARG COVE_BASE_IMAGE` / `FROM ${COVE_BASE_IMAGE}`, `AT_JAM_STUDIO_*`, `ENV COVE_SSHENV`, a `base: image: ghcr.io/aethons-tools/cove-base-image@sha256:…` pin, and a child kit pinned to the parent's pre-migration version.
   - *Expected:* serve start writes a new version (`Meta.Origin = "migration"`) of each such kit's current tree with the new names, and re-pins the child to the parent's migrated version. The launcher then passes exactly `JAM_STUDIO_BASE_IMAGE`/`JAM_STUDIO_TARGET_ARCH`.
   - A tree that can't be rewritten keeps its version and opens `kit.migrate:<name>`. A later push still using an old name is refused with the new name in the message.
   - *Pinned by Task 3* (build-arg argv) *and Task 8:*
     - `TestMigrateKitRenameRewritesCurrentTree`
     - `TestMigrateKitRenameRepinsAChildPinnedToTheOldParent`
     - `TestMigrateKitRenameKeepsAnUnrewritableKit`
     - `TestMigrateKitRenameIsIdempotent`
     - `TestPushKitFilesRefusesPreRenameNames`
4. **A studio still running on an old image when the new Jam starts.**
   - Its `cove-master` speaks `harbor.attach.v1` and reads `AT_JAM_*`, so it can never attach to the new Jam.
   - *Expected:* the serve-start migration removes the old `atcove-cove-<id>` container, its `-docker` cache and its pinned `known_hosts.d` entry. It records the new `Location`, clears `ImageTag`/`KitDraftImage` and marks the instance `lost`.
   - The supervisor's first reconcile finishes that teardown through the normal path, and the standing reconciler re-raises declared standing sessions onto their copied volumes. Every other studio is re-raised by the operator: runbook step 5, which covers it.
   - *Pinned by Task 8:*
     - `TestRetireLegacyStudioRemovesContainerCacheAndKnownHosts`
     - `TestMigrateStudioNamesRetiresAndRewrites`
     - `TestMigratedStudioIsTornDownByReconcile`
     - `TestMigrateStudioNamesIsIdempotent`
5. **The provenance gate after the base image moves.**
   - *Expected:* the launcher's default base and the blessed list name `ghcr.io/aethons-tools/jam-studio-base`. A kit base under the re-cut repo that descends from a blessed base is accepted.
   - A kit that still names `ghcr.io/aethons-tools/cove-base-image` (via `image.base`) is refused even with `--allow-unverified-base`, because the gate compares layers and would otherwise pass identical old-repo layers silently. The message names the new repo.
   - *Pinned by Task 4:*
     - `TestResolveAcceptsRecutBlessedBase`
     - `TestResolveRefusesThePreRenameRepo`
     - `TestResolveRefusesThePreRenameRepoEvenUnverified`
     - `TestDefaultRefIsTheRecutRepo`
     - the pre-merge `gen-blessed` check of Task 4 Step 9

---

## Rulings this plan makes beyond the spec

1. **The old `at-jam update` fails cleanly. It does not "keep working".**
   - The new release ships only `jam_<ver>_*`. An old updater asks for `cove_<ver>_*`, gets "no asset" and exits non-zero before `place` runs. Nothing changes on disk.
   - Shims were rejected. A compat `cove_*` archive would have to carry either a real `jam` under the name `at-jam` (an alias, forbidden), or a tombstone `at-jam`. The tombstone would silently break a running service on its next restart.
   - The spec already says the upgrade is a manual install. `scripts/release/at-cove` and the tombstone check are deleted.
   - The new `install.sh` *notices* (never deletes) stale `at-jam`/`at-mint`/`at-cove` in the install dir.
2. **Directory copies run in Task 6, beside the rename of the directories**, through the existing `migrateConfigDir` hook (renamed `migrateDirs`, every invocation). They are not part of the Task 8 serve-start set.
   - This keeps the old dirs never unread in any commit.
   - The launcher key is copied before serve's `keys.EnsureFile` can mint a fresh one.
3. **Studio state migration fails closed.** Any volume copy or removal failure stops serve start: rather an operator retry than a standing session raised onto an empty workspace.
   - Instances are written one row at a time. The store has no multi-instance transaction, and each row follows its own docker side effect, so per-row writes are what makes a retry idempotent.
   - Kit rewrites are one atomic `PutKitDocs` per pass.
4. **Pre-rename containers are removed by the migration**, not left for teardown. A container the new Jam can't name would otherwise leak, and it can't attach anyway.
   - The `-docker` cache volume is removed, not copied (teardown already drops it).
   - `-shadow-*` volumes do not exist in code (spec text only), so there is nothing to migrate.
5. **Kits.**
   - **Scope of the rewrite.** The migration rewrites each kit's *current* version only: `kit.yml` plus files named `Dockerfile`, `Dockerfile.*`, `*.Dockerfile` or `Containerfile`.
   - **Re-pinning.** It re-pins a child pinned to a pre-migration parent version.
   - **Drafts are not rewritten.** Publishing one that still uses old names is refused by the same push-time check, which names the new name.
   - **Git-sourced and config-managed kits** show drift until their source is edited; the runbook says so.
6. **Reserved env prefix.** It becomes `JAM_` (was `AT_JAM_`/`AT_HARBOR_`), in both `modelspec.ReservedEnvKey` and the IDE's `secretEnvPrefixes`.
   - IDE terminals now drop every `JAM_*`, including the non-secret `JAM_WORKDIR` etc. That is stricter than before, never looser.
   - Stored operator env keys that newly fall under `JAM_` are dropped at injection by the existing defensive filter and refused on the next edit.
7. **In-image agent docs** (`images/jam-studio-base/image-files/home/agent/.init-agent-data/`):
   - Only names of renamed *things* change here: `.refresh`'s `/usr/local/lib/jam/seed-agent-data.sh` (functional, Task 3) and `cove-base-image` → `jam-studio-base` (Task 4).
   - Their `at-cove` workflow text describes a deleted CLI and is slice 4's rewrite. The guard allows the single token `at-cove` under that directory, and in `docs/OVERVIEW.md`'s interim note, nowhere else.
8. **`jam mint`.**
   - `cmd/at-mint`'s code moves to `internal/mintcmd` (exported `Run`). This avoids `main`/`run`/test-helper collisions in `cmd/jam`.
   - `internal/mint` invokes the running binary itself (`os.Executable()`, fallback `jam`) with `mint github|anthropic`.
9. **Re-cutting the blessed bases is an operator action before merge** (Task 4 Step 9).
   - Use `crane copy`, which preserves digests, for every release-tagged `cove-base-image` version from the watermark (`732-1005`) onward, oldest first, plus `latest`, into `jam-studio-base`.
   - `gen-blessed` must then succeed against the new package. Otherwise the merge's release job fails on `Walk`.

---

## File Structure

| Area | Created | Deleted / moved | Modified (main ones) |
|---|---|---|---|
| Guard + legacy names | `renamed_test.go`, `internal/legacynames/{legacynames.go,legacynames_test.go}` | — | `deleted_test.go` (paths) |
| Go identifiers (T1) | — | `internal/covemaster/` → `internal/studioagent/` (`covemaster.go` → `studioagent.go`), `internal/covemasterbin/` → `internal/studioagentbin/` (`covemasterbin*.go` → `studioagentbin*.go`), `internal/connect/covemaster{,_test}.go` → `studioagent{,_test}.go` | `internal/install/identity*.go`, `internal/naming/naming*.go`, `internal/assemble/assemble*.go`, `internal/jam/launcher/*`, every importer |
| Env + wire (T2) | `internal/connect/studioagent_env_test.go`, `internal/jam/attach/wire_test.go` | `cmd/cove-master/rename_test.go` (replaced by `env_test.go`) | `internal/connect/studioagent.go`, `internal/jam/snippet/*`, `cmd/cove-master/{main,mcp}.go`, `internal/studioagent/ide.go`, `internal/jam/modelspec/validate.go`, `cmd/at-jam/{clientconfig,rename_test}.go`, `internal/logging/*`, `internal/jam/attach/{proto/attach.proto,server.go,attachpb/*}`, `internal/studioagent/client.go`, hardening scripts, harness payload scripts, `cmd/at-mint/*`, `internal/mint/*` |
| In-image (T3) | `internal/assemble/names_test.go` | hardening `usr/local/lib/cove/` → `usr/local/lib/jam/`, `cove-egress.service` → `jam-egress.service`, `*.service.d/cove-egress.conf` → `jam-egress.conf`, `sshd_config.d/cove.conf` → `00-jam.conf`, `cove-git-credential.sh` → `jam-git-credential.sh` | `internal/assemble/{hardening/Dockerfile,assemble.go,*_test.go}`, `internal/backend/colima/{baseimage,sshca,egress}.go`, `internal/connect/studioagent.go`, `internal/agentrun/*`, `internal/jam/setup_image_bodies.go` + goldens, `images/cove-image/Dockerfile`, `.at-jam/cove-ic/context/Dockerfile`, `scripts/lint.sh`, `.refresh` |
| Images (T4) | — | `images/cove-base-image/` → `images/jam-studio-base/`, `images/cove-image/` → `images/jam-studio/` | `internal/basedigest/*`, `internal/blessgen/*`, `cmd/adopt-base/main.go`, `internal/adoptbase/*`, `internal/baseimage/{resolve.go,resolve_test.go}`, `.github/workflows/release.yml`, `scripts/build-images.sh`, `renovate.json`, `.hadolint.yaml` |
| Tags/labels/containers (T5) | — | — | `internal/naming/*`, `internal/jam/launcher/{launcher,asm,draft,prepare}.go`, `internal/backend/colima/baseimage.go`, `internal/jam/kit_draft_build.go`, tests |
| Dirs + ssh (T6) | `cmd/at-jam/dirs_migrate_test.go`, `cmd/at-jam/ssh_legacy_test.go` | `cmd/at-jam/rename_test.go`'s at-harbor dir tests | `cmd/at-jam/{clientconfig,config,ssh}.go` |
| Binaries + release (T7) | `internal/mintcmd/{mintcmd.go,…}` (moved), `cmd/jam/mint_test.go` | `cmd/at-jam/` → `cmd/jam/`, `cmd/cove-master/` → `cmd/jam-studio-agent/`, `cmd/at-mint/` (code → `internal/mintcmd/`), `scripts/stage-covemaster.sh` → `scripts/stage-studio-agent.sh`, `scripts/release/at-cove` (deleted), `.at-jam/` → `.jam/` | `.goreleaser.yaml`, `.github/workflows/release.yml`, `scripts/release/check-archive.sh`, `install.sh`, `install.test.sh`, `internal/update/*`, `installer.go`, `scripts/build.sh`, `justfile`, `.air.toml`, `.gitignore`, `dev/*`, `deploy/monitoring/jam-rules.yml`, `web/app/*`, `internal/mint/*`, hardening Dockerfile, `internal/jam/sessionctx/boilerplate.go` |
| Migrations (T8) | `internal/jam/launcher/rename_migrate{,_test}.go`, `internal/jam/rename_migrate{,_test}.go`, `internal/jam/kit_rename_migrate{,_test}.go`, `internal/jam/storetest/rename_migrations.go`, `internal/backend/colima/volume_copy{,_test,_integration_test}.go` | — | `internal/backend/backend.go`, `internal/jam/launcher/launcher_test.go` (fakeOps), `internal/jam/kit_push.go`, `internal/jam/kit_migrate.go`, `internal/jam/storetest/conformance.go`, `cmd/jam/main.go` |
| Docs (T9) | `docs/usage/jam/renamed-from-at.md` | `docs/usage/at-mint.md` → `docs/usage/jam-mint.md` | `docs/usage/**`, `docs/{INDEX,OVERVIEW,DEVELOPMENT}.md`, `README.md`, `AGENTS.md`, `renamed-from-harbor.md`, `renamed_test.go` |

### Tooling used by several tasks

Create once before Task 1 (not committed):

```bash
cat > /tmp/jamrename.py <<'EOF'
#!/usr/bin/env python3
"""jamrename.py [--dry] [--docs] MAPFILE [PATHSPEC...]

Apply MAPFILE's replacements, in file order, to every tracked UTF-8 text file
under PATHSPECs (default: the whole tree). A MAPFILE line is OLD<TAB>NEW
(literal) or re:REGEX<TAB>NEW (Python regex; NEW is inserted literally).
Blank lines and #-comments are ignored. Prints "path: N" per changed file.
Without --docs the docs (docs/, README.md, AGENTS.md, CLAUDE.md) are skipped;
the allow-listed history/legacy files are always skipped."""
import re, subprocess, sys

ALWAYS_SKIP = (
    "docs/superpowers/",
    "docs/usage/jam/renamed-from-harbor.md",
    "docs/usage/jam/renamed-from-at.md",
    "internal/legacynames/",
    "renamed_test.go",
    "deleted_test.go",
    "testdata/install-legacy.sh",
    "install.sh",
    "install.test.sh",
    ".gitignore",
)
DOCS = ("docs/", "README.md", "AGENTS.md", "CLAUDE.md")

def main():
    args = sys.argv[1:]
    dry, docs = "--dry" in args, "--docs" in args
    args = [a for a in args if a not in ("--dry", "--docs")]
    if not args:
        sys.exit(__doc__)
    rules = []
    for raw in open(args[0], encoding="utf-8"):
        line = raw.rstrip("\n")
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        old, new = line.split("\t")
        rx = re.compile(old[3:]) if old.startswith("re:") else re.compile(re.escape(old))
        rules.append((rx, new))
    specs = args[1:] or ["."]
    out = subprocess.run(["git", "ls-files", "-z", "--", *specs],
                         capture_output=True, check=True).stdout.decode()
    total = 0
    for f in out.split("\0"):
        if not f or f.startswith(ALWAYS_SKIP):
            continue
        if not docs and f.startswith(DOCS):
            continue
        try:
            s = open(f, encoding="utf-8").read()
        except (UnicodeDecodeError, FileNotFoundError, IsADirectoryError):
            continue
        if "\0" in s:
            continue
        t, n = s, 0
        for rx, new in rules:
            t, k = rx.subn(lambda m: new, t)
            n += k
        if n:
            total += n
            print(f"{f}: {n}")
            if not dry:
                open(f, "w", encoding="utf-8").write(t)
    print(f"-- {total} replacements")

main()
EOF
chmod +x /tmp/jamrename.py
```

Run it with `--dry` first, read the file list, then without. After every run: `gofmt -w $(git diff --name-only -- '*.go')`.

---

### Task 1: Guard, legacy names, and the Go identifiers and packages

**Files:**
- Create: `renamed_test.go`, `internal/legacynames/legacynames.go`, `internal/legacynames/legacynames_test.go`
- Move: `internal/covemaster/` → `internal/studioagent/` (and `covemaster.go` → `studioagent.go` inside it), `internal/covemasterbin/` → `internal/studioagentbin/` (`covemasterbin.go` → `studioagentbin.go`, `covemasterbin_test.go` → `studioagentbin_test.go`), `internal/connect/covemaster.go` → `internal/connect/studioagent.go`, `internal/connect/covemaster_test.go` → `internal/connect/studioagent_test.go`
- Modify: `internal/naming/naming.go` (`SafeID`, `StudioContainer`), `internal/install/identity.go` (+ test), `internal/assemble/assemble.go` (+ tests), `internal/jam/launcher/{launcher,asm}.go` (+ tests), `scripts/stage-covemaster.sh` (output dir only), every importer the helper touches

**Interfaces:**
- Produces:
  - `install.AssemblyIdentity() (string, error)`
  - `connect.LaunchStudioAgent(r runner.Runner, o StudioAgentOptions) error`
  - `connect.StudioAgentOptions`, `connect.StudioAgentLogVMPath`
  - packages `internal/studioagent` (`package studioagent`) and `internal/studioagentbin` (`studioagentbin.Binary(goarch)`, `studioagentbin.BinFS()`)
  - `naming.SafeID(actorID string) string`
  - `naming.StudioContainer(actorID string) string` (value unchanged in this task: `atcove-cove-<id>`)
  - from `internal/legacynames`:
    - constants `ContainerPrefix`, `StateLabel`, `JamLabel`, `ConfigDir`, `StateDir`, `LauncherDir`, `SSHBlockBegin`, `SSHBlockEnd`, `BaseImageRepo`
    - `type KitRename struct{ Old, New string }`, `var KitRenames []KitRename`
    - `Container(actorID string) string`
    - `RewriteKitText(s string) (string, bool)`
    - `FindKitName(s string) (string, bool)`
    - `NewKitName(old string) string`
    - `KitFileScanned(path string) bool`
  - Guard: `renamedGroups []renamedGroup`, `renamedAllow`, `docsPending(rel string) bool` in `renamed_test.go` (package `cove`). Later tasks append to `renamedGroups` and `renamedAllow`.
- Consumes: `gitLsFiles(t, …)` from `deleted_test.go`.

- [ ] **Step 1: Write the legacy-names package and its test**

`internal/legacynames/legacynames.go`:

```go
// Package legacynames holds the pre-rename names (jam-only slice 3,
// docs/usage/jam/renamed-from-at.md) that Jam's one-time migrations and its
// refusals must still recognise. Nothing else may spell an old name: the
// renamed guard (renamed_test.go) allows them only here.
package legacynames

import (
	"path"
	"regexp"
	"strings"

	"github.com/aethons-tools/cove/internal/naming"
)

const (
	// ContainerPrefix starts a pre-rename studio container's name: atcove-cove-<id>.
	ContainerPrefix = "atcove-cove-"
	// StateLabel is a pre-rename state volume's owner label (value: actor id).
	StateLabel = "harbor.cove.state"
	// JamLabel is a pre-rename state volume's Jam label (value: the Jam id).
	JamLabel = "harbor.cove.jam"
	// ConfigDir is the pre-rename CLI config directory under $XDG_CONFIG_HOME.
	ConfigDir = "at-jam"
	// StateDir is the pre-rename state directory under $XDG_STATE_HOME.
	StateDir = "at-jam"
	// LauncherDir held the launcher's id_ed25519 and known_hosts.d.
	LauncherDir = "at-cove"
	// SSHBlockBegin and SSHBlockEnd prefix a pre-rename ~/.ssh/config block's markers.
	SSHBlockBegin = "# >>> at-jam "
	SSHBlockEnd   = "# <<< at-jam "
	// BaseImageRepo is the pre-rename base image repository.
	BaseImageRepo = "ghcr.io/aethons-tools/cove-base-image"
	// newBaseImageRepo is its new name (basedigest.Image after Task 4).
	newBaseImageRepo = "ghcr.io/aethons-tools/jam-studio-base"
)

// KitRename is one name a stored kit may still use and the name Jam passes now.
type KitRename struct{ Old, New string }

// KitRenames are the build-arg and ENV names (whole words) and the base image
// repository (a literal) a kit's kit.yml or Dockerfile used before the rename.
var KitRenames = []KitRename{
	{"AT_JAM_STUDIO_BASE_IMAGE", "JAM_STUDIO_BASE_IMAGE"},
	{"AT_JAM_STUDIO_TARGET_ARCH", "JAM_STUDIO_TARGET_ARCH"},
	{"COVE_BASE_IMAGE", "JAM_STUDIO_BASE_IMAGE"},
	{"COVE_SSHENV", "JAM_SSHENV"},
	{BaseImageRepo, newBaseImageRepo},
}

var kitNameRe = regexp.MustCompile(`\b(AT_JAM_STUDIO_BASE_IMAGE|AT_JAM_STUDIO_TARGET_ARCH|COVE_BASE_IMAGE|COVE_SSHENV)\b|` +
	regexp.QuoteMeta(BaseImageRepo))

// Container is actorID's pre-rename container name.
func Container(actorID string) string { return ContainerPrefix + naming.SafeID(actorID) }

// NewKitName is old's new name (old itself when it is not a KitRenames name).
func NewKitName(old string) string {
	for _, r := range KitRenames {
		if r.Old == old {
			return r.New
		}
	}
	return old
}

// RewriteKitText is s with every pre-rename kit name replaced; changed
// reports whether anything was.
func RewriteKitText(s string) (out string, changed bool) {
	out = kitNameRe.ReplaceAllStringFunc(s, NewKitName)
	return out, out != s
}

// FindKitName is the first pre-rename kit name in s, if any.
func FindKitName(s string) (string, bool) {
	m := kitNameRe.FindString(s)
	return m, m != ""
}

// KitFileScanned reports whether a kit file is one the rename rewrites and
// push refuses on: kit.yml and Dockerfiles.
func KitFileScanned(p string) bool {
	b := path.Base(p)
	return b == "kit.yml" || b == "Dockerfile" || b == "Containerfile" ||
		strings.HasPrefix(b, "Dockerfile.") || strings.HasSuffix(b, ".Dockerfile")
}
```

`internal/legacynames/legacynames_test.go`:

```go
package legacynames

import "testing"

func TestRewriteKitText(t *testing.T) {
	in := "ARG COVE_BASE_IMAGE\nARG AT_JAM_STUDIO_TARGET_ARCH\nFROM ${COVE_BASE_IMAGE}\n" +
		"ENV COVE_SSHENV=\"GOPATH\"\nENV MY_COVE_BASE_IMAGE=keep\n# " + BaseImageRepo + "@sha256:ab\n"
	want := "ARG JAM_STUDIO_BASE_IMAGE\nARG JAM_STUDIO_TARGET_ARCH\nFROM ${JAM_STUDIO_BASE_IMAGE}\n" +
		"ENV JAM_SSHENV=\"GOPATH\"\nENV MY_COVE_BASE_IMAGE=keep\n# ghcr.io/aethons-tools/jam-studio-base@sha256:ab\n"
	got, changed := RewriteKitText(in)
	if !changed || got != want {
		t.Fatalf("RewriteKitText =\n%s\nwant\n%s", got, want)
	}
	if again, changed := RewriteKitText(got); changed || again != got {
		t.Fatalf("a second rewrite changed %q", again)
	}
}

func TestFindKitNameAndScannedFiles(t *testing.T) {
	if old, ok := FindKitName("FROM ${AT_JAM_STUDIO_BASE_IMAGE}"); !ok || old != "AT_JAM_STUDIO_BASE_IMAGE" {
		t.Fatalf("FindKitName = %q, %v", old, ok)
	}
	if _, ok := FindKitName("FROM ${JAM_STUDIO_BASE_IMAGE}"); ok {
		t.Fatal("a new name is not a pre-rename one")
	}
	for p, want := range map[string]bool{"kit.yml": true, "context/Dockerfile": true, "a/b.Dockerfile": true,
		"Dockerfile.dev": true, "Containerfile": true, "install.sh": false, "README.md": false} {
		if KitFileScanned(p) != want {
			t.Errorf("KitFileScanned(%q) != %v", p, want)
		}
	}
}

func TestContainer(t *testing.T) {
	if got := Container("ses_1/x"); got != "atcove-cove-ses_1-x" {
		t.Fatalf("Container = %q", got)
	}
}
```

- [ ] **Step 2: Export `naming.SafeID` and add `StudioContainer` (value unchanged)**

In `internal/naming/naming.go`, replace `CoveContainer` with:

```go
// SafeID is actorID with every docker-unsafe character replaced by '-'.
func SafeID(actorID string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, actorID)
}

// StudioContainer names a managed studio's container from its actor id,
// docker-safe (SafeID).
func StudioContainer(actorID string) string { return prefix + "-cove-" + SafeID(actorID) }
```

(Task 5 changes the value.)

- [ ] **Step 3: Write the guard with the first group**

`renamed_test.go`:

```go
package cove

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

// renamedGroup is one jam-rename task's old names
// (docs/superpowers/specs/2026-10-26-jam-only-design.md §3). Once a group is
// listed, no tracked file outside renamedAllow may contain any of its tokens,
// in its content or its path.
type renamedGroup struct {
	name   string
	tokens []string // Go regexp sources
}

var renamedGroups = []renamedGroup{
	{"go identifiers", []string{`AtCove`, `CoveMaster`, `coveMaster`, `covemasterbin`,
		`internal/covemaster\b`, `package covemaster`, `\bcovemaster\.[A-Z]`, `CoveContainer`}},
}

// renamedAllow is where an old name may survive: the design history, the
// Harbor-rename table, the migrations' legacy names, the frozen legacy
// installer, and the guards. only lists the token sources allowed there
// (nil: every token).
var renamedAllow = []struct {
	prefix string
	only   []string
}{
	{"docs/superpowers/", nil},
	{"docs/usage/jam/renamed-from-harbor.md", nil},
	{"internal/legacynames/", nil},
	{"renamed_test.go", nil},
	{"deleted_test.go", nil},
	{"testdata/install-legacy.sh", nil},
}

// docsPending skips the docs until Task 9 of the slice-3 plan renames them.
func docsPending(rel string) bool {
	return strings.HasPrefix(rel, "docs/") || rel == "README.md" || rel == "AGENTS.md" || rel == "CLAUDE.md" ||
		strings.Contains(rel, "/.init-agent-data/")
}

func renamedAllowed(rel, token string) bool {
	for _, a := range renamedAllow {
		if !strings.HasPrefix(rel, a.prefix) {
			continue
		}
		if a.only == nil {
			return true
		}
		for _, o := range a.only {
			if o == token {
				return true
			}
		}
	}
	return false
}

func TestNoPreRenameNames(t *testing.T) {
	type tok struct {
		group, src string
		re         *regexp.Regexp
	}
	var toks []tok
	for _, g := range renamedGroups {
		for _, s := range g.tokens {
			toks = append(toks, tok{g.name, s, regexp.MustCompile(s)})
		}
	}
	for _, rel := range gitLsFiles(t) {
		if docsPending(rel) {
			continue
		}
		b, err := os.ReadFile(rel)
		if err != nil || bytes.IndexByte(b, 0) >= 0 {
			b = nil // gone from the tree, or binary: check its path only
		}
		for _, k := range toks {
			if renamedAllowed(rel, k.src) {
				continue
			}
			if k.re.MatchString(rel) {
				t.Errorf("%s: path keeps the pre-rename name %q (%s)", rel, k.src, k.group)
			}
			if loc := k.re.FindIndex(b); loc != nil {
				line := 1 + bytes.Count(b[:loc[0]], []byte("\n"))
				t.Errorf("%s:%d: %q survives the jam rename (%s)", rel, line, b[loc[0]:loc[1]], k.group)
			}
		}
	}
}
```

- [ ] **Step 4: Run the guard and the new package tests to verify they fail as expected**

Run: `git add internal/legacynames renamed_test.go && go test ./internal/legacynames/ && go test -run TestNoPreRenameNames .`
Expected:
- `legacynames` PASS. `go build ./...` now fails on the remaining `naming.CoveContainer` callers; that is expected, and the Step 5 map fixes it.
- `TestNoPreRenameNames` FAIL, listing files with `AtCoveIdentity`, `LaunchCoveMaster`, `CoveMasterOptions`, `internal/covemaster`, `covemasterbin`, `CoveContainer`, ….

- [ ] **Step 5: Move the packages and run the identifier map**

```bash
git mv internal/covemaster internal/studioagent
git mv internal/studioagent/covemaster.go internal/studioagent/studioagent.go
git mv internal/covemasterbin internal/studioagentbin
git mv internal/studioagentbin/covemasterbin.go internal/studioagentbin/studioagentbin.go
git mv internal/studioagentbin/covemasterbin_test.go internal/studioagentbin/studioagentbin_test.go
git mv internal/connect/covemaster.go internal/connect/studioagent.go
git mv internal/connect/covemaster_test.go internal/connect/studioagent_test.go
cat > /tmp/map-t1.tsv <<'EOF'
AtCoveIdentity	AssemblyIdentity
LaunchCoveMaster	LaunchStudioAgent
CoveMasterOptions	StudioAgentOptions
CoveMasterLogVMPath	StudioAgentLogVMPath
CoveMaster	StudioAgent
coveMaster	studioAgent
covemasterbin	studioagentbin
re:internal/covemaster\b	internal/studioagent
re:package covemaster\b	package studioagent
re:\bcovemaster\.(?=[A-Z])	studioagent.
CoveContainer	StudioContainer
EOF
/tmp/jamrename.py --dry /tmp/map-t1.tsv && /tmp/jamrename.py /tmp/map-t1.tsv
gofmt -w $(git diff --name-only -- '*.go') internal/studioagent internal/studioagentbin internal/connect
```

The `covemasterbin` rule also rewrites `scripts/stage-covemaster.sh`'s output dir (`internal/studioagentbin/bin`) and the error text in `studioagentbin.lookup`. The *binary file* names `cove-master-linux-<arch>` and the build-context dir `covemaster/` stay until Task 7.

- [ ] **Step 6: Fix the doc comments the map left reading oddly**

Run `git grep -n 'StudioAgent\|studioAgent' -- '*.go' | grep '//'` and read each hit. A comment that now says "the StudioAgent" where it meant the binary `cove-master` keeps the word `cove-master` until Task 7; only the Go identifier changed. `internal/install/identity.go`'s doc comment for `AssemblyIdentity` should open with "AssemblyIdentity hashes the binary's embedded build contributions…".

- [ ] **Step 7: Run the guard and the full gates**

Run:
```bash
go test -run TestNoPreRenameNames . && go build ./... && go vet ./... && go vet -tags integration ./... && just test && just lint; echo "lint exit $?"; bash install.test.sh
```
Expected: all PASS, `lint exit 0`.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor: jam rename — Go identifiers and packages (studioagent, AssemblyIdentity) + renamed guard

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Environment variables and the attach wire names

**Files:**
- Create: `internal/connect/studioagent_env_test.go`, `internal/jam/attach/wire_test.go`, `cmd/cove-master/env_test.go`
- Delete: `cmd/cove-master/rename_test.go`
- Modify:
  - `internal/connect/studioagent.go`
  - `internal/jam/snippet/{snippet,connector}.go` (+ tests)
  - `cmd/cove-master/{main,mcp}.go`
  - `internal/studioagent/{ide,client}.go` (+ tests)
  - `internal/jam/modelspec/validate.go` (+ test)
  - `internal/jam/policy.go` (doc comment)
  - `cmd/at-jam/clientconfig.go`, `cmd/at-jam/rename_test.go`
  - `internal/logging/{deprecated,logging}.go` (+ tests)
  - `internal/jam/attach/{proto/attach.proto,server.go,server_test.go,attachpb/doc.go}` and the regenerated `attachpb/*.pb.go`
  - `internal/assemble/hardening/image-files/usr/local/{bin/entrypoint.sh,bin/cove-git-credential.sh,lib/cove/*.sh}`
  - `internal/backend/colima/colima.go`
  - `internal/harnessinstall/payload/claude/*.sh`
  - `cmd/at-mint/*`, `internal/mint/*`
  - test-knob users found by the map

**Interfaces:**
- Consumes: `connect.LaunchStudioAgent`, `connect.StudioAgentOptions` (Task 1).
- Produces:
  - Studio env names: `JAM_RUNTIME_ADDR`, `JAM_IDENTITY_TOKEN`, `JAM_LAUNCH_SECRET`, `JAM_BASE_URL`, `JAM_CONNECTOR`, `JAM_WORKDIR`, `JAM_AGENT_PROMPT_FILE`, `JAM_AGENT_CONTEXT_FILE`, `JAM_RESIDENT`, `JAM_SESSION_KIND`.
  - Host env: `JAM_ADMIN_TOKEN`, `JAM_LOG_MODE`, `JAM_MINT_*`, `JAM_RUN_TIMEOUT`.
  - `modelspec.ReservedEnvKey(key string) bool` (true for `JAM_*`).
  - Wire: `attachpb.Runtime_ServiceDesc.ServiceName == "jam.attach.v1.Runtime"`; metadata key `x-jam-launch-secret`.

- [ ] **Step 1: Add the guard group**

Append to `renamedGroups` in `renamed_test.go`:

```go
	{"env and wire", []string{`\bAT_(COVE|HARBOR|MINT|LOG)_`,
		`\bAT_JAM_(ADMIN|IDENTITY|RUNTIME|LAUNCH|BASE|CONNECTOR|SHUTDOWN|SIGNAL|TOKEN|TEST|X)`,
		`\bCOVE_(DOCKER|SHADOW_DIRS|PLUGIN_|RUN_|KIT_DOMAINS_FILE|SESSION_DOMAINS_FILE|EGRESS_CEILING_FILE|ENV_FILE|SSH_USER_CA_FILE|BROWSER|BOARD_SCREENSHOT|RUNNER_TEST_HELPER)`,
		`harbor\.attach\.v1`, `x-harbor-launch-secret`}},
```

- [ ] **Step 2: Write the failing behaviour tests**

`internal/connect/studioagent_env_test.go`:

```go
package connect

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// The env a studio's agent is launched with names only JAM_* (jam rename):
// no AT_JAM_*, AT_COVE_* or dual-written AT_HARBOR_*.
func TestLaunchStudioAgentEnvNamesOnlyJam(t *testing.T) {
	fake := &runner.Fake{}
	err := LaunchStudioAgent(fake, StudioAgentOptions{
		Target:  sshargs.Target{Host: "h", User: "agent", Port: 2222, IdentityFile: "k", KnownHostsFile: "kh"},
		JamHost: "jam.example.com", RuntimeAddr: "jam.example.com:443",
		IdentityToken: "tok-123", LaunchSecret: "sec-456",
		WorkDir: "/home/agent/workspace", Prompt: "p", Resident: true, SessionKind: "standing",
	})
	if err != nil {
		t.Fatal(err)
	}
	var env string
	for _, c := range fake.Calls {
		if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "cat > "+studioAgentEnvVMPath) {
			env = c.Stdin
		}
	}
	for _, want := range []string{"export JAM_RUNTIME_ADDR=", "export JAM_LAUNCH_SECRET=", "export JAM_IDENTITY_TOKEN=tok-123",
		"export JAM_WORKDIR=", "export JAM_AGENT_PROMPT_FILE=", "export JAM_BASE_URL=", "export JAM_RESIDENT=1",
		"export JAM_SESSION_KIND="} {
		if !strings.Contains(env, want) {
			t.Errorf("env script lacks %q:\n%s", want, env)
		}
	}
	if old := regexp.MustCompile(`\bAT_[A-Z]+_`).FindString(env); old != "" {
		t.Errorf("env script still names %q:\n%s", old, env)
	}
	if strings.Count(env, "tok-123") != 1 || strings.Count(env, "sec-456") != 1 {
		t.Errorf("each secret must be written exactly once:\n%s", env)
	}
}
```

(`Resident` and `SessionKind` are existing `StudioAgentOptions` fields; check with `grep -n 'Resident\|SessionKind' internal/connect/studioagent.go` and use the field names found there.)

`internal/jam/attach/wire_test.go`:

```go
package attach

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// The jam rename renamed the Attach wire: service and launch-secret metadata.
func TestAttachWireNames(t *testing.T) {
	if got := attachpb.Runtime_ServiceDesc.ServiceName; got != "jam.attach.v1.Runtime" {
		t.Fatalf("service = %q", got)
	}
	if attachpb.Runtime_Attach_FullMethodName != "/jam.attach.v1.Runtime/Attach" {
		t.Fatalf("attach method = %q", attachpb.Runtime_Attach_FullMethodName)
	}
	if mdLaunchSecret != "x-jam-launch-secret" {
		t.Fatalf("launch-secret metadata key = %q", mdLaunchSecret)
	}
}
```

Add to `internal/jam/modelspec/validate_test.go` (create the test function in the existing file):

```go
func TestReservedEnvKeyIsJAM(t *testing.T) {
	// The pre-rename prefixes are spelled split ("AT_" + …) so the renamed guard
	// (renamed_test.go) does not flag this test for naming them.
	for k, want := range map[string]bool{"JAM_X": true, "JAM_IDENTITY_TOKEN": true, "AT_" + "JAM_X": false, "AT_" + "HARBOR_X": false, "MY_JAM_X": false} {
		if ReservedEnvKey(k) != want {
			t.Errorf("ReservedEnvKey(%q) != %v", k, want)
		}
	}
}
```

`cmd/cove-master/env_test.go` replaces `rename_test.go`. Read `rename_test.go` first; its `buildConfig` helper call shape is reused.

```go
package main

import "testing"

// old spells a pre-rename variable split, so the renamed guard
// (renamed_test.go) does not flag this test for naming it.
func old(prefix, suffix string) string { return "AT_" + prefix + "_" + suffix }

// The agent reads only JAM_* names; a pre-rename env is ignored (one cut, no
// fallback).
func TestBuildConfigReadsOnlyJamNames(t *testing.T) {
	env := map[string]string{
		"JAM_RUNTIME_ADDR": "new:443", "JAM_IDENTITY_TOKEN": "t", "JAM_LAUNCH_SECRET": "s",
		old("JAM", "RUNTIME_ADDR"): "old:443", old("HARBOR", "RUNTIME_ADDR"): "older:443",
	}
	cfg, err := buildConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeAddr != "new:443" {
		t.Fatalf("RuntimeAddr = %q", cfg.RuntimeAddr)
	}
	if _, err := buildConfig(func(k string) string {
		return map[string]string{old("JAM", "RUNTIME_ADDR"): "old:443", old("JAM", "IDENTITY_TOKEN"): "t", old("JAM", "LAUNCH_SECRET"): "s"}[k]
	}); err == nil {
		t.Fatal("pre-rename names alone must not configure the agent")
	}
}
```

If `buildConfig`'s signature or the config field differs, mirror `TestBuildConfigNewNamesWin` in the old `rename_test.go` exactly, changing only the names. Then `git rm cmd/cove-master/rename_test.go`.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/connect/ ./internal/jam/attach/ ./internal/jam/modelspec/ ./cmd/cove-master/ && go test -run TestNoPreRenameNames .`
Expected: FAIL. The env script names `AT_JAM_*`, the service is `harbor.attach.v1.Runtime`, `ReservedEnvKey("JAM_X")` is false, and the guard lists the env/wire hits.

- [ ] **Step 4: Remove every `AT_HARBOR_*` read and write by hand**

The map must not rename these; they go. Each site:

- `internal/connect/studioagent.go`: delete the comment and the two `script.WriteString("export AT_HARBOR_…")` lines.
- `internal/jam/snippet/snippet.go`: delete `legacyTokenVar` and the export it renders. The credential-helper fallback `${AT_JAM_IDENTITY_TOKEN:-$AT_HARBOR_IDENTITY_TOKEN}` becomes `${AT_JAM_IDENTITY_TOKEN}`; Step 5's map then makes it `${JAM_IDENTITY_TOKEN}`. Fix `snippet_test.go`'s expectations of `AT_HARBOR_*` (delete those lines/asserts).
- `cmd/cove-master/main.go`: `jamEnv(getenv, suffix)` returns `getenv("AT_JAM_" + suffix)` only; delete the fallback and its doc lines. Same in `mcp.go`'s readers.
- `internal/studioagent/ide.go`: `var secretEnvPrefixes = []string{"AT_JAM_"}` (Step 5 makes it `JAM_`). Update its comment to "the agent's own credentials and settings (JAM_*)". Fix `ide_test.go`.
- `internal/jam/modelspec/validate.go`: `return strings.HasPrefix(key, "AT_JAM_")`, with comment "the Jam-owned env namespace (JAM_*)".
- `cmd/at-jam/clientconfig.go`: `adminTokenEnv` returns `getenv("AT_JAM_ADMIN_TOKEN")` only, and `resolveToken`'s loop has one name. In `cmd/at-jam/rename_test.go`, replace `TestAdminTokenOldEnvAloneWorksAndWarns` with:

```go
func TestAdminTokenOldEnvIgnored(t *testing.T) {
	t.Setenv("JAM_ADMIN_TOKEN", "")
	// Spelled split so the renamed guard (renamed_test.go) does not flag them.
	t.Setenv("AT_"+"HARBOR_ADMIN_TOKEN", "old")
	t.Setenv("AT_"+"JAM_ADMIN_TOKEN", "older")
	if got := adminTokenEnv(); got != "" {
		t.Fatalf("adminTokenEnv() = %q; pre-rename names must be ignored", got)
	}
}
```

  If `adminTokenEnv` takes a getenv parameter in this file, pass `os.Getenv`. Mirror the call in `TestAdminTokenNewEnvWins`.
- `internal/jam/connector_test.go`: drop the `{"AT_HARBOR_IDENTITY_TOKEN": "x"}` reserved case.
- The hardening git credential helper (`usr/local/bin/cove-git-credential.sh`): if it reads `AT_HARBOR_IDENTITY_TOKEN`, delete that fallback.

Then `git grep -n 'AT_HARBOR_' -- ':!docs' ':!internal/legacynames'` must list nothing except `renamed_test.go`.

- [ ] **Step 5: Run the env/wire map**

```bash
cat > /tmp/map-t2.tsv <<'EOF'
re:\bAT_JAM_(?!STUDIO_)	JAM_
re:\bAT_COVE_	JAM_
re:\bAT_MINT_	JAM_MINT_
re:\bAT_LOG_	JAM_LOG_
re:\bCOVE_(?=(DOCKER|SHADOW_DIRS|PLUGIN_|RUN_|KIT_DOMAINS_FILE|SESSION_DOMAINS_FILE|EGRESS_CEILING_FILE|ENV_FILE|SSH_USER_CA_FILE|BROWSER|BOARD_SCREENSHOT|RUNNER_TEST_HELPER))	JAM_
harbor.attach.v1	jam.attach.v1
x-harbor-launch-secret	x-jam-launch-secret
EOF
/tmp/jamrename.py --dry /tmp/map-t2.tsv . ':(exclude)internal/jam/attach/attachpb/*.pb.go'
/tmp/jamrename.py /tmp/map-t2.tsv . ':(exclude)internal/jam/attach/attachpb/*.pb.go'
```

Then edit `internal/jam/attach/proto/attach.proto` lines 2–5 and `attachpb/doc.go`, which explain the name was "kept on purpose": the comment now says the service is `jam.attach.v1.Runtime` and that studios built before the jam rename cannot attach (they are retired at serve start, `docs/usage/jam/renamed-from-at.md`). Do the same for the comment above `const Label` in `internal/jam/launcher/launcher.go`, which says the harbor name is kept; Task 5 changes the value, so reword it then.

- [ ] **Step 6: Regenerate the attach code**

Run: `just buf-gen && git diff --stat internal/jam/attach/attachpb`
Expected: `attach.pb.go` and `attach_grpc.pb.go` change; `grep -c harbor internal/jam/attach/attachpb/*.go` prints 0 for each. If the generator versions differ from the committed header, keep the regenerated files as they are.

- [ ] **Step 7: Run the tests to verify they pass, then the gates**

Run: `go test ./internal/connect/ ./internal/jam/attach/ ./internal/jam/modelspec/ ./cmd/cove-master/ ./cmd/at-jam/ ./internal/jam/snippet/ ./internal/studioagent/ && go test -run TestNoPreRenameNames .`, then the full gates (Global Constraints).
Expected: PASS. `install.test.sh` is untouched by this task, but it is part of the gates and still passes.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor!: jam rename — env vars to JAM_*, drop AT_HARBOR_*, attach wire jam.attach.v1

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: In-image paths, units, build args and the assembly

**Files:**
- Create: `internal/assemble/names_test.go`
- Move:
  - `internal/assemble/hardening/image-files/usr/local/lib/cove/` → `…/usr/local/lib/jam/`
  - `…/etc/systemd/system/cove-egress.service` → `jam-egress.service`
  - `…/etc/systemd/system/{ssh,squid,docker}.service.d/cove-egress.conf` → `jam-egress.conf`
  - `…/etc/ssh/sshd_config.d/cove.conf` → `00-jam.conf`
  - `…/usr/local/bin/cove-git-credential.sh` → `jam-git-credential.sh`
- Modify:
  - `internal/assemble/hardening/Dockerfile`
  - `internal/assemble/{assemble.go,assemble_test.go,embed_test.go,harness_layer_test.go,image_scripts_test.go,install_ssh_ca_test.go}`
  - `internal/backend/colima/{baseimage.go,sshca.go,egress.go}` (+ tests), `internal/backend/backend.go` (comments)
  - `internal/connect/studioagent.go`, `internal/agentrun/{harness_claude,workload,spawner,linesplit}.go` (+ tests)
  - `internal/studio/base.go`
  - `internal/jam/setup_image_bodies.go` + `testdata/setup/{role,studio}.golden`
  - kit test fixtures, `internal/jam/testdata/kitmigrate/context.golden`
  - `images/cove-image/Dockerfile`, `.at-jam/cove-ic/context/Dockerfile`
  - `images/cove-base-image/image-files/home/agent/.init-agent-data/.refresh`
  - `scripts/lint.sh`

**Interfaces:**
- Consumes: `install.AssemblyIdentity`, `connect.StudioAgentLogVMPath` (Task 1).
- Produces:
  - in-image paths `/usr/local/lib/jam/{apply-sshenv,apply-role-egress,apply-session-domains,install-ssh-ca,seed-agent-data}.sh`
  - `jam-egress.service`, `/etc/ssh/sshd_config.d/00-jam.conf`, `/usr/local/bin/jam-git-credential.sh`
  - `connect.StudioAgentLogVMPath == "/agent-data/jam-studio-agent.log"`
  - kit build args exactly `JAM_STUDIO_BASE_IMAGE`, `JAM_STUDIO_TARGET_ARCH`; image ENV `JAM_SSHENV`

- [ ] **Step 1: Add the guard group**

```go
	{"in-image paths and build args", []string{`lib/cove\b`, `cove-egress`, `sshd_config\.d/cove\.conf`,
		`cove-git-credential`, `/dev/shm/cove-`, `cove-master\.log`, `\bAT_JAM_`, `\bCOVE_BASE_IMAGE\b`, `\bCOVE_SSHENV\b`}},
```

- [ ] **Step 2: Write the failing name test**

`internal/assemble/names_test.go`:

```go
package assemble

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// The sealed hardening layer's files carry the jam names (slice 3), and the
// sshd drop-in sorts first: sshd keeps the first TrustedUserCAKeys it reads,
// in lexical order, so the sealed one must precede any kit-supplied drop-in.
func TestHardeningLayerNames(t *testing.T) {
	var paths []string
	if err := fs.WalkDir(HardeningFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	has := func(suffix string) bool {
		return slices.ContainsFunc(paths, func(p string) bool { return strings.HasSuffix(p, suffix) })
	}
	for _, want := range []string{
		"etc/systemd/system/jam-egress.service",
		"etc/systemd/system/ssh.service.d/jam-egress.conf",
		"etc/systemd/system/squid.service.d/jam-egress.conf",
		"etc/systemd/system/docker.service.d/jam-egress.conf",
		"etc/ssh/sshd_config.d/00-jam.conf",
		"usr/local/bin/jam-git-credential.sh",
		"usr/local/lib/jam/apply-sshenv.sh",
		"usr/local/lib/jam/apply-role-egress.sh",
		"usr/local/lib/jam/apply-session-domains.sh",
		"usr/local/lib/jam/install-ssh-ca.sh",
		"usr/local/lib/jam/seed-agent-data.sh",
	} {
		if !has(want) {
			t.Errorf("hardening layer lacks %s", want)
		}
	}
	var dropins []string
	for _, p := range paths {
		if strings.Contains(p, "etc/ssh/sshd_config.d/") {
			dropins = append(dropins, p[strings.LastIndex(p, "/")+1:])
		}
		if strings.Contains(p, "cove") {
			t.Errorf("hardening path %s keeps a pre-rename name", p)
		}
	}
	if len(dropins) != 1 || !strings.HasPrefix(dropins[0], "00-") {
		t.Fatalf("sshd drop-ins = %v; want exactly one, sorting first (00-…)", dropins)
	}
}
```

Add to `internal/backend/colima/baseimage_test.go` a check inside the existing test that asserts the build-arg argv. Find it with `grep -n 'build-arg' internal/backend/colima/baseimage_test.go`. In its `for _, want := range` list, replace the three pre-rename entries with `"--build-arg JAM_STUDIO_BASE_IMAGE="` and `"--build-arg JAM_STUDIO_TARGET_ARCH="`, and add after it:

```go
	if n := strings.Count(argv, "--build-arg JAM_STUDIO_BASE_IMAGE="); n != 1 {
		t.Fatalf("JAM_STUDIO_BASE_IMAGE passed %d times; want once:\n%s", n, argv)
	}
	if strings.Contains(argv, "COVE_") || strings.Contains(argv, "AT_JAM_") {
		t.Fatalf("pre-rename build arg passed:\n%s", argv)
	}
```

(`argv` is the joined docker args the existing test builds; use that test's variable name.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/assemble/ -run TestHardeningLayerNames; go test ./internal/backend/colima/ -run BaseImage; go test -run TestNoPreRenameNames .`
Expected: FAIL. The layer lacks `jam-egress.service` etc.; the argv passes `COVE_BASE_IMAGE`; the guard lists hits.

- [ ] **Step 4: Move the hardening files**

```bash
h=internal/assemble/hardening/image-files
git mv $h/usr/local/lib/cove $h/usr/local/lib/jam
git mv $h/etc/systemd/system/cove-egress.service $h/etc/systemd/system/jam-egress.service
for u in ssh squid docker; do git mv $h/etc/systemd/system/$u.service.d/cove-egress.conf $h/etc/systemd/system/$u.service.d/jam-egress.conf; done
git mv $h/etc/ssh/sshd_config.d/cove.conf $h/etc/ssh/sshd_config.d/00-jam.conf
git mv $h/usr/local/bin/cove-git-credential.sh $h/usr/local/bin/jam-git-credential.sh
```

- [ ] **Step 5: Run the in-image map**

```bash
cat > /tmp/map-t3.tsv <<'EOF'
/usr/local/lib/cove/	/usr/local/lib/jam/
re:\blib/cove\b	lib/jam
cove-egress	jam-egress
sshd_config.d/cove.conf	sshd_config.d/00-jam.conf
cove-git-credential	jam-git-credential
/dev/shm/cove-master-env	/dev/shm/jam-studio-agent-env
/dev/shm/cove-	/dev/shm/jam-
/agent-data/cove-master.log	/agent-data/jam-studio-agent.log
cove-master.log	jam-studio-agent.log
re:\bAT_JAM_STUDIO_BASE_IMAGE\b	JAM_STUDIO_BASE_IMAGE
re:\bAT_JAM_STUDIO_TARGET_ARCH\b	JAM_STUDIO_TARGET_ARCH
re:\bCOVE_BASE_IMAGE\b	JAM_STUDIO_BASE_IMAGE
re:\bCOVE_SSHENV\b	JAM_SSHENV
EOF
/tmp/jamrename.py --dry /tmp/map-t3.tsv && /tmp/jamrename.py /tmp/map-t3.tsv
```

This also rewrites `images/cove-base-image/image-files/home/agent/.init-agent-data/.refresh`. That rewrite is intended: it calls the seed script by path.

- [ ] **Step 6: Hand-finish what the map can't**

- `internal/backend/colima/baseimage.go` (both build sites, ~lines 36–38 and 114–116): the map leaves two `"--build-arg", "JAM_STUDIO_BASE_IMAGE="+d.baseArg,` lines. Delete the second, so it reads:

```go
		"--build-arg", "JAM_STUDIO_BASE_IMAGE="+d.baseArg,
		"--build-arg", "JAM_STUDIO_TARGET_ARCH="+runtime.GOARCH,
```

  Rewrite the comments above (lines ~20, 28–32, 152) to say Jam passes `JAM_STUDIO_BASE_IMAGE` and `JAM_STUDIO_TARGET_ARCH`, nothing else.
- `.at-jam/cove-ic/context/Dockerfile`: the map leaves `ARG JAM_STUDIO_BASE_IMAGE=scratch` / `FROM ${JAM_STUDIO_BASE_IMAGE}` / `ARG JAM_STUDIO_TARGET_ARCH`. Check no duplicate `ARG` line remains.
- Kit fixtures that declared both `ARG COVE_BASE_IMAGE` and `ARG AT_JAM_STUDIO_BASE_IMAGE` now repeat `ARG JAM_STUDIO_BASE_IMAGE`. Find them with `git grep -n -c 'ARG JAM_STUDIO_BASE_IMAGE' -- '*_test.go' '*.golden' | grep -v ':1$'` and delete the duplicate line.
- `internal/assemble/assemble_test.go` `TestAssembleContextStagesOnlyCoveMaster`'s ordered list now reads `"systemctl enable jam-egress.service squid.service"` and `"/usr/local/lib/jam/apply-sshenv.sh"` (the map did it). Confirm no step was dropped: the list still has 8 entries in the original order. Do the same for `harness_layer_test.go`'s `"RUN /usr/local/lib/jam/apply-sshenv.sh"`.
- `internal/assemble/hardening/Dockerfile` comments say "cove-egress"/"COVE_SSHENV": the map renamed them. Read the file once top to bottom and confirm only names changed (`git diff -w internal/assemble/hardening/Dockerfile`).

- [ ] **Step 7: Regenerate the setup goldens and run the tests**

Run: `go test ./internal/jam/ -run Setup -update 2>/dev/null || true; git diff --stat internal/jam/testdata/setup`. Use the golden-update flag the setup tests define: find it with `grep -n 'flag.Bool' internal/jam/*_test.go`. Then:
`go test ./internal/assemble/ ./internal/backend/colima/ ./internal/connect/ ./internal/agentrun/ ./internal/jam/... && go test -run TestNoPreRenameNames .`
Expected: PASS. The golden diff shows only `COVE_SSHENV` → `JAM_SSHENV`.

- [ ] **Step 8: Gates, then commit**

Run the full gates. `just lint` runs shellcheck/hadolint over the moved scripts via the renamed `usr/local/lib/jam/*.sh` glob in `scripts/lint.sh`.

```bash
git add -A
git commit -m "refactor!: jam rename — in-image paths, jam-egress unit, 00-jam.conf, JAM_STUDIO_* build args, JAM_SSHENV

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Image names, GHCR repos, blessed bases re-cut, and the gate's refusal of the old repo

**Files:**
- Move: `images/cove-base-image/` → `images/jam-studio-base/`, `images/cove-image/` → `images/jam-studio/`
- Modify:
  - `internal/basedigest/{basedigest.go,basedigest_test.go,blessed/watermark.txt}`
  - `internal/blessgen/{blessgen.go,blessgen_test.go}`, `cmd/gen-blessed/main.go` (comments)
  - `cmd/adopt-base/main.go`, `internal/adoptbase/{adoptbase.go,adoptbase_test.go}`
  - `internal/baseimage/{resolve.go,resolve_test.go}`
  - `.github/workflows/release.yml`, `scripts/build-images.sh`, `renovate.json`, `.hadolint.yaml`
  - `images/jam-studio-base/Dockerfile`, `images/jam-studio/Dockerfile`
  - `.at-jam/cove-ic/kit.yml`, `.at-jam/cove-ic/context/Dockerfile` (comments)
  - `deleted_test.go` (`mentionExempt` path)

**Interfaces:**
- Consumes: `legacynames.BaseImageRepo` (Task 1).
- Produces:
  - `basedigest.Image == "ghcr.io/aethons-tools/jam-studio-base"`
  - `baseimage.Resolve` refuses a kit-chosen base under `legacynames.BaseImageRepo`, with error text `base image ghcr.io/aethons-tools/cove-base-image was renamed ghcr.io/aethons-tools/jam-studio-base; update the kit (docs/usage/jam/renamed-from-at.md)`

- [ ] **Step 1: Add the guard group**

```go
	{"images", []string{`\bcove-base\b`, `\bcove-image\b`, `\bCOVE_IMAGE\b`}},
```

- [ ] **Step 2: Write the failing tests**

In `internal/baseimage/resolve_test.go`:
- change the constant to `blessedRef = "ghcr.io/aethons-tools/jam-studio-base@sha256:blessed"`;
- change `TestResolveBaseTag`'s `tag` to `"ghcr.io/acme/jam-studio@sha256:child"`;
- add the import `"github.com/aethons-tools/cove/internal/legacynames"` and:

```go
func TestResolveAcceptsRecutBlessedBase(t *testing.T) {
	const kitBase = "ghcr.io/aethons-tools/jam-studio-base@sha256:older"
	d := &fakeDocker{layers: map[string][]string{
		blessedRef: {"sha256:a", "sha256:b"},
		kitBase:    {"sha256:a", "sha256:b"},
	}}
	if ref, err := Resolve(d, Spec{Base: kitBase}, []string{blessedRef}, &bytes.Buffer{}); err != nil || ref != kitBase {
		t.Fatalf("Resolve = %q, %v; a re-cut blessed base must pass", ref, err)
	}
}

func TestResolveRefusesThePreRenameRepo(t *testing.T) {
	old := legacynames.BaseImageRepo + "@sha256:blessed"
	d := &fakeDocker{layers: map[string][]string{blessedRef: {"sha256:a"}, old: {"sha256:a"}}}
	_, err := Resolve(d, Spec{Base: old}, []string{blessedRef}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "was renamed ghcr.io/aethons-tools/jam-studio-base") {
		t.Fatalf("err = %v; the old repo must be refused, naming the new one", err)
	}
}

func TestResolveRefusesThePreRenameRepoEvenUnverified(t *testing.T) {
	old := legacynames.BaseImageRepo + ":latest"
	d := &fakeDocker{layers: map[string][]string{blessedRef: {"sha256:a"}, old: {"sha256:a"}}}
	var warn bytes.Buffer
	if _, err := Resolve(d, Spec{Base: old, AllowUnverified: true}, []string{blessedRef}, &warn); err == nil {
		t.Fatal("--allow-unverified-base downgrades provenance failures only, not the rename refusal")
	}
}
```

In `internal/basedigest/basedigest_test.go` add:

```go
func TestDefaultRefIsTheRecutRepo(t *testing.T) {
	if Image != "ghcr.io/aethons-tools/jam-studio-base" {
		t.Fatalf("Image = %q", Image)
	}
	if !strings.HasPrefix(DefaultRef(), "ghcr.io/aethons-tools/jam-studio-base@sha256:") {
		t.Fatalf("DefaultRef = %q", DefaultRef())
	}
	for _, r := range BlessedRefs() {
		if !strings.HasPrefix(r, Image+"@") {
			t.Fatalf("blessed ref %q is not under %s", r, Image)
		}
	}
}
```

(Add the `strings` import if absent.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/baseimage/ ./internal/basedigest/; go test -run TestNoPreRenameNames .`
Expected:
- `TestResolveRefusesThePreRenameRepo*` FAIL: the gate passes identical layers.
- `TestDefaultRefIsTheRecutRepo` FAIL.
- The guard lists `cove-base`/`cove-image` hits, including the `images/cove-*` paths.

- [ ] **Step 4: Implement the refusal**

In `internal/baseimage/resolve.go`, add imports `basedigest` and `legacynames`, and at the top of `Resolve`, right after `resolveRef` returns:

```go
	if userChosen && isPreRenameRepo(ref) {
		return "", fmt.Errorf("base image %s was renamed %s; update the kit (docs/usage/jam/renamed-from-at.md)",
			legacynames.BaseImageRepo, basedigest.Image)
	}
```

and below `resolveRef`:

```go
// isPreRenameRepo reports whether ref names the pre-rename base repository
// (by tag or digest). The layer gate alone would pass it: a re-cut base has
// the same layers under the new name.
func isPreRenameRepo(ref string) bool {
	rest, ok := strings.CutPrefix(ref, legacynames.BaseImageRepo)
	return ok && (rest == "" || rest[0] == '@' || rest[0] == ':')
}
```

Check `go list -deps ./internal/basedigest | grep baseimage` prints nothing (no cycle). `basedigest` embeds only data.

- [ ] **Step 5: Move the image dirs and run the image map**

```bash
git mv images/cove-base-image images/jam-studio-base
git mv images/cove-image images/jam-studio
cat > /tmp/map-t4.tsv <<'EOF'
ghcr.io/aethons-tools/cove-base-image	ghcr.io/aethons-tools/jam-studio-base
ghcr.io/aethons-tools/cove-image	ghcr.io/aethons-tools/jam-studio
images/cove-base-image	images/jam-studio-base
images/cove-image	images/jam-studio
cove-base-image	jam-studio-base
re:\bcove-image\b	jam-studio
re:\bcove-base\b	jam-studio-base
re:\bCOVE_IMAGE\b	STUDIO_IMAGE
cove_image	studio_image
EOF
/tmp/jamrename.py --dry /tmp/map-t4.tsv && /tmp/jamrename.py /tmp/map-t4.tsv
```

The helper skips `deleted_test.go`. Edit its `mentionExempt` entry by hand to `"images/jam-studio-base/image-files/home/agent/.init-agent-data/"`.

- [ ] **Step 6: Hand-finish**

- `cmd/adopt-base/main.go`: rename the constant `coveBasePkg` → `basePkg` (value now `"jam-studio-base"`).
- `internal/adoptbase/adoptbase.go`: the watermark comment format reads `"# Current watermark: jam-studio-base:%s (%s)"`. Update `adoptbase_test.go`.
- `internal/basedigest/blessed/watermark.txt`: the comment names `jam-studio-base:732-1005`. The digest line is unchanged; the re-cut copies it.
- `images/jam-studio-base/Dockerfile`:
  - line 1 comment → `# jam-studio-base — …`;
  - `LABEL org.opencontainers.image.description="jam studio base image: …"` (keep the rest of the text);
  - keep `org.opencontainers.image.source` pointing at `aethons-tools/cove`, so GHCR links the new package to this repo.
- `images/jam-studio/Dockerfile`: `FROM jam-studio-base:${BASE_TAG}`.
- `renovate.json`: the map turned `["cove-base-image","cove-base"]` into `["jam-studio-base","jam-studio-base"]`. Dedupe to `["jam-studio-base"]`.
- `.github/workflows/release.yml`:
  - `env: BASE_IMAGE: ghcr.io/aethons-tools/jam-studio-base`, `STUDIO_IMAGE: ghcr.io/aethons-tools/jam-studio`;
  - local tags `jam-studio-base:ci` / `jam-studio:ci`;
  - path filters `images/jam-studio-base/**` / `images/jam-studio/**`;
  - output `studio_image`.

  Read the `legs` step: its shell variable `COVE` (`COVE: ${{ steps.filter.outputs.cove_image }}`) becomes `STUDIO: ${{ steps.filter.outputs.studio_image }}` with `if [ "$STUDIO" = 'true' ]`, and its variable `cove` becomes `studio`. The `atcove` job/output is Task 7's.

- [ ] **Step 7: Run the tests to verify they pass, then the gates**

Run: `go test ./internal/baseimage/ ./internal/basedigest/ ./internal/blessgen/ ./internal/adoptbase/ ./cmd/adopt-base/ ./cmd/gen-blessed/ && go test -run 'TestNoPreRenameNames|TestNoMentionsOfDeletedBinaries' .`, then the full gates.
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor!: jam rename — images jam-studio-base/jam-studio; gate refuses the pre-rename base repo

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 9: Re-cut the blessed bases (operator, before the PR merges): STOP and ask the user**

This step needs `write:packages` on the `aethons-tools` org, which the sandbox does not have. Post these commands to the user and wait for confirmation. Mark the step done only when they report the final check passing.

```bash
# 1. Auth docker to GHCR (crane reads ~/.docker/config.json).
gh auth token | docker login ghcr.io -u "$(gh api user --jq .login)" --password-stdin
# 2. Release-tagged versions of the old package, oldest first.
gh api --paginate /orgs/aethons-tools/packages/container/cove-base-image/versions \
  --jq '.[] | select(.metadata.container.tags | map(select(test("^(sha-|.*-(amd64|arm64)$)") | not)) | length > 0)
        | [.created_at, .name, (.metadata.container.tags | join(","))] | @tsv' | sort > /tmp/base-versions.tsv
# 3. Keep the watermark (tag 732-1005) and every newer line; inspect the file, then copy oldest→newest.
awk -F'\t' '/(^|,)732-1005(,|$)/{f=1} f' /tmp/base-versions.tsv > /tmp/recut.tsv
while IFS=$'\t' read -r _ digest tags; do
  for tag in ${tags//,/ }; do
    case "$tag" in sha-*|*-amd64|*-arm64) continue;; esac
    go run github.com/google/go-containerregistry/cmd/crane@latest copy \
      "ghcr.io/aethons-tools/cove-base-image@$digest" "ghcr.io/aethons-tools/jam-studio-base:$tag"
    got=$(go run github.com/google/go-containerregistry/cmd/crane@latest digest "ghcr.io/aethons-tools/jam-studio-base:$tag")
    [ "$got" = "$digest" ] || { echo "digest changed for $tag: $got != $digest"; exit 1; }
  done
done < /tmp/recut.tsv
# 4. GHCR: Packages → jam-studio-base → Package settings → Manage Actions access: add aethons-tools/cove (Write).
#    The first CI push will create jam-studio; give it the same access once it exists.
# 5. Check: the blessed walk succeeds against the new package and keeps the watermark.
GITHUB_TOKEN=$(gh auth token) go run ./cmd/gen-blessed && grep -c sha256 internal/basedigest/blessed/generated.txt
git checkout -- internal/basedigest/blessed/generated.txt 2>/dev/null || rm -f internal/basedigest/blessed/generated.txt
```

Expected: every copy prints the same digest; `gen-blessed` exits 0 and `generated.txt` holds ≥ 1 digest (the watermark first-or-later). If `gen-blessed` fails with "watermark not found", step 3 missed the watermark's tag. Rerun step 3 for that line.

---

### Task 5: Tag prefixes, labels, container and volume names

**Files:**
- Modify:
  - `internal/naming/{naming.go,naming_test.go}`
  - `internal/jam/launcher/{launcher.go,asm.go,draft.go,prepare.go,inventory.go}` (+ tests)
  - `internal/backend/colima/baseimage.go` (+ `{baseimage,buildlog,colima,dispatch}_test.go`)
  - `internal/jam/kit_draft_build.go` (+ tests)
  - fixtures in `cmd/at-jam/{image,main,kitdraft_build}_test.go`, `internal/jam/{adminui,…}/*_test.go`

**Interfaces:**
- Consumes: `naming.SafeID`, `naming.StudioContainer` (Task 1).
- Produces:
  - `naming.StudioContainer(id) == "jam-studio-" + SafeID(id)`
  - `launcher.Label == "jam.studio"`, `launcher.StateLabel == "jam.studio.state"`, `launcher.JamLabel == "jam.studio.jam"`
  - kit image tags `jam-kit:<kit32>-<asm32>`, `jam-kit-base:<id>`, `jam-kit-draft:<kit>-<tree12>`
  - default `Config.BuildRoot = $TMPDIR/jam-kit-builds`

- [ ] **Step 1: Add the guard group**

```go
	{"tags, labels, containers", []string{`cove-kit`, `harbor\.cove`, `atcove-cove`, `"atcove"`}},
```

- [ ] **Step 2: Write the failing tests**

Replace the body of the container test in `internal/naming/naming_test.go` (find it with `grep -n 'func Test' internal/naming/naming_test.go`), or add:

```go
func TestStudioNames(t *testing.T) {
	c := StudioContainer("ses_1/x")
	if c != "jam-studio-ses_1-x" {
		t.Fatalf("StudioContainer = %q", c)
	}
	if AgentDataVolume(c) != "jam-studio-ses_1-x-agent-data" || WorkspaceVolume(c) != "jam-studio-ses_1-x-workspace" ||
		DockerVolume(c) != "jam-studio-ses_1-x-docker" {
		t.Fatal("volumes must be named after the new container")
	}
}
```

Add to `internal/jam/launcher/asm_test.go`:

```go
func TestStudioLabelsAndTags(t *testing.T) {
	if Label != "jam.studio" || StateLabel != "jam.studio.state" || JamLabel != "jam.studio.jam" {
		t.Fatalf("labels = %q %q %q", Label, StateLabel, JamLabel)
	}
	if got := tagFor("sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 64)); !strings.HasPrefix(got, "jam-kit:") {
		t.Fatalf("tagFor = %q", got)
	}
	if !strings.HasPrefix(draftTagPrefix, "jam-kit-draft:") {
		t.Fatalf("draftTagPrefix = %q", draftTagPrefix)
	}
	if got := New(Config{}).cfg.BuildRoot; filepath.Base(got) != "jam-kit-builds" {
		t.Fatalf("BuildRoot = %q", got)
	}
}
```

(Imports `path/filepath`, `strings`. If `tagFor`'s first parameter is not the `sha256:`-prefixed digest, pass what `asm_test.go`'s existing test passes.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/naming/ ./internal/jam/launcher/ -run 'TestStudioNames|TestStudioLabelsAndTags'; go test -run TestNoPreRenameNames .`
Expected: FAIL (`atcove-cove-…`, `harbor.cove`, `cove-kit:`).

- [ ] **Step 4: Change the definitions**

- `internal/naming/naming.go`: delete `const prefix = "atcove"`. Make `StudioContainer` return `"jam-studio-" + SafeID(actorID)`. Rewrite the package comment: "a studio's container (jam-studio-<id>, StudioContainer) and the volumes named after it; every name starts `jam-studio-`, so one studio's resources sort together and never collide with an unrelated docker object."
- `internal/jam/launcher/launcher.go`: `const Label = "jam.studio"` (replace its comment: "Label tags every studio container this launcher runs."), `StateLabel = "jam.studio.state"`, `JamLabel = "jam.studio.jam"`, `cfg.BuildRoot = filepath.Join(os.TempDir(), "jam-kit-builds")`.

- [ ] **Step 5: Run the tags/labels map over everything else**

```bash
cat > /tmp/map-t5.tsv <<'EOF'
cove-kit-builds	jam-kit-builds
cove-kit-base:	jam-kit-base:
cove-kit-draft:	jam-kit-draft:
cove-kit:	jam-kit:
re:\bcove-kit\b	jam-kit
harbor.cove.state	jam.studio.state
harbor.cove.jam	jam.studio.jam
harbor.cove	jam.studio
atcove-cove-	jam-studio-
EOF
/tmp/jamrename.py --dry /tmp/map-t5.tsv && /tmp/jamrename.py /tmp/map-t5.tsv
```

Read the `--dry` list. Any production `.go` file other than `asm.go`, `draft.go`, `prepare.go`, `baseimage.go`, `kit_draft_build.go`, `inventory.go` and `naming.go` should be a comment change only; check each with `git diff`.

- [ ] **Step 6: Run the tests to verify they pass, then the gates**

Run: `go test ./internal/naming/ ./internal/jam/... ./internal/backend/... ./cmd/at-jam/ && go test -run TestNoPreRenameNames .`, then the full gates.
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor!: jam rename — jam-kit tags, jam.studio labels, jam-studio-<id> containers and volumes

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Directories, the launcher defaults, ssh-config markers, and the directory migration

**Files:**
- Create: `cmd/at-jam/dirs_migrate_test.go`, `cmd/at-jam/ssh_legacy_test.go`
- Modify:
  - `cmd/at-jam/clientconfig.go`: `configDir`, `xdgConfigHome`, `xdgStateHome`, `migrateDirs`, `copyEntry`; delete `legacyConfigDir` and `configDirNamed`
  - `cmd/at-jam/config.go`: delete `atCoveConfigDir` and `atJamConfigDir`; `atJamStateDir` → `jamStateDir`; launcher defaults
  - `cmd/at-jam/main.go` (`runAs` calls `migrateDirs`)
  - `cmd/at-jam/ssh.go` (markers, `stripLegacySSHBlocks`)
  - `cmd/at-jam/rename_test.go` (delete the at-harbor dir tests)
  - `cmd/at-jam/{ssh_test,launcher_key_test,config_test}.go` (expectations)
  - `internal/logging/deprecated.go` (`RenamedFromAtDoc`)

**Interfaces:**
- Consumes: `legacynames.ConfigDir`, `StateDir`, `LauncherDir`, `SSHBlockBegin`, `SSHBlockEnd` (Task 1); `JAM_LOG_MODE` (Task 2).
- Produces:
  - `configDir() string` (= `$XDG_CONFIG_HOME/jam`)
  - `jamStateDir() (string, error)` (= `$XDG_STATE_HOME/jam`)
  - `migrateDirs(stderr io.Writer)`
  - `stripLegacySSHBlocks(config string) (string, []string)`
  - `logging.RenamedFromAtDoc = "docs/usage/jam/renamed-from-at.md"`
  - ssh markers `# >>> jam <studio> >>>` / `# <<< jam <studio> <<<`

- [ ] **Step 1: Add the guard group**

```go
	{"directories and markers", []string{`\.config/at-(cove|jam|harbor)`, `state/at-jam`, `[<>]{3} at-jam`,
		`atCoveConfigDir`, `atJamConfigDir`, `atJamStateDir`, `legacyConfigDir`}},
```

- [ ] **Step 2: Write the failing tests**

`cmd/at-jam/dirs_migrate_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/legacynames"
)

func writeFile(t *testing.T, path, data string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), perm); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string, perm os.FileMode) string {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if fi.Mode().Perm() != perm {
		t.Fatalf("%s mode %v, want %v", path, fi.Mode().Perm(), perm)
	}
	b, _ := os.ReadFile(path)
	return string(b)
}

// One jam run copies the pre-rename config, state and launcher directories to
// their new homes (modes kept, old ones left, contents never logged); a second
// run copies nothing.
func TestMigrateDirsCopiesConfigStateAndLauncher(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", state)
	oldCfg, oldLaunch, oldState := filepath.Join(cfg, legacynames.ConfigDir), filepath.Join(cfg, legacynames.LauncherDir), filepath.Join(state, legacynames.StateDir)
	writeFile(t, filepath.Join(oldCfg, "settings.yml"), "token: SECRET-VALUE\n", 0o600)
	writeFile(t, filepath.Join(oldCfg, "ssh", "s1-cert.pub"), "cert", 0o600)
	writeFile(t, filepath.Join(oldLaunch, "id_ed25519"), "PRIVATE-KEY", 0o600)
	writeFile(t, filepath.Join(oldLaunch, "id_ed25519.pub"), "ssh-ed25519 AAAA", 0o644)
	writeFile(t, filepath.Join(oldLaunch, "known_hosts.d", "x"), "kh", 0o600)
	writeFile(t, filepath.Join(oldState, "relay.cursor"), "42", 0o600)

	var log bytes.Buffer
	migrateDirs(&log)

	jam := filepath.Join(cfg, "jam")
	if got := mustRead(t, filepath.Join(jam, "settings.yml"), 0o600); got != "token: SECRET-VALUE\n" {
		t.Fatalf("settings = %q", got)
	}
	mustRead(t, filepath.Join(jam, "ssh", "s1-cert.pub"), 0o600)
	mustRead(t, filepath.Join(jam, "id_ed25519"), 0o600)
	mustRead(t, filepath.Join(jam, "id_ed25519.pub"), 0o644)
	mustRead(t, filepath.Join(jam, "known_hosts.d", "x"), 0o600)
	mustRead(t, filepath.Join(state, "jam", "relay.cursor"), 0o600)
	for _, old := range []string{oldCfg, oldLaunch, oldState} {
		if _, err := os.Stat(old); err != nil {
			t.Fatalf("old %s must be left in place: %v", old, err)
		}
	}
	if s := log.String(); !strings.Contains(s, "copied a pre-rename directory") || strings.Contains(s, "SECRET-VALUE") || strings.Contains(s, "PRIVATE-KEY") {
		t.Fatalf("log = %q", s)
	}

	writeFile(t, filepath.Join(oldCfg, "settings.yml"), "token: CHANGED\n", 0o600)
	log.Reset()
	migrateDirs(&log)
	if got := mustRead(t, filepath.Join(jam, "settings.yml"), 0o600); got != "token: SECRET-VALUE\n" || log.Len() != 0 {
		t.Fatalf("second run copied again: settings %q, log %q", got, log.String())
	}
}

// A Jam host with only the launcher directory gets its key under ~/.config/jam
// (before serve's keys.EnsureFile could mint a fresh one); an existing new key
// is never overwritten.
func TestMigrateDirsLauncherOnlyAndNewWins(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeFile(t, filepath.Join(cfg, legacynames.LauncherDir, "id_ed25519"), "OLD", 0o600)
	migrateDirs(&bytes.Buffer{})
	if got := mustRead(t, filepath.Join(cfg, "jam", "id_ed25519"), 0o600); got != "OLD" {
		t.Fatalf("key = %q", got)
	}
	writeFile(t, filepath.Join(cfg, "jam", "id_ed25519"), "NEW", 0o600)
	migrateDirs(&bytes.Buffer{})
	if got := mustRead(t, filepath.Join(cfg, "jam", "id_ed25519"), 0o600); got != "NEW" {
		t.Fatalf("an existing new key was overwritten: %q", got)
	}
}

func TestLauncherDefaultsUnderJamConfig(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	c, err := parseServeConfig([]byte("runtime:\n  launcher:\n    runtime-addr: jam.example.com:443\n    jam-host: jam.example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.validateLauncher(); err != nil {
		t.Fatal(err)
	}
	lc := c.Runtime.Launcher
	if lc.IdentityFile != filepath.Join(cfg, "jam", "id_ed25519") || lc.KnownHostsDir != filepath.Join(cfg, "jam", "known_hosts.d") {
		t.Fatalf("launcher defaults = %q, %q", lc.IdentityFile, lc.KnownHostsDir)
	}
}

func TestStateDirIsJam(t *testing.T) {
	s := t.TempDir()
	t.Setenv("XDG_STATE_HOME", s)
	if got, err := jamStateDir(); err != nil || got != filepath.Join(s, "jam") {
		t.Fatalf("jamStateDir = %q, %v", got, err)
	}
}
```

`cmd/at-jam/ssh_legacy_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/legacynames"
)

func legacyBlock(studio string) string {
	return legacynames.SSHBlockBegin + studio + " >>>\nHost jam-" + studio + "\n  ProxyCommand old ssh-proxy " + studio + "\nMatch all\n" +
		legacynames.SSHBlockEnd + studio + " <<<\n"
}

func TestStripLegacySSHBlocks(t *testing.T) {
	cfg := "# mine\n" + legacyBlock("s1") + "\n" + legacyBlock("s2") + "Host *\n  User me\n"
	got, studios := stripLegacySSHBlocks(cfg)
	if strings.Contains(got, legacynames.SSHBlockBegin) || strings.Contains(got, "jam-s1") || strings.Contains(got, "jam-s2") {
		t.Fatalf("legacy blocks survive:\n%s", got)
	}
	if !strings.Contains(got, "# mine\n") || !strings.Contains(got, "Host *\n  User me\n") {
		t.Fatalf("the user's own lines must be kept:\n%s", got)
	}
	if strings.Join(studios, ",") != "s1,s2" {
		t.Fatalf("studios = %v", studios)
	}
	if again, none := stripLegacySSHBlocks(got); again != got || len(none) != 0 {
		t.Fatal("strip is not idempotent")
	}
}

// --write replaces a pre-rename block for the same studio rather than leaving
// a shadowing duplicate Host above the new one.
func TestSSHConfigWriteReplacesPreRenameBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".ssh", "config")
	writeFile(t, path, legacyBlock("s1")+"Host *\n  User me\n", 0o600)
	begin, end := sshBlockMarkers("s1")
	if _, err := installSSHConfigBlock("s1", begin+"\nHost jam-s1\nMatch all\n"+end+"\n"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	if strings.Contains(got, legacynames.SSHBlockBegin) || strings.Count(got, "Host jam-s1") != 1 || !strings.HasPrefix(got, "# >>> jam s1 >>>") {
		t.Fatalf("config =\n%s", got)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./cmd/at-jam/ -run 'TestMigrateDirs|TestLauncherDefaults|TestStateDirIsJam|TestStripLegacy|TestSSHConfigWriteReplacesPreRename'`
Expected: FAIL to compile: `migrateDirs`, `jamStateDir`, `stripLegacySSHBlocks` undefined.

- [ ] **Step 4: Implement the directories**

In `cmd/at-jam/clientconfig.go`, replace `configDir`, `legacyConfigDir`, `configDirNamed` and `migrateConfigDir` with:

```go
// configDir is jam's host-side config directory: $XDG_CONFIG_HOME/jam, else
// ~/.config/jam. It also holds the launcher's default SSH identity and
// known_hosts.d (validateLauncher).
func configDir() string { return filepath.Join(xdgConfigHome(), "jam") }

// xdgConfigHome is $XDG_CONFIG_HOME, else ~/.config.
func xdgConfigHome() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// xdgStateHome is $XDG_STATE_HOME, else ~/.local/state ("" when no home
// directory resolves).
func xdgStateHome() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return x
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state")
}

// dirMove is one pre-rename directory copied to its new home: the whole of
// it when entries is nil (only if to is missing), else each listed entry
// that is missing under to.
type dirMove struct {
	from, to string
	entries  []string
}

// renamedDirs are the jam rename's directory moves
// (docs/usage/jam/renamed-from-at.md), CLI config first so the launcher's
// entries land in the copied directory rather than pre-empting it.
func renamedDirs() []dirMove {
	cfg := xdgConfigHome()
	moves := []dirMove{
		{from: filepath.Join(cfg, legacynames.ConfigDir), to: configDir()},
		{from: filepath.Join(cfg, legacynames.LauncherDir), to: configDir(), entries: []string{"id_ed25519", "id_ed25519.pub", "known_hosts.d"}},
	}
	if s := xdgStateHome(); s != "" {
		moves = append(moves, dirMove{from: filepath.Join(s, legacynames.StateDir), to: filepath.Join(s, "jam")})
	}
	return moves
}

// migrateDirs carries the pre-rename directories across once, at every jam
// invocation before any command: it copies (modes kept, so keys and tokens
// stay 0600), leaves the old ones in place, and logs the two paths — never
// file contents. A failed copy is logged and its partial target removed, so
// the next run tries again.
func migrateDirs(stderr io.Writer) {
	var lg *slog.Logger
	logger := func() *slog.Logger {
		if lg == nil {
			lg, _ = logging.New(logging.Options{Mode: logging.ModeFrom(os.Getenv("JAM_LOG_MODE")), Stderr: stderr, Level: slog.LevelInfo})
		}
		return lg
	}
	copied := func(from, to string, err error) {
		if err != nil {
			_ = os.RemoveAll(to)
			logger().Warn("could not copy a pre-rename directory; using defaults",
				slog.String("from", from), slog.String("to", to), slog.String("err", err.Error()), slog.String("see", logging.RenamedFromAtDoc))
			return
		}
		logger().Info("copied a pre-rename directory (the old one is left in place)",
			slog.String("from", from), slog.String("to", to), slog.String("see", logging.RenamedFromAtDoc))
	}
	for _, m := range renamedDirs() {
		if fi, err := os.Stat(m.from); err != nil || !fi.IsDir() {
			continue
		}
		if m.entries == nil {
			if _, err := os.Stat(m.to); !errors.Is(err, os.ErrNotExist) {
				continue
			}
			copied(m.from, m.to, copyDir(m.from, m.to))
			continue
		}
		for _, e := range m.entries {
			src, dst := filepath.Join(m.from, e), filepath.Join(m.to, e)
			if _, err := os.Lstat(src); err != nil {
				continue
			}
			if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err := os.MkdirAll(m.to, 0o700); err != nil {
				copied(src, dst, err)
				continue
			}
			copied(src, dst, copyEntry(src, dst))
		}
	}
}

// copyEntry copies src — a regular file or a directory tree — to dst,
// keeping permission bits.
func copyEntry(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return copyDir(src, dst)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, fi.Mode().Perm())
}
```

(Add the `legacynames` import.)

Then make these changes:
- `cmd/at-jam/main.go` `runAs`: delete the `at-harbor` branch and call `migrateDirs(stderr)`. Update its doc comment: "copies the pre-rename directories across once (migrateDirs)".
- `internal/logging/deprecated.go`: add `const RenamedFromAtDoc = "docs/usage/jam/renamed-from-at.md"` beside `RenameDoc`.
- `cmd/at-jam/config.go`:
  - Delete `atCoveConfigDir` and `atJamConfigDir`.
  - `credentialsFilePath` uses `configDir()`.
  - In `validateLauncher`: `lc.IdentityFile = filepath.Join(configDir(), "id_ed25519")` and `lc.KnownHostsDir = filepath.Join(configDir(), "known_hosts.d")`.
  - Replace `atJamStateDir` with:

```go
// jamStateDir is $XDG_STATE_HOME/jam, else ~/.local/state/jam.
func jamStateDir() (string, error) {
	s := xdgStateHome()
	if s == "" {
		return "", errStateDirNotAbsolute
	}
	return filepath.Join(s, "jam"), nil
}
```

  and call it from `stateDir()`.
- `cmd/at-jam/rename_test.go`: delete `TestInvokedAsAtHarborWarnsOnceAndRuns`, `TestConfigDirMigratesFromAtHarborOnce` and `TestConfigDirNoOldNoCopy`. They are replaced by the tests above. Keep `TestInvokedAsAtJamDoesNotWarn` until Task 7 renames it.

- [ ] **Step 5: Implement the ssh markers and the legacy strip**

In `cmd/at-jam/ssh.go`:

```go
// sshBlockBeginPrefix and sshBlockEndPrefix start every studio's markers.
const (
	sshBlockBeginPrefix = "# >>> jam "
	sshBlockEndPrefix   = "# <<< jam "
)

func sshBlockMarkers(studio string) (begin, end string) {
	return sshBlockBeginPrefix + studio + " >>>", sshBlockEndPrefix + studio + " <<<"
}

// stripLegacySSHBlocks returns config without every complete pre-rename
// (`# >>> at-jam <studio> >>>`) block and the blank line after it, plus the
// studios it removed, in order. Their Host jam-<studio> would shadow the new
// block, and their ProxyCommand names a binary that no longer exists.
func stripLegacySSHBlocks(config string) (string, []string) {
	lines := strings.SplitAfter(config, "\n")
	var out strings.Builder
	var studios []string
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		studio, ok := strings.CutPrefix(t, legacynames.SSHBlockBegin)
		if ok {
			studio, ok = strings.CutSuffix(studio, " >>>")
		}
		end := -1
		if ok {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == legacynames.SSHBlockEnd+studio+" <<<" {
					end = j
					break
				}
			}
		}
		if end < 0 {
			out.WriteString(lines[i])
			continue
		}
		studios = append(studios, studio)
		i = end
		if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" && lines[i+1] != "" {
			i++
		}
	}
	return out.String(), studios
}
```

Then:
- `installSSHConfigBlock`'s edit becomes `func(s string) (string, error) { s, _ = stripLegacySSHBlocks(s); return spliceSSHConfigBlock(s, studio, block) }`.
- In `sshConfigEntries`, the end-marker literal becomes `sshBlockEndPrefix+cur.studio+" <<<"`.
- In `pruneSSHConfig`, before computing `gone`, strip legacy blocks. If any were found, write the stripped file (honouring `dry`) and print one line per studio: `removed pre-rename block for jam-<studio>` (`would remove …` when dry).
- Update `ssh_test.go`'s literal `# >>> at-jam`/`# <<< at-jam` expectations to the new markers. The guard group lists them.

- [ ] **Step 6: Run the tests to verify they pass, then the gates**

Run: `go test ./cmd/at-jam/ ./internal/logging/ && go test -run TestNoPreRenameNames .`, then the full gates.
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor!: jam rename — ~/.config/jam, ~/.local/state/jam, launcher defaults, '# >>> jam' markers; copy the old dirs once

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Binaries — `jam`, `jam mint`, `jam-studio-agent` — and the release

**Files:**
- Move:
  - `cmd/at-jam/` → `cmd/jam/`
  - `cmd/cove-master/` → `cmd/jam-studio-agent/`
  - `cmd/at-mint/{github,anthropic,jwt}.go` and their `_test.go` → `internal/mintcmd/`, plus `cmd/at-mint/main.go` → `internal/mintcmd/mintcmd.go` (rewritten)
  - `cmd/at-mint/main_test.go` → `internal/mintcmd/mintcmd_test.go`
  - `scripts/stage-covemaster.sh` → `scripts/stage-studio-agent.sh`
  - `.at-jam/` → `.jam/`
- Delete: `scripts/release/at-cove`, `cmd/at-mint/`
- Create: `cmd/jam/mint_test.go`
- Modify:
  - `cmd/jam/{main.go,update.go,update_test.go,ssh.go,nag.go,rename_test.go,…}`
  - `internal/mint/{mint.go,mint_test.go}`
  - `internal/studioagentbin/{studioagentbin.go,bin/.gitignore,bin/README}`
  - `internal/assemble/{assemble.go,assemble_test.go,hardening/Dockerfile}`, `internal/install/identity.go`
  - `internal/connect/studioagent.go`, `internal/agentrun/harness_claude.go`
  - `internal/update/{update.go,update_test.go}`, `installer.go`, `install.sh`, `install.test.sh`
  - `.goreleaser.yaml`, `.github/workflows/release.yml`, `scripts/release/check-archive.sh`, `scripts/{build.sh,whiteboard.sh,app.sh}`
  - `justfile`, `.air.toml`, `.gitignore`, `dev/*`, `deploy/monitoring/jam-rules.yml`
  - `web/app/{package.json,vite.config.ts,src/session/SessionView.test.tsx}`, `internal/jam/userui/session.go`
  - `internal/jam/sessionctx/boilerplate.go` (+ test), `internal/logging/{usererror,logging}.go` (+ tests)
  - remaining `at-cove` mentions in code (Step 6)

**Interfaces:**
- Consumes: everything above.
- Produces:
  - the binary `jam` (`cli.App{Name: "jam"}`), its `mint` command
  - `mintcmd.Run(args []string, version string, env func(string) (string, bool), httpc *http.Client, stdout, stderr io.Writer) int`
  - in-studio binary `/usr/local/bin/jam-studio-agent`, staged as `studioagent/jam-studio-agent-linux-<arch>`
  - release asset `jam_<ver>_<os>_<arch>.tar.gz` holding exactly `jam`
  - `update.Env(target) == []string{"JAM_VERSION=" + target}`

- [ ] **Step 1: Add the guard group and the release allow-list**

```go
	{"binaries and release", []string{`at-jam`, `at-mint`, `at-cove`, `at-harbor`, `cove-master`, `covemaster`,
		`\batcove\b`, `cove_(\{\{|\$\{|\*)`, `\bCOVE_[A-Z]`, `\bAT_[A-Z]+_`, `atMint`}},
```

and to `renamedAllow`:

```go
	{"install.test.sh", nil},                                          // drives the frozen pre-rename installer
	{"install.sh", []string{`at-jam`, `at-mint`, `at-cove`}},          // the stale-binary notice
	{".gitignore", []string{`at-jam`, `at-mint`, `cove-master`}},      // keeps stale local builds out of git
```

- [ ] **Step 2: Write the failing release tests (install.test.sh)**

Replace `install.test.sh` with:

```bash
#!/usr/bin/env bash
# install.test.sh — hermetic tests for the root install.sh installer.
#
# No network, no Docker, no live release. Stub `gh` and `uname` executables on
# PATH serve a locally-built fixture "release": a jam_<ver>_<os>_<arch>.tar.gz
# holding a fake `jam`, plus, as a control, an old-format cove_<ver>_* archive
# like the releases before the jam rename. Pure helpers are unit-tested by
# sourcing install.sh in lib mode (JAM_INSTALL_LIB=1), which suppresses main.
#
# Run: bash install.test.sh   (`just test-install`; a CI gate)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SH="$HERE/install.sh"
LEGACY_SH="$HERE/testdata/install-legacy.sh" # install.sh as embedded in every pre-rename at-jam

pass=0
fail=0
note() { printf '  %s\n' "$*"; }
ok() { pass=$((pass + 1)); printf 'ok   - %s\n' "$1"; }
bad() {
  fail=$((fail + 1))
  printf 'FAIL - %s\n' "$1"
  shift || true
  for line in "$@"; do note "$line"; done
}
assert_eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected: $2" "actual:   $3"; fi; }

sha_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

FIXTURE_VERSION="200-1026"   # a jam release
OLD_VERSION="150-1001"       # a pre-rename release (old archive name and contents)
RELEASE_DIR="$WORK/release"
STUB_BIN="$WORK/stubbin"
mkdir -p "$RELEASE_DIR" "$STUB_BIN"

# fake_bin <dir> <name> <version>
fake_bin() {
  cat >"$1/$2" <<EOF
#!/bin/sh
[ "\$1" = version ] && echo "$2 $3"
EOF
  chmod +x "$1/$2"
}

# build_jam_archive <os> <arch> — the new shape: exactly `jam`.
build_jam_archive() {
  local stage="$WORK/stage-jam-$1-$2"
  mkdir -p "$stage"
  fake_bin "$stage" jam "$FIXTURE_VERSION"
  tar -C "$stage" -czf "$RELEASE_DIR/jam_${FIXTURE_VERSION}_$1_$2.tar.gz" jam
}
# build_old_archive <os> <arch> — the pre-rename shape the legacy installer expects.
build_old_archive() {
  local stage="$WORK/stage-old-$1-$2" b
  mkdir -p "$stage"
  for b in at-cove at-mint at-jam; do fake_bin "$stage" "$b" "$OLD_VERSION"; done
  tar -C "$stage" -czf "$RELEASE_DIR/cove_${OLD_VERSION}_$1_$2.tar.gz" at-cove at-mint at-jam
}
build_jam_archive linux amd64
build_jam_archive darwin arm64
build_old_archive linux amd64
(
  cd "$RELEASE_DIR"
  : >checksums.txt
  for f in *.tar.gz; do printf '%s  %s\n' "$(sha_of "$f")" "$f" >>checksums.txt; done
)

PATTERN_LOG="$WORK/patterns.log"
: >"$PATTERN_LOG"
cat >"$STUB_BIN/gh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
sub="\${1:-}"; act="\${2:-}"
if [ "\$sub" = auth ] && [ "\$act" = status ]; then exit 0; fi
if [ "\$sub" = release ] && [ "\$act" = view ]; then echo "$FIXTURE_VERSION"; exit 0; fi
if [ "\$sub" = release ] && [ "\$act" = download ]; then
  dir=.; patterns=(); shift 2
  while [ \$# -gt 0 ]; do
    case "\$1" in
      --dir|-D) dir="\$2"; shift 2;;
      --pattern) patterns+=("\$2"); echo "\$2" >>"$PATTERN_LOG"; shift 2;;
      --repo|-R) shift 2;;
      *) shift;;
    esac
  done
  mkdir -p "\$dir"
  for p in "\${patterns[@]}"; do
    [ -e "$RELEASE_DIR/\$p" ] || { echo "gh: no assets match the file pattern \$p" >&2; exit 1; }
    cp "$RELEASE_DIR/\$p" "\$dir/"
  done
  exit 0
fi
echo "stub gh: unhandled: \$*" >&2; exit 2
EOF
chmod +x "$STUB_BIN/gh"
cat >"$STUB_BIN/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in -s) echo "${UNAME_S:-Linux}";; -m) echo "${UNAME_M:-x86_64}";; *) echo "${UNAME_S:-Linux}";; esac
EOF
chmod +x "$STUB_BIN/uname"
run_install() { PATH="$STUB_BIN:$PATH" bash "$INSTALL_SH"; }

echo "== unit: pure helpers (sourced in lib mode) =="
# shellcheck disable=SC1090
JAM_INSTALL_LIB=1 source "$INSTALL_SH"
assert_eq "normalize_arch x86_64 -> amd64" amd64 "$(normalize_arch x86_64)"
assert_eq "normalize_arch aarch64 -> arm64" arm64 "$(normalize_arch aarch64)"
if normalize_arch i386 >/dev/null 2>&1; then bad "normalize_arch i386 rejected"; else ok "normalize_arch i386 rejected"; fi
assert_eq "normalize_os Darwin -> darwin" darwin "$(normalize_os Darwin)"
if normalize_os MINGW64_NT >/dev/null 2>&1; then bad "normalize_os windows rejected"; else ok "normalize_os windows rejected"; fi
assert_eq "resolve_bindir default" "$HOME/.local/bin" "$(BINDIR='' JAM_SYSTEM='' resolve_bindir)"
assert_eq "resolve_bindir JAM_SYSTEM=1" /usr/local/bin "$(BINDIR='' JAM_SYSTEM=1 resolve_bindir)"
assert_eq "resolve_bindir BINDIR wins" /opt/bin "$(BINDIR=/opt/bin JAM_SYSTEM=1 resolve_bindir)"
assert_eq "resolve_bindir ignores COVE_SYSTEM" "$HOME/.local/bin" "$(BINDIR='' JAM_SYSTEM='' COVE_SYSTEM=1 resolve_bindir)"
vc="$WORK/vc"; mkdir -p "$vc"; echo hello >"$vc/jam_x.tar.gz"
printf '%s  %s\n' "$(sha_of "$vc/jam_x.tar.gz")" jam_x.tar.gz >"$vc/checksums.txt"
if (cd "$vc" && verify_checksum jam_x.tar.gz checksums.txt); then ok "verify_checksum good"; else bad "verify_checksum good"; fi
echo tampered >>"$vc/jam_x.tar.gz"
if (cd "$vc" && verify_checksum jam_x.tar.gz checksums.txt 2>/dev/null); then bad "verify_checksum tamper"; else ok "verify_checksum tamper -> abort"; fi

echo "== e2e: installs exactly jam =="
BIN1="$WORK/bin1"
out="$(JAM_VERSION="$FIXTURE_VERSION" BINDIR="$BIN1" run_install 2>&1)" && rc=0 || rc=$?
assert_eq "install exit 0" 0 "$rc"
if [ -x "$BIN1/jam" ]; then ok "jam installed + executable"; else bad "jam installed + executable" "$out"; fi
for b in at-jam at-mint at-cove; do
  if [ -e "$BIN1/$b" ]; then bad "$b NOT installed"; else ok "$b NOT installed"; fi
done
if grep -q "jam_${FIXTURE_VERSION}_linux_amd64.tar.gz" "$PATTERN_LOG"; then ok "requested the jam_ archive"; else bad "requested the jam_ archive" "$(cat "$PATTERN_LOG")"; fi
case "$out" in *"pre-rename binaries"*) bad "no stale notice on a clean dir" "$out" ;; *) ok "no stale notice on a clean dir" ;; esac

echo "== e2e: stale pre-rename binaries are noticed, never touched =="
BIN2="$WORK/bin2"; mkdir -p "$BIN2"
printf 'old-at-jam\n' >"$BIN2/at-jam"; printf 'old-at-mint\n' >"$BIN2/at-mint"
out="$(JAM_VERSION="$FIXTURE_VERSION" BINDIR="$BIN2" run_install 2>&1)" && rc=0 || rc=$?
assert_eq "install with stale binaries exit 0" 0 "$rc"
case "$out" in *"pre-rename binaries still in $BIN2: at-jam at-mint"*"renamed-from-at.md"*) ok "stale notice names them and the runbook" ;; *) bad "stale notice" "$out" ;; esac
assert_eq "stale at-jam untouched" old-at-jam "$(cat "$BIN2/at-jam")"
assert_eq "stale at-mint untouched" old-at-mint "$(cat "$BIN2/at-mint")"

echo "== e2e: arch mapping and latest resolution =="
: >"$PATTERN_LOG"
UNAME_S=Darwin UNAME_M=arm64 JAM_VERSION="$FIXTURE_VERSION" BINDIR="$WORK/bin3" run_install >/dev/null 2>&1 || true
if grep -q "jam_${FIXTURE_VERSION}_darwin_arm64.tar.gz" "$PATTERN_LOG"; then ok "requested darwin_arm64"; else bad "requested darwin_arm64"; fi
BINDIR="$WORK/bin4" run_install >/dev/null 2>&1 || true
if [ -x "$WORK/bin4/jam" ]; then ok "latest resolves + installs"; else bad "latest resolves + installs"; fi

echo "== compat: an old at-jam update against the jam release fails, changing nothing =="
BIN5="$WORK/bin5"; mkdir -p "$BIN5"
printf 'running-at-jam\n' >"$BIN5/at-jam"; printf 'running-at-mint\n' >"$BIN5/at-mint"
before="$(cd "$BIN5" && ls -A | sort | tr '\n' ' ')"
out="$(COVE_VERSION="$FIXTURE_VERSION" BINDIR="$BIN5" PATH="$STUB_BIN:$PATH" bash "$LEGACY_SH" 2>&1)" && rc=0 || rc=$?
if [ "$rc" != 0 ]; then ok "legacy installer exits non-zero on a jam release"; else bad "legacy installer exits non-zero on a jam release" "$out"; fi
assert_eq "legacy installer leaves at-jam byte-identical" running-at-jam "$(cat "$BIN5/at-jam")"
assert_eq "legacy installer leaves at-mint byte-identical" running-at-mint "$(cat "$BIN5/at-mint")"
assert_eq "legacy installer adds no file" "$before" "$(cd "$BIN5" && ls -A | sort | tr '\n' ' ')"
# Control: the same legacy installer still works against a pre-rename release,
# so the failure above is the missing asset, not a broken harness.
BIN6="$WORK/bin6"
out="$(COVE_VERSION="$OLD_VERSION" BINDIR="$BIN6" PATH="$STUB_BIN:$PATH" bash "$LEGACY_SH" 2>&1)" && rc=0 || rc=$?
if [ "$rc" = 0 ] && [ -x "$BIN6/at-jam" ]; then ok "control: legacy installer still installs a pre-rename release"; else bad "control" "rc=$rc" "$out"; fi

echo "== release: check-archive.sh guards the jam archive shape =="
CHECK_SH="$HERE/scripts/release/check-archive.sh"
ca_case() { # ca_case <name> <want: ok|reject> <jam mode> <bins...>
  local name="$1" want="$2" mode="$3" d="$WORK/ca-$1"
  shift 3
  mkdir -p "$d/stage" "$d/dist"
  for b in jam at-cove README.md; do printf 'x\n' >"$d/stage/$b"; done
  chmod "$mode" "$d/stage/jam"
  tar -C "$d/stage" -czf "$d/dist/jam_1-0101_linux_amd64.tar.gz" "$@"
  if bash "$CHECK_SH" "$d/dist" >/dev/null 2>&1; then got=ok; else got=reject; fi
  assert_eq "check-archive $name -> $want" "$want" "$got"
}
ca_case good ok 0755 jam
ca_case mode reject 0644 jam
ca_case tombstone reject 0755 jam at-cove
ca_case extra reject 0755 jam README.md
if bash "$CHECK_SH" "$WORK/no-such-dist" >/dev/null 2>&1; then bad "check-archive no archives -> reject"; else ok "check-archive no archives -> reject"; fi

echo "== e2e: tampered checksum aborts, nothing installed =="
cp "$RELEASE_DIR/checksums.txt" "$WORK/checksums.good"
printf '%s  %s\n' "$(sha_of /dev/null)" "jam_${FIXTURE_VERSION}_linux_amd64.tar.gz" >"$RELEASE_DIR/checksums.txt"
JAM_VERSION="$FIXTURE_VERSION" BINDIR="$WORK/bin7" run_install >/dev/null 2>&1 && rc=0 || rc=$?
if [ "$rc" != 0 ]; then ok "tamper aborts (nonzero exit)"; else bad "tamper aborts (nonzero exit)"; fi
if [ -e "$WORK/bin7/jam" ]; then bad "no binary installed on tamper"; else ok "no binary installed on tamper"; fi
cp "$WORK/checksums.good" "$RELEASE_DIR/checksums.txt"

echo
echo "-------- $pass passed, $fail failed --------"
[ "$fail" -eq 0 ]
```

- [ ] **Step 3: Run it to verify it fails**

Run: `bash install.test.sh`
Expected: FAIL. `jam` isn't installed (install.sh still asks for `cove_*` and places `at-mint`/`at-jam`), there is no stale notice, and check-archive still wants the tombstone. The "compat" cases already pass, because the legacy installer can't find `cove_200-1026_*`, which is exactly the property being pinned.

- [ ] **Step 4: Rewrite `install.sh`, `check-archive.sh` and the update knobs**

In `install.sh`:
- Header comment: "one-command installer for the jam host binary"; the archive is `jam_<version>_<os>_<arch>.tar.gz`, and it drops `jam` onto your PATH. The env knobs are `JAM_VERSION`, `BINDIR`, `JAM_SYSTEM`, `JAM_SYSTEM_BINDIR`, `JAM_REPO`. Remove the at-cove tombstone sentence.
- `REPO="${JAM_REPO:-aethons-tools/cove}"`.
- In `resolve_bindir` and `resolve_version`, `COVE_SYSTEM`→`JAM_SYSTEM`, `COVE_SYSTEM_BINDIR`→`JAM_SYSTEM_BINDIR`, `COVE_VERSION`→`JAM_VERSION`.
- Add after `path_check`:

```bash
# stale_binaries <dir> -> the pre-rename binaries still in dir, space-separated.
stale_binaries() {
  local b found=()
  for b in at-jam at-mint at-cove; do
    if [ -e "$1/$b" ]; then found+=("$b"); fi
  done
  echo "${found[*]:-}"
}
```

- In `main`: `archive="jam_${version}_${os}_${arch}.tar.gz"`. Replace the install loop and the trailing version line with:

```bash
  place "$tmp/extract/jam" "$bindir"
  info "installed jam -> $bindir"

  path_check "$bindir"
  local stale
  stale="$(stale_binaries "$bindir")"
  if [ -n "$stale" ]; then
    info "pre-rename binaries still in $bindir: $stale — point your service and scripts at jam, then delete them (docs/usage/jam/renamed-from-at.md)"
  fi
  if command -v "$bindir/jam" >/dev/null 2>&1; then
    info "$("$bindir/jam" version 2>/dev/null || echo "jam $version")"
  fi
```

- Lib mode: `if [ "${JAM_INSTALL_LIB:-0}" != 1 ]; then main "$@"; fi`.

Replace `scripts/release/check-archive.sh` with:

```bash
#!/usr/bin/env bash
# check-archive.sh — assert every release archive goreleaser built
# (dist/jam_*_<os>_<arch>.tar.gz) holds exactly `jam`, mode 0755.
# Run after `goreleaser release --snapshot` (release.yml, PR and main).
# Usage: scripts/release/check-archive.sh [distdir]   (default: dist)
set -euo pipefail

dist="${1:-dist}"
shopt -s nullglob
archives=("$dist"/jam_*.tar.gz)
if [ "${#archives[@]}" -eq 0 ]; then
  echo "check-archive: no $dist/jam_*.tar.gz (run goreleaser release --snapshot first)" >&2
  exit 1
fi

fail=0
for a in "${archives[@]}"; do
  names="$(tar -tzf "$a" | sort | tr '\n' ' ')"
  if [ "$names" != "jam " ]; then
    echo "check-archive: $a holds [${names% }], want [jam]" >&2
    fail=1
    continue
  fi
  mode="$(tar -tzvf "$a" jam | awk '{print $1}')"
  if [ "$mode" != "-rwxr-xr-x" ]; then
    echo "check-archive: $a jam mode $mode, want -rwxr-xr-x" >&2
    fail=1
    continue
  fi
  echo "check-archive: $a ok (jam)"
done
exit "$fail"
```

`git rm scripts/release/at-cove`.

In `internal/update/update.go`:
- `Env` returns `[]string{"JAM_VERSION=" + target}`;
- `ResolveLatest` uses `[]string{"JAM_INSTALL_LIB=1"}`;
- the temp file pattern is `"jam-install-*.sh"`;
- `Options.CoveEnv` → `Options.PinEnv` (doc: "the JAM_VERSION env knob");
- `Target`'s second parameter → `pinEnv`.

Update `update_test.go` expectations (`JAM_VERSION=`).

- [ ] **Step 5: Move the binaries and make `jam mint`**

```bash
git mv cmd/at-jam cmd/jam
git mv cmd/cove-master cmd/jam-studio-agent
mkdir -p internal/mintcmd
for f in github anthropic jwt; do git mv cmd/at-mint/$f.go internal/mintcmd/$f.go; git mv cmd/at-mint/${f}_test.go internal/mintcmd/${f}_test.go; done
git mv cmd/at-mint/main.go internal/mintcmd/mintcmd.go
git mv cmd/at-mint/main_test.go internal/mintcmd/mintcmd_test.go
git mv scripts/stage-covemaster.sh scripts/stage-studio-agent.sh
git mv .at-jam .jam
sed -i 's/^package main$/package mintcmd/' internal/mintcmd/*.go
```

Replace `internal/mintcmd/mintcmd.go`'s top (package doc, imports, `version`, `main`, `run`) with:

```go
// Package mintcmd is `jam mint`: it mints short-lived tokens (GitHub App
// installation tokens, Anthropic oauth tokens) for a minter credential.
// Non-secret inputs come from flags; secret inputs come only from env.
// Exactly one token is printed to stdout on success; on any error, nothing is
// printed to stdout and a diagnostic goes to stderr with a non-zero exit.
package mintcmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
)

// Run dispatches `jam mint`'s subcommands (args: everything after "mint").
// env and httpc are injected for tests.
func Run(args []string, version string, env func(string) (string, bool), httpc *http.Client, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:    "jam mint",
		Version: version,
		Commands: []cli.Command{
			{Name: "github", Brief: "mint a repo-scoped GitHub App installation token", Run: func(a []string, g cli.Globals, out, errw io.Writer) int {
				return doGitHub(a, env, httpc, out, errw)
			}},
			{Name: "anthropic", Brief: "mint an Anthropic oauth token via Auth0 WIF", Run: func(a []string, g cli.Globals, out, errw io.Writer) int {
				return doAnthropic(a, env, httpc, out, errw)
			}},
		},
	}
	return app.Run(args, stdout, stderr)
}
```

Keep `getenv` and the rest of the file. Drop imports the compiler reports unused. In the moved tests:
- replace each call `run(` with `Run(` and insert `"dev", ` as the second argument;
- change expected strings `at-mint` → `jam mint` (e.g. `TestRunVersion` now expects `"jam mint dev"`);
- change error prefixes `"at-mint: "` in `github.go`/`anthropic.go` to `"jam mint: "`.

`git rm -r cmd/at-mint` (anything left).

In `cmd/jam/main.go`:
- `cli.App{Name: "jam", …}`;
- add the command:

```go
			{Name: "mint", Brief: "mint a short-lived token (github | anthropic) — what Jam runs for a minter credential", Run: func(a []string, g cli.Globals, out, errw io.Writer) int {
				return mintcmd.Run(a, version, os.LookupEnv, http.DefaultClient, out, errw)
			}},
```

- the `update` Brief becomes `"update jam to the latest release"`.

`cmd/jam/mint_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestJamVersionAndMintVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, func(string) string { return "" }, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "jam ") {
		t.Fatalf("jam version: code %d out %q err %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"mint", "version"}, func(string) string { return "" }, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "jam mint ") {
		t.Fatalf("jam mint version: code %d out %q err %q", code, out.String(), errb.String())
	}
}
```

(`run`'s signature is `run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int`; mirror an existing `main_test.go` call if it differs.)

In `internal/mint/mint.go`, replace `atMintBinary` with:

```go
// jamBinary is argv[0] of a minter invocation: the running jam itself
// (minting is its `mint` subcommand), else "jam" on PATH.
func jamBinary() string {
	if self, err := execPath(); err == nil {
		return self
	}
	return "jam"
}
```

The argvs become `[]string{jamBinary(), "mint", "github", "--app-id", …}` and `[]string{jamBinary(), "mint", "anthropic", …}`. In `mint_test.go`:
- replace `TestAtMintBinary*` with `TestJamBinaryIsSelf` (stub `execPath` to return `/x/jam`; want `/x/jam`) and `TestJamBinaryFallsBackToPATH` (stub to return an error; want `jam`);
- the argv expectations read `"/x/jam mint github --app-id 123 …"` (keep each existing string's tail).

- [ ] **Step 6: Run the binaries map, then finish by hand**

```bash
cat > /tmp/map-t7.tsv <<'EOF'
stage-covemaster	stage-studio-agent
cmd/cove-master	cmd/jam-studio-agent
cove-master-linux-	jam-studio-agent-linux-
/tmp/covemaster	/tmp/studioagent
re:\bcovemaster/	studioagent/
"covemaster"	"studio-agent"
cove-master	jam-studio-agent
cmd/at-mint	internal/mintcmd
at-mint	jam mint
cmd/at-jam	cmd/jam
.at-jam/	.jam/
at-jam	jam
EOF
/tmp/jamrename.py --dry /tmp/map-t7.tsv && /tmp/jamrename.py /tmp/map-t7.tsv
gofmt -w $(git diff --name-only -- '*.go')
go test -run TestNoPreRenameNames . 2>&1 | head -80
```

Then, by hand:
- `.goreleaser.yaml`:
  - `project_name: jam`;
  - hooks `bash scripts/stage-studio-agent.sh`;
  - a single build `id: jam`, `main: ./cmd/jam`, `binary: jam`;
  - one archive `id: jam`, `ids: [jam]`, `name_template: "jam_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`, with `files:` set to `- none*` (goreleaser's "binaries only": without it, goreleaser adds README/LICENSE files).
  
  Rewrite the header comment: it builds `jam`, embeds `jam-studio-agent`, and no longer mentions at-mint or the tombstone.
- `.github/workflows/release.yml`:
  - job `atcove` → `binaries` and output `atcove` → `binaries` (`needs.changes.outputs.binaries`);
  - the DAG comment names `jam-studio-base` → `jam-studio` → `jam (embeds jam-studio-agent + list)`;
  - the path filter `scripts/stage-studio-agent.sh`, and `scripts/release/**` with comment "the archive check";
  - the check step's comment: "every archive must hold exactly jam";
  - publish `dist/jam_*.tar.gz dist/checksums.txt`;
  - `--notes "Continuous release from ${GITHUB_SHA::7} (jam; jam-studio-agent embedded)."`.
- `scripts/build.sh`: `BINARIES=(jam)`; delete the `cp at-jam at-harbor` block (lines ~73–77) and its comment.
- `justfile`:
  - the `build`/`build-all` comments name `dist/<os>-<arch>/jam`;
  - `jam *ARGS` runs `dist/$(go env GOOS)-$(go env GOARCH)/jam`;
  - `install` loops `for b in jam`, then `command -v jam` / `jam version`;
  - `dev-serve` uses `.../jam serve`;
  - `integration-jam` tests `./cmd/jam/...`.
- `.air.toml`: `go build -o .air/jam ./cmd/jam`, `bin = ".air/jam"` (or the file's existing key).
- `.gitignore`: add `/jam`, `/jam-studio-agent`, `/cmd/*/jam`, `/cmd/*/jam-studio-agent`. Keep the old `/at-jam`, `/at-mint`, `/cove-master`, `/cmd/*/at-*` lines under a comment `# pre-rename local builds`. In `internal/studioagentbin/bin/.gitignore`: `/jam-studio-agent-linux-*`.
- `cmd/jam/main.go` `runAs`: no at-harbor branch remains (Task 6). Rename `TestInvokedAsAtJamDoesNotWarn` → `TestInvokedAsJamDoesNotWarn` (the map already changed its body).
- `cmd/jam/ssh.go`: `proxyBinary()`'s fallback is `"jam"`. The key comment `ssh.MarshalPrivateKey(priv, "jam")` was changed by the map; keep it.
- `internal/logging/usererror.go`: the prefix `"at-cove: "` → `"jam: "`. Fix the `ModeFrom`/`LevelFrom` comments that say "at-cove and at-task CLIs" → "jam".
- `internal/jam/sessionctx/boilerplate.go`: `"You are " + who + ", running in a Jam studio."` (was "…a Jam-managed at-cove sandbox."). The `at-jam egress set`/`kit push` mentions were renamed by the map. Update `boilerplate_test.go`'s expectations.
- Every remaining `at-cove` the guard prints (about 30 lines: test fixtures using it as an arbitrary string, historical comments): replace with `jam` in fixtures, and with the present-tense fact in comments. For example, `internal/studio/default.go`'s "at-cove's default" → "the default". Never leave a sentence that describes the deleted CLI as current.
- `web/app/package.json` description, `web/app/vite.config.ts` comment, `web/app/src/session/SessionView.test.tsx` fixture `ssh: "jam ssh ses_1"` (mapped); `internal/jam/userui/session.go` `hd.SSH = "jam ssh " + inst.ActorID` (mapped).

- [ ] **Step 7: Run all tests, the release checks, and the gates**

Run:
```bash
go test ./... && go test -run TestNoPreRenameNames . && bash install.test.sh
go vet -tags integration ./... && just lint; echo "lint exit $?"
just build && ls dist/*/ && "dist/$(go env GOOS)-$(go env GOARCH)/jam" version && "dist/$(go env GOOS)-$(go env GOARCH)/jam" mint version
command -v goreleaser >/dev/null && goreleaser check && VERSION=0-0000 goreleaser release --snapshot --clean --skip=publish && bash scripts/release/check-archive.sh dist
```
Expected:
- all PASS, `lint exit 0`;
- `dist/<plat>/` holds `jam` only;
- `jam 0.0.0-dev…` or `jam dev`;
- `jam mint dev`;
- with goreleaser installed, `check-archive: dist/jam_0-0000_<os>_<arch>.tar.gz ok (jam)` for all four. Without it, the PR's `binaries` job runs the same check.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor!: jam rename — binaries jam, jam mint, jam-studio-agent; jam_<ver> release archive; drop the at-cove tombstone

An old at-jam update cannot fetch jam_* archives and fails before changing
anything; the upgrade is the one-command install (renamed-from-at.md).

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Serve-start migrations — studios, volumes, kit trees

**Files:**
- Create:
  - `internal/backend/colima/volume_copy.go`, `internal/backend/colima/volume_copy_test.go`, `internal/backend/colima/volume_copy_integration_test.go`
  - `internal/jam/launcher/rename_migrate.go`, `internal/jam/launcher/rename_migrate_test.go`
  - `internal/jam/rename_migrate.go`, `internal/jam/rename_migrate_test.go`
  - `internal/jam/kit_rename_migrate.go`, `internal/jam/kit_rename_migrate_test.go`
  - `internal/jam/storetest/rename_migrations.go`
- Modify:
  - `internal/backend/backend.go` (`VolumeOps`)
  - `internal/jam/launcher/launcher_test.go` (`fakeOps` gains the two methods)
  - `internal/jam/kit_push.go` (`kitTreeOf` refusal)
  - `internal/jam/kit_migrate.go` (`kitMigrateConditions`)
  - `internal/jam/storetest/conformance.go`
  - `cmd/jam/main.go` (`builtLauncher.local`, the two calls)
  - any other `backend.VolumeOps` implementer the compiler names

**Interfaces:**
- Consumes:
  - `legacynames.*` (Task 1), `naming.StudioContainer` (Task 5), `launcher.StateLabel`/`JamLabel` (Task 5)
  - `basedigest.DefaultRef()` (Task 4)
  - `kitTreeOf`, `planKitTreeLocked`, `writeKitVersionsLocked`, `pendingKitVersion`, `currentFilesWithKitYML`, `LoadKitVersion`, `studio.SetBaseKit`, `kitMigrateConditions`, `configMu`, `kitMu` (existing)
- Produces:
  - `backend.VolumeOps.VolumeExists(name string) (bool, error)`
  - `backend.VolumeOps.CopyVolume(image, from, to string) error`
  - `launcher.MigratedFromLabel = "jam.studio.migrated-from"`
  - `(*launcher.Launcher).RetireLegacyStudio(ctx context.Context, actorID string) error`
  - `(*launcher.Launcher).MigrateStateVolumes(ctx context.Context, also []string) ([]string, error)`
  - `(*launcher.Launcher).StudioLocation(actorID string) string`
  - `jam.StudioRenamer` (interface of those three)
  - `jam.StudioRenameMigration{Retired, Volumes []string}`
  - `jam.MigrateStudioNames(ctx context.Context, store Store, r StudioRenamer, log *slog.Logger) (StudioRenameMigration, error)`
  - `jam.KitOriginMigration = "migration"`
  - `jam.KitRenameMigration{Rewritten, Kept []string}`
  - `jam.MigrateKitRename(store Store, t *condition.Tracker, log *slog.Logger) (KitRenameMigration, error)`

- [ ] **Step 1: Write the failing colima tests**

`internal/backend/colima/volume_copy_test.go`:

```go
package colima

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestCopyVolumeArgv(t *testing.T) {
	f := &runner.Fake{}
	c := NewWithContext(f, "")
	if err := c.CopyVolume("img@sha256:x", "old-agent-data", "new-agent-data"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(dockerCall(f.Calls, "run"), " ")
	want := "run --rm --network none --user 0:0 --entrypoint /bin/sh -v old-agent-data:/from:ro -v new-agent-data:/to img@sha256:x -c cp -a /from/. /to/"
	if got != want {
		t.Fatalf("argv =\n%s\nwant\n%s", got, want)
	}
}

func TestVolumeExistsMatchesExactly(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "jam-studio-s1-agent-data-x\njam-studio-s1-agent-data\n"}, {Stdout: "jam-studio-s1-agent-data-x\n"}}}
	c := NewWithContext(f, "")
	if ok, err := c.VolumeExists("jam-studio-s1-agent-data"); err != nil || !ok {
		t.Fatalf("exists = %v, %v", ok, err)
	}
	if ok, err := c.VolumeExists("jam-studio-s1-agent-data"); err != nil || ok {
		t.Fatalf("a prefix match is not existence: %v, %v", ok, err)
	}
	if args := strings.Join(dockerCall(f.Calls, "volume"), " "); args != "volume ls --quiet --filter name=jam-studio-s1-agent-data" {
		t.Fatalf("argv = %q", args)
	}
}
```

(`dockerCall` is in `colima_test.go`; if it returns the *first* matching call, the last assertion checks the first `VolumeExists` call, which is the same argv.)

`internal/backend/colima/volume_copy_integration_test.go`:

```go
//go:build integration

package colima

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// A real docker copies a volume's files with content, owner and mode intact.
// Skips without a docker daemon or the alpine image (pulled once).
func TestCopyVolumeRealDocker(t *testing.T) {
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("no docker daemon")
	}
	if exec.Command("docker", "pull", "-q", "alpine:3").Run() != nil {
		t.Skip("alpine:3 unavailable")
	}
	c := NewWithContext(runner.OS{}, "")
	from, to := "jam-copytest-from", "jam-copytest-to"
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", from, to).Run() })
	for _, v := range []string{from, to} {
		if err := c.CreateVolume(v); err != nil {
			t.Fatal(err)
		}
	}
	seed := exec.Command("docker", "run", "--rm", "-v", from+":/v", "alpine:3", "sh", "-c",
		"mkdir -p /v/ws && echo hello > /v/ws/f && chown -R 1000:1000 /v/ws && chmod 600 /v/ws/f")
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v %s", err, out)
	}
	if err := c.CopyVolume("alpine:3", from, to); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "run", "--rm", "-v", to+":/v", "alpine:3", "sh", "-c", "stat -c '%u:%g %a' /v/ws/f; cat /v/ws/f").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "1000:1000 600\nhello" {
		t.Fatalf("copied = %q, %v", out, err)
	}
}
```

Run: `go test ./internal/backend/colima/ -run 'TestCopyVolumeArgv|TestVolumeExists'`
Expected: FAIL to compile (`CopyVolume`, `VolumeExists` undefined).

- [ ] **Step 2: Implement the colima ops and extend `VolumeOps`**

`internal/backend/colima/volume_copy.go`:

```go
package colima

import "strings"

// VolumeExists reports whether the named volume exists (`docker volume ls`
// filters by substring, so the name is matched exactly here).
func (c *Colima) VolumeExists(name string) (bool, error) {
	if err := c.preflight(); err != nil {
		return false, err
	}
	out, err := c.r.Output("docker", c.dargs("volume", "ls", "--quiet", "--filter", "name="+name)...)
	if err != nil {
		return false, err
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == name {
			return true, nil
		}
	}
	return false, nil
}

// CopyVolume copies every file of volume from into volume to (both exist)
// with a throwaway root container of image: no network, from mounted
// read-only, ownership and modes kept (cp -a). Only names reach argv.
func (c *Colima) CopyVolume(image, from, to string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.r.Run("docker", c.dargs("run", "--rm", "--network", "none", "--user", "0:0", "--entrypoint", "/bin/sh",
		"-v", from+":/from:ro", "-v", to+":/to", image, "-c", "cp -a /from/. /to/")...)
}
```

In `internal/backend/backend.go`'s `VolumeOps`, add:

```go
	// VolumeExists reports whether the named volume exists.
	VolumeExists(name string) (bool, error)
	// CopyVolume copies every file of volume from into volume to (both must
	// exist), ownership and modes kept, with a throwaway no-network root
	// container of image. The jam-rename migration's copy (renamed-from-at.md).
	CopyVolume(image, from, to string) error
```

In `internal/jam/launcher/launcher_test.go`'s `fakeOps`, add fields `exists map[string]bool`, `copies []string` and `copyErr error`, plus:

```go
func (f *fakeOps) VolumeExists(name string) (bool, error) { return f.exists[name], nil }
func (f *fakeOps) CopyVolume(image, from, to string) error {
	f.copies = append(f.copies, image+" "+from+" "+to)
	return f.copyErr
}
```

Run `go build ./... && go vet ./...`. Add the same two methods to any other implementer the compiler names; return `false, nil` / `nil` in test fakes.

Run: `go test ./internal/backend/colima/ -run 'TestCopyVolumeArgv|TestVolumeExists'`
Expected: PASS.

- [ ] **Step 3: Write the failing launcher migration tests**

`internal/jam/launcher/rename_migrate_test.go`:

```go
package launcher

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/naming"
	"github.com/aethons-tools/cove/internal/runner"
)

// volFake is a stateful docker for the migration: volumes with labels and a
// content marker, containers by name.
type volFake struct {
	*fakeOps
	vols       map[string]map[string]string // name → labels
	data       map[string]string            // name → content
	containers map[string]bool
	copyFail   map[string]bool // from volume → copy fails
	rmFail     map[string]bool // volume → removal fails
}

func newVolFake() *volFake {
	return &volFake{fakeOps: &fakeOps{}, vols: map[string]map[string]string{}, data: map[string]string{},
		containers: map[string]bool{}, copyFail: map[string]bool{}, rmFail: map[string]bool{}}
}

func (f *volFake) addVol(name, content string, labels ...string) {
	l := map[string]string{}
	for _, kv := range labels {
		k, v, _ := strings.Cut(kv, "=")
		l[k] = v
	}
	f.vols[name], f.data[name] = l, content
}
func (f *volFake) CreateVolume(name string, labels ...string) error {
	if _, ok := f.vols[name]; !ok {
		f.addVol(name, "", labels...)
	}
	return nil
}
func (f *volFake) RemoveVolumes(names ...string) error {
	for _, n := range names {
		if f.rmFail[n] {
			return errors.New("volume in use")
		}
		delete(f.vols, n)
		delete(f.data, n)
	}
	return nil
}
func (f *volFake) ListVolumes(key string, match ...string) (map[string]string, error) {
	out := map[string]string{}
next:
	for n, l := range f.vols {
		v, ok := l[key]
		if !ok {
			continue
		}
		for _, m := range match {
			k, want, _ := strings.Cut(m, "=")
			if l[k] != want {
				continue next
			}
		}
		out[n] = v
	}
	return out, nil
}
func (f *volFake) VolumeExists(name string) (bool, error) { _, ok := f.vols[name]; return ok, nil }
func (f *volFake) CopyVolume(image, from, to string) error {
	if image != basedigest.DefaultRef() {
		return errors.New("unexpected copy image " + image)
	}
	if f.copyFail[from] {
		f.data[to] = "partial"
		return errors.New("no space left on device")
	}
	f.data[to] = f.data[from]
	return nil
}
func (f *volFake) GetStatus(c string) (backend.State, error) {
	if f.containers[c] {
		return backend.StateRunning, nil
	}
	return backend.StateAbsent, nil
}
func (f *volFake) RemoveContainer(name string) error { delete(f.containers, name); return nil }

func migLauncher(t *testing.T, f *volFake) *Launcher {
	return New(Config{Ops: f, Runner: &runner.Fake{}, JamID: "j1", KnownHostsDir: t.TempDir()})
}

func oldVols(id string) (agent, ws string) {
	c := legacynames.Container(id)
	return naming.AgentDataVolume(c), naming.WorkspaceVolume(c)
}
func newVols(id string) (agent, ws string) {
	c := naming.StudioContainer(id)
	return naming.AgentDataVolume(c), naming.WorkspaceVolume(c)
}

func TestMigrateStateVolumesCopiesThenRemovesOld(t *testing.T) {
	f := newVolFake()
	oa, ow := oldVols("s1")
	f.addVol(oa, "agent-data-bytes", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.addVol(ow, "workspace-bytes", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.addVol("other-jam-vol", "x", legacynames.StateLabel+"=s9", legacynames.JamLabel+"=j2") // another Jam's: untouched
	done, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	na, nw := newVols("s1")
	if f.data[na] != "agent-data-bytes" || f.data[nw] != "workspace-bytes" {
		t.Fatalf("copied data = %q / %q", f.data[na], f.data[nw])
	}
	if f.vols[na][StateLabel] != "s1" || f.vols[na][JamLabel] != "j1" || f.vols[na][MigratedFromLabel] != oa {
		t.Fatalf("new labels = %v", f.vols[na])
	}
	if _, ok := f.vols[oa]; ok {
		t.Fatal("old volume must be removed after a successful copy")
	}
	if _, ok := f.vols["other-jam-vol"]; !ok {
		t.Fatal("another Jam's volume must be untouched")
	}
	if len(done) != 2 {
		t.Fatalf("done = %v", done)
	}
	again, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), []string{"s1"})
	if err != nil || len(again) != 0 {
		t.Fatalf("second run = %v, %v", again, err)
	}
}

func TestMigrateStateVolumesFailedCopyLeavesOldAndFails(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.copyFail[oa] = true
	_, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil)
	na, _ := newVols("s1")
	want := "migration: volume " + oa + " could not be copied to " + na + "; left in place"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if f.data[oa] != "precious" {
		t.Fatal("old volume must be left in place")
	}
	if _, ok := f.vols[na]; ok {
		t.Fatal("the partial new volume must be removed")
	}
}

func TestMigrateStateVolumesRetriesAPartialCopy(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	na, _ := newVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.addVol(na, "partial", StateLabel+"=s1", JamLabel+"=j1", MigratedFromLabel+"="+oa) // left by a crashed start
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if f.data[na] != "precious" {
		t.Fatalf("new = %q; a partial copy must be redone", f.data[na])
	}
}

func TestMigrateStateVolumesRefusesAForeignNewVolume(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	na, _ := newVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.addVol(na, "someone-elses", StateLabel+"=s1", JamLabel+"=j1") // not made by the migration
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err == nil {
		t.Fatal("must refuse to overwrite a new-named volume it did not create")
	}
	if f.data[na] != "someone-elses" || f.data[oa] != "precious" {
		t.Fatal("neither volume may change")
	}
}

func TestMigrateStateVolumesFindsUnlabelledStandingVolumes(t *testing.T) {
	f := newVolFake()
	oa, ow := oldVols("standing-p-r-n")
	f.addVol(oa, "a") // created before state volumes were labelled
	f.addVol(ow, "w")
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), []string{"standing-p-r-n"}); err != nil {
		t.Fatal(err)
	}
	na, nw := newVols("standing-p-r-n")
	if f.data[na] != "a" || f.data[nw] != "w" {
		t.Fatalf("vols = %v", slices.Sorted(maps.Keys(f.vols)))
	}
}

func TestRetireLegacyStudioRemovesContainerCacheAndKnownHosts(t *testing.T) {
	f := newVolFake()
	old := legacynames.Container("s1")
	f.containers[old] = true
	f.addVol(naming.DockerVolume(old), "cache")
	l := migLauncher(t, f)
	kh := filepath.Join(l.cfg.KnownHostsDir, old)
	if err := os.WriteFile(kh, []byte("pin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.RetireLegacyStudio(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if f.containers[old] {
		t.Fatal("old container must be removed")
	}
	if _, ok := f.vols[naming.DockerVolume(old)]; ok {
		t.Fatal("old -docker cache must be removed")
	}
	if _, err := os.Stat(kh); !os.IsNotExist(err) {
		t.Fatal("old known_hosts pin must be removed")
	}
	if err := l.RetireLegacyStudio(context.Background(), "s1"); err != nil {
		t.Fatalf("retiring again (nothing left) must succeed: %v", err)
	}
	if l.StudioLocation("s1") != naming.StudioContainer("s1") {
		t.Fatal("StudioLocation is the new container name")
	}
}
```

Run: `go test ./internal/jam/launcher/ -run 'MigrateStateVolumes|RetireLegacyStudio'`
Expected: FAIL to compile (`MigrateStateVolumes`, `RetireLegacyStudio`, `StudioLocation`, `MigratedFromLabel` undefined).

- [ ] **Step 4: Implement the launcher migration**

`internal/jam/launcher/rename_migrate.go`:

```go
package launcher

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/naming"
)

// MigratedFromLabel marks a state volume the jam-rename migration created;
// its value is the pre-rename volume it was copied from, so a retry can tell
// its own partial copy from a volume it must not overwrite.
const MigratedFromLabel = "jam.studio.migrated-from"

// StudioLocation is actorID's container name: the Location Raise records.
func (l *Launcher) StudioLocation(actorID string) string { return naming.StudioContainer(actorID) }

// RetireLegacyStudio removes actorID's pre-rename container (it runs an image
// whose agent cannot attach to this Jam), its -docker cache volume and its
// pinned known_hosts file. Absent ones are fine; its state volumes are
// MigrateStateVolumes'.
func (l *Launcher) RetireLegacyStudio(ctx context.Context, actorID string) error {
	old := legacynames.Container(actorID)
	if st, err := l.cfg.Ops.GetStatus(old); err != nil {
		return fmt.Errorf("migration: inspect %s: %w", old, err)
	} else if st != backend.StateAbsent {
		if err := l.cfg.Ops.RemoveContainer(old); err != nil {
			return fmt.Errorf("migration: remove %s: %w", old, err)
		}
	}
	if err := l.cfg.Ops.RemoveVolumes(naming.DockerVolume(old)); err != nil {
		l.cfg.Log.Warn("migration: removing a pre-rename docker cache failed", "volume", naming.DockerVolume(old), "error", err.Error())
	}
	if err := os.Remove(filepath.Join(l.cfg.KnownHostsDir, old)); err != nil && !errors.Is(err, os.ErrNotExist) {
		l.cfg.Log.Warn("migration: removing a pre-rename known_hosts pin failed", "file", old, "error", err.Error())
	}
	l.cfg.Log.Info("pre-rename studio retired", "id", actorID, "container", old)
	return nil
}

// MigrateStateVolumes copies this Jam's pre-rename state volumes to their new
// names: every volume carrying the old state label and this Jam's old Jam
// label, plus the agent-data/workspace volumes of each actor in also that
// exist unlabelled. Each copy goes into a fresh volume labelled as state and
// MigratedFromLabel; the old one is removed only after its copy succeeds. A
// copy or removal that fails stops the migration (serve start fails closed:
// nothing raises onto an empty volume while its data sits in the old one); a
// retry redoes a partial copy and refuses to overwrite a new-named volume the
// migration did not create. It returns "old → new" per volume moved.
func (l *Launcher) MigrateStateVolumes(ctx context.Context, also []string) ([]string, error) {
	owners, err := l.cfg.Ops.ListVolumes(legacynames.StateLabel, legacynames.JamLabel+"="+l.cfg.JamID)
	if err != nil {
		return nil, fmt.Errorf("migration: list pre-rename volumes: %w", err)
	}
	for _, id := range also {
		for _, v := range stateVolumes(legacynames.Container(id)) {
			if _, seen := owners[v]; seen {
				continue
			}
			ok, err := l.cfg.Ops.VolumeExists(v)
			if err != nil {
				return nil, fmt.Errorf("migration: inspect volume %s: %w", v, err)
			}
			if ok {
				owners[v] = id
			}
		}
	}
	marked, err := l.cfg.Ops.ListVolumes(MigratedFromLabel)
	if err != nil {
		return nil, fmt.Errorf("migration: list migrated volumes: %w", err)
	}
	var done []string
	for _, old := range slices.Sorted(maps.Keys(owners)) {
		id := owners[old]
		nw, ok := renamedStateVolume(old, id)
		if !ok {
			if old == naming.DockerVolume(legacynames.Container(id)) {
				_ = l.cfg.Ops.RemoveVolumes(old) // a cache once labelled as state: never copied
			} else {
				l.cfg.Log.Warn("migration: pre-rename volume with an unknown name left in place", "volume", old, "id", id)
			}
			continue
		}
		failed := fmt.Errorf("migration: volume %s could not be copied to %s; left in place", old, nw)
		exists, err := l.cfg.Ops.VolumeExists(nw)
		if err != nil {
			return done, fmt.Errorf("migration: inspect volume %s: %w", nw, err)
		}
		if exists {
			if marked[nw] != old {
				l.cfg.Log.Warn("migration: the new volume already exists and was not made by the migration", "from", old, "to", nw)
				return done, failed
			}
			if err := l.cfg.Ops.RemoveVolumes(nw); err != nil {
				return done, failed
			}
		}
		if err := l.cfg.Ops.CreateVolume(nw, StateLabel+"="+id, JamLabel+"="+l.cfg.JamID, MigratedFromLabel+"="+old); err != nil {
			return done, failed
		}
		if err := l.cfg.Ops.CopyVolume(basedigest.DefaultRef(), old, nw); err != nil {
			l.cfg.Log.Warn("migration: volume copy failed", "from", old, "to", nw, "error", err.Error())
			_ = l.cfg.Ops.RemoveVolumes(nw)
			return done, failed
		}
		if err := l.cfg.Ops.RemoveVolumes(old); err != nil {
			return done, fmt.Errorf("migration: volume %s was copied to %s but could not be removed: %w", old, nw, err)
		}
		done = append(done, old+" → "+nw)
	}
	if len(done) > 0 {
		l.cfg.Log.Info("state volumes migrated to the jam names", "volumes", done)
	}
	return done, nil
}

// renamedStateVolume is old's new name when old is one of id's pre-rename
// state volumes.
func renamedStateVolume(old, id string) (string, bool) {
	oc, nc := legacynames.Container(id), naming.StudioContainer(id)
	switch old {
	case naming.AgentDataVolume(oc):
		return naming.AgentDataVolume(nc), true
	case naming.WorkspaceVolume(oc):
		return naming.WorkspaceVolume(nc), true
	}
	return "", false
}
```

(`l.cfg.Log` is never nil after `New`: "nil → discard". If `New` doesn't default it, use the launcher's existing log accessor.)

Run: `go test ./internal/jam/launcher/`
Expected: PASS.

- [ ] **Step 5: Write the failing jam-side studio migration tests**

`internal/jam/rename_migrate_test.go`:

```go
package jam

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/legacynames"
)

type fakeRenamer struct {
	retired []string
	also    []string
	moved   []string
	err     error
}

func (f *fakeRenamer) RetireLegacyStudio(_ context.Context, id string) error {
	f.retired = append(f.retired, id)
	return nil
}
func (f *fakeRenamer) MigrateStateVolumes(_ context.Context, also []string) ([]string, error) {
	f.also = also
	return f.moved, f.err
}
func (f *fakeRenamer) StudioLocation(id string) string { return "jam-studio-" + id }

func TestMigrateStudioNamesRetiresAndRewrites(t *testing.T) {
	st := NewMemStore()
	put := func(i Instance) {
		if err := st.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	put(Instance{ActorID: "s1", SessionKind: SessionKindStanding, Phase: PhaseLive, Location: legacynames.Container("s1"), ImageTag: "old-tag", KitDraftImage: "old-draft"})
	put(Instance{ActorID: "e1", Phase: PhaseIdled, Location: legacynames.Container("e1"), ImageTag: "old-tag"})
	put(Instance{ActorID: "g1", Phase: PhaseGone, Location: legacynames.Container("g1")})
	put(Instance{ActorID: "n1", Phase: PhaseLive, Location: "jam-studio-n1", ImageTag: "new-tag"})
	if err := st.PutStandingSession("p", "r", "idle", "s2"); err != nil {
		t.Fatal(err)
	}
	r := &fakeRenamer{}
	rep, err := MigrateStudioNames(context.Background(), st, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.retired, []string{"e1", "s1"}) && !slices.Equal(r.retired, []string{"s1", "e1"}) {
		t.Fatalf("retired = %v (gone and already-renamed instances are skipped)", r.retired)
	}
	if !slices.Equal(r.also, []string{"s1", "s2"}) {
		t.Fatalf("standing ids = %v", r.also)
	}
	s1, _ := st.GetInstance("s1")
	if s1.Location != "jam-studio-s1" || s1.ImageTag != "" || s1.KitDraftImage != "" || s1.Phase != PhaseLost {
		t.Fatalf("s1 = %+v", s1)
	}
	if n1, _ := st.GetInstance("n1"); n1.ImageTag != "new-tag" || n1.Phase != PhaseLive {
		t.Fatalf("an already-renamed instance changed: %+v", n1)
	}
	if len(rep.Retired) != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestMigrateStudioNamesIsIdempotent(t *testing.T) {
	st := NewMemStore()
	_ = st.PutInstance(Instance{ActorID: "s1", Phase: PhaseLive, Location: legacynames.Container("s1")})
	if _, err := MigrateStudioNames(context.Background(), st, &fakeRenamer{}, nil); err != nil {
		t.Fatal(err)
	}
	r := &fakeRenamer{}
	if _, err := MigrateStudioNames(context.Background(), st, r, nil); err != nil || len(r.retired) != 0 {
		t.Fatalf("second run retired %v, %v", r.retired, err)
	}
}

func TestMigrateStudioNamesFailsClosedOnAVolumeError(t *testing.T) {
	st := NewMemStore()
	r := &fakeRenamer{err: errText("migration: volume a could not be copied to b; left in place")}
	if _, err := MigrateStudioNames(context.Background(), st, r, nil); err == nil || err.Error() != "migration: volume a could not be copied to b; left in place" {
		t.Fatalf("err = %v", err)
	}
}

type errText string

func (e errText) Error() string { return string(e) }

// After the migration, the supervisor's first reconcile finishes the retired
// studio's teardown through the normal path.
func TestMigratedStudioIsTornDownByReconcile(t *testing.T) {
	st := NewMemStore()
	_ = st.PutInstance(Instance{ActorID: "s1", Phase: PhaseLive, Location: legacynames.Container("s1")})
	if _, err := MigrateStudioNames(context.Background(), st, &fakeRenamer{}, nil); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup := NewSupervisor(st, fl, "holder-A", time.Minute, time.Minute, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fl.tornDown, "s1") {
		t.Fatalf("tornDown = %v", fl.tornDown)
	}
	if _, ok := st.GetInstance("s1"); ok {
		t.Fatal("the retired instance must be removed once torn down")
	}
}
```

(If `PutStandingSession` takes an `ident.ID` project, convert `"p"` the way `standing_sessions` tests do, e.g. `ident.ID("p")`.)

Run: `go test ./internal/jam/ -run 'MigrateStudioNames|MigratedStudio'`
Expected: FAIL to compile (`MigrateStudioNames` undefined).

- [ ] **Step 6: Implement `MigrateStudioNames`**

`internal/jam/rename_migrate.go`:

```go
package jam

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/legacynames"
)

// StudioRenamer is the launcher surface the studio-name migration drives
// (the local launcher; *launcher.Launcher).
type StudioRenamer interface {
	RetireLegacyStudio(ctx context.Context, actorID string) error
	MigrateStateVolumes(ctx context.Context, also []string) ([]string, error)
	StudioLocation(actorID string) string
}

// StudioRenameMigration reports a MigrateStudioNames run.
type StudioRenameMigration struct {
	Retired []string // instances whose pre-rename container was removed
	Volumes []string // "old → new" per state volume moved
}

// MigrateStudioNames is the jam rename's studio migration
// (docs/usage/jam/renamed-from-at.md), run at serve start before the
// supervisor and the standing reconciler. Each instance not gone whose
// Location is a pre-rename container is retired (container, cache and known
// hosts pin removed), then rewritten — the new container name as Location,
// ImageTag and KitDraftImage cleared (stale), Phase lost so the supervisor's
// next reconcile finishes its teardown. One row per instance, each after its
// own docker side effect, so a retry is idempotent. Then every standing
// session's state volumes (and any this Jam labelled) move to their new
// names. Any failure stops it: serve start fails closed.
func MigrateStudioNames(ctx context.Context, store Store, r StudioRenamer, log *slog.Logger) (StudioRenameMigration, error) {
	var rep StudioRenameMigration
	var standing []string
	for _, ref := range store.ListStandingSessions() {
		standing = append(standing, ref.SessionID)
	}
	for _, inst := range store.ListInstances() {
		if inst.SessionKind == SessionKindStanding {
			standing = append(standing, inst.ActorID)
		}
		if inst.Phase == PhaseGone || !strings.HasPrefix(inst.Location, legacynames.ContainerPrefix) {
			continue
		}
		if err := r.RetireLegacyStudio(ctx, inst.ActorID); err != nil {
			return rep, err
		}
		inst.Location = r.StudioLocation(inst.ActorID)
		inst.ImageTag, inst.KitDraftImage = "", ""
		if inst.Phase != PhaseTerminating {
			inst.Phase = PhaseLost
		}
		if err := store.PutInstance(inst); err != nil {
			return rep, fmt.Errorf("migration: studio %s: %w", inst.ActorID, err)
		}
		rep.Retired = append(rep.Retired, inst.ActorID)
	}
	slices.Sort(standing)
	vols, err := r.MigrateStateVolumes(ctx, slices.Compact(standing))
	rep.Volumes = vols
	if err != nil {
		return rep, err
	}
	if log != nil && (len(rep.Retired) > 0 || len(rep.Volumes) > 0) {
		log.Info("studios migrated to the jam names", "retired", rep.Retired, "volumes", len(rep.Volumes),
			"see", "docs/usage/jam/renamed-from-at.md")
	}
	return rep, nil
}
```

Run: `go test ./internal/jam/ -run 'MigrateStudioNames|MigratedStudio'`
Expected: PASS. If `Reconcile` leaves a `lost` instance in the store after `Teardown` (it calls `RemoveInstance` on success), the second assertion pins that.

- [ ] **Step 7: Write the failing kit tests**

`internal/jam/kit_rename_migrate_test.go`:

```go
package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/legacynames"
)

// oldDockerfile uses every pre-rename kit name.
var oldDockerfile = "ARG " + legacynames.KitRenames[2].Old + "\nFROM ${" + legacynames.KitRenames[2].Old + "}\n" +
	"ARG " + legacynames.KitRenames[1].Old + "\nENV " + legacynames.KitRenames[3].Old + "=\"GOPATH\"\n"

// seedKit stores files as kit name's next version straight through the store
// (PushKitFiles would refuse the old names).
func seedKit(t *testing.T, st Store, name string, files map[string]string) int {
	t.Helper()
	var fs []kittree.File
	for p, d := range files {
		fs = append(fs, kittree.File{Path: p, Mode: 0o644, Data: []byte(d)})
	}
	tree, blobs, err := kittree.BuildFiles(fs)
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.PushKitVersion(name, tree.Hash(), KitObjects{Trees: []kittree.Tree{tree}, Blobs: blobs}, KitVersionMeta{PushedAt: time.Now()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func kitFile(t *testing.T, st Store, name, path string) string {
	t.Helper()
	cur, ok := LoadKitVersion(st, name, 0)
	if !ok {
		t.Fatalf("no kit %s", name)
	}
	for _, e := range cur.Tree.Entries {
		if e.Path == path {
			b, _ := st.KitBlob(e.SHA256)
			return string(b)
		}
	}
	t.Fatalf("kit %s has no %s", name, path)
	return ""
}

const parentYML = "kind: studio\nbase:\n  context-dir: context\n"

func TestMigrateKitRenameRewritesCurrentTree(t *testing.T) {
	st := NewMemStore()
	script := "echo " + legacynames.KitRenames[3].Old + " stays in scripts\n"
	seedKit(t, st, "base", map[string]string{"kit.yml": parentYML, "context/Dockerfile": oldDockerfile, "context/install.sh": script})
	tr := condition.New(condition.Options{})
	rep, err := MigrateKitRename(st, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := st.GetKit("base")
	if k.Current != 2 || k.Meta[2].Origin != KitOriginMigration {
		t.Fatalf("current v%d meta %+v", k.Current, k.Meta[k.Current])
	}
	got := kitFile(t, st, "base", "context/Dockerfile")
	if _, old := legacynames.FindKitName(got); old || !strings.Contains(got, "FROM ${JAM_STUDIO_BASE_IMAGE}") || !strings.Contains(got, "ENV JAM_SSHENV=") {
		t.Fatalf("Dockerfile =\n%s", got)
	}
	if kitFile(t, st, "base", "context/install.sh") != script {
		t.Fatal("only kit.yml and Dockerfiles are rewritten")
	}
	if len(rep.Rewritten) != 1 || rep.Rewritten[0] != "base@v2" || tr.IsOpen(condition.Key(KitMigrateKind, "base")) {
		t.Fatalf("report %+v; condition open %v", rep, tr.IsOpen(condition.Key(KitMigrateKind, "base")))
	}
}

func TestMigrateKitRenameIsIdempotent(t *testing.T) {
	st := NewMemStore()
	seedKit(t, st, "base", map[string]string{"kit.yml": parentYML, "context/Dockerfile": oldDockerfile})
	if _, err := MigrateKitRename(st, nil, nil); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateKitRename(st, nil, nil)
	if k, _ := st.GetKit("base"); err != nil || len(rep.Rewritten) != 0 || k.Current != 2 {
		t.Fatalf("second run: %+v, %v, current v%d", rep, err, k.Current)
	}
}

func TestMigrateKitRenameRepinsAChildPinnedToTheOldParent(t *testing.T) {
	st := NewMemStore()
	seedKit(t, st, "base", map[string]string{"kit.yml": parentYML, "context/Dockerfile": oldDockerfile})
	seedKit(t, st, "child", map[string]string{"kit.yml": "kind: studio\nbase:\n  kit: base@v1\n"})
	if _, err := MigrateKitRename(st, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := kitFile(t, st, "child", "kit.yml"); !strings.Contains(got, "kit: base@v2") {
		t.Fatalf("child kit.yml =\n%s", got)
	}
	if k, _ := st.GetKit("child"); k.Meta[k.Current].Origin != KitOriginMigration {
		t.Fatalf("child meta = %+v", k.Meta[k.Current])
	}
}

func TestMigrateKitRenameKeepsAnUnrewritableKit(t *testing.T) {
	st := NewMemStore()
	// Its parent does not exist, so the rewritten version cannot validate.
	seedKit(t, st, "orphan", map[string]string{"kit.yml": "kind: studio\nbase:\n  kit: nosuch\n", "Dockerfile": oldDockerfile})
	tr := condition.New(condition.Options{})
	rep, err := MigrateKitRename(st, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := st.GetKit("orphan"); k.Current != 1 || len(rep.Kept) != 1 {
		t.Fatalf("current v%d, report %+v", k.Current, rep)
	}
	c, ok := tr.Get(condition.Key(KitMigrateKind, "orphan"))
	if !ok || !strings.Contains(c.Summary, "pre-rename name") || !strings.Contains(c.Fix, "renamed-from-at.md") {
		t.Fatalf("condition = %+v, %v", c, ok)
	}
}

func TestPushKitFilesRefusesPreRenameNames(t *testing.T) {
	st := NewMemStore()
	_, _, err := PushKitFiles(st, "k", []kittree.File{
		{Path: "kit.yml", Mode: 0o644, Data: []byte(parentYML)},
		{Path: "context/Dockerfile", Mode: 0o644, Data: []byte(oldDockerfile)},
	})
	if err == nil || !strings.Contains(err.Error(), "context/Dockerfile uses "+legacynames.KitRenames[2].Old+", renamed JAM_STUDIO_BASE_IMAGE") {
		t.Fatalf("err = %v", err)
	}
}
```

(`seedKit` keys the files map, so its order is random; `kittree.BuildFiles` sorts entries. `tr.Get` returns `(Condition, bool)`; use the tracker's accessor if it differs. If `validateKit` accepts a missing parent, make the orphan's `kit.yml` invalid for the chain in another way that the migration's `planKitTreeLocked` rejects, e.g. `base:\n  kit: orphan` (self-reference). Keep the assertion that the version is kept and the condition opens.)

Run: `go test ./internal/jam/ -run 'MigrateKitRename|PushKitFilesRefusesPreRename'`
Expected: FAIL to compile (`MigrateKitRename`, `KitOriginMigration` undefined).

- [ ] **Step 8: Implement the kit migration, the push refusal and the condition**

`internal/jam/kit_rename_migrate.go`:

```go
package jam

import (
	"fmt"
	"log/slog"
	"slices"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitOriginMigration is KitVersionMeta.Origin of a version the jam-rename
// kit migration wrote, so a kit's history shows it.
const KitOriginMigration = "migration"

// KitRenameMigration reports a MigrateKitRename run.
type KitRenameMigration struct {
	Rewritten []string // name@vN of each version written
	Kept      []string // kits whose current version uses an old name and could not be rewritten
}

// MigrateKitRename is the jam rename's kit migration
// (docs/usage/jam/renamed-from-at.md), run at serve start after
// MigrateKitTrees. Each kit whose current tree's kit.yml or Dockerfiles use a
// pre-rename build-arg/ENV name or base repository gets a new version with
// the new names (Meta.Origin "migration"); a kit pinned to a parent version
// that still uses old names, while the parent's current is a migration
// version, is re-pinned to it. Passes repeat (one PutKitDocs each) until
// nothing changes, so chains of any depth settle. A kit that cannot be
// rewritten keeps its version and opens kit.migrate:<name>
// (kitMigrateConditions). Only a store write error is returned.
func MigrateKitRename(store Store, t *condition.Tracker, log *slog.Logger) (KitRenameMigration, error) {
	configMu.Lock()
	defer configMu.Unlock()
	kitMu.Lock()
	defer kitMu.Unlock()
	var rep KitRenameMigration
	kept := map[string]bool{}
	for pass := 0; pass <= len(store.ListKits()); pass++ {
		var pend []pendingKitVersion
		for _, k := range store.ListKits() {
			p, ok, err := renamedKitVersionLocked(store, k)
			if err != nil {
				kept[k.Name] = true
				if log != nil {
					log.Warn("kit kept on its pre-rename names", "kit", k.Name, "error", err.Error())
				}
				continue
			}
			if ok {
				pend = append(pend, p)
				delete(kept, k.Name)
			}
		}
		if len(pend) == 0 {
			break
		}
		if err := writeKitVersionsLocked(store, pend); err != nil {
			return rep, fmt.Errorf("migrate kits to the jam names: %w", err)
		}
		for _, p := range pend {
			rep.Rewritten = append(rep.Rewritten, fmt.Sprintf("%s@v%d", p.ref.ID, p.ref.Version))
		}
	}
	for name := range kept {
		rep.Kept = append(rep.Kept, name)
	}
	slices.Sort(rep.Kept)
	if log != nil && len(rep.Rewritten) > 0 {
		log.Info("kits migrated to the jam names", "versions", rep.Rewritten, "kept", rep.Kept)
	}
	kitMigrateConditions(store, t)
	return rep, nil
}

// renamedKitVersionLocked plans kit k's migration version: ok=false when its
// current version needs none (or has no tree). The caller holds kitMu.
func renamedKitVersionLocked(store Store, k Kit) (pendingKitVersion, bool, error) {
	if k.Current == 0 || k.Broken[k.Current] != "" {
		return pendingKitVersion{}, false, nil
	}
	cur, ok := LoadKitVersion(store, k.Name, 0)
	if !ok || cur.TreeHash == "" {
		return pendingKitVersion{}, false, nil
	}
	files, err := currentFilesWithKitYML(store, k.Name, cur.KitYML)
	if err != nil {
		return pendingKitVersion{}, false, err
	}
	changed := false
	for i, f := range files {
		if !legacynames.KitFileScanned(f.Path) {
			continue
		}
		if out, c := legacynames.RewriteKitText(string(f.Data)); c {
			files[i].Data, changed = []byte(out), true
		}
	}
	if repin, ok := repinTarget(store, cur); ok {
		for i, f := range files {
			if f.Path == kittree.KitFile {
				yml, err := studio.SetBaseKit(f.Data, repin)
				if err != nil {
					return pendingKitVersion{}, false, err
				}
				files[i].Data, changed = yml, true
			}
		}
	}
	if !changed {
		return pendingKitVersion{}, false, nil
	}
	tree, objs, sk, err := kitTreeOf(k.Name, files)
	if err != nil {
		return pendingKitVersion{}, false, err
	}
	ref, meta, unchanged, err := planKitTreeLocked(store, k.Name, tree, sk, KitOriginMigration)
	if err != nil || unchanged {
		return pendingKitVersion{}, false, err
	}
	return pendingKitVersion{ref: ref, meta: meta, tree: tree, objs: objs}, true, nil
}

// repinTarget is "parent@vN" when cur pins a parent version that still uses
// a pre-rename name while the parent's current version (N) is a migration
// version.
func repinTarget(store Store, cur KitVersion) (string, bool) {
	if cur.Err != nil || !cur.Kit.Base.HasParent() {
		return "", false
	}
	parent, pin, err := cur.Kit.Base.ParentRef()
	if err != nil || pin == 0 {
		return "", false
	}
	pk, ok := store.GetKit(parent)
	if !ok || pin == pk.Current || pk.Meta[pk.Current].Origin != KitOriginMigration {
		return "", false
	}
	pinned, ok := LoadKitVersion(store, parent, pin)
	if !ok {
		return "", false
	}
	if _, _, old := legacyKitName(store, pinned); !old {
		return "", false
	}
	return fmt.Sprintf("%s@v%d", parent, pk.Current), true
}

// legacyKitName is the first pre-rename name in v's kit.yml or Dockerfiles,
// and the file it is in.
func legacyKitName(store KitReader, v KitVersion) (old, path string, ok bool) {
	for _, e := range v.Tree.Entries {
		if !legacynames.KitFileScanned(e.Path) {
			continue
		}
		b, found := store.KitBlob(e.SHA256)
		if !found {
			continue
		}
		if old, ok := legacynames.FindKitName(string(b)); ok {
			return old, e.Path, true
		}
	}
	return "", "", false
}
```

(If `KitReader` lacks `KitBlob`, take `Store`.)

In `internal/jam/kit_push.go` `kitTreeOf`, after `kittree.Ignore`:

```go
	for _, f := range files {
		if !legacynames.KitFileScanned(f.Path) {
			continue
		}
		if old, ok := legacynames.FindKitName(string(f.Data)); ok {
			return kittree.Tree{}, KitObjects{}, studio.StudioKit{}, fmt.Errorf("kit %s: %s uses %s, renamed %s (docs/usage/jam/renamed-from-at.md)",
				name, f.Path, old, legacynames.NewKitName(old))
		}
	}
```

In `internal/jam/kit_migrate.go` `kitMigrateConditions`, replace the `if !broken { continue }` branch so a non-broken current version is checked for old names:

```go
	for _, k := range store.ListKits() {
		key := condition.Key(KitMigrateKind, k.Name)
		if reason, broken := k.Broken[k.Current]; broken {
			open[key] = true
			t.Raise(condition.Condition{Key: key, Severity: condition.Warning,
				Summary: fmt.Sprintf("kit %s v%d could not be migrated to a tree", k.Name, k.Current),
				Detail:  reason,
				Fix:     "push the kit as a directory (a Dockerfile next to kit.yml), or pin a version that migrated (jam kit pin)"})
			continue
		}
		cur, ok := LoadKitVersion(store, k.Name, 0)
		if !ok || cur.TreeHash == "" {
			continue
		}
		if old, path, found := legacyKitName(store, cur); found {
			open[key] = true
			t.Raise(condition.Condition{Key: key, Severity: condition.Warning,
				Summary: fmt.Sprintf("kit %s v%d still uses the pre-rename name %s", k.Name, k.Current, old),
				Detail:  fmt.Sprintf("%s names %s; Jam now passes %s", path, old, legacynames.NewKitName(old)),
				Fix:     "rename it in the kit and push a new version (docs/usage/jam/renamed-from-at.md)"})
		}
	}
```

Run: `go test ./internal/jam/ -run 'MigrateKitRename|PushKitFilesRefusesPreRename|MigrateKitTrees|RunConfigDriftCheck'`
Expected: PASS.

- [ ] **Step 9: Add the conformance case (MemStore here, Postgres in CI)**

`internal/jam/storetest/rename_migrations.go`:

```go
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/legacynames"
)

type recordRenamer struct{ retired []string }

func (r *recordRenamer) RetireLegacyStudio(_ context.Context, id string) error {
	r.retired = append(r.retired, id)
	return nil
}
func (r *recordRenamer) MigrateStateVolumes(context.Context, []string) ([]string, error) { return nil, nil }
func (r *recordRenamer) StudioLocation(id string) string                                 { return "jam-studio-" + id }

// runRenameMigrationConformance: the jam-rename migrations leave both stores
// in the same state, and a second run changes nothing.
func runRenameMigrationConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	t.Run("rename_migrations", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutInstance(jam.Instance{ActorID: "s1", Phase: jam.PhaseLive, Location: legacynames.Container("s1"), ImageTag: "old"}); err != nil {
			t.Fatal(err)
		}
		df := "ARG " + legacynames.KitRenames[2].Old + "\nFROM ${" + legacynames.KitRenames[2].Old + "}\n"
		tree, blobs, err := kittree.BuildFiles([]kittree.File{
			{Path: "kit.yml", Mode: 0o644, Data: []byte("kind: studio\nbase:\n  context-dir: context\n")},
			{Path: "context/Dockerfile", Mode: 0o644, Data: []byte(df)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PushKitVersion("base", tree.Hash(), jam.KitObjects{Trees: []kittree.Tree{tree}, Blobs: blobs}, jam.KitVersionMeta{PushedAt: time.Now()}, nil); err != nil {
			t.Fatal(err)
		}
		for run := 0; run < 2; run++ {
			if _, err := jam.MigrateStudioNames(context.Background(), s, &recordRenamer{}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := jam.MigrateKitRename(s, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		inst, _ := s.GetInstance("s1")
		if inst.Location != "jam-studio-s1" || inst.ImageTag != "" || inst.Phase != jam.PhaseLost {
			t.Fatalf("instance = %+v", inst)
		}
		k, _ := s.GetKit("base")
		if k.Current != 2 || k.Meta[2].Origin != jam.KitOriginMigration {
			t.Fatalf("kit current v%d meta %+v (exactly one migration version)", k.Current, k.Meta[k.Current])
		}
	})
}
```

Add `runRenameMigrationConformance(t, newStore)` to `RunConformance`'s list in `conformance.go`.

Run: `go test ./internal/jam/ -run Conformance`
Expected: PASS (the MemStore run). The Postgres run is `TestPostgresStoreConformance` in CI (`JAM_TEST_POSTGRES_DSN`).

- [ ] **Step 10: Wire both migrations into serve start**

In `cmd/jam/main.go`:
- `builtLauncher` gains `local *launcher.Launcher // the colima launcher (nil without one): the studio-name migration's`. Set `out.local = colimaLauncher` where `buildLauncher` creates it.
- Right after the `jam.MigrateKitTrees` block:

```go
	// The jam rename's kit migration (docs/usage/jam/renamed-from-at.md):
	// kits still on pre-rename build-arg/ENV names get a migration version.
	// A store write error fails closed, like MigrateKitTrees.
	if mig, err := jam.MigrateKitRename(st, conds, log); err != nil {
		fmt.Fprintln(stderr, "jam:", err)
		return 1
	} else if len(mig.Kept) > 0 {
		log.Warn("kits kept on pre-rename names (kit.migrate conditions open)", "kits", mig.Kept)
	}
```

- Right after `built, err := buildLauncher(...)` succeeds and before `jam.NewSupervisor`:

```go
	// The jam rename's studio migration: retire pre-rename containers and move
	// standing sessions' state volumes before anything raises. Fails closed.
	if built.local != nil {
		if _, err := jam.MigrateStudioNames(ctx, st, built.local, log); err != nil {
			fmt.Fprintln(stderr, "jam:", err)
			return 1
		}
	}
```

(Use the serve function's existing context variable; find it with `grep -n 'ctx' cmd/jam/main.go | sed -n '1,40p'` around `cmdServe`. If none exists at that point, use `context.Background()`.)

- [ ] **Step 11: Gates, then commit**

Run the full gates, plus `go test -tags integration ./internal/backend/colima/ -run TestCopyVolumeRealDocker -v` (skips without docker).
Expected: all PASS.

```bash
git add -A
git commit -m "feat(jam): serve-start migrations for the jam rename — retire old studios, move state volumes, rewrite kits

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Docs, the upgrade runbook, and the guard over the docs

**Files:**
- Create: `docs/usage/jam/renamed-from-at.md`
- Move: `docs/usage/at-mint.md` → `docs/usage/jam-mint.md`
- Modify: `docs/usage/**/*.md`, `docs/{INDEX,OVERVIEW,DEVELOPMENT}.md`, `README.md`, `AGENTS.md`, `docs/usage/jam/renamed-from-harbor.md`, `docs/usage/jam/INDEX.md`, `docs/usage/INDEX.md`, `renamed_test.go`

**Interfaces:**
- Consumes: every name above; `logging.RenamedFromAtDoc` points at the runbook created here.
- Produces: the runbook at `docs/usage/jam/renamed-from-at.md` (owns: the at-/cove → jam old→new table, the upgrade steps, what the serve-start migrations do).

- [ ] **Step 1: Capture the docs-audit baseline**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep ERROR | grep -v superpowers/ > /tmp/audit-baseline.txt; wc -l /tmp/audit-baseline.txt`
Expected: the pre-existing errors (record the count).

- [ ] **Step 2: Turn the guard on for the docs (failing)**

In `renamed_test.go`:
- delete `docsPending` and its call;
- add to `renamedAllow`:

```go
	{"docs/usage/jam/renamed-from-at.md", nil},
	{"docs/OVERVIEW.md", []string{`at-cove`}}, // its interim slice-2 note; slice 4 rewrites OVERVIEW
	{"images/jam-studio-base/image-files/home/agent/.init-agent-data/", []string{`at-cove`}}, // the in-image agent docs describe the deleted at-cove workflow; slice 4 rewrites them
```

Run: `go test -run TestNoPreRenameNames .`
Expected: FAIL, listing the docs' old names (about 400 lines).

- [ ] **Step 3: Rename the names in the docs**

```bash
git mv docs/usage/at-mint.md docs/usage/jam-mint.md
cat > /tmp/map-t9.tsv <<'EOF'
at-mint.md	jam-mint.md
`at-mint github`	`jam mint github`
`at-mint anthropic`	`jam mint anthropic`
`at-mint`	`jam mint`
at-mint	jam mint
.at-jam/	.jam/
~/.config/at-jam	~/.config/jam
~/.local/state/at-jam	~/.local/state/jam
~/.config/at-cove	~/.config/jam
# >>> at-jam	# >>> jam
# <<< at-jam	# <<< jam
cmd/at-jam	cmd/jam
at-jam	jam
cmd/cove-master	cmd/jam-studio-agent
/agent-data/cove-master.log	/agent-data/jam-studio-agent.log
cove-master	jam-studio-agent
ghcr.io/aethons-tools/cove-base-image	ghcr.io/aethons-tools/jam-studio-base
ghcr.io/aethons-tools/cove-image	ghcr.io/aethons-tools/jam-studio
cove-base-image	jam-studio-base
re:\bcove-image\b	jam-studio
cove-kit-builds	jam-kit-builds
cove-kit-base:	jam-kit-base:
cove-kit-draft:	jam-kit-draft:
cove-kit:	jam-kit:
harbor.cove.state	jam.studio.state
harbor.cove.jam	jam.studio.jam
harbor.cove	jam.studio
atcove-cove-	jam-studio-
/usr/local/lib/cove/	/usr/local/lib/jam/
cove-egress	jam-egress
sshd_config.d/cove.conf	sshd_config.d/00-jam.conf
cove-git-credential.sh	jam-git-credential.sh
/dev/shm/cove-master-env	/dev/shm/jam-studio-agent-env
/dev/shm/cove-	/dev/shm/jam-
re:\bAT_JAM_STUDIO_BASE_IMAGE\b	JAM_STUDIO_BASE_IMAGE
re:\bAT_JAM_STUDIO_TARGET_ARCH\b	JAM_STUDIO_TARGET_ARCH
re:\bCOVE_BASE_IMAGE\b	JAM_STUDIO_BASE_IMAGE
re:\bCOVE_SSHENV\b	JAM_SSHENV
re:\bAT_MINT_	JAM_MINT_
re:\bAT_LOG_	JAM_LOG_
re:\bAT_JAM_	JAM_
re:\bAT_COVE_	JAM_
COVE_SYSTEM_BINDIR	JAM_SYSTEM_BINDIR
COVE_SYSTEM	JAM_SYSTEM
COVE_VERSION	JAM_VERSION
COVE_REPO	JAM_REPO
COVE_INSTALL_LIB	JAM_INSTALL_LIB
COVE_RUN_TIMEOUT	JAM_RUN_TIMEOUT
COVE_SHADOW_DIRS	JAM_SHADOW_DIRS
COVE_DOCKER	JAM_DOCKER
COVE_BROWSER	JAM_BROWSER
cove_<version>	jam_<version>
cove_<ver>	jam_<ver>
EOF
/tmp/jamrename.py --dry --docs /tmp/map-t9.tsv docs README.md AGENTS.md CLAUDE.md
/tmp/jamrename.py --docs /tmp/map-t9.tsv docs README.md AGENTS.md CLAUDE.md
git grep -nE '\bCOVE_[A-Z]' -- docs README.md AGENTS.md ':!docs/superpowers' ':!docs/usage/jam/renamed-from-*.md'
```

The last command lists any `COVE_*` name the map did not know. Rename each to `JAM_` + the same suffix by hand.

Then read `git diff --stat docs README.md AGENTS.md`. Open every file with > 20 replacements and read its diff for sentences the map made wrong. Typical cases:
- "`jam` and `jam mint` ship together": now one binary;
- "two binaries";
- "AT_HARBOR_*" table rows in `coves.md`: delete them.

- [ ] **Step 4: Hand edits the map can't make**

- `docs/OVERVIEW.md`:
  - Lines ~262 and ~301–304 describe the at-cove tombstone and upgrading from at-cove. Replace that paragraph with: "**Upgrading from a release named `at-jam`.** Follow [the rename runbook](usage/jam/renamed-from-at.md); an old `at-jam update` cannot fetch the new release." Delete the tombstone sentence at ~262 ("the archive also holds an `at-cove` tombstone…").
  - The install section now says the archive holds the one `jam` binary.
  - Leave the interim note (~12–17) and line ~176 (allowed `at-cove`).
- `docs/DEVELOPMENT.md` ~266–272: the release paragraph now says goreleaser builds `jam` (darwin/linux × amd64/arm64) into `jam_<ver>_<os>_<arch>.tar.gz` holding exactly `jam`, checked by `scripts/release/check-archive.sh`. Delete the tombstone sentences.
- `docs/usage/jam/serve.md`:
  - line ~70's example `identity-file: /var/lib/jam/launcher/id_ed25519`;
  - line ~246 "(defaults to `~/.config/jam/id_ed25519`; serve generates the pair on first use)" (the map got the path; check the sentence).
- `docs/usage/jam/standing-state.md` ~18: `jam-studio-<session id>` (mapped). Add one sentence after it: "Pre-rename volumes are moved at serve start ([renamed-from-at.md](renamed-from-at.md))."
- `docs/usage/jam/coves.md` env table: rows for `JAM_*` only. Delete every `AT_HARBOR_*` row and any "deprecated name" note.
- `docs/usage/jam/INDEX.md`:
  - frontmatter `summary`/`owns` say `jam`;
  - the paragraph "`at-jam` ships in one release archive with `at-mint`" becomes "`jam` ships as one binary (minting is `jam mint`): install it with the one-command installer and keep it current with `jam update`";
  - add the routing row:

```markdown
| [renamed-from-at.md](renamed-from-at.md) | You are upgrading from a release whose binaries were `at-jam`/`at-mint`, or you have a kit, script, env var or path that still says `at-`, `cove-` or `harbor.cove`, and need the new name and the upgrade steps. |
```

- `docs/usage/INDEX.md`: the row for `jam-mint.md` reads "The `jam mint` token minter — the `github` and `anthropic` subcommands…", and the summary says "Jam (`jam`, including `jam mint`) and docker-in-sandbox".
- `docs/INDEX.md` line 20: "You are running `jam` and need its flags, config, or I/O contract."
- `docs/usage/jam/renamed-from-harbor.md`:
  - `updated: 2026-10-26`;
  - the table's "Old name still accepted?" cells for the `at-harbor` binary, `~/.config/at-harbor/` and the `AT_HARBOR_*` rows become "No — removed in the jam rename ([renamed-from-at.md](renamed-from-at.md))";
  - in "Deliberately unchanged", replace the sentence about the three wire names with: "The Attach gRPC service, its launch-secret metadata key and the `harbor.cove` label kept their Harbor names until the jam rename renamed them ([renamed-from-at.md](renamed-from-at.md))";
  - every `at-jam` in the "New" column stays: it was the name then, and this file is history.

- [ ] **Step 5: Write the runbook**

`docs/usage/jam/renamed-from-at.md`:

```markdown
---
summary: The at-/cove → jam rename — every old name (binaries, images, tags, labels, containers and volumes, env vars, build args, in-image paths, directories, release assets) and its new name, the upgrade steps, and what Jam migrates by itself at serve start.
read_when: You are upgrading a Jam host or a workstation from a release whose binaries were `at-jam`/`at-mint`, or something you own (a kit, script, service unit, env var, path or ssh config) still says `at-jam`, `at-mint`, `cove-master`, `cove-*` or `harbor.cove`, and you need the new name or the upgrade steps.
owns: the at-/cove → jam old→new name table, the jam-rename upgrade runbook, and what the jam-rename serve-start migrations do
prereqs: serve.md for running serve
tier: leaf
updated: 2026-10-26
---

# Renamed from `at-*` and `cove-*` to `jam`

Everything this repo ships is now named for **Jam**: one binary, `jam`. There
are **no aliases** — an old name is simply not read. Jam migrates its own state
the first time the new `jam serve` starts; you install the binary, point your
service at it, and re-raise your studios once. Every studio image and every kit
rebuilds once (the studio assembly changed).

## Upgrade, step by step

1. **Stop serve** (your service manager, e.g. `systemctl stop jam` for a unit
   that runs `at-jam serve`).
2. **Install `jam`** with the one-command installer — an old `at-jam update`
   cannot fetch the new release (it asks for a `cove_*` archive that no longer
   exists, fails, and changes nothing):
   `gh api -H "Accept: application/vnd.github.raw" /repos/aethons-tools/cove/contents/install.sh | bash`
   It notices any `at-jam`, `at-mint` or `at-cove` left in the install
   directory; delete them after step 3.
3. **Point the service and your PATH at `jam`**: the unit's `ExecStart`
   becomes `jam serve --config …`; scripts call `jam …`; `AT_JAM_ADMIN_TOKEN`
   becomes `JAM_ADMIN_TOKEN` (table below).
4. **Start serve.** Its migrations run first (next section); if one fails,
   serve exits with the reason and changes nothing more — fix it and start
   again (each migration is safe to rerun).
5. **Re-raise every studio** that was running: its old container was removed
   (its agent could not reach the new Jam). Standing sessions come back by
   themselves, on their own state.
6. **Run `jam ssh-config --write <studio>`** (or `jam ssh-config --prune`) on
   each workstation: it replaces the old `# >>> at-jam` blocks with
   `# >>> jam` ones.
7. **Log in to the admin UI again** if it asks.

Hosts enrolled with a saved enroll snippet: fetch it again — it now exports
`JAM_IDENTITY_TOKEN`.

## What serve migrates by itself

- **Directories** (every `jam` command, once): `~/.config/at-jam` →
  `~/.config/jam`, `~/.local/state/at-jam` → `~/.local/state/jam`, and the
  launcher's `~/.config/at-cove/{id_ed25519,id_ed25519.pub,known_hosts.d}` →
  `~/.config/jam/`. Copied only when the target is missing; the old ones stay.
- **Studios**: each studio still on an `atcove-cove-<id>` container has it
  removed (with its docker cache and pinned host key) and is marked lost, so
  Jam finishes its teardown.
- **Standing sessions' state**: each `atcove-cove-<id>-agent-data` and
  `-workspace` volume is copied to `jam-studio-<id>-…` (files, owners and
  modes kept); the old volume is removed only after its copy succeeded. If a
  copy fails, serve stops with
  `migration: volume <old> could not be copied to <new>; left in place` —
  your data is still in the old volume; free space or fix docker and start again.
- **Kits**: a kit whose current `kit.yml` or Dockerfile still uses an old
  build-arg or ENV name, or names the old base image repository, gets a new
  version with the new names (history shows it as a *migration* version); a
  kit pinned to such a parent version is re-pinned to the migrated one. A kit
  that can't be rewritten keeps its version and shows a `kit.migrate:<name>`
  condition. Kits kept in git (config-as-code or a tracked source) show drift
  until you rename there too; a push that still uses an old name is refused
  with the new name in the message. Kit drafts are not rewritten.

Old images (`cove-kit:*`) are not deleted; remove them when you like:
`docker image ls --filter 'reference=cove-kit*' -q | xargs -r docker image rm`.

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
| `/usr/local/lib/cove/`, `cove-egress.service`, `/etc/ssh/sshd_config.d/cove.conf`, `cove-git-credential.sh`, `/dev/shm/cove-*`, `/agent-data/cove-master.log` | `/usr/local/lib/jam/`, `jam-egress.service`, `/etc/ssh/sshd_config.d/00-jam.conf`, `jam-git-credential.sh`, `/dev/shm/jam-*`, `/agent-data/jam-studio-agent.log` |
| `~/.config/at-jam`, `~/.local/state/at-jam`, `~/.config/at-cove` (launcher key) | `~/.config/jam`, `~/.local/state/jam`, `~/.config/jam` |
| `~/.ssh/config` `# >>> at-jam <studio> >>>` | `# >>> jam <studio> >>>` |
| release `cove_<ver>_<os>_<arch>.tar.gz` (at-cove, at-mint, at-jam) | `jam_<ver>_<os>_<arch>.tar.gz` (`jam`) |
| attach gRPC `harbor.attach.v1.Runtime`, `x-harbor-launch-secret` | `jam.attach.v1.Runtime`, `x-jam-launch-secret` |
| `.at-jam/<kit>/` (this repo's dev kit) | `.jam/<kit>/` |

### Environment variables

| Old | New |
|---|---|
| `AT_JAM_ADMIN_TOKEN` | `JAM_ADMIN_TOKEN` |
| `AT_LOG_MODE`, `AT_LOG_LEVEL` | `JAM_LOG_MODE`, `JAM_LOG_LEVEL` |
| `AT_MINT_GITHUB_APP_KEY`, `AT_MINT_AUTH0_CLIENT_SECRET`, `COVE_RUN_TIMEOUT` | `JAM_MINT_GITHUB_APP_KEY`, `JAM_MINT_AUTH0_CLIENT_SECRET`, `JAM_RUN_TIMEOUT` |
| `COVE_VERSION`, `COVE_SYSTEM`, `COVE_SYSTEM_BINDIR`, `COVE_REPO` (installer) | `JAM_VERSION`, `JAM_SYSTEM`, `JAM_SYSTEM_BINDIR`, `JAM_REPO` |
| in studios: `AT_JAM_RUNTIME_ADDR`, `AT_JAM_IDENTITY_TOKEN`, `AT_JAM_LAUNCH_SECRET`, `AT_JAM_BASE_URL`, `AT_JAM_CONNECTOR` | `JAM_RUNTIME_ADDR`, `JAM_IDENTITY_TOKEN`, `JAM_LAUNCH_SECRET`, `JAM_BASE_URL`, `JAM_CONNECTOR` |
| in studios: `AT_COVE_WORKDIR`, `AT_COVE_AGENT_PROMPT_FILE`, `AT_COVE_AGENT_CONTEXT_FILE`, `AT_COVE_RESIDENT`, `AT_COVE_SESSION_KIND` | `JAM_WORKDIR`, `JAM_AGENT_PROMPT_FILE`, `JAM_AGENT_CONTEXT_FILE`, `JAM_RESIDENT`, `JAM_SESSION_KIND` |
| in images: `COVE_DOCKER`, `COVE_SHADOW_DIRS`, `COVE_*_FILE`, `COVE_PLUGIN_*` | `JAM_DOCKER`, `JAM_SHADOW_DIRS`, `JAM_*_FILE`, `JAM_PLUGIN_*` |
| `AT_HARBOR_*` (every one) | removed — use the `JAM_*` name |

Operator-authored env (a model-spec's `provider-env`, a destination's env) may
not set `JAM_*`: that namespace is Jam's.

## Not renamed

The admin API and UI routes (`/admin/coves…`), JSON fields and ids that say
"cove", the Go module and repository (`aethons-tools/cove`), the Postgres
migration lock key, and the files a standing session keeps in `/agent-data`
(`.cove-conversation`). The earlier Harbor → Jam names are in
[renamed-from-harbor.md](renamed-from-harbor.md).
```

- [ ] **Step 6: Run the guard and the docs audit**

Run:
```bash
go test -run 'TestNoPreRenameNames|TestNoMentionsOfDeletedBinaries' .
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep ERROR | grep -v superpowers/ > /tmp/audit-now.txt; diff /tmp/audit-baseline.txt /tmp/audit-now.txt && echo "no new audit errors"
wc -l docs/usage/jam/renamed-from-at.md
```
Expected:
- the guard PASSes;
- `no new audit errors` (fix any dangling link to `at-mint.md`, or a renamed anchor, that the diff shows);
- the runbook is ≤ 200 lines.

- [ ] **Step 7: Gates, then commit**

```bash
git add -A
git commit -m "docs: jam rename — names throughout docs/usage, the renamed-from-at runbook, guard over the docs

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Final verification

**Files:** none new. This task only fixes what the checks find.

**Interfaces:** consumes everything; produces the verified branch.

- [ ] **Step 1: The whole gate set from clean**

```bash
export GOPROXY=https://proxy.golang.org,direct
git status --short   # empty
go build ./... && go vet ./... && go vet -tags integration ./...
just test
just lint; echo "lint exit $?"
bash install.test.sh
go test -run 'TestNoPreRenameNames|TestDeletedPackagesStayDeleted|TestNoMentionsOfDeletedBinaries' -v . | tail -5
test -z "$(gofmt -l $(git ls-files '*.go'))" && echo gofmt-clean
```
Expected: every command exits 0; `lint exit 0`; `gofmt-clean`.

- [ ] **Step 2: Nothing old is left outside the allow-list (belt and braces)**

```bash
git grep -nE 'at-jam|at-mint|at-cove|cove-master|covemaster|cove-base-image|cove-image|cove-kit|harbor\.cove|atcove|AT_(JAM|COVE|HARBOR|MINT|LOG)_|COVE_[A-Z]|harbor\.attach|x-harbor-launch' -- \
  ':!docs/superpowers' ':!docs/usage/jam/renamed-from-at.md' ':!docs/usage/jam/renamed-from-harbor.md' \
  ':!internal/legacynames' ':!renamed_test.go' ':!deleted_test.go' ':!testdata/install-legacy.sh' ':!install.test.sh' \
  ':!install.sh' ':!.gitignore' ':!docs/OVERVIEW.md' ':!images/jam-studio-base/image-files/home/agent/.init-agent-data'
git grep -n 'at-jam\|at-mint' -- install.sh .gitignore
git grep -n 'at-cove' -- docs/OVERVIEW.md images/jam-studio-base/image-files/home/agent/.init-agent-data | wc -l
```
Expected:
- the first command prints nothing;
- the second prints only `install.sh`'s `stale_binaries` loop and its notice, and `.gitignore`'s pre-rename block;
- the third prints a count (the slice-4 text) and nothing else is renamed there.

- [ ] **Step 3: The kept names are still kept**

```bash
git grep -n '"/admin/coves' -- '*.go' | head -3
grep -n '^module ' go.mod
git grep -n 'advisory' -- internal/jam/pgstore.go | head -3
```
Expected:
- the admin routes are unchanged;
- `module github.com/aethons-tools/cove`;
- the advisory-lock key line is unchanged from `main` (`git diff main -- internal/jam/pgstore.go` shows no change to it).

- [ ] **Step 4: The built binary end to end**

```bash
just build
b="dist/$(go env GOOS)-$(go env GOARCH)"
ls "$b"                         # jam only
"$b/jam" version; "$b/jam" mint --help 2>&1 | head -5; "$b/jam" --help | grep -c '^  mint'
XDG_CONFIG_HOME=$(mktemp -d) sh -c 'mkdir -p "$XDG_CONFIG_HOME/at-jam" && echo x > "$XDG_CONFIG_HOME/at-jam/settings.yml" && '"$b"'/jam version >/dev/null 2>/tmp/mig.log; ls "$XDG_CONFIG_HOME/jam"; cat /tmp/mig.log'
```
Expected:
- `jam` only;
- `jam <ver>`;
- the mint usage lists `github` and `anthropic`;
- `1`;
- `settings.yml` and the "copied a pre-rename directory" notice.

- [ ] **Step 5: The hardening layer is unchanged except for names**

```bash
git diff main --stat -- internal/assemble/hardening
git diff main -M -- internal/assemble/hardening | grep '^[-+]' | grep -vE '^(\+\+\+|---)' | grep -viE 'cove|jam|COVE_|JAM_|harbor' || echo "names only"
go test ./internal/assemble/ -run 'StagesOnlyStudioAgent|HarnessStageBetweenBaseAndHardening|HardeningLayerNames|Egress|SSHEnv|GitCredential' -v | grep -E '^(--- |ok|FAIL)'
```
Expected:
- the diff is renames plus name-only line changes, and prints `names only`. A line that changes anything else is a defect: revert it.
- the ordered-steps, egress and credential-helper tests PASS.

- [ ] **Step 6: Push readiness (do not push)**

Run: `git log --oneline main..HEAD` (nine commits, one per task, plus any fixes). Confirm Task 4 Step 9 (the operator re-cut) is checked off. If it isn't, report that the PR must not merge until the user has run it. Report the branch as ready for review; push only when the user asks.

---

## Self-review

**1. Spec coverage (§3, §5, §6, Decisions 5–7):**
- **The rename table, row by row:**
  - binaries → T7
  - `jam mint` → T7
  - `jam-studio-agent` → T1 (packages) + T7 (binary)
  - images/GHCR/dirs/blessed → T4
  - tags/BuildRoot → T5
  - labels → T5
  - containers/volumes → T5
  - env → T2 (all but build args and installer knobs), T3 (build args, `JAM_SSHENV`), T7 (installer knobs)
  - in-image paths → T3
  - directories → T6
  - ssh markers → T6
  - release archive → T7
  - attach wire → T2
  - Go identifiers → T1
  - repo dev kit and justfile → T7
- **Kept names** → Global Constraints, T10 Step 3.
- **Serve-start migrations:**
  - dirs → T6
  - instances, Location, ImageTag, volume copy and old-after-copy removal → T8
  - kit trees with `Meta.Origin "migration"` and `kit.migrate:<name>` → T8
  - teardown/purge by new names only → T5 (naming); the old names are read only by the migration (T8)
- **Blessed bases re-cut** → T4 Step 9.
- **Runbook steps** → T9 Step 5.
- **§5 error text** `migration: volume <old> could not be copied to <new>; left in place` → T8 (exact-string test); `kit.migrate` → T8.
- **§6:**
  - goldens for env, labels, tags, in-image paths → T2/T3/T5
  - grep test → `renamed_test.go` (T1 onward, docs in T9)
  - config/state copy, volume rename with old-kept-on-failure, Location rewrite, kit rewrite, idempotent second start → T6/T8
  - Postgres parity → T8 Step 9 (conformance) in CI

**2. Placeholder scan.** No TBD/TODO.
- Five steps tell the executor to match an existing helper's exact signature: `buildConfig`, `adminTokenEnv`, `tagFor`, `run`, `Tracker.Get`. Each names the existing test or line to mirror, because those signatures weren't read in full while planning.
- The orphan-kit test gives a concrete fallback input if `validateKit` accepts a missing parent.

**3. Type consistency:**
- `StudioRenamer`'s three methods match `*launcher.Launcher`'s: `RetireLegacyStudio(ctx, string) error`, `MigrateStateVolumes(ctx, []string) ([]string, error)`, `StudioLocation(string) string`.
- `MigratedFromLabel` is defined in T8 Step 4 and used in its tests.
- `legacynames.Container` is used consistently in T1/T6/T8, `naming.StudioContainer` in T1/T5/T8.
- `KitOriginMigration` is `"migration"` in T8 code, tests and conformance.
- `mintcmd.Run(args, version, env, httpc, stdout, stderr)` matches its T7 caller.
- `logging.RenamedFromAtDoc` is defined in T6 and used in `migrateDirs`.

**4. Review Focus.**
- All five lines have tests in their owning tasks: T8 volumes; T7 legacy update; T3+T8 kits; T8 old running studio; T4 gate.
- The sshd drop-in ordering risk, found while planning, is pinned by `TestHardeningLayerNames` (T3) rather than a Review Focus line, since it is a ruling the plan makes, not an input users bring.
