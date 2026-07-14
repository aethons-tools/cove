---
name: board-execute
description: The implementation procedure for a single board issue — used by a dispatched worker in the autonomous agent step, and by a collaborator making a direct fix during review or troubleshooting. Make exactly the change the issue describes, keep it minimal, verify it, and end in one PR against main.
---

# board-execute

Implement one issue. Two entry points share this procedure:

- **Dispatched worker** (autonomous, headless): at-cove has already run
  `at-task prepare` (cloned the repo, cut a fresh branch) and will run
  `at-task complete` (commit, push, open the PR) after you. You make the change and
  verify — you do **not** run git yourself, and you never push to the default
  branch.
- **Collaborator direct fix** (interactive, during review/troubleshooting): you
  make a small fix in place and open/update the PR yourself via the GitHub
  connector. Do this **only** for review/troubleshoot fixes — greenfield and
  feature work go through board-intake → board-plan → dispatch, not here.

## Procedure

1. **The issue is the whole contract.** Its What to do / Constraints / Definition
   of done are authoritative. Do exactly that — no more (honor the Constraints;
   don't touch what it says not to), no less.
2. **Make the change**, matching the repo's conventions and surrounding code. Use
   the repo's own sub-skills where they apply (e.g. test-driven-development).
3. **Verify** — run the project's tests and the issue's stated checks. It isn't
   done until they pass.
4. **Stay minimal and focused** — one issue, one PR's worth. Don't scope-creep into
   unrelated fixes you notice; capture those as new issues via board-intake.
5. **Finish**:
   - *Worker* — leave a green, minimal diff committed-ready; `at-task complete`
     opens the PR (branch-first, against main, one PR per issue). Your job ends at
     the verified diff.
   - *Collaborator* — open or update the PR against main via the GitHub connector
     and link it on the issue.

## When you hit a wall

Don't guess or thrash. Stop and write back: record what you tried and the specific
question or blocker as a comment on the issue, and move it to the `needs-input`
state (read the real state name from `.at-cove/config.yml` →
`tracker.linear.states`). A human moving it back to `ready` is the "answered —
resume" signal. Never silently widen scope to route around a blocker.
