# intercom 4a: escalation as call-in — plan

**Spec:** [`2026-10-07-intercom-slice4-escalation-call-in-design.md`](../specs/2026-10-07-intercom-slice4-escalation-call-in-design.md); its §8 questions were decided as recommended.
**Ships as:** one PR. The only data change is the `Instance.EscalationAsked` doc field.

## Tasks (TDD)

1. **`Instance.EscalationAsked`**
   - `Supervisor.SetEscalationCategory`, which backs the `escalate` tool, sets it.
   - It is cleared whenever a turn starts.
2. **The engine** (`internal/escalate`)
   - An escalation opens for a session that is Waiting and either has a `needs-input` report or `EscalationAsked` set. Any kind of session qualifies, ticket or not.
   - Tiers resolve to `jam.Member`s.
   - `Pinger` is replaced by `Caller.Escalate(ctx, inst, tier, category, members)`.
   - Unchanged:
     - a tier that resolves to nobody still advances;
     - a failed call is retried on the next tick without advancing.
3. **`jam.Intercom.Escalate`**
   - Joins the members to the home channel.
   - Posts the notice as the session. Its id has the form `escalate:<session>:<tier>:<nanos>:<usr,…>` (`EscalationNoticeTargets` reads it back), and the notice text names everyone asked.
   - It is a notice (`IsNotice`), so it wakes no other session, and it is not local, so it goes to every surface.
4. **Relays**
   - On a ticket or room channel, an escalation notice's Linear delivery is prefixed with the tier's `@`-handles.
   - In a Discord-chat project, the notice is also delivered to each called-in person's Discord inbox.
5. **Serve**
   - The engine runs whenever Jam does, with no Requisitioner needed.
   - The new `runtime.escalation-poll-interval` key wins over `runtime.requisitioner.escalation-poll-interval`.
   - `linearCommenter` is removed.
6. **The `escalate` tool and the docs**
   - The `escalate` tool's description is updated.
   - Docs: rewrite `escalation.md`, and update `intercom.md`, `comms-addressing.md`, `personal-sessions.md`, `requisitioner.md`, `serve.md`, and `INDEX.md`.

## Verification

`go test ./...`; `-tags integration`; lint; docs audit; review before merge.
