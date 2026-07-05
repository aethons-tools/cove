# cove Monorepo (at-cove + at-dispatch) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure the repo into a single-module, multi-binary monorepo hosting `at-cove` (moved, behavior unchanged) and a new buildable-but-logic-free `at-dispatch` skeleton.

**Architecture:** One Go module (`github.com/aethons-tools/cove`). Both binaries live under `cmd/` and share `internal/`. `at-cove`'s entry moves from the repo root into `cmd/at-cove/`; `at-dispatch` is a new `cmd/at-dispatch/` that calls a stub in `internal/dispatch/`. `scripts/build.sh` and the `justfile` build both binaries. No dispatcher logic, no new dependencies.

**Tech Stack:** Go 1.22 (stdlib + `gopkg.in/yaml.v3` only), `just` task runner over `scripts/`, `go test` (hermetic).

## Global Constraints

- **Single Go module**, module path `github.com/aethons-tools/cove`; both binaries under `cmd/`.
- **No new third-party dependencies.** The `at-dispatch` skeleton is **stdlib only**. `go.mod` must still list only `gopkg.in/yaml.v3`.
- **Go 1.22 floor** (per `go.mod`). Toolchain env for this egress-locked repo (GOPROXY/GOPATH) is in [`docs/DEVELOPMENT.md`](../../DEVELOPMENT.md) — use `just test` / `just build` which encapsulate it.
- **Binary name `at-dispatch`**; shared package `internal/dispatch`.
- **at-cove behavior is unchanged** — only its source directory moves. Do not edit `main.go`'s logic.
- **TDD, hermetic tests** — every test uses `t.TempDir()` / in-memory buffers; no docker/network/ssh. New tests must keep `go test ./...` hermetic.
- **Docs-gate:** the repo's docs are updated in this same change (Task 4). Do **not** edit frozen historical plans under `docs/superpowers/plans/`.

---

## File Structure

- `cmd/at-cove/main.go` — moved verbatim from `./main.go` (at-cove entry; unchanged logic).
- `cmd/at-cove/main_test.go` — moved verbatim from `./main_test.go`.
- `cmd/at-dispatch/main.go` — new skeleton entry: `version` + stubbed `serve`.
- `cmd/at-dispatch/main_test.go` — new hermetic tests for the skeleton.
- `internal/dispatch/doc.go` — package doc pointing at the orchestration design.
- `internal/dispatch/dispatch.go` — `Serve() error` stub + `ErrNotImplemented`.
- `internal/dispatch/dispatch_test.go` — test for the stub.
- `scripts/build.sh` — build every binary in `cmd/` per target.
- `justfile` — `run-dispatch`, install both binaries, refreshed comments.
- `AGENTS.md`, `docs/OVERVIEW.md`, `README.md`, `docs/orchestration/at-cove-dispatch-interface.md` — doc updates.

---

## Task 1: Relocate at-cove's entry into `cmd/at-cove/`

Move the root `package main` (and its test) into `cmd/at-cove/` and point the build at the new path. at-cove keeps building and all existing tests keep passing.

**Files:**
- Create (via move): `cmd/at-cove/main.go` (from `main.go`)
- Create (via move): `cmd/at-cove/main_test.go` (from `main_test.go`)
- Modify: `scripts/build.sh:48` (build source `.` → `./cmd/at-cove`)

**Interfaces:**
- Consumes: nothing new.
- Produces: `at-cove` binary built from `./cmd/at-cove`; the `main` package's `version` var (already exists, stamped via `-X main.version`).

- [ ] **Step 1: Move the two files with git (preserves history)**

```bash
mkdir -p cmd/at-cove
git mv main.go cmd/at-cove/main.go
git mv main_test.go cmd/at-cove/main_test.go
```

- [ ] **Step 2: Verify the move breaks the build (source path is now stale)**

Run: `go build ./cmd/at-cove`
Expected: PASS (the package compiles from its new location — `internal/…` imports are absolute module paths, unaffected).

Run: `go build .`
Expected: FAIL — `no Go files in <repo root>` (root no longer has a `package main`). This confirms `build.sh` must change.

- [ ] **Step 3: Point `build.sh` at the new package path**

In `scripts/build.sh`, change the build line (currently line 48):

```bash
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$dir/at-cove" .
```

to:

```bash
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$dir/at-cove" ./cmd/at-cove
```

- [ ] **Step 4: Verify everything is green from the new layout**

Run: `go test ./...`
Expected: PASS (moved `main_test.go` is hermetic — `t.TempDir()` + in-package `run(...)`; nothing referenced the old path).

Run: `just run version`
Expected: builds via `build.sh`, then prints the version string (e.g. a `git describe` value), exit 0.

Run: `go vet ./... && gofmt -l cmd/`
Expected: no vet errors; `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-cove/ scripts/build.sh
git commit -m "refactor: move at-cove entry into cmd/at-cove/"
```

---

## Task 2: Add the `internal/dispatch` stub (test-first)

Create the shared package the dispatcher binary will grow into. For now it exposes a single `Serve()` that reports "not implemented."

**Files:**
- Test: `internal/dispatch/dispatch_test.go`
- Create: `internal/dispatch/dispatch.go`
- Create: `internal/dispatch/doc.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `dispatch.Serve() error`, and sentinel `dispatch.ErrNotImplemented` (an `error` value satisfying `errors.Is(Serve(), dispatch.ErrNotImplemented)`).

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/dispatch_test.go`:

```go
package dispatch

import (
	"errors"
	"testing"
)

func TestServeReturnsNotImplemented(t *testing.T) {
	err := Serve()
	if err == nil {
		t.Fatal("Serve() = nil; want a not-implemented error")
	}
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Serve() = %v; want errors.Is(err, ErrNotImplemented)", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/`
Expected: FAIL to build — `undefined: Serve`, `undefined: ErrNotImplemented`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/dispatch/doc.go`:

```go
// Package dispatch will own the Linear-driven dispatcher's control plane —
// the scheduler and the webhook receiver that drive at-cove worker containers.
//
// It is currently a skeleton with no behavior. See the design:
//   - docs/orchestration/at-cove-dispatch-interface.md (the at-cove contract)
//   - docs/orchestration/linear-agent-workflow.md      (the workflow)
package dispatch
```

Create `internal/dispatch/dispatch.go`:

```go
package dispatch

import "errors"

// ErrNotImplemented is returned by skeleton entry points that have no logic yet.
var ErrNotImplemented = errors.New("at-dispatch: not implemented yet — see docs/orchestration/")

// Serve will run the dispatcher (scheduler + webhook receiver). It is a stub
// until the orchestration design is implemented.
func Serve() error {
	return ErrNotImplemented
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/dispatch/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/
git commit -m "feat(dispatch): add internal/dispatch skeleton package"
```

---

## Task 3: Add the `at-dispatch` binary + wire build/just (test-first)

Create the second binary as a thin CLI over `internal/dispatch`, then teach `build.sh` and the `justfile` about it.

**Files:**
- Test: `cmd/at-dispatch/main_test.go`
- Create: `cmd/at-dispatch/main.go`
- Modify: `scripts/build.sh` (build every `cmd/` binary)
- Modify: `justfile` (add `run-dispatch`; install both; refresh comments)

**Interfaces:**
- Consumes: `dispatch.Serve()`, `dispatch.ErrNotImplemented` (from Task 2).
- Produces: `at-dispatch` binary; an in-package `run(args []string, stdout, stderr io.Writer) int` (for hermetic testing) and a `version` var stamped via `-X main.version`.

- [ ] **Step 1: Write the failing test**

Create `cmd/at-dispatch/main_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionPrintsStampedValue(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "1.2.3"

	var out, errOut bytes.Buffer
	code := run([]string{"version"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr: %q)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "1.2.3" {
		t.Fatalf("stdout = %q; want %q", got, "1.2.3")
	}
}

func TestServeReportsNotImplemented(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"serve"}, &out, &errOut)

	if code != 1 {
		t.Fatalf("exit = %d; want 1", code)
	}
	if !strings.Contains(errOut.String(), "not implemented") {
		t.Fatalf("stderr = %q; want it to mention 'not implemented'", errOut.String())
	}
}

func TestUnknownCommandPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"bogus"}, &out, &errOut)

	if code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") ||
		!strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("stderr = %q; want 'unknown command' and 'Usage:'", errOut.String())
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(nil, &out, &errOut)

	if code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("stderr = %q; want usage text", errOut.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/at-dispatch/`
Expected: FAIL to build — `undefined: run`, `undefined: version` (no `main.go` yet).

- [ ] **Step 3: Write the minimal implementation**

Create `cmd/at-dispatch/main.go`:

```go
// Command at-dispatch is the Linear-driven dispatcher that schedules work onto
// at-cove sandboxes. It is currently a skeleton — see docs/orchestration/.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aethons-tools/cove/internal/dispatch"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `at-dispatch — Linear-driven dispatcher for at-cove sandboxes (skeleton)

Usage:
  at-dispatch version      print the build version
  at-dispatch serve        run the dispatcher (not implemented yet)

Status: skeleton only. See docs/orchestration/ for the design.
`

// run is the testable entry point: it returns a process exit code and writes
// only to the provided streams (no direct os.Stdout/os.Stderr use).
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "serve":
		err := dispatch.Serve()
		fmt.Fprintln(stderr, err)
		if errors.Is(err, dispatch.ErrNotImplemented) {
			return 1
		}
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "at-dispatch: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/at-dispatch/`
Expected: PASS (all four tests).

- [ ] **Step 5: Teach `build.sh` to build every `cmd/` binary**

In `scripts/build.sh`, add a binary list after the `ALL_TARGETS` line (line 30):

```bash
ALL_TARGETS=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64)
BINARIES=(at-cove at-dispatch)
```

Change the header echo (line 39) from:

```bash
echo "Building at-cove ${VERSION}"
```

to:

```bash
echo "Building cove ${VERSION} (${BINARIES[*]})"
```

Replace the per-target build body (currently lines 41-50, the `for t in …` loop) with a nested loop over binaries:

```bash
for t in "${TARGETS[@]}"; do
  os="${t%/*}"
  arch="${t#*/}"
  dir="${OUT}/${os}-${arch}"
  mkdir -p "$dir"
  for bin in "${BINARIES[@]}"; do
    echo "  building ${t} -> ${dir}/${bin}"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$dir/$bin" "./cmd/$bin"
    built+=("$dir/$bin")
  done
done
```

(This supersedes Task 1's single-binary edit to the same line.)

- [ ] **Step 6: Add the `justfile` recipes and refresh comments**

In `justfile`, update the `build` / `build-all` comments (lines 9 and 13) to name both binaries:

```
# build for the current host into dist/<os>-<arch>/{at-cove,at-dispatch} (host-sensitive)
build:
    ./scripts/build.sh

# cross-compile every supported target into dist/<os>-<arch>/{at-cove,at-dispatch}
build-all:
    ./scripts/build.sh all
```

Immediately after the existing `run` recipe, add a sibling for the dispatcher:

```
# build, then run the at-dispatch binary, forwarding ARGS.
# e.g. `just run-dispatch version`
run-dispatch *ARGS: build
    "dist/$(go env GOOS)-$(go env GOARCH)/at-dispatch" {{ARGS}}
```

Replace the `install` recipe body so it installs **both** binaries:

```
# install the host binaries (at-cove, at-dispatch) onto your PATH.
# Default dir: $(go env GOBIN) or $(go env GOPATH)/bin (~/go/bin) — no sudo.
# Override with BINDIR, e.g. `BINDIR=/usr/local/bin just install` (may need sudo).
install: build
    #!/usr/bin/env bash
    set -euo pipefail
    bin="${BINDIR:-$(go env GOBIN)}"
    [ -n "$bin" ] || bin="$(go env GOPATH)/bin"
    plat="$(go env GOOS)-$(go env GOARCH)"
    mkdir -p "$bin"
    for b in at-cove at-dispatch; do
      install -m 0755 "dist/$plat/$b" "$bin/$b"
      echo "installed dist/$plat/$b -> $bin/$b"
    done
    if command -v at-cove >/dev/null 2>&1; then
      echo "on PATH: $(command -v at-cove) ($(at-cove version))"
    else
      echo "warning: $bin is not on your PATH — add it or use BINDIR=/usr/local/bin"
    fi
```

- [ ] **Step 7: Verify the whole repo is green and both binaries build**

Run: `go test ./...`
Expected: PASS (at-cove tests, dispatch tests, at-dispatch tests).

Run: `just build`
Expected: prints "Building cove … (at-cove at-dispatch)" and builds both into `dist/<os>-<arch>/`.

Run: `just run-dispatch version` then `just run version`
Expected: the first prints the version and exits 0; the second (at-cove) still prints its version and exits 0.

Run: `go vet ./... && gofmt -l cmd/ internal/dispatch/`
Expected: no vet errors; `gofmt -l` prints nothing.

- [ ] **Step 8: Commit**

```bash
git add cmd/at-dispatch/ scripts/build.sh justfile
git commit -m "feat(at-dispatch): add skeleton binary and multi-binary build"
```

---

## Task 4: Update the repo docs (docs-gate)

Bring current-behavior docs in line with the `cmd/` layout and the two binaries. Do **not** touch historical plans under `docs/superpowers/plans/`.

**Files:**
- Modify: `AGENTS.md:11` (+ orientation)
- Modify: `docs/OVERVIEW.md` (architecture block ~line 283; build section ~line 302)
- Modify: `README.md:77` (+ Status)
- Modify: `docs/orchestration/at-cove-dispatch-interface.md` (Purpose: one-line boundary pointer)

**Interfaces:** none (documentation only).

- [ ] **Step 1: Update `AGENTS.md` orientation**

Replace the entry-point bullet (line 11):

```markdown
- **Entry point:** `main.go` (argv parsing + subcommand dispatch).
```

with:

```markdown
- **Entry points:** `cmd/at-cove/main.go` (the sandbox CLI) and `cmd/at-dispatch/main.go` (the Linear dispatcher — a skeleton today; see [`docs/orchestration/`](docs/orchestration/INDEX.md)).
```

In the same bullet list, extend the packages line to mention the new package — change:

```markdown
- **Packages:** `internal/{kit,assemble,backend,connect,secret,sshargs,keys,state,runner}` — see the architecture section in the overview for what each owns.
```

to append `,dispatch` inside the braces:

```markdown
- **Packages:** `internal/{kit,assemble,backend,connect,secret,sshargs,keys,state,runner,dispatch}` — see the architecture section in the overview for what each owns.
```

- [ ] **Step 2: Update the `docs/OVERVIEW.md` architecture map**

In the architecture code block, change the `main.go` line:

```
main.go                       parse argv, discover kit, select backend, dispatch
```

to the two entry points plus the shared package, and keep the rest of the block unchanged:

```
cmd/at-cove/                  at-cove entry: parse argv, discover kit, select backend, dispatch
cmd/at-dispatch/              at-dispatch entry: dispatcher CLI skeleton (version + stubbed serve)
internal/dispatch/            dispatcher control plane (skeleton; owned by docs/orchestration/)
```

Immediately after that code block, add a short paragraph recording the executable boundary (this doc is the single source for it):

```markdown
This module builds **two binaries**. `at-cove` is the sandbox substrate.
`at-dispatch` is a **separate executable** that *consumes* the `at-cove` CLI
(it never imports at-cove's internals) to schedule Linear-driven work onto
sandboxes — see the [orchestration design](orchestration/INDEX.md). It is a
skeleton today.
```

- [ ] **Step 3: Update the `docs/OVERVIEW.md` build section**

Change the build recipe comment (line ~302):

```
just build           # build for the host into dist/<os>-<arch>/at-cove
```

to:

```
just build           # build both binaries into dist/<os>-<arch>/{at-cove,at-dispatch}
```

- [ ] **Step 4: Update `README.md`**

Change the build line (line 77):

```
just build         # build for the host into dist/<os>-<arch>/at-cove
```

to:

```
just build         # build both binaries into dist/<os>-<arch>/{at-cove,at-dispatch}
```

Append a sentence to the **Status** section (after the "designed but deferred" line):

```markdown
The repo also builds `at-dispatch`, a skeleton of the Linear-driven dispatcher
(see [`docs/orchestration/`](docs/orchestration/INDEX.md)); it has no behavior yet.
```

- [ ] **Step 5: Add the boundary pointer in the dispatch-interface doc**

In `docs/orchestration/at-cove-dispatch-interface.md`, at the end of the `## Purpose` section, add exactly one line (keeps the doc at its 200-line budget):

```markdown
In this repo the scheduler is realized as the **`at-dispatch`** binary — a separate executable that consumes the `at-cove` CLI; see [OVERVIEW's architecture](../OVERVIEW.md#architecture) for the repo layout.
```

- [ ] **Step 6: Verify docs are consistent and no stale current-docs references remain**

Run: `python3 ${CLAUDE_SKILL_DIR:-/agent-data/skills/docs-audit}/scripts/docs_audit.py docs/orchestration --index INDEX.md`
Expected: `0 error(s)`, and no leaf over 200 lines (the dispatch-interface doc is at 200).

Run: `grep -rnE 'Entry point:.*main\.go|dist/<os>-<arch>/at-cove\b' AGENTS.md README.md docs/OVERVIEW.md`
Expected: no matches (all current-docs references updated). Historical plans under `docs/superpowers/plans/` are intentionally untouched.

- [ ] **Step 7: Commit**

```bash
git add AGENTS.md docs/OVERVIEW.md README.md docs/orchestration/at-cove-dispatch-interface.md
git commit -m "docs: describe cmd/ layout and the at-dispatch binary"
```

---

## Final verification

- [ ] Run `go test ./...` — all hermetic tests pass.
- [ ] Run `just build` — both `at-cove` and `at-dispatch` land in `dist/<os>-<arch>/`.
- [ ] Run `just lint` — `go vet` + `gofmt` clean (shellcheck/hadolint if installed).
- [ ] Run `just run version` and `just run-dispatch version` — both print a version, exit 0.
- [ ] Confirm `go.mod` still requires only `gopkg.in/yaml.v3` (no new deps).
- [ ] Confirm `git log --oneline -4` shows the four task commits.
