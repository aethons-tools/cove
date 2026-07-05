# AGENTS.md

`at-cove` — a small Go CLI that runs hardened Claude Code sandboxes from a `.at-cove/` kit directory across pluggable VM backends.

**Start here:** [`docs/OVERVIEW.md`](docs/OVERVIEW.md) —
what the project is, the kit format, the command surface, the security model, the architecture, and how to build/test/run.

## Quick orientation

- **Module:** `github.com/aethons-tools/cove` · **Binaries:** `at-cove`, `at-dispatch`
- **Entry points:** `cmd/at-cove/main.go` (the sandbox CLI) and `cmd/at-dispatch/main.go` (the Linear dispatcher — a skeleton today; see [`docs/orchestration/`](docs/orchestration/INDEX.md)).
- **Packages:** `internal/{kit,assemble,backend,connect,secret,sshargs,keys,state,runner,dispatch}` — see the architecture section in the overview for what each owns.

## Working in this repo

- **This repo *builds* sandboxes — keep template files distinct from the repo's own files.**
  Everything under `internal/assemble/*/image-files/` is *payload*:
  a template tree copied into the sandbox VMs that `at-cove` builds,
  not configuration for this repo.
  For example, `internal/assemble/hardening/image-files/home/agent/.init-agent-data/CLAUDE.md`
  and its `SANDBOX.md` are instructions *for an agent running inside a built sandbox* —
  they are not instructions for you working in this repo (that's this file).
  When reviewing or documenting, always say which side of the line a file is on,
  and never edit a template as if it were this repo's config, or vice versa.

- **No task is complete until the repo's docs are updated.**
  Do not consider work done, and do not open a commit or PR,
  until the documentation describing what you changed is updated in the *same* change.
  Route the update to the doc that owns the subject
  (start from [`docs/OVERVIEW.md`](docs/OVERVIEW.md)),
  and keep `docs/` in sync — never leave docs describing the old behavior.
  Note this covers *this repo's* docs; a change to the template tree above
  updates the sandbox's docs, which is a separate concern.

- **Build/test via `just`** (run `just` to list recipes):
  `just test` (hermetic unit tests), `just build`, `just run <args>`, `just lint`, `just integration` (real-ssh, needs ssh/sshd).
- **Tests are hermetic by default** —
  they drive `internal/runner.Fake`,
  so no Docker, network, or live VM is needed.
  Keep new tests that way;
  put real-ssh tests behind the `integration` build tag.
- **Use TDD** —
  write the failing test first.
  The codebase separates a pure *plan* (argv builders, config parsing, assembly) from *execution* (the `Runner`),
  specifically so logic is testable without a VM.
  Preserve that split.
- **The hardening layer is a security boundary.**
  Files under `internal/assemble/hardening/` (nftables, squid, sshd, entrypoint, git credential helper) are applied *last*
  and must always win over user files.
  Don't move hardening into the overridable layer or weaken the egress allow-list
  without understanding the threat model in the overview.
- **Secrets never hit disk or argv.**
  Resolver commands run on the host;
  values flow into the VM in memory only.
  Don't add code paths that write secret values to files, logs, or command lines.

- **Toolchain quirks** —
  this repo is developed in an egress-locked sandbox;
  see [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) for the `GOPROXY`/`GOPATH` settings that make `go` work here.

## Design history

Authoritative design docs and the implementation plans live under [`docs/superpowers/`](docs/superpowers/).
Read `specs/2026-06-26-cove-sandboxes-design.md` for the current design before making structural changes.
