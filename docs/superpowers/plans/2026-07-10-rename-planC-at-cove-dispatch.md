# Fold `at-dispatch` scheduler → `at-cove dispatch` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fold the standalone `at-dispatch` scheduler binary into a new `at-cove dispatch` subcommand (`at-cove dispatch --config <path>` runs the poll→dispatch→broker loop), delete `cmd/at-dispatch`, and bring the repo down to **two** binaries (`at-cove`, `at-task`).

**Architecture:** The scheduler logic already lives in reusable internal packages (`internal/dispatch/{config,scheduler,linear,exec}`) that `cmd/at-dispatch/main.go` only *wires together*. We move that wiring verbatim into a new `doDispatch` in `cmd/at-cove/main.go`, register a `dispatch` subcommand, port the `serve` tests, then delete the old binary. The scheduler's engine already shells `at-cove work` (Plan B), so nothing about the run path changes — only the entry point. Finally the docs are reorganized: the interface doc, which documents `at-cove work` (not the scheduler), is renamed off the now-colliding "dispatch" name.

**Tech Stack:** Go 1.22 — **no new dependencies** (the folded packages import only `internal/*` + `gopkg.in/yaml.v3`, already the sole dep) and **no logic changes**. Pure move + rename.

**Scope note:** Plan C of 3 for [AET-30](https://linear.app/aethons-tools/issue/AET-30), on branch `feat/cli-rename` (after Plans A + B). This is the plan that reorganizes the orchestration docs — including the doc-file rename Plan B deferred.

## Global Constraints

- **`at-cove dispatch` replaces `at-dispatch serve`.** `dispatch` is the verb directly — there is no `serve` sub-verb. Invocation: `at-cove dispatch --config <path>`.
- **Keep internal package names** (`internal/dispatch`, `internal/dispatch/{config,scheduler,linear,exec,worker,github}`) and their exported API (`config.LoadConfig`, `scheduler.New`, `linear.New`, `dexec.New`). Only the *binary* and its user-facing strings move; the packages stay put.
- **Two binaries after this plan:** `at-cove`, `at-task`. `cmd/at-dispatch/` is deleted.
- **No new `go.mod`/`go.sum` deps.** The folded packages add zero external imports to `at-cove` (all `internal/*` + `yaml.v3`). Verify `go.mod`/`go.sum` are byte-identical after every task.
- **No behavior change** — the scheduler polls, dispatches via `at-cove work`, and brokers exactly as before. Every commit builds + `go test ./...` green; `gofmt` clean.
- **`dispatch` the verb stays in prose** (the scheduler *dispatches* work). Only the proper-noun phrase **"at-cove dispatch interface" / "at-cove dispatch substrate"** — which now collides with the `at-cove dispatch` *command* (the scheduler) — is renamed, because that doc actually documents `at-cove work`.

---

## Task 1: add the `at-cove dispatch` subcommand (code)

Add the scheduler entry point to `at-cove` alongside `work`. `at-dispatch` still exists after this task (both build) — Task 2 removes it. This keeps each commit green.

**Files:**
- Modify: `cmd/at-cove/main.go` (add imports, register `dispatch`, add `doDispatch`)
- Modify: `cmd/at-cove/main_test.go` (port the three serve tests, renamed to `dispatch`)

**Interfaces (consumed, already exist — do not change):**
- `config.LoadConfig(path string) (*config.Config, error)` — `internal/dispatch/config`
- `linear.New(cfg *config.Config, token string, httpClient *http.Client) (scheduler.Tracker, error)` — `internal/dispatch/linear` (pass `nil` for the client)
- `scheduler.New(cfg, tracker, executor, logger) *scheduler.Engine` with `(*Engine).Run(ctx) error` — `internal/dispatch/scheduler`
- `dexec.New() scheduler.Executor` — `internal/dispatch/exec`
- `runner.OS{}.Output(name string, args ...string) (string, error)` — `internal/runner`

- [ ] **Step 1: Add the imports**

In `cmd/at-cove/main.go`'s import block, add the stdlib imports `context`, `log`, `os/signal`, `sort`, `strings`, `syscall`, and the four dispatch packages. `internal/dispatch/exec` **must** be aliased `dexec` because `os/exec` is already imported as `exec` in this file. Final additions:

```go
	"context"
	"log"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/aethons-tools/cove/internal/dispatch/config"
	dexec "github.com/aethons-tools/cove/internal/dispatch/exec"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
```

(`runner` is already imported.) Let `gofmt`/`goimports` order them; keep the existing `internal/...` grouping.

- [ ] **Step 2: Register the `dispatch` subcommand**

In the `cli.App` `Commands` slice in `run(...)`, add this entry immediately after the `work` command entry:

```go
				{Name: "dispatch", Brief: "poll the tracker and dispatch ready work", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
					return doDispatch(args, out, errw)
				}},
```

- [ ] **Step 3: Add `doDispatch`**

Add this function to `cmd/at-cove/main.go` (near `doWork`). It is `cmd/at-dispatch/main.go`'s `doServe` verbatim except the flagset name is `"dispatch"`, the `--config` help and every user-facing string say `at-cove dispatch` (not `at-dispatch serve`/`at-dispatch`), and the log prefix is `at-cove dispatch `. It uses `runner.OS{}` for the host-side token resolver exactly as `doServe` did (the tracker token is resolved on the host; there is no Fake path for the live scheduler):

```go
// doDispatch runs `at-cove dispatch --config <path>`: it loads the scheduler
// config, resolves the tracker API token on the host, connects to the tracker,
// and runs the poll → dispatch → broker loop until SIGINT/SIGTERM. Each ready
// issue is dispatched as a fresh `at-cove work` run (see internal/dispatch/scheduler).
func doDispatch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to the at-cove dispatch config file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		fmt.Fprintln(stderr, "at-cove dispatch: --config <path> is required")
		return 2
	}
	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: %v\n", err)
		return 1
	}

	classes := make([]string, 0, len(cfg.Classes))
	for name := range cfg.Classes {
		classes = append(classes, name)
	}
	sort.Strings(classes)
	fmt.Fprintf(stdout, "at-cove dispatch: config OK — %d class(es): %s\n",
		len(classes), strings.Join(classes, ", "))

	// resolver: run a secret's argv on the host, return trimmed stdout (in memory).
	resolve := func(argv []string) (string, error) {
		out, err := runner.OS{}.Output(argv[0], argv[1:]...)
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(out, "\n"), nil
	}

	token, err := resolve(cfg.Tracker.Token.Command)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: resolve tracker token: %v\n", err)
		return 1
	}

	tracker, err := linear.New(cfg, token, nil)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: connect to Linear: %v\n", err)
		return 1
	}

	logger := log.New(stderr, "at-cove dispatch ", log.LstdFlags)
	engine := scheduler.New(cfg, tracker, dexec.New(), logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Printf("scheduler started (poll %s); Ctrl-C to stop", cfg.Tracker.PollInterval)
	_ = engine.Run(ctx) // returns ctx.Err() on signal — a clean shutdown
	logger.Printf("scheduler stopped")
	return 0
}
```

> If `linear.New`'s signature differs from the interface block above (e.g. no `*http.Client` param), match the **actual** call in `cmd/at-dispatch/main.go:83` (`linear.New(cfg, token, nil)`) — copy that call verbatim.

- [ ] **Step 4: Port the serve tests as dispatch tests**

Add to `cmd/at-cove/main_test.go` (ensure `bytes`, `os`, `path/filepath`, `strings`, and the `runner` package are imported — most already are). These are `cmd/at-dispatch/main_test.go`'s three behavioral serve tests, renamed to `dispatch` and adapted to at-cove's `run(argv, r, lookup, lookPath, stdout, stderr)` signature. The helper is named `writeDispatchConfig`/`dispatchGoodConfig` to avoid collision with at-cove's existing `writeKit`/`writeState`. Because `doDispatch` resolves the token via `runner.OS{}` internally, the `r` passed to `run` is irrelevant here — pass `&runner.Fake{}`. Keep the `token: { command: ["true"]/["false"] }` spacing exact (the `strings.Replace` depends on it):

```go
const dispatchGoodConfig = `
tracker:
  provider: linear
  team: AET
  token:          { command: ["true"] }
  webhook-secret: { command: ["true"] }
  poll-interval: 60s
  states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
classes:
  implement: { mode: autonomous, kit: ./kits/implement, timeout: 30m }
concurrency: 1
reaper-timeout: 45m
`

func writeDispatchConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "at-cove-dispatch.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDispatchTokenResolveFailure: valid config, but the tracker token resolver
// command fails → dispatch exits 1 before constructing the tracker client.
func TestDispatchTokenResolveFailure(t *testing.T) {
	cfg := strings.Replace(dispatchGoodConfig, `token:          { command: ["true"] }`, `token:          { command: ["false"] }`, 1)
	p := writeDispatchConfig(t, cfg)
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--config", p}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "token") {
		t.Fatalf("stderr = %q; want a token-resolution error", errOut.String())
	}
}

// TestDispatchRejectsBadConfig: no tracker section → Validate rejects on
// tracker.provider (config: error), exit 1.
func TestDispatchRejectsBadConfig(t *testing.T) {
	p := writeDispatchConfig(t, "classes:\n  implement: { mode: autonomous, kit: ./kits/implement, timeout: 30m }\n")
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--config", p}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "config:") {
		t.Fatalf("stderr = %q; want a config error", errOut.String())
	}
}

// TestDispatchRequiresConfig: missing --config → usage error, exit 2.
func TestDispatchRequiresConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "--config") {
		t.Fatalf("stderr = %q; want mention of --config", errOut.String())
	}
}
```

(at-cove's `cli.App` already covers `version`/unknown-command/no-args, so those serve tests are not ported.)

- [ ] **Step 5: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
go test ./cmd/at-cove/ -run Dispatch -v    # the three new tests pass
git diff --stat go.mod go.sum              # expect: no output (unchanged)
```
Expected: green; the three `Dispatch` tests pass; `go.mod`/`go.sum` unchanged. `at-dispatch` still builds (removed next task).
```bash
git commit -am "feat(at-cove): add \`dispatch\` subcommand (folds at-dispatch serve)"
```

---

## Task 2: delete `cmd/at-dispatch` + fix build scripts + internal comments

Now that `at-cove dispatch` exists, remove the old binary and scrub the now-stale `at-dispatch` binary references in build scripts and internal package comments (the internal *packages* stay; only the prose naming the dead binary changes).

**Files:** delete `cmd/at-dispatch/` (both files); modify `scripts/build.sh`; modify the doc comments/strings in `internal/dispatch/config/config.go`, `internal/dispatch/linear/linear.go`, `internal/dispatch/scheduler/scheduler.go`, `internal/dispatch/scheduler/engine.go`, `internal/dispatch/exec/exec.go`, `internal/dispatch/config/config_test.go`.

- [ ] **Step 1: Delete the binary**

```bash
git rm cmd/at-dispatch/main.go cmd/at-dispatch/main_test.go
```
(Its three behavioral tests were ported to `cmd/at-cove/main_test.go` in Task 1.)

- [ ] **Step 2: `scripts/build.sh` — drop `at-dispatch` from the build**

- Line 2 comment: `# build.sh — build at-cove, at-dispatch, and at-task, host-sensitive by default.` → `# build.sh — build at-cove and at-task, host-sensitive by default.`
- Line 8 comment: `dist/linux-amd64/{at-cove,at-dispatch,at-task}` → `dist/linux-amd64/{at-cove,at-task}`.
- Line 31: `BINARIES=(at-cove at-dispatch at-task)` → `BINARIES=(at-cove at-task)`.

- [ ] **Step 3: Scrub `at-dispatch` from internal package comments/strings**

Re-grep: `grep -rn "at-dispatch" internal/ --include=*.go`. Replace the *binary* name `at-dispatch` → `at-cove dispatch` in each doc comment, and the temp-dir prefix + test filename:
- `internal/dispatch/config/config.go`: `// Package config defines and loads the at-dispatch configuration:` → `… the at-cove dispatch configuration:`; `// Config is the parsed contents of an at-dispatch config file.` → `… an at-cove dispatch config file.`; `// TrackerConfig wires at-dispatch to one tracker team.` → `… wires at-cove dispatch to one tracker team.`; `// Class maps a handler class to how at-dispatch runs it.` → `… how at-cove dispatch runs it.`
- `internal/dispatch/linear/linear.go`: `// Package linear is at-dispatch's real scheduler.Tracker:` → `… is at-cove dispatch's real scheduler.Tracker:`
- `internal/dispatch/scheduler/scheduler.go`: `// Package scheduler is the at-dispatch engine:` → `… is the at-cove dispatch engine:`
- `internal/dispatch/scheduler/engine.go:62`: `os.MkdirTemp("", "at-dispatch-")` → `os.MkdirTemp("", "at-cove-dispatch-")` (cosmetic temp-dir prefix; keep everything else).
- `internal/dispatch/exec/exec.go`: `// context timeout. It is at-dispatch's real scheduler.Executor.` → `… It is at-cove dispatch's real scheduler.Executor.`
- `internal/dispatch/config/config_test.go:110`: the temp filename `at-dispatch.yml` → `at-cove-dispatch.yml` (arbitrary test path; keep test logic).

- [ ] **Step 4: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
ls cmd/            # expect: at-cove  at-task   (no at-dispatch)
grep -rn "at-dispatch" cmd/ internal/ scripts/ --include=*.go --include=*.sh    # expect: nothing
git diff --stat go.mod go.sum    # expect: unchanged
bash scripts/build.sh            # builds at-cove + at-task only (host target)
```
Expected: green; `cmd/at-dispatch` gone; no `at-dispatch` in code/scripts; `go.mod`/`go.sum` unchanged; build produces exactly two binaries.
```bash
git add -A
git commit -m "rename(cmd): remove at-dispatch binary; scheduler is now \`at-cove dispatch\`"
```

---

## Task 3: reorganize the orchestration docs

Reflect the new command surface (`at-cove dispatch` = scheduler; two binaries) and rename the interface doc off the now-colliding "dispatch" name — it documents `at-cove work`, not the scheduler.

**Files:** `git mv docs/orchestration/at-cove-dispatch-interface.md docs/orchestration/at-cove-work-interface.md`; modify `docs/OVERVIEW.md`, `docs/orchestration/INDEX.md`, `docs/orchestration/scheduler-config.md`, `docs/orchestration/linear-agent-workflow.md`, the renamed `at-cove-work-interface.md`, `internal/dispatch/doc.go`, `AGENTS.md`, `README.md`.

- [ ] **Step 1: Rename the interface doc + update inbound links**

```bash
git mv docs/orchestration/at-cove-dispatch-interface.md docs/orchestration/at-cove-work-interface.md
```
Then fix every inbound link `at-cove-dispatch-interface.md` → `at-cove-work-interface.md` (re-grep: `grep -rn "at-cove-dispatch-interface" . | grep -v docs/superpowers/ | grep -v .build/`). Known referrers: `docs/OVERVIEW.md`, `docs/orchestration/INDEX.md` (the table row + the intro sentence "the dispatch-interface and config docs"), `docs/orchestration/linear-agent-workflow.md` (multiple `[at-cove dispatch interface](at-cove-dispatch-interface.md…)` links, incl. `#three-separated-authorities`, `#worker-contract` anchors — keep the anchors, change file + link text), `docs/orchestration/scheduler-config.md` (if any), and `internal/dispatch/doc.go:5`.

- [ ] **Step 2: Update the renamed doc's own identity**

In `docs/orchestration/at-cove-work-interface.md`:
- Frontmatter `owns:` — `the at-cove dispatch substrate contract, at-cove's host-orchestrated worker bracket…` → `the at-cove work substrate contract, at-cove's host-orchestrated worker bracket…`.
- `summary:`/`read_when:` — leave the `at-cove work`/`at-task` mechanics as-is; they already center on `at-cove work` (no `at-cove dispatch` command phrase appears there).
- H1: `# at-cove Dispatch Interface` → `# at-cove Work Interface`.
- Line ~14: `In this repo the scheduler is the **\`at-dispatch\`** binary and the worker is **\`at-task\`**, both consuming the **\`at-cove\`** CLI` → `In this repo the scheduler is **\`at-cove dispatch\`** and the worker is **\`at-task\`**, both driving the **\`at-cove work\`** command` (the scheduler is no longer a separate binary).
- The three-authorities table row `| **Scheduler** (\`at-dispatch\`) |` → `| **Scheduler** (\`at-cove dispatch\`) |`.
- Bump `updated: 2026-07-10`.

- [ ] **Step 3: `scheduler-config.md` — the scheduler is `at-cove dispatch`**

Replace the binary/command name (the config *packages* and file format are unchanged; only the runner's name changes):
- Frontmatter `summary:`/`read_when:`/`owns:` — `at-dispatch scheduler configuration` / `at-dispatch instance` / `the at-dispatch configuration file format` → `at-cove dispatch scheduler configuration` / `at-cove dispatch instance` / `the at-cove dispatch configuration file format`.
- H1 `# at-dispatch Scheduler Configuration` → `# at-cove dispatch Scheduler Configuration`.
- Line ~14: `An \`at-dispatch\` instance is configured…` → `An \`at-cove dispatch\` instance is configured…`.
- Line ~16 invocation: `at-dispatch serve --config /path/to/at-dispatch.yml` → `at-cove dispatch --config /path/to/at-cove-dispatch.yml`.
- Any other `at-dispatch` in the body → `at-cove dispatch`; the example config filename `at-dispatch.yml` → `at-cove-dispatch.yml`.
- Bump `updated: 2026-07-10`.

- [ ] **Step 4: `INDEX.md` + `linear-agent-workflow.md` — prose**

- `docs/orchestration/INDEX.md`: the intro line "`at-cove work`, the `at-task` worker, and the `at-dispatch` scheduler" → "… and the `at-cove dispatch` scheduler"; the interface-doc table row link text "at-cove-dispatch-interface.md" target already fixed in Step 1 — also update the row's prose if it names the doc "dispatch interface" (keep it describing `at-cove work`); the `scheduler-config.md` row "The at-dispatch configuration schema" → "The at-cove dispatch configuration schema" and "setting up an at-dispatch instance" → "setting up an at-cove dispatch instance". Bump `updated:`.
- `docs/orchestration/linear-agent-workflow.md`: the link **text** `at-cove dispatch interface` → `at-cove work interface` (targets fixed in Step 1); line ~21 `The dispatch substrate — how the scheduler actually launches workers…` → `The worker-execution substrate — how the scheduler actually launches workers…`. Leave the verb "dispatch"/"dispatches"/"dispatch model" untouched. Bump `updated:` if edited.

- [ ] **Step 5: `OVERVIEW.md` — command surface, file tree, two-binaries framing**

- The architecture file-tree block: delete the `cmd/at-dispatch/ …` line; change `internal/dispatch/config/     at-dispatch config: …` → `at-cove dispatch config: …`; annotate `cmd/at-cove/` line to mention it now also hosts `dispatch` if natural (e.g. append `+ work + dispatch` context), or leave the existing `at-cove entry…` line and rely on the scheduler-engine line. Change `internal/dispatch/scheduler/  scheduler engine (poll → claim → dispatch via at-cove → broker) …` — "via at-cove" already fine; leave.
- The "two binaries" paragraph (~line 281): rewrite from `at-dispatch is a **separate executable** that consumes the at-cove CLI…` to reflect that the scheduler is now the **`at-cove dispatch`** subcommand: e.g. "This module builds **two binaries**: `at-cove` (the sandbox substrate, which also hosts the `dispatch` scheduler and the one-shot `work` runner) and `at-task` (the git/PR worker). The scheduler drives work by shelling `at-cove work` — it never imports at-cove's internals. See the [orchestration design](orchestration/INDEX.md)." Drop "It is a skeleton today" if no longer accurate (the scheduler is shipped) — or keep a hedge if the workflow layer is still partial; prefer accuracy.
- Line ~295 `just build … dist/<os>-<arch>/{at-cove,at-dispatch}` → `{at-cove,at-task}`.
- Line ~352 (forward-looking design bullet): `the at-cove dispatch interface it needs` → `the at-cove work interface it needs`.
- Bump `updated:` if OVERVIEW carries one.

- [ ] **Step 6: `internal/dispatch/doc.go`, `AGENTS.md`, `README.md`**

- `internal/dispatch/doc.go`: the doc link `docs/orchestration/at-cove-dispatch-interface.md (the at-cove contract)` → `docs/orchestration/at-cove-work-interface.md (the at-cove work contract)`; if the package comment calls the control plane a "skeleton with no behavior," update to reflect it is now wired into `at-cove dispatch` (the scheduler/config/linear/exec packages are live; the webhook receiver remains future).
- `AGENTS.md`: line 10 `**Binaries:** \`at-cove\`, \`at-dispatch\`` → `**Binaries:** \`at-cove\`, \`at-task\``; line 11 entry points `cmd/at-cove/main.go (the sandbox CLI) and cmd/at-dispatch/main.go (the Linear dispatcher — a skeleton today; …)` → `cmd/at-cove/main.go (the sandbox CLI — `create`/`connect`/`work`/`dispatch`) and cmd/at-task/main.go (the git/PR worker)`. Keep the `docs/orchestration/` pointer.
- `README.md`: line ~77 `just build … dist/<os>-<arch>/{at-cove,at-dispatch}` → `{at-cove,at-task}`; line ~91 "The repo also builds `at-dispatch`, a skeleton of the Linear-driven dispatcher" → describe the scheduler as the `at-cove dispatch` subcommand (and `at-task` as the second binary), matching OVERVIEW.

- [ ] **Step 7: Verify + commit**

```
grep -rn "at-dispatch\|at-cove-dispatch-interface" . --include=*.md --include=*.go | grep -v docs/superpowers/ | grep -v .build/    # expect: nothing
grep -rn "at-cove dispatch interface\|at-cove dispatch substrate" docs/ | grep -v docs/superpowers/    # expect: nothing (renamed to work interface / worker-execution substrate)
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/orchestration/
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/
go build ./... && go test ./...    # doc.go comment change compiles; nothing else touched
```
Expected: no stale `at-dispatch` / old-filename references in live docs+code; docs-audit 0 errors (a pre-existing line-count warning on `linear-agent-workflow.md` is fine); no dangling links to `at-cove-dispatch-interface.md`; build+tests green.
```bash
git add -A
git commit -m "docs(rename): scheduler is \`at-cove dispatch\`; rename interface doc -> at-cove-work-interface.md"
```

---

## Final verification (Plan C)

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` green; `gofmt -l cmd/ internal/` empty; `go.mod`/`go.sum` unchanged.
- [ ] `bash scripts/build.sh` produces exactly `at-cove` and `at-task` (no `at-dispatch`).
- [ ] `at-cove dispatch --config <f>` runs the scheduler (the three ported tests cover config-missing/bad-config/token-fail); the engine still shells `at-cove work`.
- [ ] `grep -rn "at-dispatch\|at-cove-dispatch-interface" . --include=*.md --include=*.go --include=*.sh | grep -v docs/superpowers/ | grep -v .build/` — nothing.
- [ ] Internal packages `internal/dispatch/{config,scheduler,linear,exec}` and their API intact (kept by design); only the binary + prose moved.
- [ ] docs-audit clean on `docs/`; every link resolves; the renamed doc reachable from `INDEX.md`.

## Notes

- **Why fold, not alias:** the scheduler was always a thin `main` over reusable `internal/dispatch/*` packages that `at-cove work` already shares (the engine shells `at-cove work`). One binary that both schedules and executes is simpler to ship and matches the mental model "`at-cove dispatch` finds work and dispatches it as `at-cove work`."
- **Zero new deps:** the folded packages import only `internal/*` + `yaml.v3`; `linear` uses stdlib `net/http`. `go.mod`/`go.sum` do not change — guard this in every task's verification.
- **Doc rename belongs here:** Plan B deliberately kept `at-cove-dispatch-interface.md` stable because this plan makes `at-cove dispatch` a real command, at which point a doc of that name would mislead (it documents `at-cove work`). Renaming to `at-cove-work-interface.md` completes the AET-30 rename coherently.
