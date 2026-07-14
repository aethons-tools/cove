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
    AT_TASK_GIT_TOKEN:
      command: ["at-mint", "github", "--app-id", "123456", "--install-id", "7890",
                "--app-key-file", "/etc/cove/gh-app.pem"]
    ANTHROPIC_AUTH_TOKEN:
      command: ["at-mint", "anthropic",
                "--auth0-tenant", "your-tenant.us.auth0.com",
                "--auth0-client-id", "YOUR_CLIENT_ID",
                "--auth0-audience", "urn:cove:anthropic-wif",
                "--anthropic-org", "YOUR_ORG_UUID",
                "--anthropic-rule", "fdrl_...",
                "--anthropic-service-account", "svac_..."]
    AT_DISPATCH_TRACKER_TOKEN: { global: linear-token }
    AT_DISPATCH_WEBHOOK_SECRET: { value: "whsec_..." }
```

`at-mint github` needs the App private key — pass a path with `--app-key-file`
(non-secret), or set `AT_MINT_GITHUB_APP_KEY` (PEM content) in the host env.
`at-mint anthropic` reads the Auth0 client secret from
`AT_MINT_AUTH0_CLIENT_SECRET` in the host env (Plan 3's `mint:` profiles will
source it from a manager instead). `COVE_RUN_REPO` is set by at-cove per run;
you do not pass it.

`~/.config/at-cove/secrets.local.yml` — keyed by this kit's **absolute path**,
not its name — overrides the above for name collisions (two checkouts sharing a
kit `name`) or temporary/test values; see
[at-cove-secrets.md](../../docs/usage/at-cove-secrets.md) for the full precedence
and the four supply sources (`value`/`command`/`global`, and the forward-looking
`mint:`, not yet runnable).

## Provisioning the GitHub App (credential minter)

The example above supplies `AT_TASK_GIT_TOKEN` by running `at-mint github` on
the **at-cove host** (not in the VM) as a `command:` source — it mints a
fresh, repo-scoped GitHub App installation token before each git step
(`at-task prepare` and `at-task complete`). Plan 3's `mint:` profiles will let
the kit demand this without spelling out the full command; until then, the
`command:` form above is how you wire it up.

1. **Create a GitHub App** with permissions `contents:write` and
   `pull_requests:write`, and **install it on your org** (so it can be scoped to
   any repo in that org at mint time).
2. **Pass `--app-id`/`--install-id`/`--app-key-file`** in the `command:` entry
   above (the App key path is non-secret); or set `AT_MINT_GITHUB_APP_KEY` (PEM
   content) in the host env instead of `--app-key-file`.
3. **`at-mint` runs on the at-cove host**, not in the VM — it need not be on the
   VM's `PATH`, only the at-cove host's.

`at-mint` fails closed: a missing flag/env var or a failed API call aborts the
resolver, which aborts dispatch before any SSH happens.

**Why per-git-step, not per-run:** `at-cove` re-runs the resolver before *each*
git step, so the App-token's fixed ~1-hour TTL never bounds how long a dispatch
run may take — only the two git steps (`prepare`, `complete`) ever hold a token,
and each gets a freshly minted one. `at-cove` also passes the run's parameters —
`COVE_RUN_REPO`, `COVE_RUN_ISSUE`, `COVE_RUN_CLASS`, `COVE_RUN_TIMEOUT` — into the
resolver's environment; `at-mint github` reads `COVE_RUN_REPO` to scope the
token to that repo (the installation itself, plus `contents`+`pull_requests`,
still bounds the maximum grantable scope).

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
