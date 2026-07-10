// Command at-task is the git/PR worker: `prepare` sets up the work branch from
// .at-task/task.json; `complete` reads the worker's result and opens the PR. See
// docs/usage/at-task.md.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/dispatch/github"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
)

var version = "dev"

func run(args []string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name: "at-task", Version: version,
		Commands: []cli.Command{
			{Name: "prepare", Brief: "clone and set up the work branch from .at-task/task.json", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doPrepare(args, errw)
			}},
			{Name: "complete", Brief: "broker the worker's result into commit/push/PR", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doComplete(args, errw)
			}},
		},
	}
	return app.Run(args, stdout, stderr)
}

func gitClient(stderr io.Writer) (*worker.ShellGit, bool) {
	g, err := worker.NewShellGit(os.Getenv("AT_TASK_GIT_TOKEN"))
	if err != nil {
		fmt.Fprintf(stderr, "at-task: %v\n", err)
		return nil, false
	}
	return g, true
}

func doPrepare(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "at-task prepare: takes no arguments (reads .at-task/task.json)")
		return 2
	}
	task, err := worker.ReadTask(".")
	if err != nil {
		fmt.Fprintf(stderr, "at-task prepare: %v\n", err)
		return 1
	}
	g, ok := gitClient(stderr)
	if !ok {
		return 1
	}
	if err := worker.Prepare(context.Background(), ".", task, g); err != nil {
		fmt.Fprintf(stderr, "at-task prepare: %v\n", err)
		return 1
	}
	return 0
}

func doComplete(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "at-task complete: takes no arguments (reads .at-task/, writes .at-task/task-result)")
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
		return writeResult(stderr, ext, worker.ErrorResult("at-task could not read the task", err.Error()))
	}
	g, err := worker.NewShellGit(os.Getenv("AT_TASK_GIT_TOKEN"))
	if err != nil {
		return writeResult(stderr, ext, worker.ErrorResult("at-task could not initialize git", err.Error()))
	}
	ch := github.New(os.Getenv("AT_TASK_GIT_TOKEN"), nil)
	tr := worker.Complete(context.Background(), ".", task, g, ch)
	return writeResult(stderr, ext, tr)
}

// writeResult writes tr to .at-task/task-result<ext>. Exit 1 ONLY if the write itself
// fails (there is then no result to deliver); otherwise 0, whatever tr's status is.
func writeResult(stderr io.Writer, ext string, tr worker.TaskResult) int {
	if err := worker.WriteTaskResult(".", ext, tr); err != nil {
		fmt.Fprintf(stderr, "at-task complete: write task-result: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
