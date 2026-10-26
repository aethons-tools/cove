# Session mission — design

**Status:** design, awaiting approval.

**Context:** In the `/app/user` inbox rail, a session shows only its name: `jam-ic-01`, a standing name, or a ticket key. The name doesn't tell you what the session is doing. Each session gets a **current mission**: one line saying what it is focused on right now. The agent sets the mission itself whenever its focus changes, for example when someone gives it a new task or it picks up a ticket.

## Decisions (2026-10-26)

- **Purpose:** a reminder while scanning the rail of what each session is about. Success means you can tell your sessions apart without opening them.
- **Rail layout (option A from the mockups):** the mission shows as a muted second line under the session name, one line long and truncated with an ellipsis. A session with no mission shows nothing extra.
- **Header:** the session header shows the mission too.
- **Who sets it:** the agent, through a new messaging tool, whenever its focus changes. The session context tells agents to do so.
- **`unit` stays.** `unit` remains the ticket key (`COV-512`): Jam finds the ticket channel by it, the dispatcher keys on it, and about 60 code paths treat it as an identifier. The mission is a **separate** field. It replaces `unit` as what tells *people* what the session is doing, but it doesn't replace `unit` as an identifier.
- **Ticket sessions** start with the ticket's title as their mission. The agent may change it.

## 1. Data

- **New field:** `jam.Instance` gains `Mission string \`json:"mission,omitempty"\``.
  - Instances persist as a JSON document (`pgstore` `instances.doc` jsonb, and the memstore). No migration is needed.
- **Raise spec:** `jam.RaiseSpec` gains `Mission string`, which `Supervisor.Raise` copies onto the new Instance.
  - The dispatcher passes `Mission: iss.Title` (`internal/dispatcher/dispatcher.go`, where it builds the RaiseSpec).
  - Other raise paths (personal, standing, manual) start with an empty mission.
  - **Every raise starts fresh, including standing-session restarts and upgrades** *(my call)*. Carrying a mission across re-raises would need new plumbing. Instead, a standing session's restart prompt (`standingRestartPrompt` in `internal/agentrun`) gains "set your `mission` again". A ticket re-raised by the dispatcher starts from the ticket title again.
- **Normalisation, applied on every set:**
  - Trim the text and collapse any whitespace run, newlines included, to a single space.
  - Cap it at **120 runes** *(my call)*. Longer text is truncated with `…`, not rejected, so the agent's call never fails over length.
  - An empty result clears the mission.

## 2. The agent tool

- **MCP tool** in `cmd/cove-master/mcp.go`: `mission`, with one argument `text`.
  - Agent-facing description *(my call)*: "Set your current mission: one line saying what you are focused on now, shown beside your name in people's inbox. Set it when your focus changes (a new task, a ticket picked up); empty text clears it."
- **Client call:** `PUT /mission` with body `{"text": "..."}`. It is modelled on `end`/`escalate`.
- **Jam handler:**
  - It authenticates the calling cove with `authenticateCove` and is self-scoped: a session can only set its own mission.
  - The body is capped at 2 KiB. It normalises the text, calls `Supervisor.SetMission(actorID, text)`, and answers 204.
  - `SetMission` follows `SetEscalationCategory`'s pattern: under `instMu`, it gets the instance, sets the field and puts it back.
- **Registration:** `cmd/at-jam/mux.go` routes `/mission`, mounted under the same condition as `/escalate`. Add the tool name to `IntercomTools` (`internal/jam/setup_config.go`) and to the cove-master tool-name test.

## 3. Session context

Add one line to the boilerplate core (`internal/jam/sessionctx/boilerplate.go`), for every session kind:

> - Mission: when your focus changes (a new task, a ticket picked up), set a one-line `mission` saying what you are on — people see it beside your name.

It must fit `BudgetBoilerplate` (2400 bytes, enforced by the boilerplate test). The dispatcher's ticket `turnEndProtocol` tool list may mention it too *(builder's call)*.

## 4. UI

- **Rail.** `jam.ChannelView` and `userui.ChannelRow` gain `Mission`.
  - **Session channel:** its session's mission.
  - **Ticket channel:** the mission of the live session working the ticket. If there are several, use the most recently raised live one *(my call)*.
  - **Chat (DM) whose only non-person member is one session:** that session's mission, because personal and standing sessions are often reached this way *(my call)*.
  - **Rooms, and chats with only people or with several sessions:** no mission.
  - **Which sessions count:** only active ones. Use the same `instanceActive` filter that `userChannel` uses for phase; a paused (Idled) session counts as active. A Gone session's row shows no mission, so ended sessions never show a stale one *(my call)*.
  - **Template:** `inbox.html` renders the name and a `.mission` line stacked (as in mockup A). The `.mission` line is muted, 12px, on one line with an ellipsis. Its full text goes in a `title` tooltip.
- **Header.** `SessionHead` gains `Mission`, shown under the name in the identity block. It is omitted when empty.
- **Freshness.** A change appears on the next rail or header refresh: 30s polling, or any `meChanged`. *Builder's call:* whether `SetMission` also triggers a `meChanged`-style change notification so it shows at once. That would be nice to have, but isn't required.

## 5. Clear resets the mission

**Clear** starts a fresh conversation for a new task, so the old mission is wrong. When a Clear press is sent to the cove successfully (`sessionOps.Command`, `CmdClear`), Jam clears the session's mission *(my call: at the press, not on cove-master's record)*. `sessionOps.Command` is the only Clear path. A `SetMission` failure there is logged and doesn't fail the press. In the rare case where the cove ignores the press, the mission is gone but the conversation isn't; the agent sets it again on its next focus change. Interrupt and Compact leave the mission alone.

## 6. Non-goals

- People editing a session's mission by hand.
- Mission history or an audit trail.
- Showing the mission anywhere beyond the rail and the session header: not in the admin UI, Discord or notifications. The admin instance listing may include it *(builder's call)*.
- Changing what `unit` means or where it is used.

## 7. Anti-goals

- **Missions that go stale and mislead.** This is why Clear resets the mission and the session context tells agents to keep it current.
- **Breaking ticket routing.** Never touch `unit` semantics.
- **A rail that gets noisy or jumps around.** Keep it to one muted line, always truncated.

## 8. Docs and tests

- **Docs:**
  - `docs/usage/jam/intercom.md`: the `mission` tool, in the tool list and "What the tools do".
  - `session-view.md`: the header, and the Clear row's mission reset.
  - `intercom-ui.md`: rail rows.
  - `session-context.md`: the boilerplate table.
  - `requisitioner.md`: a ticket title becomes the starting mission.
- **Tests:**
  - Handler: auth, self-scope, normalisation (trim, whitespace, the 120-rune cap, empty clears).
  - `SetMission` persistence.
  - Dispatcher passes the title, and Raise copies it.
  - The MCP tool calls `PUT /mission`.
  - Boilerplate stays within budget and contains the line.
  - Rail and header render the mission (session row, ticket row, none).
  - Clear resets the mission.
