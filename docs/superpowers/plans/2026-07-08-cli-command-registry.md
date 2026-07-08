# CLI Command Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the three binaries' hand-rolled CLI parsing with a shared, zero-dependency `internal/cli` command registry — per-command validated flags, uniform help, no `dispatch` special-case.

**Architecture:** `internal/cli` provides `App{Name,Version,Commands}` (parses global `--dry-run`/`--version` before the command, dispatches to a `Command`) and `ParseInterspersed` (a command's flags + positionals in any order). Each binary's existing `run(argv,…) int` seam is kept — it now builds an `App` whose command closures capture the binary's deps and own their `flag.FlagSet`. The `doX()` bodies are unchanged.

**Tech Stack:** Go 1.22, stdlib `flag`/`text/tabwriter` only — **no new dependencies**.

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new third-party dependencies** (`go.mod` stays only `gopkg.in/yaml.v3`).
- **Flag model:** `<binary> [--dry-run] <command> [--command-flags] [positionals]`. `--dry-run`/`--version` are global (before the command); each command owns its own flags **after** the command; an **unknown flag for a command is an error** (exit 2). Within a command, flags and positionals may appear in any order (`ParseInterspersed`).
- **Exit codes preserved:** arg/flag/usage errors → 2; operational (`doX`) errors → 1 (unwrap `runner.ExitError` to its code); success → 0.
- **Behavior-preserving except** the intentional flags-after-command validation and a uniform `version` output (`"<name> <version>"`).
- **The `run(argv,…) int` + `Fake`-runner test seam is unchanged** — existing per-command tests stay the behavioral gate (with flag-placement updates in at-cove).
- Spec: [`docs/superpowers/specs/2026-07-08-cli-command-registry-design.md`](../specs/2026-07-08-cli-command-registry-design.md).

---

## File Structure

- `internal/cli/cli.go` (+ `cli_test.go`) — the registry: `App`, `Command`, `Globals`, `App.Run`, `ParseInterspersed`, usage.
- `cmd/at-cove/main.go` (+ test updates) — `run()` builds the App; 9 command closures own their flags; `doDispatch` refactored to receive parsed values.
- `cmd/at-dispatch/main.go` (+ test) — App with `version`/`serve`.
- `cmd/at-work/main.go` (+ test) — App with `version`/`prepare`/`complete`.

---

## Task 1: the `internal/cli` package

**Files:**
- Create: `internal/cli/cli.go`
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Produces: `cli.Globals{DryRun bool}`; `cli.Command{Name, Brief string, Run func(args []string, g Globals, stdout, stderr io.Writer) int}`; `cli.App{Name, Version string, Commands []Command}` with `func (App) Run(argv []string, stdout, stderr io.Writer) int`; `func ParseInterspersed(fs *flag.FlagSet, args []string) ([]string, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/cli_test.go`:

```go
package cli

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func testApp(rec *string) App {
	return App{
		Name: "tool", Version: "1.2.3",
		Commands: []Command{
			{Name: "greet", Brief: "say hi", Run: func(args []string, g Globals, stdout, stderr io.Writer) int {
				*rec = strings.Join(args, ",") + "|dry=" + map[bool]string{true: "1", false: "0"}[g.DryRun]
				return 0
			}},
		},
	}
}

func TestAppDispatchesWithGlobals(t *testing.T) {
	var rec string
	var out, errOut bytes.Buffer
	code := testApp(&rec).Run([]string{"--dry-run", "greet", "a", "--x"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if rec != "a,--x|dry=1" {
		t.Fatalf("command got %q", rec)
	}
}

func TestAppVersionAndHelp(t *testing.T) {
	var rec string
	app := testApp(&rec)
	var out, errOut bytes.Buffer
	if app.Run([]string{"--version"}, &out, &errOut); !strings.Contains(out.String(), "tool 1.2.3") {
		t.Fatalf("--version = %q", out.String())
	}
	out.Reset()
	if app.Run([]string{"version"}, &out, &errOut); !strings.Contains(out.String(), "tool 1.2.3") {
		t.Fatalf("version cmd = %q", out.String())
	}
	out.Reset()
	if code := app.Run([]string{"help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "greet") {
		t.Fatalf("help code=%d out=%q", code, out.String())
	}
}

func TestAppUnknownCommandAndNoArgs(t *testing.T) {
	var rec string
	app := testApp(&rec)
	var out, errOut bytes.Buffer
	if code := app.Run([]string{"nope"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("unknown: code=%d err=%q", code, errOut.String())
	}
	errOut.Reset()
	if code := app.Run(nil, &out, &errOut); code != 2 {
		t.Fatalf("no args code=%d", code)
	}
}

func TestParseInterspersed(t *testing.T) {
	for _, args := range [][]string{
		{"./kit", "--raw"},
		{"--raw", "./kit"},
	} {
		fs := flag.NewFlagSet("connect", flag.ContinueOnError)
		raw := fs.Bool("raw", false, "")
		pos, err := ParseInterspersed(fs, args)
		if err != nil {
			t.Fatalf("args %v: %v", args, err)
		}
		if !*raw || len(pos) != 1 || pos[0] != "./kit" {
			t.Fatalf("args %v -> raw=%v pos=%v", args, *raw, pos)
		}
	}
	// unknown flag errors
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	if _, err := ParseInterspersed(fs, []string{"--nope"}); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}
```

Add `"io"` to the imports (used by the `Command.Run` signature in `testApp`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/`
Expected: FAIL to build — `App`/`Command`/`ParseInterspersed` undefined.

- [ ] **Step 3: Implement the package**

Create `internal/cli/cli.go`:

```go
// Package cli is a tiny zero-dependency command registry shared by the cove
// binaries. Global flags (--dry-run, --version) come before the command; each
// command owns its own flags, after the command name.
package cli

import (
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
)

// Globals are the cross-cutting flags parsed before the command name.
type Globals struct{ DryRun bool }

// Command is one subcommand. Run receives the tokens after the command name
// (its flags + positionals, in any order), the parsed globals, and the writers;
// it returns the process exit code.
type Command struct {
	Name  string
	Brief string
	Run   func(args []string, g Globals, stdout, stderr io.Writer) int
}

// App is a registry + dispatcher for one binary.
type App struct {
	Name     string
	Version  string
	Commands []Command
}

// Run parses leading globals, handles version/help, then dispatches to a command.
func (a App) Run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(a.Name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { a.usage(stderr) }
	dry := fs.Bool("dry-run", false, "print planned actions without executing")
	ver := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *ver {
		fmt.Fprintln(stdout, a.Name+" "+a.Version)
		return 0
	}
	rest := fs.Args()
	if len(rest) == 0 {
		a.usage(stderr)
		return 2
	}
	name, cmdArgs := rest[0], rest[1:]
	switch name {
	case "version":
		fmt.Fprintln(stdout, a.Name+" "+a.Version)
		return 0
	case "help", "-h", "--help":
		a.usage(stdout)
		return 0
	}
	for _, c := range a.Commands {
		if c.Name == name {
			return c.Run(cmdArgs, Globals{DryRun: *dry}, stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "%s: unknown command %q\n\n", a.Name, name)
	a.usage(stderr)
	return 2
}

func (a App) usage(w io.Writer) {
	fmt.Fprintf(w, "usage: %s [--dry-run] <command> [flags] [args]\n\ncommands:\n", a.Name)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range a.Commands {
		fmt.Fprintf(tw, "  %s\t%s\n", c.Name, c.Brief)
	}
	tw.Flush()
}

// ParseInterspersed parses fs against args allowing flags and positionals in any
// order (the "Parse in a loop" idiom), returning the collected positionals. A flag
// error (unknown flag, bad value) returns a non-nil error; callers should exit 2.
func ParseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cli/ && go vet ./internal/cli/ && gofmt -l internal/cli/`
Expected: PASS; no vet errors; gofmt clean.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): shared zero-dep command registry"
```

---

## Task 2: at-cove adopts `cli`

Rewrite `cmd/at-cove/main.go`'s `run()` to build a `cli.App`; move each command's flags out of the global loop into its closure; remove the `dispatch` special-case. Refactor `doDispatch` to receive parsed values. Update the affected tests to the flags-after-command placement.

**Files:**
- Modify: `cmd/at-cove/main.go`
- Modify: `cmd/at-cove/main_test.go`

**Interfaces:**
- Consumes: `cli.App`/`Command`/`Globals`/`ParseInterspersed` (Task 1); the existing `doBuild`/`doCreate`/`doConnect`/`doLoop`/`doRecreate`/`doDestroyInstance`/`doStatusInstance` (unchanged) and `resolveKit`, `state.*`, `runner.ExitError`.

- [ ] **Step 1: Replace the body of `run()` and add helpers**

Read the current `run()` (the global-flag loop + positional resolution + the `switch cmd`) and `doDispatch`. Replace `run()`'s body with an `App` builder. Add two helpers. New code:

```go
func run(argv []string, r runner.Runner, lookup func(string) (string, bool), lookPath func(string) (string, error), stdout, stderr io.Writer) int {
	app := cli.App{
		Name: "at-cove", Version: version,
		Commands: []cli.Command{
			{Name: "build", Brief: "assemble the kit's build context", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("build", flag.ContinueOnError)
				fs.SetOutput(errw)
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := kitDirArg(pos, "build", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doBuild(kitDir, r, g.DryRun, out), errw)
			}},
			{Name: "create", Brief: "build the image and start the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("create", flag.ContinueOnError)
				fs.SetOutput(errw)
				ws := fs.String("workspace", "", "share a host workspace path instead of an isolated volume")
				fs.StringVar(ws, "ws", "", "alias for --workspace")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := kitDirArg(pos, "create", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doCreate(kitDir, r, *ws, g.DryRun, out), errw)
			}},
			{Name: "connect", Brief: "open an interactive session in the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("connect", flag.ContinueOnError)
				fs.SetOutput(errw)
				raw := fs.Bool("raw", false, "open a raw shell instead of the agent")
				noAuth := fs.Bool("no-auth", false, "skip the interactive login step")
				fresh := fs.Bool("fresh", false, "start a fresh agent session")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := kitDirArg(pos, "connect", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doConnect(kitDir, r, g.DryRun, *raw, *noAuth, *fresh, out, errw), errw)
			}},
			{Name: "loop", Brief: "run the kit's autonomous loop", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("loop", flag.ContinueOnError)
				fs.SetOutput(errw)
				once := fs.Bool("once", false, "run a single iteration and exit")
				keep := fs.Bool("keep", false, "keep the sandbox after the loop exits")
				interval := fs.String("interval", "", "override the loop interval (duration)")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				// loop takes [<name>] [kit-dir]; a path-looking arg is the kit-dir.
				loopArg, start := "", "."
				switch len(pos) {
				case 0:
				case 1:
					if filepath.IsAbs(pos[0]) || strings.Contains(pos[0], string(filepath.Separator)) {
						start = pos[0]
					} else {
						loopArg = pos[0]
					}
				case 2:
					loopArg, start = pos[0], pos[1]
				default:
					fmt.Fprintln(errw, "at-cove: loop takes [<name>] [kit-dir]")
					return 2
				}
				kitDir, err := resolveKit(start)
				if err != nil {
					fmt.Fprintln(errw, "at-cove:", err)
					return 1
				}
				var override time.Duration
				if *interval != "" {
					d, perr := time.ParseDuration(*interval)
					if perr != nil || d <= 0 {
						fmt.Fprintf(errw, "at-cove: --interval must be a positive duration: %q\n", *interval)
						return 2
					}
					override = d
				}
				return exitCode("at-cove", doLoop(kitDir, r, loopArg, *once, *keep, override, g.DryRun, out, errw), errw)
			}},
			{Name: "recreate", Brief: "destroy and rebuild the sandbox, keeping saved state", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("recreate", flag.ContinueOnError)
				fs.SetOutput(errw)
				ws := fs.String("workspace", "", "share a host workspace path instead of an isolated volume")
				fs.StringVar(ws, "ws", "", "alias for --workspace")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := kitDirArg(pos, "recreate", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doRecreate(kitDir, r, *ws, g.DryRun, out), errw)
			}},
			{Name: "destroy", Brief: "destroy the sandbox and its volumes", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return instanceCmd("destroy", args, r, g, out, errw, func(kitDir string, inst state.Instance) error {
					return doDestroyInstance(kitDir, r, inst, false, g.DryRun, out)
				})
			}},
			{Name: "status", Brief: "print sandbox status", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return instanceCmd("status", args, r, g, out, errw, func(kitDir string, inst state.Instance) error {
					return doStatusInstance(kitDir, r, inst, g.DryRun, out)
				})
			}},
			{Name: "dispatch", Brief: "run one unit of work in a fresh ephemeral sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doDispatch(args, r, g.DryRun, out, errw)
			}},
		},
	}
	return app.Run(argv, stdout, stderr)
}

// kitDirArg resolves an optional single kit-dir positional. Returns (dir, 0) on
// success, ("", 2) for too many args, ("", 1) if resolveKit fails.
func kitDirArg(pos []string, cmd string, stderr io.Writer) (string, int) {
	start := "."
	if len(pos) == 1 {
		start = pos[0]
	} else if len(pos) > 1 {
		fmt.Fprintf(stderr, "at-cove: %s takes at most one kit-dir\n", cmd)
		return "", 2
	}
	kitDir, err := resolveKit(start)
	if err != nil {
		fmt.Fprintln(stderr, "at-cove:", err)
		return "", 1
	}
	return kitDir, 0
}

// instanceCmd handles destroy/status: they share the --loop flag and the
// kit-dir positional, resolving to an interactive or loop instance.
func instanceCmd(cmd string, args []string, r runner.Runner, g cli.Globals, out, errw io.Writer, do func(kitDir string, inst state.Instance) error) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errw)
	loopName := fs.String("loop", "", "target the named loop instance instead of the interactive one")
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := kitDirArg(pos, cmd, errw)
	if code != 0 {
		return code
	}
	inst := state.Interactive
	if *loopName != "" {
		if err := state.ValidLoopName(*loopName); err != nil {
			fmt.Fprintln(errw, "at-cove:", err)
			return 2
		}
		inst = state.LoopInstance(*loopName)
	}
	return exitCode("at-cove", do(kitDir, inst), errw)
}

// exitCode maps a doX error to a process exit code (unwrapping ExitError).
func exitCode(name string, err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	var xe *runner.ExitError
	if errors.As(err, &xe) {
		return xe.ExitCode()
	}
	fmt.Fprintln(stderr, name+":", err)
	return 1
}
```

Delete the now-dead code from the old `run()`: the entire global-flag `for` loop, the `showVersion`/`--version` handling, the positional-resolution block, the `inst`/`--loop` block, and the `switch cmd`. Keep `resolveKit`, `usage` (the old package-level `usage` string may now be unused — remove it if so; the App generates usage), and all `doX` functions. Keep the `main()` that calls `run(os.Args[1:], …)`.

- [ ] **Step 2: Refactor `doDispatch` to receive parsed values**

Change `doDispatch` so the dispatch closure passes it the already-split args — OR keep `doDispatch(args, r, dryRun, stdout, stderr) int` and let it parse via `cli.ParseInterspersed` itself. Simpler and consistent: keep the `doDispatch(args, r, dryRun, stdout, stderr) int` signature (the closure already passes `args` = tokens after `dispatch`), and replace its internal `flag.NewFlagSet(...); fs.Parse(args[1:])` block with:

```go
func doDispatch(args []string, r runner.Runner, dryRun bool, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inPath := fs.String("in", "", "path to the input.json to inject")
	outPath := fs.String("out", "", "path to write the extracted output.json")
	timeout := fs.Duration("timeout", 30*time.Minute, "hard wall-clock cap for the work")
	grace := fs.Duration("grace", 60*time.Minute, "age past which a labeled orphan is scavenged")
	reap := fs.Bool("reap", false, "scavenge dispatch orphans and exit")
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := kitDirArg(pos, "dispatch", stderr)
	if code != 0 {
		return code
	}
	// ... the REST of doDispatch's existing body is unchanged, but now uses
	// `kitDir`, `*inPath`, `*outPath`, `*timeout`, `*grace`, `*reap`, `dryRun`
	// instead of re-parsing. (The old body already referenced these vars; only
	// the arg-parsing/kit-resolution at the top changes to the block above.)
	...
}
```

Keep the remainder of `doDispatch` (the `--reap` early return, the `dispatch.command` check, assemble, backend `DispatchOps` type-assert, `dispatchrun.Dispatch`) exactly as-is — only its top-of-function arg parsing changes to the block above, and `kitDir` now comes from `kitDirArg` (so `dispatch <kit> --in …` and `dispatch --in … <kit>` both work). Ensure the `--dry-run` handling inside `doDispatch` uses the passed `dryRun` param (it already does).

- [ ] **Step 3: Update imports**

Add `"github.com/aethons-tools/cove/internal/cli"` to the imports. Confirm `flag`, `errors`, `time`, `strings`, `filepath` are still imported (they're used by the closures/helpers); remove any import left unused by deleting the old loop (e.g. if `io` is still used by the signature, keep it).

- [ ] **Step 4: Update the affected tests to flags-after-command**

In `cmd/at-cove/main_test.go`, update invocations that put a **command-specific** flag before the command. Rule: `--dry-run`/`--version` stay before the command; move `--raw`/`--no-auth`/`--fresh`/`--once`/`--keep`/`--interval`/`--loop`/`--ws`/`--in`/`--out`/etc. to **after** the command name. Examples:
- `{"--dry-run", "--raw", "--no-auth", "connect", kitDir}` → `{"--dry-run", "connect", "--raw", "--no-auth", kitDir}`
- `{"--dry-run", "--fresh", "connect", kitDir}` → `{"--dry-run", "connect", "--fresh", kitDir}`
- `{"--dry-run", "--interval", "30s", "loop", kitDir}` → `{"--dry-run", "loop", "--interval", "30s", kitDir}`
- `{"--interval", "nonsense", "loop", kitDir}` → `{"loop", "--interval", "nonsense", kitDir}`
- `{"--loop", "foo", "build", kitDir}` (the misuse test) → `{"build", "--loop", "foo", kitDir}`: now `build` has no `--loop` flag, so this is an **unknown-flag** error → still exit 2. Update the test's expected message assertion (if it checked for the old "only valid for destroy and status" text) to just assert `code == 2` (the unknown-flag path prints flag's own error).
- `{"destroy", "--loop", "foo", kitDir}` / `{"status", "--loop", "foo", kitDir}` / `{"recreate", kitDir, "--ws", newPath}` / `{"--dry-run", "dispatch", cove, "--in", inFile, "--out", outFile}` — already flags-after-command, leave unchanged.

Read the whole test file; apply the rule to every invocation. Do not change assertions other than the `--loop`-misuse message noted above.

- [ ] **Step 5: Run the tests**

Run: `go test ./cmd/at-cove/ ./internal/...`
Expected: PASS. Then `go build ./... && go vet ./... && gofmt -l cmd/ internal/`.
Expected: builds; clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-cove/main.go cmd/at-cove/main_test.go
git commit -m "refactor(at-cove): adopt the cli command registry (per-command flags)"
```

---

## Task 3: at-dispatch adopts `cli`

**Files:**
- Modify: `cmd/at-dispatch/main.go`
- Modify: `cmd/at-dispatch/main_test.go` (if it asserts version output)

**Interfaces:**
- Consumes: `cli.App`/`Command`/`Globals` (Task 1); the existing `doServe(args, stdout, stderr) int` (unchanged).

- [ ] **Step 1: Replace `run()` with an App**

Replace `run()`'s switch with:

```go
func run(args []string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name: "at-dispatch", Version: version,
		Commands: []cli.Command{
			{Name: "serve", Brief: "poll the tracker and dispatch ready work", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doServe(args, out, errw)
			}},
		},
	}
	return app.Run(args, stdout, stderr)
}
```

Add the `cli` import. Remove the old `usage` string if now unused (the App generates it). `doServe` is unchanged (it owns its `--config` FlagSet — that's fine; `serve --config x` reaches it via the closure). Note: `doServe`'s own `flag.NewFlagSet(...).Parse(args)` is flags-first; the existing `serve` tests pass `--config` right after `serve`, so no interspersing is needed here — leave `doServe` as-is.

- [ ] **Step 2: Version output**

The App now prints `"at-dispatch <version>"` for both `version` and `--version` (previously `run` printed the bare `version`). If `cmd/at-dispatch/main_test.go` asserts the version output, update it to expect `"at-dispatch "+version`. If no version test exists, skip.

- [ ] **Step 3: Run the tests**

Run: `go test ./cmd/at-dispatch/ ./internal/...`
Expected: PASS. Then `go build ./... && go vet ./... && gofmt -l cmd/`.
Expected: builds; clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/at-dispatch/
git commit -m "refactor(at-dispatch): adopt the cli command registry"
```

---

## Task 4: at-work adopts `cli`

**Files:**
- Modify: `cmd/at-work/main.go`
- Modify: `cmd/at-work/main_test.go` (if it asserts version output)

**Interfaces:**
- Consumes: `cli.App`/`Command`/`Globals` (Task 1); the existing `doPrepare(args, stderr) int` / `doComplete(args, stderr) int` (unchanged).

- [ ] **Step 1: Replace `run()` with an App**

Replace `run()`'s switch with:

```go
func run(args []string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name: "at-work", Version: version,
		Commands: []cli.Command{
			{Name: "prepare", Brief: "clone/branch and write the brief", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doPrepare(args, errw)
			}},
			{Name: "complete", Brief: "broker the agent outcome into commit/push/PR", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doComplete(args, errw)
			}},
		},
	}
	return app.Run(args, stdout, stderr)
}
```

Add the `cli` import. Remove the old `usage` string if now unused. `doPrepare`/`doComplete` are unchanged (they validate their positional counts).

- [ ] **Step 2: Version output**

The App now prints `"at-work <version>"` for `version`/`--version` (previously the bare `version`). Update `cmd/at-work/main_test.go`'s version assertion to expect `"at-work "+version` if present; else skip.

- [ ] **Step 3: Run the tests**

Run: `go test ./cmd/at-work/ ./internal/...`
Expected: PASS. Then `go build ./... && go vet ./... && gofmt -l cmd/`.
Expected: builds; clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/at-work/
git commit -m "refactor(at-work): adopt the cli command registry"
```

---

## Final verification

- [ ] `go test ./...` — all pass.
- [ ] `just build` — all three binaries build.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3`.
- [ ] `go vet ./...` clean; `gofmt -l cmd/ internal/` prints nothing.
- [ ] Spot-check: `at-cove build --raw ./x` now errors (unknown flag for build); `at-cove --dry-run connect ./x --raw` works.

## Notes

- **Behavior changes are intentional and spec'd:** command flags are validated per command (unknown flag → exit 2), and `version` output is uniform (`"<name> <version>"`). These are the only deliberate changes; every `doX` body is untouched.
- **Reconciliation for the implementer:** the exact `Brief` strings are illustrative — match the repo's existing usage wording where the old `usage` string had descriptions. Confirm which imports remain used after deleting the old global-flag loop (`go build` will flag any unused import). The `run()` signature keeps its `lookup`/`lookPath` params (unused params are legal in Go); if the old `run()` body actually used them, thread them into whichever command closure needs them rather than dropping the call.
- **`doServe`/`doPrepare`/`doComplete`/`doDispatch` bodies** stay as-is except `doDispatch`'s top-of-function arg parsing (Task 2 Step 2).
