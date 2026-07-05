# cove — monorepo hosting at-cove + at-dispatch — Design

**Date:** 2026-07-05
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (module `github.com/aethons-tools/cove`)
**Related:** `docs/orchestration/` (the agent-orchestration design that motivates a dispatcher)

## 1. Purpose

Convert this repo from a single-binary project into a **multi-binary monorepo**
that hosts both:

- **`at-cove`** — the existing hardened-sandbox CLI (unchanged in behavior), and
- **`at-dispatch`** — the new Linear-driven dispatcher, which the
  [orchestration design](../../orchestration/INDEX.md) establishes as a *separate
  executable* that consumes the `at-cove` CLI rather than importing it.

**Scope of this change:** the repository restructure plus a **buildable,
tested `at-dispatch` skeleton** — no real dispatcher logic. The scheduler,
webhook receiver, Linear client, and queue are explicitly deferred to the
orchestration design's own spec/plan. This change only creates the dispatcher's
home and proves the two-binary layout compiles and tests green.

**Non-goals:** any dispatcher functionality; any new third-party dependency; any
change to at-cove's behavior, hardening, or command surface.

## 2. Decisions (settled during brainstorming)

- **Single Go module**, both binaries under `cmd/`, sharing `internal/`. The
  dispatcher's *future* deps will land in the shared `go.mod` graph, but at-cove's
  linked binary stays clean (Go links only what a `main` imports). Simplicity of
  one `go test ./...` and one build outweighs dependency-graph isolation at this
  stage; a split to multiple modules remains possible later if the dep surface
  becomes a concern.
- **Binary name `at-dispatch`**, mirroring the `at-cove` prefix; shared package
  `internal/dispatch`.
- **Relocate at-cove's entry** from the repo root into `cmd/at-cove/` (rather than
  leaving `main.go` at root and only adding `cmd/at-dispatch/`), so the two
  binaries are symmetric and the root stays free of a `package main`.

## 3. Target layout

```
go.mod / go.sum                         # unchanged: module github.com/aethons-tools/cove
cmd/
  at-cove/
    main.go                             # moved verbatim from ./main.go
    main_test.go                        # moved verbatim from ./main_test.go
  at-dispatch/
    main.go                             # new skeleton
    main_test.go                        # new, hermetic
internal/
  kit/ assemble/ backend/ connect/      # unchanged, shared by both binaries
  secret/ state/ runner/ sshargs/ …
  dispatch/
    doc.go                              # package doc → points at docs/orchestration/
    dispatch.go                         # tiny stub called by cmd/at-dispatch
scripts/build.sh                        # builds both binaries
justfile                                # recipes updated
```

Nothing under `internal/` moves or changes. `package main` for at-cove is
identical; only its directory changes. The `//go:embed` directives in
`internal/assemble/embed.go` are relative to that package and are unaffected.

## 4. Build and test tooling

**`scripts/build.sh`** — introduce a binary list and build each from its `cmd/`
package into the per-target output dir:

```
BINARIES=(at-cove at-dispatch)
# for each target os/arch, for each bin:
CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
  go build -trimpath -ldflags "$LDFLAGS" -o "$dir/$bin" "./cmd/$bin"
```

- `dist/<os>-<arch>/` now holds both `at-cove` and `at-dispatch`.
- Version stamping keeps `-X main.version=$VERSION`; it applies to whichever
  `main` is being built, so both binaries carry an independent `version` var.
- The `all` / explicit-targets / host-default target selection is unchanged.

**`justfile`**

- `build` builds both binaries (loops or delegates to `build.sh`).
- `run *ARGS` continues to build-and-run **at-cove** (the primary dev loop); add a
  parallel `run-dispatch *ARGS`.
- `install` installs both `at-cove` and `at-dispatch` into `BINDIR`.
- `test` (`go test ./...`) and `lint` (`go vet ./...` + gofmt + shell/Docker lint)
  need no logic change — they already recurse into `cmd/`. The `lint` recipe's
  explicit shellcheck/hadolint paths are unchanged.

## 5. The `at-dispatch` skeleton

Buildable and tested, **no real logic, no new dependencies** (stdlib only):

- **`cmd/at-dispatch/main.go`** — `package main` with:
  - a usage string and argv dispatch consistent in style with `cmd/at-cove`;
  - a `version` subcommand printing the `-X`-stamped `main.version`;
  - a `serve` subcommand that calls `dispatch.Serve()`, prints its
    "not implemented yet — see docs/orchestration/" error, and exits non-zero;
  - unknown/no command → usage + non-zero exit.
- **`internal/dispatch/doc.go`** — package-level doc comment stating the package
  will own the scheduler/receiver logic and pointing to
  `docs/orchestration/at-cove-dispatch-interface.md`.
- **`internal/dispatch/dispatch.go`** — exposes a single stub,
  `func Serve() error`, returning a sentinel "not implemented" error. This is what
  makes the shared-module wiring real (`cmd/at-dispatch` imports `internal/dispatch`).
  Kept intentionally tiny.
- **`cmd/at-dispatch/main_test.go`** — hermetic table tests (no network/docker):
  `version` prints the stamped value; `serve` reports not-implemented and exits
  non-zero; unknown command prints usage and exits non-zero.

Per the repo's TDD rule, the skeleton is built **test-first**: the plan writes the
failing `main_test.go` cases before the `main.go`/`internal/dispatch` code.

## 6. Documentation updates (same change — repo docs-gate)

- **`AGENTS.md`** — "Entry point: `main.go`" → `cmd/at-cove/main.go`; add the
  second binary `cmd/at-dispatch/` and `internal/dispatch` to the orientation.
- **`docs/OVERVIEW.md`** — the Architecture map and the Build/test section name
  `main.go` and `go build … .`; update both for the `cmd/` layout and the two
  binaries.
- **`README.md`** — fix any build/entry-point references.
- **`docs/orchestration/at-cove-dispatch-interface.md`** — add a short
  **component-boundaries** note: the scheduler is a separate executable
  (`at-dispatch`) that *consumes* the `at-cove` CLI, now co-located in this
  module. This records the "separate executable, same repo" decision.

## 7. Migration order and verification

1. `git mv main.go cmd/at-cove/main.go` and
   `git mv main_test.go cmd/at-cove/main_test.go`. (Both are hermetic — tests use
   `t.TempDir()` and call the in-package `run(...)`; no cwd/testdata assumptions.)
2. Add the `at-dispatch` skeleton and `internal/dispatch`, test-first.
3. Update `scripts/build.sh` and `justfile`.
4. Update the docs in §6.
5. **Green gate:** `go build ./cmd/...`, `just test`, `just lint` all pass, and
   `just run version` (at-cove) plus `just run-dispatch version` both work.

## 8. Risks

- **Low overall** — the move is mechanical and the moved tests are hermetic.
- **Version `-X` path:** relies on `main.version` resolving to each built `main`
  package; verified by `at-cove version` and `at-dispatch version` in the gate.
- **Stale references:** any script/doc/CI that hardcodes `go build .` or the root
  `main.go` path must be updated; §6 and a repo grep cover this.

## 9. Open questions

None blocking. Future (out of scope here): whether the webhook receiver becomes a
third `cmd/` binary or a mode of `at-dispatch`; and whether the dependency surface
ever justifies splitting `at-dispatch` into its own module. Both are deferred to
the orchestration design.
