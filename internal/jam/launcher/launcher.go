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
	"slices"
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
	backend.KitImageBuilder // BuildKitImage, HasKitImage (studio-kit prepare)
	backend.VolumeOps       // standing sessions' labeled state volumes
	GetStatus(container string) (backend.State, error)
}

// Config configures the Colima launcher.
type Config struct {
	Ops           Backend
	Runner        runner.Runner
	JamHost       string
	RuntimeAddr   string
	IdentityFile  string
	KnownHostsDir string
	DNS           []string
	Docker        bool
	Subscription  bool   // legacy-render fallback only: the supervisor always sets RaiseSpec.Connector, whose env carries the mode
	WorkDir       string // AT_COVE_WORKDIR; default /home/agent/workspace
	// JamID labels the state volumes this Jam creates (JamLabel) and scopes
	// its sweep to them. It must be stable across restarts and distinct per
	// Jam sharing a docker host; default RuntimeAddr (the address coves dial
	// this Jam at — two Jams can't share it).
	JamID string
	Log   *slog.Logger // nil → discard

	// PrepareKit inputs (studio-cove build path). BuildRoot is where per-kit
	// build contexts are assembled (default os.TempDir()/cove-kit-builds).
	// PublicKey is the launcher's own SSH public key, baked into the built image's
	// authorized_keys so Jam can reach the raised cove (its private half is
	// IdentityFile). The build context comes entirely from the KitDefinition (data)
	// and resources compiled into this binary — no source kit directory, so the
	// build stays a data-only transfer (remote-tolerant); the FROM-base is resolved
	// per kit on the substrate (ResolveKitBase, gate ON). Inventory is the
	// launcher's prepared-kit source of truth (nil → the substrate backend's image
	// inventory).
	BuildRoot string
	PublicKey []byte
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
	asm      string                 // assembly fingerprint, part of every image tag
}

var (
	_ jam.Launcher    = (*Launcher)(nil)
	_ jam.StateKeeper = (*Launcher)(nil)
)

func New(cfg Config) *Launcher {
	if cfg.sleep == nil {
		cfg.sleep = time.Sleep
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/home/agent/workspace"
	}
	if cfg.JamID == "" {
		cfg.JamID = cfg.RuntimeAddr
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	if cfg.BuildRoot == "" {
		cfg.BuildRoot = filepath.Join(os.TempDir(), "cove-kit-builds")
	}
	l := &Launcher{cfg: cfg, inflight: map[string]*sync.Mutex{}}
	asm, err := currentAsmDigest(cfg)
	if err != nil {
		cfg.Log.Warn("launcher: at-jam build identity unreadable; image tags key on the other inputs only", "error", err.Error())
	}
	l.asm = asm
	l.inv = cfg.Inventory
	if l.inv == nil {
		l.inv = backendInventory{ops: cfg.Ops, tag: l.imageTag}
	}
	if l.cfg.assemble == nil {
		l.cfg.assemble = l.defaultAssemble
	}
	return l
}

func (l *Launcher) Raise(ctx context.Context, spec jam.RaiseSpec, creds jam.LaunchCreds) (string, error) {
	name := naming.CoveContainer(spec.ActorID)
	// A studio raise always carries a kit: its image is cove-kit:<build-digest>-<asm>. A
	// launcher without that kit in its inventory returns ErrKitNotReady and creates
	// NO container, so the supervisor prepares the kit and retries.
	if spec.Kit.ID == "" {
		return "", fmt.Errorf("raise %s: no kit (studio raises require a kit)", name)
	}
	// The build-digest keys the image tag cove-kit:<Digest>; empty would yield an invalid tag.
	if spec.Kit.Digest == "" {
		return "", fmt.Errorf("raise %s: kit %s has no build-digest", name, spec.Kit.ID)
	}
	ok, err := l.inv.Has(spec.Kit)
	if err != nil {
		return "", fmt.Errorf("raise %s: inventory: %w", name, err)
	}
	if !ok {
		return "", fmt.Errorf("raise %s: %w", spec.Kit, ErrKitNotReady)
	}
	// Run the immutable cove-kit:<build-digest>-<asm> tag. No digest pin: the tag already
	// names the exact built image by its build-input digest.
	image, digest := l.imageTag(spec.Kit), ""
	mounts, err := l.stateMounts(spec, name)
	if err != nil {
		return "", fmt.Errorf("raise %s: %w", name, err)
	}
	if _, err := l.cfg.Ops.RunEphemeral(image, digest, name, Label, l.cfg.DNS, []string{l.cfg.JamHost}, l.cfg.Docker, mounts...); err != nil {
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
			// The connector (always set by the supervisor) carries the
			// Anthropic mode; Subscription only drives the legacy fallback render.
			Subscription: l.cfg.Subscription,
			Connector:    spec.Connector,
			Context:      spec.Context,
			SessionKind:  spec.SessionKind,
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

// StateLabel is the docker label on a standing session's state volumes; its
// value is the owning actor id. The standing reconciler sweeps by it: a
// labeled volume whose actor id is no longer declared is removed.
const StateLabel = "harbor.cove.state"

// JamLabel scopes a state volume to the Jam that created it (its value is
// Config.JamID), so a Jam sweeps only its own volumes, never another's on a
// shared docker host.
const JamLabel = "harbor.cove.jam"

// agentDataPath is where a standing session's agent-data volume mounts: the
// agent's CLAUDE_CONFIG_DIR (conversations, settings, logs).
const agentDataPath = "/agent-data"

// stateVolumes names a session's state volumes, given its container name: the
// at-cove create path's <container>-agent-data and <container>-workspace. The
// <container>-docker /var/lib/docker cache (which the backend mounts itself)
// is the studio's, not the session's: Teardown removes it.
func stateVolumes(name string) []string {
	return []string{naming.AgentDataVolume(name), naming.WorkspaceVolume(name)}
}

// stateMounts creates every state volume (labeled with the actor id and this
// Jam) and returns what a standing raise mounts to persist the session across
// restarts: its agent-data volume at /agent-data and its workspace volume at
// the agent's WorkDir. They outlive the --rm container and re-attach when the
// same actor id is raised again. Ephemeral and personal sessions get none:
// always fresh (COV-249).
func (l *Launcher) stateMounts(spec jam.RaiseSpec, name string) ([]backend.Mount, error) {
	if spec.SessionKind != jam.SessionKindStanding {
		return nil, nil
	}
	vols := stateVolumes(name)
	for _, v := range vols {
		// -v would auto-create the volume, but without the labels the sweep keys on.
		if err := l.cfg.Ops.CreateVolume(v, StateLabel+"="+spec.ActorID, JamLabel+"="+l.cfg.JamID); err != nil {
			return nil, fmt.Errorf("create state volume %s: %w", v, err)
		}
	}
	return []backend.Mount{{Volume: vols[0], Target: agentDataPath}, {Volume: vols[1], Target: l.cfg.WorkDir}}, nil
}

// PurgeState deletes actorID's state volumes (and any -docker cache, e.g. one
// labelled as state before the cache became the studio's) so its next raise
// starts fresh. A volume still in use by a container errors (the caller
// retries later); an absent one is fine.
func (l *Launcher) PurgeState(ctx context.Context, actorID string) error {
	name := naming.CoveContainer(actorID)
	if err := l.cfg.Ops.RemoveVolumes(append(stateVolumes(name), naming.DockerVolume(name))...); err != nil {
		return fmt.Errorf("purge %s state: %w", name, err)
	}
	l.cfg.Log.Info("cove state purged", "id", actorID, "container", name)
	return nil
}

// StateOwners lists the actor ids owning this Jam's StateLabel-ed volumes,
// sorted.
func (l *Launcher) StateOwners(ctx context.Context) ([]string, error) {
	vols, err := l.cfg.Ops.ListVolumes(StateLabel, JamLabel+"="+l.cfg.JamID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, id := range vols {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
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
	// Post-mortem insurance: before the container is removed (a standing
	// session's agent-data and workspace volumes survive it; only PurgeState
	// deletes them), grab the tail of cove-master's log and record it, so a cove
	// that died — crash, auth failure, egress-blocked, one-shot exit — leaves a
	// reason in Jam's log instead of vanishing silently. Strictly best-effort:
	// any failure here never blocks the teardown.
	if tail := l.captureAgentLog(inst.Location); tail != "" {
		l.cfg.Log.Warn("cove agent log (tail, captured on teardown)", "id", inst.ActorID, "log", tail)
	}
	if err := l.cfg.Ops.RemoveContainer(inst.Location); err != nil {
		return err
	}
	// The -docker cache is the studio's: it goes with the container, so the
	// session's next studio starts with a clean Docker store. Best-effort.
	if l.cfg.Docker {
		if err := l.cfg.Ops.RemoveVolumes(naming.DockerVolume(inst.Location)); err != nil {
			l.cfg.Log.Warn("teardown: removing the studio's docker cache failed", "id", inst.ActorID, "error", err.Error())
		}
	}
	return nil
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
