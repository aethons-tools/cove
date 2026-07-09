#!/bin/sh
# The kit's dispatch.command. at-cove runs this in the container with the kit's secrets
# in the environment and .at-work/task.json already injected under /home/agent/work. It
# sequences the git/PR worker around the agent, stripping the token for the agent step
# (the air-gap).
#
# `at-work complete` ALWAYS runs — even if prepare or the agent fails — and always writes
# .at-work/task-result.json (a missing/unreadable task, or a missing/invalid worker-result,
# becomes a structured error). A failed prepare skips the agent but still completes; a
# nonzero agent exit is tolerated so completion is never skipped.
set -e

cd /home/agent/work

# Only run the agent if prepare succeeded (a clean, ready checkout). The agent runs
# WITHOUT the code-host token (the air-gap); its failure is tolerated so `at-work
# complete` below always runs.
if at-work prepare; then
	env -u AT_WORK_GIT_TOKEN sh -c "$AT_WORK_AGENT_COMMAND" || true
fi

at-work complete
