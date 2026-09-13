package harbor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"
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
// cmd/at-harbor.
type Launcher interface {
	Raise(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (location string, err error)
	Teardown(ctx context.Context, inst Instance) error
	Probe(ctx context.Context, inst Instance) (Liveness, error)
	Pause(ctx context.Context, inst Instance) error
	Unpause(ctx context.Context, inst Instance) error
}

// ControlSink pushes lifecycle control to a connected cove (implemented by the
// Attach server). Best-effort and non-blocking; no connected stream is a no-op.
// nil when no stream server runs (slice-1 behavior).
type ControlSink interface {
	RequestTeardown(actorID string)
	Wake(actorID string)
}

// Supervisor owns the managed-cove lifecycle: the durable registry (via Store),
// the lease model, and the state machine. One supervisor per harbor process.
type Supervisor struct {
	store     Store
	launcher  Launcher
	holder    string // this process's lease-holder id
	ttl       time.Duration
	reconcile time.Duration
	now       func() time.Time
	log       *slog.Logger
	sink      ControlSink
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
	tok, err := Enroll(s.store, spec.ActorID, spec.Project, spec.Role, nil, s.now())
	if err != nil {
		return Instance{}, "", "", err
	}
	secret, err := MintToken()
	if err != nil {
		_ = s.store.RemoveActor(spec.ActorID)
		return Instance{}, "", "", err
	}
	loc, err := s.launcher.Raise(ctx, spec, LaunchCreds{IdentityToken: tok, LaunchSecret: secret})
	if err != nil {
		if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil { // rollback identity on failed launch
			s.log.Warn("raise rollback: failed to revoke identity after launch failure", "id", spec.ActorID, "error", rmErr)
		}
		return Instance{}, "", "", fmt.Errorf("raise: %w", err)
	}
	now := s.now()
	inst := Instance{
		ActorID: spec.ActorID, Project: orDefaultProject(spec.Project), Role: spec.Role, Unit: spec.Unit,
		Location: loc, Phase: PhaseLive, Activity: ActivityRunning,
		Lease:            Lease{Holder: s.holder, Expiry: now.Add(s.ttl)},
		LaunchSecretHash: HashToken(secret),
		RaisedAt:         now, LastSeen: now,
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
	inst.Activity = a
	inst.LastSeen = now
	inst.Lease = Lease{Holder: s.holder, Expiry: now.Add(s.ttl)}
	if enteringWaiting {
		inst.WaitingSince = now
		inst.WaitCursor = ""
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

// SetWaitCursor persists an opaque wake-on baseline on the instance (used by the
// wake-on engine to detect a new ticket comment). No-op semantics if the actor
// is gone.
func (s *Supervisor) SetWaitCursor(actorID, cursor string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.WaitCursor = cursor
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
// retry-wake window starts fresh.
func (s *Supervisor) Resume(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.Phase == PhaseGone {
		return fmt.Errorf("resume: no live instance for %q", actorID)
	}
	if err := s.launcher.Unpause(ctx, inst); err != nil {
		return err
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
				inst.Lease.Expiry = now.Add(s.ttl) // renew our own lease
				_ = s.store.PutInstance(inst)
			}
			continue // someone else's live lease: not ours to touch
		}
		live, err := s.launcher.Probe(ctx, inst)
		if err != nil || live == LivenessDead {
			inst.Phase = PhaseLost
			_ = s.store.PutInstance(inst)
			if derr := s.Teardown(ctx, inst.ActorID); derr != nil && s.log != nil {
				s.log.Warn("reconcile teardown failed", "id", inst.ActorID, "err", derr.Error())
			}
			continue
		}
		if live == LivenessAlive {
			inst.Lease = Lease{Holder: s.holder, Expiry: now.Add(s.ttl)} // steal + renew
			_ = s.store.PutInstance(inst)
		}
		// LivenessUnknown: leave for the next tick.
	}
	return nil
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
// call would be wrong — FileStore.RemoveActor deletes from memory before it
// persists, so a save failure would look like "already absent".
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
