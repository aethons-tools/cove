---
summary: Discord delivery for a Project — a member's delivery profile (inbox channel) and Discord account, the per-project chat service, how chats and rooms are posted to Discord, the reply loop, and who a Discord reply is attributed to.
read_when: You are setting up a human's Discord inbox or binding them to their Discord user id (`--delivery discord:<channel>[:<user-id>]`), setting a project's chat service, or a Discord reply was routed or attributed (to a roster human vs a display name) differently than you expected.
owns: Human.Delivery profiles and the `--delivery service:address[:user-id]` syntax, the Discord user-id binding and its Jam-wide uniqueness, Project.ChatService and the `project chat-service` verbs, the Discord egress + reply loop (receipts), and the Discord reply attribution rules (bound id / unbound member's inbox / account)
prereqs: comms-addressing.md for the Project roster and `send(to=…)` targets this delivers; intercom.md for the Discord relay engine; serve.md for `runtime.discord`
tier: leaf
updated: 2026-10-06
---

# Discord delivery & reply attribution

In a Project whose chat service is `discord`, Jam posts people's chats to their
Discord **inbox channels** and rooms to their bound Discord channels, and puts
Discord **replies** back into the conversation they answer. A reply is attributed
to a member **by Discord author id** once they are bound; unbound members by their
own inbox channel.

## Delivery profiles & chat service

A **Human** carries `Delivery []{Service, Address, UserID}` — one
entry per non-tracker service the human can be reached on. For `Service:
"discord"`, `Address` is the id of the **inbox channel** Jam posts that human's
DMs into, and `UserID` (optional) **binds** the human to their Discord user id
(never a bot token or other secret — see [operators.md](operators.md) for where
credentials actually live).

A **Project** carries `ChatService string` — the service backing
that project's human DMs (e.g. `"discord"`); empty means tracker `@`-mentions
only, same as before this field existed.

Set a member's inbox with `--delivery service:address` on `project member add`
(repeatable — one flag per service), and bind their Discord user id as an
**account** on the `discord` connection:

```
at-jam project member add <project> alice --delivery discord:<inbox-channel-id>
at-jam account add --connection discord --uid <alice's-discord-user-id> --user alice
```

The service and address must be non-empty. The optional user id is
**discord-only** and all digits (a Discord snowflake); any other shape, or a user
id on another service, exits `2` (the admin route answers **400**). A Discord
user id binds **one person Jam-wide** (their Discord account in the
[identity registry](roster.md)) — binding it to anyone else, in any project, is
**400**, like a duplicate login.
`account list --connection discord` shows a binding as `uid=<id>`. To find your id: Discord
→ Settings → Advanced → **Developer Mode** on, then right-click yourself →
**Copy User ID**.

Manage a project's chat service with `at-jam project chat-service`:

```
at-jam project chat-service set   --project <project> --service discord
at-jam project chat-service show  --project <project>
at-jam project chat-service clear --project <project>
```

`set` requires `--project` and `--service`; `clear` is `set` with `""` under
the hood; `show` prints the configured service or `(none)`. All three take the
same admin-client flags as every other `at-jam` verb.

## Egress & the reply loop

**What's posted where.** The Discord relay renders the [channel log](intercom.md#enabling-it):

- a **chat** onto the inbox channel of each person in it (but the post's
  author) in a project whose chat service is `discord` — a person with no
  Discord inbox is reached on Linear instead
  ([comms-addressing.md](comms-addressing.md), delivery and reply semantics);
- a **room** bound to a Discord channel onto that channel.

Every post is prefixed `"<sender>: "` (a session's name, a person's name), and
nothing goes back to the inbox or channel it came from — so in a group chat,
alice's reply from her inbox reaches bob's. Delivery is exactly-once per
surface (the relay's own `EgressMark`, seeded to the log's tail on first
enable) — see [serve.md](serve.md) for the `runtime.discord.connection` config.

**The reply loop:** when a person **replies** (Discord's own reply-to-message
feature) to one of Jam's posts, Jam puts the reply in that post's conversation
— the chat or room it was in — so the session(s) in it hear it and a waiting
one [wakes](intercom.md#waiting-for-a-reply-wake-on). Routing works by a
**receipt** recorded on every post (`discord-msg-id → {channel, squawk id,
author}`); the reply's `reply_to` is the answered squawk's id, so it joins
that squawk's thread. A receipt from before the channel log (no channel) still
routes, to its session's default channel, while that session runs. Who the
reply is *from* is decided by [the attribution rules](#who-a-discord-reply-is-from).

- **In an inbox, only a reply routes.** A person's inbox is shared by all
  their chats, so a bare post there belongs to none and is dropped (Jam's own
  posts, echoed back, are dropped the same way). A bare post in a **room's**
  channel goes to the room.
- **Bots are not routed.**
- **Receipts are currently unpruned** — one entry per post on local disk
  (a known follow-up, not a correctness issue).

## Who a Discord reply is from

Jam decides a reply's sender from Discord's immutable **author id**, never the
display name (which anyone can set), in order:

1. An author id **bound** to exactly one member of the project → that user, in
   **any** channel, shared inboxes included.
2. A channel that is the `discord` inbox of **exactly one** member (and not also
   a room's) → that user, **only while they are unbound**.
3. Otherwise → the **account** their author id is recorded as on the discord
   connection (labelled with their display name) — or, once an operator links
   it to a user who is a member (`at-jam account list --connection discord`,
   then `account link <account> <user>`), that user.

Rule 2 keeps unbound members working; once someone is bound, only their own
Discord account counts as them, so someone else posting in their inbox is
their own account. Binding is operator-asserted (like `--login`), not
verified. Jam logs at debug whether a reply was attributed `by=id` or
`by=channel` (never the message body).
