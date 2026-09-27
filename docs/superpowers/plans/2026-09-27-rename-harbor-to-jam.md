# Rename: Harbor → Jam, the resident dispatcher → Requisitioner, Cove → Studio in the UI

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Apply the settled names.
- **Harbor becomes Jam everywhere.** That covers the binary, the Go package, docs, config keys, environment variables and cookies. Every name that a user, a kit or a running cove depends on keeps working for one release as a **deprecated alias**.
- **The resident dispatcher becomes the Requisitioner** in everything users see.
- **A Cove is called a Studio** in the admin UI, the `at-jam` CLI and the docs. Internal identifiers stay "cove" for now.

**Naming source:** [`../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md`](../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md) § *Naming decisions*. Task 9 adds Jam there.

## Decisions

- **Jam: a full rename, with aliases where something outside this repo depends on the name.**

  | Old | New | Alias for one release |
  |---|---|---|
  | `at-harbor` binary | `at-jam` | `at-harbor` is shipped as the same binary; invoked as `at-harbor` it runs normally and warns that the name is deprecated |
  | `internal/harbor/…` (package `harbor`) | `internal/jam/…` (package `jam`) | none (internal) |
  | kit `config.yml` `harbor:` block | `jam:` | `harbor:` accepted with a deprecation warning; both present is an error |
  | serve config `runtime.launcher.harbor-host` | `jam-host` | old key accepted with a warning; both present is an error |
  | `AT_HARBOR_ADMIN_TOKEN` | `AT_JAM_ADMIN_TOKEN` | old one read if the new one is unset, with a warning |
  | `AT_HARBOR_IDENTITY_TOKEN`, `AT_HARBOR_LAUNCH_SECRET`, `AT_HARBOR_RUNTIME_ADDR` (injected into coves) | `AT_JAM_…` | **writers set both** (older images read the old names). Readers (`cove-master`, its MCP server) read new, then old |
  | `~/.config/at-harbor/` (client settings and cached tokens) | `~/.config/at-jam/` | if the new directory is missing and the old one exists, copy it across once, with a notice; the old one is left in place |
  | `docs/usage/harbor/` | `docs/usage/jam/` | none; every link is updated |
  | cookies `harbor_session`, `harbor_oauth_*` | `jam_session`, `jam_oauth_*` | none. Admin UI users log in again once; this is noted in the rename doc |
  | basic-auth realm `harbor` | `jam` | none |
  | `HARBOR_TEST_POSTGRES_DSN`, CI database `harbor` | `JAM_TEST_POSTGRES_DSN`, database `jam` | none (test infrastructure) |
  | `just harbor`, `just integration-harbor`, dev `harbor.dev.yml` | `just jam`, `just integration-jam`, `dev/jam.dev.yml` | none |
  | dev hostname and cert CN `harbor.local.aethons.tools` | `jam.local.aethons.tools` (`*.local.aethons.tools` already resolves to localhost) | none; rerun `just dev-cert` |

- **Deliberately unchanged:**
  - **The Postgres migration advisory-lock key** (`0x686172626f72`, "harbor"). Changing it would let an old and a new binary migrate the same database concurrently during a rollout. Keep the value and say why in a comment.
  - **The Go module path.**
  - **Historical design docs under `docs/superpowers/`.**
- **Requisitioner:**
  - The serve config's `runtime.dispatcher` becomes `runtime.requisitioner`, with the old key accepted and a warning (both present is an error).
  - `docs/usage/jam/dispatcher.md` becomes `requisitioner.md`.
  - CLI help, log messages and docs say Requisitioner for the resident role.
  - The standalone `at-dispatch` / `internal/dispatch` keeps its name, as do the resident role's Go identifiers that live in `internal/dispatch` (shared with `at-dispatch`). Rename the identifiers only where they are specific to the resident role in `cmd/at-jam`.
- **Studio (UI only):**
  - The admin UI's nav, headings, labels and messages say Studio.
  - The CLI verb `at-jam studio raise|list|status|teardown`, with `cove` kept as a deprecated alias that warns.
  - CLI help and output text say Studio.
  - User docs describe the entity as a Studio.
  - **Staying "cove":** Go identifiers, JSON field names, admin API routes (`/admin/coves…`), `cove-master`, `.at-cove/`, the `at-cove` tool, and `docs/usage/jam/coves.md`'s file name. Its title and prose say Studio, and the file name keeps inbound links stable.
- **One place lists the renames:** a new leaf, `docs/usage/jam/renamed-from-harbor.md`, gives every old name, its new name, whether it is still accepted, and that the aliases are removed in a later release. The alias code points to it.
- **Deprecation warnings are diagnostics:** they go through `internal/logging` on stderr (each one names the old and new name and links the rename doc). Each is logged once per process.

## Global Constraints

- **Mechanical first; behavior only where this plan says.** No other refactors.
- **Each commit builds and passes `just test`, `just lint` and `go vet -tags integration ./...`.** Use `git mv` for moves so history follows.
- **Secrets:** when writers set both the old and new identity-token and launch-secret variables, the values stay in the same in-memory env map. Nothing goes to argv, disk or logs. In the sourceable snippet, the old name is exported from the new variable (`export AT_HARBOR_IDENTITY_TOKEN="$AT_JAM_IDENTITY_TOKEN"`), so the secret is not written twice.
- **Hardening files are template payload:** under `internal/assemble/hardening/image-files/` change only comments and agent-facing docs. There is no behavior change there.
- **Leftover check (Task 10):** scan everything, **including `.github/workflows`, SQL migrations, `scripts/`, `justfile`, `dev/` and the image payload**, not just Go identifiers.
- **Docs in the same change.** TDD where there is behavior (the aliases). Stage files by path (`.switchboard/` is now gitignored, but still stage by path). End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Move the package

- [ ] `git mv internal/harbor internal/jam`. Package `harbor` → `jam`, including the sub-packages' imports. Qualified uses `harbor.X` → `jam.X` across the repo.
- [ ] Exported identifiers that contain `Harbor` (e.g. `kit.HarborConfig`, `HarborHost` fields) → `Jam…`. **Keep the YAML and JSON tags unchanged in this task**; the aliases come in Task 3.
- [ ] The advisory-lock constant keeps its value; add the comment.
- [ ] Commit: `rename: internal/harbor → internal/jam (package jam)`.

## Task 2: The binary

- [ ] `git mv cmd/at-harbor cmd/at-jam`. `scripts/build.sh` builds `at-jam`, and also emits `at-harbor` as a copy of it (for the deprecation release). Update the `justfile` recipes (`jam`, `integration-jam`, the dev serve recipe) and the release workflow if it names the binary.
- [ ] Test first: invoked as `at-harbor` (the `os.Args[0]` basename), it logs one deprecation warning and otherwise behaves the same. As `at-jam`, there is no warning.
- [ ] Update usage, help and error strings to `at-jam`.
- [ ] Client config dir: test first that with only `~/.config/at-harbor/` present, the first run copies it to `~/.config/at-jam/` and logs a notice. With both present, the new one is used and nothing is copied.
- [ ] `AT_JAM_ADMIN_TOKEN`, with `AT_HARBOR_ADMIN_TOKEN` as the fallback (test first: new wins; old alone works and warns).
- [ ] Commit: `rename: at-harbor → at-jam (at-harbor kept as a deprecated alias)`.

## Task 3: Config keys

- [ ] Kit `config.yml`: `jam:` is the key. `harbor:` is still accepted (warning), and both present is a validation error. Tests first.
- [ ] Serve config: `jam-host`, with `harbor-host` accepted, following the same pattern.
- [ ] Commit: `rename: jam config keys, harbor keys accepted as deprecated aliases`.

## Task 4: Cove-side environment

- [ ] Writers (`internal/jam/snippet`, the launcher's cove-master bootstrap in `internal/connect`, `internal/dispatchrun`) set `AT_JAM_IDENTITY_TOKEN`, `AT_JAM_LAUNCH_SECRET` and `AT_JAM_RUNTIME_ADDR`, **and** the old names. Tests first, including that no secret appears on argv.
- [ ] Readers (`cmd/cove-master` and its MCP server) read the new name, then the old. Tests first: new wins, and old alone works.
- [ ] The git credential helper and any image payload that references the variable follow the new name, falling back to the old.
- [ ] Commit: `rename: AT_JAM_* cove environment (AT_HARBOR_* still set and read)`.

## Task 5: Requisitioner

- [ ] Serve config `runtime.requisitioner`, with `runtime.dispatcher` accepted (warning; both present is an error). Tests first, including the `runtime.wake` fallback that currently reads the dispatcher block.
- [ ] Resident-role identifiers in `cmd/at-jam` (`dispatcherConfig` → `requisitionerConfig`, etc.), plus log messages and help text. `internal/dispatch` and `at-dispatch` are untouched.
- [ ] Commit: `rename: the resident dispatcher is the Requisitioner`.

## Task 6: Studio in the UI

- [ ] Admin UI templates, static text and handler-rendered messages say Studio. Update the adminui tests that assert on text.
- [ ] `at-jam studio …` with `cove` as a deprecated alias (test first). Command-table briefs, help and output text say Studio.
- [ ] Commit: `rename: Studio in the admin UI and at-jam CLI`.

## Task 7: Wire names and test infrastructure

- [ ] Cookies `jam_session` and `jam_oauth_*`, and the realm `jam`. Update the tests.
- [ ] `JAM_TEST_POSTGRES_DSN` in the integration tests, the CI workflow (database, user and password `jam`) and `dev/`. `dev/harbor.dev.yml` → `dev/jam.dev.yml`, and `dev/README.md` is updated. The dev hostname and the `just dev-cert` CN become `jam.local.aethons.tools`.
- [ ] Commit: `rename: jam cookies, realm, and test infrastructure`.

## Task 8: Docs

- [ ] `git mv docs/usage/harbor docs/usage/jam`, and `dispatcher.md` → `requisitioner.md`. Fix every link and anchor across `docs/`, `AGENTS.md`, `README*` and code comments that link docs.
- [ ] Prose: Harbor → Jam, resident dispatcher → Requisitioner, and the entity → Studio (file names and code identifiers stay as they are, per the Decisions).
- [ ] New leaf `docs/usage/jam/renamed-from-harbor.md` (frontmatter per the progressive-disclosure schema). It holds the full table from Decisions and the one-time admin UI re-login, and it is linked from `docs/usage/jam/INDEX.md` and `OVERVIEW.md`.
- [ ] Image payload docs (`internal/assemble/hardening/image-files/home/agent/.init-agent-data/…`) and payload comments (e.g. `squid.conf`) say Jam. They are payload, not repo config.
- [ ] Docs-audit checker (`--index OVERVIEW.md`): no new errors against main.
- [ ] Commit: `docs: harbor → jam, requisitioner, studio`.

## Task 9: Record the names

- [ ] In the orchestration spec's *Naming decisions*, add **Jam** (was Harbor; a full rename with deprecation aliases) and record that Studio now extends to the UI while internal names stay Cove. Leave the rest of the historical docs alone.
- [ ] Commit: `spec: record Jam and Studio's UI scope`.

## Task 10: Leftover check

- [ ] Run `rg -i harbor` over the whole tree, excluding only `docs/superpowers/` and `.git/`. **Every remaining hit must be on an allow-list:**
  - the alias code and its tests
  - the rename doc
  - the advisory-lock comment
  - `build.sh`'s alias copy

  Paste the final hit list, grouped by allow-list reason, into the report.
- [ ] Do the same for `dispatcher` (the remaining hits must be `at-dispatch` / `internal/dispatch`, or the alias) and for user-facing "cove" in the admin UI templates and CLI help.
- [ ] No commit unless the check finds something to fix.

## Out of scope

- Removing the aliases (a later release; the rename doc says so).
- Renaming internal "cove" identifiers, the `at-cove` tool, `cove-master` or the module.
