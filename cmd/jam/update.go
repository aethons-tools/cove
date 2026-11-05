package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cove "github.com/aethons-tools/cove"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/update"
)

// updateRunner is the Runner `jam update` drives install.sh through; tests
// swap in a runner.Fake.
var updateRunner runner.Runner = runner.OS{}

// updateBinDir is the directory the running jam was started from; tests swap it.
var updateBinDir = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Dir(exe), nil
}

// missingCompanions is ["jam-colima"] when an update from a
// legacynames.StudioManager release stopped half-way: that binary is still
// beside jam and jam-colima is not. A jam-only install (the Jam image) has
// neither and stays a no-op when current.
func missingCompanions() []string {
	dir, err := updateBinDir()
	if err != nil {
		return nil
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	if exists(legacynames.StudioManager) && !exists("jam-colima") {
		return []string{"jam-colima"}
	}
	return nil
}

// cmdUpdate updates the on-PATH Jam binaries (jam and jam-colima ship
// in one release archive) via the shared update.Do and the embedded install.sh.
// The current release still installs when a half-finished upgrade left the
// old manager binary beside jam without jam-colima.
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
			Missing: missingCompanions(),
		})
		if err != nil {
			fmt.Fprintf(stderr, "jam update: %v\n", err)
			return 1
		}
		return 0
	}
}
