# intercom 3c: sessions wake sessions — plan

**Spec:** [`2026-10-07-intercom-slice3-session-channels-design.md`](../specs/2026-10-07-intercom-slice3-session-channels-design.md) §5 and §9 Q3. It builds on 3a and 3b.
**Ships as:** one PR. Nothing is stored: the breaker is derived from the log.

## Tasks (TDD)

1. **`wakeon.Engine.SetSessionWakes(history, breaker, limit)`**
   - Another session's delivery wakes a waiting session, unless its channel's last `limit+1` squawks (the delivery included) all came from sessions.
   - In that case the breaker posts one notice. Its id is `breaker:<channel>:<seq of the last person's post>`, so a repeat is a no-op.
   - Without `SetSessionWakes`, wake-on behaves as before: session posts never wake.
2. **Jam's notices are for people.** `Notify` gives an id-less notice a `notice:` id. A session-authored `nag:`, `notice:` or `breaker:` squawk never wakes another session (`jam.IsNotice`), while a call-in notice (`callin:`) does.
3. **`jam.Intercom.BreakerNotice`** posts trusted into a live channel; a duplicate id or a channel that is gone is no error.
4. **Serve** wires it in with `sessionWakeLimit = 8`. Making the limit configurable is a follow-up.
5. **Docs:** `intercom.md` covers the wake trigger and the breaker.

## Review decisions (built)

- Each session delivery's verdict is cached by seq. It can't change, because every earlier seq is settled. So a suppressed delivery is checked against the log once, not on every tick, and its notice is offered once.
- Unlike a call-in notice, the breaker's notice goes to every surface, including a ticket's Linear issue. It asks whoever follows the conversation to reply.
- Accepted: the notice is posted as the session that wasn't woken. A run longer than 500 messages, where nobody spoke within the scan, could get a second notice.

## Verification

`go test ./...`; `-tags integration`; lint; docs audit; review before merge.
