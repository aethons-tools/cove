// Command at-work is the git/PR worker: `prepare` sets up a branch and drops the
// brief; `complete` reads the agent's outcome and opens the PR. See docs/orchestration/.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/aethons-tools/cove/internal/dispatch/github"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
)

var version = "dev"

const usage = `at-work — the at-dispatch git/PR worker

Usage:
  at-work prepare  <input.json>                 set up the branch + write .at-work/brief.md
  at-work complete <input.json> <output.json>   read .at-work/outcome.json → commit/push/PR → output.json
  at-work version

Both steps run in the current directory. Env: AT_WORK_GIT_TOKEN (code-host token).
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "prepare":
		return doPrepare(args[1:], stderr)
	case "complete":
		return doComplete(args[1:], stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "at-work: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
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
