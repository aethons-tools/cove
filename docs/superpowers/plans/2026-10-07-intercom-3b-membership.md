# intercom 3b: call-in, join, leave — plan

**Spec:** [`2026-10-07-intercom-slice3-session-channels-design.md`](../specs/2026-10-07-intercom-slice3-session-channels-design.md) §3 (membership), §4 (`call_in`/`leave` tools), §7 (`/me` Join, Leave, Call in). It builds on 3a ([plan](2026-10-07-intercom-3a-session-channels.md)).
**Ships as:** one PR. There is no schema change: membership rows already exist.

## Tasks (TDD: each test first)

1. **`Intercom.CallIn(p Poster, ch, who)`**
   - The inviter must be a current member of `ch`.
   - `ch` must be live and of a kind that takes members:
     - a session channel or ticket channel takes users and sessions;
     - a room takes users only, since rooms stay post-only for sessions;
     - a chat is fixed (`ErrFixedMembers`, 409).
   - `who` is an address:
     - `user:<name|id>` names a live member of `ch`'s project;
     - `session:<label|id>` names a live session of that project.
   - A session inviter also needs its ceiling to allow the address. That means an explicit `session:` glob, and `ticket:` addressing for a ticket session, as in `Plan`.
   - Errors: 403 when the inviter isn't a member or isn't allowed, checked before 404 for "no such invitee". A channel that doesn't exist and one the inviter isn't in are both 403.
   - Calling in a current member is a no-op.
   - Otherwise the invitee joins at the tail, and the inviter posts a notice into `ch`: "*inviter* called in *invitee*". The invitee is in that notice's audience.
   - For a session poster, `ch` "" means its home channel.
2. **`Intercom.JoinChannel(u, ch)`** (people, from `/me`)
   - The person must be able to see the channel (`CanSee`), and the kind must not be a chat.
   - A personal session's channel stays invite-only, because `CanSee` already refuses non-members.
   - Joining a channel you're already in is a no-op.
   - Sessions join by posting (`session:`) or by call-in, so they get no join verb.
3. **`Intercom.LeaveChannel(p, ch)`**
   - Any member may leave except:
     - the channel's own session (its session channel);
     - a live session from its own ticket's channel;
     - anyone from a chat, which is fixed.
   - Leaving a channel you're not in is a no-op.
   - What was delivered before leaving stays delivered.
4. **The agent wire.**
   - `POST /squawks/call-in {who, channel?}` returns `200 {channel, member}`; `POST /squawks/leave {channel}` returns `204`.
   - Status codes: 403, 404 or 409 as above, and 400 for a malformed body.
   - `cove-master mcp` gains the `call_in(who, channel?)` and `leave(channel)` tools.
   - The `send` tool's description becomes "your own channel", and it names `session:`.
5. **`/me`.**
   - New routes `POST /me/join`, `/me/leave` and `/me/call-in`.
   - The conversation header gets a **Join** button for a visible channel the person isn't in, a **Leave** button (never on a chat), and a **Call in…** picker. The picker lists the project's members and live sessions that aren't already in the channel, and is offered on session, ticket and room channels.
6. **Docs.**
   - `intercom.md`: the tools.
   - `comms-addressing.md`: call-in rules.
   - `intercom-ui.md`: the buttons and routes.
   - `personal-sessions.md`: the starter may leave and may call others in.
   - `standing-sessions.md`: call-in.

## Verification

`go test ./...`; `-tags integration` against local Postgres; `scripts/lint.sh`; docs audit; a fresh review before merge.
