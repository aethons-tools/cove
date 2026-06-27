// Command at-cove runs hardened Claude Code sandboxes from a .at-cove kit
// directory across pluggable VM backends.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/backend"
	_ "github.com/aethons-tools/cove/internal/backend/colima" // register colima
	"github.com/aethons-tools/cove/internal/connect"
	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

const usage = `at-cove — run hardened Claude Code sandboxes

Usage:
  at-cove build    [kit-dir]
  at-cove create   [kit-dir] [--workspace|--ws <path>]
  at-cove connect  [kit-dir]
  at-cove recreate [kit-dir] [--workspace|--ws <path>]
  at-cove destroy  [kit-dir]
  at-cove status   [kit-dir]

If kit-dir is omitted, at-cove walks up from the cwd to the nearest .at-cove/.
recreate rebuilds the VM from the kit while keeping its volumes (state — incl.
saved login — and workspace).

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
				fmt.Fprintln(stderr, "at-cove: --workspace requires a path")
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
		fmt.Fprintf(stderr, "at-cove: %s takes at most one kit-dir\n", cmd)
		return 2
	}
	kitDir, err := resolveKit(start)
	if err != nil {
		fmt.Fprintln(stderr, "at-cove:", err)
		return 1
	}
	cfg, err := kit.Load(kitDir)
	if err != nil {
		fmt.Fprintln(stderr, "at-cove:", err)
		return 1
	}
	factory, err := backend.Get(cfg.Backend)
	if err != nil {
		fmt.Fprintln(stderr, "at-cove:", err)
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
	case "recreate":
		err = doRecreate(kitDir, cfg, b, r, wsPath, dryRun, stdout)
	case "destroy":
		err = doSimple(b.Destroy, cfg.Name, "destroy", dryRun, stdout)
	case "status":
		err = doStatus(b, cfg.Name, dryRun, stdout)
	default:
		fmt.Fprintf(stderr, "at-cove: unknown command %q\n\n%s", cmd, usage)
		return 2
	}

	if err != nil {
		var xe *runner.ExitError
		if errors.As(err, &xe) {
			return xe.ExitCode()
		}
		fmt.Fprintln(stderr, "at-cove:", err)
		return 1
	}
	return 0
}

func resolveKit(start string) (string, error) {
	// An explicit path that already ends in .at-cove (or contains config.yml) is used directly.
	if filepath.Base(start) == ".at-cove" {
		return start, nil
	}
	if _, err := os.Stat(filepath.Join(start, "config.yml")); err == nil {
		return start, nil
	}
	return kit.Discover(start)
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "at-cove")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "at-cove")
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

// doRecreate destroys the sandbox container and creates it again, KEEPING the
// volumes. The named volumes — state at /agent-data (including the saved OAuth
// login) and the isolated workspace — survive because Destroy removes only the
// container (docker rm -f, never -v). The destroy is skipped when no container
// exists, so recreate works from any state. --workspace is honored just like
// create.
func doRecreate(kitDir string, cfg kit.Config, b backend.Backend, r runner.Runner, wsPath string, dryRun bool, stdout io.Writer) error {
	if dryRun {
		fmt.Fprintf(stdout, "would destroy %s (keeping volumes) then recreate\n", cfg.Name)
		return nil
	}
	st, err := b.GetStatus(cfg.Name)
	if err != nil {
		return err
	}
	if st != backend.StateAbsent {
		if err := b.Destroy(cfg.Name); err != nil {
			return err
		}
	}
	return doCreate(kitDir, cfg, b, r, wsPath, false, stdout)
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
	return connect.Connect(b, r, connect.StdinScript{R: r}, connect.Options{
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
