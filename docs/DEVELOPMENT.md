# Development notes

Operational notes for building and testing `at-cove`,
including the workarounds needed inside the egress-locked dev sandbox this repo is developed in.
See [`OVERVIEW.md`](OVERVIEW.md) for the architecture and the `just` task list.

## The dev sandbox is egress-locked

This repo is developed inside a sandbox with no open internet:

- An HTTP(S) proxy at `127.0.0.1:3128` (already exported as `HTTP_PROXY`/`HTTPS_PROXY`).
- An allow-list permitting only a fixed set of domains —
  `.anthropic.com`, `.claude.com`, `claude.ai`, `.github.com`, `.githubusercontent.com`, `gopkg.in`, `pypi.org`, `.pythonhosted.org`.

Anything off the list is dropped,
which is what makes the Go toolchain settings below necessary.

## Go toolchain settings

Two host constraints shape how `go` is run here:

- **`proxy.golang.org` / `sum.golang.org` are not reachable** (only `github.com` and `gopkg.in` are allow-listed).
  Set `GOPROXY=direct` and `GOSUMDB=off` so `go get gopkg.in/yaml.v3@v3.0.1` resolves directly through the allow-listed hosts —
  no module proxy, no checksum DB.
  This is why the one dependency can be fetched without a vendor tree or an allow-list change.
- **`/home/agent` is not writable**, so the default `GOPATH` (`~/go`) can't be created.
  Redirect it to a writable path outside the repo: `GOPATH=/home/agent/workspace/.gopath`.
  (`GOCACHE` under `~/.cache` works by default.)

These are already exported in this environment.
If you need to set them inline (e.g. a fresh shell, or `settings.json` `env` not yet picked up), prefix the command:

```bash
GOPATH=/home/agent/workspace/.gopath GOPROXY=direct GOSUMDB=off GOFLAGS=-mod=mod go test ./...
```

`go` itself may not be on `PATH` in every shell — install it / add it before building.

## Tests

- `just test` (`go test ./...`) is **hermetic** —
  every test drives `internal/runner.Fake`,
  so no Docker, network, or live VM is required.
  Keep new tests this way.
- `just integration` (`go test -tags integration ./internal/connect/ -v`) runs the **real-ssh** suite:
  it boots an unprivileged throwaway `sshd` on loopback with a fake `claude`
  and exercises the transports and TOFU end-to-end with the real `ssh` client.
  Needs `ssh`/`sshd`/`ssh-keygen` but **not** Docker.
- `just setup` installs the optional dev tooling (podman + a `docker` shim, shellcheck, hadolint, jq).
- The remaining untested gap is a full `create`→container→`connect` against a real image,
  which needs a container runtime;
  podman covers the SSH/lifecycle mechanics,
  but the in-container `nftables`/Squid egress lockdown under rootless is the part to watch.

## CI — the merge gate

[`.github/workflows/gate.yml`](../.github/workflows/gate.yml) runs on every PR
to `main`, and on `main` itself. It runs the two hermetic checks —
`go test ./...` and [`scripts/lint.sh`](../scripts/lint.sh) — the same ones
`just test` and `just lint` run, through the same script, so CI and the local
loop cannot drift.

- The job is named **`gate`**, and that name is what branch protection lists as
  the required check. Renaming the job silently un-gates `main`: protection goes
  on waiting for a check that never reports. Rename only alongside the setting.
- CI runs lint as `STRICT=1 scripts/lint.sh`. Outside CI a missing `shellcheck`
  or `hadolint` is skipped with a note, so a fresh clone can lint before
  `just setup`; in CI that default would let the gate pass having linted
  nothing, so `STRICT=1` turns a missing linter into a failure.
- The workflow needs no `just` — the logic lives in `scripts/`, per the
  justfile's header.
- **Not** gated: `just integration` (real-ssh) and `just e2e` (live infra).
- CI leaves `GOPROXY`/`GOSUMDB` at their defaults. The `direct`/`off` settings
  above are a workaround for *this sandbox's* egress lock; a runner has open
  egress and should verify module checksums against `go.sum`.
- `.hadolint.yaml` records the two rules ignored for the hardening Dockerfile,
  each with its reason. Everything else fails the gate.

## Verified `claude` CLI facts

Checked against the `claude` CLI present in this sandbox (v2.1.x):

- `claude auth login --claudeai` is the real subscription-OAuth login command (`--claudeai` is the default).
- `claude auth status` exits `0` when logged in and non-zero otherwise —
  this is what `connect` probes to decide whether a first-session login is needed.

## Git / pushing

`origin` is `https://github.com/aethons-tools/cove.git`.
SSH git is impossible here — the egress lock drops port 22 —
so the hardening layer rewrites GitHub remotes to HTTPS
and a credential helper supplies the token from `GITHUB_TOKEN` in the session env
(see [`OVERVIEW.md`](OVERVIEW.md#authentication) for the mechanism).
A session whose kit does not declare that secret has no token,
and git fails closed rather than prompting;
this repo's kit declares it under `collaborators.<common>.secrets`
(see [at-cove-config.md](usage/at-cove-config.md#collaborators)).

`main` is protected: changes land through a PR whose **`gate`** check passes.
Direct pushes to `main` are rejected, including for admins.
Inside a *sandbox* the story is different —
the hardening rewrites GitHub remotes to HTTPS and feeds a `GITHUB_TOKEN` via a credential helper (see [`OVERVIEW.md`](OVERVIEW.md#authentication)).
