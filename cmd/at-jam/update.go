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

// updateRunner is the Runner `at-jam update` drives install.sh through; tests
// swap in a runner.Fake.
var updateRunner runner.Runner = runner.OS{}

// cmdUpdate updates the on-PATH cove binaries (at-jam ships in the same release
// archive as at-cove and at-mint) via the shared update.Do — the same embedded
// install.sh flow as `at-cove update`.
func cmdUpdate(getenv func(string) string) func([]string, cli.Globals, io.Writer, io.Writer) int {
	return func(args []string, g cli.Globals, stdout, stderr io.Writer) int {
		fs := flag.NewFlagSet("update", flag.ContinueOnError)
		ver := fs.String("version", "", "pin a release tag to install (default: the latest; equivalently COVE_VERSION)")
		pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
		if !ok {
			return code
		}
		if len(pos) > 0 {
			fmt.Fprintln(stderr, "at-jam update: takes no positional arguments")
			return 2
		}
		err := update.Do(updateRunner, cove.InstallScript, update.Options{
			Binary: "at-jam", Current: version, Pin: *ver, CoveEnv: getenv("COVE_VERSION"), DryRun: g.DryRun, Stdout: stdout,
		})
		if err != nil {
			fmt.Fprintf(stderr, "at-jam update: %v\n", err)
			return 1
		}
		return 0
	}
}
