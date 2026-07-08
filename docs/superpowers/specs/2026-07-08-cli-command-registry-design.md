# CLI command registry (`internal/cli`) — Design

**Date:** 2026-07-08
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-dispatch`, `at-work`)

## 1. Purpose

Replace the hand-rolled, ad-hoc command-line parsing in the three binaries with a
small **shared, zero-dependency command registry** (`internal/cli`). Today
`cmd/at-cove/main.go` collects every flag in one global loop and interprets it
per-command — so a flag is accepted where it is meaningless (`build --raw` is
silently ignored) and `dispatch` had to be special-cased because its flags aren't in
that loop. The registry gives each command **its own flags, validated**, a uniform
help/usage surface, and one pattern for adding a command in any binary — **without
adding a third-party CLI dependency** (a deliberate posture for this hardened-sandbox
tool: `go.mod` stays at its single dep).

## 2. Governing decisions (from brainstorming)

- **No new dependency.** stdlib `flag` is sufficient at this scale (2–9 commands per
  binary, one level deep). A library (`cobra`/`urfave`) would add supply-chain +
  build-egress surface for modest gain. Revisit only if nested subcommands / shell
  completions / large shared flag-sets appear.
- **Scope: all three binaries, via a shared `internal/cli`.** One registry pattern
  everywhere, even though `at-dispatch`/`at-work` are already small — uniformity so a
  new command follows one shape.
- **Flag-placement model: conventional.** `at-cove [--global] <command> [--command-flags]
  [positionals]`. `--dry-run`/`--version` are global (parsed before the command); every
  command owns its own flags **after** the command name; an **unknown flag for a
  command is an error** (today it is silently ignored). Within a command, flags and
  positionals may appear in any order (order-independence is preserved *within* the
  command, not across the command boundary).

## 3. The `internal/cli` package

```go
package cli

// Globals are the cross-cutting flags parsed before the command name.
type Globals struct{ DryRun bool }

// Command is one subcommand. Run receives the tokens AFTER the command name
// (its own flags + positionals, in any order), the parsed globals, and the
// writers; it returns the process exit code.
type Command struct {
	Name  string
	Brief string // one-line help, shown in usage
	Run   func(args []string, g Globals, stdout, stderr io.Writer) int
}

// App is a registry + dispatcher for one binary.
type App struct {
	Name     string
	Version  string
	Commands []Command
}

// Run parses leading global flags (--dry-run, --version) with a top-level FlagSet
// (which stops at the first non-flag = the command name), handles version/help,
// finds the command, and dispatches. Unknown command or unknown global flag =>
// auto-generated usage + exit 2.
func (a App) Run(argv []string, stdout, stderr io.Writer) int

// ParseInterspersed parses fs against args allowing flags and positionals in any
// order (the "fs.Parse in a loop" idiom), returning the collected positionals.
// A flag error (unknown flag, bad value) returns a non-nil error; callers exit 2.
func ParseInterspersed(fs *flag.FlagSet, args []string) (positionals []string, err error)
```

- **`App.Run`**: top-level `flag.NewFlagSet(Name, ContinueOnError)` with `--dry-run`
  and `--version`; `Parse(argv)` consumes the leading globals and stops at the command
  name. `--version` (or a `version` command) prints `Name+" "+Version`; `help`/`-h`/no
  args print the usage table built from `Commands` (Name + Brief). An unrecognized
  command → usage + exit 2. Otherwise call the matched `Command.Run(rest, Globals{DryRun}, …)`.
- **`ParseInterspersed`**: loops `fs.Parse` / collect one positional / continue, so
  `connect ./kit --raw` and `connect --raw ./kit` both parse. This is what lets the
  refactor keep the existing tests' positional-then-flag orderings.

## 4. Per-binary wiring

Each binary's existing `run(argv, deps…, stdout, stderr) int` seam is **kept** — it now
builds an `App` (its command closures capture the deps) and calls `app.Run(argv, …)`.
The `doX()` bodies are unchanged; only flag parsing + positional resolution moves into
each command's closure.

- **at-cove** (9 commands): `build`, `create` (`--ws`), `connect`
  (`--raw`/`--no-auth`/`--fresh`), `loop` (`--once`/`--keep`/`--interval` + the
  `[name] [kit-dir]` positional heuristic), `recreate` (`--ws`), `destroy` (`--loop`),
  `status` (`--loop`), `version`, `dispatch` (`--in`/`--out`/`--timeout`/`--grace`/`--reap`).
  Each closure builds its own `flag.FlagSet`, calls `ParseInterspersed`, resolves the
  kit-dir positional (via the existing `resolveKit`), and calls the existing `doX`.
  **`dispatch` is now an ordinary command — the special-case is removed.**
- **at-dispatch**: `version`, `serve` (`--config`).
- **at-work**: `version`, `prepare`, `complete` (positionals only, no flags).

## 5. Behavior changes (intentional)

- Command flags move **after** the command name and are **validated** per command:
  `build --raw` (previously silently ignored) is now an error. `--dry-run`/`--version`
  remain global, before the command.
- The old global "--loop is only valid for destroy and status" check is replaced by
  per-command flag registration (only `destroy`/`status` declare `--loop`); misuse on
  another command is now an unknown-flag error.
- Exit codes preserved: arg/flag/usage errors → 2; operational (`doX`) errors → 1
  (unwrapping `runner.ExitError` to its code, as today); success → 0.

## 6. Testing

- **`internal/cli`** gets its own unit tests: global-flag parsing, command dispatch,
  `ParseInterspersed` ordering (flag-before/after positional), unknown-command and
  unknown-flag → exit 2, and `version`/`help` output.
- **The `run(argv, …) int` + `Fake`-runner drive is unchanged**, so each binary's
  existing command tests remain the behavioral gate. The **at-cove** tests whose
  invocations put command flags *before* the command are updated to the conventional
  placement (mechanical: `--raw --no-auth connect kit` → `connect --raw --no-auth kit`);
  the `--loop`-misuse test still asserts exit 2 (now via the unknown-flag path).
  `at-dispatch`/`at-work` command tests are largely untouched (globals/positionals only).

## 7. Non-goals

- No third-party CLI dependency. No nested subcommands, shell completion, or generated
  man pages. No change to command *behavior* beyond the intentional flags-after-command
  validation. No change to the `doX` implementations.

## 8. Scope

One coherent refactor, but sizable: a new `internal/cli` package + rewiring three
binaries + updating the affected at-cove tests. `go.mod` unchanged. The plan sequences
it: the `cli` package first (with its own tests), then each binary adopts it.
