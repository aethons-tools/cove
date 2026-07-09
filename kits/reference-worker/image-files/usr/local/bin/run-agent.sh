#!/bin/sh
# The agent harness. at-work prepare has checked the repo out into the cwd (/home/agent/work)
# and the task spec is at .at-work/task.json. Drive headless claude to do the work and write
# its self-report to .at-work/worker-result.json. at-work complete reads that file; a missing
# or invalid worker-result becomes a structured error, so this script never synthesizes one.
set -e

claude -p --dangerously-skip-permissions "$(cat <<'PROMPT'
Your task is specified in .at-work/task.json in the current directory. Read that file:
the "task" -> "brief" field contains your instructions, and "repo" describes the
checked-out repository (already cloned into the cwd on the correct work branch).

Do the work described in this repository: make the changes and run the project's tests.
When you are finished, write your result to .at-work/worker-result.json as EXACTLY ONE
of these JSON objects (and nothing else in that file):

  {"status":{"ok":{"pull-request":{"title":"<PR title>","message":"<PR description>"}}}}
  {"status":{"needs-input":{"doing":"…","blocker":"…","need":"…","tried":"…"}}}
  {"status":{"error":{"message":"<what went wrong>"}}}

Use ok only if the change is complete and the tests pass (omit "pull-request" to push the
branch without opening a PR). Use needs-input if you are blocked on a decision only a human
can make. Do NOT push or open a PR yourself — that is handled after you exit.
PROMPT
)"
