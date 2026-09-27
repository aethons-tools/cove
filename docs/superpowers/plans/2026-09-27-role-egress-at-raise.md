# Role egress at raise

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A harbor-managed cove's raw egress is set by its **Role**, not only by the kit baked into the image. An operator gives a role an egress list. When harbor raises a cove of that role, it pushes the list into the box before the agent starts, and the box refuses any domain outside the kit's own list (the **ceiling**). The agent still can't widen its own egress.

**Architecture:**
- **Four lists in the box, not three.** Today squid allows sealed base ∪ kit (`allowed_domains.kit.txt`) ∪ per-session delta (COV-39). The kit file mixes two things. One is the operator's policy (`image.allowed-domains`). The other is infrastructure the kit needs to work at all: model-provider domains, a self-hosted GitLab host, and the harbor host. Split them:
  - `allowed_domains.txt`: the sealed base, unchanged and always on.
  - `allowed_domains.infra.txt` (new): `ProviderDomains ∪ SourceControlDomains ∪ harbor.host`. Always on; a role can't remove it.
  - `allowed_domains.kit.txt`: now just `image.allowed-domains`. This is the **active policy list**. By default it holds the kit's list, so the dev sandbox and `at-cove work` behave exactly as before.
  - `allowed_domains.session.txt`: unchanged.
  - `egress_ceiling.txt` (new, **not** referenced by squid): an immutable copy of `image.allowed-domains`, the bound a role must fit inside.
- **A new sealed helper, `apply-role-egress.sh`** (root-only, like `apply-session-domains.sh`). It reads the role's domains on stdin and checks each one is within the ceiling; if any isn't, it exits non-zero and changes nothing. Otherwise it overwrites the kit file with the role's list, empties the session file, and runs `squid -k reconfigure`.
- **Harbor delivers at raise.** `Supervisor.Raise` reads the role's egress policy into the `RaiseSpec`. The launcher applies it with `docker exec -u root` after sshd answers and **before** cove-master (and so the agent) starts. If that fails, the raise fails and the container is removed, as for any failed raise.
- **Role policy lives on `Role.Scope.Egress`**, managed through its own routes and verbs, like standing declarations. `role add`, the role PUT, and the admin UI role form keep it.

**Tech Stack:** Go 1.26, bash, squid, `just`. Spec: [`../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md`](../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md) § *Security: split mechanism from policy* (the carry-forward in *Open questions*). Builds on COV-39 per-session egress ([`../specs/2026-07-19-per-class-egress-design.md`](../specs/2026-07-19-per-class-egress-design.md)).

## Decisions in this plan

- **A role replaces the kit's policy list; it doesn't narrow the whole perimeter.** The sealed base (Anthropic, claude.ai, GitHub, GitLab, PyPI) and the infra list stay on for every cove. The agent can't run without Anthropic, and harbor/provider reachability is mechanism, not policy. Narrowing the sealed base per role is out of scope.
- **No policy means the kit default.** `Scope.Egress == nil` means the role hasn't opted in, and the cove gets the kit's list as today. So existing roles are unchanged. A **set but empty** policy means "nothing beyond base + infra".
- **The box is the authority on the ceiling.** Harbor only checks syntax and normalizes. The launcher raises one image from the install manifest, not a per-role kit, so the only ceiling that is really enforced is the one baked into the running image. A role that asks for more fails its raise with an error naming the domain. (For a standing session that means backoff, logged each time.) A harbor-side pre-check against the manifest is deferred.
- **Applied at raise only.** Changing a role's egress affects the next cove raised for it. A running cove keeps what it booted with. Tearing a standing session down makes the reconciler raise it again with the new policy. Live re-application to running (or paused) coves is deferred.
- **Fail closed.** If a role has a policy and the backend can't apply it (no `RoleEgress` op), the raise fails. It never falls back to the wider kit default.
- **Grant overrides don't touch egress.** An `Override` can't narrow or widen egress in this slice.

## Global Constraints

- **The dev sandbox and `at-cove work`/`dispatch` behave exactly as before.** Base ∪ infra ∪ kit equals the old kit file, and the COV-39 session delta is untouched. An assemble test proves the union is unchanged.
- **Sealed from inside:** every list and the ceiling are root-owned 0644. The helper refuses to run as non-root, and only the host's `docker exec -u root` calls it. The agent user can't write the files or reconfigure squid. nftables still forces all egress through squid.
- **Domains never go on argv.** Stdin only, as with `ApplySessionEgress`. Don't log the domain list at info level. A domain isn't a secret, but keep to the existing pattern: log the count, and the rejected domain on failure.
- **Domain syntax** (harbor and the helper enforce the same rule): lowercase `^\.?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$` after lowercasing. That is at least two labels, no scheme, port, path, `*` or whitespace, and an optional leading dot for "and subdomains".
- **Ceiling match:** ceiling entry `c` covers requested `r` when `r == c`, or `c` starts with `.` and (`r == c[1:]` or `r` ends with `c`). An exact host never covers a wildcard (`x.com` doesn't cover `.x.com`).
- **Normalization (harbor side):** lowercase, dedupe, sort, and drop an entry covered by another wildcard in the same list. Squid warns on overlapping entries inside one ACL file.
- **Hardening files are the security boundary.** Edit them under `internal/assemble/hardening/image-files/` only as described here. They are **template payload**, not this repo's config.
- **Docs in the same change.** TDD. Stage files by path (untracked `.switchboard/`). Each commit builds. End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Split the kit list; bake the ceiling

**Files:** `internal/kit/config.go`, `internal/assemble/assemble.go`, `internal/assemble/hardening/image-files/etc/squid/squid.conf`, `internal/assemble/assemble_test.go`, `internal/assemble/embed_test.go` (if it lists files), `internal/kit/config_test.go`.

- [ ] Test first: `kit.InfraDomains(c)` returns `ProviderDomains ∪ SourceControlDomains ∪ harbor.host`, deduped and sorted. `RootDomains(c)` still equals `image.allowed-domains ∪ InfraDomains(c)`; keep it, because other callers and docs use it.
- [ ] Test first: assemble writes
  - `allowed_domains.kit.txt` = `image.allowed-domains` only
  - `allowed_domains.infra.txt` = `InfraDomains`
  - `egress_ceiling.txt` = `image.allowed-domains`

  Each file always gets a header, even when empty. Add one test that the union of the kit and infra files equals the old `RootDomains` for a config with a provider, a GitLab host and a harbor host.
- [ ] `squid.conf`: add `acl allowed_infra_domains dstdomain "/etc/squid/allowed_domains.infra.txt"` and `http_access allow allowed_infra_domains`, and update the comments. Update the kit ACL comment: it is the *active policy list* (kit default, replaced by a role's list at raise). **Don't** reference `egress_ceiling.txt` from squid.conf. Add a test that asserts that.
- [ ] Commit: `assemble: split kit egress into policy, infra and ceiling lists`.

## Task 2: The `apply-role-egress.sh` helper

**Files:** `internal/assemble/hardening/image-files/usr/local/lib/cove/apply-role-egress.sh` (new, 0755 through the existing `.sh` rule), `internal/assemble/image_scripts_test.go`, `internal/assemble/embed_test.go`.

Model it on `apply-session-domains.sh`: `set -euo pipefail`, a root check, a mktemp-and-mv atomic write, and a loud failure on reconfigure. Override paths with `COVE_KIT_DOMAINS_FILE`, `COVE_SESSION_DOMAINS_FILE` and `COVE_EGRESS_CEILING_FILE` (defaults under `/etc/squid`).

Behavior:
1. Read stdin lines, dropping blank lines. Lowercase each; reject any line failing the syntax rule (print it, exit 2).
2. Load the ceiling, skipping `#` comments and blank lines. For each requested domain, if no ceiling entry covers it, print `apply-role-egress: <domain> is outside the kit's egress ceiling` and exit 3. **Validate everything before writing anything.**
3. Write the kit file atomically. The header says it is a role policy applied at raise, then one domain per line. Truncate the session file to its header.
4. Run `squid -k reconfigure`; on failure, exit 1.

- [ ] Tests first (hermetic; put fake `id` (prints `0`) and fake `squid` (records its args; can be made to fail) on `PATH`, and point the file env vars at `t.TempDir()`):
  - within ceiling: kit file replaced and session file cleared, squid called with `-k reconfigure`
  - `.x.com` in the ceiling covers `x.com`, `a.x.com` and `.a.x.com`; an exact `x.com` doesn't cover `.x.com`
  - one outside-ceiling domain: exit 3, and the kit file, session file and squid calls are **unchanged**
  - a bad-syntax line (`https://x.com`, `*.x.com`, `x`): exit 2, nothing changed
  - empty stdin: kit file is header only (nothing beyond base + infra)
  - non-root (fake `id` prints `1000`): exits non-zero, nothing changed
  - squid reconfigure fails: non-zero exit
- [ ] Commit: `hardening: apply-role-egress helper (role list within the kit ceiling)`.

## Task 3: The backend op

**Files:** `internal/backend/backend.go`, `internal/backend/colima/egress.go`, `internal/backend/colima/egress_test.go`.

```go
// RoleEgress replaces a running container's active egress policy list with a
// role's domains, which must fit the kit's baked ceiling (enforced in-box by the
// sealed apply-role-egress.sh). Privileged: host docker exec as root; domains on
// stdin only.
type RoleEgress interface {
	ApplyRoleEgress(container string, domains []string) error
}
```

- [ ] Test first (runner.Fake): `docker exec -i -u root <container> /usr/local/lib/cove/apply-role-egress.sh`, with the domains on stdin one per line and **never** on argv. An empty list still runs the helper, with empty stdin.
- [ ] `var _ backend.RoleEgress = (*Colima)(nil)`.
- [ ] Commit: `backend: RoleEgress op (colima)`.

## Task 4: Egress policy on the Role

**Files:** `internal/harbor/identity.go`, `internal/harbor/egress.go` (new: validate and normalize), `internal/harbor/admin.go`, `internal/harbor/adminclient/adminclient.go`, `internal/harbor/adminui/writes.go`, `cmd/at-harbor/main.go`, tests.

```go
// EgressPolicy is a role's raw-egress allow-list, applied to its coves at raise
// within the kit's ceiling. Domains may be empty (nothing beyond the sealed base
// and the kit's infra domains).
type EgressPolicy struct {
	Domains []string `json:"domains"`
}

// Scope gains:
Egress *EgressPolicy `json:"egress,omitempty"` // nil = the kit's default list

// NormalizeEgress lowercases, validates, dedupes, sorts, and drops entries
// covered by another wildcard in the list. The error names the first bad domain.
func NormalizeEgress(domains []string) ([]string, error)
```

Routes (read-modify-write the Role, keeping all its other fields):
- `PUT /admin/roles/{project}/{role}/egress` with body `{domains:[…]}` → **204**. It stores the normalized list. **400** on a bad domain (the message names it); **404** for an unknown role. An empty list is valid.
- `GET /admin/roles/{project}/{role}/egress` → **200** `{managed: bool, domains: […]}`. `managed:false` means kit default. **404** for an unknown role.
- `DELETE /admin/roles/{project}/{role}/egress` → **204**. Reverts to the kit default. **404** for an unknown role.

CLI (top-level, like `standing`):
```
at-harbor egress set   --project P --role R a.com,.b.org   # or --none for an empty policy
at-harbor egress show  --project P --role R                # "kit default" or the list
at-harbor egress clear --project P --role R
```

- [ ] Tests first: the routes (including the 400 naming the bad domain), normalization cases, and the client round trip.
- [ ] Tests first: **`POST /admin/roles` keeps an existing `Scope.Egress`**, as it already keeps `Standing`.
- [ ] Tests first: **the admin UI role form keeps the existing `Scope.Egress` and `Scope.Addressing`**. The form edits neither. Today it drops `Addressing`, the same class of bug as the Slice-1 allocation wipe; fix it here.
- [ ] `role list` output and `RoleSummary` gain the egress state (`egress=kit` or `egress=a.com,.b.org`, and `none` for an empty policy).
- [ ] The file store and Postgres store keep roles as JSON docs, so no migration is needed. Add a store-conformance case that round-trips a nil, an empty and a set policy.
- [ ] Commit: `harbor: role egress policy (egress set|show|clear)`.

## Task 5: Deliver at raise

**Files:** `internal/harbor/supervisor.go`, `internal/harbor/launcher/launcher.go`, tests for both.

- [ ] `RaiseSpec` gains `Egress *EgressPolicy`. **`Supervisor.Raise` fills it** from `store.GetRole(project, spec.Role)` after enrolling, overriding anything the caller set, so the dispatcher, sessions and standing callers need no change. Test first: a role with a policy yields a spec carrying it, and one without yields nil.
- [ ] Launcher `Raise`: after `waitForSSH` and **before** `connect.LaunchCoveMaster`, when `spec.Egress != nil`:
  - Type-assert `l.cfg.Ops.(backend.RoleEgress)`. If that fails, return `backend does not support role egress (required for role <project>/<role>)`.
  - Call `ApplyRoleEgress(name, spec.Egress.Domains)`. On error, wrap it as `apply role egress: %w`.

  Either failure goes through the existing cleanup, so the container is removed.
- [ ] Tests first (fake Ops that records call order):
  - a policy is applied before cove-master launches
  - nil policy: no egress call
  - an apply error fails the raise and removes the container
  - a backend without `RoleEgress` fails closed
  - the domains reach the op unchanged
- [ ] Log at info: `role egress applied` with id, project, role and `domains` = the **count**.
- [ ] Commit: `harbor: apply a role's egress policy at raise, before the agent starts`.

## Task 6: Real-docker check (CI integration)

**Files:** `internal/dockere2e/docker_e2e_integration_test.go`.

- [ ] Extend the existing e2e subtests. It already checks squid is up and reconfigurable (`squid -k reconfigure` with a live pid file). Run `apply-role-egress.sh` as root with a domain that is in the baked ceiling. Assert the kit file now holds it and the command exits 0, meaning the real reconfigure succeeded. Then:
  - Assert squid **denies** a CONNECT to a kit-ceiling domain the role dropped. The 403 comes from squid locally, so the test needs no internet; don't assert an allowed CONNECT, which would need real egress.
  - Run it with a domain outside the ceiling. Assert a non-zero exit and that the kit file is unchanged.
  - Assert the helper run as the `agent` user fails.
- [ ] Commit: `dockere2e: role egress replaces the kit list within the ceiling`.

## Task 7: Docs

Route each update through `docs/INDEX.md` to the doc that owns it; don't copy.

- [ ] **Repo docs:**
  - `docs/OVERVIEW.md` § *Egress: three additive allow-lists*. This becomes four lists (base, infra, kit policy, session delta) plus the ceiling, and the harbor-managed rule: the role's list replaces the kit policy list, inside the ceiling, at raise. Rename the heading and fix every inbound anchor (`rg 'egress-three-additive'`).
  - `docs/usage/at-cove-config.md` § `image.allowed-domains`: it is also the ceiling for harbor roles. Provider, GitLab and harbor hosts go in the infra list.
  - `docs/usage/harbor/roster.md` § roles: the `egress set|show|clear` verbs, the routes, nil vs empty, and "takes effect at the next raise".
  - `docs/usage/harbor/coves.md`: the raise sequence gains "apply role egress (before cove-master)" and the fail-closed rule.
  - `docs/usage/harbor/standing-sessions.md`: one line saying a standing session over the ceiling backs off, and a pointer to roster.md.
- [ ] **Template docs** (payload for agents inside a built sandbox, a separate concern):
  - `internal/assemble/hardening/image-files/home/agent/.init-agent-data/reference/sandbox-kit-changes.md` and `sandbox-hardening-limits.md`: in a harbor-managed cove, egress is its role's list. To add a domain, ask an operator to run `at-harbor egress set`, and the kit's `image.allowed-domains` must already cover it. The dev-sandbox path (edit the kit, then recreate) is unchanged.
- [ ] Run the **docs-audit** skill; fix what it finds.
- [ ] Commit: `docs: role egress at raise`.

## Out of scope (deferred)

- Re-applying a changed policy to running or paused coves.
- A harbor-side ceiling pre-check against the install manifest.
- Egress narrowing through Grant `Override`.
- Narrowing the sealed base or the infra list per role.
- Per-role kit images (the launcher still raises one manifest image).
