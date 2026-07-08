#!/bin/sh
# The kit's dispatch.command. at-cove runs this in the container with the kit's
# secrets in the environment and /in/input.json present. It sequences the git/PR
# worker around the agent, stripping the token for the agent step (the air-gap).
set -e

at-work prepare  /in/input.json

# The untrusted-brief-ingesting agent runs WITHOUT the code-host token.
env -u AT_WORK_GIT_TOKEN  run-agent.sh

at-work complete /in/input.json /out/output.json
