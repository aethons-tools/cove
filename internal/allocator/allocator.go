// Package allocator is harbor's capacity authority (the "Allocator" role from the
// orchestration design): it rations session existence per (project, role) against
// a budget. Slice 1 is an in-memory admission check that moves the concurrency cap
// out of the dispatcher; the event-sourced reservation ledger arrives in a later
// slice, behind this same interface.
package allocator

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// Key identifies a role within a project — the allocation aggregate's key.
type Key struct{ Project, Role string }

// Counter reports how many sessions currently exist (are live) for a
// (project, role). Slice 1's implementation counts globally, preserving the
// dispatcher's prior max-concurrent semantics.
type Counter interface {
	LiveCount(project, role string) int
	// IsLive reports whether a specific actor (reservationID == actorID) currently
	// holds a live instance — the reconcile sweep's dangling-reservation signal.
	IsLive(actorID string) bool
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
	// OutstandingReservations returns the net-outstanding reservations (granted −
	// released > 0) whose latest grant predates olderThan — the reconcile sweep's
	// grace-windowed candidate set.
	OutstandingReservations(ctx context.Context, olderThan time.Time) ([]Reservation, error)
}

// Allocator decides admission: may another session exist for (project, role)?
type Allocator struct {
	counter Counter
	budget  Budget
	ledger  Ledger
	now     func() time.Time // injectable clock for the sweep cutoff; defaults to time.Now
	log     *slog.Logger
}

// New builds an Allocator. ledger may be nil: with no event store (file-store dev)
// Grant falls back to the registry live count and RecordRelease is a no-op. The
// sweep clock defaults to time.Now and the logger to a discard logger (the wiring
// in cmd/at-harbor can override either after construction).
func New(counter Counter, budget Budget, ledger Ledger) *Allocator {
	return &Allocator{
		counter: counter,
		budget:  budget,
		ledger:  ledger,
		now:     time.Now,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
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

// SetLogger routes the reconcile sweep's diagnostics to log (nil is ignored). The
// sweep is otherwise silent (New defaults to a discard logger); cmd/at-harbor
// calls this so a resident SweepLoop's Info/Warn records reach the sink.
func (a *Allocator) SetLogger(log *slog.Logger) {
	if log != nil {
		a.log = log
	}
}

// Sweep reclaims leaked slots: it releases outstanding reservations (whose latest
// grant is older than grace) whose actor has no live instance — the
// crash-between-grant-and-raise gap Slice 4 left open. No ledger (file-store dev)
// ⇒ nothing to sweep. Best-effort and idempotent: a per-reservation release
// failure is logged and skipped (the next tick retries), and releasing an
// already-live or already-released reservation is harmless (the net-count query
// excludes it next pass). Returns the number swept.
func (a *Allocator) Sweep(ctx context.Context, grace time.Duration) (int, error) {
	if a.ledger == nil {
		return 0, nil
	}
	outstanding, err := a.ledger.OutstandingReservations(ctx, a.now().Add(-grace))
	if err != nil {
		return 0, err
	}
	swept := 0
	for _, r := range outstanding {
		if a.counter.IsLive(r.ReservationID) {
			continue // a real session — leave it
		}
		if err := a.RecordRelease(ctx, r.Project, r.Role, r.ReservationID); err != nil {
			a.log.Warn("allocator: sweep release failed", "reservation", r.ReservationID, "err", err.Error())
			continue
		}
		swept++
	}
	return swept, nil
}

// SweepLoop runs Sweep every interval until ctx is cancelled — harbor's resident
// reconcile loop, like Dispatcher.Run. Errors and non-zero sweeps are logged.
func (a *Allocator) SweepLoop(ctx context.Context, interval, grace time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := a.Sweep(ctx, grace); err != nil {
				a.log.Warn("allocator: sweep failed", "err", err.Error())
			} else if n > 0 {
				a.log.Info("allocator: swept leaked reservations", "count", n)
			}
		}
	}
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
