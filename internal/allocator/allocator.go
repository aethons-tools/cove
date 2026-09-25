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

// Recorder persists allocation events (the durable reservation ledger). Optional:
// a nil Recorder (file-store dev, no Postgres) means the Allocator records nothing,
// exactly as slice 1. Implemented by internal/allocator/allocpg.
type Recorder interface {
	Record(ctx context.Context, ev Event) error
}

// Allocator decides admission: may another session exist for (project, role)?
type Allocator struct {
	counter  Counter
	budget   Budget
	recorder Recorder
}

// New builds an Allocator. recorder may be nil: with no event store (file-store
// dev) the Allocator records nothing, preserving slice-1 behavior.
func New(counter Counter, budget Budget, recorder Recorder) *Allocator {
	return &Allocator{counter: counter, budget: budget, recorder: recorder}
}

// Admit reports whether a new session may be created for (project, role): the live
// count is strictly below the configured budget. Fail-closed when no budget.
func (a *Allocator) Admit(project, role string) bool {
	limit, ok := a.budget.For(project, role)
	if !ok {
		return false
	}
	return a.counter.LiveCount(project, role) < limit
}

// RecordGrant durably records that a session was granted for (project, role) — a
// best-effort shadow write (the cap is still the registry count in this slice). A
// nil Recorder is a no-op.
func (a *Allocator) RecordGrant(ctx context.Context, project, role, reservationID string) error {
	if a.recorder == nil {
		return nil
	}
	return a.recorder.Record(ctx, Event{
		Category:      project,
		Project:       project,
		Role:          role,
		Kind:          KindReservationGranted,
		ReservationID: reservationID,
	})
}

// RecordRelease durably records that a session's reservation was released (its
// Studio was torn down) for (project, role) — a best-effort shadow write (the cap
// is still the registry count in this slice). A nil Recorder is a no-op.
func (a *Allocator) RecordRelease(ctx context.Context, project, role, reservationID string) error {
	if a.recorder == nil {
		return nil
	}
	return a.recorder.Record(ctx, Event{
		Category:      project,
		Project:       project,
		Role:          role,
		Kind:          KindReservationReleased,
		ReservationID: reservationID,
	})
}
