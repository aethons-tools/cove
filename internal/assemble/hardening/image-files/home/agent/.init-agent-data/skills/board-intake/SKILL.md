---
name: board-intake
description: Use when turning a raw idea, bug report, or feature request into ONE well-formed Linear issue on this repo's board — captured in the house task shape (what to do / constraints / definition-of-done → a PR) so a dispatched worker can pick it up. Use it whenever you're about to "file a ticket", "add this to the board", or "open an issue" for this repo.
---

# board-intake

Turn an idea into one crisp, self-contained Linear issue. You capture *intent and
shape*, never implementation. Sizing and dispatch belong to board-plan; here you
produce a well-formed issue it (or a worker) can act on.

## Read the board's parameters first

The board's conventions live in this repo's kit config, not in your head. Read
`.at-cove/config.yml` → `tracker.linear`:

- `team` — the Linear team to file under.
- `states` — the lifecycle map: which *real* state name means `ready`,
  `in-progress`, `in-review`, `done`, `needs-input`, `blocked`.
- `class-label-prefix` (default `class:`) — the label prefix that routes an issue
  to a handler. The class names are the kit's `workers:` keys (autonomous) and
  `collaborators:` keys (interactive).

Do all board reads/writes through your Linear connector (you have it via your
account). Never invent a team, state, or label — read them.

## Shape the issue (the house form)

One issue = one outcome. Title: imperative and specific ("Add X", "Fix Y in Z").
Body — exactly these three sections:

- **What to do** — the concrete change, in a few sentences. Enough that a fresh
  worker with zero chat context can act on it alone.
- **Constraints** — what to touch and, especially, what NOT to ("add only the new
  file", "don't modify existing tests/config", "no new dependencies"). This is
  your main lever for keeping a dispatched run small and safe.
- **Definition of done** — the observable end state, always ending in **a pull
  request against the repo's main branch** (one PR per issue). Say how to verify
  (tests pass, etc.).

Keep it minimal and unambiguous: a dispatched worker treats the issue as its whole
contract and does exactly what it says.

## File it

1. Create the issue in `team`, in the **backlog/default** state — NOT the `ready`
   state. Intake produces a shaped issue; promoting it to dispatchable is
   board-plan's job.
2. **Fast path** (only if the request is already a single worker-sized unit — one
   PR's worth, no decomposition): you may apply the `class:<handler>` label and
   move it to the `ready` state yourself. Pick the lane as board-plan describes — a
   worker class (e.g. `class:implementor`) for autonomous dispatch, or
   `class:attended` for supervised work the **board-attend** loop runs. Otherwise,
   hand off — tell the user it's ready for **board-plan**.
3. Post the issue URL back.

You implement nothing here. If the idea is large or fuzzy, file the shaped issue
and route to board-plan.
