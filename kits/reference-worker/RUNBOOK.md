# Reference worker — end-to-end runbook

Runs the whole worker path on real infra: `at-cove dispatch` → a fresh hardened
container → `claude` implements the task → `at-task` opens a PR. Cannot run in the
egress-locked dev sandbox (no docker/claude/GitHub); run it on a machine with:

## Prerequisites
- **Colima** running (`colima start`) — the `at-cove` backend.
- **The GitHub App minter provisioned** — see below; then supply
  `AT_TASK_GIT_TOKEN` and the other demanded secrets in
  `~/.config/at-cove/secrets.yml` (see "Supplying secrets" below).
- **A seeded `claude` login** — run `at-cove connect` once and log in; `at-cove`
  saves the credentials and `dispatch` seeds them into the worker container.
- **A scratch GitHub repo** you can push branches / open PRs on, with a `main` branch.
- The kit **completed** for your target: base image, `claude` install, pinned
  `at-task` ref, target toolchain, and the full `allowed-domains` in `config.yml`.

## Supplying secrets (machine-side, never committed)

This kit only **demands** secrets — `config.yml`'s `secrets:` maps carry a name
and a `description:`, never a command or a value. Every value is supplied on the
**at-cove host**, out of source control, in
`~/.config/at-cove/secrets.yml`, keyed by this kit's `name` (`reference-worker`):

```yaml
# ~/.config/at-cove/secrets.yml
global:                                                    # shared supplies; inert until delegated
  linear-token: { command: ["gh", "auth", "token"] }

kits:
  reference-worker:
    AT_TASK_GIT_TOKEN:         { command: ["kits/reference-worker/mint-github-token.sh"] } # mint: coming later
    ANTHROPIC_AUTH_TOKEN:      { command: ["your-anthropic-mint.sh"] }                     # mint: coming later
    AT_DISPATCH_TRACKER_TOKEN: { global: linear-token }
    AT_DISPATCH_WEBHOOK_SECRET: { value: "whsec_..." }
```

`~/.config/at-cove/secrets.local.yml` — keyed by this kit's **absolute path**,
not its name — overrides the above for name collisions (two checkouts sharing a
kit `name`) or temporary/test values; see
[at-cove-secrets.md](../../docs/usage/at-cove-secrets.md) for the full precedence
and the four supply sources (`value`/`command`/`global`, and the forward-looking
`mint:`, not yet runnable).

## Provisioning the GitHub App (credential minter)

The example above supplies `AT_TASK_GIT_TOKEN` by running
`kits/reference-worker/mint-github-token.sh` on the **at-cove host** (not in the
VM) as a `command:` source — it mints a fresh, repo-scoped GitHub App
installation token before each git step (`at-task prepare` and `at-task
complete`). A future plan replaces this script with a structured `mint:` profile
(`at-mint github`); until then it's a working `command:` example.

1. **Create a GitHub App** with permissions `contents:write` and
   `pull_requests:write`, and **install it on your org** (so it can be scoped to
   any repo in that org at mint time).
2. **Reference `mint-github-token.sh` by path** (absolute, or relative to the
   at-cove host's cwd) in the `command:` entry above — it need not be on `PATH`.
3. **Export on the at-cove host** (never committed, never in the kit):
   - `COVE_GH_APP_ID` — the App's ID.
   - `COVE_GH_INSTALL_ID` — the installation ID on your org.
   - `COVE_GH_APP_KEY` — path to the App's private key (`.pem`).

The script fails closed (`set -eu` + `:?` guards): a missing var or a failed API
call aborts the resolver, which aborts dispatch before any SSH happens.

**Why per-git-step, not per-run:** `at-cove` re-runs the resolver before *each*
git step, so the App-token's fixed ~1-hour TTL never bounds how long a dispatch
run may take — only the two git steps (`prepare`, `complete`) ever hold a token,
and each gets a freshly minted one. `at-cove` also passes the run's parameters —
`COVE_RUN_REPO`, `COVE_RUN_ISSUE`, `COVE_RUN_CLASS`, `COVE_RUN_TIMEOUT` — into the
resolver's environment; the minter reads `COVE_RUN_REPO` to scope the token to
that repo (the installation itself, plus `contents`+`pull_requests`, still bounds
the maximum grantable scope).

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
bracket. It injects the task file at `/home/agent/work/.at-task/task.json`, runs
`at-task prepare` there, then runs `claude -p "<the class prompt, plus a result
protocol appended, with any secret tokens stripped>"`, then runs `at-task complete`,
and finally extracts `/home/agent/work/.at-task/task-result.json` as the dispatch
result.

## Expected
- A new PR on the scratch repo implementing the brief.
- `task-result.json` with `status.ok` set and `status.ok.pr-url` present.
- A `needs-input` or `error` status means the agent stopped or failed — read
  `task-result.json`'s `status` block (and the worker's own `.at-task/worker-result.json`
  inside the container, if you need more detail).
