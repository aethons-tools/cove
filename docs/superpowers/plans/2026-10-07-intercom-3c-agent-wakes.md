# intercom 3c: sessions wake sessions — plan

**Spec:** [`2026-10-07-intercom-slice3-session-channels-design.md`](../specs/2026-10-07-intercom-slice3-session-channels-design.md) §5 and §9 Q3. It builds on 3a and 3b.
**Ships as:** one PR. Nothing is stored: the breaker is derived from the log.

## Tasks (TDD)

1. **`wakeon.Engine.SetSessionWakes(history, breaker, limit)`**
   - Another session's delivery wakes a waiting session, unless its channel's last `limit+1` squawks (the delivery included) all came from sessions.
   - In that case the breaker posts one notice. Its id is `breaker:<channel>:<seq of the last person's post>`, so a repeat is a no-op.
   - Without `SetSessionWakes`, wake-on behaves as before: session posts never wake.
2. **Jam's notices are for people.** `Notify` gives an id-less notice a `notice:` id. A session-authored `nag:`, `notice:` or `breaker:` squawk never wakes another session (`jam.IsNotice`), while a call-in notice (`callin:`) does.
3. **`jam.Intercom.BreakerNotice`** posts trusted into a live channel; a duplicate id or a channel that is gone is no error. Like call-in notices, the breaker's notice is never rendered onto a ticket's issue or a room's surface (`IsLocalNotice`).
4. **Serve** wires it in with `sessionWakeLimit = 8`. Making the limit configurable is a follow-up.
5. **Docs:** `intercom.md` covers the wake trigger and the breaker.

## Verification

`go test ./...`; `-tags integration`; lint; docs audit; review before merge.
