---
kind: design-spec
subject: harbor — a durable central service for hardened agent sandboxes (control plane · credential broker · comms hub · dispatcher)
status: draft
date: 2026-09-10
read-when: seeding, evaluating, or slicing the "harbor" central-service pivot; or before starting any harbor sub-project spec
---

# Harbor — durable central service for hardened agent sandboxes

## One paragraph

`at-cove` today is a **host-run CLI**: each repo carries a committed `.at-cove/` kit, each operator machine
runs Colima + supplies secrets locally, and the actors (autonomous **workers**, interactive
**collaborators**, standing Discord **teammates**) are launched imperatively from a laptop, dying when that
process does. **Harbor** (`at-harbor`) inverts this into a **durable, always-on central service** that owns
the kit registry, the roster of running actors and their role/security/prompt bindings, a credential +
egress **broker** that keeps secrets out of every sandbox, a **comms hub** across Discord/tracker/API, and a
resident **dispatcher** that receives webhooks. Coves become near-stateless per-turn compute that harbor
raises, brokers, suspends, and drops.

## Drivers (owner's priority order)

1. **Multi-operator** — move from a single-laptop tool to a shared service that a team (human *and* agent)
   drives.
2. **Durability / always-on** — dispatcher and standing teammates resident 24/7, webhook-driven, surviving
   restarts.
3. **Setup friction** — define a kit once, centrally, run it anywhere; no per-repo/per-host bootstrapping.
4. **Security consolidation** — pull kits and secrets out of repos and laptops into one controlled place.

> **Value order ≠ dependency order.** Security ranked #4, but its core mechanism — the **broker** — turned
> out to be the *keystone* everything else rests on (see [Security](#security-the-central-boundary-broker)
> and [Decomposition](#decomposition--sequencing)).

## On-the-shelf posture

Deliberately borrow, don't invent, where a pattern exists:

- The credential-injecting broker is the **CyberArk Secretless Broker** / SPIFFE-SVID pattern.
- Tiered escalation is **on-call escalation policy** (PagerDuty/Opsgenie): ordered tiers + per-tier timeout.
- "Module owner" resolution overlaps **CODEOWNERS**.
- The resident webhook dispatcher + job model is commodity queue/scheduler territory (the existing
  `internal/dispatch/scheduler` made resident behind an HTTP intake).

The genuinely at-cove-specific work is (a) a live roster of hardened actors with uniform role→security→prompt
binding, and (b) threading the broker through *at-cove's* egress model.

---

## Core model

### Actors and the one fault line

**Actor** is the umbrella for any authorized party harbor coordinates (the actor-model connotation is apt —
harbor routes messages between them). Four classes:

| Class | What it is | Lifecycle |
|---|---|---|
| **Worker** | Ephemeral cove, bound to one unit of work; may wait/converse/escalate meanwhile, self-terminates when the unit resolves. | **trigger**-raised (ticket→Ready), **terminal** |
| **Manager** | Standing role-bound cove (the evolved chat `collaborator`). | **roster**-declared, **perpetual** (suspend-when-idle, never self-drops) |
| **Guest** | A cove harbor does **not** manage; self/externally run; opts into the broker + channels. | self-managed |
| **External** | A **human** outside the boundary, reached via a channel (Discord). Peer of the others: same authz + comms interface, not a cove. | outside |

The taxonomy's **primary axis is one question — "does harbor raise it?"** — and it governs three things at
once:

| | Worker | Manager | Guest | External |
|---|:--:|:--:|:--:|:--:|
| Harbor raises it? | ✓ | ✓ | ✗ | ✗ |
| Prompt has teeth | ✓ | ✓ | inert | inert |
| Security enforcement | by construction* | by construction* | boundary policy | boundary policy |
| Lifecycle owner | harbor | harbor | self/ext | self/ext |

\* "by construction" = the forcing boundary; with the broker it *relocates* to harbor-owned fabric (below).
Worker↔Manager then differ only by **lifetime**; Guest↔External only by **substrate** (a cove vs not).

### Two planes

- **Actor plane (runtime):** Worker, Manager, Guest, External human — things that reason/converse. Uniform
  authz + comms.
- **Config / infrastructure plane (meta):** **Services, Kits, Projects, Roles** — mechanical, structured,
  admin-edited.

**Service** (config plane) — a configured platform integration (Linear, GitHub, Discord-the-platform, the
Anthropic API). **Not an actor**: no prompt, no conversation; trust is a config decision (webhook signature),
not actor authz. **Channel** rides *on* a Service — an addressable conduit an actor speaks on (a Discord
channel, a ticket thread); one Service → many Channels.

### Object model

```
Project (ACME)  ──defines──▶  Role (implement, review, steward, …)
                                   │  each Role binds:
                                   │   • Kit (the cove recipe that fulfills it)  ← Harbor data store
                                   │   • security scope · prompt · addressing
                                   ▼
                              Actor (spider-18) = a runtime fulfilling a Role
```

- **Project** ≈ a repo/tracker-project; top of the tree; owns its Roles, a **Roster**, and an
  **Escalation policy**.
- **Role** = the old "class"; `dispatch:{handler}` names a Role; harbor maps Role → Kit. Attaches
  **per-class with per-actor overrides**.
- **Kit** now lives in harbor's data store, referenced by a Role — not committed in the repo (#1/#3).
- **Actor** = a named runtime instance of a Role.

### Role = {security scope, instructions, addressing}

Teeth vary by the fault line above: **instructions** apply only where harbor launches the process
(Worker/Manager); **security scope** and **addressing** apply to all (by construction for managed coves, by
boundary policy otherwise).

---

## Security: the central boundary broker

**Direction:** push most security to **boundary policy**, even for harbor-raised actors, by moving the egress
proxy **central** and injecting credentials there (Secretless-Broker / SVID). Precise scope:

- **Relocate, don't remove, the construction boundary.** A central proxy is only a boundary if the cove
  can't route around it; the forcing function moves from in-cove nftables → the **network fabric harbor
  wraps the cove in**. Works for harbor-raised coves; **Guests can't be pinned**.
- **Per-connector, client-addressed (no MITM CA).** The cove re-addresses harbor per service
  (`ANTHROPIC_BASE_URL`, git `insteadOf`, messaging MCP); harbor serves its **own** TLS cert, injects the
  downstream credential, re-originates upstream. No cert forgery, nothing to install. Harbor sees plaintext
  of brokered traffic (the client sent it there on purpose) — the conscious trade.
- **Two proxy functions:** a **filtering forward-proxy** (allow-listed-but-not-brokered hosts — PyPI/apt; no
  secret, no termination) and **connector-broker endpoints** (client-addressed, per service, inject creds).
- **One minimal secret stays in the cove: its identity** (mTLS/scoped token) so harbor knows which actor to
  inject for. High-value downstream creds all move to harbor. Trade: N cove-side risks → **1 crown-jewels
  harbor** — a win for multi-operator *iff* harbor is defended accordingly.
- **Internal hardening stays by construction** (non-root, sealed layer, fs/process isolation) — no boundary
  equivalent. "Boundary policy" is the model for the **network + credential axis only**.
- **Guests: carrot, not stick.** Harbor can't force a Guest's fabric; it offers the *same* connector
  endpoints + secret-rewrites (cheap opt-in: a couple of env vars, no CA). Otherwise the Guest self-manages
  secrets.

The shipped per-class egress delta (`ApplySessionEgress`) becomes central policy keyed by actor identity.

---

## Runtime: the conductor

One thin runtime for **every** managed actor (Worker *and* Manager), generalizing the shipped `at-switchboard`
loop. **Imperative messaging is split from declarative suspension:**

- **Imperative — messaging is a tool.** The in-cove agent gets a **messaging MCP** (`read`/`send`) and blends
  reading, working, and replying within a turn (glance at inbox, post progress, check for a reply, continue).
  `read` returns the merged, tagged inbox as a tool result.
- **Declarative — the exit says only *when to wake*:** `exit { wake-on: messages | ticket-event | timer(n) |
  none }` (combinable; first trigger wins; `none` → drop). The "disposition intents matrix" dissolves into
  ordinary tool calls; escalation sends and tracker writes are brokered MCP calls too.

**Token safety preserved — the messaging MCP is a harbor-brokered connector:** harbor holds the tokens, does
platform I/O, and **enforces the comms access-graph on every `send`**. Only the agent's identity is in-cove.

**Cove states:** `active` (running a turn) · `idled` (suspended, waiting) · `dropped` (exit). On **Fly**,
`idled` = a **memory-snapshot suspend** (near-zero cost, fast resume), woken on an inbound message or timer —
so a worker blocked overnight costs nothing and survives harbor restarts.

**Guardrails:** trigger-check at exit (non-empty queue re-enters immediately); per-turn wall-clock budget
(no busy-looping the cove active); read/**ack** semantics (at-least-once, mid-turn crash redelivers).

**Consequence — the conductor lives in harbor, not the cove** (a suspended machine can't poll itself). Harbor
owns the loop, channels, timers, tokens, lifecycle; the cove is per-turn compute. This *aligns* with the
broker: secrets **and** the loop both leave the cove.

---

## Escalation, roster & the comms access-graph

A "supervisor" is an **escalation target** that depends on the *kind* of block, defined at the **Project**
level:

| Block category | Traditional target |
|---|---|
| Ticket / spec understanding | team lead · ticket owner · known SME |
| Mechanical (infra / tooling) | PM · sysadmin · IT-help channel |
| Code / architecture ambiguity | system / module owner |

- **Tiered** (cheap/local first) to prevent noise — PagerDuty-style ordered tiers + per-tier timeout.
- **Project gains:** a **Roster** (humans, Manager roles, channels) and an **Escalation policy**
  (`category → ordered tiers of {actor|role|channel}`, per-tier timeout).
- **Comms access-control falls out of escalation:** an actor may address exactly the targets its escalation
  paths name (plus its own channels); harbor-the-router drops the rest. Boundary policy on the *comms* plane,
  mirroring the broker on the *network* plane.
- **Reuse hooks:** harbor-detectable categories (egress-wall denial, `401`) auto-route to the infra tier
  (no agent self-classification); judgment categories are agent-declared. Ingest **CODEOWNERS** for
  code-ambiguity routing; ingest tracker `owner`/`assignee`.
- **Triage-first:** tier-1 is a generalist that re-routes; precise routing is the system's job, not the
  worker's (coarse categories + triage beats a fine taxonomy the worker must get right).
- **Guests make poor escalation targets** (can't be woken) — treat as best-effort, never a relied-on tier.

Two clocks, independent: the worker's self-paced `wake-on: timer(n)` vs harbor's policy-owned tier-escalation
timer; an answer at any tier wakes the worker regardless.

---

## Lifecycle & workspaces

- **Worker:** trigger-raised, terminal. On a block it isn't forced to give up — it can escalate/wait/handoff
  and be **suspended**, resuming on a message or ticket event. **Abandonment TTL:** after escalation exhausts
  + max wait, harbor drops it (today's die-immediately = TTL 0).
- **Manager:** roster-declared, perpetual. `exit` means *suspend until next address*; only roster removal
  drops it. **Managers don't raise Workers — they groom the board** (brokered tracker writes), and dispatch
  raises the Worker. One dispatch path; **tracker stays the single source of truth.**
- **Workspaces = blank + self-clone** for Workers *and* Managers. The broker keeps the git token out of the
  agent even though the agent clones itself, so at-cove's reason to pre-clone disappears; agents clone
  whatever/however they need, bounded by role authz. **Obsoletes (in the harbor model):**
  clone-on-first-session, isolated/shared volumes + `share-repo-dir`, `shadow-dirs`, the chat-side
  git-with-token askpass + `at-task clone-workspace`, declarative multi-repo clone.
- **Durable-reconstructable invariant:** push branch + write context **before** suspending. Warm resume (Fly
  snapshot) keeps the workspace; **cold resume re-clones and recovers from the pushed branch + ticket.** The
  warm session is an optimization; **ticket + branch is the source of truth** — this is what makes aggressive
  idling and blank workspaces safe.

---

## Validation — three flows rode one machine

- **Flow 1 (Worker ships an issue):** webhook → claim → raise (identity only, egress pinned) → brokered work
  → escalate/handoff sub-stories (4a message, 4b ticket-transition, 4c=TTL exit) → brokered PR + tracker
  write → drop. Exercised broker, identity, conductor, suspend/resume, escalation, comms graph.
- **Flow 2 (human ↔ standing Manager):** address `#acme` → wake → converse → **groom the board** (files a
  ticket) → dispatch raises a Worker → report back on the ticket event. Defined Manager lifecycle; confirmed
  the Manager *is* today's collaborator, evolved.
- **Flow 3 (Guest):** out-of-band enrollment → opt into broker + channels → boundary policy, no
  lifecycle/wake. Reused everything; the cheapest first user of the broker.

4a ≡ 4b (both "wait for subscribed events"); human→Manager→tracker→Worker→event→Manager→human all brokered
through the one hub. The model composes.

---

## Relationship to shipped at-cove

**Reused wholesale:** the hardened cove substrate (sealed layer, egress stack, kit assembly), `at-cove work`
bracket, `at-task` git/PR worker, the switchboard conductor loop (generalized), the dispatch scheduler (made
resident), the Colima backend (for local Guests).

**Net-new:** the harbor service + data store + API/UI, the central broker (egress proxy + per-connector
credential injection + messaging MCP + comms authz + identity store), the **Fly backend** (remote compute
with suspend/resume + wake-on-trigger), the hoisted conductor, and webhook intake.

**Obsoleted in the harbor model** (kept in local at-cove until harbor replaces it): the workspace pre-clone
machinery listed above; in-cove secret injection for managed coves; the in-sandbox switchboard token model.

---

## Decomposition & sequencing

Each sub-project is its own spec → plan → build cycle. Dependency leads the first slice; value leads the rest.

| # | Sub-project | Delivers | New infra | Reuses |
|---|---|---|---|---|
| **1** | **Broker + identity/enrollment, serving Guests** *(walking skeleton)* | central secrets/egress for *local* coves; #3/#4 now | credential-injecting proxy, per-connector broker, messaging MCP, comms-authz, tiny identity store | all of at-cove; coves stay **local** — no remote compute |
| **2** | **Control plane: Projects / Roles / Kit registry** | kits leave repos; roles central; #1/#3 | data store + minimal API | kit format, assembly |
| **3** | **Fly backend + managed Worker lifecycle + resident webhook dispatcher** | durable autonomous work; #2 | remote compute, raise/drop, webhook intake | `at-cove work`, `at-task`, the scheduler (resident) |
| **4** | **Hoisted conductor + suspend/resume + Managers + escalation/roster** | standing durable teammates; the hub | wake-on-trigger runtime, Fly suspend, escalation engine | switchboard loop (generalized) |
| **5** | **UI + observability** (live roster, audit, enrollment self-service) | multi-operator polish; #1 | web UI | the API from #2 |

**Walking skeleton = #1 (broker serving Guests):** smallest new surface, validates the hardest
security-critical piece first, immediate value (secrets off every laptop), no remote-compute lift, and Guests
need none of harbor's hardest machinery (lifecycle/wake). **#3 is the largest single bite** (remote compute +
lifecycle + webhooks) and may want splitting at its own spec stage.

---

## Open questions (carried forward)

- Per-actor role overrides: which fields are overridable, and who may set them?
- Identity issuance/rotation details; the **identity-broker opportunity** (per-actor external identities:
  git author/email, tracker user — unlocks PR attribution "AT-Harbor on behalf of spider-18" and
  "assign ticket to worker").
- Where the messaging MCP endpoint lives (in-cove shim vs remote `harbor/messages`) — lean remote.
- Warm-resume vs cold-re-dispatch policy (by expected wait / cost).
- Map of tracker states → resume semantics (go/stop); abandonment TTL value + ticket disposition.
- Guest enrollment scope/TTL; External-human enrollment (deferred; non-roster posters ignored for now).
- Harbor's own compromise/threat model — now the crown jewels (routable-backend TOFU concerns from the
  at-cove OVERVIEW apply directly to the Fly backend).
