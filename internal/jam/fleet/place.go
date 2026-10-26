package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// maxDeclineBackoff caps the per-manager delay after repeated declines.
const maxDeclineBackoff = 30 * time.Second

// declineState is one manager's backoff within a single RaiseOn.
type declineState struct {
	delay time.Duration // doubles on each further decline, starting at poll
	next  time.Time     // not tried again before this
	last  string        // the decline's error text
}

// RaiseOn places the raise on the first manager that accepts it, trying the
// spec's candidates in order. A manager that is unavailable or declines is
// skipped (a decliner is retried after a doubling backoff, and a Kick makes it
// eligible at once); a raise interrupted mid-call fails at once with
// jam.ErrRaiseInterrupted, as its outcome is unknown, and one cut short by ctx
// returns ctx's error. If none accepts, it waits (until a Kick, the poll tick,
// the placement timeout or ctx) and tries again. An unregistered candidate fails
// at once; a manager needing its kit prepared fails with KitNotReadyError.
func (r *Router) RaiseOn(ctx context.Context, spec jam.RaiseSpec, creds jam.LaunchCreds) (jam.Placed, error) {
	names := spec.Placement.Managers
	if len(names) == 0 {
		names = r.Names()
	}
	for _, name := range names {
		if !r.HasManager(name) {
			return jam.Placed{}, fmt.Errorf("%w: %s", jam.ErrUnknownManager, name)
		}
	}
	deadline := r.now().Add(spec.Placement.Timeout)
	backoff := map[string]*declineState{}
	for {
		wake := r.wakeChan() // taken before the round so a Kick during it is not lost
		var reasons []string
		for _, name := range names {
			m, ok := r.get(name)
			if !ok { // removed since the check above
				reasons = append(reasons, name+": not registered")
				continue
			}
			if !m.Available() {
				reasons = append(reasons, name+": unavailable")
				continue
			}
			if st := backoff[name]; st != nil && r.now().Before(st.next) {
				reasons = append(reasons, name+": "+st.last+" (backing off)")
				continue
			}
			loc, err := m.Raise(ctx, spec, creds)
			switch {
			case err == nil:
				return jam.Placed{Manager: name, Location: loc}, nil
			case errors.Is(err, jam.ErrKitNotReady):
				return jam.Placed{}, &jam.KitNotReadyError{Manager: name}
			case errors.Is(err, jam.ErrRaiseInterrupted):
				return jam.Placed{}, fmt.Errorf("manager %s: %w", name, err)
			case ctx.Err() != nil: // the caller gave up mid-call: not a decline
				return jam.Placed{}, ctx.Err()
			default:
				st := backoff[name]
				if st == nil {
					st = &declineState{delay: r.poll}
					backoff[name] = st
				} else {
					st.delay = min(st.delay*2, maxDeclineBackoff)
				}
				st.next = r.now().Add(st.delay)
				st.last = err.Error()
				reasons = append(reasons, name+": "+err.Error())
			}
		}
		remaining := deadline.Sub(r.now())
		if remaining <= 0 {
			return jam.Placed{}, &jam.NoManagerError{Reasons: reasons}
		}
		wait := min(r.poll, remaining)
		timer := time.NewTimer(wait)
		r.waiters.Add(1)
		select {
		case <-ctx.Done():
			r.waiters.Add(-1)
			timer.Stop()
			return jam.Placed{}, ctx.Err()
		case <-wake:
			for _, st := range backoff { // a Kick makes every manager eligible
				st.next = time.Time{}
			}
		case <-timer.C:
		}
		r.waiters.Add(-1)
		timer.Stop()
	}
}

// PrepareKitOn prepares def on the named manager.
func (r *Router) PrepareKitOn(ctx context.Context, name string, def jam.KitDefinition) (jam.KitStatus, error) {
	m, ok := r.get(name)
	switch {
	case !ok:
		return jam.KitStatus{}, fmt.Errorf("%w: %s", jam.ErrUnknownManager, name)
	case !m.Available():
		return jam.KitStatus{}, fmt.Errorf("%w: %s", jam.ErrManagerUnavailable, name)
	}
	return m.PrepareKit(ctx, def)
}

// ImageTagOn names the image on the named manager; "" if it is not registered
// or cannot tag images.
func (r *Router) ImageTagOn(name string, ref jam.KitRef) string {
	m, ok := r.get(name)
	if !ok {
		return ""
	}
	if t, ok := optional(m).(jam.ImageTagger); ok {
		return t.ImageTag(ref)
	}
	return ""
}
