package jam

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// Liveness is a Launcher.Probe result.
type Liveness int

const (
	LivenessUnknown Liveness = iota
	LivenessAlive
	LivenessDead
)

// RaiseSpec is the request to raise a managed cove. Scope/kit resolution lives in
// the role (and, in a later slice, the Launcher); this carries only identity.
type RaiseSpec struct {
	ActorID string
	Project string
	Role    string
	Unit    string
	// Mission is the session's initial mission, its end goal (normalised on Raise); "" = none.
	Mission string
	Prompt  string // workload prompt for the raised cove's agent; consumed by the launcher, not persisted
	// Owner is the owning user's name for a personal session; "" otherwise.
	Owner string
	// OwnerID is the owning user for a personal session.
	OwnerID ident.ID
	// Name is a standing session's declared name; "" otherwise.
	Name string
	// SessionKind is "ephemeral" | "standing" | "personal"; "" = ephemeral. A
	// plain string (not allocator.SessionKind) so Jam never imports allocator.
	SessionKind string
	// Egress is the role's egress policy, which the launcher applies in-box before
	// the agent starts; nil = the kit's default list. Supervisor.Raise always
	// fills it from the role, overriding any caller-set value.
	Egress *EgressPolicy
	// Kit is the kit to raise the cove from (its light reference). The launcher
	// consults its prepared-kit inventory: a miss returns ErrKitNotReady (the
	// supervisor then prepares the full definition and retries), a hit raises the
	// jam-kit:<build-digest>-<asm> image. A zero KitRef (empty ID) selects the legacy
	// path that raises the launcher's statically configured image, so existing
	// callers are unaffected (Phase-1 additive).
	Kit KitRef
	// Connector is the client env/git the raised cove sets up to use its role's
	// destinations (ConnectorFor). Supervisor.Raise always fills it; nil (a
	// caller bypassing the supervisor) = the legacy built-in contract.
	Connector *snippet.Connector
	// Context is the compiled session context (sessionctx.Compile): Jam
	// boilerplate for the session kind, then the kit layer. Supervisor.Raise
	// always sets it; the launcher stages it for jam-studio-agent, which delivers
	// its core as an appended system prompt every turn.
	Context *sessionctx.Bundle
	// DraftImage, set by Supervisor.Raise for a draft studio
	// (docs/usage/jam/kits-drafts.md#draft-studio), is the test build's image
	// the launcher runs (by id) instead of the kit inventory's. nil for every
	// other raise.
	DraftImage *KitDraftImage
	// KitDraft names a kit whose draft the studio runs instead of the role's
	// kit: an admin manual raise only (docs/usage/jam/kits-drafts.md#draft-studio).
	// No other caller sets it.
	KitDraft string
	// draft is the draft tree a draft studio runs (Raise and a context
	// refresh set it), for the kit fact; nil for every other raise.
	draft *KitDraftRef
	// turnProtocol is the turn end the cove speaks (a context refresh sets
	// it from GET /context?turn-protocol=); 0 = legacy, as at every raise.
	turnProtocol int
	// Placement is where a raise may land: managers in preference order, and how long to look.
	Placement Placement
}

// LaunchCreds carries the per-instance credentials the supervisor mints and the
// launcher must inject into the cove (identity token + launch secret). Passed to
// Raise so the launcher can bootstrap jam-studio-agent without the supervisor leaking
// them elsewhere.
type LaunchCreds struct {
	IdentityToken string
	LaunchSecret  string
}

// Launcher is the seam over "actually start/stop/probe a cove on a backend". The
// supervisor depends only on this, so it stays kit-free and grpc-free and fully
// hermetic. The real backend+kit implementation is a later slice, wired from
// cmd/jam.
type Launcher interface {
	Raise(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (location string, err error)
	Teardown(ctx context.Context, inst Instance) error
	Probe(ctx context.Context, inst Instance) (Liveness, error)
	Pause(ctx context.Context, inst Instance) error
	Unpause(ctx context.Context, inst Instance) error
	// ApplyEgress sets a running cove's egress to p (nil = the kit default).
	ApplyEgress(ctx context.Context, inst Instance, p *EgressPolicy) error
	// PrepareKit builds/records a kit from its full definition, wherever this
	// substrate builds (local now; remote later). The supervisor calls it only
	// when a Raise returned ErrKitNotReady, then retries the Raise. Idempotent and
	// de-duped per (id,version); may report KitPreparing (async) so the supervisor
	// defers rather than blocking.
	PrepareKit(ctx context.Context, def KitDefinition) (KitStatus, error)
}

// ImageTagger is optionally implemented by a Launcher that can name the image a
// raise of ref runs (a pure function of ref and the launcher's own build
// inputs). The supervisor records it on each raised Instance and compares it with
// CurrentImageTag to report image staleness; a launcher without it leaves every
// cove's image status unknown.
type ImageTagger interface {
	ImageTag(ref KitRef) string
}

// CurrentImage is what a raise for a role would run now (Supervisor.CurrentImage).
type CurrentImage struct {
	Kit    KitRef // the kit ref, keyed on the role's harness (meaningful when HasKit)
	HasKit bool   // false: the role raises no kit
	Tag    string // the launcher's image tag for Kit; "" when it cannot name images
}

// Key identifies the image: its tag, else its kit ref; "" for no kit.
func (c CurrentImage) Key() string {
	switch {
	case !c.HasKit:
		return ""
	case c.Tag != "":
		return c.Tag
	}
	return c.Kit.String()
}

// StateKeeper is the optional launcher surface over sessions' persisted state
// (a standing session's labeled volumes, COV-249). Only the standing
// reconciler drives it — sweeping the state of undeclared names and purging a
// reset one — never a teardown, so a restart or a Lost cove keeps its state. A
// launcher without it persists no state.
type StateKeeper interface {
	// PurgeState deletes actorID's state; state still in use errors.
	PurgeState(ctx context.Context, actorID string) error
	// StateOwners lists the actor ids that have persisted state.
	StateOwners(ctx context.Context) ([]string, error)
}

// StandingResetter resets a declared standing session — tears its cove down
// and deletes its state, the declaration kept — serialized with the standing
// reconciler so it can't re-raise the session on the old state mid-reset.
// An error means the reset is still pending: the reconciler finishes it.
type StandingResetter interface {
	ResetStanding(ctx context.Context, project, role, name string) error
}

// StandingUpgrader is the standing reconciler's upgrade queue (COV-251). The
// admin API and UI only record intent (QueueUpgrade); each reconciler pass
// then prepares the current image, waits for the session to be idle (unless
// forced), tears its cove down keeping its state, and raises it again. Pending
// upgrades are in memory: a Jam restart drops them.
type StandingUpgrader interface {
	// QueueUpgrade records a pending upgrade (a re-queue keeps one, OR-ing
	// force). Errors wrap ErrStandingNotDeclared or ErrStandingResetPending.
	QueueUpgrade(project, role, name string, force bool) error
	// UpgradeState is the name's pending upgrade state — queued, preparing,
	// waiting-for-idle, teardown-failed or error, with a detail after ": " —
	// or "" when none is pending.
	UpgradeState(project, role, name string) string
}

// Pending-upgrade states (StandingUpgrader.UpgradeState's prefix).
const (
	UpgradeQueued         = "queued"
	UpgradePreparing      = "preparing"
	UpgradeWaitingIdle    = "waiting-for-idle"
	UpgradeTeardownFailed = "teardown-failed"
	UpgradeError          = "error"
)

// Standing-reconciler refusals, mapped to HTTP statuses by the admin API.
var (
	ErrStandingNotDeclared  = errors.New("standing session not declared")
	ErrStandingResetPending = errors.New("standing session reset pending")
)

// ControlSink pushes lifecycle control to a connected cove (implemented by the
// Attach server). Best-effort and non-blocking; no connected stream is a no-op.
// nil when no stream server runs (slice-1 behavior).
type ControlSink interface {
	RequestTeardown(actorID string)
	Wake(actorID string, reasons ...WakeReason)
}

// TunnelCloser closes the port tunnels a studio holds (implemented by
// *attach.TunnelBroker). Once closed, a studio takes no tunnels until
// Reopen. nil when no tunnel broker runs.
type TunnelCloser interface {
	// CloseAll closes every tunnel (teardown).
	CloseAll(actorID string)
	// CloseIfUnused closes them only if none is in use, reporting whether it
	// did (a pause must not cut off a connected human).
	CloseIfUnused(actorID string) bool
	// Reopen lets the studio take tunnels again (raise, resume).
	Reopen(actorID string)
}

// ErrStudioInUse: Idle refused to pause a studio with a tunnel in use (an
// open SSH connection counts as activity).
var ErrStudioInUse = errors.New("studio has a tunnel in use")

// tailReader is the sliver of the message log the supervisor needs to baseline a
// cove's wake-on cursor to the current log position when it enters Waiting.
type tailReader interface {
	TailSeq() (int64, bool)
}

// Releaser records that a session's reservation was released when its cove is torn
// down — the actual-state-out half of the Supervisor↔Allocator seam (the design's
// "reports releases/liveness back up"; not the Allocator directing the Supervisor).
// Best-effort shadow write: nil (in-memory test store / no Postgres) is a no-op, and a
// recording failure never fails teardown. Satisfied structurally by
// *allocator.Allocator (no import of allocator here — no cycle).
type Releaser interface {
	RecordRelease(ctx context.Context, project, role, reservationID string) error
}

// Supervisor owns the managed-cove lifecycle: the durable registry (via Store),
// the lease model, and the state machine. One supervisor per Jam process.
type Supervisor struct {
	// instMu serializes every read-modify-write of an Instance: the attach
	// stream (Report, Heartbeat), the wake-on engine (SetWaitSeq, FireAlarms,
	// RecordNag…) and the cove endpoints (SetAlarm, SetEndRequested…) all
	// write whole records, so an unserialized pair loses one write.
	instMu    sync.Mutex
	store     Store
	launcher  Launcher
	holder    string // this process's lease-holder id
	ttl       time.Duration
	reconcile time.Duration
	now       func() time.Time
	log       *slog.Logger
	sink      ControlSink
	tunnels   TunnelCloser // SetTunnelCloser; nil → no tunnels to close
	tail      tailReader
	released  Releaser
	channels  SessionChannels
	// defaultStudioKit is the studio kit a raise runs from when its role names no
	// kit (its light reference). nil leaves such a raise with no kit (hermetic
	// tests, no kit wiring). Set once at wiring via SetDefaultStudioKit; the full
	// definition is resolved from the registry only on an ErrKitNotReady miss.
	defaultStudioKit *KitRef

	kitRefs   kitRefCache          // CurrentImage's resolved kit refs
	imgLogMu  sync.Mutex           // guards imgLogged
	imgLogged map[string]time.Time // project/role → last CurrentImage error logged
	// resetter is the standing reconciler (SetStandingResetter); nil → a
	// standing reset is unavailable (503).
	resetter StandingResetter
	// upgrader is the standing reconciler (SetStandingUpgrader); nil → a
	// standing upgrade is unavailable (503).
	upgrader StandingUpgrader
	// exposureSink hears a session's exposed ports change (SetExposureSink).
	exposureSink ExposureSink
	// previews: studio previews are enabled on this Jam (SetPreviewsEnabled).
	previews bool
	// draftBuilds runs kit draft test builds, and the images draft studios
	// run (SetKitDraftBuilds); nil → both are unavailable (503).
	draftBuilds *KitDraftBuilds
	notifyMu    sync.Mutex // orders sink deliveries (see notifyExposures)
	// labelsMu guards pendingLabels: the labels of manual raises in flight
	// (RaiseManual's reserveLabel).
	labelsMu      sync.Mutex
	pendingLabels map[string]bool
}

func NewSupervisor(store Store, launcher Launcher, holder string, ttl, reconcile time.Duration, now func() time.Time, log *slog.Logger) *Supervisor {
	if now == nil {
		now = time.Now
	}
	return &Supervisor{store: store, launcher: launcher, holder: holder, ttl: ttl, reconcile: reconcile, now: now, log: log}
}

// NewHolderID mints a per-process lease-holder id: <hostname>/<pid>/<4 hex>.
func NewHolderID() string {
	host, _ := os.Hostname()
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(b[:]))
}

// SetControlSink installs the control sink after construction (resolving the
// supervisor↔Attach-server cycle). nil-safe throughout.
func (s *Supervisor) SetControlSink(sink ControlSink) { s.sink = sink }

// SetTunnelCloser installs the tunnel closer after construction. Like the
// control sink it is set once at wiring time; nil-safe throughout.
func (s *Supervisor) SetTunnelCloser(c TunnelCloser) { s.tunnels = c }

// SetKitDraftBuilds wires the kit draft test builds: the one registry the
// admin build routes and a draft studio's raise both use. Set once at
// wiring, before serving.
func (s *Supervisor) SetKitDraftBuilds(b *KitDraftBuilds) { s.draftBuilds = b }

// KitDraftBuilds is the registry SetKitDraftBuilds wired; nil when none was
// (no launcher that builds images) or s is nil.
func (s *Supervisor) KitDraftBuilds() *KitDraftBuilds {
	if s == nil {
		return nil
	}
	return s.draftBuilds
}

// SetTailReader wires the squawk log tail source used to baseline WaitSeq on
// entering Waiting (and CommitSeq at Raise). Called once at wiring time before
// serving begins; nil (no message log) leaves both 0.
func (s *Supervisor) SetTailReader(r tailReader) { s.tail = r }

// SetReleaser wires the allocation releaser recorded on teardown (the
// actual-state-out seam). Called once at wiring time; nil (no Postgres ledger)
// leaves teardown recording nothing, exactly as before.
func (s *Supervisor) SetReleaser(r Releaser) { s.released = r }

// SessionChannels follows sessions into and out of their channels: the
// intercom joins a session set up on a ticket to the ticket's channel, and
// takes it out when the session ends (teardown).
type SessionChannels interface {
	SetUp(inst Instance) error
	Ended(inst Instance) error
}

// SetSessionChannels wires the intercom in. Best effort: a failure is logged
// and never fails the setup or teardown. Call before serving.
func (s *Supervisor) SetSessionChannels(c SessionChannels) { s.channels = c }

// trackerOf is the tracker connection the session channels key tickets on
// (the one serve resolved), nil when they don't say: TrackerConnection then.
func (s *Supervisor) trackerOf() func() (ident.ID, bool) {
	if t, ok := s.channels.(interface{ Tracker() (ident.ID, bool) }); ok {
		return t.Tracker
	}
	return nil
}

// SetDefaultStudioKit wires the studio kit a raise runs from when its role names
// no kit — its light reference, recorded in the registry by EnsureDefaultStudioKit
// at wiring. With it set, a role without a kit stamps this ref on the launch spec
// and, if the launcher reports ErrKitNotReady, resolves the definition from the
// registry and PrepareKits it before retrying. Unset (the default) leaves an
// unnamed-kit raise with no kit so hermetic tests and un-wired setups are
// unaffected. While the config repo manages the default kit, a raise runs its
// current version instead of ref's (ref.Version 0 always means current).
func (s *Supervisor) SetDefaultStudioKit(ref KitRef) { s.defaultStudioKit = &ref }

func (s *Supervisor) tailSeq() int64 {
	if s.tail == nil {
		return 0
	}
	seq, _ := s.tail.TailSeq()
	return seq
}

// assignColor gives actorID a session color (docs/usage/jam/session-colors.md)
// unless it has one: the palette color farthest from the live sessions'.
// Best effort: a store error leaves the session without one.
func (s *Supervisor) assignColor(actorID string) {
	if _, ok := s.store.SessionColor(actorID); ok {
		return
	}
	var live []string
	for _, i := range s.store.ListInstances() {
		if i.ActorID == actorID || i.Phase == PhaseGone {
			continue
		}
		if c, ok := s.store.SessionColor(i.ActorID); ok {
			live = append(live, c)
		}
	}
	if _, err := s.store.PutSessionColor(actorID, PickSessionColor(live)); err != nil && s.log != nil {
		s.log.Warn("raise: failed to assign a session color", "id", actorID, "error", err)
	}
}

// Raise enrolls the identity, mints a per-instance launch secret, launches the
// cove, and records a Live Instance leased to this process. Returns the
// Instance, the minted identity token, and the minted launch secret (each
// returned once — the launcher/cove consume them to connect in a later slice;
// only the launch secret's hash is persisted). A failed mint or launch rolls
// back the enrollment so no dangling identity is left.
func (s *Supervisor) Raise(ctx context.Context, spec RaiseSpec) (Instance, string, string, error) {
	if spec.ActorID == "" {
		return Instance{}, "", "", fmt.Errorf("actor id is required")
	}
	projectID, projectOK := ProjectIDOf(s.store, spec.Project)
	if projectOK {
		spec.Project = string(projectID) // stored references name the project by id
	}
	// A standing session lives on one manager: pin it before enrolling, so a
	// refusal leaves nothing to roll back.
	pin, err := s.applyStandingPin(&spec, projectID, projectOK)
	if err != nil {
		return Instance{}, "", "", err
	}
	// A personal session's cove may message its owner and nobody else: the
	// override REPLACES the role's addressing (least privilege).
	var ov *Override
	switch {
	case spec.OwnerID != "":
		ov = &Override{Addressing: []string{"user:" + string(spec.OwnerID)}}
	case spec.Owner != "":
		ov = &Override{Addressing: []string{"user:" + spec.Owner}}
	}
	tok, err := Enroll(s.store, spec.ActorID, spec.Project, spec.Role, ov, s.now())
	if err != nil {
		return Instance{}, "", "", err
	}
	// The cove's client connector comes from its own grants' destinations; a
	// conflict among them fails the raise (fail closed), revoking the identity.
	actor, _ := s.store.Lookup(HashToken(tok))
	conn, err := ConnectorFor(s.store, actor)
	if err != nil {
		if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil {
			s.log.Warn("raise rollback: failed to revoke identity after connector conflict", "id", spec.ActorID, "error", rmErr)
		}
		return Instance{}, "", "", fmt.Errorf("raise: connector for role %s/%s: %w", orDefaultProject(spec.Project), spec.Role, err)
	}
	spec.Connector = &conn
	// The role, not the caller, decides the cove's egress policy AND its kit
	// (Enroll just proved the role exists). Callers — the Requisitioner, sessions,
	// standing — need no change, and none can widen a role's egress or pick its kit
	// by setting the spec; the one exception is the admin raise of a draft studio
	// (spec.KitDraft, below), which runs that kit's draft in place of the role's.
	spec.Egress = nil
	role, roleOK := s.store.GetRole(spec.Project, spec.Role)
	if roleOK && role.Scope.Egress != nil {
		spec.Egress = &EgressPolicy{Domains: slices.Clone(role.Scope.Egress.Domains)}
	}
	secret, err := MintToken()
	if err != nil {
		_ = s.store.RemoveActor(spec.ActorID)
		return Instance{}, "", "", err
	}
	// Stamp the kit the role raises from (its light reference); the launcher
	// resolves it against its prepared-kit inventory. A role that names a kit uses
	// a managed variant of it; otherwise the default managed kit (nil = legacy
	// static-image path). Fail closed on a bad role.Kit, rolling back the identity.
	//
	// The image is keyed on the role's model-spec too (the harness layer: CLI
	// version + plugins) — the one the connector above carries — so the kit is
	// resolved — parsed and hashed once — for that harness; the one definition
	// serves the image key, the session context and a PrepareKit. raiseKit is
	// shared with CurrentImage, so a recorded tag and a current one can't drift.
	//
	// A draft studio (spec.KitDraft) runs that kit's draft instead: its
	// image is the test build of the draft tree under the role's harness,
	// built (or reused) here, synchronously.
	var def *KitDefinition
	var draft *draftRaise
	switch {
	case spec.KitDraft != "":
		dr, derr := s.raiseDraft(ctx, spec, role)
		if derr != nil {
			if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil {
				s.log.Warn("raise rollback: failed to revoke identity after draft resolve failure", "id", spec.ActorID, "error", rmErr)
			}
			return Instance{}, "", "", fmt.Errorf("raise: draft studio: %w", derr)
		}
		draft = &dr
		spec.Kit, spec.DraftImage, spec.draft, def = dr.def.Ref, &dr.image, &dr.ref, &dr.def
		// It runs where its image is, and nowhere else. Without a placing
		// launcher there are no manager names: the one launcher built it.
		if _, ok := s.launcher.(PlacingLauncher); ok {
			spec.Placement.Managers = []string{dr.manager}
		}
	case roleOK:
		d, have, kerr := s.raiseKit(spec.Project, role)
		if kerr != nil {
			if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil {
				s.log.Warn("raise rollback: failed to revoke identity after kit resolve failure", "id", spec.ActorID, "error", rmErr)
			}
			return Instance{}, "", "", fmt.Errorf("raise: resolve kit for role %s/%s: %w", orDefaultProject(spec.Project), spec.Role, kerr)
		}
		if have {
			spec.Kit, def = d.Ref, &d
		}
	}
	bundle := s.compileContext(spec, actor, spec.Kit, def, nil)
	s.logContextWarnings(spec.ActorID, bundle)
	spec.Context = &bundle
	creds := LaunchCreds{IdentityToken: tok, LaunchSecret: secret}
	placed, err := s.raiseOn(ctx, spec, creds)
	if errors.Is(err, ErrKitNotReady) && draft == nil {
		// The launcher lacks this kit: send it the full definition (from the
		// registry, unless Raise resolved it) and retry once. A still-preparing
		// build does not block — the raise fails and the caller's reconcile
		// retries on a later tick.
		var ran KitDefinition
		var knr *KitNotReadyError
		var manager string
		if errors.As(err, &knr) {
			manager = knr.Manager
		}
		if placed, ran, err = s.prepareKitAndRetry(ctx, manager, &spec, actor, creds, def); err == nil {
			def = &ran // the definition prepared and run
		}
	}
	if err != nil {
		if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil { // rollback identity on failed launch
			s.log.Warn("raise rollback: failed to revoke identity after launch failure", "id", spec.ActorID, "error", rmErr)
		}
		return Instance{}, "", "", fmt.Errorf("raise: %w", err)
	}
	now := s.now()
	inst := Instance{
		ActorID: spec.ActorID, Project: orDefaultProject(spec.Project), Role: spec.Role, Unit: spec.Unit,
		Owner: spec.Owner, OwnerID: spec.OwnerID, Name: spec.Name, SessionKind: spec.SessionKind,
		Mission: NormalizeStatusLine(spec.Mission),
		Manager: placed.Manager, Location: placed.Location, Phase: PhaseLive, Activity: ActivityRunning,
		Lease:            Lease{Holder: s.holder, Expiry: now.Add(s.ttl)},
		LaunchSecretHash: HashToken(secret),
		RaisedAt:         now, LastSeen: now,
		WaitSeq:   s.tailSeq(), // wake-on baseline: the cove starts Running and reads its inbox itself
		CommitSeq: s.tailSeq(), // CommitCursor stays "" — the cove has read nothing yet, this is an ordering baseline, not an echoable id
		Egress:    EgressFingerprint(spec.Egress),
		Kit:       spec.Kit,
		ImageTag:  s.imageTagOn(placed.Manager, spec.Kit),
	}
	if def != nil && def.Ref == spec.Kit {
		inst.KitChain = chainRefs(def.Chain)
	}
	if draft != nil {
		inst.KitDraft, inst.ImageTag, inst.KitDraftImage = &draft.ref, draft.image.Tag, draft.image.ID
	}
	if err := s.store.PutInstance(inst); err != nil {
		if tdErr := s.launcher.Teardown(ctx, inst); tdErr != nil && s.log != nil {
			s.log.Warn("raise rollback: failed to tear down launched cove", "id", spec.ActorID, "error", tdErr)
		}
		if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil {
			s.log.Warn("raise rollback: failed to revoke identity after PutInstance failure", "id", spec.ActorID, "error", rmErr)
		}
		return Instance{}, "", "", err
	}
	s.assignColor(spec.ActorID)
	if draft != nil {
		s.keepDraftTree(draft.objs)
	}
	if pin.track && pin.manager == "" && placed.Manager != "" {
		if err := s.store.SetStandingSessionManager(projectID, spec.Role, spec.Name, placed.Manager); err != nil && s.log != nil {
			s.log.Warn("raise: pinning the standing session to its manager failed (non-fatal)", "id", spec.ActorID, "manager", placed.Manager, "err", err.Error())
		}
	}
	s.recordRaiseSetup(inst, spec, def)
	if s.tunnels != nil {
		s.tunnels.Reopen(spec.ActorID) // the same id may have been closed by an earlier teardown
	}
	if s.channels != nil {
		if err := s.channels.SetUp(inst); err != nil && s.log != nil {
			s.log.Warn("raise: joining the session's channels failed (non-fatal)", "id", spec.ActorID, "err", err.Error())
		}
	}
	// A raise starts a fresh studio: nothing is exposed (inst.Exposures is
	// unset), so tell the sink the set is empty.
	s.notifyExposures(spec.ActorID)
	if s.log != nil {
		s.log.Info("cove raised", "id", spec.ActorID, "project", inst.Project, "role", spec.Role, "phase", string(inst.Phase))
	}
	return inst, tok, secret, nil
}

// kitFor resolves the studio kit a raise for role runs, for harness install h
// (its definition, Ref keyed on h). A role that names a kit (role.Kit) resolves
// that registered studio kit's current version (fail closed if the kit is
// absent, not tag-safe, or not a studio kit); an unnamed kit falls back to the
// wiring-set default studio kit (fail closed if it left the registry).
// ok=false (no default set) leaves the raise with no kit, so un-wired setups
// and hermetic tests are unaffected.
func (s *Supervisor) kitFor(role Role, h harnessinstall.Install) (KitDefinition, bool, error) {
	src, have, err := s.kitSourceFor(role)
	if err != nil || !have {
		return KitDefinition{}, false, err
	}
	def, err := src.definition(s.store, h)
	return def, err == nil, err
}

// kitSource is the registry entry a raise's kit comes from: its ref (no digest
// yet) and stored version (LoadKitVersion). Turning it into a definition
// resolves its parent chain and hashes.
type kitSource struct {
	ref   KitRef
	ver   KitVersion
	named bool // role.Kit names it (vs the wiring default) — error wording only
}

func (k kitSource) definition(store Store, h harnessinstall.Install) (KitDefinition, error) {
	def, err := kitDefinitionFromVersion(store, k.ref, k.ver, h)
	if err != nil && k.named {
		return KitDefinition{}, fmt.Errorf("studio kit %q: %w", k.ref.ID, err)
	}
	return def, err
}

// kitSourceFor is kitFor's registry lookup: which kit version a raise for role
// runs and its stored version, without resolving its chain.
func (s *Supervisor) kitSourceFor(role Role) (kitSource, bool, error) {
	if role.Kit != "" {
		ref, ver, err := studioKitCurrent(s.store, role.Kit)
		if err != nil {
			return kitSource{}, false, err
		}
		return kitSource{ref: ref, ver: ver, named: true}, true, nil
	}
	if s.defaultStudioKit != nil {
		// The wired version, unless the config repo manages the default kit:
		// then its current version, so a commit to kits/default/ reaches the
		// next raise without a serve restart. Version 0 is current too.
		id, version := s.defaultStudioKit.ID, s.defaultStudioKit.Version
		if KitManaged(s.store, id) {
			version = 0
		}
		ver, ok := LoadKitVersion(s.store, id, version)
		if !ok || ver.Version == 0 {
			return kitSource{}, false, fmt.Errorf("default studio kit %s not in registry", s.defaultStudioKit)
		}
		return kitSource{ref: KitRef{ID: id, Version: ver.Version}, ver: ver}, true, nil
	}
	return kitSource{}, false, nil
}

// raiseHarness is the harness a raise for project/role installs: the model-spec
// its actor resolves to. A raised actor holds exactly this one grant (Enroll),
// so this is the spec its connector carries.
func (s *Supervisor) raiseHarness(project string, role Role) (harnessinstall.Install, error) {
	ms, err := ModelSpecFor(s.store, Actor{Grants: []Grant{{Project: orDefaultProject(project), Role: role.Name}}})
	if err != nil {
		return harnessinstall.Install{}, err
	}
	return harnessinstall.FromSpec(ms), nil
}

// raiseKit is THE resolution of what a raise for project/role builds its image
// from: the role's kit under the role's model-spec harness. Raise uses it, and
// CurrentImage resolves through the same steps (cached), so a recorded tag and
// a current one cannot drift. have=false: the role raises no kit.
func (s *Supervisor) raiseKit(project string, role Role) (KitDefinition, bool, error) {
	h, err := s.raiseHarness(project, role)
	if err != nil {
		return KitDefinition{}, false, err
	}
	return s.kitFor(role, h)
}

// draftRaiseGone answers a draft studio raise whose request went away while
// it waited for the draft's test build.
const draftRaiseGone = "the raise stopped waiting for the draft's test build (it runs on: raise again to use it)"

// draftRaise is what a draft studio's raise resolved: its definition, the
// draft tree it runs, that tree's objects, its built image, and the
// manager holding that image.
type draftRaise struct {
	def     KitDefinition
	ref     KitDraftRef
	objs    KitObjects
	image   KitDraftImage
	manager string
}

// raiseDraft resolves a draft studio of kit spec.KitDraft for role: the
// draft tree as of now under the role's harness (DraftKitDefinition), and
// its image through the test-build jobs (Ensure, over the raise's
// placement managers), waited for.
func (s *Supervisor) raiseDraft(ctx context.Context, spec RaiseSpec, role Role) (draftRaise, error) {
	if s.draftBuilds == nil {
		return draftRaise{}, writeErr(http.StatusServiceUnavailable, kitDraftBuildsOff)
	}
	h, err := s.raiseHarness(spec.Project, role)
	if err != nil {
		return draftRaise{}, err
	}
	def, ref, err := DraftKitDefinition(s.store, spec.KitDraft, h)
	if err != nil {
		return draftRaise{}, err
	}
	img, manager, objs, err := s.draftBuilds.Ensure(ctx, def, ref, "studio raise "+spec.Name, spec.Placement.Managers)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// The raise's request went away while it waited for the build;
		// the build runs on, for a later raise to reuse.
		return draftRaise{}, writeErr(http.StatusServiceUnavailable, "%s", draftRaiseGone)
	}
	if err != nil {
		return draftRaise{}, err
	}
	return draftRaise{def: def, ref: ref, objs: objs, image: img, manager: manager}, nil
}

// keepDraftTree re-stores a draft studio's tree should GC have collected
// it between the raise reading the draft and recording the instance (a
// discard or an edit meanwhile). From the recorded instance on, it is a GC
// root, so this check cannot race a later collection.
func (s *Supervisor) keepDraftTree(objs KitObjects) {
	tree := objs.Trees[0].Hash()
	if _, ok := s.store.KitTree(tree); ok {
		return
	}
	if err := s.store.PutKitDocs(nil, objs); err != nil && s.log != nil {
		s.log.Warn("draft studio: could not re-store its tree", "tree", shortTree(tree), "err", err.Error())
	}
}

// currentKitRef is raiseKit's Ref, cached on (kit ref, version content, parent chain, harness):
// the UI polls the studios list every few seconds, and a kit parse + digest per
// poll per role is waste. The launcher's assembly half of the tag is never
// cached (ImageTag is cheap and live).
func (s *Supervisor) currentKitRef(project string, role Role) (KitRef, bool, error) {
	h, err := s.raiseHarness(project, role)
	if err != nil {
		return KitRef{}, false, err
	}
	src, have, err := s.kitSourceFor(role)
	if err != nil || !have {
		return KitRef{}, false, err
	}
	chain := chainUnresolved
	if src.ver.Err == nil {
		chain = chainCacheKey(s.store, src.ver.Kit)
	}
	if chain == chainUnresolved {
		// Never touch the cache for a broken chain: resolve fresh, which fails
		// with the real error (or, if the chain healed meanwhile, succeeds
		// without storing under a key that could outlive the next break).
		def, err := src.definition(s.store, h)
		if err != nil {
			return KitRef{}, false, err
		}
		return def.Ref, true, nil
	}
	key := kitRefKey{ref: src.ref, content: src.ver.Key(), chain: chain, harness: fmt.Sprintf("%s|%s|%s", h.Type, h.Version, strings.Join(h.Plugins, ","))}
	if ref, ok := s.kitRefs.get(key); ok {
		return ref, true, nil
	}
	def, err := src.definition(s.store, h)
	if err != nil {
		return KitRef{}, false, err
	}
	s.kitRefs.put(key, def.Ref)
	return def.Ref, true, nil
}

type kitRefKey struct {
	ref     KitRef
	content string // the version's content identity (KitVersion.Key): a removed-and-re-pushed kit can reuse a version number
	chain   string // the parents' resolved id@version + content identity ("" unchained): a parent push changes it
	harness string
}

// kitRefCache memoizes currentKitRef. Bounded: past kitRefCacheMax entries it
// starts over (entries only go stale, never wrong — the key is the full input).
type kitRefCache struct {
	mu     sync.Mutex
	m      map[kitRefKey]KitRef
	misses int // resolutions (parse + hash) performed; tests read it
}

const kitRefCacheMax = 256

func (c *kitRefCache) get(k kitRefKey) (KitRef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[k]
	return r, ok
}

func (c *kitRefCache) put(k kitRefKey, r KitRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= kitRefCacheMax {
		c.m = map[kitRefKey]KitRef{}
	}
	c.m[k] = r
	c.misses++
}

// standingPin is a standing raise's pin state: track is set when the raise is
// for a standing session on a placing launcher; manager is its pin ("" =
// unpinned, so the placed manager becomes the pin).
type standingPin struct {
	track   bool
	manager string
}

// applyStandingPin enforces a standing session's pin on spec's placement. Only
// a standing, named raise through a PlacingLauncher is pinned; others pass
// untouched. A pin the request excludes fails (ErrPinExcluded); a pin to a
// manager that is no longer configured is cleared, so the session places fresh.
func (s *Supervisor) applyStandingPin(spec *RaiseSpec, project ident.ID, projectOK bool) (standingPin, error) {
	pl, ok := s.launcher.(PlacingLauncher)
	if !ok || !projectOK || spec.SessionKind != SessionKindStanding || spec.Name == "" {
		return standingPin{}, nil
	}
	pin := s.store.StandingSessionManager(project, spec.Role, spec.Name)
	if pin != "" && !pl.HasManager(pin) {
		if s.log != nil {
			s.log.Warn("standing session's pinned manager is not configured; placing fresh, state not carried over",
				"id", spec.ActorID, "project", spec.Project, "role", spec.Role, "name", spec.Name, "pin", pin)
		}
		if err := s.store.SetStandingSessionManager(project, spec.Role, spec.Name, ""); err != nil {
			return standingPin{}, fmt.Errorf("raise: clear stale pin: %w", err)
		}
		pin = ""
	}
	if pin == "" {
		return standingPin{track: true}, nil
	}
	if len(spec.Placement.Managers) > 0 && !slices.Contains(spec.Placement.Managers, pin) {
		return standingPin{}, fmt.Errorf("raise: %w: %s", ErrPinExcluded, pin)
	}
	spec.Placement.Managers = []string{pin}
	return standingPin{track: true, manager: pin}, nil
}

// raiseOn raises through the launcher, recording which manager hosts the cove
// when the launcher places raises; otherwise the Manager stays "" and, since no
// manager names exist, a placement naming any is refused (ErrUnknownManager).
func (s *Supervisor) raiseOn(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (Placed, error) {
	if pl, ok := s.launcher.(PlacingLauncher); ok {
		return pl.RaiseOn(ctx, spec, creds)
	}
	if len(spec.Placement.Managers) > 0 {
		return Placed{}, fmt.Errorf("%w: %s", ErrUnknownManager, spec.Placement.Managers[0])
	}
	loc, err := s.launcher.Raise(ctx, spec, creds)
	return Placed{Location: loc}, err
}

// imageTag is the tag the launcher runs ref under on its default manager.
func (s *Supervisor) imageTag(ref KitRef) string { return s.imageTagOn("", ref) }

// imageTagOn is the tag manager runs ref under ("" = the default); "" for no kit
// or a launcher that cannot name its images (the one ImageTagger guard).
func (s *Supervisor) imageTagOn(manager string, ref KitRef) string {
	if ref.ID == "" {
		return ""
	}
	if pl, ok := s.launcher.(PlacingLauncher); ok && manager != "" {
		return pl.ImageTagOn(manager, ref)
	}
	t, ok := s.launcher.(ImageTagger)
	if !ok || ref.ID == "" {
		return ""
	}
	return t.ImageTag(ref)
}

// CurrentImage is what a raise for project/role would run NOW (via raiseKit's
// resolution): the kit ref (HasKit=false: the role raises no kit) and the image
// tag the launcher names it ("" when the launcher cannot name images). Errors
// (no supervisor, a missing role, an unresolvable kit or model-spec) are
// returned, never guessed around, and logged at debug level once per role per
// minute so an "unknown" image status can be explained. manager names whose
// image tag is reported ("" — or one no longer configured — = the launcher's
// default).
func (s *Supervisor) CurrentImage(project, role, manager string) (CurrentImage, error) {
	if s == nil {
		return CurrentImage{}, fmt.Errorf("no supervisor")
	}
	cur, err := s.currentImage(project, role, s.imageManager(manager))
	if err != nil {
		s.logImageErr(project, role, err)
	}
	return cur, err
}

// imageManager is the manager image calls for manager go to: a name the
// placing launcher no longer has is treated as a reset — the default manager
// ("") — as applyStandingPin treats a stale pin on raise.
func (s *Supervisor) imageManager(manager string) string {
	if manager == "" {
		return ""
	}
	if pl, ok := s.launcher.(PlacingLauncher); ok && !pl.HasManager(manager) {
		if s.log != nil {
			s.log.Debug("image manager is not configured; using the default manager", "manager", manager)
		}
		return ""
	}
	return manager
}

func (s *Supervisor) currentImage(project, role, manager string) (CurrentImage, error) {
	r, ok := s.store.GetRole(project, role)
	if !ok {
		return CurrentImage{}, fmt.Errorf("role %s/%s not found", orDefaultProject(project), role)
	}
	ref, have, err := s.currentKitRef(project, r)
	if err != nil || !have {
		return CurrentImage{}, err
	}
	return CurrentImage{Kit: ref, HasKit: true, Tag: s.imageTagOn(manager, ref)}, nil
}

// logImageErr logs a CurrentImage error at debug level, at most once per role
// per minute (the UI polls the list every few seconds).
func (s *Supervisor) logImageErr(project, role string, err error) {
	if s.log == nil {
		return
	}
	key, now := orDefaultProject(project)+"/"+role, s.now()
	s.imgLogMu.Lock()
	last, seen := s.imgLogged[key]
	if seen && now.Sub(last) < time.Minute {
		s.imgLogMu.Unlock()
		return
	}
	if s.imgLogged == nil {
		s.imgLogged = map[string]time.Time{}
	}
	s.imgLogged[key] = now
	s.imgLogMu.Unlock()
	s.log.Debug("image status unresolvable", "project", orDefaultProject(project), "role", role, "error", err.Error())
}

// prepareKitAndRetry handles a Raise that returned ErrKitNotReady: it takes the
// kit definition Raise resolved (else resolves it from the registry), asks the
// launcher to prepare it, and retries the Raise once when the kit is ready. It
// returns the definition it prepared — the image that runs. A KitPreparing
// status (an async remote build) returns an error WITHOUT blocking — the
// caller's reconcile loop retries the raise on a later tick, by which point the
// build may be done. The caller (Raise) rolls back the enrollment on any error
// this returns.
//
// manager is the one that reported the kit missing ("" for a non-placing
// launcher): the kit is prepared there and the retry is pinned to it.
func (s *Supervisor) prepareKitAndRetry(ctx context.Context, manager string, spec *RaiseSpec, actor Actor, creds LaunchCreds, resolved *KitDefinition) (Placed, KitDefinition, error) {
	var def KitDefinition
	if resolved != nil && resolved.Ref == spec.Kit {
		def = *resolved
	} else {
		d, ok, err := ResolveKitDefinition(s.store, spec.Kit, harnessFor(*spec))
		if err != nil {
			return Placed{}, KitDefinition{}, err
		}
		if !ok {
			return Placed{}, KitDefinition{}, fmt.Errorf("kit %s not in registry", spec.Kit)
		}
		// The chain was re-resolved at the following parents' current versions.
		// If a parent moved since spec.Kit was keyed, the digest differs: the
		// image prepared (and run) now is the fresh one, so the retried raise
		// carries its ref and a context compiled from it, and the caller records
		// its chain.
		if d.Ref != spec.Kit {
			spec.Kit = d.Ref
			bundle := s.compileContext(*spec, actor, spec.Kit, &d, nil)
			s.logContextWarnings(spec.ActorID, bundle)
			spec.Context = &bundle
		}
		def = d
	}
	var status KitStatus
	var err error
	pl, placing := s.launcher.(PlacingLauncher)
	if placing && manager != "" {
		status, err = pl.PrepareKitOn(ctx, manager, def)
	} else {
		status, err = s.launcher.PrepareKit(ctx, def)
	}
	if err != nil {
		return Placed{}, KitDefinition{}, fmt.Errorf("prepare kit %s: %w", spec.Kit, err)
	}
	if status.State != KitReady {
		if s.log != nil {
			s.log.Info("kit preparing; deferring raise to reconcile", "id", spec.ActorID, "kit", spec.Kit.String())
		}
		return Placed{}, KitDefinition{}, fmt.Errorf("kit %s is preparing; deferring", spec.Kit)
	}
	if s.log != nil {
		s.log.Info("kit prepared; retrying raise", "id", spec.ActorID, "kit", spec.Kit.String())
	}
	retry := *spec
	if placing && manager != "" {
		retry.Placement = Placement{Managers: []string{manager}}
	}
	placed, err := s.raiseOn(ctx, retry, creds)
	return placed, def, err
}

// logContextWarnings logs a raise's session-context warnings — once, at raise,
// not on every GET /context refresh.
func (s *Supervisor) logContextWarnings(actorID string, b sessionctx.Bundle) {
	if s.log == nil {
		return
	}
	for _, w := range b.Warnings {
		s.log.Warn("raise: session context", "id", actorID, "warning", w)
	}
}

// harnessFor is the harness layer a raise's image installs: its role's
// model-spec (delivered in the connector Raise resolved), or claude-default's
// install when none is (harnessinstall.FromSpec(nil)).
func harnessFor(spec RaiseSpec) harnessinstall.Install {
	if spec.Connector == nil {
		return harnessinstall.Default()
	}
	return harnessinstall.FromSpec(spec.Connector.ModelSpec)
}

// RecordConnector records the connector fingerprint a cove reports having
// applied to its latest agent spawn. Staleness is derived on read
// (CoveSummaries), never stored, so it cannot itself drift.
func (s *Supervisor) RecordConnector(actorID, fp string) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.Phase == PhaseGone || inst.Connector == fp {
		return nil
	}
	inst.Connector = fp
	return s.store.PutInstance(inst)
}

// Heartbeat renews the lease + LastSeen for a connected cove WITHOUT changing
// Activity or Phase (the stream keepalive path). Errors if the instance is
// absent or gone.
func (s *Supervisor) Heartbeat(actorID string) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.Phase == PhaseGone {
		return fmt.Errorf("instance %q is gone", actorID)
	}
	now := s.now()
	inst.LastSeen = now
	inst.Lease = Lease{Holder: s.holder, Expiry: now.Add(s.ttl)}
	return s.store.PutInstance(inst)
}

// Report records a cove-reported Activity, renewing (and stealing if necessary)
// the lease — a report means the cove is talking to THIS process now. The only
// phase effect is ActivityDone ⇒ Terminating (then teardown).
func (s *Supervisor) Report(ctx context.Context, actorID string, a Activity) error {
	inst, err := s.recordActivity(actorID, a)
	if err != nil {
		return err
	}
	if inst.Phase == PhaseTerminating {
		return s.Teardown(ctx, actorID)
	}
	return nil
}

// recordActivity is Report's read-modify-write of the instance, under instMu.
func (s *Supervisor) recordActivity(actorID string, a Activity) (Instance, error) {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return Instance{}, fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.Phase == PhaseGone {
		return Instance{}, fmt.Errorf("instance %q is gone", actorID)
	}
	now := s.now()
	enteringWaiting := a == ActivityWaiting && inst.Activity != ActivityWaiting
	// A turn ends when the cove leaves Running for Holding or Waiting (not
	// Holding → Waiting: same turn end).
	turnEnded := inst.Activity == ActivityRunning && (a == ActivityHolding || a == ActivityWaiting)
	turnStarted := a == ActivityRunning && inst.Activity != ActivityRunning // from Holding too
	// holding → running is the same run resuming, not a new one: keep WaitSeq so
	// a reply that landed while holding (not yet woken for) still wakes it.
	enteringRunning := a == ActivityRunning && inst.Activity != ActivityRunning && inst.Activity != ActivityHolding
	inst.Activity = a
	inst.LastSeen = now
	inst.Lease = Lease{Holder: s.holder, Expiry: now.Add(s.ttl)}
	if enteringRunning {
		// The wake-on baseline moves to the log tail when a run starts: the agent
		// reads its inbox itself, so only later replies need a Wake. While Running,
		// the wake-on engine wakes the cove on each later reply and advances the
		// baseline past it (SetWaitSeq).
		inst.WaitSeq = s.tailSeq()
		// A wake (not a resume from Holding) answers an escalate ask.
		inst.EscalationAsked = false
	}
	if turnStarted {
		// Whatever woke it answered this turn end, so the idle deadline is
		// disarmed until the next one.
		inst.IdleDeadline = time.Time{}
		// A mute (PUT /mute) lasts only until the session next runs.
		inst.MessagesMuted = false
		// Fired alarms were answered too: retire one-shots, re-arm the rest.
		var kept []Alarm
		for _, al := range inst.Alarms {
			if al.FiredAt.IsZero() {
				kept = append(kept, al)
			} else if !al.OneShot() {
				al.FiredAt = time.Time{}
				kept = append(kept, al)
			}
		}
		inst.Alarms = kept
	}
	if turnEnded {
		s.armIdleDeadline(&inst, now)
	}
	if enteringWaiting {
		// WaitSeq is deliberately kept: a reply that landed while the cove was
		// Running (an agent holding its episode open for a background task, or
		// mid-turn) and was not yet woken for must still wake it now.
		inst.WaitingSince = now
		inst.EscalationTier = 0
		inst.TierPingedAt = time.Time{}
		inst.LastNagAt = time.Time{} // a new Waiting period restarts the idle ladder
		inst.Nags = 0
	}
	if a == ActivityDone {
		inst.Phase = PhaseTerminating
	}
	return inst, s.store.PutInstance(inst)
}

// SetWaitSeq persists a wake-on baseline (squawk log append Seq) on the
// instance (used by the wake-on engine to detect a new ticket comment). No-op
// semantics if the actor is gone.
func (s *Supervisor) SetWaitSeq(actorID string, seq int64) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.WaitSeq = seq
	return s.store.PutInstance(inst)
}

// armIdleDeadline stamps a turn end on inst and arms its idle deadline from
// the override (a next-scoped one is used up here) or the role's timeout.
func (s *Supervisor) armIdleDeadline(inst *Instance, now time.Time) {
	inst.TurnEndedAt = now
	inst.IdleDeadline = time.Time{}
	var d time.Duration
	if role, ok := s.store.GetRole(inst.Project, inst.Role); ok {
		d = role.TurnEnd.IdleTimeout
	}
	if o := inst.IdleOverride; o != nil {
		d = o.Duration
		if o.Scope == IdleScopeNext {
			inst.IdleOverride = nil
		}
	}
	if d > 0 {
		inst.IdleDeadline = now.Add(d)
	}
}

// SetEndRequested records that the cove asked to end (first request wins).
// Wake-on tears it down once it is Waiting and never wakes it again.
func (s *Supervisor) SetEndRequested(actorID, reason string) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.EndRequested != nil {
		return nil
	}
	inst.EndRequested = &EndRequest{Reason: reason, At: s.now()}
	return s.store.PutInstance(inst)
}

// SetIdleOverride replaces the cove's idle-timeout override; it applies from
// the next turn end (a next-scoped one only to that turn end).
func (s *Supervisor) SetIdleOverride(actorID string, o IdleOverride) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.IdleOverride = &o
	return s.store.PutInstance(inst)
}

// SetMessagesMuted sets whether new messages wake the cove; the mute is
// cleared when its next turn starts.
func (s *Supervisor) SetMessagesMuted(actorID string, on bool) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.MessagesMuted = on
	return s.store.PutInstance(inst)
}

// SetKeptProcesses records how many processes the cove keeps running across
// turns; wake-on doesn't pause a waiting session while any run.
func (s *Supervisor) SetKeptProcesses(actorID string, n int) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.KeptProcesses == n {
		return nil
	}
	inst.KeptProcesses = n
	return s.store.PutInstance(inst)
}

// SetAlarm validates and schedules the named alarm on the cove (replacing one
// of the same name), in its role's time zone.
func (s *Supervisor) SetAlarm(actorID, name, schedule, note, gate string) (Alarm, error) {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	if err := ValidateAlarmName(name); err != nil {
		return Alarm{}, err
	}
	if len(note) > maxAlarmNote {
		return Alarm{}, fmt.Errorf("note must be at most %d bytes", maxAlarmNote)
	}
	if len(gate) > maxGateBytes {
		return Alarm{}, fmt.Errorf("gate must be at most %d bytes", maxGateBytes)
	}
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return Alarm{}, fmt.Errorf("no instance for actor %q", actorID)
	}
	next, err := ParseSchedule(schedule, s.alarmZone(inst), s.now())
	if err != nil {
		return Alarm{}, err
	}
	a := Alarm{Name: name, Schedule: schedule, Note: note, NextAt: next, Gate: gate}
	alarms := slices.Clone(inst.Alarms)
	if i := slices.IndexFunc(alarms, func(x Alarm) bool { return x.Name == name }); i >= 0 {
		alarms[i] = a
	} else if len(alarms) >= MaxAlarms {
		return Alarm{}, ErrAlarmLimit
	} else {
		alarms = append(alarms, a)
	}
	inst.Alarms = alarms
	return a, s.store.PutInstance(inst)
}

// ClearAlarm removes the named alarm (ErrNoSuchAlarm if absent).
func (s *Supervisor) ClearAlarm(actorID, name string) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	i := slices.IndexFunc(inst.Alarms, func(x Alarm) bool { return x.Name == name })
	if i < 0 {
		return ErrNoSuchAlarm
	}
	inst.Alarms = slices.Delete(slices.Clone(inst.Alarms), i, i+1)
	return s.store.PutInstance(inst)
}

// FireAlarms marks the cove's due alarms fired (keeping an earlier FiredAt)
// and moves each cron alarm to its next match after now (no catch-up); a
// one-shot's NextAt goes zero. It returns the alarms after the update. Report
// retires fired alarms when the cove next starts a turn.
func (s *Supervisor) FireAlarms(actorID string, now time.Time) ([]Alarm, error) {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return nil, fmt.Errorf("no instance for actor %q", actorID)
	}
	alarms := slices.Clone(inst.Alarms)
	if inst.Activity != ActivityHolding && inst.Activity != ActivityWaiting {
		return alarms, nil // it started a turn since the caller looked: held
	}
	changed := false
	for i, a := range alarms {
		if a.Gate != "" || a.NextAt.IsZero() || a.NextAt.After(now) {
			continue // a gated alarm fires only on its gate's verdict (ResolveGate)
		}
		if a.FiredAt.IsZero() {
			a.FiredAt, a.FireKind, a.FireDetail = now, WakeAlarm, ""
		}
		a.NextAt = NextAfter(a, s.alarmZone(inst), now)
		alarms[i], changed = a, true
	}
	if !changed {
		return alarms, nil
	}
	inst.Alarms = alarms
	return alarms, s.store.PutInstance(inst)
}

// StartGate records a gate run for the named alarm when it is gated, due, has
// no run in flight, and the cove's turn is over; it reports whether it did
// (the caller then sends RunGate).
func (s *Supervisor) StartGate(actorID, name, runID string, now time.Time) (Alarm, bool, error) {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return Alarm{}, false, fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.Activity != ActivityHolding && inst.Activity != ActivityWaiting {
		return Alarm{}, false, nil
	}
	alarms := slices.Clone(inst.Alarms)
	i := slices.IndexFunc(alarms, func(x Alarm) bool { return x.Name == name })
	if i < 0 {
		return Alarm{}, false, nil
	}
	a := alarms[i]
	if a.Gate == "" || a.GateRun != nil || a.NextAt.IsZero() || a.NextAt.After(now) {
		return a, false, nil
	}
	a.GateRun = &GateRun{RunID: runID, StartedAt: now}
	alarms[i] = a
	inst.Alarms = alarms
	return a, true, s.store.PutInstance(inst)
}

// ResolveGate applies a gate run's outcome to the alarm whose run it is (a
// stale run — the alarm cleared, re-set, or already resolved — is ignored):
// pass fires it with the output, failed fires it as gate-failed with the
// cause, not-yet lets it sleep (a cron alarm to its next match; a one-shot is
// removed).
func (s *Supervisor) ResolveGate(actorID, runID string, o GateOutcome) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	alarms := slices.Clone(inst.Alarms)
	i := slices.IndexFunc(alarms, func(x Alarm) bool { return x.GateRun != nil && x.GateRun.RunID == runID })
	if i < 0 {
		return nil
	}
	a := alarms[i]
	a.GateRun, a.LastGate = nil, &o
	a.NextAt = NextAfter(a, s.alarmZone(inst), o.At)
	switch o.Verdict() {
	case GatePass:
		a.FireKind, a.FireDetail = WakeAlarm, o.Output
	case GateFailed:
		a.FireKind, a.FireDetail = WakeGateFailed, gateFailure(o)
	default: // not yet
		if a.OneShot() {
			if s.log != nil {
				s.log.Info("alarm gate said not yet; one-shot alarm dropped", "id", actorID, "alarm", a.Name, "exit", o.Exit)
			}
			inst.Alarms = slices.Delete(alarms, i, i+1)
			return s.store.PutInstance(inst)
		}
		alarms[i] = a
		inst.Alarms = alarms
		return s.store.PutInstance(inst)
	}
	if a.FiredAt.IsZero() {
		a.FiredAt = o.At
	}
	alarms[i] = a
	inst.Alarms = alarms
	return s.store.PutInstance(inst)
}

// SetReport stamps the session's last accepted ticket report.
func (s *Supervisor) SetReport(actorID string, r TicketReport) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.Report = &r
	return s.store.PutInstance(inst)
}

// alarmZone is the cove's role time zone (UTC when the role is gone).
func (s *Supervisor) alarmZone(inst Instance) *time.Location {
	if role, ok := s.store.GetRole(inst.Project, inst.Role); ok {
		return role.TurnEnd.Location()
	}
	return time.UTC
}

// SetEscalationCategory stamps the cove-declared block category on its
// instance, and marks it as asking for a person (EscalationAsked) until a turn
// next starts.
// Persists until re-declared or teardown (Report does not clear it); the
// escalation engine reads it to pick the tier chain, falling back to the default
// when the category isn't configured. No-op semantics if the actor is gone.
func (s *Supervisor) SetEscalationCategory(actorID, category string) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.EscalationCategory = category
	inst.EscalationAsked = true // asks for a person until it is next woken
	return s.store.PutInstance(inst)
}

// SetMission stamps the agent-declared mission — its end goal or standing
// order (normalised; "" clears it) — on its instance. Errors if the actor has
// no instance.
func (s *Supervisor) SetMission(actorID, mission string) error {
	return s.setStatusLine(actorID, func(inst *Instance) { inst.Mission = NormalizeStatusLine(mission) })
}

// SetFocus stamps the agent-declared focus — what it is on right now
// (normalised; "" clears it) — on its instance. Errors if the actor has no
// instance.
func (s *Supervisor) SetFocus(actorID, focus string) error {
	return s.setStatusLine(actorID, func(inst *Instance) { inst.Focus = NormalizeStatusLine(focus) })
}

// setStatusLine applies set to actorID's instance under the instance lock.
func (s *Supervisor) setStatusLine(actorID string, set func(*Instance)) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	set(&inst)
	return s.store.PutInstance(inst)
}

// SetOpenAsks records the session's count of open asks (the intercom's
// asks hook), counted under the instance lock so two changes at once can't
// leave an older count last. No-op semantics if the actor is gone.
func (s *Supervisor) SetOpenAsks(actorID string, count func() int) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return nil
	}
	n := count()
	if inst.OpenAsks == n {
		return nil
	}
	inst.OpenAsks = n
	return s.store.PutInstance(inst)
}

// SetEscalation persists the escalation engine's per-instance tier state (which
// tier was last pinged, and when). The zero TierPingedAt means "no escalation
// open" — see the escalation engine. No-op semantics if the actor is gone.
func (s *Supervisor) SetEscalation(actorID string, tier int, at time.Time) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.EscalationTier = tier
	inst.TierPingedAt = at
	return s.store.PutInstance(inst)
}

// RecordNag records that the wake-on idle ladder nagged a personal session's
// owner at at: LastNagAt = at, Nags++. Both reset when the cove enters a new
// Waiting period (see Report). Errors if the actor is gone.
func (s *Supervisor) RecordNag(actorID string, at time.Time) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.LastNagAt = at
	inst.Nags++
	return s.store.PutInstance(inst)
}

// KeepWaiting restarts a Waiting personal session's idle period without waking
// it — the owner replied "keep" to a nag: WaitSeq = afterSeq (past the reply,
// so it is not seen again), WaitingSince = at, and the idle ladder resets
// (LastNagAt zero, Nags 0). Phase is untouched: an Idled cove stays paused.
// Errors if the instance is gone or not Waiting.
func (s *Supervisor) KeepWaiting(actorID string, afterSeq int64, at time.Time) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.Phase == PhaseGone {
		return fmt.Errorf("keep waiting: no live instance for %q", actorID)
	}
	if inst.Activity != ActivityWaiting {
		return fmt.Errorf("keep waiting: instance %q is not waiting", actorID)
	}
	inst.WaitSeq = afterSeq
	inst.WaitingSince = at
	inst.LastNagAt = time.Time{}
	inst.Nags = 0
	return s.store.PutInstance(inst)
}

// Idle pauses a live cove (Launcher.Pause — e.g. docker pause) and marks it
// PhaseIdled. A paused cove can't heartbeat or be probed, so Reconcile must
// skip Idled instances (see Reconcile) rather than treating the now-frozen
// lease as an abandoned/dead instance. Ownership: only the supervisor (via the
// wake-on engine calling Idle/Resume) ever pauses a cove — never the
// Launcher/backend directly.
func (s *Supervisor) Idle(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.Phase == PhaseGone {
		return fmt.Errorf("idle: no live instance for %q", actorID)
	}
	// A paused studio can't serve tunnels; one with a tunnel in use is not
	// paused at all. The check and the close are one step in the broker.
	if s.tunnels != nil && !s.tunnels.CloseIfUnused(actorID) {
		return fmt.Errorf("idle %s: %w", actorID, ErrStudioInUse)
	}
	if err := s.launcher.Pause(ctx, inst); err != nil {
		if s.tunnels != nil {
			s.tunnels.Reopen(actorID) // still running
		}
		return err
	}
	if _, ok := s.patchInstance(actorID, func(i *Instance) { i.Phase = PhaseIdled }); !ok {
		return fmt.Errorf("idle: record %q failed", actorID)
	}
	return nil
}

// Resume unpauses a previously Idled cove (Launcher.Unpause) and marks it
// PhaseLive again, resetting WaitingSince to now so the wake-on engine's
// retry-wake window starts fresh. If the role's egress policy changed while the
// cove was paused, it is applied after unpausing and before the cove is Live
// (the wake comes on a later tick, so the agent never runs a turn under the
// stale policy); a failed apply tears the cove down at once and errors.
func (s *Supervisor) Resume(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.Phase == PhaseGone {
		return fmt.Errorf("resume: no live instance for %q", actorID)
	}
	if err := s.launcher.Unpause(ctx, inst); err != nil {
		return err
	}
	if want, ok := s.roleEgress(inst); ok && EgressFingerprint(want) != inst.Egress {
		if err := s.launcher.ApplyEgress(ctx, inst, want); err != nil {
			s.warn("torn down: egress re-apply failed", "id", actorID, "project", inst.Project, "role", inst.Role, "on", "resume", "err", err.Error())
			// Freeze it again first, so a failed teardown leaves the cove paused
			// rather than running under the stale policy until the next pass.
			if pErr := s.launcher.Pause(ctx, inst); pErr != nil {
				s.warn("egress: re-pause before teardown failed", "id", actorID, "err", pErr.Error())
			}
			if tdErr := s.Teardown(ctx, actorID); tdErr != nil {
				return fmt.Errorf("resume %s: egress re-apply: %w (teardown also failed: %v)", actorID, err, tdErr)
			}
			return fmt.Errorf("resume %s: egress re-apply failed, torn down: %w", actorID, err)
		}
		inst.Egress, inst.EgressFailures = EgressFingerprint(want), 0
		s.logEgressApplied(inst, want)
		s.recordEgress(inst, want)
	}
	now := s.now()
	if _, ok := s.patchInstance(actorID, func(i *Instance) {
		i.Egress, i.EgressFailures = inst.Egress, inst.EgressFailures
		i.Phase = PhaseLive
		i.WaitingSince = now
	}); !ok {
		return fmt.Errorf("resume: record %q failed", actorID)
	}
	if s.tunnels != nil {
		s.tunnels.Reopen(actorID)
	}
	return nil
}

// Teardown tears the cove down and deregisters it: Launcher.Teardown, then
// revoke the identity, then remove the Instance. Revoke-before-deregister so a
// failed revoke leaves the Instance in place and the whole teardown is
// retryable — a dangling identity is never left behind silently. Idempotent —
// an absent instance, or an already-revoked identity, is a no-op.
func (s *Supervisor) Teardown(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return nil
	}
	if s.sink != nil {
		s.sink.RequestTeardown(actorID) // best-effort cooperative nudge
	}
	if s.tunnels != nil {
		s.tunnels.CloseAll(actorID)
	}
	if inst.Phase != PhaseTerminating && inst.Phase != PhaseLost {
		inst.Phase = PhaseTerminating
		s.patchInstance(actorID, func(i *Instance) {
			if i.Phase != PhaseTerminating && i.Phase != PhaseLost {
				i.Phase = PhaseTerminating
			}
		})
	}
	if err := s.launcher.Teardown(ctx, inst); err != nil {
		return fmt.Errorf("teardown launcher: %w", err)
	}
	if err := s.revokeActor(actorID); err != nil {
		return fmt.Errorf("teardown revoke identity: %w", err)
	}
	if err := s.store.RemoveInstance(actorID); err != nil {
		return err
	}
	if s.tunnels != nil {
		s.tunnels.Reopen(actorID) // forget the gone studio's closed mark
	}
	s.notifyExposures(actorID) // instance gone → the sink forgets its port set
	if err := s.store.RemoveShareGrantsOf(actorID); err != nil && s.log != nil {
		s.log.Warn("teardown: removing the session's share grants failed (non-fatal)", "id", actorID, "err", err.Error())
	}
	if s.channels != nil {
		if err := s.channels.Ended(inst); err != nil && s.log != nil {
			s.log.Warn("teardown: leaving the session's channels failed (non-fatal)", "id", actorID, "err", err.Error())
		}
	}
	if s.released != nil {
		if err := s.released.RecordRelease(ctx, inst.Project, inst.Role, actorID); err != nil && s.log != nil {
			s.log.Warn("teardown: record release failed (shadow, non-fatal)", "id", actorID, "err", err.Error())
		}
	}
	if s.log != nil {
		s.log.Info("cove torn down", "id", actorID)
	}
	return nil
}

// PurgeState deletes actorID's persisted state via the launcher's
// StateKeeper; a launcher without one has none. State still in use (a live
// container) errors: the caller retries later.
func (s *Supervisor) PurgeState(ctx context.Context, actorID string) error {
	k, ok := s.launcher.(StateKeeper)
	if !ok {
		return nil
	}
	return k.PurgeState(ctx, actorID)
}

// StateOwners lists the actor ids with persisted state (none without a
// StateKeeper launcher).
func (s *Supervisor) StateOwners(ctx context.Context) ([]string, error) {
	k, ok := s.launcher.(StateKeeper)
	if !ok {
		return nil, nil
	}
	return k.StateOwners(ctx)
}

// SetStandingResetter wires the standing reconciler in as the reset path for
// the admin API and UI (jam.ResetStanding). Call before serving.
func (s *Supervisor) SetStandingResetter(r StandingResetter) { s.resetter = r }

// SetStandingUpgrader wires the standing reconciler in as the upgrade queue for
// the admin API and UI (jam.UpgradeStanding). Call before serving.
func (s *Supervisor) SetStandingUpgrader(u StandingUpgrader) { s.upgrader = u }

// StandingUpgradeState is the declared standing session's pending upgrade
// state ("" none, or no upgrader wired). Nil-safe: the UI holds a possibly-nil
// *Supervisor as its ImageResolver.
func (s *Supervisor) StandingUpgradeState(project, role, name string) string {
	if s == nil || s.upgrader == nil {
		return ""
	}
	return s.upgrader.UpgradeState(project, role, name)
}

// PrepareImage makes the image a raise for project/role would run now ready,
// via the launcher's PrepareKit (idempotent: an image already present returns
// at once; otherwise it builds, which may take minutes — callers must not hold
// a lock across it). A role that raises no kit is ready as is. manager names
// the manager to prepare on (a PlacingLauncher's PrepareKitOn); "" — or a
// manager no longer configured — is the launcher's default. The returned CurrentImage identifies what was prepared
// (its Key and Tag match CurrentImage's for the same manager).
func (s *Supervisor) PrepareImage(ctx context.Context, project, role, manager string) (CurrentImage, KitStatus, error) {
	r, ok := s.store.GetRole(project, role)
	if !ok {
		return CurrentImage{}, KitStatus{}, fmt.Errorf("role %s/%s not found", orDefaultProject(project), role)
	}
	def, have, err := s.raiseKit(project, r)
	if err != nil {
		return CurrentImage{}, KitStatus{}, err
	}
	if !have {
		return CurrentImage{}, KitStatus{State: KitReady}, nil
	}
	manager = s.imageManager(manager)
	cur := CurrentImage{Kit: def.Ref, HasKit: true, Tag: s.imageTagOn(manager, def.Ref)}
	var st KitStatus
	if pl, ok := s.launcher.(PlacingLauncher); ok && manager != "" {
		st, err = pl.PrepareKitOn(ctx, manager, def)
	} else {
		st, err = s.launcher.PrepareKit(ctx, def)
	}
	return cur, st, err
}

// Reconcile is the self-healing + restart-re-adoption pass. For each non-Gone
// Instance: renew our own unexpired lease; for an expired lease, Probe the cove —
// dead (or probe error) ⇒ declare Lost and tear down; alive ⇒ steal + renew
// (adopt); unknown ⇒ leave for a later tick. Run once at startup (re-adopting
// instances a crashed/old process left behind) and on every tick.
//
// The pass iterates one ListInstances snapshot but does slow work (Probe, egress
// exec) between instances, so each write is a patchInstance on a fresh read that
// touches only the field the pass owns — never the snapshot, which would clobber
// a connector report, activity, or heartbeat that landed meanwhile.
func (s *Supervisor) Reconcile(ctx context.Context) error {
	now := s.now()
	for _, inst := range s.store.ListInstances() {
		if inst.Phase == PhaseGone {
			continue
		}
		if inst.Phase == PhaseIdled {
			// Paused on purpose: the wake-on engine owns its lifecycle
			// (resume/teardown), unless its manager is gone.
			if s.ownsDecision(inst, now) {
				s.reapIfManagerGone(ctx, inst)
			}
			continue
		}
		if inst.Phase == PhaseTerminating || inst.Phase == PhaseLost {
			// An instance already mid-teardown (Done reported, or reconciler-
			// declared Lost) must be finished, never renewed or adopted. Resume
			// teardown idempotently regardless of lease state.
			if err := s.Teardown(ctx, inst.ActorID); err != nil && s.log != nil {
				s.log.Warn("reconcile resume-teardown failed", "id", inst.ActorID, "err", err.Error())
			}
			continue
		}
		if inst.Lease.Expiry.After(now) {
			if inst.Lease.Holder == s.holder {
				if s.reapIfManagerGone(ctx, inst) {
					continue
				}
				// Renew our own lease. Only the lease holder re-applies egress, so
				// two Jams never both exec in.
				if cur, ok := s.patchInstance(inst.ActorID, func(c *Instance) { c.Lease.Expiry = now.Add(s.ttl) }); ok {
					s.reconcileEgress(ctx, cur)
				}
			}
			continue // someone else's live lease: not ours to touch
		}
		live, err := s.launcher.Probe(ctx, inst)
		if err != nil || live == LivenessDead {
			s.patchInstance(inst.ActorID, func(c *Instance) { c.Phase = PhaseLost })
			if derr := s.Teardown(ctx, inst.ActorID); derr != nil && s.log != nil {
				s.log.Warn("reconcile teardown failed", "id", inst.ActorID, "err", derr.Error())
			}
			continue
		}
		if live == LivenessAlive {
			s.patchInstance(inst.ActorID, func(c *Instance) { c.Lease = Lease{Holder: s.holder, Expiry: now.Add(s.ttl)} }) // steal + renew
		}
		// LivenessUnknown: leave for the next tick.
	}
	return nil
}

// ownsDecision reports whether this Jam may act on inst: it holds the lease,
// or the lease has expired. Another holder's live lease is not ours to touch.
func (s *Supervisor) ownsDecision(inst Instance, now time.Time) bool {
	return !inst.Lease.Expiry.After(now) || inst.Lease.Holder == s.holder
}

// reapIfManagerGone declares a Live or Idled inst Lost and tears it down when
// it is recorded on a named manager the launcher no longer has (only a
// PlacingLauncher knows). A raise that succeeded after its manager was
// force-revoked (ForgetManager ran first) leaves such a record, which no
// manager will ever probe and whose own lease would be renewed forever.
// Reports whether it reaped.
func (s *Supervisor) reapIfManagerGone(ctx context.Context, inst Instance) bool {
	name := cmp.Or(inst.Manager, LocalManager) // legacy "" records were the old local manager
	pl, ok := s.launcher.(PlacingLauncher)
	if !ok || pl.HasManager(name) {
		return false
	}
	cur, ok := s.patchInstance(inst.ActorID, func(c *Instance) {
		if cmp.Or(c.Manager, LocalManager) == name && (c.Phase == PhaseLive || c.Phase == PhaseIdled) {
			c.Phase = PhaseLost
		}
	})
	if !ok || cur.Phase != PhaseLost {
		return false
	}
	if err := s.Teardown(ctx, inst.ActorID); err != nil && s.log != nil {
		s.log.Warn("reconcile teardown of studio on a removed manager failed", "id", inst.ActorID, "manager", name, "err", err.Error())
	}
	return true
}

// egressMaxFailures is how many consecutive failed egress re-applies a Live cove
// survives before it is torn down: a short grace for a transient exec or squid
// reload failure, never an indefinite run under a policy other than its role's.
const egressMaxFailures = 3

// roleEgress is the egress policy inst's role currently wants (a copy); nil is
// the kit default. ok=false when the role no longer exists: callers then leave
// the cove's egress as is, since the kit default could be wider.
func (s *Supervisor) roleEgress(inst Instance) (*EgressPolicy, bool) {
	role, ok := s.store.GetRole(inst.Project, inst.Role)
	if !ok {
		return nil, false
	}
	if role.Scope.Egress == nil {
		return nil, true
	}
	return &EgressPolicy{Domains: slices.Clone(role.Scope.Egress.Domains)}, true
}

// reconcileEgress re-applies inst's role egress policy when it has drifted from
// the one the cove is running under (inst.Egress). Success records the new
// fingerprint; an unavailable manager is retried next pass without counting;
// any other failure is counted and retried next pass, and the
// egressMaxFailures-th consecutive one tears the cove down (fail closed — a
// standing session is raised again under the new policy by its reconciler).
//
// The exec can take a while, so the outcome is patched onto a fresh read of the
// instance rather than written from inst, which would clobber a report or
// heartbeat that landed meanwhile.
func (s *Supervisor) reconcileEgress(ctx context.Context, inst Instance) {
	want, ok := s.roleEgress(inst)
	if !ok {
		return // role gone: leave egress as is (the kit default could be wider)
	}
	fp := EgressFingerprint(want)
	if fp == inst.Egress {
		if inst.EgressFailures != 0 { // the role changed back before a re-apply landed
			s.patchEgress(inst.ActorID, func(cur *Instance) { cur.EgressFailures = 0 })
		}
		return
	}
	err := s.launcher.ApplyEgress(ctx, inst, want)
	if err == nil {
		s.patchEgress(inst.ActorID, func(cur *Instance) { cur.Egress, cur.EgressFailures = fp, 0 })
		s.logEgressApplied(inst, want)
		s.recordEgress(inst, want)
		return
	}
	if errors.Is(err, ErrManagerUnavailable) {
		// The manager is away, which says nothing about the policy: the stale
		// fingerprint makes the first pass after it reconnects re-apply.
		if s.log != nil {
			s.log.Debug("egress re-apply deferred: manager unavailable", "id", inst.ActorID, "manager", inst.Manager)
		}
		return
	}
	failures := 0
	counted := s.patchEgress(inst.ActorID, func(cur *Instance) {
		cur.EgressFailures++
		failures = cur.EgressFailures
	})
	if !counted {
		return // torn down or paused meanwhile: the exec failing says nothing about the policy
	}
	s.warn("egress re-apply failed", "id", inst.ActorID, "project", inst.Project, "role", inst.Role, "failures", failures, "err", err.Error())
	if failures < egressMaxFailures {
		return
	}
	s.warn("torn down: egress re-apply failed", "id", inst.ActorID, "project", inst.Project, "role", inst.Role, "failures", failures)
	if tdErr := s.Teardown(ctx, inst.ActorID); tdErr != nil {
		s.warn("egress teardown failed", "id", inst.ActorID, "err", tdErr.Error())
	}
}

// patchInstance applies fn to a fresh read of the instance and writes it back,
// returning the written instance. An instance gone meanwhile is left alone.
func (s *Supervisor) patchInstance(actorID string, fn func(*Instance)) (Instance, bool) {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	cur, ok := s.store.GetInstance(actorID)
	if !ok || cur.Phase == PhaseGone {
		return Instance{}, false
	}
	fn(&cur)
	if err := s.store.PutInstance(cur); err != nil {
		s.warn("reconcile: record instance failed", "id", actorID, "err", err.Error())
		return Instance{}, false
	}
	return cur, true
}

// patchEgress applies fn to a fresh read of the instance and writes it back,
// only while it is still Live (a torn-down or paused cove is left alone; Resume
// re-checks a paused one). It reports whether it wrote.
func (s *Supervisor) patchEgress(actorID string, fn func(*Instance)) bool {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	cur, ok := s.store.GetInstance(actorID)
	if !ok || cur.Phase != PhaseLive {
		return false
	}
	fn(&cur)
	if err := s.store.PutInstance(cur); err != nil {
		s.warn("egress: record instance failed", "id", actorID, "err", err.Error())
		return false
	}
	return true
}

// logEgressApplied logs a landed re-apply: the policy's kind and domain count,
// never the list.
func (s *Supervisor) logEgressApplied(inst Instance, p *EgressPolicy) {
	if s.log == nil {
		return
	}
	if p == nil {
		s.log.Info("egress re-applied", "id", inst.ActorID, "project", inst.Project, "role", inst.Role, "policy", "kit")
		return
	}
	s.log.Info("egress re-applied", "id", inst.ActorID, "project", inst.Project, "role", inst.Role, "policy", "role", "domains", len(p.Domains))
}

func (s *Supervisor) warn(msg string, args ...any) {
	if s.log != nil {
		s.log.Warn(msg, args...)
	}
}

// Run drives the reconciler: one startup pass (restart re-adoption) then a tick
// every reconcile interval until ctx is cancelled. reconcile MUST be < ttl so a
// live owner renews before its own lease expires (enforced by serve config).
func (s *Supervisor) Run(ctx context.Context) {
	_ = s.Reconcile(ctx)
	t := time.NewTicker(s.reconcile)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Reconcile(ctx)
		}
	}
}

// revokeActor removes the identity if present. Presence is checked BEFORE the
// mutation: if the actor is already absent it is a no-op success (a retry after a
// prior partial teardown); if it is present, any RemoveActor error is a real
// failure the caller must surface (so teardown is retryable). Checking after the
// call would be wrong — a failed removal could look like "already absent".
func (s *Supervisor) revokeActor(actorID string) error {
	present := false
	for _, a := range s.store.ListActors() {
		if a.ID == actorID {
			present = true
			break
		}
	}
	if !present {
		return nil
	}
	return s.store.RemoveActor(actorID)
}

// compileContext compiles a session's context bundle from the current config:
// Jam boilerplate for its kind, kit, studio, project, role and Jam layers. Raise
// and ContextFor share it, so a refresh matches what a raise would deliver.
//
// spec.Kit is the kit image the cove runs (its build-args and egress ceiling);
// promptKit, when it names the same kit, supplies the prompt and notes — a
// newer version's text edits reach a running session, its image changes don't.
// raised, when it is spec.Kit's definition (Raise resolved it), is used as is
// rather than re-read from the registry. Otherwise (a refresh) imageChain, when
// recorded, is the chain the image was built from: its build-args and egress
// come from those exact link versions, so a following parent's later edit does
// not reach them (its prompt and notes do, via the current-chain resolution).
func (s *Supervisor) compileContext(spec RaiseSpec, actor Actor, promptKit KitRef, raised *KitDefinition, imageChain []KitRef) sessionctx.Bundle {
	// Compile the session context (Jam boilerplate → kit; later slices add
	// studio, project, role, jam). The prompt stays the launch text alone.
	in := sessionctx.Inputs{Session: sessionctx.SessionFacts{
		Kind: spec.SessionKind, Name: spec.Name, Project: ProjectName(s.store, spec.Project), Role: spec.Role, Owner: spec.Owner, Unit: spec.Unit,
		Previews: s.previews, TurnProtocol: spec.turnProtocol,
	}}
	var kitEgress []string
	var warnings []string
	haveKit := false
	if spec.Kit.ID != "" {
		in.Session.Kit = spec.Kit.String()
		if spec.draft != nil {
			in.Session.Kit = spec.draft.String()
		}
		def, ok := KitDefinition{}, false
		if raised != nil && raised.Ref == spec.Kit {
			def, ok = *raised, true
		} else if d, found, derr := ResolveKitDefinition(s.store, spec.Kit, harnessinstall.Default()); derr != nil {
			warnings = append(warnings, fmt.Sprintf("kit layer omitted: %v", derr))
		} else if found {
			def, ok = d, true // only the kit text is read: the harness does not matter
		}
		if ok {
			text := def.Kit
			if promptKit.ID == spec.Kit.ID && promptKit.Version != spec.Kit.Version {
				if cur, ok, cerr := ResolveKitDefinition(s.store, promptKit, harnessinstall.Default()); cerr == nil && ok {
					text = cur.Kit
				}
			}
			build, buildOK := def.Kit, true
			if raised == nil && len(imageChain) > 0 {
				var berr error
				if build, berr = recordedChainKit(s.store, imageChain); berr != nil {
					buildOK = false // fail closed: no tool list, egress unknown
					warnings = append(warnings, fmt.Sprintf("kit build facts omitted: %v", berr))
				}
			}
			if buildOK {
				in.Kit = sessionctx.KitLayer(text.Prompt, text.Leaves(), build.BuildArgs)
				kitEgress, haveKit = build.Egress, true
			} else {
				in.Kit = sessionctx.KitLayer(text.Prompt, text.Leaves(), nil)
			}
		}
	}
	in.Studio = studioFacts(s.store, s.trackerOf(), actor, spec.Owner, spec.Egress, kitEgress, haveKit, s.now())
	if role, ok := s.store.GetRole(spec.Project, spec.Role); ok {
		in.Role = role.Context
	}
	if p, ok := s.store.GetProject(orDefaultProject(spec.Project)); ok {
		in.Project = sessionctx.ProjectLayer(p.Context, p.Resources)
	}
	in.Jam = s.store.GetJamContext()
	b := sessionctx.Compile(in)
	b.Warnings = append(b.Warnings, warnings...)
	return b
}

// ContextFor recompiles the session context of a running instance from the
// current config — its role's kit and egress as a raise would resolve them now.
func (s *Supervisor) ContextFor(actorID string) (sessionctx.Bundle, error) {
	for _, a := range s.store.ListActors() {
		if a.ID == actorID {
			return s.ContextForActor(a)
		}
	}
	return sessionctx.Bundle{}, ErrNoInstance
}

// ContextForActor is ContextFor for an already-authenticated actor (the
// GET /context handler has it from the token lookup).
// It compiles for the turn protocol the cove last fetched its context with.
func (s *Supervisor) ContextForActor(actor Actor) (sessionctx.Bundle, error) {
	inst, ok := s.store.GetInstance(actor.ID)
	if !ok {
		return sessionctx.Bundle{}, ErrNoInstance
	}
	return s.ContextForTurns(actor, inst.TurnProtocol)
}

// SetTurnProtocol records the turn protocol the cove fetched its context with
// (GET /context), so later recompiles (setup inspection) match what it was
// served.
func (s *Supervisor) SetTurnProtocol(actorID string, p int) error {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return ErrNoInstance
	}
	if inst.TurnProtocol == p {
		return nil
	}
	inst.TurnProtocol = p
	return s.store.PutInstance(inst)
}

// ContextForTurns is ContextForActor for a cove that speaks turnProtocol
// (sessionctx.SessionFacts.TurnProtocol; GET /context?turn-protocol=).
func (s *Supervisor) ContextForTurns(actor Actor, turnProtocol int) (sessionctx.Bundle, error) {
	actorID := actor.ID
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return sessionctx.Bundle{}, ErrNoInstance
	}
	// The cove runs the kit image it was raised with (inst.Kit); only the
	// current version's prompt and notes are picked up.
	spec := RaiseSpec{ActorID: actorID, Project: inst.Project, Role: inst.Role, Unit: inst.Unit, Owner: inst.Owner, OwnerID: inst.OwnerID, Name: inst.Name, SessionKind: inst.SessionKind, Kit: inst.Kit, turnProtocol: turnProtocol}
	promptKit := inst.Kit
	if inst.KitDraft != nil {
		spec.KitDraft, spec.draft = inst.KitDraft.Kit, inst.KitDraft
	}
	if role, ok := s.store.GetRole(inst.Project, inst.Role); ok {
		if role.Scope.Egress != nil {
			spec.Egress = &EgressPolicy{Domains: slices.Clone(role.Scope.Egress.Domains)}
		}
		// Only the current version's id/version matter here, not its image
		// key. A draft studio keeps its draft's prompt: never the role's kit.
		if inst.KitDraft == nil {
			if d, have, err := s.kitFor(role, harnessinstall.Default()); err == nil && have {
				ref := d.Ref
				promptKit = ref
				if spec.Kit.ID == "" { // raised before instances recorded their kit
					spec.Kit = ref
				}
			}
		}
	}
	if inst.KitDraft != nil {
		// Recompiled from the draft tree as of the raise and the recorded
		// parents (draftStudioKit), as the raise compiled it.
		sk, err := draftStudioKit(s.store, inst)
		if err != nil {
			spec.Kit = KitRef{}
			b := s.compileContext(spec, actor, KitRef{}, nil, nil)
			b.Warnings = append(b.Warnings, fmt.Sprintf("kit layer omitted: %v", err))
			return b, nil
		}
		def := KitDefinition{Ref: inst.Kit, Kit: sk}
		return s.compileContext(spec, actor, inst.Kit, &def, nil), nil
	}
	return s.compileContext(spec, actor, promptKit, nil, inst.KitChain), nil
}

// ErrNoInstance means an identity has no registered instance (e.g. a
// hand-enrolled actor), so there is no session to compile context for.
var ErrNoInstance = errors.New("no registered instance for this identity")

// RoleImage is the image a raise for a role would run on one manager.
type RoleImage struct {
	Manager string `json:"manager"`
	Kit     KitRef `json:"kit"`
	HasKit  bool   `json:"has_kit"`
	Tag     string `json:"tag"`
}

// RoleImages is the current image of project/role on each connected manager, in
// the launcher's candidate order. Without a manager router it is one row with
// Manager "".
func (s *Supervisor) RoleImages(project, role string) ([]RoleImage, error) {
	if s == nil {
		return nil, fmt.Errorf("no supervisor")
	}
	names := []string{""}
	if a, ok := s.launcher.(interface{ AvailableNames() []string }); ok {
		names = a.AvailableNames()
	}
	out := make([]RoleImage, 0, len(names))
	for _, n := range names {
		cur, err := s.CurrentImage(project, role, n)
		if err != nil {
			return nil, err
		}
		out = append(out, RoleImage{Manager: n, Kit: cur.Kit, HasKit: cur.HasKit, Tag: cur.Tag})
	}
	return out, nil
}
