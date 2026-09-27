// Package standing is harbor's standing-session reconciler: a resident loop that
// keeps exactly one live cove per standing session declared on a role
// (RoleAllocation.Standing). A declared name with no cove is granted and raised;
// one whose cove died is raised again under the same actor id (a fresh session:
// no context carries over); a cove whose name is no longer declared, or whose
// role is gone, is torn down. A name whose raise keeps failing backs off
// exponentially. It lives outside internal/jam core (harbor must not import
// it) and is wired from cmd/at-jam whenever harbor serves.
package standing

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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

// Granter is harbor's capacity authority (satisfied by *allocator.Allocator).
type Granter interface {
	Grant(ctx context.Context, req allocator.Request) (bool, error)
	RecordRelease(ctx context.Context, project, role, reservationID string) error
}

// Supervisor raises and tears down coves (satisfied by *jam.Supervisor).
// Teardown records the reservation release itself.
type Supervisor interface {
	Raise(ctx context.Context, spec jam.RaiseSpec) (jam.Instance, string, string, error)
	Teardown(ctx context.Context, actorID string) error
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
	backoff  map[string]backoff // actor id → raise-failure backoff
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
		interval: interval, now: time.Now, log: log, backoff: map[string]backoff{},
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

// Tick runs one reconcile pass: ensure a cove for every declared name, then tear
// down standing coves that are no longer declared.
func (r *Reconciler) Tick(ctx context.Context) {
	byID := map[string]jam.Instance{}
	for _, inst := range r.registry.ListInstances() {
		if inst.Phase != jam.PhaseGone {
			byID[inst.ActorID] = inst
		}
	}

	declared := map[declKey]bool{}
	for _, project := range r.roster.ListProjects() {
		for _, role := range r.roster.ListRoles(project) {
			for _, s := range role.Allocation.Standing {
				declared[declKey{project, role.Name, s.Name}] = true
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
}

// ensure makes sure the declared name s of (project, role) has a cove: grant,
// then raise, releasing the grant and backing off if the raise fails.
func (r *Reconciler) ensure(ctx context.Context, project, role string, s jam.StandingSession, byID map[string]jam.Instance) {
	id := jam.StandingActorID(project, role, s.Name)
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
	// No live cove holds this harbor-owned id, so an actor with it can only be
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
		Prompt: Prompt(project, role, s), SessionKind: jam.SessionKindStanding,
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

// Prompt is the prompt a standing session is raised with: a preamble telling
// the agent how a standing session works, then the declared prompt.
func Prompt(project, role string, s jam.StandingSession) string {
	return fmt.Sprintf("You are the standing session %q for role %s in project %s. You run until an operator\n"+
		"removes you. When you have results or need input, message people with the intercom `send` tool — you\n"+
		"must name the recipient (`to`); replies wake you and are available via `read`.\n"+
		"---\n%s", s.Name, role, project, s.Prompt)
}
