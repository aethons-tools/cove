// Command atsbx runs hardened Claude Code sandboxes from a .atsbx kit
// directory across pluggable VM backends.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aethons-tools/at-sbx/internal/assemble"
	"github.com/aethons-tools/at-sbx/internal/backend"
	_ "github.com/aethons-tools/at-sbx/internal/backend/colima" // register colima
	"github.com/aethons-tools/at-sbx/internal/connect"
	"github.com/aethons-tools/at-sbx/internal/keys"
	"github.com/aethons-tools/at-sbx/internal/kit"
	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/secret"
)

const usage = `atsbx — run hardened Claude Code sandboxes

Usage:
  atsbx build   [kit-dir]
  atsbx create  [kit-dir] [--workspace|--ws <path>]
  atsbx connect [kit-dir]
  atsbx destroy [kit-dir]
  atsbx status  [kit-dir]

If kit-dir is omitted, atsbx walks up from the cwd to the nearest .atsbx/.

Global flags:
  --dry-run   print planned actions without executing
`

func main() {
	code := run(os.Args[1:], runner.OS{}, os.LookupEnv, exec.LookPath, os.Stdout, os.Stderr)
	os.Exit(code)
}

func run(argv []string, r runner.Runner, lookup func(string) (string, bool), lookPath func(string) (string, error), stdout, stderr io.Writer) int {
	dryRun := false
	var args []string
	wsPath := ""
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--dry-run" || a == "-dry-run":
			dryRun = true
		case a == "--workspace" || a == "--ws":
			if i+1 >= len(argv) {
				fmt.Fprintln(stderr, "atsbx: --workspace requires a path")
				return 2
			}
			i++
			wsPath = argv[i]
		default:
			args = append(args, a)
		}
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]

	// Resolve the kit directory (explicit arg or discovery).
	start := "."
	if len(rest) == 1 {
		start = rest[0]
	} else if len(rest) > 1 {
		fmt.Fprintf(stderr, "atsbx: %s takes at most one kit-dir\n", cmd)
		return 2
	}
	kitDir, err := resolveKit(start)
	if err != nil {
		fmt.Fprintln(stderr, "atsbx:", err)
		return 1
	}
	cfg, err := kit.Load(kitDir)
	if err != nil {
		fmt.Fprintln(stderr, "atsbx:", err)
		return 1
	}
	factory, err := backend.Get(cfg.Backend)
	if err != nil {
		fmt.Fprintln(stderr, "atsbx:", err)
		return 1
	}
	b := factory(r)

	switch cmd {
	case "build":
		err = doBuild(kitDir, r, dryRun, stdout)
	case "create":
		err = doCreate(kitDir, cfg, b, r, wsPath, dryRun, stdout)
	case "connect":
		err = doConnect(cfg, b, r, dryRun, stdout)
	case "destroy":
		err = doSimple(b.Destroy, cfg.Name, "destroy", dryRun, stdout)
	case "status":
		err = doStatus(b, cfg.Name, dryRun, stdout)
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

func resolveKit(start string) (string, error) {
	// An explicit path that already ends in .atsbx (or contains config.yml) is used directly.
	if filepath.Base(start) == ".atsbx" {
		return start, nil
	}
	if _, err := os.Stat(filepath.Join(start, "config.yml")); err == nil {
		return start, nil
	}
	return kit.Discover(start)
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "atsbx")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "atsbx")
}

func doBuild(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	buildDir := filepath.Join(kitDir, ".build")
	if dryRun {
		fmt.Fprintf(stdout, "would assemble %s and inject managed key\n", buildDir)
		return nil
	}
	_, pub, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	return assemble.Assemble(kitDir, buildDir, pub)
}

func doCreate(kitDir string, cfg kit.Config, b backend.Backend, r runner.Runner, wsPath string, dryRun bool, stdout io.Writer) error {
	buildDir := filepath.Join(kitDir, ".build")
	ws := backend.WorkspaceMount{Mode: backend.Isolated}
	if wsPath != "" {
		abs, err := filepath.Abs(wsPath)
		if err != nil {
			return err
		}
		ws = backend.WorkspaceMount{Mode: backend.Shared, HostPath: abs}
	}
	if dryRun {
		fmt.Fprintf(stdout, "would assemble %s then backend.Create(%s)\n", buildDir, cfg.Name)
		return nil
	}
	if err := doBuild(kitDir, r, false, stdout); err != nil {
		return err
	}
	return b.Create(backend.CreateContext{Name: cfg.Name, BuildDir: buildDir, Workspace: ws})
}

func doConnect(cfg kit.Config, b backend.Backend, r runner.Runner, dryRun bool, stdout io.Writer) error {
	if dryRun {
		fmt.Fprintf(stdout, "would resolve %d secrets and connect to %s\n", len(cfg.Secrets), cfg.Name)
		return nil
	}
	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	specs := make([]secret.Spec, len(cfg.Secrets))
	for i, s := range cfg.Secrets {
		specs[i] = secret.Spec{Name: s.Name, Command: s.Command}
	}
	return connect.Connect(b, r, connect.SendEnv{R: r}, connect.Options{
		Name:          cfg.Name,
		Secrets:       specs,
		IdentityFile:  priv,
		KnownHostsDir: filepath.Join(configDir(), "known_hosts.d"),
	})
}

func doSimple(fn func(string) error, name, verb string, dryRun bool, stdout io.Writer) error {
	if dryRun {
		fmt.Fprintf(stdout, "would %s %s\n", verb, name)
		return nil
	}
	return fn(name)
}

func doStatus(b backend.Backend, name string, dryRun bool, stdout io.Writer) error {
	if dryRun {
		fmt.Fprintf(stdout, "would query status of %s\n", name)
		return nil
	}
	st, err := b.GetStatus(name)
	if err != nil {
		return err
	}
	labels := map[backend.State]string{
		backend.StateAbsent:  "absent",
		backend.StateStopped: "stopped",
		backend.StateRunning: "running",
	}
	fmt.Fprintln(stdout, labels[st])
	return nil
}
