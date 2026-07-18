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
# ~/.config/at-cove/secrets.yml (host-side, never committed)
minters:
  gh-cove:
    github:
      app-id: "123456"
      install-id: "7890"
      app-key: { value: /etc/cove/gh-app.pem }   # a path (non-secret) -> --app-key-file
  anthropic-cove:
    anthropic:
      oidc:
        auth0:
          tenant: your-tenant.us.auth0.com
          client-id: YOUR_CLIENT_ID
          audience: urn:cove:anthropic-wif
          client-secret: { command: ["pass", "cove/auth0-secret"] }   # from a manager
      federation:
        org: YOUR_ORG_UUID
        rule: fdrl_...
        service-account: svac_...
kits:
  reference-worker:
    AT_TASK_GIT_TOKEN:    { mint: gh-cove }
    ANTHROPIC_AUTH_TOKEN: { mint: anthropic-cove }
    AT_DISPATCH_TRACKER_TOKEN: { command: ["gh", "auth", "token"] }
```

at-cove builds the `at-mint` invocation from the profile: non-secret identifiers
become flags (including `--repo`, which at-cove fills from the kit's
`source-control.github.project`), and a `command:`/`global:`-sourced secret (the
Auth0 client secret, or an App key not given as a path) is passed to `at-mint` as
env in memory — never on argv. A bare `command: ["at-mint", "github", …]` still
works if you prefer to inline it (pass `--repo owner/name` yourself).

`~/.config/at-cove/secrets.local.yml` — keyed by this kit's **absolute path**,
not its name — overrides the above for name collisions (two checkouts sharing a
kit `name`) or temporary/test values; see
[at-cove-secrets.md](../../docs/usage/at-cove-secrets.md) for the full precedence
and the four supply sources (`value`/`command`/`global`/`mint`).

## Provisioning the GitHub App (credential minter)

The example above supplies `AT_TASK_GIT_TOKEN` by minting it from the `gh-cove`
`minters:` profile — at-cove runs `at-mint github` on the **at-cove host** (not
in the VM), assembling its flags/env from the profile, before each git step
(`at-task prepare` and `at-task complete`). A bare
`command: ["at-mint", "github", …]` (see [at-mint.md](../../docs/usage/at-mint.md))
is a manual alternative if you'd rather spell out the full invocation yourself.

1. **Create a GitHub App** with permissions `contents:write` and
   `pull_requests:write`, and **install it on your org** (so it can be scoped to
   any repo in that org at mint time).
2. **Set `app-id`/`install-id`/`app-key`** in the `minters:` profile above (an
   `app-key` given as a path, like `/etc/cove/gh-app.pem`, is non-secret and
   becomes `--app-key-file`; sourced from `command:`/`global:` instead, its
   content is passed as env).
3. **`at-mint` runs on the at-cove host**, not in the VM — it need not be on the
   VM's `PATH`, only the at-cove host's.

`at-mint` fails closed: a missing flag/env var or a failed API call aborts the
resolver, which aborts dispatch before any SSH happens.

**Why per-git-step, not per-run:** `at-cove` re-runs the resolver before *each*
git step, so the App-token's fixed ~1-hour TTL never bounds how long a dispatch
run may take — only the two git steps (`prepare`, `complete`) ever hold a token,
and each gets a freshly minted one. at-cove scopes the token via `at-mint github
--repo <kit's source-control.github.project>` (the installation itself, plus
`contents`+`pull_requests`, still bounds the maximum grantable scope). It also
passes the run's parameters — `COVE_RUN_REPO`, `COVE_RUN_ISSUE`, `COVE_RUN_CLASS`,
`COVE_RUN_TIMEOUT` — into the resolver's environment for any custom `command:`
resolver that wants them (`at-mint` itself takes `--repo`, not the env var).

## Run
```
E2E_REPO=<org>/<scratch-repo> go test -tags integration ./internal/dispatchrun/ -run TestE2EReferenceWorker -v
```
or, hand-run — compile the kit once, then run a unit of work against the pre-built
image (`work` never builds; it consumes what `install` produced):
```
at-cove install --project-dir kits/reference-worker
at-cove work --project-dir kits/reference-worker \
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
