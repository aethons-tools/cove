---
name: board-execute
description: The implementation procedure for a single board issue — used by a ticket session raised by Jam, and by a collaborator making a direct fix during review or troubleshooting. Make exactly the change the issue describes, keep it minimal, verify it, and end in one PR against main.
---

# board-execute

Implement one issue. Three entry points share this procedure:

- **Ticket session** (raised by Jam's Requisitioner): your prompt is the issue
  brief. You own the work end to end — clone or enter the repo, cut a fresh
  branch (branch first; never work on or push to the default branch), make the
  change, verify, push, and open the PR yourself, then see it through to merge.
  Keep the ticket current with `report(state, summary, pr?)` as its state
  changes (`in-progress`, `in-review` with the PR url, `needs-input`, `blocked`,
  `done`). Talk in your own session channel (`send` without `to`); `call_in`
  the people you need. Call `end` only once your work has landed (merged, or the
  PR handed off) and is reported — never end with work that is not pushed. While
  the PR is open, `alarm_set` a PR-watch alarm so you are woken for review
  comments, failing CI, or the merge instead of polling.
- **Collaborator direct fix** (interactive, during review/troubleshooting): you
  make a small fix in place and open/update the PR yourself with `gh` or your
  connection. Do this **only** for review/troubleshoot fixes — greenfield and
  feature work go through board-intake → board-plan → dispatch, not here.
- **Attended loop** (interactive, driven by **board-attend**): you work a
  `class:attended` ticket with the user present. Because the work is *supervised*,
  feature/greenfield work is legitimate here — the review/troubleshoot-only limit
  above is for *unsupervised* collaborator fixes. board-attend owns the surrounding
  claim → start-from-`main` → PR → in-review flow; this skill is just the "make
  the change and verify" core.

## Procedure

1. **The issue is the whole contract.** Its What to do / Constraints / Definition
   of done are authoritative. Do exactly that — no more (honor the Constraints;
   don't touch what it says not to), no less.
2. **Branch first.** Start from an up-to-date `main` on a fresh branch.
3. **Make the change**, matching the repo's conventions and surrounding code. Use
   the repo's own sub-skills where they apply (e.g. test-driven-development).
4. **Verify** — run the project's tests and the issue's stated checks. It isn't
   done until they pass.
5. **Stay minimal and focused** — one issue, one PR's worth. Don't scope-creep into
   unrelated fixes you notice; capture those as new issues via board-intake.
6. **Finish**: push the branch and open the PR against main (one PR per issue),
   and link it on the issue. A ticket session reports `in-review` with the PR and
   follows it to merge.

## When you hit a wall

Don't guess or thrash, and don't exit. A ticket session calls
`report(needs-input, <the specific question or blocker, and what you tried>)`
(use `blocked` if it cannot proceed at all), says the same in its session
channel, `call_in`s whoever can help, and ends its turn to wait — its studio
and work stay alive, and escalation brings people to it. A human's reply wakes
it to resume. Otherwise comment on the issue and move it to the needs-input
state. Never silently widen scope to route around a blocker.
