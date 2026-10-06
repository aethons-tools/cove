// Package standing is Jam's standing-session reconciler: a resident loop that
// keeps exactly one live cove per standing session declared on a role
// (RoleAllocation.Standing). A declared name with no cove is granted and raised;
// one whose cove died is raised again under the same actor id, and resumes:
// its /agent-data and workspace volumes survive and cove-master continues the
// prior conversation (COV-249); a cove whose name is no longer declared, or
// whose role is gone, is torn down. Persisted state is purged level-triggered:
// every pass sweeps the state of names no longer declared (once no cove uses
// it), and ResetStanding purges a declared name's state, holding it back from
// being raised until that's done. A name whose raise keeps failing backs off
// exponentially. It lives outside internal/jam core (Jam must not import
// it) and is wired from cmd/at-jam whenever Jam serves.
package standing

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/jam"
)

// Roster reads the declarations (satisfied by jam.Store).
type Roster interface {
	ListProjects() []string
	ListRoles(project string) []jam.Role
}

// Registry reads the durable Instance registry (satisfied by jam.Store).
type Registry interface {
	ListInstances() []jam.Instance
}

// Granter is Jam's capacity authority (satisfied by *allocator.Allocator).
type Granter interface {
	Grant(ctx context.Context, req allocator.Request) (bool, error)
	RecordRelease(ctx context.Context, project, role, reservationID string) error
}

// Supervisor raises and tears down coves and manages their persisted state
// (satisfied by *jam.Supervisor). Teardown records the reservation release
// itself and never touches state; PurgeState refuses state still in use.
type Supervisor interface {
	Raise(ctx context.Context, spec jam.RaiseSpec) (jam.Instance, string, string, error)
	Teardown(ctx context.Context, actorID string) error
	PurgeState(ctx context.Context, actorID string) error
	StateOwners(ctx context.Context) ([]string, error)
}

// Actors is the actor store, used to clear a leftover identity. A crash between
// Raise enrolling a standing cove's actor and recording its instance leaves an
// actor with the standing id and no cove; every later Raise would then fail
// "already exists". Satisfied by jam.Store.
type Actors interface {
	ListActors() []jam.Actor
	RemoveActor(id string) error
}

// Backoff bounds for a name whose raise fails: the first retry waits
// BackoffInitial, each further failure doubles it, up to BackoffMax.
const (
	DefaultInterval = 30 * time.Second
	BackoffInitial  = 30 * time.Second
	BackoffMax      = 30 * time.Minute
)

// backoff is one name's raise-failure state (in memory; a restart retries at once).
type backoff struct {
	fails int
	next  time.Time
}

// Reconciler keeps the declared standing sessions running.
type Reconciler struct {
	roster   Roster
	registry Registry
	granter  Granter
	sup      Supervisor
	actors   Actors // optional; nil ⇒ leftover identities are not cleared
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger
	// mu serializes Tick and ResetStanding, so a reset can't interleave with
	// an ensure that would re-raise the name on its old state.
	mu      sync.Mutex
	backoff map[string]backoff // actor id → raise-failure backoff
	// resetting holds the actor ids whose reset is pending (teardown or purge
	// failed): ensure won't raise them; each Tick retries. In memory only.
	resetting map[string]bool
}

// New builds a Reconciler that ticks every interval (DefaultInterval if <= 0).
// log may be nil.
func New(roster Roster, registry Registry, granter Granter, sup Supervisor, interval time.Duration, log *slog.Logger) *Reconciler {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Reconciler{
		roster: roster, registry: registry, granter: granter, sup: sup,
		interval: interval, now: time.Now, log: log, backoff: map[string]backoff{}, resetting: map[string]bool{},
	}
}

// SetActors lets the reconciler clear a leftover identity for a declared name
// that has no live cove (see Actors).
func (r *Reconciler) SetActors(a Actors) { r.actors = a }

// Run reconciles until ctx is cancelled: an immediate tick, then every interval
// (mirrors Supervisor.Run).
func (r *Reconciler) Run(ctx context.Context) {
	r.Tick(ctx)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Tick(ctx)
		}
	}
}

// declKey identifies one declaration; it is matched against an Instance's
// (Project, Role, Name).
type declKey struct{ project, role, name string }

// Tick runs one reconcile pass: finish pending resets, ensure a cove for every
// declared name, tear down standing coves that are no longer declared, then
// sweep the persisted state of names no longer declared.
func (r *Reconciler) Tick(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.resetting {
		if err := r.finishReset(ctx, id); err != nil {
			r.log.Warn("standing: reset still pending", "id", id, "err", err.Error())
		}
	}

	byID := map[string]jam.Instance{}
	for _, inst := range r.registry.ListInstances() {
		if inst.Phase != jam.PhaseGone {
			byID[inst.ActorID] = inst
		}
	}

	declared := map[declKey]bool{}
	declaredIDs := map[string]bool{}
	for _, project := range r.roster.ListProjects() {
		for _, role := range r.roster.ListRoles(project) {
			for _, s := range role.Allocation.Standing {
				declared[declKey{project, role.Name, s.Name}] = true
				declaredIDs[jam.StandingActorID(project, role.Name, s.Name)] = true
				r.ensure(ctx, project, role.Name, s, byID)
			}
		}
	}

	for _, inst := range byID {
		if inst.SessionKind != jam.SessionKindStanding {
			continue // ephemeral and personal coves are not ours
		}
		if declared[declKey{inst.Project, inst.Role, inst.Name}] {
			continue
		}
		if err := r.sup.Teardown(ctx, inst.ActorID); err != nil {
			r.log.Warn("standing: teardown of dismissed session failed", "id", inst.ActorID, "err", err.Error())
			continue
		}
		delete(r.backoff, inst.ActorID)
		r.log.Info("standing: dismissed session torn down", "id", inst.ActorID, "project", inst.Project, "role", inst.Role, "name", inst.Name)
	}

	r.sweep(ctx, declaredIDs)
}

// sweep purges the persisted state of every actor id not declared on any role:
// a dismissed name (even one dismissed while down), a removed role, a purge
// that failed before. State still in use (its cove not yet gone) is left for
// a later pass.
func (r *Reconciler) sweep(ctx context.Context, declaredIDs map[string]bool) {
	owners, err := r.sup.StateOwners(ctx)
	if err != nil {
		r.log.Warn("standing: listing persisted state failed", "err", err.Error())
		return
	}
	for _, id := range owners {
		if declaredIDs[id] {
			continue
		}
		if err := r.sup.PurgeState(ctx, id); err != nil {
			r.log.Info("standing: undeclared session's state not purged yet; retrying next pass", "id", id, "err", err.Error())
			continue
		}
		r.log.Info("standing: undeclared session's state purged", "id", id)
	}
}

// ResetStanding resets the declared standing session name of (project, role):
// its cove is torn down and its persisted state purged, the declaration kept,
// so the next pass raises it fresh. It clears the name's backoff. Serialized
// with Tick; until the purge succeeds the name is held back from raising and
// each Tick retries — an error here means the reset is pending, not lost. The
// caller (jam.ResetStanding) has checked the name is declared.
func (r *Reconciler) ResetStanding(ctx context.Context, project, role, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := jam.StandingActorID(project, role, name)
	delete(r.backoff, id)
	r.resetting[id] = true
	r.log.Info("standing: reset requested", "id", id, "project", project, "role", role, "name", name)
	return r.finishReset(ctx, id)
}

// finishReset tears id's cove down and purges its state, clearing its reset
// mark on success. Caller holds mu.
func (r *Reconciler) finishReset(ctx context.Context, id string) error {
	if err := r.sup.Teardown(ctx, id); err != nil {
		return fmt.Errorf("teardown: %w", err)
	}
	if err := r.sup.PurgeState(ctx, id); err != nil {
		return fmt.Errorf("purge state: %w", err)
	}
	delete(r.resetting, id)
	r.log.Info("standing: session reset", "id", id)
	return nil
}

// ensure makes sure the declared name s of (project, role) has a cove: grant,
// then raise, releasing the grant and backing off if the raise fails.
func (r *Reconciler) ensure(ctx context.Context, project, role string, s jam.StandingSession, byID map[string]jam.Instance) {
	id := jam.StandingActorID(project, role, s.Name)
	if r.resetting[id] {
		return // its old state isn't purged yet: raising would re-attach it
	}
	if inst, ok := byID[id]; ok {
		if inst.SessionKind != jam.SessionKindStanding || inst.Project != project || inst.Role != role || inst.Name != s.Name {
			r.log.Warn("standing: actor id held by another cove; not raising", "id", id, "project", project, "role", role, "name", s.Name)
			return
		}
		delete(r.backoff, id) // live: nothing to do
		return
	}
	now := r.now()
	if b, ok := r.backoff[id]; ok && now.Before(b.next) {
		return
	}
	// No live cove holds this Jam-owned id, so an actor with it can only be
	// left over from a raise that crashed before recording its instance.
	if r.actors != nil {
		for _, a := range r.actors.ListActors() {
			if a.ID != id {
				continue
			}
			if err := r.actors.RemoveActor(id); err != nil {
				r.log.Warn("standing: removing leftover identity failed", "id", id, "err", err.Error())
				return
			}
			r.log.Warn("standing: removed leftover identity from an interrupted raise", "id", id)
			break
		}
	}
	granted, err := r.granter.Grant(ctx, allocator.Request{
		Project: project, Role: role, ReservationID: id,
		Kind: allocator.SessionStanding, Name: s.Name,
	})
	if err != nil {
		r.log.Warn("standing: grant failed", "id", id, "err", err.Error())
		return
	}
	if !granted {
		r.log.Warn("standing: grant denied", "id", id, "project", project, "role", role, "name", s.Name)
		return
	}
	_, _, _, err = r.sup.Raise(ctx, jam.RaiseSpec{
		ActorID: id, Project: project, Role: role, Name: s.Name,
		Prompt: s.Prompt, SessionKind: jam.SessionKindStanding,
	})
	if err != nil {
		// Grant, then raise, then compensate: free the reserved slot.
		if rerr := r.granter.RecordRelease(context.WithoutCancel(ctx), project, role, id); rerr != nil {
			r.log.Warn("standing: compensating release failed", "id", id, "err", rerr.Error())
		}
		b := r.backoff[id]
		b.fails++
		delay := min(BackoffInitial<<min(b.fails-1, 16), BackoffMax)
		b.next = now.Add(delay)
		r.backoff[id] = b
		r.log.Warn("standing: raise failed; backing off", "id", id, "fails", b.fails, "retry_in", delay, "err", err.Error())
		return
	}
	delete(r.backoff, id)
	r.log.Info("standing: session raised", "id", id, "project", project, "role", role, "name", s.Name)
}
