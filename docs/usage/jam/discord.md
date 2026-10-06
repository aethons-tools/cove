---
summary: Discord delivery for a Project — a Human's delivery profiles (inbox channel + optional Discord user-id binding), the per-project chat service, the Discord egress and reply loop, and who a Discord reply is attributed to.
read_when: You are setting up a human's Discord inbox or binding them to their Discord user id (`--delivery discord:<channel>[:<user-id>]`), setting a project's chat service, or a Discord reply was routed or attributed (to a roster human vs a display name) differently than you expected.
owns: Human.Delivery profiles and the `--delivery service:address[:user-id]` syntax, the Discord user-id binding and its Jam-wide uniqueness, Project.ChatService and the `project chat-service` verbs, the Discord egress + reply loop (receipts), and the Discord reply attribution rules (bot / bound id / unbound owner's inbox / display name)
prereqs: comms-addressing.md for the Project roster and `send(to=…)` targets this delivers; intercom.md for the Discord relay engine; serve.md for `runtime.discord`
tier: leaf
updated: 2026-10-06
---

# Discord delivery & reply attribution

In a Project whose chat service is `discord`, Jam delivers a studio's messages to
humans' Discord **inbox channels** and routes their Discord **replies** back to the
studio. A reply is attributed to a roster human **by Discord author id** once that
human is bound; unbound humans are attributed by their own inbox channel, as before.

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
`roster list` shows a binding as `discord-user=<id>`. To find your id: Discord
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

**Discord egress and reply-routing are both live.** A discord-project's
`send(to=human:<name>)` posts to that human's Discord **inbox channel** when
they have a `discord` delivery profile (falling back to the Linear
`@`-mention when they don't); `send(to=channel:<name>)` posts to a discord
roster channel's own `Ref` when the channel's `Service` is `discord`. Every
Discord post is prefixed `"<cove>: "` (the sending studio's identity —
`Delivery.BodyPrefix`, no webhook this slice; per-sender webhook
username/avatar is a future polish). Delivery is exactly-once (the resident
Discord relay engine's own `EgressMark`, seeded to the Log tail on first
enable so turning it on never redelivers the backlog) — see
[intercom.md](intercom.md#enabling-it) for the engine and
[serve.md](serve.md) for the `runtime.discord.connection` config.

**The reply loop:** when a human **replies** (Discord's own reply-to-message
feature, not a bare follow-up post) to a studio's Discord post, Jam routes
that reply back to the studio that sent the original squawk — the same
[wake-on](intercom.md#waiting-for-a-reply-wake-on) a Linear reply triggers,
so a Waiting studio resumes with the reply already in its inbox. Routing works
by a **receipt** recorded on every Discord post (`discord-msg-id → {actor,
message}`: the sending studio and the squawk's Log id). An inbound reply is
matched by the id it *replies to*; its `reply_to` is the answered squawk's id,
so it joins that squawk's thread. An older receipt (no squawk id) still routes,
with `reply_to` `in:discord:<id>`. Who the reply is *from* is decided by
[the attribution rules](#who-a-discord-reply-is-from). Consequences:

- **Only a reply routes.** A bare (non-reply) squawk posted into a shared
  inbox channel carries no id to look up against, so it can't be attributed
  to any studio — it is silently dropped, by construction (this also means
  Jam's own outbound Discord posts, echoed back on the same channel,
  never mis-route to themselves; no separate self-post filter is needed).
- **Receipts are currently unpruned** — one entry per post on local disk,
  never collected (a known follow-up, not a correctness issue).

## Who a Discord reply is from

Jam decides a reply's sender from Discord's immutable **author id**, never the
display name (which anyone can set), in order:

1. A **bot** author is never a roster human.
2. An author id **bound** to exactly one roster human in the project → that
   human (`human:<roster name>`), in **any** channel, shared inboxes included.
3. A channel that is the `discord` address of **exactly one** roster human (and
   not also a roster channel) → that human, **only while they are unbound**.
4. Otherwise → `human:<Discord display name>`, an ordinary reply.

Rule 3 keeps unbound rosters working as before; once a human is bound, only
their own Discord account counts as them, so someone else posting in their
inbox is attributed by display name. Binding is operator-asserted (like
`--login`), not verified.
Jam logs at debug whether a reply was attributed `by=id` or `by=channel`
(never the message body).
