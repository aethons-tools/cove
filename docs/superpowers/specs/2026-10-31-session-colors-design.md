# Session colors — design

**Status:** agreed with the user 2026-10-31; not built yet.
**Context:** a session is known by its name. Telling several studio windows apart, or remembering which session you are writing to, means reading that name. The user wants each session to have its own **color**: one extra cue that is quicker to find than a title. It doesn't replace the name. Later, an image from the BRYC service joins it as a second cue (out of scope here).

## Intent

- **Telling windows apart (the main moment).** With several studio windows open, each window's title bar is the session's color.
- **A gentle reminder in the conversation.** The session's chat bubbles have a left border in its color, and so does the composer when you write to the session.
- The color is one cue among several: the name stays everywhere it is today.

## Goals

- Every session gets a color automatically, at setup. The user doesn't pick it (decided: "Automatic is good for now").
- A session's color doesn't change for as long as it lives. It is the same in the web app and in VS Code.
- Sessions that are live at the same time get colors that are clearly different from each other. 64–125 sessions over time must work. Two sessions that are never live together may share a color.
- The colors work in light, dark and high-contrast VS Code themes, and under one fixed title-bar text color.

## Non-goals

- Choosing or changing a session's color (later, maybe).
- The BRYC image.
- Colors for people, chats or channels.
- Changing colors that carry meaning: phase, needs-you, card states.

## Anti-goals

- **Loud UI.** The theming just made the panes look native to VS Code. The color goes only in the title bar, a thin bubble border and the composer accent; it is never a fill behind text in the conversation.
- **Writing into the studio's repo.** VS Code keeps per-window colors in a workspace's settings. A studio window's folder is the agent's workspace (`/home/agent/workspace`, over Remote-SSH), so writing `.vscode/settings.json` there would put our settings in the agent's git repo. That must not happen (see Title bar).
- **Muddy or low-contrast colors.** These are why a naive RGB grid was rejected.

## Decided

### The palette (my call, agreed in outline)

- **Built in OKLCH, not RGB.** RGB grids have uneven steps to the eye and produce muddy, near-gray and near-black colors. Instead, use one lightness/chroma band (optionally 2–3 lightness tiers) and vary the hue: about 16–24 hues per tier. That is roughly 24–72 colors with no muddy ones.
- **Readable.** Every palette color must reach a contrast of at least 4.5:1 against the title-bar text color chosen for the palette (white, or a near-black). A test enforces this.
- **Stored as `#rrggbb`.** The palette lives in Go and is converted to sRGB hex once. What is stored on a session is the hex, so a later palette change never recolors a live session.

### Assignment (my call, agreed in outline)

- **Where:** Jam assigns the color in `Supervisor.Raise` (`internal/jam/supervisor.go`), so personal, ticket, standing and manual sessions all get one.
- **Stored durably, by session id (my call).** The color goes in a new table, `session_colors(actor_id text primary key, color text not null)`, which needs a migration and a memstore counterpart. Rows are never deleted: they are tiny. The color can't live on the `Instance`, because `Teardown` deletes the Instance (`supervisor.go`, `RemoveInstance`), and a standing session's upgrade, restart or reset tears down before it raises again. A durable row keeps a standing session's color across those. It also keeps the color for an ended session's old messages.
- **Raise:** if the session id already has a color, keep it. Otherwise assign one and store it.
- **How:** take the colors of the sessions live now (sessions with an Instance whose phase is not gone), across the whole Jam (my call). Pick the palette color whose smallest distance to any of them is largest, measured in OKLab (ΔE). Break ties by palette order, so the result is deterministic and testable. With no sessions live, the first palette color is used.
- **Standing sessions keep their color when raised again,** through the durable row.
- **Sessions from before this change have no color.** They show none, as today, until they are raised again. There is no backfill (my call).
- Assigning the color is best effort: if it fails, the session starts with no color rather than failing.

### Where it reaches the clients

The field is set in the Go API types (`internal/jam/userui/apitypes.go`); `web/app/src/api/types.ts` is regenerated with tygo.

- `SessionHead` and `SessionCard` get `color`, for the session.
- `Conversation` gets `color`, for its home session's color (empty for a chat).
- `MessageRow` gets `color`, for the sender's session color (empty when a person sent it). The conversation builder (`userui/conversation.go`) already has the sender id: a session sender is `ident.ID.Kind() == ident.Session`, and its color comes from `session_colors`, so an ended session's messages keep theirs. Look it up once per sender per response, not once per message.
- `SessionTarget` (`web/app/src/bridge/protocol.ts`, `web/vscode/src/focus.ts`) carries `color`, so the extension and the panes have it.

### Title bar (VS Code)

- Only in the session's studio window: the active and inactive title bar backgrounds are the session color (inactive: dimmed). Its foreground is the palette's text color. The hub, ordinary windows and other studio windows are untouched.
- **Without touching the studio's repo** (see Anti-goals). The extension opens a studio window as a local `.code-workspace` file in the extension's storage, next to the hub's files (`hubDir`). The file has one folder, the remote URI (`remoteUri(id)`), and in its `settings` a `workbench.colorCustomizations` with the title-bar colors. The file is written or updated each time the window is opened, so a session's color is always current. `windowKind` must still recognize such a window as a studio window. The builder decides how: by the workspace file's name, the remote authority, or both.
- **Existing studio windows (my call).** A studio window opened as a plain folder before this change stays uncolored. The first time Jam opens or focuses that session, it opens the workspace-file window, which VS Code doesn't match to the plain-folder one, so you get a second window once. Closing the old window settles it. The VS Code docs say so. Jam doesn't try to find and refocus the old window, because that window could never be colored.

### Conversation (web app and VS Code alike)

- **Bubbles.** A message from a session (`MessageRow.color` set) gets a thin left border in that color, about 3px. Nothing else in the bubble changes. Your own messages and other people's have none.
- **Composer.** When the conversation has a home session with a color, the composer's left edge gets the same thin accent. Typing to a session then carries its color. A composer with no session (a chat, New message to a person) has none.

## Left to the builder

- The exact OKLCH lightness, chroma and hue count, within the readability rule. The palette needs about 48 colors (two tiers of about 24 hues; my call). When more sessions are live than there are colors, the assignment rule degrades gracefully: the colors repeat as far apart as possible.
- The inactive title bar's dimming, and whether `titleBar.border` is set.
- How `windowKind` recognizes a workspace-file studio window, and the workspace file's name.
- Whether the composer accent sits on the input or on its wrapper.

## Open

- Should the session status card's icon block (a plain box today; see [2026-10-30-session-status-card-design.md](2026-10-30-session-status-card-design.md)) be filled with the session's color until real icons arrive? It isn't asked for yet.
