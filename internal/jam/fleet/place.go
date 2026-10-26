package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// RaiseOn places the raise on the first manager that accepts it, trying the
// spec's candidates in order. A manager that is unavailable or declines is
// skipped; if none accepts, it waits (until a Kick, the poll tick, the
// placement timeout or ctx) and tries again. An unregistered candidate fails
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
	for {
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
			loc, err := m.Raise(ctx, spec, creds)
			switch {
			case err == nil:
				return jam.Placed{Manager: name, Location: loc}, nil
			case errors.Is(err, jam.ErrKitNotReady):
				return jam.Placed{}, &jam.KitNotReadyError{Manager: name}
			default:
				reasons = append(reasons, name+": "+err.Error())
			}
		}
		remaining := deadline.Sub(r.now())
		if remaining <= 0 {
			return jam.Placed{}, &jam.NoManagerError{Reasons: reasons}
		}
		wait := min(r.poll, remaining)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return jam.Placed{}, ctx.Err()
		case <-r.wake:
		case <-timer.C:
		}
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
