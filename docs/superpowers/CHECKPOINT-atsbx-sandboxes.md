# CHECKPOINT — atsbx multi-backend sandboxes

**Written:** 2026-06-26
**Purpose:** Resume the atsbx sandboxes work in a fresh session after this
conversation's context is lost.
**Repo:** `/home/agent/workspace/sbx` (module `github.com/aethons-tools/at-sbx`, binary `atsbx`).
**Branch:** `design/atsbx-sandboxes` (NOT merged to `main`).

> **THIS FILE IS THE DURABLE MEMORY.** In this prototype sandbox `/agent-data`
> is wiped when the session ends, so the agent's normal memory store
> (`/agent-data/projects/.../memory/`) does NOT persist. Everything needed to
> resume lives in this committed repo doc. If the workspace itself is also
> ephemeral, push this branch to a remote (egress to `github.com` is allowed).

## TL;DR — where we are

**UPDATE 2026-06-27 (later) — IMPLEMENTATION COMPLETE.**
All 14 required plan tasks are implemented and committed on `design/atsbx-sandboxes`
(13 `feat`/`chore` commits, `58b658d`..`c3d1031`),
each via a fresh subagent (TDD).
Final verification all green:
`go test ./...` PASS (10 packages),
`go build -o atsbx .` OK,
`go vet ./...` clean,
`--dry-run create`/`connect` smoke OK,
and `agent-infrastructure/` byte-for-byte unchanged vs the pre-work baseline.
Only the **optional** plan Task 15 (stdin/tmpfs transport) is left undone —
`SendEnv` is the shipping transport per the spec.
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

- Spec: `docs/superpowers/specs/2026-06-26-atsbx-sandboxes-design.md`
- Plan: `docs/superpowers/plans/2026-06-26-atsbx-sandboxes.md`

## Relevant commits on `design/atsbx-sandboxes`

```
4591b6a docs: implementation plan for multi-backend atsbx sandboxes
6eea3f4 docs: document secret-command injection risk and .local mitigation
bfe9cc1 docs: kit is a discoverable .atsbx/ directory
e58f834 docs: design for YAML-driven multi-backend atsbx sandboxes
724be68 feat: add atsbx CLI dispatcher with dry-run and exit-code propagation  (pre-existing)
```

The pre-existing `atsbx` (build/create/run/delete wrapping `sbx`) is described in
the older `docs/superpowers/specs/2026-06-22-atsbx-design.md`; the new spec
supersedes it.

## What was decided (so you don't relitigate)

- **atsbx owns the mechanism**; `agent-infrastructure/` ships image/kit files.
- **`agent-infrastructure/` is READ-ONLY.** Copy needed files into `sbx` and
  modify the copies there. (User's explicit instruction; enforced in the plan's
  Global Constraints + Task 7 + final verification.)
- **Multi-backend abstraction; SSH is the universal interface.** Scope of this
  spec = the `Backend` interface + uniform `connect` + the **Colima** backend.
  Firecracker and Fly are follow-on specs.
- **Kit = a directory** (`.atsbx/` at repo root, cwd walk-up discovery):
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
- **Managed SSH keypair** at `~/.config/atsbx/id_ed25519` (auto-generated);
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

1. `cd /home/agent/workspace/sbx && git checkout design/atsbx-sandboxes`.
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
