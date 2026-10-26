# Jam only: remove at-cove, rename to `jam`

**Status:** design approved in brainstorming (2026-10-26); spec, pre-plan.
**Owner:** Jam service (admin-spider).
**Supersedes:** the standalone sandbox CLI of
[`2026-06-26-cove-sandboxes-design.md`](2026-06-26-cove-sandboxes-design.md) and the
Linear-driven `at-cove dispatch` / `at-task` worker flow. Builds on the Harbor
rename ([`renamed-from-harbor.md`](../../usage/jam/renamed-from-harbor.md)).

## Summary

Jam is the only way this repo runs agents. The standalone `at-cove` CLI, the
Discord conductor `at-switchboard` and the in-sandbox worker `at-task` are
deleted with every package only they reach. Jam gains the one capability only
at-cove had: dispatch from **GitHub Issues**. Then everything that remains is
renamed in **one cut** — binaries without the `at-` prefix, images, labels, tag
prefixes, environment variables, in-image paths, directories and release
assets — with serve-start migrations and a short upgrade runbook. Every studio
is re-raised once after the upgrade; every kit rebuilds once.

## Decisions (brainstorming record)

1. **Delete outright.** No one else runs `at-cove`; the last release remains
   available if needed. No deprecation window.
2. **Port GitHub Issues dispatch to Jam** before deleting anything.
3. **`at-switchboard` goes.** Jam's Discord relay keeps only the REST client.
4. **`at-task` goes.** Only at-cove's `dispatchrun` ever ran it; Jam ticket
   sessions open their own PRs.
5. **Rename scope (b):** identifiers, paths, release assets and image names. The
   Go module and repo (`aethons-tools/cove`) keep their names (a possible later
   mechanical change).
6. **Binary names:** `at-jam` → `jam`; `at-mint` → `jam mint …` (subcommand);
   `cove-master` → `jam-studio-agent`. Dev tools (`adopt-base`, `gen-blessed`,
   `gen-icons`) are unchanged.
7. **Persisted names: one cut, no aliases.** Env vars, labels, tag prefixes,
   in-image paths and directories are renamed together; migrations at serve
   start handle Jam's own state; the operator re-raises studios.
8. **GitHub ticket keys are `owner/repo#<n>`**, and the comment relay covers
   GitHub as it does Linear.

## 1. GitHub Issues in Jam (slice 1)

- **Config.** `runtime.requisitioner` takes exactly one of `linear:` or
  `github:` (both or neither is a validation error). `github:` holds `repo`
  (`owner/name`), `poll-interval`, `class-label-prefix`,
  `dispatch-label-prefix` (new for GitHub; an issue is dispatchable only with a
  label under it) and `states` (status labels; done = the issue is closed).
- **Credentials.** A new connection kind `github`: a token with issues
  read/write on the repo, chosen with `connection:` like Linear's. It resolves
  through the existing credentials path; its value is never logged.
- **One tracker interface.** Jam's four Linear touch points — the
  requisitioner (`ListReady`, `Comments`, `Transition`), ticket reports and
  wake-on close (`IssueByIdentifier`, `Transition`, `PostComment`), the
  comment relay (`CommentFeed`) and its self-filter (`Viewer`) — become one
  Jam-side interface. Linear and GitHub both implement it.
- **GitHub tracker gains** the dispatch-label check, `IssueByIdentifier`,
  `CommentFeed` (`GET /repos/{r}/issues/comments?since=`) and `Viewer`
  (`GET /user`). Blockers stay "Depends on #N" in the body.
- **Ticket keys** are `owner/repo#<n>` everywhere Jam shows or stores one:
  ticket channels (`ticket:<key>`), intercom targets, escalation, reports.
  Keys are validated (`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]+$`) before use.
- The package moves from `internal/dispatch/githubissues` to sit beside
  Jam's Linear tracker; `kit.GitHubTracker` becomes a Jam config type.

## 2. Delete (slice 2)

- **Binaries:** `cmd/at-cove`, `cmd/at-switchboard`, `cmd/at-task`.
- **Packages only they reach:** `dispatchrun`, `state`, `keys`, `sshconfig`,
  `awake`, `dispatch/exec`, `dispatch/worker`, `dispatch/github`,
  `dispatch/gitlab`, `attask`, `atswitchboard`, and the scheduler's `Engine`
  (`engine.go`; Jam keeps only the scheduler's types).
- **Dead code in shared packages**, confirmed by a build after removal:
  `connect` (all but the cove-master launch and its helpers), `install` (all
  but the assembly identity), `kit.Discover` and the `.at-cove/` walk plus the
  dispatch/class helpers, `backend.Backend`/`Factory`/`Create`/`Install` and
  their colima implementations, `assemble.Assemble`/`EgressFor`, `naming`'s
  container/image helpers that only at-cove used.
- **switchboard** shrinks to its Discord REST client, moved to
  `internal/discord`.
- **Payload:** at-task and at-switchboard leave the hardening layer
  (`writeSwitchboard`, the Dockerfile stanzas, `stage-attask.sh`,
  `build.sh`). The assembly identity changes, so every studio image tag
  changes and every kit rebuilds on its next raise.
- **Kits and tests:** `kits/reference-worker` and its e2e, the at-cove
  integration tests, `dockere2e` testdata `.at-cove/` dirs.
- **Docs deleted** (~2.8k lines): `at-cove-config.md`, `at-cove-secrets.md`,
  `discord-teammate.md`, `workspace-visibility.md`, `at-task*.md`,
  `orchestration/` (its Linear workflow design is kept only if still true of
  Jam's requisitioner, moved under `docs/usage/jam/`).
- The hardening layer, the sealed egress base and the egress ceiling are
  **not weakened**: only the binaries staged into the image change.

## 3. Rename and migrate in one cut (slice 3)

| Old | New |
|---|---|
| `at-jam` | `jam` |
| `at-mint github\|anthropic` | `jam mint github\|anthropic` |
| `cove-master` (in studios) | `jam-studio-agent` |
| images `cove-base-image`, `cove-image` (`ghcr.io/aethons-tools/…`) | `jam-studio-base`, `jam-studio` |
| tags `cove-kit:`, `cove-kit-base:`, `cove-kit-draft:`; `$TMP/cove-kit-builds` | `jam-kit:`, `jam-kit-base:`, `jam-kit-draft:`; `$TMP/jam-kit-builds` |
| labels `harbor.cove`, `harbor.cove.state`, `harbor.cove.jam` | `jam.studio`, `jam.studio.state`, `jam.studio.jam` |
| containers `atcove-cove-<id>`, volumes `<container>-{workspace,agent-data,docker,shadow-*}` | `jam-studio-<id>`, `<container>-…` |
| env `AT_JAM_*`, `AT_COVE_*`, `AT_HARBOR_*`, `COVE_*`, `AT_MINT_*`, `AT_LOG_*` | `JAM_*` (one table in the runbook) |
| build args `COVE_BASE_IMAGE`, `AT_JAM_STUDIO_BASE_IMAGE`, `AT_JAM_STUDIO_TARGET_ARCH`; kit `ENV COVE_SSHENV` | `JAM_STUDIO_BASE_IMAGE`, `JAM_STUDIO_TARGET_ARCH`, `JAM_SSHENV` |
| in-image `/usr/local/lib/cove/`, `cove-egress.service`, `sshd_config.d/cove.conf`, `cove-git-credential.sh`, `/dev/shm/cove-*`, `/agent-data/cove-master.log` | `/usr/local/lib/jam/`, `jam-egress.service`, `jam.conf`, `jam-git-credential.sh`, `/dev/shm/jam-*`, `/agent-data/jam-studio-agent.log` |
| `~/.config/at-jam`, `~/.local/state/at-jam`, launcher defaults under `~/.config/at-cove` | `~/.config/jam`, `~/.local/state/jam` |
| `~/.ssh/config` markers `# >>> at-jam` | `# >>> jam` |
| release archive `cove_<ver>_<os>_<arch>` with at-cove/at-mint/at-jam | `jam_<ver>_<os>_<arch>` with `jam` |
| attach gRPC `harbor.attach.v1.Runtime`, metadata `x-harbor-launch-secret` | `jam.attach.v1.Runtime`, `x-jam-launch-secret` |
| Go identifiers `AtCoveIdentity`, `AtCoveBuildIdentity`, `LaunchCoveMaster`, `CoveMaster*` | `AssemblyIdentity`, `AssemblyBuildIdentity`, `LaunchStudioAgent`, `StudioAgent*` |

**Kept on purpose:** the admin API and UI routes `/admin/coves…` and
`/app/admin/coves…` (wire API; "cove" remains the studio's sandbox), the Go
module path, and the Postgres migration advisory-lock key (changing it would
let an old and a new binary migrate one database at once).

**Serve-start migrations** (idempotent, logged, one transaction where the store
allows):
- Copy `~/.config/at-jam` and `~/.local/state/at-jam` to the new directories
  if those are missing (the `migrateConfigDir` pattern).
- For each instance not `gone`: record the new container name; for each
  standing session, copy every old volume to its new name and remove the old
  one only after the copy succeeds; rewrite `Location`; mark `ImageTag` stale
  (it rebuilds on re-raise).
- Rewrite stored kit trees whose `kit.yml`/`Dockerfile` use the old build-arg
  or ENV names (a new version with `Meta.Source = migration`, so history shows
  it); a tree that can't be rewritten keeps its version and raises
  `kit.migrate:<name>`.
- Teardown and purge find containers and volumes by the new names only.

**Blessed bases** are re-cut under `ghcr.io/aethons-tools/jam-studio-base`;
kits pinned to the old repo by digest are rewritten by the migration above.

**Upgrade runbook** (`docs/usage/jam/renamed-from-at.md`, alongside the Harbor
table): stop serve; install `jam` (one command; the old `at-jam update` cannot
fetch the new archive); point the service and PATH at `jam`; start serve
(migrations run); re-raise every studio; run `jam ssh-config`; re-login to the
admin UI if prompted.

## 4. Docs and agent context (slice 4)

- Rewrite `docs/OVERVIEW.md`, `AGENTS.md`, `docs/DEVELOPMENT.md`,
  `docs/INDEX.md` and `docs/usage/INDEX.md` around Jam; one fact one place;
  docs audit clean.
- Rewrite the in-image agent docs and skills
  (`images/jam-studio-base/image-files/home/agent/.init-agent-data`): a kit
  change goes through a kit draft and publish, not `.at-cove/config.yml` plus
  `at-cove recreate`; the board skills drop the at-task flow.
- Jam session context text (`sessionctx` boilerplate) names `jam`.
- The repo's own dev kit `.at-jam/cove-ic` and the justfile follow the rename.

## 5. Errors

Fixed texts, never echoing tokens: `requisitioner: set exactly one of linear or
github`, `github ticket key must be owner/repo#<n>`, `migration: volume <old>
could not be copied to <new>; left in place`, and the existing `kit.migrate`
condition for kits.

## 6. Testing

- GitHub tracker against an `httptest` GitHub: ready listing with dispatch and
  class labels, comments, transitions by label and close, `IssueByIdentifier`,
  `CommentFeed` since-cursor, `Viewer`; requisitioner and relay end to end on
  the shared interface with both trackers.
- Deletion: `go build ./...`, `go vet -tags integration ./...` and the suite
  pass; a test asserts no remaining import of the deleted packages.
- Rename: goldens for env passed to studios, labels, tags and in-image paths;
  a grep test that no old name survives outside `docs/superpowers/`, the
  runbook and the migration code.
- Migrations: config/state copy, volume rename (runner.Fake argv; old volume
  kept when a copy fails), `Location` rewrite, kit tree rewrite; idempotent on
  a second start; Postgres parity in CI.

## Slices

1. **GitHub Issues in Jam** (additive).
2. **Delete** at-cove, at-switchboard, at-task and what only they reach.
3. **Rename and migrate** in one cut, with the runbook and release changes.
4. **Docs and agent context.**

## Out of scope

- Renaming the Go module or repository.
- Renaming the `/admin/coves` routes.
- A deprecation window or aliases for old names.
