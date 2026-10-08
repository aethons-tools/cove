---
summary: Plan — bind a roster human to their Discord user id so a Discord reply is attributed by who sent it (Discord's immutable author id), not by which inbox channel it landed in; lifts reply-to-act's unique-inbox limit and closes the "anyone who can post in the inbox" gap.
read_when: Implementing or reviewing Discord user binding, or reply-to-act attribution.
owns: the Discord user binding implementation plan (tasks, decisions, out-of-scope)
prereqs: docs/usage/jam/discord.md for the shipped attribution rules
tier: plan
updated: 2026-09-27
---

# Discord user binding

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A roster human can be bound to their **Discord user id**. A Discord reply is then attributed to a roster human **by its author id**. Discord assigns that id and a user can't change it, unlike a display name. This:
- lets reply-to-act (`keep`/`release` on an idle nag) work from a shared inbox;
- stops a stranger who can post in the owner's inbox from counting as the owner, once the owner is bound.

**Architecture:**
- **Carry the author id through.** The switchboard's Discord poll already receives `author.id` and `author.bot` and drops them. It keeps them: `switchboard.Message.AuthorID` and `AuthorBot`, then `relay.Event.AuthorID` and `AuthorBot`.
- **Bind on the roster.** `DeliveryProfile` gains `UserID` (for `service: discord`, the human's Discord user id). It is set with `add-human --delivery discord:<channel>:<user-id>` (the third part is optional) and validated unique per project, like `Login`.
- **Attribute by id first.** A new `jam.DiscordAuthor(roster, channel, authorID, isBot)` decides who a Discord message is from (see Decisions). `routeDiscord` uses it in place of `DiscordInboxOwner`. Wake-on is unchanged: it still checks `From == human:<owner>`.
- **The nag hint follows the same rule.** The nag offers `keep`/`release` when the owner could be attributed: they are bound, or their inbox is uniquely theirs and they are not bound.

**Tech Stack:** Go 1.26, `just`. Builds on reply-to-act ([`2026-09-27-reply-to-act-on-nags.md`](2026-09-27-reply-to-act-on-nags.md), merged).

## Decisions

- **Attribution, in order:**
  1. **A bot author is never a roster human.** Use the display name, as today.
  2. **The author id is bound to exactly one roster human in the project:** that human, whatever the channel.
  3. **The channel is uniquely one human's inbox (today's rule), and that human is not bound:** that human. This is backward compatible for unbound rosters.
  4. **Otherwise:** the display name (`human:<author display>`), as today.

  Rule 3's "not bound" is the security point. Once an owner is bound, only their own Discord account counts as them. Someone else posting in their inbox is attributed by display name, so their `keep`/`release` is an ordinary reply.
- **Uniqueness:** a Discord user id may be bound to at most one human per project. A second is **400**, with the same message shape as a duplicate login. Different projects may bind the same id; attribution is per project.
- **Syntax:** `--delivery discord:<channel>[:<user-id>]`. A user id is all digits (a Discord snowflake), and anything else is 400. Other services ignore the third part and reject it (400) if it is given.
- **Discord only.** The ids never go into logs beyond debug. They aren't secret, but keep logs lean; log whether a message was attributed by id or by channel.
- **No migration.** Profiles are JSON in the roster doc, so existing rosters load with an empty `UserID` (unbound) and behave exactly as today.

## Global Constraints

- **Unchanged:** unbound rosters, Linear routing, and wake-on logic.
- **Fail toward "ordinary reply".** An ambiguous or unknown author is never attributed to an owner.
- **Docs in the same change.** TDD. Stage files by path. End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Keep the author id

**Files:** `internal/switchboard/discord.go`, `internal/switchboard/message.go`, `internal/relay/relay.go`, `cmd/at-jam/relay_discord.go`, and tests.

- [ ] Tests first:
  - the poll decodes `author.id` and `author.bot` into `Message.AuthorID` and `AuthorBot`;
  - `discordSurface.Poll` copies them into `relay.Event`;
  - the existing `Author` (display name) behavior is unchanged.
- [ ] Check other users of `switchboard.Message` (the in-sandbox conductor, `cmd/at-switchboard`). New fields must not change their rendering.
- [ ] Commit: `switchboard: keep the Discord author id and bot flag`.

## Task 2: Bind on the roster

**Files:** `internal/jam/identity.go`, `internal/jam/admin.go` (add-human), `cmd/at-jam/main.go` (the `--delivery` parse), `internal/jam/adminclient` (if it shapes the body), and tests.

```go
// DeliveryProfile gains:
UserID string `json:"user_id,omitempty"` // discord: the human's Discord user id (snowflake); "" = unbound

// HumanByDiscordUser returns the one roster human in project bound to userID.
func HumanByDiscordUser(r Roster, userID string) (Human, bool)
```

- [ ] Tests first:
  - the CLI parses `discord:C:U` and `discord:C`; `discord:C:abc` is 400 (exit 2); `linear:X:U` is rejected;
  - the admin route returns 400 for a user id already bound to another human in the project, 201 for re-adding the same human, and accepts the same id in another project;
  - the file-store round trip keeps `user_id`;
  - `roster list` shows the binding.
- [ ] Commit: `jam: bind a roster human to a Discord user id`.

## Task 3: Attribute by author id

**Files:** `internal/jam/identity.go`, `cmd/at-jam/relay_linear.go` (`routeDiscord`), `cmd/at-jam/nag.go`, and tests.

```go
// DiscordAuthor returns the roster human a Discord message is from (the
// Decisions' rules 1–3); ok=false means fall back to the display name.
func DiscordAuthor(r Roster, channel, authorID string, isBot bool) (name string, ok bool)
```

- [ ] Tests first (one per rule, plus the edges):
  - a bot is never attributed;
  - a bound id wins in any channel, including a shared inbox and another human's inbox;
  - a unique inbox with an unbound owner uses the channel rule;
  - a unique inbox whose owner is bound and a different author: not attributed;
  - an empty author id with an unbound owner uses the channel rule (older events);
  - an id bound in another project only: not attributed.
- [ ] `routeDiscord` uses `DiscordAuthor` with `e.AuthorID` and `e.AuthorBot`, and logs at debug whether the attribution was `by=id` or `by=channel`.
- [ ] The nagger's hint condition becomes "the owner is attributable". Tests first: a bound owner with a shared inbox gets the hint; an unbound owner with a shared inbox gets none (as today).
- [ ] An end-to-end-ish test in `cmd/at-jam`: a relay event from the bound owner in a shared inbox, replying `release` to a nag, is routed as `human:<owner>` with `ReplyTo` set to the nag id. That is exactly what wake-on acts on.
- [ ] Commit: `relay: attribute a Discord reply by its author id`.

## Task 4: Docs

- [ ] `docs/usage/jam/comms-addressing.md`: the attribution rules (id first, then the channel for unbound owners, then the display name), the `--delivery discord:<channel>:<user-id>` syntax, and how to find your Discord user id (Developer Mode, then Copy User ID).
- [ ] `docs/usage/jam/personal-sessions.md` § the idle ladder:
  - reply-to-act works from any inbox once you are bound;
  - binding is the recommended setup;
  - the "only you may post in your inbox" caveat now applies only to unbound owners.

  Link to comms-addressing rather than repeating the rules.
- [ ] The roster doc that owns `add-human` (`roster.md` or `operators.md`; follow `OVERVIEW.md`): the flag syntax and the uniqueness rule.
- [ ] Docs-audit checker with `--index OVERVIEW.md`: no new errors against main.
- [ ] Commit: `docs: bind a roster human to their Discord user id`.

## Out of scope

- Binding for other services (Linear user ids).
- Verifying a binding (e.g. a DM challenge). The operator who adds the human asserts it, as with `Login`.
- Reply-to-act through Linear.
