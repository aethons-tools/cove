# cove — Design

**Date:** 2026-06-22
**Status:** Approved (pre-implementation)
**Repo:** `~/local-repos/aethons-tools/sbx`
**Module path:** `github.com/aethons-tools/cove`
**Binary:** `cove`

## 1. Purpose

`cove` is a small, dependency-free Go CLI that wraps the `sbx` Docker sandbox
tool. It customizes a "kit" for the local machine (environment-variable
templating + pack) and manages sandbox VMs through four commands: `build`,
`create`, `run`, and `delete`.

`build` and `create` carry real logic (templating, packing, build-before-run
orchestration). `run` and `delete` are thin pass-throughs to `sbx` for
symmetry.

## 2. Commands

| Command | Args | Behavior |
|---|---|---|
| `cove build [kitdir]` | `kitdir` optional, defaults to cwd | Template + pack the kit |
| `cove create <name> <kitdir> [volume...]` | `name` & `kitdir` required; `volume` optional, repeatable, defaults to `.` (cwd) | Build, then start a new sandbox |
| `cove run <name>` | `name` required | Pass-through to `sbx run <name>` |
| `cove delete <name>` | `name` required | Pass-through to `sbx remove <name>` |

A global `--dry-run` flag (accepted before or after the subcommand) prints the
planned actions — including the exact `sbx` argv and "would template N files" —
instead of executing anything.

### 2.1 `build` (core logic)

Derived from the existing zsh build script, reimplemented in Go.

1. Resolve `kitdir` (positional arg, or cwd if omitted). Validate it exists and
   is a directory.
2. Walk all **regular files** under `kitdir` recursively, **including dotfiles**,
   but **excluding the `.build/` subtree** so the build never packs its own
   output.
3. For each file: substitute environment variables (GNU-`envsubst` semantics,
   see §4), write the result to `kitdir/.build/kit/<relpath>`, **preserving the
   directory structure and the source file mode**.
4. Run `sbx kit pack <kitdir>/.build/kit -o <kitdir>/.build/kit.zip`.
5. Remove the staging dir `kitdir/.build/kit`, leaving `kit.zip` in place.

### 2.2 `create` (build-then-run orchestration)

1. Always run `build <kitdir>` first (packing is cheap, so no staleness check —
   YAGNI).
2. Run:
   ```
   sbx run --name <name> --kit <kitdir>/.build/kit.zip claude <vol1> <vol2> ...
   ```
   - `claude` is the agent name, **hardcoded** for now.
   - Volumes are the trailing positional args from the command line; if none are
     given, a single volume `.` (cwd) is used.

### 2.3 `run` / `delete`

- `cove run <name>` → `sbx run <name>`
- `cove delete <name>` → `sbx remove <name>`

## 3. Architecture

Standard-library `flag` package with a small subcommand dispatcher — no
third-party dependencies (e.g. cobra) for a tool this size. The design separates
a pure **plan** (what to do) from **execution** (doing it), so the interesting
logic is unit-testable without `sbx` installed.

```
sbx/                          repo root
  go.mod                      module github.com/aethons-tools/cove
  main.go                     flag parsing + subcommand dispatch; wires real runner
  internal/kit/               templating (envsubst) + build orchestration
  internal/sbx/               pure argv builders: Pack(), Run(), CreateRun(), Remove()
  internal/runner/            Runner interface; OS impl streams stdio & propagates
                              exit code; fake impl records calls for tests
  docs/superpowers/specs/     this design doc
```

### 3.1 Responsibilities

- **`internal/sbx`** — pure functions that return the `sbx` argv (`[]string`) for
  each operation. No I/O. Trivially testable.
- **`internal/kit`** — the `build` logic: walk + envsubst + stage, then ask the
  runner to execute the `Pack()` argv, then clean up staging. Templating is pure
  enough to test against a temp dir.
- **`internal/runner`** — `Runner` interface, e.g.
  `Run(name string, args ...string) error`. The OS implementation shells out,
  streams stdout/stderr live, and propagates the child exit code. A fake
  implementation records calls (and can simulate failures) for tests.
- **`main.go`** — parse args/flags, build the plan, and either execute it via the
  real runner or, under `--dry-run`, print it.

## 4. Environment-variable substitution (`envsubst` semantics)

Replicates GNU `envsubst` as used by the original script:

- Substitute `${VAR}` and `$VAR` where `VAR` is a valid shell identifier
  (`[A-Za-z_][A-Za-z0-9_]*`).
- An **unset** variable is replaced with the **empty string** (matching
  `envsubst`).
- No command substitution, no `$$`/escaping logic, no arithmetic — a plain
  variable expansion only.
- Applied to **all** walked files indiscriminately, matching the original
  script's behavior.

## 5. Error handling

- `sbx` not found on `PATH` → a clear, actionable error before any work begins.
- Missing required args or a `kitdir` that does not exist / is not a directory →
  a usage message and a non-zero exit.
- When `sbx` exits non-zero, **propagate the same exit code**; stdout/stderr are
  streamed live so the user sees `sbx` output verbatim.

## 6. Testing (TDD)

All tests run **without `sbx` installed**:

- **argv builders** (`internal/sbx`): exact argv for `Pack`, `CreateRun`, `Run`,
  `Remove`, including volume defaulting and the hardcoded `claude` agent.
- **templating** (`internal/kit`): given a temp dir of template files and a set
  of env vars — verify output content (substitution incl. unset→empty),
  preserved directory structure, preserved file mode, and `.build/` exclusion.
- **`create` orchestration**: via a fake `Runner`, assert build-runs-before-run
  ordering and the exact `sbx run` argv.
- **dry-run**: asserts no runner calls are executed and the planned commands are
  printed.

## 7. Out of scope (YAGNI)

- Build staleness caching for `create` (always rebuild).
- Configurable agent name (hardcoded `claude`).
- A third-party CLI framework.
- Any `sbx` functionality beyond the four mapped commands.
