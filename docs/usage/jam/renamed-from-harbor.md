---
summary: The Harbor → Jam rename — every old name (binary, config keys, environment variables, directories, cookies, docs paths), its new name, and whether the old one is still accepted.
read_when: You have a kit, serve config, script, environment or bookmark that still says "harbor", or you saw a "deprecated name" warning, and need to know what to change it to and how long the old name keeps working.
owns: the Harbor → Jam old→new name table, which old names are deprecated aliases, and the alias removal policy
prereqs: none
tier: leaf
updated: 2026-10-26
---

# Renamed from Harbor

The central service used to be called **Harbor**; it is now **Jam**. Most names a
user, a kit or a running cove depends on keep working **for one release** as a
**deprecated alias**: using one logs a `deprecated name` warning on stderr (once per
process) that names the old and new name and points here. The aliases are
**removed in a later release** — switch to the new names now.

| Old | New | Old name still accepted? |
|---|---|---|
| `at-harbor` binary | `at-jam` | No — removed in the jam rename ([renamed-from-at.md](renamed-from-at.md)). |
| `~/.config/at-harbor/` (settings, cached login tokens) | `~/.config/at-jam/` | No — removed in the jam rename ([renamed-from-at.md](renamed-from-at.md)). |
| `AT_HARBOR_ADMIN_TOKEN` | `AT_JAM_ADMIN_TOKEN` | No — removed in the jam rename ([renamed-from-at.md](renamed-from-at.md)). |
| `AT_HARBOR_IDENTITY_TOKEN`, `AT_HARBOR_LAUNCH_SECRET`, `AT_HARBOR_RUNTIME_ADDR` (set inside a cove) | `AT_JAM_IDENTITY_TOKEN`, `AT_JAM_LAUNCH_SECRET`, `AT_JAM_RUNTIME_ADDR` | No — removed in the jam rename ([renamed-from-at.md](renamed-from-at.md)). |
| kit `config.yml` `harbor:` block | `jam:` | Removed with at-cove (jam-only slice 2): there is no kit `config.yml` any more. |
| serve config `runtime.launcher.harbor-host` | `runtime.launcher.jam-host` | Yes, with a warning. Setting both is an error. |
| serve config `runtime.dispatcher` (the resident dispatcher) | `runtime.requisitioner` (the Requisitioner) | Yes, with a warning. Setting both is an error. |
| `at-jam cove raise\|list\|status\|teardown` | `at-jam studio raise\|list\|status\|teardown` | Yes, with a warning. The entity is called a **Studio** in the CLI, the admin UI and the docs; ids, admin API routes (`/admin/coves…`), JSON fields and `cove-master` keep "cove". |
| admin UI cookies `harbor_session`, `harbor_oauth_*` | `jam_session`, `jam_oauth_*` | No. **Admin UI users log in again once** after upgrading; the old cookies are simply ignored. |
| broker basic-auth realm `harbor` | `jam` | No. Git picks its credential helper by URL, not realm, so coves are unaffected. |
| `HARBOR_TEST_POSTGRES_DSN`; CI and dev database/user/password `harbor` | `JAM_TEST_POSTGRES_DSN`; `jam` | No (test infrastructure). The dev compose project is now `jam-dev`, so an old `harbor-dev` volume is not reused — `just dev-up` starts fresh. |
| `just harbor`, `just integration-harbor`, `dev/harbor.dev.yml` | `just jam`, `just integration-jam`, `dev/jam.dev.yml` | No. |
| dev hostname / cert CN `harbor.local.aethons.tools` | `jam.local.aethons.tools` (`*.local.aethons.tools` already resolves to localhost) | No — rerun `just dev-cert`. |
| docs `docs/usage/harbor/` (and its `dispatcher.md`) | `docs/usage/jam/` (`requisitioner.md`) | No; every in-repo link is updated. |
| Go package `internal/harbor` | `internal/jam` | No (internal). |

**Deliberately unchanged:** the Postgres migration advisory-lock key (it spells
"harbor"; changing it would let an old and a new binary migrate one database
concurrently during a rollout). The Attach gRPC service, its launch-secret
metadata key and the `harbor.cove` label kept their Harbor names until the jam
rename renamed them ([renamed-from-at.md](renamed-from-at.md)). The Go module path, the internal "cove" identifiers, the Requisitioner's
`internal/dispatcher` package and the historical design docs under
`docs/superpowers/` are unchanged too.
