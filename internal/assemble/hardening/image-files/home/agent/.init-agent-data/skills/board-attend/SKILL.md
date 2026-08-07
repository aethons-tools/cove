---
name: board-attend
description: Use when you want an interactive session to continuously pull and work this repo's board tickets labeled `class:attended`, with you in the loop — the attended, human-supervised counterpart to the autonomous `at-cove dispatch` scheduler. Triggers: "work my attended queue", "loop over the attended tickets", "run an interactive agent that finds and works class:attended work while I supervise". Run it under `/loop`.
---

# board-attend

Continuously work the board's **attended** queue in an interactive session, one
ticket at a time, with the user present. Each iteration claims one ready
`class:attended` ticket and carries it to a PR; `/loop` repeats the iteration.

This is the **attended** counterpart to `at-cove dispatch`: the autonomous
scheduler only handles `workers.*` classes and never sees `class:attended`
tickets, so the two paths share the board without colliding on work. You plan
nothing new here and file nothing new here — you *work* tickets that already
exist.

## Read the board's parameters first

As in the other `board-*` skills, read `.at-cove/config.yml` → `tracker.linear`
for `team`, the `states` map (which real state name means `ready`,
`in-progress`, `in-review`, `needs-input`, `blocked`, `done`), and
`class-label-prefix` (default `class:`). Do all board reads/writes through your
Linear connector. Never invent a team, state, or label — read them.

## Each iteration works one attended ticket

1. **Find the next ticket.** Query the board for issues in the `ready` state
   carrying the `class:attended` label. **Skip any with an unfinished blocker**
   (a `blocked-by` relation whose blocker isn't `done`) — same "ready =
   dispatchable now" rule the scheduler uses. Pick by priority, then oldest.
2. **Nothing ready → stop this iteration.** Say so plainly and let `/loop` wait
   for the next tick. Do not invent work or pull a non-`attended` ticket.
3. **Claim it, then announce.** Transition the ticket `ready` → `in-progress` and
   tell the user which one you took, with its link — you don't need to wait for
   their go-ahead first; claiming is what makes you the single writer, and the
   conversation happens while you work it (step 5). (A collision with another
   attender is possible and accepted for now; claiming first just narrows the
   window.)
4. **Start clean from `main`.** `git checkout main`, pull, then cut a fresh
   branch. Attended work always starts from an up-to-date `main`.
5. **Work it with the user, via board-execute.** Use the **board-execute** skill
   (collaborator entry). A `class:attended` ticket is the sanctioned scope for
   *supervised* interactive work — including feature/greenfield work — because a
   human is in the loop. When something is unclear or blocked, **ask the user
   inline and continue**; do not park the ticket in `needs-input`.
6. **Finish at `in-review`.** Open the PR against `main` via your connector, link
   it on the ticket, and transition `in-progress` → `in-review`. **Stop there** —
   never merge and never move to `done`. The human reviews and merges, so review
   stays independent of the hand that wrote the change.
7. **Loop** to the next ticket.

## Deliberately unlike the autonomous path

- **`needs-input` is resolved by asking you**, not by parking the ticket.
- **No reaper** — you manage an abandoned or stuck claim; nothing reaps it.
- **Collision on claim is accepted** for now; the claim narrows but doesn't close
  the window.
- **You are the reviewer** — the loop ends at `in-review`, never `done`.

## Launch

Run in an interactive collaborator session:

```
claude "/loop board-attend"
```

The loop only advances while the session is attended — which is the point: a
human is present to answer, steer, and review. When you hit a wall you can't
resolve with the user, stop and surface it; don't thrash or silently widen scope.
