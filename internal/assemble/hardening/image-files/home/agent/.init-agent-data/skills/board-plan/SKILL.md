---
name: board-plan
description: Use when decomposing a Linear issue into dispatchable, worker-sized sub-issues (or sizing a single issue) and moving them to the ready state so the at-cove scheduler picks them up. This is the "turn a plan into tickets a worker implements — do NOT implement it here" exit ramp of a planning session.
---

# board-plan

Take an issue and make it — or its parts — **dispatchable**: each unit sized for
one autonomous worker run, correctly labeled and stated so the scheduler dispatches
it. You plan; workers build. You write no feature code here.

## Read the board's parameters first

As in board-intake, read `.at-cove/config.yml` → `tracker.linear` for `team`, the
`states` map, and `class-label-prefix`. Handler class names are the kit's
`workers:` keys (autonomous, scheduler-dispatched) and `collaborators:` keys
(interactive, human-driven in `chat`). Operate on Linear via your connector.

## Size each unit for one worker run

A dispatched worker does one thing and opens **one PR** (branch-first, never the
default branch). So each dispatchable issue must be:

- **One PR's worth** — a change a competent implementer finishes and tests in a
  single focused run. Needs more than one PR? Split it.
- **Self-contained** — carries its own What to do / Constraints / Definition of
  done (the board-intake shape). A worker sees only the issue, no chat context.
- **Independently mergeable** where you can — minimize cross-issue ordering; record
  genuine dependencies with Linear blocking relations.

## Decompose

- **Single worker-sized issue** → size-check it, ensure it's in the intake shape,
  apply `class:<worker-class>`, move it to `ready`. Done.
- **Larger issue / epic** → keep it as the tracking parent (leave it in a
  non-`ready` state); create a Linear sub-issue per worker-sized unit, each in the
  intake shape, each labeled `class:<worker-class>` and moved to `ready`. Add
  blocking relations for real ordering.

Match the `class:` label to the **lane** that will work the unit:

- **Autonomous** — a self-contained unit a headless worker finishes alone → a
  worker class (e.g. the implement class). `at-cove dispatch` picks these up from
  `ready` and runs the bracket. Use this whenever the issue's contract is complete
  enough to execute with **no human in the loop**.
- **Attended** — a unit that wants a human in the loop (UX/design choices,
  exploratory or judgment-heavy work, or anything you'd want to supervise) →
  `class:attended`. The **board-attend** loop works these from `ready` in an
  interactive session. Unlike an arbitrary interactive-class label (which nothing
  pulls), a `class:attended` unit in `ready` *does* get worked — so route
  supervised work here rather than leaving it inert.

Keep separation of duties: the class that implements an issue should not be the one
that reviews its PR. (In the attended lane the human present is the reviewer — the
board-attend loop stops at `in-review` and never merges.) A pure spec/review step
handled by a specific collaborator class is still human-driven in `chat` and not
run by the autonomous scheduler.

## Hand off to dispatch

Once the `ready` + `class:<worker>` issues exist, the running `at-cove dispatch`
scheduler picks each up and runs the bracket (prepare → agent → complete → PR).
You are done — do not implement. Post the list of dispatchable issue URLs.
