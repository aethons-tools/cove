# intercom slice 4: escalation as call-in — design

**Status:** approved (2026-10-07); §8 questions decided as recommended.
**Parent:** [`2026-10-06-intercom-identity-and-channels-design.md`](2026-10-06-intercom-identity-and-channels-design.md), slice 4 of 4. It builds on slice 3 ([`…-slice3-session-channels-design.md`](2026-10-07-intercom-slice3-session-channels-design.md)): home channels, call-in, and sessions waking sessions.
**Today's engine:** [`docs/usage/jam/escalation.md`](../../usage/jam/escalation.md); the original designs are [`2026-09-14-harbor-escalation.md`](2026-09-14-harbor-escalation.md) and [`…-escalation-categories.md`](2026-09-14-harbor-escalation-categories.md).
**Delivers:**
- escalation tiers **call people into the session's home channel**, through the intercom, instead of a Linear comment posted beside the log;
- escalation for every kind of session, not just ticket sessions;
- an engine that no longer needs the Requisitioner.

**Unchanged:**
- the policy model (default and category chains, ordered tiers, per-tier timeouts);
- the operator commands;
- the "two independent clocks" split with wake-on.

## 1. What changes, in one paragraph

Today, while a ticket session is Waiting with a `needs-input` report, the escalation engine @-mentions each tier's people in a comment it posts straight to the session's Linear issue. The comment never enters the channel log. A session without a ticket is skipped, and a Jam without a Requisitioner has no escalation.

After this slice, each tier is a **call-in**:
- the tier's people join the session's home channel;
- an **escalation notice** is posted there, saying they were called in and why;
- the relays render that notice on every surface the channel has, so people are reached however they follow it.

Replies come back like any other reply in that channel, and wake the session as today. Escalation now works for standing, personal and manual sessions too, and on a Jam that only chats over Discord or `/me`.

## 2. When an escalation opens

An escalation opens when a session is **Waiting** and asks for a person:
- a **ticket session** asks with a `needs-input` report, as today;
- **any session** asks with the `escalate(category)` tool. The tool now does two things: it records the category, and it **asks for a person** until the session is next woken.

A new field, `Instance.EscalationAsked`, records the ask. It is set by `escalate` and cleared when the session is woken or starts a new run.

Today's tier state is unchanged: `EscalationTier` and `TierPingedAt` reset on each fresh Waiting entry. A session that asked to end is still skipped.

## 3. A tier is a call-in

When tier *n* is due, the engine does three things:
1. **Resolves the targets** to live members of the session's project. `user:<name|usr_id>` works as today, and `human:` is read as `user:`. Other target kinds are skipped with a warning, as today.
2. **Calls them in** to the session's **home channel**: its ticket channel, or its session channel. This is Jam acting on operator policy, so no addressing ceiling applies; that matches today's rule that escalation is not a session's `send`. A person who is already a member stays one.
3. **Posts the escalation notice** into that channel, as the session, with an id of the form `escalate:<session>:<tier>:<nanos>`. The text reads: "*Needs input* — called in *alice, bob* (escalation tier *n*, *category*)." Every target of the tier is listed, including people who were already members, so the notice names who is being asked. The notice is posted even when every target was already a member: it is the nudge.

Everything else works as it does today:
- A tier whose targets resolve to nobody posts nothing but still advances, so a broken tier can't wedge the session.
- A failure to post is retried on the next tick, without advancing.
- The engine never reads replies, wakes a session or tears one down.

## 4. Rendering: everywhere the channel goes

The escalation notice is a normal post in the channel, with one addition in the relays, so it reaches people on every surface:

| Home channel | Rendered |
|---|---|
| ticket channel | a comment on the issue that **@-mentions the tier's people by their tracker handles** (as today's comment does), and each called-in person's Discord inbox when the project chats over Discord |
| session channel | each member's Discord inbox, as any post there; a Linear-only person gets it in `/me` (§8 Q3) |

Some things don't change:
- A notice is for people. It never wakes another session (slice 3's `IsNotice`).
- A reply from a person wakes the waiting session, as any reply does.

## 5. The engine without the Requisitioner

The engine now posts through the intercom, and the relays deliver. So:
- `escalate.Pinger` (`IssueByIdentifier`/`PostComment`) is replaced by a narrow `Caller`: `Escalate(inst, tier, targets, category) error`, implemented by `jam.Intercom`.
- Serve starts the engine whenever the intercom runs, with or without a Requisitioner.
- `runtime.requisitioner.escalation-poll-interval` stays where it is, for compatibility, and a top-level `runtime.escalation-poll-interval` is added. The top-level key wins if both are set.

## 6. Config, data and upgrade

- **Data:** the only schema change is the `Instance.EscalationAsked` doc field. Policies already store user ids (slice 1a-3d).
- **Upgrade:** an escalation already open at the upgrade (its `TierPingedAt` set) continues from its tier. The next tier is a call-in.
- **Rollback:** an older Jam ignores `EscalationAsked` and the `escalate:` notices, which are plain squawks to it.

## 7. Plans (tentative; the plan doc decides)

- **4a — call-in escalation.**
  - `Intercom.Escalate`, the engine port, `EscalationAsked` (set by the `escalate` tool and cleared on wake), and the relay rendering of `escalate:` notices.
  - Serve without the Requisitioner, and the config key.
  - Docs: `escalation.md` is rewritten around call-in, and `intercom.md` and `comms-addressing.md` drop their "not yet" lines.
- It fits one PR. A 4b exists only if §8 Q4 is taken.

## 8. Decided in review (2026-10-07): each as recommended

1. **Who escalates.**
   - Rec.: **every kind of session**. A ticket session asks by reporting `needs-input` or by calling `escalate`; any other session asks by calling `escalate`.
   - Alternative: keep ticket sessions only. Then standing and personal sessions keep waiting passively, and their only way to reach people stays their home channel.
2. **Do called-in people stay?**
   - Rec.: **they stay members** after the session gets its answer. They are now in the conversation, can see what follows, and can leave (`/me` Leave).
   - Alternative: drop them when the escalation resolves. That's tidier, but the person loses the thread they were just asked into.
3. **Linear-only people and a session without a ticket.**
   - Rec.: they're reached in **`/me`** only. A non-ticket session's channel has no tracker surface, and inventing one (an issue per session) is out of scope.
   - Alternative: refuse a tier whose targets can't be reached anywhere but `/me`. That's stricter, but an escalation then dead-ends silently.
4. **Session and room targets in tiers** (call another agent in, or post into a room).
   - Rec.: **not in this slice**. Keep tiers to people, and log other kinds as today. It is a small follow-up (4b) once someone wants an on-call agent.
5. **Personal sessions.**
   - Rec.: **escalate them too**, under the same opt-in policy. A personal session's starter is already in its channel, so tier 0 is usually that person plus whoever the policy adds.
   - Alternative: never escalate personal sessions. The idle ladder (nags, then reclaim) already chases the starter.

## 9. Testing

- Engine (hermetic):
  - opening rules: `needs-input` versus `EscalationAsked`, with every session kind;
  - tier timing, unchanged from today's tests;
  - an empty tier advances;
  - a failed post doesn't advance.
- `Intercom.Escalate`:
  - joins the targets to the home channel;
  - posts the notice once per tier, as a notice that doesn't wake other sessions;
  - members who are already in stay in.
- Relays:
  - an escalation notice on a ticket channel renders as a Linear comment with the tier's handles, and in the called-in people's Discord inboxes;
  - on a session channel it renders like any post.
- Serve wiring: escalation runs without a Requisitioner.

## 10. Docs updated with the change

- `escalation.md`: rewritten around call-in. It stops being a ticket-only story, and its "Not yet" list is updated.
- `intercom.md`: the `escalate` tool asks for a person.
- `comms-addressing.md`: "Escalation as call-in" is no longer listed under "not yet".
- `serve.md`: the poll-interval key.
- `personal-sessions.md` and `standing-sessions.md`: they can escalate.
- The `INDEX.md` row for `escalation.md`.
