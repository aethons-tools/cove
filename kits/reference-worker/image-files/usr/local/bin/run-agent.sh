#!/bin/sh
# The agent harness. at-work prepare has written the task to .at-work/brief.md and
# cloned the repo into the cwd. Drive headless claude to do the work and write its
# self-report to .at-work/outcome.json. at-work complete reads that file; a missing
# or invalid outcome is treated as ERROR, so this script never has to synthesize one.
set -e

brief=$(cat .at-work/brief.md)

claude -p --dangerously-skip-permissions "$(cat <<PROMPT
$brief

---
Do the work described above in this repository: make the changes and run the
project's tests. When you are finished, write your outcome to .at-work/outcome.json
as ONE of these JSON objects (and nothing else in that file):

  {"status":"OK","pr-message":"<a concise PR description of what you did>"}
  {"status":"NEEDS_INPUT","needs-input":{"doing":"…","blocker":"…","need":"…","tried":"…"}}
  {"status":"ERROR","message":"<what went wrong>"}

Use OK only if the change is complete and tests pass. Use NEEDS_INPUT if you are
blocked on a decision only a human can make. Do not push or open a PR yourself —
that is handled after you exit.
PROMPT
)"
