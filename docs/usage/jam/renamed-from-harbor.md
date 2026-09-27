---
summary: The Harbor → Jam rename — every old name (binary, config keys, environment variables, directories, cookies, docs paths), its new name, and whether the old one is still accepted.
read_when: You have a kit, serve config, script, environment or bookmark that still says "harbor", or you saw a "deprecated name" warning, and need to know what to change it to and how long the old name keeps working.
owns: the Harbor → Jam old→new name table, which old names are deprecated aliases, and the alias removal policy
prereqs: none
tier: leaf
updated: 2026-09-27
---

# Renamed from Harbor

The central service used to be called **Harbor**; it is now **Jam**. Most names a
user, a kit or a running cove depends on keep working **for one release** as a
**deprecated alias**: using one logs a `deprecated name` warning on stderr (once per
process) that names the old and new name and points here. The aliases are
**removed in a later release** — switch to the new names now.

| Old | New | Old name still accepted? |
|---|---|---|
| `at-harbor` binary | `at-jam` | Yes. `at-harbor` ships as a copy of `at-jam`; run under that name it works and warns. `at-cove` looks for `at-jam` first, then `at-harbor`. |
| `~/.config/at-harbor/` (settings, cached login tokens) | `~/.config/at-jam/` | Copied across once: if `at-jam/` is missing and `at-harbor/` exists, the first `at-jam` run copies it (with a notice) and leaves the old one in place. |
| `AT_HARBOR_ADMIN_TOKEN` | `AT_JAM_ADMIN_TOKEN` | Yes, read when the new one is unset, with a warning. |
| `AT_HARBOR_IDENTITY_TOKEN`, `AT_HARBOR_LAUNCH_SECRET`, `AT_HARBOR_RUNTIME_ADDR` (set inside a cove) | `AT_JAM_IDENTITY_TOKEN`, `AT_JAM_LAUNCH_SECRET`, `AT_JAM_RUNTIME_ADDR` | Yes. Writers (the enroll snippet, at-cove, the launcher) set both names, because older images read only the old ones; the sourceable snippet exports each old name from its new variable rather than repeating the value. `cove-master`, its MCP server and the git credential helper read the new name, then the old (no warning — a cove can't act on one). |
| kit `config.yml` `harbor:` block | `jam:` | Yes, with a warning. Setting both is a validation error. |
| serve config `runtime.launcher.harbor-host` | `runtime.launcher.jam-host` | Yes, with a warning. Setting both is an error. |
| serve config `runtime.dispatcher` (the resident dispatcher) | `runtime.requisitioner` (the Requisitioner) | Yes, with a warning. Setting both is an error. The standalone `at-dispatch` keeps its name. |
| `at-jam cove raise\|list\|status\|teardown` | `at-jam studio raise\|list\|status\|teardown` | Yes, with a warning. The entity is called a **Studio** in the CLI, the admin UI and the docs; ids, admin API routes (`/admin/coves…`, `/ui/coves`), JSON fields, `cove-master` and `.at-cove/` keep "cove". |
| admin UI cookies `harbor_session`, `harbor_oauth_*` | `jam_session`, `jam_oauth_*` | No. **Admin UI users log in again once** after upgrading; the old cookies are simply ignored. |
| broker basic-auth realm `harbor` | `jam` | No. Git picks its credential helper by URL, not realm, so coves are unaffected. |
| `HARBOR_TEST_POSTGRES_DSN`; CI and dev database/user/password `harbor` | `JAM_TEST_POSTGRES_DSN`; `jam` | No (test infrastructure). The dev compose project is now `jam-dev`, so an old `harbor-dev` volume is not reused — `just dev-up` starts fresh. |
| `just harbor`, `just integration-harbor`, `dev/harbor.dev.yml` | `just jam`, `just integration-jam`, `dev/jam.dev.yml` | No. |
| dev hostname / cert CN `harbor.local.aethons.tools` | `jam.local.aethons.tools` (`*.local.aethons.tools` already resolves to localhost) | No — rerun `just dev-cert`. |
| docs `docs/usage/harbor/` (and its `dispatcher.md`) | `docs/usage/jam/` (`requisitioner.md`) | No; every in-repo link is updated. |
| Go package `internal/harbor` | `internal/jam` | No (internal). |

**Deliberately unchanged:** the Postgres migration advisory-lock key (it spells
"harbor"; changing it would let an old and a new binary migrate one database
concurrently during a rollout) and the Attach gRPC service name
`harbor.attach.v1.Runtime` (the wire name cove-master in already-built images
dials). The Go module path, the internal "cove" identifiers, the Requisitioner's
`internal/dispatcher` package and the historical design docs under
`docs/superpowers/` are unchanged too.
