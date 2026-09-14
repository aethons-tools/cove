# harbor: comms C2 v1 — escalation engine (auto-on-Waiting, human tiers) (COV-165)

**Status:** design approved, pre-plan
**Issue:** COV-165 (comms hub slice C2 — the escalation engine). This is **C2 v1**: auto-on-Waiting, a single default per-project policy, human tiers only. Categories, auto-detection, and channel tiers are deferred (see below).
**Foundation:** COV-161 (C1 — Project/Roster/Human/Channel, `Scope.Addressing`, `send(to=…)`), COV-160/162 (wake-on engine + `WaitingSince`/`WaitCursor` + the reply→wake / max-wait→teardown loop), COV-149 (supervisor Instance/Report). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md` §"Escalation, roster & the comms access-graph".

## Summary

While a managed cove is **Waiting** (blocked for input), a **separate resident escalation engine** pings an ordered set of **human tiers** on **per-tier timers**, advancing to the next tier if the current one doesn't answer. The existing **wake-on** engine keeps owning the other clock — "a reply arrived → wake the cove" and "max-wait → abandon". Two independent clocks, cleanly split across two engines: **escalation only pings; wake-on only wakes/reaps.** Opt-in per project (no policy → today's passive wait, unchanged).

**Decided in brainstorming:**
- **Shape:** auto-on-Waiting; one default per-project policy; **human tiers only** (a human is @-mentioned on the cove's **own ticket**, so a reply wakes it via existing wake-on — no cross-thread reply-routing, which is C3).
- **Architecture:** a **separate engine** (`internal/escalate`), not an extension of wake-on. It gates purely on `Activity==Waiting` + persisted tier state + timers, so it **reads no comments** (no double-poll) and calls the tracker only when it actually pings.
- **State:** persisted on the `Instance` (restart-safe), mirroring `WaitingSince`/`WaitCursor`.
- **Tier-0 ping is immediate** on Waiting-entry (per-tier *timeout* governs advancement; no pre-tier-0 grace — an operator wanting a delay sets a longer tier-0 timeout).

## 1. Policy data (`internal/harbor`, additive store)

`Project` (first-class since C1) gains an ordered escalation policy:

```go
// EscalationTier is one rung of a Project's escalation policy: the targets to
// ping and how long to wait for an answer before advancing to the next tier.
type EscalationTier struct {
	Targets []string      `json:"targets"` // kind-prefixed human names, e.g. "human:alice"
	Timeout time.Duration `json:"timeout"` // wait after pinging this tier before advancing
}

// (added to Project)
	Escalation []EscalationTier `json:"escalation,omitempty"`
```

- `Targets` are **kind-prefixed human names** (`human:<name>`), resolved against the same C1 `Roster.Humans` for `@handle`. C2 v1 only supports `human:` targets; a `channel:` (or other) target in a tier is ignored with a logged warning (channel tiers need reply-routing → C3). Bare (unprefixed) or malformed entries are skipped with a warning — never a hard failure that would strand a blocked cove.
- **Additive store:** add `Escalation` to the persisted `Project`; a store with no policy loads to `nil` → no escalation (backward-compatible). No new store version machinery (shape-detected, as with the C1 roster).
- Store gains `SetEscalationPolicy(project string, tiers []EscalationTier) error` and exposes the policy via the existing `GetProject`. (Roster resolution reuses `GetRoster` from C1.) *(Named `…Policy` to disambiguate from the supervisor's per-actor `SetEscalation` runtime-state setter in §2.)*

## 2. Runtime state (`internal/harbor`, additive `Instance` fields)

```go
// (added to Instance)
	EscalationTier int       `json:"escalation_tier,omitempty"` // -1/absent = no escalation open; else last-pinged tier index
	TierPingedAt   time.Time `json:"tier_pinged_at,omitempty"`  // when EscalationTier was pinged
```

- **Default/absent = no escalation open.** An `Instance` loaded from a pre-C2 store has `EscalationTier == 0` by Go zero-value — which would wrongly read as "tier 0 already pinged". To avoid a migration, treat the pair as *open* only when `TierPingedAt` is non-zero; a zero `TierPingedAt` means "no escalation open yet" regardless of the int. (Equivalently: the engine opens tier 0 iff `TierPingedAt.IsZero()`.) This is the load-bearing invariant — the plan's tests pin it.
- **Reset on Waiting-entry:** `Supervisor.Report`, which already clears `WaitCursor` and stamps `WaitingSince` on the transition **into** Waiting, additionally clears the escalation state (`EscalationTier=0`, `TierPingedAt=zero`) — so every fresh Waiting-entry starts a new escalation from tier 0.
- **New supervisor method:** `SetEscalation(actorID string, tier int, at time.Time) error` — the engine's sole writer of escalation state (read-modify-write under the store lock, like `SetWaitCursor`; the pre-existing non-atomic-RMW class is COV-163, not this slice).

## 3. The escalation engine (`internal/escalate`)

A new resident engine mirroring `internal/wakeon`'s structure (`New(...) *Engine`, `Run(ctx)`, `tick(ctx)`, injected clock, narrow interfaces so it stays out of harbor's grpc/kit/dispatch import graph).

```go
type Registry interface{ ListInstances() []harbor.Instance }
type Projects interface {
	GetProject(name string) (harbor.Project, bool)
	GetRoster(project string) (harbor.Roster, bool)
}
type State interface {
	SetEscalation(actorID string, tier int, at time.Time) error
}
type Pinger interface {
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	PostComment(ctx context.Context, issueID, body string) error
}
type Config struct{ PollInterval time.Duration } // default 30s
```

**Per tick**, for each instance with `Activity == harbor.ActivityWaiting`:
1. Resolve the instance's project policy: `proj, ok := projects.GetProject(inst.Project)`; if `!ok || len(proj.Escalation) == 0` → skip (escalation not configured).
2. **Open (tier 0):** if `inst.TierPingedAt.IsZero()` → `ping(inst, proj, 0)`; `state.SetEscalation(inst.ActorID, 0, now)`; continue.
3. **Advance:** `cur := inst.EscalationTier`; if `now - inst.TierPingedAt > proj.Escalation[cur].Timeout` and `cur+1 < len(proj.Escalation)` → `ping(inst, proj, cur+1)`; `state.SetEscalation(inst.ActorID, cur+1, now)`; continue.
4. Otherwise (timer not elapsed, or last tier already pinged) → nothing. (Abandonment is wake-on's `max-wait`, not escalation's job.)

**`ping(inst, proj, tier)`** — resolve the tier's human handles from the roster and post an @-mention nudge on the cove's own ticket:
```
issueID ← Pinger.IssueByIdentifier(ctx, inst.Unit)         // the cove's own ticket
handles ← for each "human:<name>" in proj.Escalation[tier].Targets:
             look up name in roster.Humans → "@"+Handle    // skip non-human/unknown with a warn
body    ← "<handles joined by space> — cove "+inst.ActorID+" needs input on "+inst.Unit+" (escalation tier "+tier+")"
Pinger.PostComment(ctx, issueID, body)
```
- A tier with **no resolvable human handles** posts nothing (logged) but **still advances the timer** (so a mis-configured tier doesn't wedge the escalation — the next tier is reached after its timeout). *(Design choice: advancing-on-empty keeps a blocked cove progressing; the plan documents it.)*
- Because the ping lands on the cove's **own ticket**, a human's reply is picked up by the **existing wake-on** engine (comment-count bump → wake) — the escalation engine never reads comments and never wakes anyone.
- **No cove authz:** harbor is the sender here (operator-policy-driven), so `DecideSend`/the comms access-graph is not consulted — that gates a *cove's* outbound `send`, not harbor's own escalation pings.

**Idled (paused) coves:** a B2-paused cove is still `Activity==Waiting`, so the engine keeps pinging tiers while it's frozen (correct — the human still needs nudging). When a reply lands, wake-on resumes+wakes it; there is a brief window (resume→run) where it's still Waiting and could get one extra tier ping if a timeout coincides — harmless (a redundant @-mention), and bounded by the seconds-long resume window vs minute-scale tier timeouts.

## 4. Config + wiring (`cmd/at-harbor`)

- `dispatcherConfig` gains `EscalationPollInterval string \`yaml:"escalation-poll-interval"\`` (optional; empty → engine default 30s), parsed like `wake-poll-interval` (sibling discard-error `time.ParseDuration`).
- In `cmdServe`, inside the tracker-gated dispatcher block (where wake-on is built), construct the escalation engine and `go eng.Run(ctx)`: `escalate.New(st /*Registry*/, st /*Projects*/, sup /*State*/, linearCommenter{tracker} /*Pinger*/, escalate.Config{PollInterval}, log)`. `st` (the store) already satisfies `Registry`/`Projects`; `sup` gains `SetEscalation`; `linearCommenter` already provides `IssueByIdentifier`/`PostComment`. No new secret, no new adapter.

## 5. Operator surface (admin + adminclient + CLI)

- **Admin:** `PUT /admin/projects/{project}/escalation` (body: the ordered tiers) + `GET /admin/projects/{project}/escalation` (read back). Behind the operator authenticator, alongside the C1 roster routes; `url.PathEscape` on the project segment.
- **adminclient:** `SetEscalationPolicy(project string, tiers []harbor.EscalationTier) error`, `GetEscalationPolicy(project string) ([]harbor.EscalationTier, error)`.
- **CLI:** `at-harbor project escalation` subcommands:
  - `escalation set <project> --tier '<targets>@<timeout>' [--tier … …]` — replaces the whole ordered policy; each `--tier` is `comma,separated,targets@duration` (e.g. `--tier 'human:alice,human:bob@15m' --tier 'human:carol@1h'`).
  - `escalation list <project>` — print the tiers.
  - `escalation clear <project>` — remove the policy.
  Parse each `--tier` value into `EscalationTier{Targets: split(csv), Timeout: ParseDuration(after @)}`; a tier with no `@<timeout>` is a parse error.

## 6. Docs

- New leaf **`docs/usage/harbor/escalation.md`** — OWNS the escalation model: the per-project ordered human tiers + per-tier timeout, auto-on-Waiting (immediate tier-0), the two independent clocks (escalation tier-timer vs wake-on `wait-max`), how a ping is delivered (@-mention on the cove's own ticket → answered via existing wake-on), the advance-on-empty-tier behavior, and the `at-harbor project escalation` commands. Proper frontmatter; `updated: 2026-09-14`; ≤200 lines. Deferred items listed (categories, auto-detection, channel tiers, `TierChanged`).
- **`messaging.md`** / **`comms-addressing.md`** — one-line cross-link to escalation.md (they own messaging/addressing; escalation.md owns the tier policy). **`INDEX.md`** — add the escalation.md row.

## Tests (hermetic)

- **store:** `Project.Escalation` round-trips + additive load (a pre-C2 store → nil policy); `SetEscalationPolicy`(project) replace semantics.
- **supervisor:** `SetEscalation`(actorID) writes tier+time; `Report` resets escalation state (`TierPingedAt` zero, tier 0) on transition into Waiting; the `TierPingedAt.IsZero()==not-open` invariant.
- **engine (clock-injected, fake Registry/Projects/State/Pinger):**
  - Waiting + policy + `TierPingedAt` zero → pings tier 0, `SetEscalation(_,0,now)`, PostComment on own ticket with the tier-0 handles.
  - tier 0 open, `now-TierPingedAt > tier0.Timeout`, tier 1 exists → pings tier 1, advances.
  - timer not elapsed → no ping.
  - last tier pinged + elapsed → no ping (no teardown — that's wake-on).
  - `Activity != Waiting` → ignored (no ping).
  - project has no policy → ignored.
  - tier with only non-human/unknown targets → advances the timer, posts nothing.
  - ping targets the cove's OWN ticket (via `IssueByIdentifier(inst.Unit)`), never a tier member's own ticket.
- **config:** `escalation-poll-interval` parse (valid + empty→default).
- **admin/adminclient/CLI:** escalation route round-trip; `--tier 'a,b@15m'` parse (+ missing-`@` error).

## Deferred (C2b / C3 / later)

- **Categories + agent-declared `escalate(category)`** and **harbor auto-detection** (egress-wall denial / `401` → infra tier; CODEOWNERS / tracker `owner`/`assignee` ingest for human tiers). v1 is a single default policy per project.
- **Channel tiers** — a tier that pings a `channel:` target; needs cross-thread reply-routing (a reply on a channel thread waking the cove), which is a C3 concern.
- **The reserved Attach `TierChanged` control** — signalling a tier change to a *waiting cove*. v1 pings the ticket, not the cove, so the cove needs no tier signal; this is the hook for a future model where the cove reacts to escalation state.
- **Escalation observability** — surfacing a cove's current tier in `at-harbor cove list` / the UI (nice-to-have follow-up).

## Boundaries (hard constraints)

- `internal/escalate` stays free of grpc/kit/dispatch/backend/connect imports — narrow interfaces + a `Pinger` (the `linearCommenter` adapter lives at the `cmd/at-harbor` wiring layer), exactly like `internal/wakeon`.
- Harbor's escalation pings are **operator-policy-driven** and do NOT consult the comms access-graph (that gates a cove's outbound `send`, not harbor's own pings).
- Secrets and message bodies never hit logs/argv; @-handles + tier indices are non-secret and may be logged.
- wake-on remains the sole owner of reply-detection, waking, and max-wait teardown; escalation never reads comments, wakes, or tears down. The two engines share only the `Instance.Activity==Waiting` gate.
