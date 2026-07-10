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
  --in kits/reference-worker/testdata/task.json --out /tmp/task-result.json --timeout 20m
```
(edit `testdata/task.json`'s `repo.name` to your scratch repo first).

The kit declares `workers.implement.prompt` in `config.yml`; `at-cove` owns the whole
bracket. It injects the task file at `/home/agent/work/.at-work/task.json`, runs
`at-work prepare` there, then runs `claude -p "<the class prompt, plus a result
protocol appended, with any secret tokens stripped>"`, then runs `at-work complete`,
and finally extracts `/home/agent/work/.at-work/task-result.json` as the dispatch
result.

## Expected
- A new PR on the scratch repo implementing the brief.
- `task-result.json` with `status.ok` set and `status.ok.pr-url` present.
- A `needs-input` or `error` status means the agent stopped or failed — read
  `task-result.json`'s `status` block (and the worker's own `.at-work/worker-result.json`
  inside the container, if you need more detail).
