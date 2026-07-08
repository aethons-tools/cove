#!/bin/sh
# The kit's dispatch.command. at-cove runs this in the container with the kit's
# secrets in the environment and /in/input.json present. It sequences the git/PR
# worker around the agent, stripping the token for the agent step (the air-gap).
#
# at-work complete ALWAYS runs — even if prepare or the agent fails — so a
# structured output.json is always produced (at-work maps a missing/invalid
# outcome to a structured ERROR). A failed prepare skips the agent but still
# completes; a nonzero agent exit is tolerated so completion is never skipped.
set -e

# Only run the agent if prepare succeeded (a clean, ready checkout). The agent
# runs WITHOUT the code-host token (the air-gap); its failure is tolerated so
# `at-work complete` below always runs.
if at-work prepare /in/input.json; then
	env -u AT_WORK_GIT_TOKEN sh -c "$AT_WORK_AGENT_COMMAND" || true
fi

at-work complete /in/input.json /out/output.json
