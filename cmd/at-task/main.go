// Command at-task is the git/PR worker: `prepare` sets up the work branch from
// .at-task/task.json; `complete` reads the worker's result and opens the PR. See
// docs/usage/at-task.md.
//
// at-task runs inside a hardened VM (driven by `at-cove work`) and is inherently
// unattended, so its diagnostics are structured slog JSONL on stderr, tagged with
// a `step` attr (`prepare`/`complete`) so every record self-identifies its layer.
// The host captures that stderr and merges it into the unified run stream (spec
// docs/superpowers/specs/2026-07-15-structured-logging-design.md §6, §7). Records
// are secret-free by construction (§6.2): only self-owned fields, and any error
// text is scrubbed of the one in-process secret (the code-host token) before it
// reaches a record — parseable records bypass the host's raw-line scrubber.
package main

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/dispatch/github"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/logging"
)

var version = "dev"

func run(args []string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name: "at-task", Version: version,
		Commands: []cli.Command{
			{Name: "prepare", Brief: "clone and set up the work branch from .at-task/task.json", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doPrepare(args, g, errw)
			}},
			{Name: "complete", Brief: "broker the worker's result into commit/push/PR", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doComplete(args, g, errw)
			}},
		},
	}
	return app.Run(args, stdout, stderr)
}

// stepLogger builds at-task's structured logger for one step and a context
// carrying it. Mode/level honor the shared --log-mode/--log-level flags and the
// AT_LOG_MODE/AT_LOG_LEVEL env fallbacks; in the VM stderr is not a TTY, so the
// mode auto-resolves to unattended → JSON to stderr. No file sink is requested
// (at-task has no kit dir), so logging.New never fails here. The returned logger
// carries the step attr so every record self-identifies its layer (spec §7).
func stepLogger(g cli.Globals, stderr io.Writer, step string) (*logging.Logger, context.Context) {
	lg, err := logging.New(logging.Options{
		Mode:   logModeFrom(envOr(g.LogMode, "AT_LOG_MODE")),
		Stderr: stderr,
		Level:  logLevelFrom(envOr(g.LogLevel, "AT_LOG_LEVEL")),
	})
	if err != nil { // defensive: no file sink is requested, so this path is unreachable
		lg, _ = logging.New(logging.Options{Mode: logging.Unattended, Stderr: stderr, Level: slog.LevelInfo})
	}
	lg = lg.With(slog.String("step", step))
	return lg, logging.Into(context.Background(), lg)
}

// scrubErr renders err secret-free for a structured record. at-task's only
// in-process secret is the code-host token (AT_TASK_GIT_TOKEN); records are
// secret-free by construction (spec §6.2), and this masks the token's value from
// any error text as the belt-and-suspenders backstop — a parseable record bypasses
// the host's raw-line scrubber, so at-task must not rely on it.
func scrubErr(err error) string {
	if err == nil {
		return ""
	}
	return logging.Scrub(err.Error(), os.Getenv("AT_TASK_GIT_TOKEN"))
}

func gitClient(ctx context.Context, lg *logging.Logger) (*worker.ShellGit, bool) {
	g, err := worker.NewShellGit(os.Getenv("AT_TASK_GIT_TOKEN"))
	if err != nil {
		lg.ErrorContext(ctx, "init git", slog.String("err", scrubErr(err)))
		return nil, false
	}
	return g, true
}

func doPrepare(args []string, g cli.Globals, stderr io.Writer) int {
	lg, ctx := stepLogger(g, stderr, "prepare")
	defer lg.Close()
	if len(args) != 0 {
		lg.ErrorContext(ctx, "prepare takes no arguments (reads .at-task/task.json)")
		return 2
	}
	task, err := worker.ReadTask(".")
	if err != nil {
		lg.ErrorContext(ctx, "read task", slog.String("err", scrubErr(err)))
		return 1
	}
	gc, ok := gitClient(ctx, lg)
	if !ok {
		return 1
	}
	if err := worker.Prepare(ctx, ".", task, gc); err != nil {
		lg.ErrorContext(ctx, "prepare failed",
			slog.String("err", scrubErr(err)),
			slog.String("issue", task.Issue.Key),
			slog.String("branch", task.Repo.WorkBranch))
		return 1
	}
	lg.InfoContext(ctx, "prepared work branch",
		slog.String("issue", task.Issue.Key),
		slog.String("branch", task.Repo.WorkBranch))
	return 0
}

func doComplete(args []string, g cli.Globals, stderr io.Writer) int {
	lg, ctx := stepLogger(g, stderr, "complete")
	defer lg.Close()
	if len(args) != 0 {
		lg.ErrorContext(ctx, "complete takes no arguments (reads .at-task/, writes .at-task/task-result)")
		return 2
	}
	// Resolve the task-result extension up front, defaulting to JSON, so we can
	// ALWAYS write a task-result — even when the task file is missing/unreadable.
	ext, err := worker.TaskExt(".")
	if err != nil {
		ext = ".json"
	}
	task, err := worker.ReadTask(".")
	if err != nil {
		lg.ErrorContext(ctx, "read task", slog.String("err", scrubErr(err)))
		return writeResult(ctx, lg, ext, worker.ErrorResult("at-task could not read the task", err.Error()))
	}
	gc, err := worker.NewShellGit(os.Getenv("AT_TASK_GIT_TOKEN"))
	if err != nil {
		lg.ErrorContext(ctx, "init git", slog.String("err", scrubErr(err)))
		return writeResult(ctx, lg, ext, worker.ErrorResult("at-task could not initialize git", err.Error()))
	}
	ch := github.New(os.Getenv("AT_TASK_GIT_TOKEN"), nil)
	tr := worker.Complete(ctx, ".", task, gc, ch)
	// The complete step itself succeeded (it ran and will write a result); the run
	// outcome (ok/needs-input/error) is a self-owned, secret-free classification —
	// the host re-classifies severity in its agent-outcome record (§6.3).
	if outcome, oerr := tr.Status.ActiveTask(); oerr == nil {
		lg.InfoContext(ctx, "complete finished",
			slog.String("outcome", outcome),
			slog.String("issue", task.Issue.Key))
	}
	return writeResult(ctx, lg, ext, tr)
}

// writeResult writes tr to .at-task/task-result<ext>. Exit 1 ONLY if the write itself
// fails (there is then no result to deliver); otherwise 0, whatever tr's status is.
func writeResult(ctx context.Context, lg *logging.Logger, ext string, tr worker.TaskResult) int {
	if err := worker.WriteTaskResult(".", ext, tr); err != nil {
		lg.ErrorContext(ctx, "write task-result", slog.String("err", scrubErr(err)))
		return 1
	}
	return 0
}

// logModeFrom maps the --log-mode flag value to a logging.Mode. An empty or
// unrecognized value falls back to logging.Auto (TTY-detected → unattended in the VM).
func logModeFrom(s string) logging.Mode {
	switch s {
	case "attended":
		return logging.Attended
	case "unattended":
		return logging.Unattended
	default:
		return logging.Auto
	}
}

// logLevelFrom maps the --log-level flag value to a slog.Level. An unrecognized
// value falls back to slog.LevelInfo.
func logLevelFrom(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// envOr returns flag when it is non-empty, else os.Getenv(key) — the env fallback
// for global flags left at their zero value (AT_LOG_MODE, AT_LOG_LEVEL).
func envOr(flag, key string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv(key)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
