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
	"github.com/aethons-tools/cove/internal/naming"
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
				assembleOnly := fs.Bool("assemble-only", false, "assemble the .build context for inspection, then stop (no docker, no manifest)")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				if !noPositionals(pos, "install", errw) {
					return 2
				}
				kitDir, code := resolveProjectDir(*pd, errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doInstall(kitDir, r, *allowUnverified, *assembleOnly, g.DryRun, out), errw)
			}},
			{Name: "create", Brief: "build the image and start the sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("create", flag.ContinueOnError)
				fs.SetOutput(errw)
				pd := projectDirFlag(fs)
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				collaborator, kitDir, code := resolveCollaborator(*pd, pos, "create", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doCreate(collaborator, kitDir, r, g.DryRun, out), errw)
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
				collaborator, kitDir, code := resolveCollaborator(*pd, pos, "chat", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doChat(collaborator, kitDir, r, g.DryRun, *raw, *noAuth, *fresh, out, errw), errw)
			}},
			{Name: "recreate", Brief: "destroy and rebuild the sandbox, keeping saved state", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("recreate", flag.ContinueOnError)
				fs.SetOutput(errw)
				pd := projectDirFlag(fs)
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				collaborator, kitDir, code := resolveCollaborator(*pd, pos, "recreate", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doRecreate(collaborator, kitDir, r, g.DryRun, out), errw)
			}},
			{Name: "destroy", Brief: "destroy the sandbox and its volumes", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
				fs.SetOutput(errw)
				pd := projectDirFlag(fs)
				all := fs.Bool("all", false, "destroy every instance of the kit")
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				if *all {
					if len(pos) > 0 {
						fmt.Fprintln(errw, "at-cove: destroy --all takes no collaborator")
						return 2
					}
					kitDir, err := resolveKit(*pd)
					if err != nil {
						fmt.Fprintln(errw, "at-cove:", err)
						return 1
					}
					return exitCode("at-cove", doDestroyAll(kitDir, r, g.DryRun, out), errw)
				}
				collaborator, kitDir, code := resolveCollaborator(*pd, pos, "destroy", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doDestroy(collaborator, kitDir, r, g.DryRun, out), errw)
			}},
			{Name: "uninstall", Brief: "remove the compiled kit image + install.json (inverse of install)", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
				fs.SetOutput(errw)
				pd := projectDirFlag(fs)
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				if !noPositionals(pos, "uninstall", errw) {
					return 2
				}
				kitDir, code := resolveProjectDir(*pd, errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doUninstall(kitDir, r, g.DryRun, out), errw)
			}},
			{Name: "status", Brief: "print sandbox status", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				fs := flag.NewFlagSet("status", flag.ContinueOnError)
				fs.SetOutput(errw)
				pd := projectDirFlag(fs)
				pos, err := cli.ParseInterspersed(fs, args)
				if err != nil {
					return 2
				}
				// No positional lists every instance of the kit; an explicit
				// collaborator shows just that one (multi-VM ergonomics, COV-71).
				if len(pos) == 0 {
					kitDir, err := resolveKit(*pd)
					if err != nil {
						fmt.Fprintln(errw, "at-cove:", err)
						return 1
					}
					return exitCode("at-cove", doStatusAll(kitDir, r, g.DryRun, out), errw)
				}
				collaborator, kitDir, code := resolveCollaborator(*pd, pos, "status", errw)
				if code != 0 {
					return code
				}
				return exitCode("at-cove", doStatus(collaborator, kitDir, r, g.DryRun, out), errw)
			}},
			{Name: "work", Brief: "run one unit of work in a fresh ephemeral sandbox", Run: func(args []string, g cli.Globals, out, errw io.Writer) int {
				return doWork(args, r, g, out, errw)
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

// resolveProjectDir resolves the --project-dir flag value to a kit directory. The
// project root is a flag-only input: this reads the flag, never a positional.
// Callers that take no positional guard that separately with noPositionals.
func resolveProjectDir(flagVal string, stderr io.Writer) (string, int) {
	kitDir, err := resolveKit(flagVal)
	if err != nil {
		fmt.Fprintln(stderr, "at-cove:", err)
		return "", 1
	}
	return kitDir, 0
}

// noPositionals guards commands that take no positional (install/uninstall/work/
// dispatch): the project root comes only from --project-dir, so a stray positional
// is a usage error. It reports the error and returns false; the caller then exits
// 2, matching how the interactive verbs reject extra positionals (COV-73).
func noPositionals(pos []string, cmd string, stderr io.Writer) bool {
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "at-cove: %s takes no positional arguments (use --project-dir)\n", cmd)
		return false
	}
	return true
}

// resolveCollaborator resolves the --project-dir flag plus an optional single
// collaborator positional — the uniform argument shape of the five instance-aware
// verbs (create/chat/recreate/destroy/status), each keyed per collaborator class
// (COV-71). At most one positional is allowed (the collaborator class); the
// project root is always the --project-dir flag, never a positional.
func resolveCollaborator(flagVal string, pos []string, cmd string, stderr io.Writer) (collaborator, kitDir string, code int) {
	if len(pos) > 1 {
		fmt.Fprintf(stderr, "at-cove: %s takes at most one collaborator\n", cmd)
		return "", "", 2
	}
	if len(pos) == 1 {
		collaborator = pos[0]
	}
	kitDir, err := resolveKit(flagVal)
	if err != nil {
		fmt.Fprintln(stderr, "at-cove:", err)
		return "", "", 1
	}
	return collaborator, kitDir, 0
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
	return assemble.Assemble(kitDir, filepath.Join(kitDir, ".build"), pub, kit.RootDomains(cfg))
}

// doInstall compiles a kit into a runnable artifact (COV-38): assemble the .build
// context, build + gate + tag the hardened image via the backend, then freeze the
// resolved result into install.json. It is the single build+gate path and the only
// home of --allow-unverified-base. `--dry-run` is a pure preview — it assembles
// nothing and touches no docker, keys, or manifest; use `--assemble-only` to
// materialize the `.build` context for inspection (the old `build`'s
// "assemble + inspect" use) without building.
func doInstall(kitDir string, r runner.Runner, allowUnverifiedBase, assembleOnly, dryRun bool, stdout io.Writer) error {
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	buildDir := filepath.Join(kitDir, ".build")
	img := naming.Image(cfg.Name)
	if dryRun {
		// A --dry-run must have no resolver/key/disk side effects: describe the plan
		// from config.yml (source) and return before assembling anything. --dry-run
		// wins over --assemble-only.
		fmt.Fprintf(stdout, "would assemble %s, then build + gate + tag %s and write %s\n", buildDir, img, install.Path(kitDir))
		return nil
	}
	if err := assembleContext(kitDir, r); err != nil {
		return err
	}
	if assembleOnly {
		fmt.Fprintf(stdout, "assembled %s; run `at-cove install` to build + gate + tag %s and write %s\n", buildDir, img, install.Path(kitDir))
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
		ImageDigest:  installed.Digest,
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

// doUninstall is the inverse of doInstall (COV-64): it removes a kit's compiled
// artifacts — the image (backend RemoveImage) and install.json — returning the kit
// to "not installed" (a later create/chat then reports `run at-cove install`).
// It is the ONLY command that removes the image; the container lifecycle
// (destroy/recreate) deliberately keeps it (COV-63). It refuses while a created
// instance still exists (that instance holds the image), directing the user to
// `at-cove destroy` first. Image removal is best-effort: when install.json is
// present but the image is already gone (e.g. a machine left broken by the
// pre-COV-63 bug), it still deletes install.json. A not-installed kit is a
// friendly no-op. `--dry-run` reports the image + manifest it would remove and
// touches nothing.
func doUninstall(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	if !install.Exists(kitDir) {
		fmt.Fprintf(stdout, "%q is not installed; nothing to uninstall\n", cfg.Name)
		return nil
	}
	m, err := install.Load(kitDir)
	if err != nil {
		return err
	}
	// Any created instance — the plain one or any per-collaborator one (COV-71) —
	// holds the image, so uninstall must refuse while any state file remains, not
	// just the interactive one.
	if insts, err := state.List(kitDir); err != nil {
		return err
	} else if len(insts) > 0 {
		return fmt.Errorf("refusing to uninstall %q: %d instance(s) still created (they hold the image); run `at-cove destroy --all` first", m.Name, len(insts))
	}
	if dryRun {
		fmt.Fprintf(stdout, "would remove image %s and delete %s\n", m.Image, install.Path(kitDir))
		return nil
	}
	b, err := getBackend(defaultBackend, r)
	if err != nil {
		return err
	}
	// Best-effort image removal: the image may already be gone (a pre-COV-63
	// destroy could have deleted it), so a failed rmi must not block returning the
	// kit to "not installed" — warn and still delete the manifest.
	if err := b.RemoveImage(m.Image); err != nil {
		fmt.Fprintf(stdout, "warning: could not remove image %s (%v); deleting %s anyway\n", m.Image, err, install.Path(kitDir))
	}
	if err := install.Delete(kitDir); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "uninstalled %q: removed image %s and deleted %s\n", m.Name, m.Image, install.Path(kitDir))
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
// current against the live kit source (COV-38 §5/§6). Run commands
// (create/recreate/chat and work/dispatch) consume the pre-built image — they
// never build — so a missing or stale install is a hard error pointing the user
// at `at-cove install`. The currency check recomputes the hash from the live kit
// source + at-cove's embedded identity + the manifest's (frozen) base ref; the raw
// config.yml bytes are folded into KitSourceTree, so any edit to the kit source
// flips the hash even though config.yml is never re-interpreted here. Because the
// check hashes source (never re-assembling .build), concurrent work units under
// dispatch cannot race.
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

// instanceFor resolves a CLI collaborator positional against a kit config to the
// instance identity (COV-71): the state.Instance key (Interactive for a kit that
// defines no collaborators, else Instance(class)) and the backend name, which
// keys the container and its -workspace/-agent-data volumes (all via naming —
// COV-77). A plain (no-collaborator) kit uses container atcove-{kit} (volumes
// atcove-{kit}-workspace/-agent-data); a class-keyed instance uses container
// atcove-{kit}-{class} (volumes atcove-{kit}-{class}-workspace/-agent-data).
// Resolution mirrors chat exactly: explicit class → that one; omitted → the
// sole/default:true class (error if ambiguous); no collaborators → the plain
// Interactive instance.
func instanceFor(cfg kit.Config, collaborator string) (class string, hasCollab bool, instKey state.Instance, name string, err error) {
	class, hasCollab, err = cfg.SelectCollaborator(collaborator)
	if err != nil {
		return "", false, state.Interactive, "", err
	}
	if !hasCollab {
		return "", false, state.Interactive, naming.Container(cfg.Name, ""), nil
	}
	return class, true, state.Instance(class), naming.Container(cfg.Name, class), nil
}

// lenientRunConfig loads a kit's frozen run-config (its collaborators tree) from
// install.json WITHOUT the currency check the run commands enforce. The
// teardown/inspection verbs (destroy/status) resolve the collaborator positional
// through this — you can always tear down or inspect what you created, even after
// the kit source drifts and the install goes stale. A not-installed kit yields an
// empty config (no collaborators → the plain Interactive instance), so an orphan
// state.json is still addressable.
func lenientRunConfig(kitDir string) (kit.Config, error) {
	m, err := install.Load(kitDir)
	if errors.Is(err, install.ErrNotInstalled) {
		return kit.Config{}, nil
	}
	if err != nil {
		return kit.Config{}, err
	}
	return m.RunConfig, nil
}

// resolveInstanceLenient resolves a collaborator positional to a state.Instance
// key for destroy/status, via the lenient (non-currency) run-config.
func resolveInstanceLenient(kitDir, collaborator string) (state.Instance, error) {
	cfg, err := lenientRunConfig(kitDir)
	if err != nil {
		return state.Interactive, err
	}
	_, _, instKey, _, err := instanceFor(cfg, collaborator)
	return instKey, err
}

// doCreate consumes the installed image (COV-38): it verifies the install is
// current, resolves the collaborator to a per-class instance (COV-71), runs the
// pre-built image via the backend (no build), and records the running instance in
// the instance's state file — sourcing the image from the manifest. The workspace
// mount is decided by the selected collaborator's share-repo-dir (COV-72): true →
// a Shared bind-mount of the kit's repo dir (live .git), else Isolated (COV-25's
// clone-on-first-session populates it).
func doCreate(collaborator, kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		return err
	}
	cfg := m.RunConfig
	class, hasCollab, instKey, name, err := instanceFor(cfg, collaborator)
	if err != nil {
		return err
	}
	ws, err := sharedWorkspaceMount(cfg, kitDir, class, hasCollab)
	if err != nil {
		return err
	}
	return createInstance(kitDir, r, cfg, m.Image, m.ImageDigest, instKey, name, ws, dryRun, stdout)
}

// sharedWorkspaceMount resolves a fresh create's workspace mount from the selected
// collaborator's share-repo-dir (COV-72). Only the kit's repo dir — the parent of
// the .at-cove kit — is shareable, so a true flag yields a Shared bind-mount at
// that (absolute) path, sharing the live .git with the host; absent/false (and any
// no-collaborator kit) yields an Isolated volume. Arbitrary host paths are no
// longer mountable.
func sharedWorkspaceMount(cfg kit.Config, kitDir, class string, hasCollab bool) (backend.WorkspaceMount, error) {
	if !hasCollab {
		return backend.WorkspaceMount{Mode: backend.Isolated}, nil
	}
	role, err := cfg.ResolvedCollaborator(class)
	if err != nil {
		return backend.WorkspaceMount{}, err
	}
	if !role.ShareRepoDir {
		return backend.WorkspaceMount{Mode: backend.Isolated}, nil
	}
	abs, err := filepath.Abs(filepath.Dir(kitDir))
	if err != nil {
		return backend.WorkspaceMount{}, err
	}
	return backend.WorkspaceMount{Mode: backend.Shared, HostPath: abs}, nil
}

// createInstance runs the installed image for one resolved instance with the given
// workspace mount and records it in state. It is the shared tail of doCreate (mount
// from config) and doRecreate (mount recovered from state). The dry-run intent line
// reflects shared-repo vs isolated + the repo path (COV-72).
func createInstance(kitDir string, r runner.Runner, cfg kit.Config, image, digest string, instKey state.Instance, name string, ws backend.WorkspaceMount, dryRun bool, stdout io.Writer) error {
	if state.ExistsFor(kitDir, instKey) {
		return fmt.Errorf("%q is already created; run `at-cove recreate` or `at-cove destroy` first", name)
	}
	if dryRun {
		// The intent line names the human-readable tag; the actual run is pinned to
		// the manifest's built-image digest when present (COV-78).
		fmt.Fprintf(stdout, "would run %s (image %s, %s) and write %s\n", name, image, workspaceIntent(ws), state.PathFor(kitDir, instKey))
		return nil
	}
	b, err := getBackend(defaultBackend, r)
	if err != nil {
		return err
	}
	bi, err := b.Create(backend.CreateContext{
		Name: name, Image: image, Digest: digest, Workspace: ws,
	})
	if err != nil {
		return err
	}
	return saveState(kitDir, instKey, cfg, bi)
}

// workspaceIntent renders a mount as a human dry-run phrase: the shared repo dir
// and its host path, or an isolated workspace.
func workspaceIntent(ws backend.WorkspaceMount) string {
	if ws.Mode == backend.Shared {
		return "sharing repo dir " + ws.HostPath
	}
	return "isolated workspace"
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
		ImageDigest:   inst.ImageDigest,
		WorkspaceMode: "isolated",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if inst.Workspace.Mode == backend.Shared {
		st.WorkspaceMode = "shared"
		st.WorkspaceHostPath = inst.Workspace.HostPath
	}
	// Record the volume names the backend actually used, so destroy removes
	// exactly those rather than re-deriving them from Container (COV-76).
	st.Volumes = &state.Volumes{State: inst.Volumes.State, Workspace: inst.Volumes.Workspace}
	for name := range cfg.Secrets {
		st.Secrets = append(st.Secrets, state.Secret{Name: name})
	}
	return st
}

// saveState snapshots a created instance into its per-instance state file
// (state.json for Interactive, <class>.json otherwise — COV-71).
func saveState(kitDir string, instKey state.Instance, cfg kit.Config, inst backend.Instance) error {
	return state.SaveFor(kitDir, instKey, buildState(cfg, inst))
}

func instanceFromState(st state.State) backend.Instance {
	ws := backend.WorkspaceMount{Mode: backend.Isolated}
	if st.WorkspaceMode == "shared" {
		ws = backend.WorkspaceMount{Mode: backend.Shared, HostPath: st.WorkspaceHostPath}
	}
	inst := backend.Instance{Backend: st.Backend, Container: st.Container, Image: st.Image, ImageDigest: st.ImageDigest, Workspace: ws}
	// A legacy state file (schemaVersion 1) has no recorded volumes; leave
	// Instance.Volumes zero so Destroy falls back to the container-derived names
	// (COV-76).
	if st.Volumes != nil {
		inst.Volumes = backend.VolumeSet{State: st.Volumes.State, Workspace: st.Volumes.Workspace}
	}
	return inst
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
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		return err
	}
	cfg := m.RunConfig
	class, hasCollab, instKey, _, err := instanceFor(cfg, collaborator)
	if err != nil {
		return err
	}
	var role kit.Collaborator
	if hasCollab {
		if role, err = cfg.ResolvedCollaborator(class); err != nil {
			return err
		}
	}
	// The selected collaborator keys its own VM/state/volumes (COV-71): chat
	// operates on this instance's state file, not the single interactive one.
	st, err := state.LoadFor(kitDir, instKey)
	if err != nil {
		return err
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
	// Load the supply files up front (a read + parse, no host lookup) so a malformed
	// secrets.yml is caught even under --dry-run. The mint expansion that turns
	// demands into resolver specs — which CAN run host lookups — is deferred until
	// after the dry-run return below.
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		return err
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
		clone := ""
		if _, ok := cfg.GitTokenName(); ok && st.WorkspaceMode == "isolated" {
			if src, ok := cfg.SourceControl.Repo(); ok {
				clone = fmt.Sprintf(", cloning %s on first session start", src.Project)
			}
		}
		// The intent line reports the *demanded* count: the secret plan/mint
		// expansion below can run host lookups (a mint: supply), which a --dry-run
		// preview must never trigger, so it happens only after this return.
		fmt.Fprintf(stdout, "would resolve %d secrets and connect to %s as %s, launching %s%s\n",
			len(demanded), st.Container, who, launch, clone)
		return nil
	}

	kitPath := canonicalKitPath(kitDir)
	expand := mint.Expander(r, store.Global, "") // chat mints no github token into the session (connectors)
	specs, unresolved, err := store.Plan(st.Name, kitPath, demanded, expand)
	if err != nil {
		return err
	}
	for _, name := range unresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q is demanded but has no supply for kit %q in %s (or secrets.local.yml); it will not be set\n", name, st.Name, secretsPath)
	}

	// A Vertex kit's GCP ADC is resolved host-side, kept out of `specs` above (it
	// is a file connect seeds, never the agent's session env) — mirroring how the
	// workspace-clone git token is handled below. Deliberately after the dry-run
	// return: like workspaceClonePlan, this can execute a resolver command, which
	// must never happen as a side effect of a --dry-run preview.
	//
	// The provider (non-secret) env is set directly from the kit config regardless
	// of --no-auth — CLAUDE_CODE_USE_VERTEX etc. must still point the agent at
	// Vertex. But ADC *resolution* is skipped under --no-auth: the escape hatch
	// means "I manage auth myself", and connect itself will neither seed the file
	// nor set GOOGLE_APPLICATION_CREDENTIALS when SkipAuth is set, so resolving a
	// credential here would just be wasted (and, for a mint: supply, needless)
	// work.
	var vertexAuth *connect.VertexAuth
	var vertexEnv map[string]string
	if _, isVertex := cfg.Vertex(); isVertex {
		vertexEnv = cfg.VertexEnv()
		if !noAuth {
			if vertexAuth, _, err = vertexPlan(cfg, store, expand, st.Name, kitPath, secretsPath, r); err != nil {
				return err
			}
		}
	}

	// First-session workspace clone (isolated mode only). The git token is
	// resolved on the git-scoped expander (so a mint: supply gets the repo) and
	// kept out of `specs` above — it flows to connect separately and never into
	// the agent's session env (the code-host air-gap). Unconfigured source-control
	// yields nil (empty workspace, session still starts); a declared-but-unsupplied
	// token is a hard error before any SSH.
	repo := ""
	if src, ok := cfg.SourceControl.Repo(); ok {
		repo = src.Project
	}
	wsClone, err := workspaceClonePlan(cfg, st, store, mint.Expander(r, store.Global, repo), kitPath, secretsPath)
	if err != nil {
		return err
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}

	lock, err := state.AcquireSharedFor(kitDir, instKey)
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

	// Scope this interactive session's egress to the selected collaborator class
	// (COV-39 §5). The persistent container booted with only the baked sealed +
	// root lists; we add this class's <common> ∪ class delta on start — before
	// connect hands the session to the agent — and clear it on exit, so an idle
	// persistent container reverts to root-only rather than retaining a
	// collaborator's widened egress. The clear is deferred so it also runs on
	// error paths. A no-collaborator plain session applies an empty delta (root
	// only). Only one active class per persistent container at a time (a single
	// interactive session) — a documented constraint, not enforced (spec §5, §8).
	// Domains come from the currency-pinned install.json RunConfig (cfg), never a
	// live config.yml — "install froze it" (COV-38). The delivery op is privileged
	// (host docker exec as root) and lives in its own interface, so we type-assert
	// it off the Backend, exactly as the ephemeral path does off DispatchOps.
	eg, ok := b.(backend.SessionEgress)
	if !ok {
		return fmt.Errorf("backend does not support session egress (required to scope a collaborator's allow-list)")
	}
	var domains []string
	if hasCollab {
		if domains, err = cfg.ResolvedCollaboratorDomains(class); err != nil {
			return err
		}
	}
	if err := eg.ApplySessionEgress(st.Container, domains); err != nil {
		return fmt.Errorf("apply session egress: %w", err)
	}
	defer func() { _ = eg.ApplySessionEgress(st.Container, nil) }()

	// The session name is collaborator-aware (COV-75): "<kit> <collaborator> cove"
	// for a collaborator instance, "<kit> cove" for a plain one — so per-collaborator
	// instances of the same kit don't collide on tmux/session identity. It is derived
	// from cfg.Name + the resolved class, deliberately NOT from st.Name: State.Name
	// (the bare kit name) is the shared secret-bucket key and must not be re-keyed.
	return connect.Connect(b, r, connect.StdinScript{R: r, Cmd: cmd, Resume: resume, Name: cfg.Name, Collaborator: class}, awake.New(), connect.Options{
		Container:          st.Container,
		Secrets:            specs,
		IdentityFile:       priv,
		KnownHostsDir:      filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:           noAuth,
		Stderr:             stderr,
		CredentialsFile:    filepath.Join(configDir(), "credentials.json"),
		CollaboratorPrompt: role.Prompt,
		WorkspaceClone:     wsClone,
		ExtraEnv:           vertexEnv,
		Vertex:             vertexAuth,
	})
}

// workspaceClonePlan decides whether a chat session should clone the target repo
// into the isolated workspace on first start, and with what URL/branch/token. It
// mirrors the worker path's git plumbing (GitTokenName + planRequired) so the
// clone reuses the same demand/supply resolution rather than inventing new logic.
//
// It returns nil (skip, start empty — today's behavior) for a shared (share-repo-dir)
// workspace or when source-control/AT_TASK_GIT_TOKEN is not declared. When the
// token IS declared but has no supply on this machine, that is a hard error
// (fail closed): cloning a configured repo without its token cannot succeed.
// The returned Token spec is resolved in memory by connect only if it actually
// clones; it is never added to the agent's session env (the code-host air-gap).
func workspaceClonePlan(cfg kit.Config, st state.State, store usersecret.Store, expand usersecret.MintExpander, kitPath, secretsPath string) (*connect.WorkspaceClone, error) {
	if st.WorkspaceMode != "isolated" {
		return nil, nil // shared: the user brought their own checkout
	}
	gitName, ok := cfg.GitTokenName()
	if !ok {
		return nil, nil // no source-control/token declared: start empty
	}
	tok, err := planRequired(store, expand, st.Name, kitPath, gitName, secretsPath)
	if err != nil {
		return nil, err
	}
	repo, _ := cfg.SourceControl.Repo() // presence already checked by the guard above
	return &connect.WorkspaceClone{
		RepoURL: repo.CloneURL(),
		Branch:  repo.MainBranch, // defaulted to "main" at parse
		Token:   tok,
	}, nil
}

// gcpADCDemand is the well-known demand name a Vertex kit's GCP Application Default
// Credentials are supplied under (machine-side, in secrets.yml/secrets.local.yml).
// It is resolved host-side and seeded into the VM as a file — it never enters the
// agent's session env.
const gcpADCDemand = "GOOGLE_APPLICATION_CREDENTIALS_JSON"

// vertexPlan resolves a Vertex kit's GCP credential host-side (kept out of the
// session secret env) and returns it plus the non-secret provider env. Fails
// closed (via planRequired) when no supply is wired for the kit.
func vertexPlan(cfg kit.Config, store usersecret.Store, expand usersecret.MintExpander, kitName, kitPath, secretsPath string, r runner.Runner) (*connect.VertexAuth, map[string]string, error) {
	spec, err := planRequired(store, expand, kitName, kitPath, gcpADCDemand, secretsPath)
	if err != nil {
		return nil, nil, err
	}
	resolved, err := secret.Resolve(r, nil, []secret.Spec{spec})
	if err != nil {
		return nil, nil, err
	}
	adc := resolved[gcpADCDemand]
	if strings.TrimSpace(adc) == "" {
		return nil, nil, fmt.Errorf("vertex kit %q: resolved GCP credential %s is empty", kitName, gcpADCDemand)
	}
	return &connect.VertexAuth{ADC: []byte(adc)}, cfg.VertexEnv(), nil
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
		fmt.Fprintf(stdout, "would destroy %s (%s), keep image %s (an install artifact), and delete %s\n",
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

// doDestroy tears one resolved instance down for the user-facing `destroy
// [collaborator]` command: it purges that instance's volumes (keepVolumes=false).
// The collaborator is resolved through the lenient run-config so a stale/absent
// install never blocks a teardown (COV-71). recreate calls the keepVolumes=true
// path directly so the saved login survives.
func doDestroy(collaborator, kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	instKey, err := resolveInstanceLenient(kitDir, collaborator)
	if err != nil {
		return err
	}
	return doDestroyInstance(kitDir, r, instKey, false, dryRun, stdout)
}

// doDestroyAll tears down every created instance of the kit (`destroy --all`,
// COV-71): it enumerates the instance state files and destroys each, purging its
// volumes. Enumerating actual state files (not config) means it also cleans up an
// orphaned pre-upgrade instance.
func doDestroyAll(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	insts, err := state.List(kitDir)
	if err != nil {
		return err
	}
	if len(insts) == 0 {
		fmt.Fprintln(stdout, "no instances to destroy")
		return nil
	}
	for _, instKey := range insts {
		if err := doDestroyInstance(kitDir, r, instKey, false, dryRun, stdout); err != nil {
			return err
		}
	}
	return nil
}

// doRecreate tears down the existing instance (under the exclusive lock, so it
// refuses with active connections) and re-runs the installed image for the
// resolved collaborator, keeping volumes. It no longer rebuilds (COV-38): it
// verifies the install is current and re-runs the pre-built image, so a
// stale/missing install fails before any teardown, pointing at `at-cove install`.
func doRecreate(collaborator, kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		return err
	}
	cfg := m.RunConfig
	_, _, instKey, name, err := instanceFor(cfg, collaborator)
	if err != nil {
		return err
	}
	// Recreate keeps volumes, but a shared workspace is a host bind-mount, not a
	// volume — it must be re-specified at `docker run`. Recover the previously
	// recorded mount from this instance's state (never re-read config, COV-72) so
	// recreate preserves what create chose instead of silently reverting to an
	// isolated volume. This must happen before the destroy, which deletes the state.
	ws := backend.WorkspaceMount{Mode: backend.Isolated}
	if st, err := state.LoadFor(kitDir, instKey); err == nil && st.WorkspaceMode == "shared" {
		ws = backend.WorkspaceMount{Mode: backend.Shared, HostPath: st.WorkspaceHostPath}
	}
	if dryRun {
		fmt.Fprintf(stdout, "would destroy any existing %s (keeping volumes) then recreate\n", name)
		return nil
	}
	if state.ExistsFor(kitDir, instKey) {
		if err := doDestroyInstance(kitDir, r, instKey, true, false, stdout); err != nil {
			return err
		}
	}
	return createInstance(kitDir, r, cfg, m.Image, m.ImageDigest, instKey, name, ws, false, stdout)
}

// doStatus reports the status of one resolved instance (`status [collaborator]`).
// The collaborator is resolved through the lenient run-config (COV-71).
func doStatus(collaborator, kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	instKey, err := resolveInstanceLenient(kitDir, collaborator)
	if err != nil {
		return err
	}
	return doStatusInstance(kitDir, r, instKey, dryRun, stdout)
}

// doStatusAll lists every created instance of the kit (`status` with no
// positional, COV-71): class, running state, container, and workspace mode. An
// uncreated kit reports "absent (not created)", matching the single-instance case.
func doStatusAll(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	insts, err := state.List(kitDir)
	if err != nil {
		return err
	}
	if len(insts) == 0 {
		fmt.Fprintln(stdout, "absent (not created)")
		return nil
	}
	for _, instKey := range insts {
		if err := doStatusInstance(kitDir, r, instKey, dryRun, stdout); err != nil {
			return err
		}
	}
	return nil
}

// instanceClass labels an instance in status output: the class name, or
// "(interactive)" for the plain no-collaborator instance.
func instanceClass(inst state.Instance) string {
	if inst == state.Interactive {
		return "(interactive)"
	}
	return string(inst)
}

func doStatusInstance(kitDir string, r runner.Runner, inst state.Instance, dryRun bool, stdout io.Writer) error {
	st, err := state.LoadFor(kitDir, inst)
	if errors.Is(err, state.ErrNotCreated) {
		fmt.Fprintf(stdout, "%s: absent (not created)\n", instanceClass(inst))
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
	fmt.Fprintf(stdout, "%s: %s  (container %s, %s, image %s)\n",
		instanceClass(inst), labels[vmState], st.Container, st.WorkspaceMode, st.Image)
	return nil
}

// workName derives a container name unique to one dispatch run via the naming
// helper (COV-77) — the kit name for readability plus the pid and a nanosecond
// timestamp so concurrent dispatches of the same kit (even from separate
// processes) never collide. The impure pid/nanotime are supplied here so the
// naming helper stays pure.
func workName(kitName string) string {
	return naming.WorkerContainer(kitName, os.Getpid(), time.Now().UnixNano())
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

// effectiveWorkTimeout resolves the hard wall-clock cap for one `at-cove work`
// unit (COV-88): an explicit --timeout always wins; an unset flag adopts the
// resolved workers.<class>.timeout (the same value the dispatch scheduler passes),
// so a manual `work` matches `dispatch` for the same kit; failing that (no class
// timeout, or an unparseable one — though config validation forbids the latter)
// it keeps the flag's own default. classTimeout is a Go duration string.
func effectiveWorkTimeout(flagValue time.Duration, flagSet bool, classTimeout string) time.Duration {
	if flagSet || classTimeout == "" {
		return flagValue
	}
	if d, err := time.ParseDuration(classTimeout); err == nil {
		return d
	}
	return flagValue
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
func doWork(args []string, r runner.Runner, g cli.Globals, stdout, stderr io.Writer) int {
	dryRun := g.DryRun
	fs := flag.NewFlagSet("work", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pd := projectDirFlag(fs)
	inPath := fs.String("in", "", "path to the local task file to inject (e.g. task.json)")
	outPath := fs.String("out", "", "path to write the extracted result (e.g. task-result.json)")
	timeout := fs.Duration("timeout", 30*time.Minute, "hard wall-clock cap for the work (default: the resolved workers.<class>.timeout)")
	grace := fs.Duration("grace", 60*time.Minute, "age past which a labeled orphan is scavenged")
	reap := fs.Bool("reap", false, "scavenge dispatch orphans and exit")
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	// Whether --timeout was given explicitly: an unset flag defaults to the
	// resolved class timeout below (COV-88), an explicit one always wins.
	timeoutSet := false
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "timeout" {
			timeoutSet = true
		}
	})
	if !noPositionals(pos, "work", stderr) {
		return 2
	}
	kitDir, code := resolveProjectDir(*pd, stderr)
	if code != 0 {
		return code
	}
	if !*reap && (*inPath == "" || *outPath == "") {
		fmt.Fprintln(stderr, "at-cove work: --in and --out are required (unless --reap)")
		return 2
	}

	if dryRun {
		// Dry-run describes the plan from config.yml (source); it consumes nothing,
		// so it does not require an install.
		cfg, err := kit.Load(kitDir)
		if err != nil {
			fmt.Fprintf(stderr, "at-cove: %v\n", err)
			return 1
		}
		if *reap {
			fmt.Fprintf(stdout, "would scavenge %s dispatch orphans older than %s\n", cfg.Name, *grace)
			return 0
		}
		if len(cfg.Workers) == 0 {
			fmt.Fprintf(stderr, "at-cove: kit %q declares no workers\n", cfg.Name)
			return 1
		}
		img := naming.Image(cfg.Name)
		fmt.Fprintf(stdout, "would dispatch %s (kit %s, image %s): verify the install is current, scavenge orphans, run an ephemeral labeled container from the installed image, inject %s, run the at-task worker bracket (prepare → agent → complete), extract %s, then destroy the container\n",
			cfg.Name, kitDir, img, *inPath, *outPath)
		return 0
	}

	// Read the dispatched task up front (--reap carries none): it names the per-run
	// log file and selects the worker secret bucket. dispatchrun.Dispatch re-reads
	// --in itself later for the actual bracket — this earlier read only drives the
	// logger's correlation and which worker bucket to plan and gate on.
	var task worker.Task
	if !*reap {
		taskBytes, err := os.ReadFile(*inPath)
		if err != nil {
			fmt.Fprintf(stderr, "at-cove: %v\n", err)
			return 1
		}
		if err := json.Unmarshal(taskBytes, &task); err != nil {
			fmt.Fprintf(stderr, "at-cove: parse task: %v\n", err)
			return 1
		}
	}

	// Build the work-run logger up front and seed it with the run/issue/class
	// correlation (spec §7/§8) so every diagnostic below — backend, install
	// currency, secret resolution, the fail-closed bearer gate, dispatch — is a
	// structured, correlated record rather than a bare Fprintf. In attended mode
	// the full trace goes to a per-run JSON file under the kit's excluded runtime
	// dir; in unattended mode (the normal way a dispatched worker runs) it is JSON
	// to stderr for the platform to capture. dispatchrun grafts `step` per record
	// and merges the VM's captured structured output into this one stream. run
	// flows from the scheduler via COVE_RUN_ID. --reap carries no task/run, so it
	// writes no per-run file.
	logFile := ""
	if !g.NoLogFile && !*reap {
		// Sanitize the issue key before it names a host file — it originates in the
		// task input, so keep it to a safe slug (no path separators / traversal).
		slug := strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
				return r
			default:
				return '_'
			}
		}, task.Issue.Key)
		logFile = filepath.Join(state.Dir(kitDir), "logs", "at-cove-work-"+slug+".jsonl")
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
	if !*reap {
		lg = lg.With(
			slog.String("run", envOr("", "COVE_RUN_ID")),
			slog.String("issue", task.Issue.Key),
			slog.String("class", task.Worker.Class),
		)
	}
	ctx := logging.Into(context.Background(), lg)

	b, err := getBackend(defaultBackend, r)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "setup"))
		return 1
	}
	ops, ok := b.(backend.DispatchOps)
	if !ok {
		lg.UserError(ctx, fmt.Errorf("backend %q does not support dispatch", defaultBackend), slog.String("step", "setup"))
		return 1
	}

	if *reap {
		if err := dispatchrun.Reap(ops, *grace, time.Now()); err != nil {
			lg.UserError(ctx, fmt.Errorf("reap: %w", err), slog.String("step", "broker"))
			return 1
		}
		return 0
	}

	// Consume the pre-built installed image (COV-38): verify the install is current
	// and fail fast (`run at-cove install`) if it is missing or stale — work never
	// builds. The run-config comes from the manifest, not config.yml.
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "setup"))
		return 1
	}
	cfg := m.RunConfig

	if len(cfg.Workers) == 0 {
		lg.UserError(ctx, fmt.Errorf("kit %q declares no workers", cfg.Name), slog.String("step", "setup"))
		return 1
	}

	// Only the ssh identity key is needed on the run path: the matching public key
	// was baked into the installed image at install time, so work never assembles.
	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "setup"))
		return 1
	}

	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "secrets"))
		return 1
	}
	kitPath := canonicalKitPath(kitDir)

	rw, err := cfg.ResolvedWorker(task.Worker.Class)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "secrets"))
		return 1
	}

	// COV-88: an unset --timeout adopts the resolved workers.<class>.timeout — the
	// same cap the dispatch scheduler passes — so a manual `at-cove work` honors the
	// kit's declared timeout instead of silently applying the 30m flag default.
	effectiveTimeout := effectiveWorkTimeout(*timeout, timeoutSet, rw.Timeout)

	// A github minter scopes its token to the kit's repo, passed to at-mint as the
	// non-secret --repo flag.
	repo := ""
	if src, ok := cfg.SourceControl.Repo(); ok {
		repo = src.Project
	}
	expand := mint.Expander(r, store.Global, repo)

	// Two demand sets: root (shared, all steps) and the dispatched class's
	// worker bucket (agent-step only) — see internal/dispatchrun.Options.
	rootDemanded := keysOf(cfg.Secrets)
	rootSpecs, rootUnresolved, err := store.Plan(cfg.Name, kitPath, rootDemanded, expand)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "secrets"))
		return 1
	}
	for _, name := range rootUnresolved {
		lg.Warn("secret has no supply; it will not be set", slog.String("step", "secrets"), slog.String("secret", name), slog.String("kit", cfg.Name))
	}
	workerDemanded := keysOf(rw.Secrets)
	workerSpecs, workerUnresolved, err := store.Plan(cfg.Name, kitPath, workerDemanded, expand)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "secrets"))
		return 1
	}
	for _, name := range workerUnresolved {
		lg.Warn("secret has no supply; it will not be set", slog.String("step", "secrets"), slog.String("secret", name), slog.String("kit", cfg.Name))
	}

	// The dispatched agent authenticates to Anthropic under either well-known
	// bearer name; config validation accepts either as the worker-bucket bearer,
	// so the gate does too. A keyless worker is a guaranteed 401, so we fail
	// closed with attribution (like the git/tracker well-known secrets below)
	// rather than launch a doomed VM — but only when NEITHER name is declared
	// and resolved. Bearer-name knowledge is confined to this gate.
	// The bearer lives in the worker-class bucket (config validation rejects it
	// in every non-worker bucket — see validateSecretNames), so the gate checks rw.Secrets/
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
		bearerNames := strings.Join(agentBearerSecrets, " or ")
		bearerErr := fmt.Errorf("no agent bearer (%s) is resolved for kit %q — the worker would fail closed with a 401; wire one under kits: %q in %s (or secrets.local.yml)",
			bearerNames, cfg.Name, cfg.Name, secretsPath)
		lg.UserError(ctx, bearerErr, slog.String("step", "secrets"), slog.String("secret", bearerNames), slog.String("kit", cfg.Name))
		return 1
	}

	// The code-host token stays a distinct demand (the air-gap); required, fail closed.
	gitName, ok := cfg.GitTokenName()
	if !ok {
		provider := "source-control"
		if cfg.SourceControl != nil {
			if name, aerr := cfg.SourceControl.Active(); aerr == nil {
				provider = "source-control." + name
			}
		}
		lg.UserError(ctx, fmt.Errorf("kit %q declares no %s.secrets AT_TASK_GIT_TOKEN", cfg.Name, provider), slog.String("step", "secrets"))
		return 1
	}
	gitTok, err := planRequired(store, expand, cfg.Name, kitPath, gitName, secretsPath)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "secrets"))
		return 1
	}

	err = dispatchrun.Dispatch(ctx, dispatchrun.Options{
		Ops: ops, R: r, Cfg: cfg, Image: m.Image, ImageDigest: m.ImageDigest, Name: workName(cfg.Name),
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
		Timeout: effectiveTimeout, GraceWindow: *grace, Now: time.Now(),
	})
	if err != nil {
		lg.UserError(ctx, fmt.Errorf("work: %w", err), slog.String("step", "dispatch"))
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
	if !noPositionals(pos, "dispatch", stderr) {
		return 2
	}
	kitDir, code := resolveProjectDir(*pd, stderr)
	if code != 0 {
		return code
	}

	if g.DryRun {
		// Dry-run previews the plan from config.yml (source) and returns before any
		// logger/file, secret resolution, or tracker connect — a --dry-run must have
		// no resolver/tracker/disk side effects, and (like doWork) it consumes
		// nothing, so it needs no install.
		cfg, err := kit.Load(kitDir)
		if err != nil {
			fmt.Fprintf(stderr, "at-cove: %v\n", err)
			return 1
		}
		if cfg.SourceControl == nil || cfg.Tracker == nil || cfg.Tracker.Linear == nil || cfg.Dispatch == nil || len(cfg.Workers) == 0 {
			fmt.Fprintln(stderr, "at-cove: kit must declare source-control, tracker.linear, dispatch, and at least one worker")
			return 1
		}
		classes := make([]string, 0, len(cfg.Workers))
		for name := range cfg.Workers {
			if _, err := cfg.ResolvedWorker(name); err == nil {
				classes = append(classes, name)
			}
		}
		sort.Strings(classes)
		fmt.Fprintf(stdout, "would dispatch %s (kit %s): resolve the tracker token, connect to Linear, and poll every %s for %d worker class(es): %s\n",
			cfg.Name, kitDir, cfg.Tracker.Linear.PollInterval, len(classes), strings.Join(classes, ", "))
		return 0
	}

	// Build the dispatch logger up front (spec §7/§8) so every startup diagnostic
	// — install/currency, the scheduler-surface check, secret resolution, tracker
	// connect — is a structured, correlated record rather than a bare Fprintf.
	// Attended mode writes human text to stderr plus the full JSON trace to a
	// fixed per-command file under the kit's excluded runtime dir; unattended mode
	// emits JSON to stderr for the platform to capture. The signal context is
	// established here too, so Ctrl-C is honored even during startup.
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

	// dispatch reads its tracker/source-control/dispatch/workers run-config from
	// install.json (COV-38), and fails fast if the install is missing or stale
	// (`run at-cove install`). Its dispatched `work` units then consume the one
	// warm installed image — no per-unit build, no per-unit gate.
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "setup"))
		return 1
	}
	cfg := m.RunConfig
	// dispatch requires the full scheduler surface.
	if cfg.SourceControl == nil || cfg.Tracker == nil || cfg.Tracker.Linear == nil || cfg.Dispatch == nil || len(cfg.Workers) == 0 {
		lg.UserError(ctx, errors.New("kit must declare source-control, tracker.linear, dispatch, and at least one worker"), slog.String("step", "setup"))
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
		lg.UserError(ctx, errors.New("kit declares no dispatchable worker class (only <common>?)"), slog.String("step", "setup"))
		return 1
	}
	fmt.Fprintf(stdout, "at-cove dispatch: kit OK — %d worker class(es): %s\n", len(classes), strings.Join(classes, ", "))

	secretsPath := filepath.Join(configDir(), "secrets.yml")
	localPath := filepath.Join(configDir(), "secrets.local.yml")
	store, err := usersecret.Load(secretsPath, localPath)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "secrets"))
		return 1
	}
	kitPath := canonicalKitPath(kitDir)
	expand := mint.Expander(runner.OS{}, store.Global, "") // dispatch resolves the tracker token, not a github token
	planned, err := planRequired(store, expand, cfg.Name, kitPath, "AT_DISPATCH_TRACKER_TOKEN", secretsPath)
	if err != nil {
		lg.UserError(ctx, err, slog.String("step", "secrets"))
		return 1
	}
	resolved, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{planned})
	if err != nil {
		// The error names a failed resolver command, never the token value.
		lg.UserError(ctx, fmt.Errorf("resolve tracker token: %w", err), slog.String("step", "secrets"))
		return 1
	}
	token := resolved["AT_DISPATCH_TRACKER_TOKEN"]

	tracker, err := linear.New(cfg, token, nil)
	if err != nil {
		lg.UserError(ctx, fmt.Errorf("connect to Linear: %w", err), slog.String("step", "broker"))
		return 1
	}

	engine := scheduler.New(cfg, kitDir, tracker, dexec.New(), lg)
	lg.Info(schedulerStartMsg, slog.String("poll", cfg.Tracker.Linear.PollInterval))
	_ = engine.Run(ctx) // returns ctx.Err() on signal — a clean shutdown
	lg.Info("scheduler stopped")
	return 0
}

// schedulerStartMsg is the dispatch scheduler's start line. It keeps the
// "Ctrl-C to stop" affordance for the attended operator (the poll interval
// rides as a structured attr, not in the message text).
const schedulerStartMsg = "scheduler started; Ctrl-C to stop"
