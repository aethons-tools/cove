# CHECKPOINT — cove multi-backend sandboxes

**Written:** 2026-06-26
**Purpose:** Resume the cove sandboxes work in a fresh session after this
conversation's context is lost.
**Repo:** `/home/agent/workspace/sbx` (module `github.com/aethons-tools/cove`, binary `at-cove`).
**Branch:** `design/cove-sandboxes` (NOT merged to `main`).

> **THIS FILE IS THE DURABLE MEMORY.** In this prototype sandbox `/agent-data`
> is wiped when the session ends, so the agent's normal memory store
> (`/agent-data/projects/.../memory/`) does NOT persist. Everything needed to
> resume lives in this committed repo doc. If the workspace itself is also
> ephemeral, push this branch to a remote (egress to `github.com` is allowed).

## TL;DR — where we are

**UPDATE 2026-06-27 (newest) — git over HTTPS + GitHub PAT (was a deferred appendix).**
The sandbox egress only allows HTTPS via the proxy (.github.com:443);
SSH git (port 22) and git:// are dropped by nftables.
Both halves now ship in the non-overridable hardening layer:
- `/etc/gitconfig` `url."https://github.com/".insteadOf` rewrites
  `git@github.com:`, `ssh://git@github.com/`, `git://github.com/` → HTTPS (`769ca4d`).
- `/usr/local/bin/cove-git-credential.sh` credential helper (`6d70542`):
  authenticates github.com HTTPS with `GITHUB_TOKEN` from the connect-session env,
  memory-only (username=x-access-token, password=$GITHUB_TOKEN), never on disk;
  `helper = ""` reset drops inherited helpers; withholds creds (fails closed) when
  the token is unset.
  **To enable private repos, declare a `GITHUB_TOKEN` secret in `config.yml`** —
  but note managed `forceLoginMethod=claudeai` blocks `ANTHROPIC_API_KEY`, NOT
  `GITHUB_TOKEN`, so a git token secret is fine.
Guarded by embed content tests plus a real `git credential fill` test
(token supplied for github.com, withheld when unset; skips if git absent).
Remote is `git@github.com:aethons-tools/cove.git` (SSH);
this egress-locked dev box can't push (no key + port 22 blocked) — the USER pushes.

---

**UPDATE 2026-06-27 — real-ssh test harness + verified auth probe + setup script.**
- `claude` CLI is present in this dev sandbox;
  verified against it (v2.1.193):
  `claude auth login` is real (`--claudeai` is the default),
  and `claude auth status` exits 0 when logged in / non-zero otherwise.
- `connect.ensureAuthenticated` now probes via `claude auth status` instead of
  statting a credentials file (`12be572`) —
  validated, path-agnostic, retires the "confirm creds path" caveat.
  Login is explicit `claude auth login --claudeai`.
- **Real-ssh integration harness** (`dddac4d`),
  `internal/connect/integration_test.go`, build tag `integration`
  (run: `go test -tags integration ./internal/connect/ -v`).
  Boots an unprivileged throwaway sshd on loopback with a fake `claude`
  (via sshd `SetEnv PATH`) and drives it with the real ssh client;
  6 tests PASS here — Base connects, TOFU writes known_hosts,
  SendEnv delivers via AcceptEnv, StdinScript delivers via tmpfs,
  Connect logs in on first session and skips when authed.
  This runs here AND in CI (no Docker); default `go test ./...` stays hermetic.
- `scripts/setup-test-tools.sh` (`58ef34a`):
  installs podman + podman-docker (a `docker` shim for the Colima backend),
  shellcheck, hadolint, jq — Debian/Ubuntu, arch-aware, idempotent.
- Remaining gap needs a container runtime only:
  full `create`→container→`connect` against a real image
  (podman covers the SSH/lifecycle mechanics; the in-container nft/squid egress
  lockdown is the part to watch under rootless).

---

**UPDATE 2026-06-27 — `recreate` command added (`3347768`).**
`cove recreate [kit-dir] [--workspace|--ws <path>]` destroys the container and
creates it again while KEEPING the named volumes
(`<name>-state`→`/agent-data` with the saved OAuth login, and `<name>-workspace`),
because `Destroy` is `docker rm -f` (never `-v`).
It composes the existing Destroy + Create primitives at the CLI level
(no `Backend` interface change),
skips the destroy when no container exists (works from any state),
and honors `--workspace` like create.
Guarded by `TestDestroyKeepsVolumes` (colima: no `-v`/`--volumes`) plus
destroy-then-create ordering and skip-when-absent integration tests.
Workspace is durable (confirmed by the user),
so this is the UAT rebuild loop.

---

**UPDATE 2026-06-27 (latest) — POST-IMPLEMENTATION: OAuth + sshd + first-session login.**
Five follow-on commits after the 14 plan tasks (`9a23d11`, `6b539dd`, `ce7c6e1`, plus the two earlier doc commits),
all on `design/cove-sandboxes`,
suite/vet/build green,
`agent-infrastructure/` still byte-for-byte unchanged:
- **Optional plan Task 15 (stdin/tmpfs transport) is now DONE** and wired as the default in `doConnect` (`03fc820`);
  `SendEnv` remains the fallback type.
- **Require subscription OAuth** (`9a23d11`):
  shipped non-overridable `etc/claude-code/managed-settings.json` in the hardening layer with `forceLoginMethod=claudeai` (only *managed* settings enforce it and block API-key fallback);
  added `claude.ai` to the squid allow-list;
  dropped the contradictory `forceLoginMethod=console` from the overridable user settings.
  Implication:
  a kit must NOT inject `ANTHROPIC_API_KEY` on this path —
  managed settings *block startup* if it is present.
- **sshd is now the container main process** (`6b539dd`):
  the entrypoint had been dropping to `bash` and never starting sshd (nothing could connect).
  Now `exec /usr/sbin/sshd -D -e`,
  host keys via `ssh-keygen -A`,
  the state-volume seed is restart-safe (guarded by a `/agent-data/.seeded` marker — the old unconditional `cp -a` crashed under `set -e` on the second boot),
  and `CLAUDE_CONFIG_DIR=/agent-data` is exported via `/etc/environment` (pam_env) so every ssh session (incl. `ssh host 'exec claude'`) finds creds on the volume.
- **First-session OAuth login** (`ce7c6e1`):
  `connect.ensureAuthenticated` probes `test -f $CLAUDE_CONFIG_DIR/.credentials.json` over a non-interactive ssh and runs interactive `claude auth login` (PTY, via new `sshargs.Interactive`) only when unauthenticated;
  idempotent because creds persist on `/agent-data`.

Two things need confirming against a LIVE image (flagged in the commit messages, not hermetically testable here):
the exact login command `claude auth login` and the creds path `$CLAUDE_CONFIG_DIR/.credentials.json` (both one-line constants in `internal/connect/connect.go`);
and full end-to-end `connect` needs a Docker host —
the Go tests only guard the wiring (file presence, argv shape, login/skip logic).

---

**UPDATE 2026-06-27 (later) — IMPLEMENTATION COMPLETE.**
All 14 required plan tasks are implemented and committed on `design/cove-sandboxes`
(13 `feat`/`chore` commits, `58b658d`..`c3d1031`),
each via a fresh subagent (TDD).
Final verification all green:
`go test ./...` PASS (10 packages),
`go build -o at-cove .` OK,
`go vet ./...` clean,
`--dry-run create`/`connect` smoke OK,
and `agent-infrastructure/` byte-for-byte unchanged vs the pre-work baseline.
At the time of this note the optional plan Task 15 (stdin/tmpfs transport) was the
only remaining item; it has since been completed — see the "latest" block above.
The new packages:
runner(ext), kit(config/discover), sshargs, secret, backend(+colima), assemble(embed+layered), keys, connect(transport+orchestration), and a rewritten main.go.
The old `internal/sbx` and old `internal/kit/{build,create,template}.go` were retired in the final task.
Branch is NOT merged to `main`,
and there is still NO git remote (cannot push).

---

Brainstorming and planning are **done and committed**.
Implementation is **done** (see the COMPLETE note above).

**UPDATE 2026-06-27 — the blocker is RESOLVED. Ready to execute.**
- Go **1.26.4 (linux/arm64) is installed** (`go version` works).
- The `gopkg.in/yaml.v3` dependency fetch is **solved without any allowlist change**:
  `GOPROXY=direct` + `GOSUMDB=off` makes `go get` resolve `gopkg.in/yaml.v3@v3.0.1`
  through the allowlisted `github.com` (go-yaml/yaml).
  It is already downloaded and `go.sum` was generated then reverted to pristine;
  the module sits cached in `GOMODCACHE`.
  **Plan Task 2 stands as written** — no vendoring, no hand-rolled parser.
  (The three options in the old BLOCKER section below are moot.)
- `/home/agent` is NOT writable,
  so default `GOPATH=~/go` and `GOENV=~/.config/go/env` cannot be created.
  Fix: **GOPATH redirected to `/home/agent/workspace/.gopath`** (writable, outside the repo).
  `GOCACHE` (`~/.cache/go-build`) works by default.
- The Go env vars are written into **`$CLAUDE_CONFIG_DIR/settings.json`**
  (here `CLAUDE_CONFIG_DIR=/agent-data`, so `/agent-data/settings.json`)
  in the `env` block: `GOPATH`, `GOPROXY=direct`, `GOSUMDB=off`, `GOFLAGS=-mod=mod`.
  Two caveats: settings.json `env` is read at **session start** (not mid-session),
  AND `/agent-data` is **wiped at session end** (same reason this checkpoint, not
  agent memory, is the durable store) — so it is NOT a cross-session persistence
  mechanism. **The reliable way to run go here is to prefix the env inline:**
  `GOPATH=/home/agent/workspace/.gopath GOPROXY=direct GOSUMDB=off GOFLAGS=-mod=mod go test ./...`
  (Note: `~/.claude/` is NOT the config dir because `CLAUDE_CONFIG_DIR` overrides it.)
- Existing pre-work tests pass (`internal/runner`, `internal/sbx`, `internal/kit`).

The authoritative artifacts (read these first):

- Spec: `docs/superpowers/specs/2026-06-26-cove-sandboxes-design.md`
- Plan: `docs/superpowers/plans/2026-06-26-cove-sandboxes.md`

## Relevant commits on `design/cove-sandboxes`

```
4591b6a docs: implementation plan for multi-backend cove sandboxes
6eea3f4 docs: document secret-command injection risk and .local mitigation
bfe9cc1 docs: kit is a discoverable .cove/ directory
e58f834 docs: design for YAML-driven multi-backend cove sandboxes
724be68 feat: add cove CLI dispatcher with dry-run and exit-code propagation  (pre-existing)
```

The pre-existing `cove` (build/create/run/delete wrapping `sbx`) is described in
the older `docs/superpowers/specs/2026-06-22-cove-design.md`; the new spec
supersedes it.

## What was decided (so you don't relitigate)

- **cove owns the mechanism**; `agent-infrastructure/` ships image/kit files.
- **`agent-infrastructure/` is READ-ONLY.** Copy needed files into `sbx` and
  modify the copies there. (User's explicit instruction; enforced in the plan's
  Global Constraints + Task 7 + final verification.)
- **Multi-backend abstraction; SSH is the universal interface.** Scope of this
  spec = the `Backend` interface + uniform `connect` + the **Colima** backend.
  Firecracker and Fly are follow-on specs.
- **Kit = a directory** (`.cove/` at repo root, cwd walk-up discovery):
  `config.yml` + `image-files/` overlay + gitignored `.build/`.
- **`.build` assembly = layered overlays, last writer wins:** embedded
  overridable defaults → kit `image-files/` → (deferred `.local/`) → embedded
  non-overridable hardening. Hardening last = security boundary. Layers embedded
  via `embed.FS`. **No envsubst.**
- **Commands:** `build` / `create` (secret-free, `--workspace`/`--ws` selects
  shared bind-mount mode) / `connect` (ssh + secret env + claude) / `destroy` /
  `status`.
- **Secrets:** declared in `config.yml` by name + argv `command`; resolved
  just-in-time at `connect`, injected memory-only. Primary transport =
  stdin/tmpfs, fallback = `SendEnv` (both behind a `Transport` interface; plan
  ships `SendEnv` first, stdin as optional Task 15).
- **Managed SSH keypair** at `~/.config/cove/id_ed25519` (auto-generated);
  public half injected into the build context's `authorized_keys`.
- **Per-sandbox known_hosts TOFU** (`accept-new`).
- **Security note (documented, deferred):** a committed `secrets[].command` is a
  host-execution / supply-chain vector. Once `.local/` lands, committed
  `config.yml` carries `name`/`description` only and the `command` may come ONLY
  from the source-control-excluded `.local/config.yml`. `.local/` merge
  semantics are deferred (GitLab include/override cautionary tale).
- **Git PAT / `insteadOf`** for shared repos and **declarative repo clone** are
  documented as spec appendices, NOT built.

## Execution decision

User chose **Subagent-Driven execution** (superpowers:subagent-driven-development):
fresh subagent per task + review between tasks. The plan has 14 required TDD
tasks + 1 optional (stdin/tmpfs transport).

## THE BLOCKER — Go toolchain + locked egress

This environment is itself an egress-locked sandbox:
- HTTP(S) proxy at `127.0.0.1:3128`; allowlist permits only `.anthropic.com`,
  `.claude.com`, `.github.com`, `.githubusercontent.com`, `pypi.org`,
  `.pythonhosted.org`.
- `go` is NOT installed; `go.dev` and `storage.googleapis.com` are blocked
  (403/000); `apt` can't reach mirrors. Arch is `aarch64` (linux/arm64).

All plan tests are hermetic (drive `runner.Fake`; no Docker/ssh/network needed),
so **once `go` is on PATH the full `go test ./...` / `go build` loop works for
every task except the one dependency fetch:**

**Task 2 needs `gopkg.in/yaml.v3`**, but `gopkg.in` / `proxy.golang.org` /
`sum.golang.org` are not allowlisted (only `github.com` is). Resolve by ONE of:

1. **Allowlist + install:** add `gopkg.in` to the squid list, set `GOSUMDB=off`;
   `go get gopkg.in/yaml.v3@v3.0.1` then works. (User leaning toward installing
   Go on the image.)
2. **Vendor from GitHub (no allowlist change):** `github.com` is allowed — clone
   `go-yaml/yaml@v3.0.1` into a `vendor/` tree, build `-mod=vendor`. Adjust
   plan Task 2 accordingly.
3. **Drop the dependency:** hand-roll a small std-lib `config.yml` parser; binary
   becomes truly std-lib-only. Adjust plan Task 2 (rewrite `ParseConfig` without
   yaml.v3; the rest of the plan is unchanged).

**This choice is still OPEN — confirm with the user before starting Task 2.**

## How to resume (next session)

1. `cd /home/agent/workspace/sbx && git checkout design/cove-sandboxes`.
2. Verify the toolchain: `go version` (need >= 1.22). If missing, the user still
   needs to install it.
3. Read the spec and the plan (paths above).
4. Resolve the yaml.v3 dependency question (the three options above) with the
   user; if option 2 or 3, edit plan Task 2 first.
5. Sanity check: `go build ./... ` may fail until Task 1+ are done — that's fine.
   Existing pre-this-work tests: `go test ./internal/runner/ ./internal/sbx/ ./internal/kit/`
   (note: `internal/sbx` and old `internal/kit/{build,create,template}.go` get
   retired in plan Task 14).
6. Invoke **superpowers:subagent-driven-development** and execute the plan
   task-by-task, committing after each (the plan's commit steps already do this).
7. Keep `agent-infrastructure/` untouched; final step verifies
   `git -C ../agent-infrastructure status --porcelain` is clean.

## Other environment notes

- Git identity in this repo is set to `Brent <brent@lcbm.life>` (repo-local).
- Commit message trailers used here: `Co-Authored-By: Claude ...` (per the
  session's git convention).
- `just` recipes in `agent-infrastructure` are the old sbx-based flow; superseded
  but not to be edited.
