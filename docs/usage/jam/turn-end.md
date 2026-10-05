---
summary: What happens when a managed session's turn ends — it waits for wake conditions (or ends) — covering the `holding` activity for a turn that ended with background tasks still running, and the reasons a Wake carries into the agent's resume prompt.
read_when: You need to know why a studio shows `holding`, what a woken agent is told about why it woke, or how Jam decides a session whose turn ended may be woken, paused, or torn down.
owns: the turn-end model, the `holding` activity (its wake/pause/teardown semantics and the WaitSeq rule), and wake reasons (kinds, rendering, the legacy fallback). Does NOT own reply detection, wait-max, or warm-timeout pausing — see intercom.md.
prereqs: coves.md for Phase vs Activity; intercom.md for the wake-on engine and how a reply is detected
tier: leaf
updated: 2026-10-05
---

# Turn end: holding and wake reasons

When a managed session's turn ends, it **waits** for something worth another
turn — today an inbound squawk ([intercom.md](intercom.md#waiting-for-a-reply-wake-on)) —
and every Wake now tells the agent **why** it was woken. Alarms, gates, an idle
timeout, `end` and `report` are designed in the
[turn-end spec](../../superpowers/specs/2026-10-05-turn-end-lifecycle-design.md)
and land in later slices.

## Holding

`holding` is the Activity a studio reports when its agent's **turn has ended
but background tasks are still running** — cove-master keeps the episode open
(up to `BackgroundWait`, 30m by default) so those tasks can finish.

| | `running` | `holding` | `waiting` |
|---|---|---|---|
| Woken by a squawk | yes, between turns | yes, at once | yes |
| Paused at `warm-timeout` | no | no | yes |
| Torn down at `wait-max` | no | no | yes (non-resident) |

- A holding studio is never paused: `docker pause` would freeze its background work.
- `holding → running` (a delivered Wake, or a background task's completion starting
  a turn) is the **same run resuming**: Jam keeps the wake baseline (`WaitSeq`), so a
  reply that landed while holding still wakes the agent. Only a run that starts from
  `waiting` (or a raise) re-baselines.
- When the episode finally ends, the studio reports `waiting` as before.
- In `/me`, a holding session reads "is working in the background".

## Wake reasons

A Wake carries a list of reasons; the agent's resume prompt names them.

| Kind | Rendered as |
|------|-------------|
| `squawk` | the session kind's usual "a message may have arrived — `read`…" prompt |
| `alarm` | `Alarm "<name>" fired: <note>` (plus the gate's output, when gated) |
| `gate-failed` | `Alarm "<name>" gate could not run: <detail>` |
| `idle` | `Idle timeout: no other wake arrived.` |
| `context-changed` | nothing extra: the context-changed notice is appended separately |
| anything else | `Woken (<kind>): <detail>` |

- Wakes that arrive mid-turn coalesce into one resume prompt that carries **every**
  distinct reason; none is dropped.
- When no `squawk` is among the reasons, the prompt ends with `Continue.` instead of
  the read-your-inbox prompt.
- A Wake with **no** reasons (from an older Jam) gets the session kind's usual prompt,
  unchanged. Today Jam only sends `squawk`.
