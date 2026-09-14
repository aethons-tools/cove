# harbor: comms C2b v1 — escalation categories + agent-declared `escalate(category)` (COV-167)

**Status:** design approved, pre-plan
**Issue:** COV-167 (comms hub slice C2b — the routing/intelligence half of escalation). This is **C2b v1**: category-keyed policies + an agent-declared `escalate(category)` MCP tool. Harbor auto-detection, CODEOWNERS/assignee ingest, and the `TierChanged` control are deferred (see below).
**Foundation:** COV-165 (C2 v1 — the resident escalation engine + `Project.Escalation` default chain + `Instance.EscalationTier`/`TierPingedAt` + auto-on-Waiting), COV-161 (C1 — the brokered messaging MCP + `/messages` endpoint + `cove-master mcp` tool pattern the `escalate` tool mirrors), COV-160/162 (wake-on). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md` §"Escalation, roster & the comms access-graph".

## Summary

Let a blocked cove declare its **block category** so harbor routes to the right people. Escalation policies become **category-keyed** — additive over C2 v1's single default chain — and a brokered **`escalate(category)`** MCP tool stamps the cove's `Instance`. The escalation engine routes by the declared category, **falling back to the default chain** for an unknown/unset category. Everything else about escalation (auto-on-Waiting trigger, immediate tier-0, per-tier timers, @-mention delivery on the cove's own ticket) is unchanged from C2 v1.

**Decided in brainstorming:**
- **Scope:** categories + the *agent-declared* setter (`escalate(category)` MCP tool). Harbor *auto-detection* (egress/401→infra) is the deferred implicit setter.
- **Additive policy:** keep `Project.Escalation` as the default chain; add `EscalationByCategory` overrides. No migration; shipped v1 policies are the default.
- **Category lifetime:** `Instance.EscalationCategory` is set only by `escalate()` and **persists** until re-declared or teardown — `Report` does NOT clear it (this sidesteps the `escalate()`-then-`Report(Waiting)`-clears ordering problem). A cove re-declares when its block kind changes.
- **`escalate` only categorizes** — escalation still auto-opens on Waiting (v1); the tool does not itself trigger an escalation.
- **Categories are free-form strings;** unknown → default chain (no validation needed, safe by construction).

## 1. Policy data (`internal/harbor`, additive store)

```go
// (added to Project)
	EscalationByCategory map[string][]EscalationTier `json:"escalation_by_category,omitempty"`
```

- `Project.Escalation []EscalationTier` (from C2 v1) remains the **default/uncategorized** chain. `EscalationByCategory` maps a category name → its ordered tier chain.
- Fully additive: a store written by C2 v1 has no `escalation_by_category` key → loads to a nil map → every cove uses the default chain, exactly as today.
- Store gains `SetEscalationPolicy(project, category string, tiers []EscalationTier) error` — **`category == ""` sets the default `Escalation`**, a non-empty `category` sets `EscalationByCategory[category]` (creating the map lazily). *(This changes the C2 v1 signature `SetEscalationPolicy(project, tiers)` → `SetEscalationPolicy(project, category, tiers)`; the existing default path passes `""`. Update the C2 v1 admin route/adminclient/CLI call sites accordingly.)*
- `GetProject` (already deep-copies `Roster`/`Escalation` slices) must additionally **deep-copy the `EscalationByCategory` map** (a fresh map, each value a fresh `[]EscalationTier` with fresh `Targets`) so the engine can't mutate store state.

## 2. Runtime state (`internal/harbor`, additive `Instance` field)

```go
// (added to Instance)
	EscalationCategory string `json:"escalation_category,omitempty"` // cove-declared block category; "" = default chain
```

- Set ONLY by `Supervisor.SetEscalationCategory` (below). **`Report` does NOT touch it** — it persists across Waiting/Running transitions until the cove re-declares or the instance is torn down. (Contrast the tier state `EscalationTier`/`TierPingedAt`, which `Report` *does* reset on entering Waiting — those still reset per block; only the *category* persists.)
- **New supervisor method:**
```go
// SetEscalationCategory stamps the cove-declared block category on its instance.
// Persists until re-declared; the escalation engine reads it to pick the tier
// chain (falling back to the default when the category isn't configured).
func (s *Supervisor) SetEscalationCategory(actorID, category string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok { return fmt.Errorf("no instance for actor %q", actorID) }
	inst.EscalationCategory = category
	return s.store.PutInstance(inst)
}
```

## 3. Setting it — brokered `POST /escalate` + `escalate` MCP tool

Mirrors the C1 messaging broker (`/messages` + `cove-master mcp` `send`), but simpler (no ticket resolution — it just stamps the instance).

- **Harbor endpoint** `POST /escalate` on the cove-facing `:443` mux (routed alongside `/messages` in `messagesMux`): a request carries the cove's identity token (`Authorization: Bearer`); harbor `store.Lookup(HashToken)` → actor → confirm a live `Instance` → `Supervisor.SetEscalationCategory(actor.ID, category)` → **204**. **Self-scoped by construction:** the category applies to the *authenticated* actor's own instance — there is no target/actor parameter. Fail-closed 401 (missing/unknown token) / 403 (no instance). The request body is `{"category": "<string>"}`, capped (~64 bytes, like the message-body cap); an empty category is allowed (resets to the default chain). Nothing logged beyond the non-secret category string.
- **`cove-master mcp`** (`cmd/cove-master/mcp.go`): a new `escalate` tool — `escalate(category string)` → `POST /escalate`. Forwarded over TLS-through-proxy using the identity token already in the cove env (no new secret). The agent calls it mid-turn to categorize its block, then finishes the turn with `needs-input` as usual.

## 4. Engine routing (`internal/escalate`, `chainFor` + bounds guard)

The engine derives the chain from the instance's category each tick and operates on it, replacing the current direct use of `proj.Escalation`:

```go
func chainFor(proj harbor.Project, category string) []harbor.EscalationTier {
	if c, ok := proj.EscalationByCategory[category]; ok && len(c) > 0 {
		return c
	}
	return proj.Escalation // default / fallback for unset or unknown category
}
```

In `tick`, after resolving `proj`:
```go
	chain := chainFor(proj, inst.EscalationCategory)
	if len(chain) == 0 { continue } // neither a category chain nor a default configured
	if inst.TierPingedAt.IsZero() { e.pingTier(ctx, inst, proj, chain, 0); continue }
	cur := inst.EscalationTier
	if cur+1 < len(chain) && e.now().Sub(inst.TierPingedAt) > chain[cur].Timeout {
		e.pingTier(ctx, inst, proj, chain, cur+1)
	}
```
- `pingTier` takes the resolved `chain` (instead of indexing `proj.Escalation`). The `cur+1 < len(chain)` guard both stops advancing past the last tier AND bounds the `chain[cur]` access (it implies `cur < len(chain)`), so a mid-escalation category change that shrank the chain (a stale `EscalationTier` index ≥ `len(chain)-1`) simply stops advancing — no panic.
- The load-bearing C2 v1 invariant (`TierPingedAt.IsZero()` ⇔ open) is unchanged. Everything else — auto-on-Waiting, immediate tier-0, per-tier timeouts, own-ticket @-mention delivery, transient-error-doesn't-advance, empty-tier-advances-without-posting — is unchanged.

## 5. Operator surface (admin + adminclient + CLI)

- **admin** (`internal/harbor/admin.go`): `EscalationBody` gains `Category string \`json:"category,omitempty"\``. `PUT /admin/projects/{project}/escalation` passes `b.Category` to `SetEscalationPolicy`. `GET /admin/projects/{project}/escalation` returns both the default and the category map — e.g. `EscalationView{Default []EscalationTier, ByCategory map[string][]EscalationTier}` (a read struct, so `list` can render everything).
- **adminclient**: `SetEscalationPolicy(project, category string, tiers []harbor.EscalationTier) error` (signature gains `category`); `GetEscalationPolicy(project) (EscalationView, error)` returns default + byCategory.
- **CLI** (`cmd/at-harbor/main.go`, `cmdProjectEscalation`): `set` gains `--category <name>` (omitted → default); `list` prints the default chain and each category's chain (labeled); `clear` gains `--category <name>` (omitted → clears the default). The `--tier 'targets@timeout'` parsing is unchanged.

## 6. Docs

- **`docs/usage/harbor/escalation.md`** — add: the **category model** (default chain + `EscalationByCategory` overrides; free-form categories; unknown→default), the **`escalate(category)` tool** (agent-declared, self-scoped, persists-until-re-declared, categorizes-but-doesn't-trigger), and the `--category` operator commands. Note that harbor auto-detection is still deferred.
- **`docs/usage/harbor/messaging.md`** — add `escalate` to the cove's brokered tool list (`read`/`send`/`list_targets`/**`escalate`**), one line, linking to escalation.md for the semantics.
- **INDEX.md** row/`updated` bumps as needed.

## Tests (hermetic)

- **store:** `EscalationByCategory` round-trips + additive load (pre-C2b store → nil map → default chain); `SetEscalationPolicy(project, "", tiers)` sets the default, `SetEscalationPolicy(project, "infra", tiers)` sets the category entry (replace semantics per category); `GetProject` deep-copies the map (mutating a returned category chain's `Targets` doesn't corrupt the store).
- **supervisor:** `SetEscalationCategory` persists; `Report` entering Waiting does NOT clear `EscalationCategory` (but still resets tier state).
- **engine:** `chainFor` picks the category chain when configured, else default (unknown category → default; unset → default; a configured-but-empty category chain → default); routing uses the category chain end-to-end (tier-0 + advance); bounds guard survives a shrunk chain after a category change.
- **/escalate endpoint:** valid token + `{category}` → 204 + `SetEscalationCategory` called with the caller's actor id; missing/unknown token → 401; no instance → 403; oversize body → 413; empty category allowed (→ default). Self-scoped (no target param exists).
- **cove-master mcp:** `escalate` tool forwards `{category}` to `POST /escalate` (in-memory transport, like the C1 `send` test); token env-only/never-logged preserved.
- **admin/adminclient/CLI:** `PUT` with a `category` round-trips into `EscalationByCategory`; `GET` returns default + byCategory; `project escalation set --category infra` + `list` shows both default and infra; `clear --category infra` removes just that category.

## Deferred (further C2b / C3)

- **Harbor auto-detection** (the implicit setter): the broker/proxy stamps `infra` on an actor when it denies an egress request (egress-wall denial / `401`), routing without agent cooperation. Next C2b slice; touches the security-critical proxy path.
- **CODEOWNERS / tracker `owner`/`assignee` ingest** to auto-populate human tiers.
- **The reserved Attach `TierChanged` control** — signalling a tier change to a waiting/paused cove.
- **Channel tiers** — a tier that pings a `channel:` target; needs cross-thread reply-routing (C3, COV-166).
- **A "list categories" discovery tool** for the cove (v1 uses coarse well-known category names; unknown→default keeps it safe without discovery).

## Boundaries (hard constraints)

- The `/escalate` endpoint stays in `internal/harbor` core-clean like `/messages` (Bearer→actor→supervisor; no kit/grpc/dispatch pulled in). `internal/escalate` still imports only `harbor` + stdlib.
- The declared category is **agent-supplied and untrusted**, but harmless by construction: an unknown category falls back to the default chain, and the category never selects a *recipient* the operator didn't configure (it selects among operator-defined tier chains). It does not bypass the comms access-graph or reach the broker's credential path.
- Secrets and message bodies never hit logs/argv; the category string is non-secret and may be logged.
- wake-on remains the sole owner of reply-detection/waking/teardown; escalation still only pings.
