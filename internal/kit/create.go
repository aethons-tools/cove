package kit

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/sbx"
)

// Create builds the kit, then starts a new sandbox from the packed kit.
// Build always runs first (packing is cheap, so no staleness check). Under
// opts.DryRun it prints the planned commands and executes nothing.
func Create(name, kitDir string, volumes []string, r runner.Runner, lookup func(string) (string, bool), opts Options) error {
	if err := Build(kitDir, r, lookup, opts); err != nil {
		return err
	}
	runArgs := sbx.CreateRun(name, ZipPath(kitDir), volumes)
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "would run: sbx %s\n", strings.Join(runArgs, " "))
		return nil
	}
	return r.Run("sbx", runArgs...)
}
