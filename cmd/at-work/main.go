// Command at-work is the git/PR worker: `prepare` sets up the work branch from
// .at-work/task.json; `complete` reads the worker's result and opens the PR. See
// docs/usage/at-work.md.
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
		Name: "at-work", Version: version,
		Commands: []cli.Command{
			{Name: "prepare", Brief: "clone and set up the work branch from .at-work/task.json", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
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
	g, err := worker.NewShellGit(os.Getenv("AT_WORK_GIT_TOKEN"))
	if err != nil {
		fmt.Fprintf(stderr, "at-work: %v\n", err)
		return nil, false
	}
	return g, true
}

func doPrepare(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "at-work prepare: takes no arguments (reads .at-work/task.json)")
		return 2
	}
	task, err := worker.ReadTask(".")
	if err != nil {
		fmt.Fprintf(stderr, "at-work prepare: %v\n", err)
		return 1
	}
	g, ok := gitClient(stderr)
	if !ok {
		return 1
	}
	if err := worker.Prepare(context.Background(), ".", task, g); err != nil {
		fmt.Fprintf(stderr, "at-work prepare: %v\n", err)
		return 1
	}
	return 0
}

func doComplete(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "at-work complete: takes no arguments (reads .at-work/, writes .at-work/task-result)")
		return 2
	}
	task, err := worker.ReadTask(".")
	if err != nil {
		fmt.Fprintf(stderr, "at-work complete: %v\n", err)
		return 1
	}
	ext, err := worker.TaskExt(".")
	if err != nil {
		fmt.Fprintf(stderr, "at-work complete: %v\n", err)
		return 1
	}
	g, ok := gitClient(stderr)
	if !ok {
		return 1
	}
	ch := github.New(os.Getenv("AT_WORK_GIT_TOKEN"), nil)
	tr := worker.Complete(context.Background(), ".", task, g, ch)
	if err := worker.WriteTaskResult(".", ext, tr); err != nil {
		fmt.Fprintf(stderr, "at-work: write task-result: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
