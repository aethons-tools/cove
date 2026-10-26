// Package fleet routes Jam's launcher calls across named managers.
package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// Manager is one named place studios run.
type Manager interface {
	jam.Launcher
	Name() string
	Available() bool // connected and serving now; Local is always true
}

var (
	_ jam.PlacingLauncher = (*Router)(nil)
	_ jam.StateKeeper     = (*Router)(nil)
	_ jam.ImageTagger     = (*Router)(nil)
	_ jam.KitDraftBuilder = (*Router)(nil)
)

// Router is a jam.Launcher over the registered managers.
type Router struct {
	log  *slog.Logger
	wake chan struct{} // 1-buffered; Kick wakes raises waiting for a manager
	now  func() time.Time
	poll time.Duration // how often a waiting raise re-checks without a Kick

	mu      sync.RWMutex
	members []Manager // registration order
}

// New returns an empty Router.
func New(log *slog.Logger) *Router {
	return &Router{log: log, wake: make(chan struct{}, 1), now: time.Now, poll: time.Second}
}

// Add registers m. It errors on a duplicate name, and on a non-Local manager
// named LocalName. It wakes raises waiting for a manager.
func (r *Router) Add(m Manager) error {
	name := m.Name()
	if _, isLocal := m.(local); name == LocalName && !isLocal {
		return fmt.Errorf("manager name %q is reserved", LocalName)
	}
	r.mu.Lock()
	if slices.ContainsFunc(r.members, func(x Manager) bool { return x.Name() == name }) {
		r.mu.Unlock()
		return fmt.Errorf("manager %q already registered", name)
	}
	r.members = append(r.members, m)
	r.mu.Unlock()
	r.Kick()
	return nil
}

// Remove unregisters the named manager.
func (r *Router) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members = slices.DeleteFunc(r.members, func(x Manager) bool { return x.Name() == name })
}

// Kick wakes raises waiting for a manager. It never blocks.
func (r *Router) Kick() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// ordered returns the members in candidate order: registration order, local last.
func (r *Router) ordered() []Manager {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Manager, 0, len(r.members))
	var loc Manager
	for _, m := range r.members {
		if m.Name() == LocalName {
			loc = m
			continue
		}
		out = append(out, m)
	}
	if loc != nil {
		out = append(out, loc)
	}
	return out
}

// Names lists the managers in candidate order: registration order, local last.
func (r *Router) Names() []string {
	ms := r.ordered()
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = m.Name()
	}
	return names
}

// HasManager reports whether name is registered.
func (r *Router) HasManager(name string) bool {
	_, ok := r.get(name)
	return ok
}

func (r *Router) get(name string) (Manager, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.members {
		if m.Name() == name {
			return m, true
		}
	}
	return nil, false
}

// available lists the available managers in candidate order.
func (r *Router) available() []Manager {
	return slices.DeleteFunc(r.ordered(), func(m Manager) bool { return !m.Available() })
}

// route resolves the manager that raised inst; "" is local.
func (r *Router) route(inst jam.Instance) (m Manager, name string, ok bool) {
	name = inst.Manager
	if name == "" {
		name = LocalName
	}
	m, ok = r.get(name)
	return m, name, ok
}

// routeAvail is route for the methods that fail when the manager cannot serve.
func (r *Router) routeAvail(inst jam.Instance) (Manager, error) {
	m, name, ok := r.route(inst)
	switch {
	case !ok:
		return nil, fmt.Errorf("%w: %s", jam.ErrUnknownManager, name)
	case !m.Available():
		return nil, fmt.Errorf("%w: %s", jam.ErrManagerUnavailable, name)
	}
	return m, nil
}

// optional returns m's underlying launcher when m wraps one (Local), else m,
// for asserting the optional interfaces.
func optional(m Manager) any {
	if u, ok := m.(interface{ Unwrap() jam.Launcher }); ok {
		return u.Unwrap()
	}
	return m
}

// Probe forwards to inst's manager. An unregistered manager is Dead (nothing
// can still be running there); an unavailable one is Unknown, not an error.
func (r *Router) Probe(ctx context.Context, inst jam.Instance) (jam.Liveness, error) {
	m, _, ok := r.route(inst)
	switch {
	case !ok:
		return jam.LivenessDead, nil
	case !m.Available():
		return jam.LivenessUnknown, nil
	}
	return m.Probe(ctx, inst)
}

// Teardown forwards to inst's manager. With the manager unregistered there is
// nothing to remove; with it unavailable the error makes the supervisor retry.
func (r *Router) Teardown(ctx context.Context, inst jam.Instance) error {
	m, name, ok := r.route(inst)
	switch {
	case !ok:
		r.log.Warn("teardown: manager not configured; nothing to remove", "manager", name, "actor", inst.ActorID)
		return nil
	case !m.Available():
		return fmt.Errorf("%w: %s", jam.ErrManagerUnavailable, name)
	}
	return m.Teardown(ctx, inst)
}

// Pause forwards to inst's manager.
func (r *Router) Pause(ctx context.Context, inst jam.Instance) error {
	m, err := r.routeAvail(inst)
	if err != nil {
		return err
	}
	return m.Pause(ctx, inst)
}

// Unpause forwards to inst's manager.
func (r *Router) Unpause(ctx context.Context, inst jam.Instance) error {
	m, err := r.routeAvail(inst)
	if err != nil {
		return err
	}
	return m.Unpause(ctx, inst)
}

// ApplyEgress forwards to inst's manager.
func (r *Router) ApplyEgress(ctx context.Context, inst jam.Instance, p *jam.EgressPolicy) error {
	m, err := r.routeAvail(inst)
	if err != nil {
		return err
	}
	return m.ApplyEgress(ctx, inst, p)
}

// Raise places the raise (RaiseOn) and returns its location.
func (r *Router) Raise(ctx context.Context, spec jam.RaiseSpec, creds jam.LaunchCreds) (string, error) {
	p, err := r.RaiseOn(ctx, spec, creds)
	return p.Location, err
}

// PrepareKit prepares def on the first available manager.
func (r *Router) PrepareKit(ctx context.Context, def jam.KitDefinition) (jam.KitStatus, error) {
	av := r.available()
	if len(av) == 0 {
		return jam.KitStatus{}, fmt.Errorf("%w: no manager available", jam.ErrManagerUnavailable)
	}
	return av[0].PrepareKit(ctx, def)
}

// ImageTag names the image on the first available manager; "" if none.
func (r *Router) ImageTag(ref jam.KitRef) string {
	av := r.available()
	if len(av) == 0 {
		return ""
	}
	return r.ImageTagOn(av[0].Name(), ref)
}

// StateOwners is the sorted union over available managers that keep state.
func (r *Router) StateOwners(ctx context.Context) ([]string, error) {
	seen := map[string]struct{}{}
	for _, m := range r.available() {
		sk, ok := optional(m).(jam.StateKeeper)
		if !ok {
			continue
		}
		ids, err := sk.StateOwners(ctx)
		if err != nil {
			return nil, fmt.Errorf("manager %s: %w", m.Name(), err)
		}
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

// PurgeState purges actorID's state on the available managers that own it,
// returning the first error. No owner is not an error.
func (r *Router) PurgeState(ctx context.Context, actorID string) error {
	var first error
	for _, m := range r.available() {
		sk, ok := optional(m).(jam.StateKeeper)
		if !ok {
			continue
		}
		ids, err := sk.StateOwners(ctx)
		if err != nil {
			first = cmpErr(first, fmt.Errorf("manager %s: %w", m.Name(), err))
			continue
		}
		if !slices.Contains(ids, actorID) {
			continue
		}
		if err := sk.PurgeState(ctx, actorID); err != nil {
			first = cmpErr(first, fmt.Errorf("manager %s: %w", m.Name(), err))
		}
	}
	return first
}

func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

var errNoDraftBuilder = errors.New("no manager can build kit drafts")

// draftBuilder is the first available manager that can build kit drafts.
func (r *Router) draftBuilder() (jam.KitDraftBuilder, error) {
	for _, m := range r.available() {
		if b, ok := optional(m).(jam.KitDraftBuilder); ok {
			return b, nil
		}
	}
	return nil, errNoDraftBuilder
}

// BuildKitDraft forwards to the first available draft-capable manager.
func (r *Router) BuildKitDraft(ctx context.Context, def jam.KitDefinition, tag string, log io.Writer) (string, error) {
	b, err := r.draftBuilder()
	if err != nil {
		return "", err
	}
	return b.BuildKitDraft(ctx, def, tag, log)
}

// HasKitDraftImage forwards to the first available draft-capable manager.
func (r *Router) HasKitDraftImage(imageID string) (bool, error) {
	b, err := r.draftBuilder()
	if err != nil {
		return false, err
	}
	return b.HasKitDraftImage(imageID)
}
