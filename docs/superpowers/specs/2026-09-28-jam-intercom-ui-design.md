# jam: the participant intercom UI — a native human surface over the Log

**Status:** design approved (brainstorm complete), pre-plan
**Scope:** a bespoke, participant-facing web intercom in `at-jam serve`, over the existing squawk Log — with Linear/Discord demoted from bidirectional relays to one-way notifiers.
**Builds on:** `internal/msglog` (the Log), `internal/msgport` (per-service egress/ingress engines), `/squawks` broker + `cove-master mcp` (the agent surface), wake-on (`runtime.wake`), the comms access-graph (`comms-addressing.md`), the roster, and the existing admin `/ui/` (server-rendered, htmx).
**Does not change:** the Log substrate, the `/squawks` agent tools, wake-on semantics, agent-side authorization, or the hardening/egress boundary. This design is **additive**.

## Summary

Agents already reach the durable squawk Log through the `/squawks` broker (MCP `read`/`send`/`commit`). Humans reach the same conversations only through **borrowed surfaces** — Linear `@`-mentions and Discord reply-to-message — and we've hit their ceiling: Discord can route only a *reply* (bare posts drop), needs an unpruned receipt store and fragile author-id/display-name attribution; Linear needs feed-polling against a `$since`/schema shape we can't even test in the egress-locked build. Neither can natively show *which* studio, *which* ticket, or *who is Waiting on you right now*.

This design adds the **first native human surface over the Log**: a participant-facing intercom in `at-jam serve` where roster humans read and reply. A human reply is *just another writer to the Log*, so **wake-on resumes the waiting studio identically to a Discord/Linear reply today** — no new resume path. Linear/Discord become **one-way egress notifiers** ("new message → deep-link"); their inbound routing is **parked behind a config flag** (kept, not deleted). This is the human analog of what `/squawks` did for agents:

| Actor | Reaches the Log via | Reads | Writes |
|---|---|---|---|
| **Agent** (today) | `cove-master mcp` → `/squawks` | `read`/`commit` (durable queue) | `send` → append |
| **Human** (this design) | Browser → new participant routes in `at-jam serve` | channel view (Log projection) | reply / new message → **the same append path** |
| **Linear / Discord** (after) | `msgport` engine | *(inbound parked behind a flag)* | egress **notification only** |

## Decisions (from brainstorm)

- **Audience: roster humans as participants.** Each human logs in as *themselves*; the UI shows *their* channels. Not an operator console — that stays the existing `/ui/*`.
- **Reply reach: UI is the only reply surface (for now).** Relays become one-way notify + deep-link. The inbound relay engines (Discord reply loop, receipts, attribution) are **parked behind a config flag**, not removed — relay-reply stays reopenable (later slice).
- **Auth: OIDC first, tiered later.** Slice 1 maps an OIDC subject → roster actor. Build the auth layer as a **pluggable principal resolver** so a capability-link resolver drops in later with no handler changes.
- **Comms model: everything is a channel** (see §2). Recipients are **humans**, **sessions**, **studios**, and **named channels**. A **DM** is a channel between the sender and a session (or another human); a **studio** has its own channel with the session as a participant — so you can address either `Spider-18` (the session → a DM) or `Spider-18's Studio` (the studio channel). Channel membership is **emergent**: anyone who has sent or received on a channel is a member and is notified of *all* activity on it.
- **Addressing is open to start.** Any authenticated participant may message **any active recipient**; the UI's **New Message** lists all active recipients. The comms access-graph gating is the *eventual* authorization but is **deferred** — not applied in the starting model. This openness is scoped to the **human UI**; agent-side `/squawks` sends keep today's access-graph.
- **Cursors are per (participant, channel).** The read/unread position is keyed by participant × channel, **not** by service. The UI is an *in-process* Log reader/writer, not a `msgport` Service engine, so it carries **no `EgressMark`**.
- **Rail grouping: by attention, with a project-grouping toggle later.** Ship `Waiting on you → Active → Channels` as the default; the view-model also supports project grouping (added later).
- **Realtime: SSE (Go stdlib), not Centrifugo or Melody.** Centrifugo is a standalone server — a new service, port, and trust surface against the single-hardened-binary + sealed-egress model, and a redundant broker in front of the Log. Melody (WebSocket) is bidirectional; our client→server path is already authenticated POSTs. SSE fits server→client push exactly, sails through squid with no upgrade, and `EventSource` gives auto-reconnect + `Last-Event-ID` resume mapping 1:1 onto the Log's monotonic `seq`. **One multiplexed SSE stream per session**, filtered to the viewer's channels. Phasing: **slice 1 on 3s htmx poll, SSE in slice 2**.
- **Design language:** IBM Plex Sans + Plex Mono (mono for session/ticket ids), a jam-berry accent, amber reserved strictly for the waiting/attention semantic. Reference mockup: the two-pane view approved in brainstorm.

## 1. Identity & session (`at-jam serve`)

A **participant session** distinct from the operator session, reusing the OIDC Authorization-Code+PKCE flow the admin UI already has, but resolving to a roster actor rather than an operator scope.

- **Principal resolver interface.** `ParticipantResolver: (*http.Request) → (actor RosterRef, err)`. Slice 1 ships `oidcResolver` (session cookie / OIDC subject → roster actor). A later `capabilityLinkResolver` (signed, operator-issued, revocable) implements the same interface — handlers don't change.
- **Roster identity binding.** Add `Identity []{Issuer, Subject}` to the roster `Human` (additive JSON, both backends, conformance test, deep-copied in `copyHumans`/`GetRoster` like `Delivery`). Set via `at-jam project roster add-human --oidc <issuer>:<subject>` — **one-at-a-time** for now (bulk enroll later). Unmapped subject → **no access (fail-closed)**, never auto-provisioned.
- **Authorization is open to start** (see Decisions): once a subject resolves to a roster actor, that participant may address any active recipient. The comms access-graph is the intended eventual gate but is not applied to human-UI sends yet. Agent `/squawks` sends are unchanged (still access-graph-gated).
- **Route + listener boundary (security-critical), settled:** participant routes live on the **same admin listener** as a **separate route tree** under a distinct `/me/*` prefix with its own gate. A participant session **never** carries operator scope and **cannot** reach the roster/kit/destination *mutation* routes under operator `/ui/*`. Loopback keeps the operator god-view.
- **Session cookie** as today (the access identity; HttpOnly + Secure + SameSite=Lax).

## 2. The channel model & read model (projection over the Log)

**Everything is a channel.** A channel is an addressable conversation identified by a stable **channel id**; the Log's `{from, to[], body, seq}` entries project onto channels. No new message storage — channels and membership are a **projection** over the Log (plus the live `Instance` state for presence).

- **Recipient taxonomy (what New Message can target):**
  - **human** — a roster human; addressing one opens/uses a DM channel `{you, human}`.
  - **session** — a specific running agent instance (e.g. `Spider-18`); addressing one is a **DM** channel `{you, session}`.
  - **studio** — a studio (e.g. `Spider-18's Studio`); its **studio channel** has the session as a participant, plus any humans who join. This is the agent's default conversation (its `send` already defaults to "its own ticket").
  - **named channel** — a group channel (`#eng-comms`).
- **Membership is emergent.** A participant is a member of every channel they have **sent or received** on, and is **notified of all activity** on those channels. A participant's **inbox = the channels they are a member of.**
- **New Message → an active-recipients directory.** A UI endpoint lists **all active recipients** (active humans, sessions, studios, named channels) so a participant can start a conversation with anyone. Addressing is open to start (see §1).
- **Presence / "Waiting on you"** is derived from the studio's live state: `Instance` phase (`waiting`/`idled`) + wake-on baseline (`WaitSeq`). The attention group = studio channels of studios that are `waiting`/`idled`. `idled` renders as *paused — a reply unpauses it* (the real `warm-timeout`→`docker pause` lifecycle). Same `Instance` state `/ui/coves` already reads.
- **Cursors: per (participant, channel).** Each member's read/unread position in a channel is a monotonic last-seen `seq`, stored on the Jam store keyed by `(participant, channel)` (additive to both backends + conformance test). Not per service; the UI has no `msgport` `EgressMark`.
- **Ordering/paging** by append `seq` via `msglog` seq-cursored bounded reads.
- **View-model supports both groupings.** Channels are tagged `{kind, project, phase, waiting, unread, lastSeq}`; the default view groups by attention, a later toggle groups by project — same data, two arrangements.

## 3. Send path (new message & reply)

Browser `POST` (participant route) → resolve actor via `ParticipantResolver` → resolve the target recipient to a **channel id** → **append to the Log** with `from = actor`, `to = [<recipient ref>]` (identity from the token, never the body). Returns `204`. To start, any active recipient is allowed (no access-graph check yet).

- **Reuses the `send` append + `Directory` resolve** — the participant send is the human analog of the agent's `send` tool. New message and reply are the same path (reply just targets an existing channel).
- **Wake-on resumes the studio** for any external-origin append addressed to a session with `seq > WaitSeq` — whether it lands in the **studio channel** or a **session DM**; if `idled`, it unpauses first. This is why the inbound relay engines become redundant for the primary flow.
- Errors mirror `/squawks`: no intercom-log → `503`; append failure → `502`.

## 4. Realtime (slice 2)

- **SSE, one multiplexed stream per session**: `GET /me/stream` holds a `text/event-stream`; the server fans **new Log appends** to subscribers, **filtered to each viewer's channels** (the membership filter is the work no library removes). Events carry the append `seq` as the SSE `id`.
- **Reconnect is gap-free:** `EventSource` resends `Last-Event-ID`; the server replays appends with `seq >` that id. Under the `:443`/TLS mux (HTTP/2) the per-domain connection cap is a non-issue; one stream per session keeps HTTP/1.1 safe too.
- Slice 1 uses the existing **3s htmx poll** (no new transport) to prove the spine; SSE replaces the poll in slice 2 without changing the read model or send path.

## 5. Relay demotion (slice 3)

- **Outbound** (Discord/Linear `msgport` egress engines): when delivering a squawk addressed to a human with a delivery profile, emit a **notification + deep-link** to the UI channel (`<jam-ui-base>/me/<channel>`, where `jam-ui-base` is set in the **serve config**). Keep the body text for convenience; the *reply affordance* points to the UI. Per-message granularity now; coalescing/digest later.
- **Inbound** (Discord reply loop + receipts + attribution; Linear comments ingress): **parked behind a config flag** (e.g. `runtime.<service>.inbound: false`, default off once the UI ships). Code stays; path off. Re-enabling it is the "relay-reply" revisit.
- **Wake-on is unaffected** — it wakes on any external append to the Log regardless of which writer produced it.

## 6. Non-goals (YAGNI)

- Relay-reply (answering from Discord/Linear) — parked, revisited only if missed.
- Capability-link auth — interface is built for it; the resolver is a later slice.
- Access-graph gating of human-UI sends — deferred (open addressing to start); the graph still gates agent `/squawks` sends.
- Presence/typing indicators, message editing/deletion, whole-Log search (the operator `/ui/intercom` audit view already filters the Log), notification coalescing/digest, project-grouping toggle UI (view-model supports it; UI is later).

## 7. Phasing

1. **Spine (prove it end-to-end):** OIDC→actor mapping + roster identity binding · the channel read model with attention grouping (htmx poll) · New Message + active-recipients directory · send/reply-into-Log → wake-on resumes · per-(participant, channel) unread cursor. **Relays untouched this slice** (still deliver + still accept replies) — purely additive, zero risk to Discord/Linear.
2. **SSE live tail + presence:** multiplexed SSE stream, `Last-Event-ID` replay, live "Waiting on you"/phase updates.
3. **Relay demotion:** outbound notice + deep-link · inbound parked behind flag.
4. **Later:** capability-link auth tier · access-graph gating of human sends · project-grouping toggle · notification coalescing · relay-reply revisit.

## 8. Testing (preserve the plan/execution split)

- **Pure, hermetic, table-driven:** the channel projection (Log + roster + `Instance` phase → channels/membership/grouping/unread) and the recipient/channel resolution are pure functions over an `msglog` fake + a roster fixture. No Docker/network/VM.
- **Handlers & SSE** via the existing `runner.Fake` / httptest pattern; the SSE broadcaster's membership filter and `Last-Event-ID` replay get unit coverage. Any real-ssh/live piece stays behind the `integration` build tag.
- **TDD:** failing test first, per repo convention.

## 9. Docs routing (part of the change, not after it)

`docs/usage/jam/ui.md` owns the `/ui` surface (routes, auth, exposure) — extend it for the participant routes + listener/scope boundary. `intercom.md` gets a short pointer that humans now have a browser send/reply surface (single source: the mechanics stay where they live). The participant UI is substantial enough to warrant a **new leaf `docs/usage/jam/intercom-ui.md`** (owned by the UI, linked from both `ui.md` and `intercom.md`, added to `INDEX.md` in the same change) — settled at authoring time via the docs-author skill.

## 10. Settled open questions & residual confirms

**Settled in brainstorm:**

1. **Listener:** same admin listener, distinct `/me/*` route tree + gate (§1).
2. **Membership / comms model:** the channel model of §2 (everything is a channel; emergent membership; open addressing to start; New Message directory of all active recipients).
3. **Deep-link base URL:** `jam-ui-base` in the serve config (§5).
4. **Cursors:** per (participant, channel); the UI is not a `msgport` engine and has no `EgressMark` (§2).
5. **OIDC binding provisioning:** one-at-a-time via CLI for now; bulk enroll later (§1).

**Confirmed (starting behavior):**

- (a) Open "anyone → anyone" addressing is scoped to the **human UI**; agent `/squawks` sends keep today's access-graph.
- (b) A Waiting studio's *question* posts to its **studio channel** by default (the session DM is a private side-channel), and wake-on fires on any external append addressed to the session in either.

> **These are defaults, not fixed routing.** How a session chooses *where* and *whom* to message is ultimately a **prompting** concern — the session prompt governs it. The routing above is sensible starting behavior; we expect to refine it as we learn, without a schema change (the channel model already carries every target the prompt might choose).

**Residual confirm (planning):**

- **Recipient/ref data model:** the Log `to[]` must distinguish a **session** ref (DM to `Spider-18`) from a **studio** ref (the studio channel). Confirm the ref taxonomy and how a DM channel id is derived (deterministic from `{participant, session}`).

---

*Reference mockup (brainstorm): two-pane participant intercom — attention-grouped rail, layered "Waiting on you" treatment, wake-on tie-in in the composer. IBM Plex Sans/Mono, jam-berry accent, amber = waiting semantic.*
