// Command at-cove runs hardened Claude Code sandboxes from a .at-cove kit
// directory across pluggable VM backends.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/awake"
	"github.com/aethons-tools/cove/internal/backend"
	_ "github.com/aethons-tools/cove/internal/backend/colima" // register colima
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/connect"
	dexec "github.com/aethons-tools/cove/internal/dispatch/exec"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/dispatchrun"
	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/mint"
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
				kd := kitDirFlag(fs)
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := resolveKitDir(*kd, pos, "build", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doBuild(kitDir, r, g.DryRun, out), errw)
			}},
			{Name: "create", Brief: "build the image and start the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("create", flag.ContinueOnError)
				fs.SetOutput(errw)
				kd := kitDirFlag(fs)
				ws := fs.String("workspace", "", "share a host workspace path instead of an isolated volume")
				fs.StringVar(ws, "ws", "", "alias for --workspace")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := resolveKitDir(*kd, pos, "create", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doCreate(kitDir, r, *ws, g.DryRun, out), errw)
			}},
			{Name: "connect", Brief: "open an interactive session in the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("connect", flag.ContinueOnError)
				fs.SetOutput(errw)
				kd := kitDirFlag(fs)
				raw := fs.Bool("raw", false, "open a raw shell instead of the agent")
				noAuth := fs.Bool("no-auth", false, "skip the interactive login step")
				fresh := fs.Bool("fresh", false, "start a fresh agent session")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := resolveKitDir(*kd, pos, "connect", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doConnect(kitDir, r, g.DryRun, *raw, *noAuth, *fresh, out, errw), errw)
			}},
			{Name: "recreate", Brief: "destroy and rebuild the sandbox, keeping saved state", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("recreate", flag.ContinueOnError)
				fs.SetOutput(errw)
				kd := kitDirFlag(fs)
				ws := fs.String("workspace", "", "share a host workspace path instead of an isolated volume")
				fs.StringVar(ws, "ws", "", "alias for --workspace")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := resolveKitDir(*kd, pos, "recreate", errw)
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
			{Name: "dispatch", Brief: "poll the tracker and dispatch ready work", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doDispatch(args, out, errw)
			}},
		},
	}
	return app.Run(argv, stdout, stderr)
}

// kitDirFlag registers the standard --kit-dir flag on fs (default ".", i.e. the
// current directory / single-kit resolution). Every command that targets a kit
// registers it.
func kitDirFlag(fs *flag.FlagSet) *string {
	return fs.String("kit-dir", ".", "kit directory (default: current dir / the single kit)")
}

// resolveKitDir resolves the --kit-dir flag value to a kit directory, rejecting
// any leftover positional (commands other than `chat` take none).
func resolveKitDir(flagVal string, pos []string, cmd string, stderr io.Writer) (string, int) {
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "at-cove: %s takes no positional arguments (use --kit-dir)\n", cmd)
		return "", 2
	}
	kitDir, err := resolveKit(flagVal)
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
	kd := kitDirFlag(fs)
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := resolveKitDir(*kd, pos, cmd, errw)
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

// canonicalKitPath returns the symlink-resolved absolute path of a kit dir — the
// key secrets.local.yml uses to disambiguate same-named kits. Falls back to the
// cleaned absolute path when the dir cannot be resolved.
func canonicalKitPath(kitDir string) string {
	abs, err := filepath.Abs(kitDir)
	if err != nil {
		abs = filepath.Clean(kitDir)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
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
	_, pub, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	// assemble.Assemble ensures the kit's .gitignore (as every .build path does).
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
// handles, the workspace mode, and the kit's secret demands (names only — a kit
// never carries a resolver command; supply is resolved machine-side at connect
// time via internal/usersecret).
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
	for name := range cfg.Secrets {
		st.Secrets = append(st.Secrets, state.Secret{Name: name})
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

	// Demand (from state) resolved against supply (the machine-side secrets files),
	// keyed by the kit name recorded in state and this checkout's canonical path.
	demanded := make([]string, len(st.Secrets))
	for i, s := range st.Secrets {
		demanded[i] = s.Name
	}
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		return err
	}
	expand := mint.Expander(r, store.Global, "") // connect mints no github token (no repo scope)
	specs, unresolved, err := store.Plan(st.Name, canonicalKitPath(kitDir), demanded, expand)
	if err != nil {
		return err
	}
	for _, name := range unresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q is demanded but has no supply for kit %q in %s (or secrets.local.yml); it will not be set\n", name, st.Name, secretsPath)
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

// planRequired resolves one required demand for a kit through the supply store.
// It errors, naming the secret and the secrets files, if nothing supplies it.
func planRequired(store usersecret.Store, expand usersecret.MintExpander, kitName, kitPath, name, secretsPath string) (secret.Spec, error) {
	specs, unresolved, err := store.Plan(kitName, kitPath, []string{name}, expand)
	if err != nil {
		return secret.Spec{}, err
	}
	if len(unresolved) > 0 {
		return secret.Spec{}, fmt.Errorf("%s has no supply entry for kit %q in %s (or secrets.local.yml)", name, kitName, secretsPath)
	}
	return specs[0], nil
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
	kd := kitDirFlag(fs)
	inPath := fs.String("in", "", "path to the local task file to inject (e.g. task.json)")
	outPath := fs.String("out", "", "path to write the extracted result (e.g. task-result.json)")
	timeout := fs.Duration("timeout", 30*time.Minute, "hard wall-clock cap for the work")
	grace := fs.Duration("grace", 60*time.Minute, "age past which a labeled orphan is scavenged")
	reap := fs.Bool("reap", false, "scavenge dispatch orphans and exit")
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := resolveKitDir(*kd, pos, "work", stderr)
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

	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	kitPath := canonicalKitPath(kitDir)
	// General (agent-injected) secrets: demand names from the kit; unresolved warn.
	demanded := make([]string, 0, len(cfg.Secrets))
	for name := range cfg.Secrets {
		demanded = append(demanded, name)
	}
	// A github minter scopes its token to the kit's repo, passed to at-mint as the
	// non-secret --repo flag.
	repo := ""
	if cfg.SourceControl != nil && cfg.SourceControl.GitHub != nil {
		repo = cfg.SourceControl.GitHub.Project
	}
	expand := mint.Expander(r, store.Global, repo)
	specs, unresolved, err := store.Plan(cfg.Name, kitPath, demanded, expand)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	for _, name := range unresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q has no supply for kit %q in %s (or secrets.local.yml); it will not be set\n", name, cfg.Name, secretsPath)
	}
	// The code-host token stays a distinct demand (the air-gap); required, fail closed.
	gitName, ok := cfg.GitTokenName()
	if !ok {
		fmt.Fprintf(stderr, "at-cove: kit %q declares no source-control.github.secrets AT_TASK_GIT_TOKEN\n", cfg.Name)
		return 1
	}
	gitTok, err := planRequired(store, expand, cfg.Name, kitPath, gitName, secretsPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}

	err = dispatchrun.Dispatch(dispatchrun.Options{
		Ops: ops, R: r, Cfg: cfg, BuildDir: buildDir, Name: workName(cfg.Name),
		Secrets:  specs,
		GitToken: gitTok,
		// A dispatched worker authenticates to Anthropic via an injected
		// ANTHROPIC_API_KEY secret, NOT the interactive subscription OAuth login.
		// So we deliberately do not seed credentials.json: with no OAuth token to
		// fall back to, a keyless worker fails closed instead of silently burning
		// the subscription. (connect still seeds it for interactive sessions.)
		CredentialsFile: "",
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

// doDispatch runs `at-cove dispatch [kit-dir]`: it loads the kit, resolves the
// tracker API token on the host, connects to the tracker, and runs the poll →
// dispatch → broker loop until SIGINT/SIGTERM. Each ready issue is dispatched as
// a fresh `at-cove work` run (see internal/dispatch/scheduler).
func doDispatch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kd := kitDirFlag(fs)
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := resolveKitDir(*kd, pos, "dispatch", stderr)
	if code != 0 {
		return code
	}
	cfg, err := kit.Load(kitDir)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: %v\n", err)
		return 1
	}
	// dispatch requires the full scheduler surface.
	if cfg.SourceControl == nil || cfg.Tracker == nil || cfg.Tracker.Linear == nil || cfg.Dispatch == nil || len(cfg.Workers) == 0 {
		fmt.Fprintln(stderr, "at-cove dispatch: kit must declare source-control, tracker.linear, dispatch, and at least one worker")
		return 1
	}

	classes := make([]string, 0, len(cfg.Workers))
	for name := range cfg.Workers {
		if _, err := cfg.ResolvedWorker(name); err == nil {
			classes = append(classes, name)
		}
	}
	sort.Strings(classes)
	if len(classes) == 0 {
		fmt.Fprintln(stderr, "at-cove dispatch: kit declares no dispatchable worker class (only <common>?)")
		return 1
	}
	fmt.Fprintf(stdout, "at-cove dispatch: kit OK — %d worker class(es): %s\n", len(classes), strings.Join(classes, ", "))

	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: %v\n", err)
		return 1
	}
	kitPath := canonicalKitPath(kitDir)
	expand := mint.Expander(runner.OS{}, store.Global, "") // dispatch resolves the tracker token, not a github token
	planned, err := planRequired(store, expand, cfg.Name, kitPath, "AT_DISPATCH_TRACKER_TOKEN", secretsPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: %v\n", err)
		return 1
	}
	resolved, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{planned})
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: resolve tracker token: %v\n", err)
		return 1
	}
	token := resolved["AT_DISPATCH_TRACKER_TOKEN"]

	tracker, err := linear.New(cfg, token, nil)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: connect to Linear: %v\n", err)
		return 1
	}
	logger := log.New(stderr, "at-cove dispatch ", log.LstdFlags)
	engine := scheduler.New(cfg, kitDir, tracker, dexec.New(), logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Printf("scheduler started (poll %s); Ctrl-C to stop", cfg.Tracker.Linear.PollInterval)
	_ = engine.Run(ctx) // returns ctx.Err() on signal — a clean shutdown
	logger.Printf("scheduler stopped")
	return 0
}
