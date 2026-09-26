// Package allocator is harbor's capacity authority (the "Allocator" role from the
// orchestration design): it rations session existence per (project, role) against
// a budget. Slice 1 is an in-memory admission check that moves the concurrency cap
// out of the dispatcher; the event-sourced reservation ledger arrives in a later
// slice, behind this same interface.
package allocator

import (
	"context"
	"errors"
	"fmt"
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

// SessionKind is what kind of session a reservation holds. It is distinct from
// Kind, which is the allocation *event* type.
type SessionKind string

const (
	// SessionEphemeral is a dispatcher-raised session for one unit of work.
	SessionEphemeral SessionKind = "ephemeral"
	// SessionStanding is an operator-declared, named session that lives until
	// dismissed (admitted in a later slice).
	SessionStanding SessionKind = "standing"
	// SessionPersonal is a human's ad-hoc session that lives until its owner
	// releases it (admitted in a later slice).
	SessionPersonal SessionKind = "personal"
)

// ErrUnsupportedKind means the Allocator does not (yet) admit this session kind.
var ErrUnsupportedKind = errors.New("allocator: unsupported session kind")

// Request asks for one reservation. Name is set for standing sessions, Owner for
// personal ones. An empty Kind means SessionEphemeral.
type Request struct {
	Project, Role string
	ReservationID string
	Kind          SessionKind
	Name          string
	Owner         string
}

// Policy is a (project, role)'s allocation policy. Later slices add the personal
// cap, the standing name set, idle settings, and requester grants.
type Policy struct {
	// MaxEphemeral caps concurrent ephemeral sessions; <= 0 admits none.
	MaxEphemeral int
}

// PolicySource returns the allocation policy for a (project, role); ok=false
// means none is configured, and admission fails closed.
type PolicySource interface {
	Policy(project, role string) (Policy, bool)
}

// StaticPolicy is a fixed policy table (tests, and the dispatcher-seeded
// fallback behind cmd/at-harbor's roster-sourced policy).
type StaticPolicy map[Key]Policy

// Policy implements PolicySource.
func (p StaticPolicy) Policy(project, role string) (Policy, bool) {
	pol, ok := p[Key{Project: project, Role: role}]
	return pol, ok
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
// assigned by the store on append. SessionKind/Name/Owner describe the
// reservation's session; the store derives them for a release from the
// reservation's latest grant, so release callers leave them empty.
type Event struct {
	Category      string
	Project, Role string
	Kind          Kind
	ReservationID string
	SessionKind   SessionKind
	Name          string
	Owner         string
}

// Reservation identifies an outstanding reservation — one still holding a slot
// (net granted − released > 0). The reconcile Sweep (Slice 5) folds the ledger to
// these and releases the ones whose actor has no live instance. SessionKind/Name/
// Owner come from the reservation's latest grant (an empty SessionKind is legacy,
// i.e. ephemeral).
type Reservation struct {
	Project, Role string
	ReservationID string
	SessionKind   SessionKind
	Name          string
	Owner         string
}

// Ledger is the durable reservation ledger: it appends allocation events and, as
// of Slice 4, is the authoritative OCC admission gate via Grant. Optional: a nil
// Ledger (file-store dev, no Postgres) means the Allocator has no ledger, so Grant
// falls back to the registry live count and RecordRelease is a no-op. Implemented
// by internal/allocator/allocpg.
type Ledger interface {
	Record(ctx context.Context, ev Event) error
	// Grant atomically appends a ReservationGranted for req iff the outstanding
	// reservations of req.Kind in its (project, role) stream are below limit.
	Grant(ctx context.Context, req Request, limit int) (bool, error)
	// OutstandingReservations returns the net-outstanding reservations (granted −
	// released > 0) whose latest grant predates olderThan — the reconcile sweep's
	// grace-windowed candidate set.
	OutstandingReservations(ctx context.Context, olderThan time.Time) ([]Reservation, error)
}

// Allocator decides admission: may another session exist for (project, role)?
type Allocator struct {
	counter Counter
	policy  PolicySource
	ledger  Ledger
	now     func() time.Time // injectable clock for the sweep cutoff; defaults to time.Now
	log     *slog.Logger
}

// New builds an Allocator. ledger may be nil: with no event store (file-store dev)
// Grant falls back to the registry live count and RecordRelease is a no-op. The
// sweep clock defaults to time.Now and the logger to a discard logger (the wiring
// in cmd/at-harbor can override either after construction).
func New(counter Counter, policy PolicySource, ledger Ledger) *Allocator {
	return &Allocator{
		counter: counter,
		policy:  policy,
		ledger:  ledger,
		now:     time.Now,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// Grant admits (and reserves) a session for req's (project, role). Only
// ephemeral sessions are admitted in this slice (an empty Kind means ephemeral);
// standing and personal fail closed with ErrUnsupportedKind. The cap is the
// (project, role) policy's MaxEphemeral. With a ledger it is the authoritative
// OCC admission — an atomic append that grants iff the stream's outstanding
// ephemeral reservations are below the cap. Without one (file-store dev) it falls
// back to the registry live count vs the cap (Slice-1 global behavior).
// Fail-closed when no policy (or no ephemeral cap) is configured.
//
// Grant reserves the slot before the raise; the caller must compensate (call
// RecordRelease for the same reservation id) on any post-grant failure. In
// file-store mode Grant reserves nothing and RecordRelease is a no-op, so
// compensation is harmless there.
func (a *Allocator) Grant(ctx context.Context, req Request) (bool, error) {
	if req.Kind == "" {
		req.Kind = SessionEphemeral
	}
	if req.Kind != SessionEphemeral {
		return false, fmt.Errorf("%w: %s", ErrUnsupportedKind, req.Kind)
	}
	pol, ok := a.policy.Policy(req.Project, req.Role)
	if !ok || pol.MaxEphemeral <= 0 {
		return false, nil // no policy ⇒ fail closed
	}
	if a.ledger != nil {
		return a.ledger.Grant(ctx, req, pol.MaxEphemeral)
	}
	return a.counter.LiveCount(req.Project, req.Role) < pol.MaxEphemeral, nil
}

// SetLogger routes the reconcile sweep's diagnostics to log (nil is ignored). The
// sweep is otherwise silent (New defaults to a discard logger); cmd/at-harbor
// calls this so a resident SweepLoop's Info/Warn records reach the sink.
func (a *Allocator) SetLogger(log *slog.Logger) {
	if log != nil {
		a.log = log
	}
}

// Sweep reclaims leaked slots: it releases outstanding ephemeral reservations
// (whose latest grant is older than grace) whose actor has no live instance — the
// crash-between-grant-and-raise gap Slice 4 left open. No ledger (file-store dev)
// ⇒ nothing to sweep. Best-effort and idempotent: a per-reservation release
// failure is logged and skipped (the next tick retries), and releasing an
// already-live or already-released reservation is harmless (the net-count query
// excludes it next pass). Standing and personal reservations own their lifecycles
// (resurrection, owner release) and are never swept. Returns the number swept.
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
		if r.SessionKind != "" && r.SessionKind != SessionEphemeral {
			continue // standing/personal — not the sweep's to release
		}
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
