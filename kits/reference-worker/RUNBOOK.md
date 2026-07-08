# Reference worker — end-to-end runbook

Runs the whole worker path on real infra: `at-cove dispatch` → a fresh hardened
container → `claude` implements the task → `at-work` opens a PR. Cannot run in the
egress-locked dev sandbox (no docker/claude/GitHub); run it on a machine with:

## Prerequisites
- **Colima** running (`colima start`) — the `at-cove` backend.
- **`gh auth login`** — the `AT_WORK_GIT_TOKEN` resolver is `gh auth token`.
- **A seeded `claude` login** — run `at-cove connect` once and log in; `at-cove`
  saves the credentials and `dispatch` seeds them into the worker container.
- **A scratch GitHub repo** you can push branches / open PRs on, with a `main` branch.
- The kit **completed** for your target: base image, `claude` install, pinned
  `at-work` ref, target toolchain, and the full `allowed-domains` in `config.yml`.

## Run
```
E2E_REPO=<org>/<scratch-repo> go test -tags integration ./internal/dispatchrun/ -run TestE2EReferenceWorker -v
```
or, hand-run:
```
at-cove dispatch kits/reference-worker \
  --in kits/reference-worker/testdata/input.json --out /tmp/output.json --timeout 20m
```
(edit `testdata/input.json`'s `repo.name` to your scratch repo first).

## Expected
- A new PR on the scratch repo implementing the brief.
- `output.json` with `"status":"OK"` and `work.pr-url` set.
- A `NEEDS_INPUT` or `ERROR` status means the agent stopped or failed — read
  `output.json`'s `agent`/`work` blocks.
