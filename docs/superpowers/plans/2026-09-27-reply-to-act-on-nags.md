# Reply-to-act on idle nags

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A personal session's owner answers an idle nag in Discord by replying **`keep`** or **`release`**, instead of running `at-harbor session release`.
- `release` tears the session down.
- `keep` restarts the idle clock without waking the agent.
- Any other reply wakes the session, as today.

**Architecture:**
- **Replies reference real messages.** Discord receipts record the posted message's id alongside the sending cove (`discord-msg-id → {actor, message}`). An inbound reply's `ReplyTo` is then the id of the message it answers. Today it is `in:discord:<foreign id>`, which matches nothing in the log, so the `read` thread view never finds Discord replies either.
- **Nags are recognizable by id.** The nagger appends each nag with the id `nag:<actorID>:<unix-nanos>`, so "a reply to one of this session's nags" is a prefix check on `ReplyTo`. No log schema change.
- **Replies from an owner's own inbox are attributed to them.** A Discord reply posted in an inbox channel that belongs to exactly one roster human is recorded `From: human:<roster name>`. That's how wake-on knows the owner sent it. Any other reply keeps today's `From: human:<Discord display name>`.
- **Wake-on acts on the command.** When a Waiting personal session has new external replies, wake-on checks for a command before waking. A command is a reply to one of its nags, from its owner, whose trimmed, lowercased body is exactly `keep` or `release`.
  - `release`: tear it down (the reservation is released as usual) and confirm to the owner.
  - `keep`: move the session's wait baseline past the reply, restart the idle ladder, and confirm. The agent is not woken.

**Tech Stack:** Go 1.26, `just`. Spec: [`../specs/2026-09-26-session-kinds-standing-personal.md`](../specs/2026-09-26-session-kinds-standing-personal.md) (Deferred: reply-to-act on nags). Builds on session-kinds Slices 3–4 (merged).

## Decisions in this plan

- **The owner is proven by the channel, not the display name.** The switchboard reports a Discord author as their display name (`GlobalName`, else `Username`), which anyone can set. So a `From` name can't authorize `release`. A reply to a nag is posted in the channel the nag was delivered to: the owner's inbox channel. If that channel is the Discord delivery address of **exactly one** human in the project's roster, the reply is attributed to that human. If the channel is shared, attribution falls back to today's display name, reply-to-act is off for that owner, and the nag doesn't advertise it. Binding Discord user ids to roster humans is deferred.
- **Only replies to a nag count.** An exact `keep` could be the owner's answer to the agent's own question ("keep the branch?"). So a command must reply to a nag. Every other reply goes to the agent.
- **`release` wins, then anything else wakes.** If several new replies are waiting: any valid `release` releases. Otherwise any non-command reply wakes the session (a `keep` alongside it is just text the agent can read). Otherwise, if there are only `keep`s, keep.
- **A non-owner's `keep`/`release` is an ordinary reply.** It wakes the session like any other reply and is logged at warn. It never tears anything down.
- **`keep` doesn't wake or resume.** A paused (Idled) session stays paused. The next nag comes `idle-after` from the `keep`.
- **Discord only.** Linear replies keep their current routing; a nag delivered through Linear works as today.

## Global Constraints

- **Unchanged:** personal sessions without a unique Discord inbox, ephemeral coves, standing coves, and the reclaim step.
- **Idempotent:** a `release` whose teardown fails is retried on the next tick, because the reply is still past the baseline. A `keep` advances the baseline **before** confirming, so it is processed once.
- **Confirmations are best-effort** squawks sent as the cove to `human:<owner>`, like nags. A failed confirmation is logged and never undoes the action.
- **Receipts file compatibility:** read the old `map[id]actorID` format as well as the new one. An old receipt routes as before, with no message id and so no reply-to-act.
- **No secrets in logs;** don't log message bodies (log the command word).
- **Docs in the same change.** TDD. Stage files by path (untracked `.switchboard/`). Each commit builds. End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Receipts carry the message id; replies reference it

**Files:** `cmd/at-harbor/relay_discord.go`, `cmd/at-harbor/relay_linear.go` (`routeDiscord`), and their tests.

```go
type receipt struct {
	Actor   string `json:"actor"`
	Message string `json:"message,omitempty"` // the posted squawk's id; "" for a legacy receipt
}
func (r *fileReceipts) Record(discordMsgID, actorID, messageID string) error
func (r *fileReceipts) Lookup(discordMsgID string) (receipt, bool)
```

- [ ] Tests first:
  - `Deliver` records `{m.From.Ref, m.ID}`.
  - The file round-trips.
  - A file in the old `{"D1":"actor-x"}` format loads as `{Actor: "actor-x"}`.
  - A torn or corrupt file still loads empty, as today.
- [ ] Test first: `routeDiscord` sets `ReplyTo` to `receipt.Message` when it is set, and otherwise to the old `in:discord:<id>`. Add a test that a Discord reply to a cove message now shows up in `intercom` `Thread(root)`.
- [ ] Commit: `relay: discord replies reference the message they answer`.

## Task 2: Attribute a reply to the human whose inbox it's in

**Files:** `internal/harbor/identity.go` (helper), `cmd/at-harbor/relay_linear.go` (`routeDiscord`), and tests.

```go
// DiscordInboxOwner returns the one roster human whose discord delivery address
// is channel; ok=false when none or more than one human uses it (a shared inbox).
func DiscordInboxOwner(r Roster, channel string) (name string, ok bool)
```

- [ ] Tests first: a unique owner, no owner, and two humans sharing a channel (ok=false).
- [ ] `routeDiscord`: when `DiscordInboxOwner(roster(project), e.Surface)` is ok, set `From = human:<name>`; otherwise keep `human:<e.Author>`. `Route` already receives the project.
- [ ] Commit: `relay: attribute a discord reply to the human whose inbox it is in`.

## Task 3: Recognizable nags

**Files:** `internal/harbor/sessions.go` (id helpers), `cmd/at-harbor/nag.go`, `cmd/at-harbor/main.go` (wiring), and tests.

```go
func NagMessageID(actorID string, at time.Time) string // "nag:<actorID>:<unix-nanos>"
func IsNagReply(replyTo, actorID string) bool          // prefix "nag:<actorID>:"
```

- [ ] Tests first: round trip, and no false match across actors whose ids share a prefix (`p-a` vs `p-ab`; the trailing `:` separates them).
- [ ] `intercomNagger` gains a roster reader. `Nag` appends with `ID: NagMessageID(inst.ActorID, now)`. Its text adds `Reply "keep" to keep it, or "release" to end it.` **only when** the owner's Discord inbox is uniquely theirs. Otherwise the text stays as today. The reclaim notice is unchanged.
- [ ] `intercomNagger` gains `NotifyKept(ctx, inst, next time.Duration)` (text: `Keeping your personal session <id> (<role>). Next reminder in <next>.`) and `NotifyReleased(ctx, inst)` (text: `Released your personal session <id> (<role>).`). Both are sent as the cove to the owner.
- [ ] Commit: `harbor: nags carry a recognizable id and offer keep/release`.

## Task 4: Wake-on acts on keep/release

**Files:** `internal/wakeon/wakeon.go`, `internal/harbor/supervisor.go`, `cmd/at-harbor/main.go` (wiring), and tests.

```go
// Supervisor.KeepWaiting restarts a Waiting personal session's idle period
// without waking it: WaitSeq = afterSeq, WaitingSince = at, LastNagAt = zero,
// Nags = 0. Errors if the instance is gone or not Waiting.
func (s *Supervisor) KeepWaiting(actorID string, afterSeq int64, at time.Time) error
```

- `NagRecorder` gains `KeepWaiting`.
- `Nagger` gains `NotifyKept` and `NotifyReleased`.
- `replied(inst)` becomes `replies(inst) []intercom.Squawk`, the external replies past `WaitSeq`.
- In `tick`, for a **personal** session with replies, run `e.command(inst, replies)` before waking:
  - Classify each reply as a `keep`, a `release`, or other. A reply is a command only if `IsNagReply(m.ReplyTo, inst.ActorID)`, `m.From == human:<inst.Owner>`, and `strings.ToLower(strings.TrimSpace(strings.TrimRight(body, ".! ")))` is `keep` or `release`. A command-shaped reply from someone else is "other", logged at warn with the actor and the command word.
  - Any `release`: `reap.Teardown`. On failure, log and return, so the next tick retries. On success, `NotifyReleased` (best-effort). Skip the rest of the tick for this instance.
  - Otherwise, any "other": fall through to today's wake (and resume if Idled).
  - Otherwise (only `keep`s): `KeepWaiting(inst.ActorID, <max Seq of the replies>, now)`, then `NotifyKept(next = idle-after)` (best-effort). Don't wake; skip the ladder and the warm-idle step this tick.
- Ephemeral and standing sessions: unchanged.

- [ ] Tests first (fake inbox, reaper, recorder, nagger; one case per behavior):
  - an owner's `release` replying to a nag tears down and notifies, and doesn't wake
  - `Release!`, ` keep. ` and `KEEP` are accepted
  - a `keep` advances WaitSeq, resets the ladder, notifies, doesn't wake, and the Idled session stays paused
  - a `keep` doesn't re-trigger on the next tick
  - a `keep` not replying to a nag wakes
  - a `release` from a non-owner wakes and doesn't tear down
  - a reply to another actor's nag isn't a command
  - `keep` + other text wakes
  - `release` + other text releases
  - a failed teardown is retried next tick
  - a failed notify doesn't undo the action
  - a standing or ephemeral session's `release` wakes as today
- [ ] Tests first: `Supervisor.KeepWaiting` sets the four fields, and errors when the instance is gone or not Waiting.
- [ ] Commit: `wakeon: an owner's keep/release reply to a nag acts on the session`.

## Task 5: Docs

Route each update from `docs/OVERVIEW.md` to the doc that owns it; don't copy.

- [ ] `docs/usage/harbor/personal-sessions.md` § *The idle ladder*: replying `keep` or `release` to a nag, what each does, owner-only, and the unique-inbox requirement (a shared inbox gets no hint and no reply-to-act). Remove any "reply-to-act is deferred" wording.
- [ ] `docs/usage/harbor/comms-addressing.md` § reply loop:
  - receipts now also record the message id
  - a reply's `ReplyTo` is the answered message, so threads work
  - a reply in an inbox channel that belongs to exactly one human is attributed to that roster human; otherwise it is recorded under the Discord display name
  - old receipts still route
- [ ] `docs/usage/harbor/intercom.md` (wake-on section): one line saying a personal session's `keep`/`release` replies to nags are acted on by harbor and don't wake the agent, with a pointer to personal-sessions.md.
- [ ] Run the docs-audit checker (`--index OVERVIEW.md`, as in the egress slice); fix anything new.
- [ ] Commit: `docs: reply keep/release to an idle nag`.

## Out of scope (deferred)

- Binding Discord user ids to roster humans (would allow reply-to-act from shared inboxes).
- Reply-to-act through Linear.
- More commands (e.g. `keep 2d`, `pause`).
- Pruning the receipt store (already a known follow-up).
