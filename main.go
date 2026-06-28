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
	"time"

	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/awake"
	"github.com/aethons-tools/cove/internal/backend"
	_ "github.com/aethons-tools/cove/internal/backend/colima" // register colima
	"github.com/aethons-tools/cove/internal/connect"
	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/state"
	"github.com/aethons-tools/cove/internal/usersecret"
)

const usage = `at-cove — run hardened Claude Code sandboxes

Usage:
  at-cove build    [kit-dir]
  at-cove create   [kit-dir] [--workspace|--ws <path>]
  at-cove connect  [kit-dir] [--raw] [--no-auth] [--fresh]
  at-cove recreate [kit-dir] [--workspace|--ws <path>]
  at-cove destroy  [kit-dir]
  at-cove status   [kit-dir]
  at-cove version

If kit-dir is omitted, at-cove walks up from the cwd to the nearest .at-cove/.
create records the running instance in .at-cove/.state/state.json; connect,
destroy, and status operate on that, not on config.yml. recreate rebuilds the
VM from the kit while keeping its volumes (state — incl. saved login — and
workspace).

Global flags:
  --dry-run   print planned actions without executing
  --version   print the at-cove version and exit

connect flags:
  --raw       launch bash instead of claude (debug what the agent sees)
  --no-auth   skip the claude auth login step
  --fresh     start a new session instead of resuming the last one
`

// version is the at-cove build version, stamped at build time via
// -ldflags "-X main.version=...". Defaults to "dev" for plain `go build`.
var version = "dev"

func main() {
	code := run(os.Args[1:], runner.OS{}, os.LookupEnv, exec.LookPath, os.Stdout, os.Stderr)
	os.Exit(code)
}

func run(argv []string, r runner.Runner, lookup func(string) (string, bool), lookPath func(string) (string, error), stdout, stderr io.Writer) int {
	dryRun := false
	showVersion := false
	raw := false
	noAuth := false
	fresh := false
	var args []string
	wsPath := ""
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--dry-run" || a == "-dry-run":
			dryRun = true
		case a == "--version":
			showVersion = true
		case a == "--raw":
			raw = true
		case a == "--no-auth":
			noAuth = true
		case a == "--fresh":
			fresh = true
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
	if showVersion {
		fmt.Fprintln(stdout, "at-cove "+version)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]

	// version needs no kit.
	if cmd == "version" {
		fmt.Fprintln(stdout, "at-cove "+version)
		return 0
	}

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

	switch cmd {
	case "build":
		err = doBuild(kitDir, r, dryRun, stdout)
	case "create":
		err = doCreate(kitDir, r, wsPath, dryRun, stdout)
	case "connect":
		err = doConnect(kitDir, r, dryRun, raw, noAuth, fresh, stdout, stderr)
	case "recreate":
		err = doRecreate(kitDir, r, wsPath, dryRun, stdout)
	case "destroy":
		err = doDestroy(kitDir, r, dryRun, stdout)
	case "status":
		err = doStatus(kitDir, r, dryRun, stdout)
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

func getBackend(name string, r runner.Runner) (backend.Backend, error) {
	f, err := backend.Get(name)
	if err != nil {
		return nil, err
	}
	return f(r), nil
}

func doBuild(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	buildDir := filepath.Join(kitDir, ".build")
	if dryRun {
		fmt.Fprintf(stdout, "would write %s/.gitignore, assemble %s, and inject managed key\n", kitDir, buildDir)
		return nil
	}
	if err := kit.EnsureGitignore(kitDir); err != nil {
		return err
	}
	_, pub, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	return assemble.Assemble(kitDir, buildDir, pub)
}

func doCreate(kitDir string, r runner.Runner, wsPath string, dryRun bool, stdout io.Writer) error {
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	if state.Exists(kitDir) {
		return fmt.Errorf("%q is already created; run `at-cove recreate` or `at-cove destroy` first", cfg.Name)
	}
	ws := backend.WorkspaceMount{Mode: backend.Isolated}
	if wsPath != "" {
		abs, err := filepath.Abs(wsPath)
		if err != nil {
			return err
		}
		ws = backend.WorkspaceMount{Mode: backend.Shared, HostPath: abs}
	}
	if dryRun {
		fmt.Fprintf(stdout, "would build then create %s (backend %s) and write %s\n", cfg.Name, cfg.Backend, state.Path(kitDir))
		return nil
	}
	b, err := getBackend(cfg.Backend, r)
	if err != nil {
		return err
	}
	if err := doBuild(kitDir, r, false, stdout); err != nil {
		return err
	}
	inst, err := b.Create(backend.CreateContext{
		Name: cfg.Name, BuildDir: filepath.Join(kitDir, ".build"), Workspace: ws,
	})
	if err != nil {
		return err
	}
	return saveState(kitDir, cfg, inst)
}

// saveState snapshots the created instance and the kit's secret specs (names +
// resolver commands, never values) into the kit state file.
func saveState(kitDir string, cfg kit.Config, inst backend.Instance) error {
	st := state.State{
		Name:          cfg.Name,
		Backend:       inst.Backend,
		Container:     inst.Container,
		Image:         inst.Image,
		WorkspaceMode: "isolated",
		Setup:         cfg.Setup,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if inst.Workspace.Mode == backend.Shared {
		st.WorkspaceMode = "shared"
		st.WorkspaceHostPath = inst.Workspace.HostPath
	}
	for _, s := range cfg.Secrets {
		st.Secrets = append(st.Secrets, state.Secret{Name: s.Name, Command: s.Command})
	}
	return state.Save(kitDir, st)
}

func instanceFromState(st state.State) backend.Instance {
	ws := backend.WorkspaceMount{Mode: backend.Isolated}
	if st.WorkspaceMode == "shared" {
		ws = backend.WorkspaceMount{Mode: backend.Shared, HostPath: st.WorkspaceHostPath}
	}
	return backend.Instance{Backend: st.Backend, Container: st.Container, Image: st.Image, Workspace: ws}
}

// doConnect launches an interactive session in the sandbox, driven by the
// recorded state (not the kit). It resolves each demanded secret from its kit
// command or, failing that, the user's ~/.config/at-cove/secrets.yml; secrets
// with neither warn (non-fatal) and are left unset. It holds a SHARED lock on
// the state file for the whole session, so destroy can't tear the sandbox down
// underneath it. With raw it drops into bash instead of claude; with noAuth it
// skips `claude auth login`.
func doConnect(kitDir string, r runner.Runner, dryRun, raw, noAuth, fresh bool, stdout, stderr io.Writer) error {
	st, err := state.Load(kitDir)
	if err != nil {
		return err
	}

	// Demand (from state) resolved against supply (the user's secrets.yml).
	demanded := make([]secret.Spec, len(st.Secrets))
	for i, s := range st.Secrets {
		demanded[i] = secret.Spec{Name: s.Name, Command: s.Command}
	}
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	store, err := usersecret.Load(secretsPath)
	if err != nil {
		return err
	}
	specs, unresolved := store.Plan(demanded)
	for _, name := range unresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q is demanded by the kit but has no command and no entry in %s; it will not be set\n", name, secretsPath)
	}

	launch := "claude"
	if raw {
		launch = "bash"
	}
	resume := !raw && !fresh
	if dryRun {
		auth := "with auth"
		if noAuth {
			auth = "no auth"
		}
		session := "resuming"
		if !resume {
			session = "fresh"
		}
		fmt.Fprintf(stdout, "would resolve %d secrets and connect to %s, launching %s (%s, %s)\n",
			len(specs), st.Container, launch, auth, session)
		return nil
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}

	lock, err := state.AcquireShared(kitDir)
	if err != nil {
		if errors.Is(err, state.ErrLocked) {
			return fmt.Errorf("sandbox %q is being destroyed; try again shortly", st.Container)
		}
		return err
	}
	defer lock.Release()

	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	cmd := ""
	if raw {
		cmd = "bash"
	}
	return connect.Connect(b, r, connect.StdinScript{R: r, Cmd: cmd, Resume: resume}, awake.New(), connect.Options{
		Container:     st.Container,
		Secrets:       specs,
		IdentityFile:  priv,
		KnownHostsDir: filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:      noAuth,
		Stderr:        stderr,
	})
}

// doDestroy tears the sandbox down under an EXCLUSIVE lock: it refuses if any
// connection holds the shared lock. It removes the container (keeping volumes)
// and image, then deletes the state file.
func doDestroy(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	st, err := state.Load(kitDir)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(stdout, "would destroy %s (keeping volumes), remove image %s, and delete %s\n",
			st.Container, st.Image, state.Path(kitDir))
		return nil
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}

	lock, err := state.AcquireExclusive(kitDir)
	if err != nil {
		if errors.Is(err, state.ErrLocked) {
			return fmt.Errorf("refusing to destroy %s: it has active connection(s)", st.Container)
		}
		return err
	}
	defer lock.Release()

	if err := b.Destroy(instanceFromState(st)); err != nil {
		return err
	}
	return state.Delete(kitDir)
}

// doRecreate tears down the existing instance (under the exclusive lock, so it
// refuses with active connections) and creates a fresh one, keeping volumes.
func doRecreate(kitDir string, r runner.Runner, wsPath string, dryRun bool, stdout io.Writer) error {
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	// Recreate keeps volumes, but a shared workspace is a host bind-mount, not a
	// volume — it must be re-specified at `docker run`. When the caller does not
	// pass --ws, recover the previously shared workspace from state so recreate
	// preserves it instead of silently reverting to an isolated volume. This must
	// happen before doDestroy, which deletes the state file.
	if wsPath == "" {
		if st, err := state.Load(kitDir); err == nil && st.WorkspaceMode == "shared" {
			wsPath = st.WorkspaceHostPath
		}
	}
	if dryRun {
		fmt.Fprintf(stdout, "would destroy any existing %s (keeping volumes) then recreate\n", cfg.Name)
		return nil
	}
	if state.Exists(kitDir) {
		if err := doDestroy(kitDir, r, false, stdout); err != nil {
			return err
		}
	}
	return doCreate(kitDir, r, wsPath, false, stdout)
}

func doStatus(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	st, err := state.Load(kitDir)
	if errors.Is(err, state.ErrNotCreated) {
		fmt.Fprintln(stdout, "absent (not created)")
		return nil
	}
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(stdout, "would query status of %s\n", st.Container)
		return nil
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}
	vmState, err := b.GetStatus(st.Container)
	if err != nil {
		return err
	}
	labels := map[backend.State]string{
		backend.StateAbsent:  "absent",
		backend.StateStopped: "stopped",
		backend.StateRunning: "running",
	}
	fmt.Fprintf(stdout, "%s  (image %s)\n", labels[vmState], st.Image)
	return nil
}
