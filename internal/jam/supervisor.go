package jam

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

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
	Prompt  string // workload prompt for the raised cove's agent; consumed by the launcher, not persisted
	// Owner is the owning roster Human's name for a personal session; "" otherwise.
	Owner string
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
	// cove-kit:<build-digest>-<asm> image. A zero KitRef (empty ID) selects the legacy
	// path that raises the launcher's statically configured image, so existing
	// callers are unaffected (Phase-1 additive).
	Kit KitRef
	// Connector is the client env/git the raised cove sets up to use its role's
	// destinations (ConnectorFor). Supervisor.Raise always fills it; nil (a
	// caller bypassing the supervisor) = the legacy built-in contract.
	Connector *snippet.Connector
	// Context is the compiled session context (sessionctx.Compile): Jam
	// boilerplate for the session kind, then the kit layer. Supervisor.Raise
	// always sets it; the launcher stages it for cove-master, which delivers
	// its core as an appended system prompt every turn.
	Context *sessionctx.Bundle
}

// LaunchCreds carries the per-instance credentials the supervisor mints and the
// launcher must inject into the cove (identity token + launch secret). Passed to
// Raise so the launcher can bootstrap cove-master without the supervisor leaking
// them elsewhere.
type LaunchCreds struct {
	IdentityToken string
	LaunchSecret  string
}

// Launcher is the seam over "actually start/stop/probe a cove on a backend". The
// supervisor depends only on this, so it stays kit-free and grpc-free and fully
// hermetic. The real backend+kit implementation is a later slice, wired from
// cmd/at-jam.
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

// ControlSink pushes lifecycle control to a connected cove (implemented by the
// Attach server). Best-effort and non-blocking; no connected stream is a no-op.
// nil when no stream server runs (slice-1 behavior).
type ControlSink interface {
	RequestTeardown(actorID string)
	Wake(actorID string)
}

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
	store     Store
	launcher  Launcher
	holder    string // this process's lease-holder id
	ttl       time.Duration
	reconcile time.Duration
	now       func() time.Time
	log       *slog.Logger
	sink      ControlSink
	tail      tailReader
	released  Releaser
	// defaultStudioKit is the studio kit a raise runs from when its role names no
	// kit (its light reference). nil leaves such a raise with no kit (hermetic
	// tests, no kit wiring). Set once at wiring via SetDefaultStudioKit; the full
	// definition is resolved from the registry only on an ErrKitNotReady miss.
	defaultStudioKit *KitRef
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

// SetTailReader wires the squawk log tail source used to baseline WaitSeq on
// entering Waiting (and CommitSeq at Raise). Called once at wiring time before
// serving begins; nil (no message log) leaves both 0.
func (s *Supervisor) SetTailReader(r tailReader) { s.tail = r }

// SetReleaser wires the allocation releaser recorded on teardown (the
// actual-state-out seam). Called once at wiring time; nil (no Postgres ledger)
// leaves teardown recording nothing, exactly as before.
func (s *Supervisor) SetReleaser(r Releaser) { s.released = r }

// SetDefaultStudioKit wires the studio kit a raise runs from when its role names
// no kit — its light reference, recorded in the registry by EnsureDefaultStudioKit
// at wiring. With it set, a role without a kit stamps this ref on the launch spec
// and, if the launcher reports ErrKitNotReady, resolves the definition from the
// registry and PrepareKits it before retrying. Unset (the default) leaves an
// unnamed-kit raise with no kit so hermetic tests and un-wired setups are
// unaffected.
func (s *Supervisor) SetDefaultStudioKit(ref KitRef) { s.defaultStudioKit = &ref }

func (s *Supervisor) tailSeq() int64 {
	if s.tail == nil {
		return 0
	}
	seq, _ := s.tail.TailSeq()
	return seq
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
	// A personal session's cove may message its owner and nobody else: the
	// override REPLACES the role's addressing (least privilege).
	var ov *Override
	if spec.Owner != "" {
		ov = &Override{Addressing: []string{"human:" + spec.Owner}}
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
	// by setting the spec.
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
	if roleOK {
		ref, have, kerr := s.kitRefFor(role)
		if kerr != nil {
			if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil {
				s.log.Warn("raise rollback: failed to revoke identity after kit resolve failure", "id", spec.ActorID, "error", rmErr)
			}
			return Instance{}, "", "", fmt.Errorf("raise: resolve kit for role %s/%s: %w", orDefaultProject(spec.Project), spec.Role, kerr)
		}
		if have {
			spec.Kit = ref
		}
	}
	// Compile the session context (Jam boilerplate → kit; later slices add
	// studio, project, role, jam). The prompt stays the launch text alone.
	in := sessionctx.Inputs{Session: sessionctx.SessionFacts{
		Kind: spec.SessionKind, Name: spec.Name, Project: orDefaultProject(spec.Project), Role: spec.Role, Owner: spec.Owner, Unit: spec.Unit,
	}}
	var kitEgress []string
	haveKit := false
	if spec.Kit.ID != "" {
		in.Session.Kit = spec.Kit.String()
		if def, ok, derr := ResolveKitDefinition(s.store, spec.Kit); derr == nil && ok {
			in.Kit = sessionctx.Layer{Core: def.Kit.Prompt}
			kitEgress, haveKit = def.Kit.Egress, true
		}
	}
	in.Studio = studioFacts(s.store, actor, spec.Owner, spec.Egress, kitEgress, haveKit, s.now())
	if roleOK {
		in.Role = role.Context
	}
	if p, ok := s.store.GetProject(orDefaultProject(spec.Project)); ok {
		in.Project = sessionctx.ProjectLayer(p.Context, p.Resources)
	}
	in.Jam = s.store.GetJamContext()
	bundle := sessionctx.Compile(in)
	for _, w := range bundle.Warnings {
		if s.log != nil {
			s.log.Warn("raise: session context", "id", spec.ActorID, "warning", w)
		}
	}
	spec.Context = &bundle
	creds := LaunchCreds{IdentityToken: tok, LaunchSecret: secret}
	loc, err := s.launcher.Raise(ctx, spec, creds)
	if errors.Is(err, ErrKitNotReady) {
		// The launcher lacks this kit: send it the full definition (from the
		// registry) and retry once. A still-preparing build does not block — the
		// raise fails and the caller's reconcile retries on a later tick.
		loc, err = s.prepareKitAndRetry(ctx, spec, creds)
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
		Owner: spec.Owner, Name: spec.Name, SessionKind: spec.SessionKind,
		Location: loc, Phase: PhaseLive, Activity: ActivityRunning,
		Lease:            Lease{Holder: s.holder, Expiry: now.Add(s.ttl)},
		LaunchSecretHash: HashToken(secret),
		RaisedAt:         now, LastSeen: now,
		WaitSeq:   s.tailSeq(), // wake-on baseline: the cove starts Running and reads its inbox itself
		CommitSeq: s.tailSeq(), // CommitCursor stays "" — the cove has read nothing yet, this is an ordering baseline, not an echoable id
		Egress:    EgressFingerprint(spec.Egress),
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
	if s.log != nil {
		s.log.Info("cove raised", "id", spec.ActorID, "project", inst.Project, "role", spec.Role, "phase", string(inst.Phase))
	}
	return inst, tok, secret, nil
}

// kitRefFor returns the studio kit reference a raise for role should carry. A
// role that names a kit (role.Kit) resolves that registered studio kit's current
// version (fail closed if the kit is absent, not tag-safe, or not a studio kit);
// an unnamed kit falls back to the wiring-set default studio ref. ok=false (no
// default set) leaves the raise with no kit, so un-wired setups and hermetic
// tests are unaffected.
func (s *Supervisor) kitRefFor(role Role) (KitRef, bool, error) {
	if role.Kit != "" {
		ref, err := StudioKitRef(s.store, role.Kit)
		if err != nil {
			return KitRef{}, false, err
		}
		return ref, true, nil
	}
	if s.defaultStudioKit != nil {
		return *s.defaultStudioKit, true, nil
	}
	return KitRef{}, false, nil
}

// prepareKitAndRetry handles a Raise that returned ErrKitNotReady: it resolves
// the full kit definition from the registry, asks the launcher to prepare it, and
// retries the Raise once when the kit is ready. A KitPreparing status (an async
// remote build) returns an error WITHOUT blocking — the caller's reconcile loop
// retries the raise on a later tick, by which point the build may be done. The
// caller (Raise) rolls back the enrollment on any error this returns.
func (s *Supervisor) prepareKitAndRetry(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (string, error) {
	def, ok, err := ResolveKitDefinition(s.store, spec.Kit)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("kit %s not in registry", spec.Kit)
	}
	status, err := s.launcher.PrepareKit(ctx, def)
	if err != nil {
		return "", fmt.Errorf("prepare kit %s: %w", spec.Kit, err)
	}
	if status.State != KitReady {
		if s.log != nil {
			s.log.Info("kit preparing; deferring raise to reconcile", "id", spec.ActorID, "kit", spec.Kit.String())
		}
		return "", fmt.Errorf("kit %s is preparing; deferring", spec.Kit)
	}
	if s.log != nil {
		s.log.Info("kit prepared; retrying raise", "id", spec.ActorID, "kit", spec.Kit.String())
	}
	return s.launcher.Raise(ctx, spec, creds)
}

// RecordConnector records the connector fingerprint a cove reports having
// applied to its latest agent spawn. Staleness is derived on read
// (CoveSummaries), never stored, so it cannot itself drift.
func (s *Supervisor) RecordConnector(actorID, fp string) error {
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
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.Phase == PhaseGone {
		return fmt.Errorf("instance %q is gone", actorID)
	}
	now := s.now()
	enteringWaiting := a == ActivityWaiting && inst.Activity != ActivityWaiting
	enteringRunning := a == ActivityRunning && inst.Activity != ActivityRunning
	inst.Activity = a
	inst.LastSeen = now
	inst.Lease = Lease{Holder: s.holder, Expiry: now.Add(s.ttl)}
	if enteringRunning {
		// The wake-on baseline moves to the log tail when a run starts: the agent
		// reads its inbox itself, so only later replies need a Wake. While Running,
		// the wake-on engine wakes the cove on each later reply and advances the
		// baseline past it (SetWaitSeq).
		inst.WaitSeq = s.tailSeq()
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
	if err := s.store.PutInstance(inst); err != nil {
		return err
	}
	if inst.Phase == PhaseTerminating {
		return s.Teardown(ctx, actorID)
	}
	return nil
}

// SetWaitSeq persists a wake-on baseline (squawk log append Seq) on the
// instance (used by the wake-on engine to detect a new ticket comment). No-op
// semantics if the actor is gone.
func (s *Supervisor) SetWaitSeq(actorID string, seq int64) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.WaitSeq = seq
	return s.store.PutInstance(inst)
}

// SetEscalationCategory stamps the cove-declared block category on its instance.
// Persists until re-declared or teardown (Report does not clear it); the
// escalation engine reads it to pick the tier chain, falling back to the default
// when the category isn't configured. No-op semantics if the actor is gone.
func (s *Supervisor) SetEscalationCategory(actorID, category string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.EscalationCategory = category
	return s.store.PutInstance(inst)
}

// SetEscalation persists the escalation engine's per-instance tier state (which
// tier was last pinged, and when). The zero TierPingedAt means "no escalation
// open" — see the escalation engine. No-op semantics if the actor is gone.
func (s *Supervisor) SetEscalation(actorID string, tier int, at time.Time) error {
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
	if err := s.launcher.Pause(ctx, inst); err != nil {
		return err
	}
	inst.Phase = PhaseIdled
	return s.store.PutInstance(inst)
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
	}
	inst.Phase = PhaseLive
	inst.WaitingSince = s.now()
	return s.store.PutInstance(inst)
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
	if inst.Phase != PhaseTerminating && inst.Phase != PhaseLost {
		inst.Phase = PhaseTerminating
		_ = s.store.PutInstance(inst)
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
			continue // paused on purpose; the wake-on engine owns its lifecycle (resume/teardown)
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
// fingerprint; a failure is counted and retried next pass, and the
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
