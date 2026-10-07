# intercom 3a: session channels and home channels — plan

**Spec:** [`2026-10-07-intercom-slice3-session-channels-design.md`](../specs/2026-10-07-intercom-slice3-session-channels-design.md) §2, §4 (`session:`), §6, §7 (New Message → a session), §8. Call-in, join and leave are 3b; session↔session wakes are 3c.
**Ships as:** one PR. No schema change: `session` is a new value of `channels.kind`.

## Tasks (TDD: each test first)

1. **The `session` source** (`internal/jam/intercom_post.go`, `channel_registry.go`).
   - `SourceSession` goes into `sourceKinds`.
   - `sessionSource`:
     - `CanSee`: members; plus the project's live users unless the session is personal.
     - `CanPost`: `allowed`, or `CanSee`.
     - A personal session's channel is invite-only for people.
   - `ic.sessionChannel(inst, create)` is keyed by the session id and labelled `sessionLabel(inst)`.
2. **Home channels.**
   - `HomeChannel(inst)` replaces `DefaultChannel`:
     - a session with a `Unit` and a tracker gets its ticket channel;
     - otherwise it gets its session channel (created on demand).
   - `ErrNoDefaultChannel` goes: an empty `to` always has a channel.
   - Callers: `Plan`, `Notify`, `resolveTicket` (own ticket), and the relay directory's legacy receipts.
3. **Lifecycle.**
   - `SetUp`: a ticket session joins its ticket as before. Any other session gets its session channel; on *creation*, the session joins it, and for a personal session so does its starter, if they are a live project member. Re-running `SetUp` never re-adds a starter who left.
   - `Ended`: leave the ticket; archive the session channel.
   - `Notify` may still post into the session's **own** archived session channel, so teardown notices (`NotifyEnded`, `NotifyReclaimed`) reach its members. This is a deviation from spec §6's "post, then archive": the effect is the same, and teardown order is untouched.
   - `Reconcile` runs `SetUp` for every live session, not just ticket sessions.
4. **`session:` addresses** (`Plan`).
   - `session:<label|id>` resolves to a live session of the poster's project, by id first, then by unique label (two sessions sharing a label are unresolved).
   - The ceiling is checked against `session:<label>`/`session:<id>`; the poster's own session is always allowed.
   - 403 comes before 404, as for users.
   - The address resolves to the target's **home** channel.
   - `Planned.Join`: the poster joins by posting.
   - `Post` also joins a user posting into a session channel.
5. **`/me`.**
   - `session:<id>` from New Message plans into the session's home channel (`PlanHome`), through the source's `CanPost`, and joins the person.
   - `PlanPersonChat` keeps only user↔user chats.
   - The rail's `viewKind(SourceSession)` is a studio conversation.
6. **Surfaces.**
   - Relay `Surfaces`: a session channel renders like a chat (its user members' Discord inboxes).
   - `ListTargets` adds `session:<label>` for the project's live sessions the ceiling allows (not the caller itself).
   - `studiofacts` describes the home channel.
   - Admin suggest lists session channels.
7. **Docs.**
   - `intercom.md`: the home channel, the `session` kind and Notify.
   - `comms-addressing.md`: `session:` and its glob (`*` doesn't match it; see the review decisions).
   - `personal-sessions.md`: the starter is a member of the session channel; there is no owner chat.
   - `standing-sessions.md`: an empty `to` now posts to its channel.
   - `intercom-ui.md`: New Message → a session.

## Review decisions (built)

- A personal session's channel never takes members by posting: a relay reply there doesn't join its author.
- A session set up again under the same id (a restart or an upgrade) reopens its archived channel, so members are kept (`ReopenChannel`).
- `*` never matches `session:`: sessions need an explicit `session:` glob.
- `session:` to a ticket session also needs `ticket:` addressing, in any form that `ticket:` takes, and `list_targets` offers only the sessions a send would reach.
- Session channels never take the chat's Linear `@`-mention fallback.
- A gone session's notice joins and creates nothing. It goes to its own channel, else to its ticket's; with neither, only a personal session's starter gets a channel, which is archived as soon as the notice is in it.
- Session channels are left out of the config export.

## Verification

`go test ./...`; `-tags integration` against local Postgres; `scripts/lint.sh`; docs audit; a fresh review before merge.
