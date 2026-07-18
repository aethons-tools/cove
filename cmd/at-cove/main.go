// Command at-cove runs hardened Claude Code sandboxes from a .at-cove kit
// directory across pluggable VM backends.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/connect"
	dexec "github.com/aethons-tools/cove/internal/dispatch/exec"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
	"github.com/aethons-tools/cove/internal/dispatchrun"
	"github.com/aethons-tools/cove/internal/install"
	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
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
			{Name: "install", Brief: "compile the kit: build + gate the image and write install.json", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("install", flag.ContinueOnError)
				fs.SetOutput(errw)
				pd := projectDirFlag(fs)
				allowUnverified := allowUnverifiedBaseFlag(fs)
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := resolveProjectDir(*pd, pos, "install", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doInstall(kitDir, r, *allowUnverified, g.DryRun, out), errw)
			}},
			{Name: "create", Brief: "build the image and start the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("create", flag.ContinueOnError)
				fs.SetOutput(errw)
				pd := projectDirFlag(fs)
				ws := fs.String("workspace", "", "share a host workspace path instead of an isolated volume")
				fs.StringVar(ws, "ws", "", "alias for --workspace")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := resolveProjectDir(*pd, pos, "create", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doCreate(kitDir, r, *ws, g.DryRun, out), errw)
			}},
			{Name: "chat", Brief: "open an interactive collaborator session in the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("chat", flag.ContinueOnError)
				fs.SetOutput(errw)
				pd := projectDirFlag(fs)
				raw := fs.Bool("raw", false, "open a raw shell instead of the agent")
				noAuth := fs.Bool("no-auth", false, "skip the interactive login step")
				fresh := fs.Bool("fresh", false, "start a fresh agent session")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				collaborator := ""
				if len(pos) == 1 {
					collaborator = pos[0]
				} else if len(pos) > 1 {
					fmt.Fprintln(errw, "at-cove: chat takes at most one collaborator")
					return 2
				}
				kitDir, err := resolveKit(*pd)
				if err != nil {
					fmt.Fprintln(errw, "at-cove:", err)
					return 1
				}
				return exitCode("at-cove", doChat(collaborator, kitDir, r, g.DryRun, *raw, *noAuth, *fresh, out, errw), errw)
			}},
			{Name: "recreate", Brief: "destroy and rebuild the sandbox, keeping saved state", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("recreate", flag.ContinueOnError)
				fs.SetOutput(errw)
				pd := projectDirFlag(fs)
				ws := fs.String("workspace", "", "share a host workspace path instead of an isolated volume")
				fs.StringVar(ws, "ws", "", "alias for --workspace")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				kitDir, code := resolveProjectDir(*pd, pos, "recreate", errw)
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
				return doDispatch(args, g, out, errw)
			}},
		},
	}
	return app.Run(argv, stdout, stderr)
}

// logModeFrom maps the --log-mode flag value to a logging.Mode. An empty or
// unrecognized value falls back to logging.Auto (TTY-detected).
func logModeFrom(s string) logging.Mode {
	switch s {
	case "attended":
		return logging.Attended
	case "unattended":
		return logging.Unattended
	default:
		return logging.Auto
	}
}

// logLevelFrom maps the --log-level flag value to a slog.Level. An
// unrecognized value falls back to slog.LevelInfo.
func logLevelFrom(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// envOr returns flag when it is non-empty, else os.Getenv(key) — the env
// fallback for global flags left at their zero value (AT_LOG_MODE,
// AT_LOG_LEVEL).
func envOr(flag, key string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv(key)
}

// allowUnverifiedBaseFlag registers the --allow-unverified-base escape hatch: it
// downgrades the provenance gate's rejection of a base that descends from no
// blessed cove-base-image to a loud warning, then proceeds. It lives only on
// `at-cove install` — the one command that builds a base and runs the gate (COV-38).
func allowUnverifiedBaseFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("allow-unverified-base", false, "harden a base that fails the provenance gate (loud warning; at your own risk)")
}

// projectDirFlag registers the standard --project-dir flag on fs. It names the
// project root, under which .at-cove/ must sit. An empty default means "walk up
// from cwd to the nearest ancestor holding .at-cove". Every command that targets
// a kit registers it.
func projectDirFlag(fs *flag.FlagSet) *string {
	return fs.String("project-dir", "", "project root holding .at-cove/ (default: walk up from cwd)")
}

// resolveProjectDir resolves the --project-dir flag value to a kit directory,
// rejecting any leftover positional (commands other than `chat` take none).
func resolveProjectDir(flagVal string, pos []string, cmd string, stderr io.Writer) (string, int) {
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "at-cove: %s takes no positional arguments (use --project-dir)\n", cmd)
		return "", 2
	}
	kitDir, err := resolveKit(flagVal)
	if err != nil {
		fmt.Fprintln(stderr, "at-cove:", err)
		return "", 1
	}
	return kitDir, 0
}

// instanceCmd handles destroy/status: it parses the shared --project-dir flag
// and resolves to the interactive instance.
func instanceCmd(cmd string, args []string, r runner.Runner, g cli.Globals, out, errw io.Writer, do func(kitDir string, inst state.Instance) error) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errw)
	pd := projectDirFlag(fs)
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := resolveProjectDir(*pd, pos, cmd, errw)
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

// resolveKit resolves the --project-dir flag value to the kit directory. An
// explicit project root <dir> requires .at-cove/ (its config.yml) to sit there,
// erroring clearly otherwise; omitted (empty), it walks up from cwd to the
// nearest ancestor holding .at-cove.
func resolveKit(projectDir string) (string, error) {
	if projectDir == "" {
		return kit.Discover(".")
	}
	kitDir := filepath.Join(projectDir, ".at-cove")
	if _, err := os.Stat(filepath.Join(kitDir, "config.yml")); err != nil {
		return "", fmt.Errorf("no .at-cove/ at project root %s", projectDir)
	}
	return kitDir, nil
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

// assembleContext writes the kit's managed .gitignore and assembles the .build
// context, injecting the managed public key. Used by `install` — the single
// build path — which then builds the image via the backend. Run commands
// (create/recreate/chat) no longer assemble; they consume the installed image.
func assembleContext(kitDir string, r runner.Runner) error {
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	_, pub, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	// assemble.Assemble ensures the kit's .gitignore (as every .build path does).
	return assemble.Assemble(kitDir, filepath.Join(kitDir, ".build"), pub, cfg.Image)
}

// doInstall compiles a kit into a runnable artifact (COV-38): assemble the .build
// context, build + gate + tag the hardened image via the backend, then freeze the
// resolved result into install.json. It is the single build+gate path and the only
// home of --allow-unverified-base. `--dry-run` assembles the context and reports
// what a full install would do (the old `build`'s "assemble + inspect" use),
// without touching docker or writing the manifest.
func doInstall(kitDir string, r runner.Runner, allowUnverifiedBase, dryRun bool, stdout io.Writer) error {
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	if err := assembleContext(kitDir, r); err != nil {
		return err
	}
	buildDir := filepath.Join(kitDir, ".build")
	img := "at-cove-for-" + cfg.Name
	if dryRun {
		fmt.Fprintf(stdout, "assembled %s; would build + gate + tag %s and write %s\n", buildDir, img, install.Path(kitDir))
		return nil
	}
	b, err := getBackend(defaultBackend, r)
	if err != nil {
		return err
	}
	installed, err := b.Install(backend.InstallContext{
		Kit: cfg.Name, BuildDir: buildDir,
		Base: backend.BaseSpec{KitDir: kitDir, Base: cfg.Image.Base, AllowUnverified: allowUnverifiedBase},
	})
	if err != nil {
		return err
	}
	in, err := currencyInputs(kitDir, cfg)
	if err != nil {
		return err
	}
	m := install.Compile(cfg, install.ResolvedBuild{
		Image:        installed.Ref,
		BaseRef:      in.BaseRef,
		BaseDigest:   installed.BaseDigest,
		CurrencyHash: install.CurrencyHash(in),
		InstalledAt:  time.Now().UTC().Format(time.RFC3339),
	})
	if err := install.Save(kitDir, m); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "installed %s: built %s and wrote %s\n", cfg.Name, installed.Ref, install.Path(kitDir))
	return nil
}

// currencyInputs gathers the build-affecting inputs the install manifest hashes
// (§5): the kit source tree, at-cove's embedded build identity, and the base ref
// as configured (or the blessed default). install writes the resulting hash; the
// run commands (S3/S4) recompute it from the live kit to detect a stale install.
func currencyInputs(kitDir string, cfg kit.Config) (install.CurrencyInputs, error) {
	kitTree, err := install.KitSourceTree(kitDir)
	if err != nil {
		return install.CurrencyInputs{}, err
	}
	identity, err := install.AtCoveIdentity()
	if err != nil {
		return install.CurrencyInputs{}, err
	}
	baseRef := cfg.Image.Base
	if baseRef == "" {
		baseRef = basedigest.DefaultRef()
	}
	return install.CurrencyInputs{
		KitSourceTree:       kitTree,
		AtCoveBuildIdentity: identity,
		BaseRef:             baseRef,
	}, nil
}

// loadCurrentInstall loads the kit's install manifest and verifies it is still
// current against the live kit source (COV-38 §5). Run commands consume the
// pre-built image — they never build — so a missing or stale install is a hard
// error pointing the user at `at-cove install`. The currency check recomputes the
// hash from the live kit source + at-cove's embedded identity + the manifest's
// (frozen) base ref; the raw config.yml bytes are folded into KitSourceTree, so
// any edit to the kit source flips the hash even though config.yml is never
// re-interpreted here.
func loadCurrentInstall(kitDir string) (install.Manifest, error) {
	// install.ErrNotInstalled already reads "…(run `at-cove install` first)", so
	// the not-installed case needs no further wrapping.
	m, err := install.Load(kitDir)
	if err != nil {
		return install.Manifest{}, err
	}
	in, err := currencyInputs(kitDir, m.RunConfig)
	if err != nil {
		return install.Manifest{}, err
	}
	if m.Stale(in) {
		return install.Manifest{}, fmt.Errorf("install for %q is stale (the kit changed since the last build); run `at-cove install`", m.Name)
	}
	return m, nil
}

// doCreate consumes the installed image (COV-38): it verifies the install is
// current, runs the pre-built image via the backend (no build), and records the
// running instance in state.json — sourcing the image from the manifest.
func doCreate(kitDir string, r runner.Runner, wsPath string, dryRun bool, stdout io.Writer) error {
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		return err
	}
	cfg := m.RunConfig
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
		fmt.Fprintf(stdout, "would run %s (image %s) and write %s\n", cfg.Name, m.Image, state.Path(kitDir))
		return nil
	}
	b, err := getBackend(defaultBackend, r)
	if err != nil {
		return err
	}
	inst, err := b.Create(backend.CreateContext{
		Name: cfg.Name, Image: m.Image, Workspace: ws,
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

// doChat launches an interactive collaborator session in the sandbox, driven
// by the recorded state (not the kit) plus the kit's collaborator config. It
// resolves each demanded secret from its kit command or, failing that, the
// user's ~/.config/at-cove/secrets.yml; secrets with neither warn (non-fatal)
// and are left unset. It holds a SHARED lock on the state file for the whole
// session, so destroy can't tear the sandbox down underneath it. With raw it
// drops into bash instead of claude; with noAuth it skips `claude auth login`.
// chat is install-aware (unlike the rest of the state-driven commands): it reads
// the resolved run-config (collaborators, secret demands) from install.json —
// never config.yml (COV-38) — and verifies the install is current, since
// selecting a collaborator and its role prompt requires that run-config. A
// missing or stale install is a hard error pointing at `at-cove install`.
func doChat(collaborator, kitDir string, r runner.Runner, dryRun, raw, noAuth, fresh bool, stdout, stderr io.Writer) error {
	st, err := state.Load(kitDir)
	if err != nil {
		return err
	}
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		return err
	}
	cfg := m.RunConfig
	class, hasCollab, err := cfg.SelectCollaborator(collaborator)
	if err != nil {
		return err
	}
	var role kit.Collaborator
	if hasCollab {
		if role, err = cfg.ResolvedCollaborator(class); err != nil {
			return err
		}
	}

	// Demand (from state, plus the selected collaborator's secrets) resolved
	// against supply (the machine-side secrets files), keyed by the kit name
	// recorded in state and this checkout's canonical path.
	demandSet := map[string]struct{}{}
	demanded := make([]string, 0, len(st.Secrets)+len(role.Secrets))
	for _, s := range st.Secrets {
		if _, dup := demandSet[s.Name]; !dup {
			demandSet[s.Name] = struct{}{}
			demanded = append(demanded, s.Name)
		}
	}
	for name := range role.Secrets {
		if _, dup := demandSet[name]; !dup {
			demandSet[name] = struct{}{}
			demanded = append(demanded, name)
		}
	}
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		return err
	}
	expand := mint.Expander(r, store.Global, "") // chat mints no github token (connectors)
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
		who := "no collaborator"
		if hasCollab {
			who = "collaborator " + class
		}
		fmt.Fprintf(stdout, "would resolve %d secrets and connect to %s as %s, launching %s\n",
			len(specs), st.Container, who, launch)
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
		Container:          st.Container,
		Secrets:            specs,
		IdentityFile:       priv,
		KnownHostsDir:      filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:           noAuth,
		Stderr:             stderr,
		CredentialsFile:    filepath.Join(configDir(), "credentials.json"),
		CollaboratorPrompt: role.Prompt,
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
// refuses with active connections) and re-runs the installed image, keeping
// volumes. It no longer rebuilds (COV-38): it verifies the install is current and
// re-runs the pre-built image, so a stale/missing install fails before any
// teardown, pointing at `at-cove install`.
func doRecreate(kitDir string, r runner.Runner, wsPath string, dryRun bool, stdout io.Writer) error {
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		return err
	}
	cfg := m.RunConfig
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

// keysOf returns a kit secrets map's demanded names, for store.Plan.
func keysOf(secrets map[string]kit.SecretConfig) []string {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	return names
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

// doWork runs `at-cove work --project-dir <dir> --in <f> --out <f> [--timeout]
// [--grace] [--reap]`: a synchronous, one-shot run of the kit's dispatch
// command in a fresh ephemeral hardened VM (or, with --reap, just a scavenge of
// crashed dispatch orphans). It registers the --project-dir flag itself (rather
// than through the shared project-dir resolution in run(), which does not
// know about these flags), assembles the build context, reads the dispatched
// task's worker class from --in to resolve that class's worker secret bucket
// (Config.ResolvedWorker), and plans both the root (shared, all steps) and
// worker (agent-step only) secret sets — as create/chat do for the root set —
// then hands off to dispatchrun. With dryRun it prints the planned actions and
// returns before touching the backend, assembling, or resolving any secret —
// mirroring doInstall/doCreate's dry-run convention.
func doWork(args []string, r runner.Runner, dryRun bool, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("work", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pd := projectDirFlag(fs)
	inPath := fs.String("in", "", "path to the local task file to inject (e.g. task.json)")
	outPath := fs.String("out", "", "path to write the extracted result (e.g. task-result.json)")
	timeout := fs.Duration("timeout", 30*time.Minute, "hard wall-clock cap for the work")
	grace := fs.Duration("grace", 60*time.Minute, "age past which a labeled orphan is scavenged")
	reap := fs.Bool("reap", false, "scavenge dispatch orphans and exit")
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := resolveProjectDir(*pd, pos, "work", stderr)
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
		fmt.Fprintf(stdout, "would dispatch %s (kit %s, image %s): scavenge orphans, build image, run an ephemeral labeled container, inject %s, run the at-task worker bracket (prepare → agent → complete), extract %s, then destroy the container\n",
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

	// Read the dispatched task's worker class up front: the gate below and the
	// worker secret bucket both need it, and dispatchrun.Dispatch re-reads --in
	// itself later for the actual bracket — this earlier read only determines
	// which worker bucket to plan and gate on.
	taskBytes, err := os.ReadFile(*inPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	var task worker.Task
	if err := json.Unmarshal(taskBytes, &task); err != nil {
		fmt.Fprintf(stderr, "at-cove: parse task: %v\n", err)
		return 1
	}
	rw, err := cfg.ResolvedWorker(task.Worker.Class)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}

	// A github minter scopes its token to the kit's repo, passed to at-mint as the
	// non-secret --repo flag.
	repo := ""
	if cfg.SourceControl != nil && cfg.SourceControl.GitHub != nil {
		repo = cfg.SourceControl.GitHub.Project
	}
	expand := mint.Expander(r, store.Global, repo)

	// Two demand sets: root (shared, all steps) and the dispatched class's
	// worker bucket (agent-step only) — see internal/dispatchrun.Options.
	rootDemanded := keysOf(cfg.Secrets)
	rootSpecs, rootUnresolved, err := store.Plan(cfg.Name, kitPath, rootDemanded, expand)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	for _, name := range rootUnresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q has no supply for kit %q in %s (or secrets.local.yml); it will not be set\n", name, cfg.Name, secretsPath)
	}
	workerDemanded := keysOf(rw.Secrets)
	workerSpecs, workerUnresolved, err := store.Plan(cfg.Name, kitPath, workerDemanded, expand)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	for _, name := range workerUnresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q has no supply for kit %q in %s (or secrets.local.yml); it will not be set\n", name, cfg.Name, secretsPath)
	}

	// The dispatched agent authenticates to Anthropic under either well-known
	// bearer name; config validation accepts either as the worker-bucket bearer,
	// so the gate does too. A keyless worker is a guaranteed 401, so we fail
	// closed with attribution (like the git/tracker well-known secrets below)
	// rather than launch a doomed VM — but only when NEITHER name is declared
	// and resolved. Bearer-name knowledge is confined to this gate.
	// The bearer lives in the worker-class bucket (config validation rejects it
	// at root — see rejectRootBearers), so the gate checks rw.Secrets/
	// workerUnresolved rather than cfg.Secrets/rootUnresolved.
	agentBearerSecrets := []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}
	unresolvedSet := make(map[string]bool, len(workerUnresolved))
	for _, name := range workerUnresolved {
		unresolvedSet[name] = true
	}
	bearerResolved := false
	for _, name := range agentBearerSecrets {
		if _, declared := rw.Secrets[name]; declared && !unresolvedSet[name] {
			bearerResolved = true
			break
		}
	}
	if !bearerResolved {
		// Build a work-path logger from env only (mirroring g.LogMode/LogLevel's
		// env fallback): doWork has no cli.Globals in scope, and this is the
		// only site in the work path that needs a logger, so a full logger
		// threaded through doWork's signature would be more machinery than the
		// gate warrants. No log file here — this aborts before .state exists.
		lg, err := logging.New(logging.Options{
			Mode:   logModeFrom(envOr("", "AT_LOG_MODE")),
			Stderr: stderr,
			Level:  logLevelFrom(envOr("", "AT_LOG_LEVEL")),
		})
		if err != nil {
			fmt.Fprintf(stderr, "at-cove: %v\n", err)
			return 1
		}
		defer lg.Close()
		bearerNames := strings.Join(agentBearerSecrets, " or ")
		bearerErr := fmt.Errorf("no agent bearer (%s) is resolved for kit %q — the worker would fail closed with a 401; wire one under kits: %q in %s (or secrets.local.yml)",
			bearerNames, cfg.Name, cfg.Name, secretsPath)
		lg.UserError(context.Background(), bearerErr, slog.String("step", "secrets"), slog.String("secret", bearerNames), slog.String("kit", cfg.Name))
		return 1
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
		Base:          backend.BaseSpec{KitDir: kitDir, Base: cfg.Image.Base},
		Secrets:       rootSpecs,
		WorkerSecrets: workerSpecs,
		GitToken:      gitTok,
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

// doDispatch runs `at-cove dispatch [--project-dir <dir>]`: it loads the kit,
// resolves the tracker API token on the host, connects to the tracker, and runs
// the poll → dispatch → broker loop until SIGINT/SIGTERM. Each ready issue is
// dispatched as a fresh `at-cove work` run (see internal/dispatch/scheduler).
func doDispatch(args []string, g cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pd := projectDirFlag(fs)
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := resolveProjectDir(*pd, pos, "dispatch", stderr)
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
	logFile := ""
	if !g.NoLogFile {
		logFile = filepath.Join(state.Dir(kitDir), "logs", "at-cove-dispatch.jsonl")
	}
	lg, err := logging.New(logging.Options{
		Mode:     logModeFrom(envOr(g.LogMode, "AT_LOG_MODE")),
		Stderr:   stderr,
		FilePath: logFile,
		Level:    logLevelFrom(envOr(g.LogLevel, "AT_LOG_LEVEL")),
	})
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	defer lg.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx = logging.Into(ctx, lg)

	engine := scheduler.New(cfg, kitDir, tracker, dexec.New(), lg)
	lg.Info("scheduler started", slog.String("poll", cfg.Tracker.Linear.PollInterval))
	_ = engine.Run(ctx) // returns ctx.Err() on signal — a clean shutdown
	lg.Info("scheduler stopped")
	return 0
}
