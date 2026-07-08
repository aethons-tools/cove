// Command at-work is the git/PR worker: `prepare` sets up a branch and drops the
// brief; `complete` reads the agent's outcome and opens the PR. See docs/orchestration/.
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

func gitClient(stderr io.Writer) (*worker.ShellGit, bool) {
	g, err := worker.NewShellGit(os.Getenv("AT_WORK_GIT_TOKEN"))
	if err != nil {
		fmt.Fprintf(stderr, "at-work: %v\n", err)
		return nil, false
	}
	return g, true
}

func doPrepare(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "at-work prepare: expected <input.json>")
		return 2
	}
	in, err := worker.ReadInput(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "at-work: %v\n", err)
		return 1
	}
	g, ok := gitClient(stderr)
	if !ok {
		return 1
	}
	if err := worker.Prepare(context.Background(), ".", in, g); err != nil {
		fmt.Fprintf(stderr, "at-work prepare: %v\n", err)
		return 1
	}
	return 0
}

func doComplete(args []string, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "at-work complete: expected <input.json> <output.json>")
		return 2
	}
	in, err := worker.ReadInput(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "at-work: %v\n", err)
		return 1
	}
	g, ok := gitClient(stderr)
	if !ok {
		return 1
	}
	ch := github.New(os.Getenv("AT_WORK_GIT_TOKEN"), nil)
	out := worker.Complete(context.Background(), ".", in, g, ch)
	if err := worker.WriteOutput(args[1], out); err != nil {
		fmt.Fprintf(stderr, "at-work: write output: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
