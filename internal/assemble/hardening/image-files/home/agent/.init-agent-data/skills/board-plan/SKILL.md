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

Match the `class:` label to the intended handler: an implementation unit → the
implement worker class; a spec/review step → the matching interactive class (those
are driven by a human in `chat`, not the autonomous scheduler — so don't expect the
scheduler to run them). Keep separation of duties: the class that implements an
issue should not be the one that reviews its PR.

## Hand off to dispatch

Once the `ready` + `class:<worker>` issues exist, the running `at-cove dispatch`
scheduler picks each up and runs the bracket (prepare → agent → complete → PR).
You are done — do not implement. Post the list of dispatchable issue URLs.
