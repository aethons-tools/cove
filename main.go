// Command at-cove runs hardened Claude Code sandboxes from a .at-cove kit
// directory across pluggable VM backends.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/awake"
	"github.com/aethons-tools/cove/internal/backend"
	_ "github.com/aethons-tools/cove/internal/backend/colima" // register colima
	"github.com/aethons-tools/cove/internal/connect"
	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/loop"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
	"github.com/aethons-tools/cove/internal/state"
	"github.com/aethons-tools/cove/internal/usersecret"
)

const usage = `at-cove — run hardened Claude Code sandboxes

Usage:
  at-cove build    [kit-dir]
  at-cove create   [kit-dir] [--workspace|--ws <path>]
  at-cove connect  [kit-dir] [--raw] [--no-auth] [--fresh]
  at-cove loop     [<name>] [kit-dir] [--once] [--keep] [--interval <dur>]
  at-cove recreate [kit-dir] [--workspace|--ws <path>]
  at-cove destroy  [kit-dir] [--loop <name>]
  at-cove status   [kit-dir] [--loop <name>]
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

  --loop <name>  act on the named loop instance (destroy, status)

loop flags:
  --once             run one drain (all currently-available work) and exit
  --keep             reuse/leave the loop instance instead of auto-destroying it
  --interval <dur>   override the loop's poll interval (e.g. 30s, 5m)
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
	once := false
	keep := false
	intervalStr := ""
	var args []string
	wsPath := ""
	loopName := ""
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
		case a == "--once":
			once = true
		case a == "--keep":
			keep = true
		case a == "--interval":
			if i+1 >= len(argv) {
				fmt.Fprintln(stderr, "at-cove: --interval requires a duration")
				return 2
			}
			i++
			intervalStr = argv[i]
		case a == "--workspace" || a == "--ws":
			if i+1 >= len(argv) {
				fmt.Fprintln(stderr, "at-cove: --workspace requires a path")
				return 2
			}
			i++
			wsPath = argv[i]
		case a == "--loop":
			if i+1 >= len(argv) {
				fmt.Fprintln(stderr, "at-cove: --loop requires a name")
				return 2
			}
			i++
			loopName = argv[i]
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

	// Resolve positionals. Most commands take an optional kit-dir; `loop` takes
	// an optional loop name first, then an optional kit-dir.
	start := "."
	loopArg := ""
	if cmd == "loop" {
		switch len(rest) {
		case 0:
		case 1:
			// If the single arg looks like a path (absolute, or contains a separator),
			// treat it as the kit-dir; otherwise treat it as the loop name.
			if filepath.IsAbs(rest[0]) || strings.Contains(rest[0], string(filepath.Separator)) {
				start = rest[0]
			} else {
				loopArg = rest[0]
			}
		case 2:
			loopArg, start = rest[0], rest[1]
		default:
			fmt.Fprintln(stderr, "at-cove: loop takes [<name>] [kit-dir]")
			return 2
		}
	} else if len(rest) == 1 {
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

	var intervalOverride time.Duration
	if intervalStr != "" {
		d, err := time.ParseDuration(intervalStr)
		if err != nil || d <= 0 {
			fmt.Fprintf(stderr, "at-cove: --interval must be a positive duration: %q\n", intervalStr)
			return 2
		}
		intervalOverride = d
	}

	inst := state.Interactive
	if loopName != "" {
		if err := state.ValidLoopName(loopName); err != nil {
			fmt.Fprintln(stderr, "at-cove:", err)
			return 2
		}
		if cmd != "destroy" && cmd != "status" {
			fmt.Fprintln(stderr, "at-cove: --loop is only valid for destroy and status")
			return 2
		}
		inst = state.LoopInstance(loopName)
	}

	switch cmd {
	case "build":
		err = doBuild(kitDir, r, dryRun, stdout)
	case "create":
		err = doCreate(kitDir, r, wsPath, dryRun, stdout)
	case "connect":
		err = doConnect(kitDir, r, dryRun, raw, noAuth, fresh, stdout, stderr)
	case "loop":
		err = doLoop(kitDir, r, loopArg, once, keep, intervalOverride, dryRun, stdout, stderr)
	case "recreate":
		err = doRecreate(kitDir, r, wsPath, dryRun, stdout)
	case "destroy":
		err = doDestroyInstance(kitDir, r, inst, dryRun, stdout)
	case "status":
		err = doStatusInstance(kitDir, r, inst, dryRun, stdout)
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

// buildState assembles the state snapshot for a created instance: the backend
// handles, the workspace mode, the setup command to seed an isolated workspace,
// and the kit's secret specs (names + resolver commands, never values).
func buildState(cfg kit.Config, inst backend.Instance, setup string) state.State {
	st := state.State{
		Name:          cfg.Name,
		Backend:       inst.Backend,
		Container:     inst.Container,
		Image:         inst.Image,
		WorkspaceMode: "isolated",
		Setup:         setup,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if inst.Workspace.Mode == backend.Shared {
		st.WorkspaceMode = "shared"
		st.WorkspaceHostPath = inst.Workspace.HostPath
	}
	for _, s := range cfg.Secrets {
		st.Secrets = append(st.Secrets, state.Secret{Name: s.Name, Command: s.Command})
	}
	return st
}

// saveState snapshots the created interactive instance into the kit state file.
func saveState(kitDir string, cfg kit.Config, inst backend.Instance) error {
	return state.Save(kitDir, buildState(cfg, inst, cfg.Setup))
}

// loopContainer is the backend container/volume name for a named loop: it
// suffixes the kit name so loop instances never collide with the interactive
// instance or each other, while sharing the kit image (CreateContext.Kit).
func loopContainer(kitName, loopName string) string {
	return kitName + "-loop-" + loopName
}

// declaresSecret reports whether the kit declares a secret with the given name.
func declaresSecret(cfg kit.Config, name string) bool {
	for _, s := range cfg.Secrets {
		if s.Name == name {
			return true
		}
	}
	return false
}

// createLoopInstance provisions a dedicated, isolated sandbox for one named loop:
// it builds the shared kit image, runs a loop-suffixed container with its own
// volumes, and records a loop state file with the resolved setup command. It
// fails fast (before any build) when the loop is undefined, ANTHROPIC_API_KEY is
// not declared (an unattended run cannot log in interactively), or the loop
// instance already exists.
func createLoopInstance(kitDir string, r runner.Runner, cfg kit.Config, loopName string, stdout io.Writer) (state.State, error) {
	lp, ok := cfg.Loops[loopName]
	if !ok {
		return state.State{}, fmt.Errorf("no loop %q defined in config.yml", loopName)
	}
	if !declaresSecret(cfg, "ANTHROPIC_API_KEY") {
		return state.State{}, fmt.Errorf("loop %q requires ANTHROPIC_API_KEY to be declared in config.yml secrets (unattended runs cannot log in interactively)", loopName)
	}
	if state.ExistsFor(kitDir, state.LoopInstance(loopName)) {
		return state.State{}, fmt.Errorf("loop %q already has an instance; run `at-cove destroy --loop %s` first", loopName, loopName)
	}
	// Resolve the backend before building (matching doCreate), so a bad backend
	// name fails fast instead of after an orphaned `docker build`.
	b, err := getBackend(cfg.Backend, r)
	if err != nil {
		return state.State{}, err
	}
	if err := doBuild(kitDir, r, false, stdout); err != nil {
		return state.State{}, err
	}
	inst, err := b.Create(backend.CreateContext{
		Name:      loopContainer(cfg.Name, loopName),
		Kit:       cfg.Name,
		BuildDir:  filepath.Join(kitDir, ".build"),
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		return state.State{}, err
	}
	setup := lp.Setup
	if setup == "" {
		setup = cfg.Setup
	}
	st := buildState(cfg, inst, setup)
	if err := state.SaveFor(kitDir, state.LoopInstance(loopName), st); err != nil {
		return state.State{}, err
	}
	return st, nil
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
	setupCmd := st.Setup
	if st.WorkspaceMode == "shared" {
		setupCmd = "" // the host bind-mount already holds the code
	}
	return connect.Connect(b, r, connect.StdinScript{R: r, Cmd: cmd, Resume: resume}, awake.New(), connect.Options{
		Container:     st.Container,
		Secrets:       specs,
		IdentityFile:  priv,
		KnownHostsDir: filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:      noAuth,
		Stderr:        stderr,
		Setup:         setupCmd,
	})
}

// doDestroyInstance tears an instance down under an EXCLUSIVE lock: it refuses
// if any connection (or a running loop) holds the shared lock. It removes the
// container (keeping volumes), removes the image for the interactive instance
// but NOT for a loop instance (which shares the kit image), then deletes the
// state file.
func doDestroyInstance(kitDir string, r runner.Runner, inst state.Instance, dryRun bool, stdout io.Writer) error {
	st, err := state.LoadFor(kitDir, inst)
	if err != nil {
		return err
	}
	if dryRun {
		if inst == state.Interactive && !state.HasLoopInstances(kitDir) {
			fmt.Fprintf(stdout, "would destroy %s (keeping volumes), remove image %s, and delete %s\n",
				st.Container, st.Image, state.PathFor(kitDir, inst))
		} else {
			fmt.Fprintf(stdout, "would destroy %s (keeping volumes and the shared image) and delete %s\n",
				st.Container, state.PathFor(kitDir, inst))
		}
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
	if inst != state.Interactive {
		// A loop instance shares the kit image; keep it unless this is the last
		// instance overall (no interactive, no other loop), in which case reclaim it.
		last := !state.Exists(kitDir) && !state.OtherLoopInstancesExist(kitDir, inst)
		if !last {
			bi.Image = ""
		}
	} else if state.HasLoopInstances(kitDir) {
		bi.Image = "" // loop instances still depend on the shared kit image; keep it
	}
	if err := b.Destroy(bi); err != nil {
		return err
	}
	return state.DeleteFor(kitDir, inst)
}

func doDestroy(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	return doDestroyInstance(kitDir, r, state.Interactive, dryRun, stdout)
}

// maxDrain caps consecutive triggers in a single drain, a safety valve so an
// unattended loop whose check never clears cannot spin forever; it resets each
// poll.
const maxDrain = 1000

// doLoop runs a scheduled, unattended agent loop against a dedicated sandbox: it
// auto-creates the loop instance (or reuses one with --keep), drains the loop's
// check — running the agent on each trigger — then polls every interval, until
// SIGINT/SIGTERM. It holds the host awake for the duration and, unless --keep,
// destroys the instance on exit.
func doLoop(kitDir string, r runner.Runner, loopName string, once, keep bool, intervalOverride time.Duration, dryRun bool, stdout, stderr io.Writer) error {
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	if loopName == "" {
		loopName = "default"
	}
	lp, ok := cfg.Loops[loopName]
	if !ok {
		return fmt.Errorf("no loop %q defined in config.yml", loopName)
	}
	interval := lp.ParsedInterval()
	if intervalOverride > 0 {
		interval = intervalOverride
	}
	if dryRun {
		fmt.Fprintf(stdout, "would run loop %q every %s (once=%v, keep=%v): check %q, prompt %q\n",
			loopName, interval, once, keep, lp.Check, lp.Prompt)
		return nil
	}

	inst := state.LoopInstance(loopName)
	exists := state.ExistsFor(kitDir, inst)
	if exists && !keep {
		return fmt.Errorf("loop %q already has an instance; run `at-cove destroy --loop %s` or pass --keep to reuse it", loopName, loopName)
	}

	// Hold the host awake for the whole loop.
	if release, err := awake.New().Inhibit(); err != nil {
		fmt.Fprintf(stderr, "at-cove: warning: could not prevent host sleep: %v\n", err)
	} else {
		defer release()
	}

	var st state.State
	if exists {
		st, err = state.LoadFor(kitDir, inst)
	} else {
		st, err = createLoopInstance(kitDir, r, cfg, loopName, stdout)
	}
	if err != nil {
		return err
	}
	if !keep {
		defer func() { _ = doDestroyInstance(kitDir, r, inst, false, io.Discard) }()
	}

	// Block destroy/recreate of this instance while the loop runs.
	lock, err := state.AcquireSharedFor(kitDir, inst)
	if err != nil {
		return err
	}
	defer lock.Release()

	// Resolve secrets once for the run; ANTHROPIC_API_KEY must actually resolve.
	demanded := make([]secret.Spec, len(st.Secrets))
	for i, s := range st.Secrets {
		demanded[i] = secret.Spec{Name: s.Name, Command: s.Command}
	}
	store, err := usersecret.Load(filepath.Join(configDir(), "secrets.yml"))
	if err != nil {
		return err
	}
	specs, _ := store.Plan(demanded)
	env, err := secret.Resolve(r, specs)
	if err != nil {
		return err
	}
	if env["ANTHROPIC_API_KEY"] == "" {
		return fmt.Errorf("loop %q: ANTHROPIC_API_KEY did not resolve to a value (declare it in config.yml and provide it via the resolver command or secrets.yml)", loopName)
	}

	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}
	ep, cleanup, err := b.Dial(st.Container)
	if err != nil {
		return err
	}
	defer cleanup()
	knownHostsDir := filepath.Join(configDir(), "known_hosts.d")
	if err := os.MkdirAll(knownHostsDir, 0o700); err != nil {
		return err
	}
	tgt := sshargs.Target{
		Host:           ep.Host,
		User:           ep.User,
		Port:           ep.Port,
		IdentityFile:   priv,
		KnownHostsFile: filepath.Join(knownHostsDir, st.Container),
	}

	// Graceful stop: a watcher closes done on SIGINT/SIGTERM; both the drain and
	// the poll observe it.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	done := make(chan struct{})
	go func() {
		<-sig
		fmt.Fprintf(stderr, "loop %q: stopping after the current tick\n", loopName)
		close(done)
	}()
	stopped := func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	sleep := func(d time.Duration) bool {
		select {
		case <-done:
			return false
		case <-time.After(d):
			return true
		}
	}

	triggers := 0
	var lastAgentErr error
	tick := func() bool {
		if stopped() || triggers >= maxDrain {
			return false
		}
		if lp.FreshWorkspace {
			if err := connect.ResetLoopWorkspace(r, tgt); err != nil {
				fmt.Fprintf(stderr, "loop %q: reset: %v\n", loopName, err)
				return false
			}
		}
		if err := connect.SeedLoopWorkspace(r, tgt, env, st.Setup); err != nil {
			fmt.Fprintf(stderr, "loop %q: seed: %v\n", loopName, err)
			return false
		}
		triggered, err := connect.RunCheck(r, tgt, env, lp.Check)
		if err != nil {
			fmt.Fprintf(stderr, "loop %q: check: %v\n", loopName, err)
			return false
		}
		if !triggered {
			triggers = 0
			return false
		}
		triggers++
		fmt.Fprintf(stdout, "loop %q: triggered, running agent\n", loopName)
		if err := connect.RunAgent(r, tgt, env, lp.Prompt); err != nil {
			lastAgentErr = err
			fmt.Fprintf(stderr, "loop %q: agent: %v\n", loopName, err)
		}
		return true
	}
	// reset the drain cap on each poll.
	sleepReset := func(d time.Duration) bool {
		triggers = 0
		return sleep(d)
	}

	loop.Run(once, interval, tick, sleepReset)
	if once && lastAgentErr != nil {
		return fmt.Errorf("loop %q: an agent run failed: %w", loopName, lastAgentErr)
	}
	return nil
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
