// Command at-dispatch is the Linear-driven dispatcher that schedules work onto
// at-cove sandboxes. It is currently a skeleton — see docs/orchestration/.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aethons-tools/cove/internal/dispatch"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `at-dispatch — Linear-driven dispatcher for at-cove sandboxes (skeleton)

Usage:
  at-dispatch version      print the build version
  at-dispatch serve        run the dispatcher (not implemented yet)

Status: skeleton only. See docs/orchestration/ for the design.
`

// run is the testable entry point: it returns a process exit code and writes
// only to the provided streams (no direct os.Stdout/os.Stderr use).
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "serve":
		err := dispatch.Serve()
		fmt.Fprintln(stderr, err)
		if errors.Is(err, dispatch.ErrNotImplemented) {
			return 1
		}
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "at-dispatch: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
