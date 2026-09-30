// Package launcher is the real jam.Launcher: it raises/tears down/probes a
// managed cove on a Colima backend and bootstraps cove-master over SSH. It lives
// outside internal/jam core (which stays backend/connect-free) and is wired
// from cmd/at-jam.
package launcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/connect"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/naming"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// Label is the docker label on every container the launcher raises, and the
// filter it lists them by. It keeps its pre-rename "harbor" value: changing it
// would hide studios an older binary raised from the new one during a rollout.
// See docs/usage/jam/renamed-from-harbor.md.
const Label = "harbor.cove"

// Backend is the backend surface the launcher needs: the ephemeral run/dial/remove
// ops, GetStatus for probing, and the managed-kit build+inventory. The Colima
// backend value satisfies all three, so its build/inventory and its RunEphemeral
// share one docker daemon (COV-217).
type Backend interface {
	backend.DispatchOps     // RunEphemeral, Dial, RemoveContainer, ScavengeLabeled
	backend.KitImageBuilder // BuildKitImage, HasKitImage (managed-kit prepare)
	GetStatus(container string) (backend.State, error)
}

// Config configures the Colima launcher.
type Config struct {
	Ops           Backend
	Runner        runner.Runner
	Image         string
	ImageDigest   string
	JamHost       string
	RuntimeAddr   string
	IdentityFile  string
	KnownHostsDir string
	DNS           []string
	Docker        bool
	Subscription  bool         // seed raised coves in subscription mode (dummy claudeAiOauth, no ANTHROPIC_API_KEY)
	WorkDir       string       // AT_COVE_WORKDIR; default /home/agent/workspace
	Log           *slog.Logger // nil → discard

	// PrepareKit inputs (managed-cove build path). BuildRoot is where per-kit
	// build contexts are assembled (default os.TempDir()/cove-kit-builds).
	// PublicKey is the launcher's own SSH public key, baked into the built image's
	// authorized_keys so Jam can reach the raised cove (its private half is
	// IdentityFile). BaseImage is the resolved, already-gated FROM-base the managed
	// image builds on (the install manifest's BaseRef) — passed to the backend as
	// the Dockerfile's BASE arg. The build context otherwise comes entirely from
	// the KitDefinition (data) and resources compiled into this binary — no source
	// kit directory, so the build stays a data-only transfer (remote-tolerant).
	// Inventory is the launcher's prepared-kit source of truth (nil → the
	// substrate backend's image inventory).
	BuildRoot string
	PublicKey []byte
	BaseImage string
	Inventory Inventory

	sleep func(time.Duration) // wait-for-sshd backoff; nil → time.Sleep
	// assemble stages a KitDefinition's build context into buildDir. nil → the
	// real assemble.AssembleContext from the definition + PublicKey; a seam so
	// PrepareKit tests stay hermetic.
	assemble func(def KitDefinition, buildDir string) error
}

type Launcher struct {
	cfg      Config
	inv      Inventory
	mu       sync.Mutex             // guards inflight
	inflight map[string]*sync.Mutex // per-ref build locks (de-dupe concurrent PrepareKit)
}

var _ jam.Launcher = (*Launcher)(nil)

func New(cfg Config) *Launcher {
	if cfg.sleep == nil {
		cfg.sleep = time.Sleep
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/home/agent/workspace"
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	if cfg.BuildRoot == "" {
		cfg.BuildRoot = filepath.Join(os.TempDir(), "cove-kit-builds")
	}
	l := &Launcher{cfg: cfg, inflight: map[string]*sync.Mutex{}}
	l.inv = cfg.Inventory
	if l.inv == nil {
		l.inv = backendInventory{cfg.Ops}
	}
	if l.cfg.assemble == nil {
		l.cfg.assemble = l.defaultAssemble
	}
	return l
}

func (l *Launcher) Raise(ctx context.Context, spec jam.RaiseSpec, creds jam.LaunchCreds) (string, error) {
	name := naming.CoveContainer(spec.ActorID)
	// A kit-referenced raise runs the prepared cove-kit:<id>-v<version> image; a
	// launcher without that kit returns ErrKitNotReady and creates NO container,
	// so the supervisor prepares the kit and retries. A zero KitRef (empty ID)
	// keeps the legacy static-image path so existing callers are unaffected.
	image, digest := l.cfg.Image, l.cfg.ImageDigest
	if spec.Kit.ID != "" {
		ok, err := l.inv.Has(spec.Kit)
		if err != nil {
			return "", fmt.Errorf("raise %s: inventory: %w", name, err)
		}
		if !ok {
			return "", fmt.Errorf("raise %s: %w", spec.Kit, ErrKitNotReady)
		}
		// Run the immutable cove-kit:<id>-v<n> tag. No digest pin: KitRef.Digest is
		// the kit CONFIG's content hash (integrity of the definition), not the built
		// image's id, so it must never be passed as RunEphemeral's image digest.
		image, digest = imageTag(spec.Kit), ""
	}
	if _, err := l.cfg.Ops.RunEphemeral(image, digest, name, Label, l.cfg.DNS, []string{l.cfg.JamHost}, l.cfg.Docker); err != nil {
		return "", fmt.Errorf("raise %s: run: %w", name, err)
	}
	// From here, clean up the container on any failure so a failed raise leaks nothing.
	launch := func() error {
		ep, cleanup, err := l.cfg.Ops.Dial(name)
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}
		defer cleanup()
		tgt := sshargs.Target{
			Host: ep.Host, User: ep.User, Port: ep.Port,
			IdentityFile:   l.cfg.IdentityFile,
			KnownHostsFile: filepath.Join(l.cfg.KnownHostsDir, name),
		}
		if err := l.waitForSSH(tgt); err != nil {
			return fmt.Errorf("wait for sshd: %w", err)
		}
		// The role's egress policy lands before cove-master (and so the agent)
		// starts, so the agent never runs under the wider kit default.
		if err := l.applyRoleEgress(name, spec); err != nil {
			return err
		}
		return connect.LaunchCoveMaster(l.cfg.Runner, connect.CoveMasterOptions{
			Target: tgt, JamHost: l.cfg.JamHost, RuntimeAddr: l.cfg.RuntimeAddr,
			IdentityToken: creds.IdentityToken, LaunchSecret: creds.LaunchSecret,
			WorkDir: l.cfg.WorkDir, Prompt: spec.Prompt,
			// A personal session is a long-lived conversation: its agent stays
			// resident, waiting for its owner's reply after every turn.
			Resident: jam.IsResident(spec.SessionKind),
			// Subscription mode seeds a dummy claudeAiOauth credential so the
			// cove's claude authenticates as a pooled subscription principal.
			Subscription: l.cfg.Subscription,
		})
	}
	if err := launch(); err != nil {
		if rmErr := l.cfg.Ops.RemoveContainer(name); rmErr != nil {
			l.cfg.Log.Warn("raise cleanup: remove container failed", "name", name, "error", rmErr)
		}
		return "", fmt.Errorf("raise %s: %w", name, err)
	}
	return name, nil
}

// applyRoleEgress pushes spec's role egress policy into the raised container. A
// nil policy is the kit default, which the image already boots with: nothing to
// apply at raise. Fail closed: a policy the backend can't apply fails the raise
// rather than falling back to the wider kit default.
func (l *Launcher) applyRoleEgress(name string, spec jam.RaiseSpec) error {
	if spec.Egress == nil {
		return nil
	}
	return l.applyEgress(name, spec.ActorID, spec.Project, spec.Role, spec.Egress)
}

// ApplyEgress sets a running cove's egress to p (nil = the kit default), via the
// same privileged backend op the raise uses; the sealed in-box helper enforces
// the kit's ceiling. Jam's supervisor calls it when the role's policy drifts
// from the one the cove is running under.
func (l *Launcher) ApplyEgress(ctx context.Context, inst jam.Instance, p *jam.EgressPolicy) error {
	return l.applyEgress(inst.Location, inst.ActorID, inst.Project, inst.Role, p)
}

// applyEgress applies p (nil = reset to the kit default) to container via the
// backend's RoleEgress op. A backend without the op errors — never a silent no-op.
func (l *Launcher) applyEgress(container, actorID, project, role string, p *jam.EgressPolicy) error {
	if project == "" {
		project = jam.DefaultProject
	}
	re, ok := l.cfg.Ops.(backend.RoleEgress)
	if !ok {
		return fmt.Errorf("backend does not support role egress (required for role %s/%s)", project, role)
	}
	if p == nil {
		if err := re.ResetRoleEgress(container); err != nil {
			return fmt.Errorf("reset role egress: %w", err)
		}
		l.cfg.Log.Info("role egress reset to kit default", "id", actorID, "project", project, "role", role)
		return nil
	}
	if err := re.ApplyRoleEgress(container, p.Domains); err != nil {
		return fmt.Errorf("apply role egress: %w", err)
	}
	l.cfg.Log.Info("role egress applied", "id", actorID, "project", project, "role", role, "domains", len(p.Domains))
	return nil
}

func (l *Launcher) Teardown(ctx context.Context, inst jam.Instance) error {
	// An idled cove is docker-paused; SSH into a frozen container hangs (so the
	// log capture below blocks and the whole teardown stalls). Unpause first —
	// best-effort, and Pause/Unpause are idempotent so this is a harmless no-op
	// on a running cove — then the post-mortem capture works and removal is never
	// blocked on a paused container.
	if err := l.cfg.Ops.Unpause(inst.Location); err != nil {
		l.cfg.Log.Warn("teardown: unpause before capture failed (continuing)", "id", inst.ActorID, "error", err.Error())
	}
	// Post-mortem insurance: before the container (and its /agent-data volume)
	// is removed, grab the tail of cove-master's log and record it, so a cove
	// that died — crash, auth failure, egress-blocked, one-shot exit — leaves a
	// reason in Jam's log instead of vanishing silently. Strictly best-effort:
	// any failure here never blocks the teardown.
	if tail := l.captureAgentLog(inst.Location); tail != "" {
		l.cfg.Log.Warn("cove agent log (tail, captured on teardown)", "id", inst.ActorID, "log", tail)
	}
	return l.cfg.Ops.RemoveContainer(inst.Location)
}

// captureAgentLog returns the last few KB of cove-master's log from the cove, or
// "" if it can't be read (container gone, no ssh, no log yet). Best-effort.
func (l *Launcher) captureAgentLog(container string) string {
	ep, cleanup, err := l.cfg.Ops.Dial(container)
	if err != nil {
		return ""
	}
	defer cleanup()
	tgt := sshargs.Target{
		Host: ep.Host, User: ep.User, Port: ep.Port,
		IdentityFile:   l.cfg.IdentityFile,
		KnownHostsFile: filepath.Join(l.cfg.KnownHostsDir, container),
	}
	out, err := l.cfg.Runner.Output("ssh", append(sshargs.Base(tgt), "tail -c 4096 "+connect.CoveMasterLogVMPath+" 2>/dev/null")...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (l *Launcher) Probe(ctx context.Context, inst jam.Instance) (jam.Liveness, error) {
	st, err := l.cfg.Ops.GetStatus(inst.Location)
	if err != nil {
		return jam.LivenessUnknown, nil // transient — don't reap (COV-151)
	}
	switch st {
	case backend.StateRunning:
		return jam.LivenessAlive, nil
	default:
		return jam.LivenessDead, nil
	}
}

func (l *Launcher) Pause(ctx context.Context, inst jam.Instance) error {
	return l.cfg.Ops.Pause(inst.Location)
}

func (l *Launcher) Unpause(ctx context.Context, inst jam.Instance) error {
	return l.cfg.Ops.Unpause(inst.Location)
}

// waitForSSH polls sshd with a trivial command until it answers or attempts run out.
func (l *Launcher) waitForSSH(tgt sshargs.Target) error {
	const attempts = 30
	probe := append(sshargs.Base(tgt), "true")
	var err error
	for i := 0; i < attempts; i++ {
		if err = l.cfg.Runner.Run("ssh", probe...); err == nil {
			return nil
		}
		if i < attempts-1 {
			l.cfg.sleep(time.Second)
		}
	}
	return err
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
