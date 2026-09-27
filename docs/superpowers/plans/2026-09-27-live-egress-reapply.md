# Live egress re-apply

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.
>
> **Prerequisite:** PR #233 (role egress at raise) must be merged first. This plan builds directly on its `Scope.Egress`, `RaiseSpec.Egress`, `backend.RoleEgress` and `apply-role-egress.sh`.

**Goal:** Changing a role's egress policy (`egress set` or `egress clear`) reaches that role's **running** coves within one supervisor reconcile pass. It no longer waits for the next raise. A paused cove gets the new policy when it resumes, before its agent is woken.

**Architecture:**
- **Drift, not push.** Each Instance records the policy it is running under: `Instance.Egress`, a canonical fingerprint. The egress routes only write the Role, as today. `Supervisor.Reconcile` compares every Live cove it holds the lease on against its role's current policy and re-applies on drift. This also covers a harbor restart, a changed role, and a role policy cleared back to the kit default, with no signalling between the admin route and the supervisor.
- **Paused coves wait for resume.** Reconcile skips Idled coves (you can't exec into a paused container). `Supervisor.Resume` checks for drift after unpausing and before marking the cove Live. The wake is only sent on a later tick, so the agent never runs a turn under a stale policy.
- **Clearing restores the kit default.** `apply-role-egress.sh --kit-default` copies the baked ceiling (`image.allowed-domains`, which is also the kit default) back into the active list. The launcher's `ApplyEgress` with a nil policy uses it.
- **Fail closed, with a short grace.** A failed re-apply is retried on the next pass. After **3 consecutive failures** the supervisor tears the cove down, because a cove must not keep running under a policy other than its role's. A standing session is raised again under the new policy; an ephemeral or personal one ends. A resume whose re-apply fails tears down at once, because the alternative is waking an agent under a stale policy.

**Tech Stack:** Go 1.26, bash, squid, `just`. Builds on [`2026-09-27-role-egress-at-raise.md`](2026-09-27-role-egress-at-raise.md).

## Decisions in this plan

- **Fingerprint:**
  - `"kit"` for the kit default (nil policy)
  - `"none"` for an empty policy
  - otherwise `"d:" + strings.Join(domains, ",")`, where the domains are already normalized and sorted by `NormalizeEgress`

  An Instance with `Egress == ""` was raised before this change, so the supervisor re-applies once (it's idempotent) and records the fingerprint.
- **The 3-failure grace applies to Live coves only.** It absorbs a transient `docker exec` or squid-reload failure without tearing down an active session. The cost is up to three reconcile intervals under the old policy. A ceiling rejection (the role asks for more than the image allows) fails every time, so it reaches the teardown too. The log line names the rejected domain.
- **Only the lease holder applies.** Re-apply runs in the same branch of `Reconcile` that renews our own lease, so two harbors never both exec into a cove.
- **Nothing here changes what a role can allow.** The in-box ceiling check is unchanged; this only changes *when* a policy lands.

## Global Constraints

- **Raise behavior unchanged,** apart from recording `Instance.Egress`.
- **Sealed:** the helper's new `--kit-default` mode is root-only like the rest of it, reads only the baked ceiling, and takes no domains on argv (the flag is the only argument). The agent user still can't run it.
- **Logs:** the fingerprint's domain count, never the list. The failure count on a failed re-apply, and "torn down: egress re-apply failed" on teardown.
- **Docs in the same change.** TDD. Stage files by path (untracked `.switchboard/`). Each commit builds. End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Helper `--kit-default` mode

**Files:** `internal/assemble/hardening/image-files/usr/local/lib/cove/apply-role-egress.sh`, `internal/assemble/image_scripts_test.go`.

- [ ] Tests first (same fake `id`/`squid` harness as the existing helper tests):
  - `--kit-default` with empty stdin: the kit file becomes the ceiling's entries under a header saying it is the kit default, and the session file is cleared, then reconfigure runs
  - `--kit-default` with non-empty stdin: exit 2, nothing changed (the mode takes no domains)
  - an unknown flag: exit 2, nothing changed
  - a missing ceiling: exit 1, nothing changed
  - run as non-root: refused
- [ ] Implement it as a branch before the stdin read. It reuses the atomic write, the `chmod 0644` and the reconfigure.
- [ ] Commit: `hardening: apply-role-egress --kit-default restores the kit's list`.

## Task 2: Backend and launcher ops

**Files:** `internal/backend/backend.go`, `internal/backend/colima/egress.go`, `internal/harbor/supervisor.go` (the `Launcher` interface), `internal/harbor/launcher/launcher.go`, the placeholder launcher in `cmd/at-harbor`, and every fake `Launcher` in tests.

```go
// backend.RoleEgress gains:
ResetRoleEgress(container string) error // docker exec -u root <c> apply-role-egress.sh --kit-default (empty stdin)

// harbor.Launcher gains:
// ApplyEgress sets a running cove's egress to p (nil = the kit default).
ApplyEgress(ctx context.Context, inst Instance, p *EgressPolicy) error
```

- [ ] Tests first:
  - colima: the argv of the reset, and its helper output wrapped into the error (as `ApplyRoleEgress` does)
  - launcher `ApplyEgress`: nil calls reset; a policy calls `ApplyRoleEgress(inst.Location, domains)`; a backend without `RoleEgress` errors
- [ ] Refactor `Raise` so it uses the same internal apply (nil policy: skip at raise, as today; the image boots with the kit default).
- [ ] Commit: `harbor: Launcher.ApplyEgress (re-apply or reset a running cove's egress)`.

## Task 3: Record and reconcile

**Files:** `internal/harbor/instance.go`, `internal/harbor/egress.go`, `internal/harbor/supervisor.go`, tests.

```go
// Instance gains:
Egress         string `json:"egress,omitempty"`          // fingerprint of the applied policy; "" = unknown (pre-feature)
EgressFailures int    `json:"egress_failures,omitempty"` // consecutive failed re-applies

func EgressFingerprint(p *EgressPolicy) string // "kit" | "none" | "d:a.com,.b.org"
const egressMaxFailures = 3
```

- **Raise:** set `inst.Egress = EgressFingerprint(spec.Egress)`.
- **Reconcile:** in the "our own unexpired lease" branch, after renewing, call `s.reconcileEgress(ctx, &inst)`. Resolve the role's desired policy (`GetRole` → `Scope.Egress`); a missing role means the kit default. If the fingerprint differs:
  - `launcher.ApplyEgress` succeeds: record the fingerprint, reset `EgressFailures`, log info.
  - It fails: increment `EgressFailures` and log a warning with the count. At `egressMaxFailures`, call `s.Teardown` and log `torn down: egress re-apply failed`.

  Write the instance back once.
- **Resume:** after `Unpause` succeeds, and before setting `PhaseLive`, if there is drift, apply it. On failure, log and `Teardown` at once, returning a wrapped error. On success, record the fingerprint.
- **Adoption:** in the lease-steal branch (after an alive probe), nothing extra. The next pass is ours and reconciles.

- [ ] Tests first (fake launcher):
  - no drift: no apply
  - a set policy on a kit-default cove: applied and recorded
  - a cleared policy on a policed cove: `ApplyEgress(nil)` and `"kit"` recorded
  - `""` (legacy): applied once, then nothing on the next pass
  - apply failing: counts 1, 2, then teardown on 3; a success in between resets the count
  - someone else's lease: no apply
  - an Idled cove is skipped by Reconcile
  - Resume with drift applies before Live
  - Resume whose apply fails tears down and returns an error
  - a missing role means the kit default
- [ ] Store round trip: `Egress` and `EgressFailures` survive in the file store (the Postgres store keeps instances as a JSON doc; add a conformance case if instances are covered there).
- [ ] Commit: `harbor: re-apply a changed role egress policy to running coves`.

## Task 4: Docs

- [ ] `docs/usage/harbor/roster.md` § Role egress: replace "takes effect at the next raise" with "takes effect on running coves within one reconcile pass; a paused cove gets it when it resumes". Add the fail-closed rule (3 failed passes, or a failed resume, tears the cove down; a standing session comes back under the new policy).
- [ ] `docs/usage/harbor/coves.md`: the reconcile pass now also re-applies egress drift, and resume applies it before Live.
- [ ] `docs/usage/harbor/standing-sessions.md` / `personal-sessions.md`: only if they repeat "next raise". Replace it with a pointer to roster.md, don't copy.
- [ ] Template `sandbox-hardening-limits.md` (payload): if it says a role change needs a new cove, correct it.
- [ ] docs-audit checker with `--index OVERVIEW.md`: no new errors against main.
- [ ] Commit: `docs: role egress changes reach running coves`.

## Out of scope (deferred)

- Notifying a personal session's owner when their session is torn down for an egress failure (it's logged; a squawk could follow).
- Making the grace (3 passes) configurable.
- Telling the agent inside the cove that its egress changed.
