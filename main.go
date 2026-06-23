// Command atsbx customizes a sbx kit for the local machine and manages
// sandbox VMs via build, create, run, and delete.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/aethons-tools/at-sbx/internal/kit"
	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/sbx"
)

const usage = `atsbx — customize and run sbx sandboxes

Usage:
  atsbx build [kitdir]
  atsbx create <name> <kitdir> [volume...]
  atsbx run <name>
  atsbx delete <name>

Global flags:
  --dry-run   print planned actions without executing
`

func main() {
	code := run(os.Args[1:], runner.OS{}, os.LookupEnv, exec.LookPath, os.Stdout, os.Stderr)
	os.Exit(code)
}

// run parses argv, dispatches to a subcommand, and returns the process exit
// code. The --dry-run flag may appear anywhere in argv.
func run(argv []string, r runner.Runner, lookup func(string) (string, bool), lookPath func(string) (string, error), stdout, stderr io.Writer) int {
	dryRun := false
	var args []string
	for _, a := range argv {
		if a == "--dry-run" || a == "-dry-run" {
			dryRun = true
			continue
		}
		args = append(args, a)
	}

	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	cmd, rest := args[0], args[1:]
	opts := kit.Options{DryRun: dryRun, Stdout: stdout}

	// Real execution needs sbx on PATH; fail clearly before doing any work.
	if !dryRun {
		if _, err := lookPath("sbx"); err != nil {
			fmt.Fprintln(stderr, "atsbx: sbx not found on PATH")
			return 1
		}
	}

	var err error
	switch cmd {
	case "build":
		if len(rest) > 1 {
			fmt.Fprintln(stderr, "atsbx: build takes at most one kitdir")
			return 2
		}
		kitDir := "."
		if len(rest) == 1 {
			kitDir = rest[0]
		}
		err = kit.Build(kitDir, r, lookup, opts)
	case "create":
		if len(rest) < 2 {
			fmt.Fprintln(stderr, "atsbx: create requires <name> <kitdir>")
			return 2
		}
		err = kit.Create(rest[0], rest[1], rest[2:], r, lookup, opts)
	case "run":
		if len(rest) != 1 {
			fmt.Fprintln(stderr, "atsbx: run requires <name>")
			return 2
		}
		err = dispatch(r, opts, sbx.Run(rest[0]))
	case "delete":
		if len(rest) != 1 {
			fmt.Fprintln(stderr, "atsbx: delete requires <name>")
			return 2
		}
		err = dispatch(r, opts, sbx.Remove(rest[0]))
	default:
		fmt.Fprintf(stderr, "atsbx: unknown command %q\n\n%s", cmd, usage)
		return 2
	}

	if err != nil {
		var xe *runner.ExitError
		if errors.As(err, &xe) {
			return xe.ExitCode()
		}
		fmt.Fprintln(stderr, "atsbx:", err)
		return 1
	}
	return 0
}

// dispatch runs (or, under dry-run, prints) a single sbx command.
func dispatch(r runner.Runner, opts kit.Options, args []string) error {
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "would run: sbx %s\n", strings.Join(args, " "))
		return nil
	}
	return r.Run("sbx", args...)
}
