// Package colimacmd serves the jam-colima binary's
// `colima setup-docker|check-docker` verbs.
package colimacmd

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

// Config finds the colima profile (VM) a manager config names, so the verbs
// prepare the VM the manager runs in. The binary supplies it; this package
// does not parse manager configs itself.
type Config struct {
	DefaultPath string                            // read when --config is absent and this file exists; "" = none
	Profile     func(path string) (string, error) // colima.profile of the config at path; nil = never read
	// Prepare runs first, with --config ("" when absent): it may move an old
	// config dir (noting it on stderr) and returns the path to read instead.
	// nil = none.
	Prepare func(path string, stderr io.Writer) (string, error)
}

// Command returns the `<prog> colima` group: host-side setup of the colima VM
// docker:true kits need (docs/usage/docker-in-sandbox.md). getenv resolves the
// colima.yaml path (COLIMA_HOME, HOME, XDG_CONFIG_HOME) and r runs the check's
// docker probe, both injected so the command stays hermetic in tests; cfg
// resolves --config to a profile.
func Command(prog string, getenv func(string) string, r runner.Runner, cfg Config) func([]string, cli.Globals, io.Writer, io.Writer) int {
	return func(args []string, g cli.Globals, stdout, stderr io.Writer) int {
		if len(args) == 0 {
			fmt.Fprintln(stderr, prog+" colima: expected setup-docker|check-docker")
			return 2
		}
		switch sub, rest := args[0], args[1:]; sub {
		case "setup-docker":
			return colimaSetupDocker(prog, rest, g, getenv, cfg, stdout, stderr)
		case "check-docker":
			return colimaCheckDocker(prog, rest, r, cfg, stdout, stderr)
		default:
			fmt.Fprintf(stderr, "%s colima: unknown subcommand %q (expected setup-docker|check-docker)\n", prog, sub)
			return 2
		}
	}
}

// profileFlags registers --config and --profile on fs; resolve turns them
// into the (normalised) profile: --profile wins, else colima.profile of
// --config (or of cfg.DefaultPath when that file exists), else the default
// profile. It prints which profile it chose and why, and returns the exit
// code for an error (1: Prepare failed, 2: a bad profile or config).
func profileFlags(fs *flag.FlagSet, cfg Config) (resolve func(stdout, stderr io.Writer) (string, int, error)) {
	path := fs.String("config", "", "the manager config whose colima.profile names the VM (default: the default config, when it exists)")
	profile := fs.String("profile", "", "the colima profile (VM) to prepare; overrides --config (default: "+colimacfg.DefaultProfile+")")
	return func(stdout, stderr io.Writer) (string, int, error) {
		from := *path
		if cfg.Prepare != nil {
			var err error
			if from, err = cfg.Prepare(from, stderr); err != nil {
				return "", 1, err
			}
		}
		raw, source := *profile, "--profile"
		if raw == "" {
			if from == "" && cfg.DefaultPath != "" {
				if _, err := os.Stat(cfg.DefaultPath); err == nil {
					from = cfg.DefaultPath
				}
			}
			switch {
			case from != "" && cfg.Profile != nil:
				var err error
				if raw, err = cfg.Profile(from); err != nil {
					return "", 2, err
				}
				source = "colima.profile in " + from
			case cfg.DefaultPath != "":
				source = "no config at " + cfg.DefaultPath
			default:
				source = "default"
			}
		}
		p := colimacfg.NormalizeProfile(raw)
		if !colimacfg.ValidProfile(p) {
			return "", 2, fmt.Errorf("profile %q is not a valid colima profile name (letters, digits, '-', '_', '.')", raw)
		}
		fmt.Fprintf(stdout, "colima profile %s (%s)\n", p, source)
		return p, 0, nil
	}
}

// colimaVerb is the colima command line for verb against profile:
// `colima <verb>`, plus --profile for any but the default profile.
func colimaVerb(verb, profile string) string {
	return "colima " + verb + profileArg(profile)
}

// profileArg is " --profile <p>" for any but the default profile.
func profileArg(profile string) string {
	if profile == colimacfg.DefaultProfile {
		return ""
	}
	return " --profile " + profile
}

// colimaConfigPath is profile's colima.yaml. It mirrors colima's own lookup:
// $COLIMA_HOME; else ~/.colima when that dir exists; else
// $XDG_CONFIG_HOME/colima (XDG_CONFIG_HOME defaulting to ~/.config), then
// <profile>/colima.yaml. dirExists is injected so the lookup is hermetic in tests.
func colimaConfigPath(getenv func(string) string, dirExists func(string) bool, profile string) (string, error) {
	const file = "colima.yaml"
	if h := getenv("COLIMA_HOME"); h != "" {
		return filepath.Join(h, profile, file), nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("cannot locate the colima config: neither COLIMA_HOME nor HOME is set")
	}
	if legacy := filepath.Join(home, ".colima"); dirExists(legacy) {
		return filepath.Join(legacy, profile, file), nil
	}
	xdg := getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "colima", profile, file), nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func colimaSetupDocker(prog string, args []string, g cli.Globals, getenv func(string) string, cfg Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("colima setup-docker", flag.ContinueOnError)
	resolve := profileFlags(fs, cfg)
	version := fs.String("sysbox-version", colimacfg.MinSysboxVersion, "Sysbox CE release the provision hook installs (≥ "+colimacfg.MinSysboxVersion+")")
	dry := fs.Bool("dry-run", false, "print the change as a diff and write nothing")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "%s colima setup-docker: unexpected argument %q\n", prog, pos[0])
		return 2
	}
	if err := colimacfg.ValidateVersion(*version); err != nil {
		fmt.Fprintln(stderr, prog+" colima setup-docker:", err)
		return 2
	}
	profile, code, err := resolve(stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, prog+" colima setup-docker:", err)
		return code
	}
	path, err := colimaConfigPath(getenv, isDir, profile)
	if err != nil {
		fmt.Fprintln(stderr, prog+" colima setup-docker:", err)
		return 1
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // edit a dotfile-managed symlink's target, keeping the link
	}
	in, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Fprintf(stderr, "%s colima setup-docker: colima config not found at %s — run '%s' once to create it\n", prog, path, colimaVerb("start", profile))
		return 1
	} else if err != nil {
		fmt.Fprintln(stderr, prog+" colima setup-docker:", err)
		return 1
	}
	out, changes, err := colimacfg.Apply(in, colimacfg.Options{SysboxVersion: *version})
	if err != nil {
		fmt.Fprintf(stderr, "%s colima setup-docker: %s: %v\n", prog, path, err)
		return 1
	}
	if len(changes) == 0 {
		fmt.Fprintf(stdout, "colima config already set up for docker:true (%s)\n", path)
		fmt.Fprintln(stdout, "verify the running VM with: "+prog+" colima check-docker"+profileArg(profile))
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
		fmt.Fprintln(stderr, prog+" colima setup-docker:", err)
		return 1
	}
	fmt.Fprintf(stdout, "updated %s (backup: %s.bak):\n", path, path)
	printChanges(stdout, changes)
	fmt.Fprintf(stdout, `
next:
  %[3]s    # runs the hook; restarts every cove in the VM
  %[2]s colima check-docker%[4]s    # confirm sysbox-runc is registered

note: the hook skips the install when sysbox-runc is already present, so it
won't upgrade an older Sysbox in an existing VM — upgrade that once by hand
(stop running coves first — the install restarts docker in the VM):
  %[5]s -- sudo sh -c 'curl -fsSL -o /tmp/sysbox.deb https://github.com/nestybox/sysbox/releases/download/v%[1]s/sysbox-ce_%[1]s.linux_$(dpkg --print-architecture).deb && apt-get install -y /tmp/sysbox.deb'
`, *version, prog, colimaVerb("restart", profile), profileArg(profile), colimaVerb("ssh", profile))
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

func colimaCheckDocker(prog string, args []string, r runner.Runner, cfg Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("colima check-docker", flag.ContinueOnError)
	resolve := profileFlags(fs, cfg)
	pos, code, parsed := cli.ParseFlags(fs, args, stdout, stderr)
	if !parsed {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "%s colima check-docker: unexpected argument %q\n", prog, pos[0])
		return 2
	}
	profile, code, err := resolve(stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, prog+" colima check-docker:", err)
		return code
	}
	ok, err := colima.NewWithContext(r, colimacfg.ProfileContext(profile)).HasSysboxRuntime()
	if err != nil {
		fmt.Fprintf(stderr, "%s colima check-docker: %v — is colima running? (%s)\n", prog, err, colimaVerb("start", profile))
		return 1
	}
	if !ok {
		fmt.Fprintf(stderr, "%s colima check-docker: sysbox-runc is not registered in the docker of colima profile %s — run `%s colima setup-docker%s`, then `%s`\n",
			prog, profile, prog, profileArg(profile), colimaVerb("restart", profile))
		return 1
	}
	fmt.Fprintln(stdout, "ok: sysbox-runc is registered — docker:true kits can run")
	return 0
}
