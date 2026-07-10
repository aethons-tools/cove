// Command at-cove runs hardened Claude Code sandboxes from a .at-cove kit
// directory across pluggable VM backends.
package main

import (
	"errors"
	"flag"
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
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/connect"
	"github.com/aethons-tools/cove/internal/dispatchrun"
	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/state"
	"github.com/aethons-tools/cove/internal/usersecret"
)

// version is the at-cove build version, stamped at build time via
// -ldflags "-X main.version=...". Defaults to "dev" for plain `go build`.
var version = "dev"

// defaultBackend is the VM backend at-cove uses. The backend is no longer a
// config knob; colima is the only supported backend for now, though the
// backend.Get registry remains multi-backend internally.
const defaultBackend = "colima"

func main() {
	code := run(os.Args[1:], runner.OS{}, os.LookupEnv, exec.LookPath, os.Stdout, os.Stderr)
	os.Exit(code)
}

func run(argv []string, r runner.Runner, lookup func(string) (string, bool), lookPath func(string) (string, error), stdout, stderr io.Writer) int {
	app := cli.App{
		Name: "at-cove", Version: version,
		Commands: []cli.Command{
			{Name: "build", Brief: "assemble the kit's build context", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("build", flag.ContinueOnError)
				fs.SetOutput(errw)
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := kitDirArg(pos, "build", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doBuild(kitDir, r, g.DryRun, out), errw)
			}},
			{Name: "create", Brief: "build the image and start the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("create", flag.ContinueOnError)
				fs.SetOutput(errw)
				ws := fs.String("workspace", "", "share a host workspace path instead of an isolated volume")
				fs.StringVar(ws, "ws", "", "alias for --workspace")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := kitDirArg(pos, "create", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doCreate(kitDir, r, *ws, g.DryRun, out), errw)
			}},
			{Name: "connect", Brief: "open an interactive session in the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("connect", flag.ContinueOnError)
				fs.SetOutput(errw)
				raw := fs.Bool("raw", false, "open a raw shell instead of the agent")
				noAuth := fs.Bool("no-auth", false, "skip the interactive login step")
				fresh := fs.Bool("fresh", false, "start a fresh agent session")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := kitDirArg(pos, "connect", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doConnect(kitDir, r, g.DryRun, *raw, *noAuth, *fresh, out, errw), errw)
			}},
			{Name: "recreate", Brief: "destroy and rebuild the sandbox, keeping saved state", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("recreate", flag.ContinueOnError)
				fs.SetOutput(errw)
				ws := fs.String("workspace", "", "share a host workspace path instead of an isolated volume")
				fs.StringVar(ws, "ws", "", "alias for --workspace")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := kitDirArg(pos, "recreate", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doRecreate(kitDir, r, *ws, g.DryRun, out), errw)
			}},
			{Name: "destroy", Brief: "destroy the sandbox and its volumes", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return instanceCmd("destroy", args, r, g, out, errw, func(kitDir string, inst state.Instance) error {
					return doDestroyInstance(kitDir, r, inst, false, g.DryRun, out)
				})
			}},
			{Name: "status", Brief: "print sandbox status", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return instanceCmd("status", args, r, g, out, errw, func(kitDir string, inst state.Instance) error {
					return doStatusInstance(kitDir, r, inst, g.DryRun, out)
				})
			}},
			{Name: "work", Brief: "run one unit of work in a fresh ephemeral sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doWork(args, r, g.DryRun, out, errw)
			}},
		},
	}
	return app.Run(argv, stdout, stderr)
}

// kitDirArg resolves an optional single kit-dir positional. Returns (dir, 0) on
// success, ("", 2) for too many args, ("", 1) if resolveKit fails.
func kitDirArg(pos []string, cmd string, stderr io.Writer) (string, int) {
	start := "."
	if len(pos) == 1 {
		start = pos[0]
	} else if len(pos) > 1 {
		fmt.Fprintf(stderr, "at-cove: %s takes at most one kit-dir\n", cmd)
		return "", 2
	}
	kitDir, err := resolveKit(start)
	if err != nil {
		fmt.Fprintln(stderr, "at-cove:", err)
		return "", 1
	}
	return kitDir, 0
}

// instanceCmd handles destroy/status: it parses the shared kit-dir positional
// and resolves to the interactive instance.
func instanceCmd(cmd string, args []string, r runner.Runner, g cli.Globals, out, errw io.Writer, do func(kitDir string, inst state.Instance) error) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errw)
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := kitDirArg(pos, cmd, errw)
	if code != 0 {
		return code
	}
	return exitCode("at-cove", do(kitDir, state.Interactive), errw)
}

// exitCode maps a doX error to a process exit code (unwrapping ExitError).
func exitCode(name string, err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	var xe *runner.ExitError
	if errors.As(err, &xe) {
		return xe.ExitCode()
	}
	fmt.Fprintln(stderr, name+":", err)
	return 1
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
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	if err := kit.EnsureGitignore(kitDir); err != nil {
		return err
	}
	_, pub, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	return assemble.Assemble(kitDir, buildDir, pub, cfg.Image)
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
		fmt.Fprintf(stdout, "would build then create %s and write %s\n", cfg.Name, state.Path(kitDir))
		return nil
	}
	b, err := getBackend(defaultBackend, r)
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

// buildState assembles the state snapshot for a created instance: the backend
// handles, the workspace mode, and the kit's secret specs (names + resolver
// commands, never values).
func buildState(cfg kit.Config, inst backend.Instance) state.State {
	st := state.State{
		Name:          cfg.Name,
		Backend:       inst.Backend,
		Container:     inst.Container,
		Image:         inst.Image,
		WorkspaceMode: "isolated",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if inst.Workspace.Mode == backend.Shared {
		st.WorkspaceMode = "shared"
		st.WorkspaceHostPath = inst.Workspace.HostPath
	}
	for name, s := range cfg.Secrets {
		st.Secrets = append(st.Secrets, state.Secret{Name: name, Command: s.Command})
	}
	return st
}

// saveState snapshots the created interactive instance into the kit state file.
func saveState(kitDir string, cfg kit.Config, inst backend.Instance) error {
	return state.Save(kitDir, buildState(cfg, inst))
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
	return connect.Connect(b, r, connect.StdinScript{R: r, Cmd: cmd, Resume: resume, Name: st.Name}, awake.New(), connect.Options{
		Container:       st.Container,
		Secrets:         specs,
		IdentityFile:    priv,
		KnownHostsDir:   filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:        noAuth,
		Stderr:          stderr,
		CredentialsFile: filepath.Join(configDir(), "credentials.json"),
	})
}

// doDestroyInstance tears an instance down under an EXCLUSIVE lock: it refuses
// if any connection holds the shared lock. It removes the container (keeping
// volumes if requested), removes the image, then deletes the state file.
func doDestroyInstance(kitDir string, r runner.Runner, inst state.Instance, keepVolumes, dryRun bool, stdout io.Writer) error {
	st, err := state.LoadFor(kitDir, inst)
	if err != nil {
		return err
	}
	volumes := "removing volumes"
	if keepVolumes {
		volumes = "keeping volumes"
	}
	if dryRun {
		fmt.Fprintf(stdout, "would destroy %s (%s), remove image %s, and delete %s\n",
			st.Container, volumes, st.Image, state.PathFor(kitDir, inst))
		return nil
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}

	lock, err := state.AcquireExclusiveFor(kitDir, inst)
	if err != nil {
		if errors.Is(err, state.ErrLocked) {
			return fmt.Errorf("refusing to destroy %s: it has active connection(s)", st.Container)
		}
		return err
	}
	defer lock.Release()

	bi := instanceFromState(st)
	if err := b.Destroy(bi, keepVolumes); err != nil {
		return err
	}
	return state.DeleteFor(kitDir, inst)
}

// doDestroy tears the interactive instance down for the user-facing `destroy`
// command: it purges the instance's volumes (keepVolumes=false). recreate calls
// the keepVolumes=true path directly so the saved login survives.
func doDestroy(kitDir string, r runner.Runner, keepVolumes, dryRun bool, stdout io.Writer) error {
	return doDestroyInstance(kitDir, r, state.Interactive, keepVolumes, dryRun, stdout)
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
		if err := doDestroy(kitDir, r, true, false, stdout); err != nil {
			return err
		}
	}
	return doCreate(kitDir, r, wsPath, false, stdout)
}

func doStatusInstance(kitDir string, r runner.Runner, inst state.Instance, dryRun bool, stdout io.Writer) error {
	st, err := state.LoadFor(kitDir, inst)
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

// workName derives a container name unique to one dispatch run: the kit
// name for readability, plus the pid and a nanosecond timestamp so concurrent
// dispatches of the same kit (even from separate processes) never collide.
func workName(kitName string) string {
	return fmt.Sprintf("at-cove-work-%s-%d-%d", kitName, os.Getpid(), time.Now().UnixNano())
}

// doWork runs `at-cove work <kit-dir> --in <f> --out <f> [--timeout]
// [--grace] [--reap]`: a synchronous, one-shot run of the kit's dispatch
// command in a fresh ephemeral hardened VM (or, with --reap, just a scavenge of
// crashed dispatch orphans). It parses the kit-dir positional itself (rather
// than through the shared single-kit-dir resolution in run(), which does not
// know about these flags), assembles the build context and resolves secrets
// exactly as `create`/`connect` do, then hands off to dispatchrun. With dryRun
// it prints the planned actions and returns before touching the backend,
// assembling, or resolving any secret — mirroring doBuild/doCreate's dry-run
// convention.
func doWork(args []string, r runner.Runner, dryRun bool, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("work", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inPath := fs.String("in", "", "path to the local task file to inject (e.g. task.json)")
	outPath := fs.String("out", "", "path to write the extracted result (e.g. task-result.json)")
	timeout := fs.Duration("timeout", 30*time.Minute, "hard wall-clock cap for the work")
	grace := fs.Duration("grace", 60*time.Minute, "age past which a labeled orphan is scavenged")
	reap := fs.Bool("reap", false, "scavenge dispatch orphans and exit")
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "at-cove work: expected <kit-dir>")
		return 2
	}
	kitDir, code := kitDirArg(pos, "work", stderr)
	if code != 0 {
		return code
	}
	if !*reap && (*inPath == "" || *outPath == "") {
		fmt.Fprintln(stderr, "at-cove work: --in and --out are required (unless --reap)")
		return 2
	}

	cfg, err := kit.Load(kitDir)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}

	if dryRun {
		if *reap {
			fmt.Fprintf(stdout, "would scavenge %s dispatch orphans older than %s\n", cfg.Name, *grace)
			return 0
		}
		if len(cfg.Workers) == 0 {
			fmt.Fprintf(stderr, "at-cove: kit %q declares no workers\n", cfg.Name)
			return 1
		}
		img := "at-cove-for-" + cfg.Name
		fmt.Fprintf(stdout, "would dispatch %s (kit-dir %s, image %s): scavenge orphans, build image, run an ephemeral labeled container, inject %s, run the at-task worker bracket (prepare → agent → complete), extract %s, then destroy the container\n",
			cfg.Name, kitDir, img, *inPath, *outPath)
		return 0
	}

	b, err := getBackend(defaultBackend, r)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	ops, ok := b.(backend.DispatchOps)
	if !ok {
		fmt.Fprintf(stderr, "at-cove: backend %q does not support dispatch\n", defaultBackend)
		return 1
	}

	if *reap {
		if err := dispatchrun.Reap(ops, *grace, time.Now()); err != nil {
			fmt.Fprintf(stderr, "at-cove: reap: %v\n", err)
			return 1
		}
		return 0
	}

	if len(cfg.Workers) == 0 {
		fmt.Fprintf(stderr, "at-cove: kit %q declares no workers\n", cfg.Name)
		return 1
	}

	// Assemble the build context (public key injected), as `build`/`create` do.
	buildDir := filepath.Join(kitDir, ".build")
	priv, pub, err := keys.Ensure(r, configDir())
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	if err := assemble.Assemble(kitDir, buildDir, pub, cfg.Image); err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}

	specs := make([]secret.Spec, 0, len(cfg.Secrets))
	for name, s := range cfg.Secrets {
		specs = append(specs, secret.Spec{Name: name, Command: s.Command})
	}

	err = dispatchrun.Dispatch(dispatchrun.Options{
		Ops: ops, R: r, Cfg: cfg, BuildDir: buildDir, Name: workName(cfg.Name),
		Secrets:         specs,
		CredentialsFile: filepath.Join(configDir(), "credentials.json"),
		IdentityFile:    priv,
		KnownHostsDir:   filepath.Join(configDir(), "known_hosts.d"),
		InputPath:       *inPath, OutputPath: *outPath,
		Timeout: *timeout, GraceWindow: *grace, Now: time.Now(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "at-cove work: %v\n", err)
		return 1
	}
	return 0
}
