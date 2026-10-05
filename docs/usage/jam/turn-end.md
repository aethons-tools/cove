---
summary: What happens when a managed session's turn ends — it waits for wake conditions or ends — covering `end(reason)`, alarms (`alarm_set`/`alarm_clear`/`alarm_list`, one-shot or cron in the role's time zone), the role's idle timeout and the agent's `idle_timeout` override, the `holding` activity, and the reasons a Wake carries into the agent's resume prompt.
read_when: You want a session to end itself, to be woken at a time or on a schedule, or to be woken (or torn down) after sitting idle; or you need to know why a studio shows `holding`, what a woken agent is told about why it woke, or how Jam decides a session whose turn ended may be woken, paused, or torn down.
owns: the turn-end model, the `end`, `idle_timeout` and `alarm_*` tools and their `/end`, `/idle`, `/alarms` endpoints, the alarm limits and firing rules, the role's turn-end policy (`--idle-timeout`/`--on-idle`/`--time-zone` semantics), the `holding` activity (its wake/pause/teardown semantics and the WaitSeq rule), and wake reasons (kinds, rendering, the legacy fallback). Does NOT own reply detection, wait-max, or warm-timeout pausing — see intercom.md.
prereqs: coves.md for Phase vs Activity; intercom.md for the wake-on engine and how a reply is detected
tier: leaf
updated: 2026-10-05
---

# Turn end

When a managed session's turn ends, it either **ends** (it called `end`) or
**waits** for something worth another turn — an inbound squawk
([intercom.md](intercom.md#waiting-for-a-reply-wake-on)), one of its
**alarms**, or its **idle timeout** — and every Wake tells the agent **why** it
was woken. Alarm gates and `report` are designed in the
[turn-end spec](../../superpowers/specs/2026-10-05-turn-end-lifecycle-design.md)
and land in later slices.

## Ending a session

`end(reason)` (an intercom tool, `POST /end`) asks Jam to end the session for
good. Any session kind may call it — e.g. a personal session told to "wrap it
up" does its housekeeping, then calls `end` as its last action.

- It takes effect at **turn end**: Jam tears the session down once it reports
  `waiting`. A `holding` session first finishes its background tasks (bounded by
  `BackgroundWait`).
- From the moment it is requested, the session is **never woken again** — not by
  a squawk, nor by its idle timeout. The first request's reason wins.
- A personal session's owner gets a squawk: *ended itself: <reason>*. A standing
  or ticket session has no owner: the end is logged. (Moving a ticket's state on
  `end` arrives with `report`.)

## Alarms

An agent sets named alarms that wake it after its turn ends — e.g. a custodian
with several duties at different times, or "check the PR every 5 minutes".

| Tool | Endpoint | |
|------|----------|---|
| `alarm_set(name, schedule, note?)` | `PUT /alarms/{name}` | set, or replace the same name |
| `alarm_clear(name)` | `DELETE /alarms/{name}` | `404` if absent |
| `alarm_list()` | `GET /alarms` | name, schedule, note, next fire time, whether fired |

- **`schedule`** is an RFC 3339 time (fires once; future, within 366 days) or a
  5-field cron expression or descriptor (`*/5 * * * *`, `@daily`), evaluated in
  the role's time zone (`role add --time-zone`, IANA name, default UTC). A
  recurring alarm may fire at most once a minute (`@every 10s` and 6-field cron
  are refused). **`note`** (≤ 1000 bytes) is what the wake tells the agent.
- **Limits:** 20 alarms per session; names are lowercase letters, digits, `-`
  and `_` (≤ 64). Anything invalid is a `400` and nothing is stored.
- **When they fire:** only once the session's turn is over (`holding` or
  `waiting`); an alarm due while it is `running` waits for the turn to end. A
  paused session is resumed first and woken on a later tick. No catch-up: a cron
  alarm whose matches were missed (e.g. Jam was down) fires once.
- **Delivery:** a fired alarm stays pending — its wake is re-sent each wake-on
  tick — until the session next runs; then a one-shot alarm is removed and a cron
  alarm waits for its next match. One wake carries a pending reply and every
  fired alarm together, and an alarm wake pre-empts the idle timeout.

## Idle timeout

A role can give its sessions an idle timeout: `at-jam role add --idle-timeout D
--on-idle wake|teardown` ([roster.md](roster.md#roles) has the flag reference).

- It is **armed when a turn ends** (the session leaves `running` for `holding` or
  `waiting`): the deadline is that moment plus the agent's override, or else the
  role's timeout. No timeout → nothing armed. A role edit applies from the next
  turn end.
- The agent overrides it with `idle_timeout(duration | "off", scope)` (`PUT
  /idle`): `scope: next` applies to the next turn end only, `always` until the
  session ends. Durations go up to 720h.
- When the deadline passes with nothing else having woken the session:
  - **`wake`** (the default) wakes it with the `idle` reason — a paused session is
    resumed first and woken on a later tick;
  - **`teardown`** ends a `waiting` session (never a `holding` one: it waits until
    the hold is over).
- It stays armed until the session next **runs** — whatever woke it — so an
  idle wake that could not be delivered is re-sent each wake-on tick, and a
  reply pending on the same tick wakes it as a squawk instead (never both).
- [`wait-max`](intercom.md#waiting-for-a-reply-wake-on) still tears down a
  non-resident session, but **only when no idle deadline is armed** (no role
  timeout, or the agent turned it `off`).

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
