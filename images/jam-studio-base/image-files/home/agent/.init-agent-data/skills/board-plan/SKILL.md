---
name: board-plan
description: Use when decomposing an issue into dispatchable, session-sized sub-issues (or sizing a single issue) and moving them to the ready state so Jam's Requisitioner picks them up. This is the "turn a plan into tickets a session implements — do NOT implement it here" exit ramp of a planning session.
---

# board-plan

Take an issue and make it — or its parts — **dispatchable**: each unit sized for
one ticket session, correctly labeled and in the ready state. You plan; ticket
sessions build. You write no feature code here.

## Learn the board's parameters first

As in board-intake: the tracker (a Linear team or a GitHub repo), its state
names, and the `class:` and `dispatch:` label prefixes come from your session
context (the Project layer) or the ticket. If they are not there, ask a human;
never guess. Read and write the board through your session's connections (the
Linear MCP or API, or `gh` for GitHub Issues).

## How dispatch works

Jam's Requisitioner polls the tracker for issues in the ready state that carry a
`dispatch:` label. It claims each one (ready → in-progress) and raises one ticket
session per issue, with the issue as the session's brief. An issue with an
unfinished blocker is not dispatchable: record blockers as Linear blocking
relations, or as `Depends on #N` in a GitHub issue body.

## Size each unit for one ticket session

A ticket session does one thing and opens **one PR** (branch-first, never the
default branch). So each dispatchable issue must be:

- **One PR's worth** — a change a competent implementer finishes and tests in a
  single focused run. Needs more than one PR? Split it.
- **Self-contained** — carries its own What to do / Constraints / Definition of
  done (the board-intake shape). A session sees only the issue, no chat context.
- **Independently mergeable** where you can — minimize cross-issue ordering; record
  genuine dependencies as blockers.

## Decompose

- **Single session-sized issue** → size-check it, ensure it's in the intake
  shape, label it, move it to the ready state. Done.
- **Larger issue / epic** → keep it as the tracking parent (leave it out of the
  ready state); create a sub-issue per session-sized unit, each in the intake
  shape, each labeled and moved to ready. Add blockers for real ordering.

Pick the **lane** for each unit and label it accordingly:

- **Autonomous** — a self-contained unit a session finishes alone → a
  `dispatch:` label. Jam's Requisitioner picks it up from the ready state. Use
  this whenever the issue's contract is complete enough to execute with **no
  human in the loop**.
- **Attended** — a unit that wants a human in the loop (UX/design choices,
  exploratory or judgment-heavy work, or anything you'd want to supervise) →
  `class:attended` and **no** `dispatch:` label. The **board-attend** loop works
  these from the ready state in an interactive session; because they carry no
  `dispatch:` label, Jam never raises them.

Keep separation of duties: the session that implements an issue should not be
the one that reviews its PR. (In the attended lane the human present is the
reviewer — the board-attend loop stops at the in-review state and never merges.)

## Hand off to dispatch

Once the ready issues exist with their labels, Jam's Requisitioner picks up the
`dispatch:` ones on its next poll, and the board-attend loop takes the
`class:attended` ones. You are done — do not implement. Post the list of issue
URLs.
