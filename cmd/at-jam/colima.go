package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/backend/colima"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/colimacfg"
	"github.com/aethons-tools/cove/internal/runner"
)

// cmdColima returns the `at-jam colima` group: host-side setup of the colima VM
// docker:true kits need (docs/usage/docker-in-sandbox.md). getenv resolves the
// config path (COLIMA_HOME, HOME, XDG_CONFIG_HOME) and r runs the check's docker
// probe, both injected so the command stays hermetic in tests.
func cmdColima(getenv func(string) string, r runner.Runner) func([]string, cli.Globals, io.Writer, io.Writer) int {
	return func(args []string, g cli.Globals, stdout, stderr io.Writer) int {
		if len(args) == 0 {
			fmt.Fprintln(stderr, "at-jam colima: expected setup-docker|check-docker")
			return 2
		}
		switch sub, rest := args[0], args[1:]; sub {
		case "setup-docker":
			return colimaSetupDocker(rest, g, getenv, stdout, stderr)
		case "check-docker":
			return colimaCheckDocker(rest, r, stdout, stderr)
		default:
			fmt.Fprintf(stderr, "at-jam colima: unknown subcommand %q (expected setup-docker|check-docker)\n", sub)
			return 2
		}
	}
}

// colimaConfigPath is the default profile's colima.yaml — the only profile a cove
// uses, since the colima backend pins docker to the `colima` context. It mirrors
// colima's own lookup: $COLIMA_HOME; else ~/.colima when that dir exists; else
// $XDG_CONFIG_HOME/colima (XDG_CONFIG_HOME defaulting to ~/.config). dirExists is
// injected so the lookup is hermetic in tests.
func colimaConfigPath(getenv func(string) string, dirExists func(string) bool) (string, error) {
	const file = "colima.yaml"
	if h := getenv("COLIMA_HOME"); h != "" {
		return filepath.Join(h, "default", file), nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("cannot locate the colima config: neither COLIMA_HOME nor HOME is set")
	}
	if legacy := filepath.Join(home, ".colima"); dirExists(legacy) {
		return filepath.Join(legacy, "default", file), nil
	}
	xdg := getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "colima", "default", file), nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func colimaSetupDocker(args []string, g cli.Globals, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("colima setup-docker", flag.ContinueOnError)
	version := fs.String("sysbox-version", colimacfg.MinSysboxVersion, "Sysbox CE release the provision hook installs (≥ "+colimacfg.MinSysboxVersion+")")
	dry := fs.Bool("dry-run", false, "print the change as a diff and write nothing")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "at-jam colima setup-docker: unexpected argument %q\n", pos[0])
		return 2
	}
	if err := colimacfg.ValidateVersion(*version); err != nil {
		fmt.Fprintln(stderr, "at-jam colima setup-docker:", err)
		return 2
	}
	path, err := colimaConfigPath(getenv, isDir)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam colima setup-docker:", err)
		return 1
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // edit a dotfile-managed symlink's target, keeping the link
	}
	in, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Fprintf(stderr, "at-jam colima setup-docker: colima config not found at %s — run 'colima start' once to create it\n", path)
		return 1
	} else if err != nil {
		fmt.Fprintln(stderr, "at-jam colima setup-docker:", err)
		return 1
	}
	out, changes, err := colimacfg.Apply(in, colimacfg.Options{SysboxVersion: *version})
	if err != nil {
		fmt.Fprintf(stderr, "at-jam colima setup-docker: %s: %v\n", path, err)
		return 1
	}
	if len(changes) == 0 {
		fmt.Fprintf(stdout, "colima config already set up for docker:true (%s)\n", path)
		fmt.Fprintln(stdout, "verify the running VM with: at-jam colima check-docker")
		return 0
	}
	if *dry || g.DryRun {
		fmt.Fprintf(stdout, "would change %s:\n", path)
		printChanges(stdout, changes)
		fmt.Fprintln(stdout)
		fmt.Fprint(stdout, colimacfg.Diff(in, out))
		return 0
	}
	if err := writeColimaConfig(path, in, out); err != nil {
		fmt.Fprintln(stderr, "at-jam colima setup-docker:", err)
		return 1
	}
	fmt.Fprintf(stdout, "updated %s (backup: %s.bak):\n", path, path)
	printChanges(stdout, changes)
	fmt.Fprintf(stdout, `
next:
  colima restart                  # runs the hook; restarts every cove in the VM
  at-jam colima check-docker      # confirm sysbox-runc is registered

note: the hook skips the install when sysbox-runc is already present, so it
won't upgrade an older Sysbox in an existing VM — upgrade that once by hand
(stop running coves first — the install restarts docker in the VM):
  colima ssh -- sudo sh -c 'curl -fsSL -o /tmp/sysbox.deb https://github.com/nestybox/sysbox/releases/download/v%[1]s/sysbox-ce_%[1]s.linux_$(dpkg --print-architecture).deb && apt-get install -y /tmp/sysbox.deb'
`, *version)
	return 0
}

func printChanges(w io.Writer, changes []colimacfg.Change) {
	for _, c := range changes {
		fmt.Fprintln(w, "  -", c.What)
	}
}

// writeColimaConfig backs the original up to <path>.bak, then replaces path
// atomically (temp file in the same dir + rename), keeping its file mode.
func writeColimaConfig(path string, orig, next []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if err := os.WriteFile(path+".bak", orig, mode); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".colima.yaml.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(next); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func colimaCheckDocker(args []string, r runner.Runner, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("colima check-docker", flag.ContinueOnError)
	pos, code, parsed := cli.ParseFlags(fs, args, stdout, stderr)
	if !parsed {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "at-jam colima check-docker: unexpected argument %q\n", pos[0])
		return 2
	}
	ok, err := colima.NewWithContext(r, "").HasSysboxRuntime() // the default profile's context
	if err != nil {
		fmt.Fprintf(stderr, "at-jam colima check-docker: %v — is colima running? (colima start)\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stderr, "at-jam colima check-docker: sysbox-runc is not registered in the colima VM's docker — run `at-jam colima setup-docker`, then `colima restart`")
		return 1
	}
	fmt.Fprintln(stdout, "ok: sysbox-runc is registered — docker:true kits can run")
	return 0
}
