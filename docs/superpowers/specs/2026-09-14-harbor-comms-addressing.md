# harbor: comms C1 — roster + addressing + comms access-graph (COV-161, slice 1)

**Status:** design approved, pre-plan
**Issue:** COV-161 (comms hub slice C — escalation). This is **C1 of the C sub-slices**: the addressing foundation. Escalation policy (C2) and multi-channel/Discord (C3) build on it.
**Foundation:** COV-145 (messaging MCP — `/messages`, `Commenter`, self-scoped send/read), COV-160/162 (wake-on engine watching the cove's own ticket), COV-143 (roster/RBAC — `Role`/`Scope`/`Grant`/`Override`/`Actor`, broker `Decide`). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md` §"Escalation, roster & the comms access-graph".

## Summary

Generalize a managed cove's messaging from **"own ticket only"** to **`send(text, to=target)`** over a small symbolic target space — **humans + channels** — where harbor authorizes every recipient through a **comms access-graph** that mirrors the broker's network-scope model. This is the target space + per-send authz that C2 escalation needs; on its own it lets a worker reach a named person or a shared channel instead of only its own ticket.

**Decided in brainstorming:**
- **Entry strategy:** foundation-first. C1 = roster + addressing + access-graph; C2 = escalation engine; C3 = Discord/multi-channel.
- **Target space:** humans + channels (defer actor/role-to-actor addressing — no Manager coves exist yet to receive).
- **Send/reply:** a **human** target → @-mention on the cove's **own ticket** (a reply there wakes the cove for free via the existing wake-on engine → two-way); a **channel** target → a comment on that channel's thread, **post-only (one-way)** in C1. `read` stays own-ticket (self-scoped by construction).
- **Access-graph:** mirror `Scope` — `Role` gains an `Addressing` allow-list (glob-capable, like `Repos`), `Override` can narrow it, and a comms `Decide` resolves live, **per-grant existential**, **fail-closed**. Own ticket always allowed.
- **Project:** promote "project" from a bare string label (on `Grant`) to a **first-class entity** owning the Roster now and the escalation policy in C2.

## 1. Data model (`internal/harbor`, store version bump + additive migration)

A new first-class **Project** entity owns a **Roster**:

```go
// Human is a roster member reachable by @-mention on a tracker thread.
type Human struct {
    Name   string `json:"name"`   // roster-local name, e.g. "alice"
    Handle string `json:"handle"` // tracker @-mention handle (Linear displayName or user id)
}

// Channel is a named conduit on a Service. C1: Service == "linear", Ref == a
// ticket/thread identifier the channel maps to.
type Channel struct {
    Name    string `json:"name"`    // roster-local name, e.g. "eng-help"
    Service string `json:"service"` // "linear" in C1
    Ref     string `json:"ref"`     // Linear issue identifier (e.g. "ACME-1") the channel posts to
}

// Roster is a Project's addressable membership.
type Roster struct {
    Humans   []Human   `json:"humans,omitempty"`
    Channels []Channel `json:"channels,omitempty"`
}

// Project is the top of the config tree: it owns its Roster (and, in C2, its
// escalation policy). Roles remain keyed by (project, name) as today.
type Project struct {
    Name   string `json:"name"`
    Roster Roster `json:"roster"`
}
```

- `Role` gains `Addressing []string` (allow-list of **kind-prefixed target names**, glob-capable exactly like `Scope.Repos`); `Override` gains `Addressing []string` (REPLACES, no merge — same semantics as `Override.Repos`).
- **Target syntax:** kind-prefixed names — `human:alice`, `channel:eng-help`. Globs match within a kind: `human:*`, `channel:eng-*`, `*` (all). A bare name without a known `human:`/`channel:` prefix is invalid (fail-closed).
- **Store:** bump the store version (currently v5 from COV-149; confirm the current max at implementation time) with an **additive** migration. A migrated store has no Projects/rosters and no `Addressing` on any role → identical to today's behavior (own-ticket only, everything else denied).

## 2. Comms authz — `DecideComms` (`internal/harbor`, mirrors broker `Decide`)

```go
// AuthorizeSend reports whether actorID may send to target. Own-ticket sends do
// not call this (they carry no target and are always allowed). Resolution is
// live, additive across the actor's grants, PER-GRANT EXISTENTIAL (target must
// be allowed by some single grant's effective addressing set), and FAIL-CLOSED:
// unknown actor, no grants, malformed target, or no matching grant → false.
func (s *Supervisor) AuthorizeSend(actorID, target string) bool
```

- Effective addressing for a grant = `Override.Addressing` if set, else the `Role.Addressing` of the grant's role (resolved within the grant's project). Matching is glob, mirroring how the broker matches `Repos`.
- The target must also **resolve** against the grant's project roster (a `human:alice` grant match still fails if project has no `alice`) — authz AND existence, both fail-closed.
- Reuse the existing glob helper the broker uses for `Repos` (do not introduce a second matcher).

## 3. Delivery (Linear-only; reuses the existing `Commenter` — no new tracker methods)

The `/messages` send path resolves and delivers:

| `to` | Delivery | Reply path |
|---|---|---|
| `""` (or absent) | `PostComment(ownTicket, text)` | own ticket → existing wake-on (unchanged) |
| `human:alice` | `PostComment(ownTicket, "@"+handle+" "+text)` | own ticket → existing wake-on (**two-way, free**) |
| `channel:eng-help` | `PostComment(channelRef, text)` | **none in C1 (post-only)** |

- `ownTicket` is resolved from the caller's `Instance.Unit` via `Commenter.IssueByIdentifier` (as today). `channelRef` is resolved from the roster `Channel.Ref` via the same `IssueByIdentifier`.
- The human `handle` is looked up from the project roster; the `@`-mention is plain body text interpolation, so `Commenter` needs **no new method** (`PostComment(issueID, body)` suffices).
- `read()` is unchanged: own ticket only, no target, self-scoped by construction.

## 4. Endpoint + MCP

- **`/messages` (`internal/harbor/messages.go`):** the send request gains an optional `to string`. When non-empty, the handler calls `AuthorizeSend(actor, to)`; **403** if denied, **404** if the target is authorized-in-form but doesn't resolve in the roster. (Authz is checked before existence, so a denied target returns 403 without revealing whether it exists.) Empty `to` keeps the exact current own-ticket path. Read is untouched (no `to`). Body cap (16 KiB), fail-closed 401/403, and no-secret/no-body logging all preserved.
- A new **`GET /messages/targets`** returns the actor's allowed, resolvable targets — backing the `list_targets` MCP tool. Self-derived from the authenticated actor; no parameters.
- **`cove-master mcp` (`cmd/cove-master/mcp.go`):** `send` gains an optional `to` argument; add a `list_targets` tool. Both forward over the existing TLS+proxy path with the identity token.

**Security posture:** send's recipient authorization becomes **explicit** (`AuthorizeSend`) — the deliberate step beyond COV-145's by-construction self-scoping now that send carries a target. `read` remains by-construction. The access-graph is fail-closed: default (no addressing configured) denies every non-own-ticket target. Tokens and message bodies are never logged (unchanged from COV-145).

## 5. CLI / admin

- New **`at-harbor project`** command group: `project add <name>`, `project roster add-human <project> --name --handle`, `project roster add-channel <project> --name --service linear --ref <identifier>`, `project roster list <project>`, `project rm` / roster `rm`.
- **`at-harbor role add`** gains `--addressing <glob[,glob…]>`; grant/override addressing via the existing grant path (`--addressing` override).
- Admin API routes mirroring the CLI (under the existing admin mux), consistent with the roster/kit admin routes from COV-143/144.

## 6. Docs

- New leaf **`docs/usage/harbor/comms-addressing.md`** — OWNS the target space (kind-prefixed `human:`/`channel:` names + globs), the Project roster, the comms access-graph (`Role.Addressing` + override, per-grant existential, fail-closed), and `send(text, to=…)` delivery/reply semantics (human two-way, channel post-only). Add its INDEX row.
- **`messaging.md`** — update the "What the tools do" and scoping sections: `send` now takes an optional `to`; link to comms-addressing.md for the target space (single source; don't duplicate). Note `read` stays own-ticket.
- **`roster.md`** — cross-link: the Project roster (humans/channels) lives in comms-addressing.md; roster.md continues to own the Actor/Role/Grant RBAC story, now noting `Role.Addressing`.

## Tests (hermetic)

- **store/migration:** new Project/Roster round-trips; version-N→N+1 migration leaves a pre-existing store with empty rosters + no addressing (behavior-preserving); glob addressing on Role + Override replace-not-merge.
- **DecideComms:** per-grant existential (a target allowed by grant A but not B passes; a target in no grant fails); fail-closed on unknown actor / no grants / malformed target / target not in roster; own-ticket path never consults it; glob matching (`human:*`, `channel:eng-*`).
- **/messages send:** `to=""` → own ticket (unchanged); `to=human:alice` → `PostComment(ownTicket, "@handle …")`; `to=channel:eng-help` → `PostComment(channelRef, …)`; denied target → 403; authorized-but-unresolvable target → 404 (authz checked before existence — a denied target never reveals existence); body cap + auth unchanged. Fake `Commenter` records (issueID, body).
- **/messages/targets:** returns exactly the authenticated actor's allowed+resolvable targets.
- **cove-master mcp:** `send` with `to` forwards the field; `list_targets` round-trips (in-memory transport, as COV-145).
- **CLI/admin:** `project`/roster/`role --addressing` parse + round-trip.

## Deferred (later C sub-slices)

- **C2 — escalation engine:** category → ordered tiers of `{actor|role|channel}` → per-tier timeout → wake-worker-on-any-answer; the reserved Attach `TierChanged` control; auto-routing hooks (egress-denial/`401` → infra tier); CODEOWNERS / tracker `owner`/`assignee` ingest. C2 will union escalation-named targets into the effective addressing set on top of C1's `Role.Addressing`.
- **Cross-thread reply-routing + merged inbox:** making channel-sends (and other non-own-ticket threads) two-way; generalizing `read` into a merged, tagged multi-source inbox.
- **C3 — Discord / multi-channel:** a real chat `Service`/transport so a channel target is a Discord channel, generalizing at-switchboard.
- **actor/role-to-actor addressing:** addressing another managed cove/Manager directly (waits on the Manager pillar).

## Boundaries (hard constraints, unchanged)

- `internal/harbor` core stays free of grpc/kit/dispatch/backend/connect imports — keep the local-types + `Commenter` interface pattern from COV-145; the Linear adapter stays at the `cmd/at-harbor` wiring layer.
- Secrets and message bodies never hit logs/argv; access-graph fail-closed by construction.
- at-cove / connect / dispatchrun stay go-oidc-free and grpc-free (`cove-master` is a separate binary; unchanged here).
