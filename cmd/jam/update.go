package main

import (
	"flag"
	"fmt"
	"io"

	cove "github.com/aethons-tools/cove"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/update"
)

// updateRunner is the Runner `jam update` drives install.sh through; tests
// swap in a runner.Fake.
var updateRunner runner.Runner = runner.OS{}

// cmdUpdate updates the on-PATH jam binary via the shared update.Do and the
// embedded install.sh.
func cmdUpdate(getenv func(string) string) func([]string, cli.Globals, io.Writer, io.Writer) int {
	return func(args []string, g cli.Globals, stdout, stderr io.Writer) int {
		fs := flag.NewFlagSet("update", flag.ContinueOnError)
		ver := fs.String("version", "", "pin a release tag to install (default: the latest; equivalently JAM_VERSION)")
		pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
		if !ok {
			return code
		}
		if len(pos) > 0 {
			fmt.Fprintln(stderr, "jam update: takes no positional arguments")
			return 2
		}
		err := update.Do(updateRunner, cove.InstallScript, update.Options{
			Binary: "jam", Current: version, Pin: *ver, PinEnv: getenv("JAM_VERSION"), DryRun: g.DryRun, Stdout: stdout,
		})
		if err != nil {
			fmt.Fprintf(stderr, "jam update: %v\n", err)
			return 1
		}
		return 0
	}
}
