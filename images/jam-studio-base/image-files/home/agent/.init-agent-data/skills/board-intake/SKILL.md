---
name: board-intake
description: Use when turning a raw idea, bug report, or feature request into ONE well-formed issue on this project's board (Linear or GitHub Issues) — captured in the house task shape (what to do / constraints / definition-of-done → a PR) so a ticket session can pick it up. Use it whenever you're about to "file a ticket", "add this to the board", or "open an issue" for this project.
---

# board-intake

Turn an idea into one crisp, self-contained issue. You capture *intent and
shape*, never implementation. Sizing and dispatch belong to board-plan; here you
produce a well-formed issue it (or a ticket session) can act on.

## Learn the board's parameters first

The board is not configured in a workspace file. An operator configures Jam's
Requisitioner with one tracker (a Linear team or a GitHub repo), its state
names, and two label prefixes: `class:` (Jam parses it into the brief; it does not gate on it) and
`dispatch:` (opts an issue into autonomous dispatch). You learn the team or
repo and the real state names (ready, in-progress, in-review, done,
needs-input, blocked) from your session context (the Project layer) or from the
ticket itself. If they are not there, ask a human. Never guess a team, repo,
state, or label.

Do all board reads and writes through your session's connections: the Linear
MCP or API for Linear, `gh` for GitHub Issues.

## Shape the issue (the house form)

One issue = one outcome. Title: imperative and specific ("Add X", "Fix Y in Z").
Body — exactly these three sections:

- **What to do** — the concrete change, in a few sentences. Enough that a fresh
  session with zero chat context can act on it alone.
- **Constraints** — what to touch and, especially, what NOT to ("add only the new
  file", "don't modify existing tests/config", "no new dependencies"). This is
  your main lever for keeping a run small and safe.
- **Definition of done** — the observable end state, always ending in **a pull
  request against the repo's main branch** (one PR per issue). Say how to verify
  (tests pass, etc.).

Keep it minimal and unambiguous: a ticket session treats the issue as its whole
contract and does exactly what it says.

## File it

1. Create the issue on the project's team or repo in the **backlog/default**
   state — NOT the ready state. Intake produces a shaped issue; promoting it to
   dispatchable is board-plan's job.
2. **Fast path** (only if the request is already a single session-sized unit —
   one PR's worth, no decomposition): you may label it and move it to the ready
   state yourself. Use a `dispatch:` label (any `dispatch:*` works; Jam checks only that one is present, so use the project's existing one) for autonomous work that Jam's
   Requisitioner raises into a ticket session, or `class:attended` for
   supervised work the **board-attend** loop runs. Otherwise, hand off — tell
   the user it's ready for **board-plan**.
3. Post the issue URL back.

You implement nothing here. If the idea is large or fuzzy, file the shaped issue
and route to board-plan.
