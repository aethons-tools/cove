---
summary: Session colors — each session's color from a fixed 24-color palette, assigned by Jam when it is raised and kept for good; how it is picked, where it is stored, and where it shows (the VS Code studio window's title bar, the session's chat bubbles and the composer, its status card's icon).
read_when: You want to know why a session or a studio window has the color it has, why two sessions share one, or you are changing the palette, the assignment, the `session_colors` table or a place the color shows.
owns: the session palette, the assignment rule, the session_colors table, the color fields of the user API, and where the color shows
prereqs: coves.md for raising and session phases; session-card.md for the status card; vscode.md for studio windows
tier: leaf
updated: 2026-11-01
---

# Session colors

Every session has a **color**: one extra cue, quicker to spot than its name, for telling studio windows apart and for remembering which session you are writing to. It never replaces the name.
Design: [session colors](../../superpowers/specs/2026-10-31-session-colors-design.md).

## The palette

24 colors (`SessionPalette`, `internal/jam/sessioncolor.go`): OKLCH hues 15° apart at lightness 0.52 and chroma up to 0.14, as sRGB.
Each is readable under white text (contrast 4.5:1 or better; a test checks it).
Status colors (phase, needs-you, card states) are separate and unchanged.

## How a session gets its color

- When a session is **raised** (`Supervisor.Raise`: personal, ticket, standing or manual), Jam gives it the palette color **farthest** (OKLab ΔE) from the colors of the sessions live now, across the whole Jam. A tie goes to the earlier palette color.
  With more than 24 sessions live, colors repeat: the least-used color goes first.
- The color is stored by session id in **`session_colors`** (migration `0024`) and **never changes or is deleted**:
  - a standing session keeps its color across upgrades, restarts and resets;
  - an ended session's old messages keep theirs.
- Assignment is best effort: if it fails, the session starts with no color.
- Sessions raised before colors existed have none until raised again.

## Where it shows

The user API carries `color` (`#rrggbb`; omitted for none) on `SessionHead`, `SessionCard`, `Conversation` (the home session's) and `MessageRow` (the sending session's).

- **The studio window's title bar (VS Code).** The extension opens a studio window from a local workspace file, `<globalStorage>/studios/jam-<studio>.code-workspace`. That file holds the remote folder, the window title (the session's name) and the title-bar colors (dimmed when the window is inactive), so nothing is written into the studio. The file is rewritten on each open ([vscode.md](vscode.md#what-it-writes)).
  A studio window opened before this was a plain remote folder: opening that session through Jam gives a **second window, once**. Close the old one.
- **Chat bubbles:** a 3px left border on the session's messages, in the web app and VS Code alike. Yours and other people's have none.
- **The composer:** the same border on its left edge, when the conversation is a session's home.
- **The status card's icon** is filled with the color ([session-card.md](session-card.md)).
