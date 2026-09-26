// Package allocator is harbor's capacity authority (the "Allocator" role from the
// orchestration design): it rations session existence per (project, role) against
// a budget. Slice 1 is an in-memory admission check that moves the concurrency cap
// out of the dispatcher; the event-sourced reservation ledger arrives in a later
// slice, behind this same interface.
package allocator

import "context"

// Key identifies a role within a project — the allocation aggregate's key.
type Key struct{ Project, Role string }

// Counter reports how many sessions currently exist (are live) for a
// (project, role). Slice 1's implementation counts globally, preserving the
// dispatcher's prior max-concurrent semantics.
type Counter interface {
	LiveCount(project, role string) int
}

// Budget returns the per-(project, role) capacity; ok=false means no budget is
// configured for that pair, and admission fails closed.
type Budget interface {
	For(project, role string) (limit int, ok bool)
}

// StaticBudget is a fixed budget table. Slice 1 seeds it from the dispatcher's
// max-concurrent; a later slice replaces it with a roster-observed budget.
type StaticBudget map[Key]int

func (b StaticBudget) For(project, role string) (int, bool) {
	limit, ok := b[Key{Project: project, Role: role}]
	return limit, ok
}

// Kind is an allocation event type.
type Kind string

// KindReservationGranted records that a session was granted for a (project, role).
// Slice 2 records grants only; ReservationRequested/Denied/Withdrawn/Released are
// defined-but-deferred (Released lands in Slice 3).
const KindReservationGranted Kind = "reservation_granted"

// KindReservationReleased records that a session's reservation was released (its
// Studio was torn down) for a (project, role). Slice 3 records releases so the
// shadow ledger's outstanding count (granted − released) tracks reality; the cap
// stays on the registry count.
const KindReservationReleased Kind = "reservation_released"

// Event is one allocation event. Category is the project (the grouping/shard
// axis); the stream is keyed by (Project, Role). Revision/seq/timestamp are
// assigned by the store on append.
type Event struct {
	Category      string
	Project, Role string
	Kind          Kind
	ReservationID string
}

// Reservation identifies an outstanding reservation — one still holding a slot
// (net granted − released > 0). The reconcile Sweep (Slice 5) folds the ledger to
// these and releases the ones whose actor has no live instance.
type Reservation struct {
	Project, Role string
	ReservationID string
}

// Ledger is the durable reservation ledger: it appends allocation events and, as
// of Slice 4, is the authoritative OCC admission gate via Grant. Optional: a nil
// Ledger (file-store dev, no Postgres) means the Allocator has no ledger, so Grant
// falls back to the registry live count and RecordRelease is a no-op. Implemented
// by internal/allocator/allocpg.
type Ledger interface {
	Record(ctx context.Context, ev Event) error
	Grant(ctx context.Context, project, role, reservationID string, budget int) (bool, error)
}

// Allocator decides admission: may another session exist for (project, role)?
type Allocator struct {
	counter Counter
	budget  Budget
	ledger  Ledger
}

// New builds an Allocator. ledger may be nil: with no event store (file-store dev)
// Grant falls back to the registry live count and RecordRelease is a no-op.
func New(counter Counter, budget Budget, ledger Ledger) *Allocator {
	return &Allocator{counter: counter, budget: budget, ledger: ledger}
}

// Grant admits (and reserves) a session for (project, role). With a ledger it is
// the authoritative OCC admission — an atomic append that grants iff the stream's
// Outstanding is below budget (per-(project, role) counting). Without one
// (file-store dev) it falls back to the registry live count vs budget (Slice-1
// global behavior). Fail-closed when no budget is configured.
//
// Grant reserves the slot before the raise; the caller must compensate (call
// RecordRelease for the same reservationID) on any post-grant failure. In
// file-store mode Grant reserves nothing and RecordRelease is a no-op, so
// compensation is harmless there.
func (a *Allocator) Grant(ctx context.Context, project, role, reservationID string) (bool, error) {
	limit, ok := a.budget.For(project, role)
	if !ok {
		return false, nil
	}
	if a.ledger != nil {
		return a.ledger.Grant(ctx, project, role, reservationID, limit)
	}
	return a.counter.LiveCount(project, role) < limit, nil
}

// RecordRelease durably records that a session's reservation was released (its
// Studio was torn down, or a post-grant failure compensated) for (project, role).
// It frees the slot Grant reserved. A nil Ledger (file-store dev) is a no-op.
func (a *Allocator) RecordRelease(ctx context.Context, project, role, reservationID string) error {
	if a.ledger == nil {
		return nil
	}
	return a.ledger.Record(ctx, Event{
		Category:      project,
		Project:       project,
		Role:          role,
		Kind:          KindReservationReleased,
		ReservationID: reservationID,
	})
}
