package main

import (
	"context"
	"errors"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/harbor"
)

// roleReader is the slice of harbor.Store the roster policy reads.
type roleReader interface {
	GetRole(project, name string) (harbor.Role, bool)
}

// rosterPolicy is the Allocator's PolicySource: the roster Role is the source of
// truth (read live from the memory-cached store on each grant, so a role edit
// takes effect on the next grant with no restart); the dispatcher's
// max-concurrent is the fallback ephemeral cap for its own (project, role) when
// the Role sets no max-ephemeral (fallback is empty when harbor serves without a
// dispatcher). The personal caps and the standing names come only from the
// Role. No role and no fallback ⇒ no policy ⇒ fail closed.
type rosterPolicy struct {
	store    roleReader
	fallback allocator.StaticPolicy
}

var _ allocator.PolicySource = rosterPolicy{}

// Policy implements allocator.PolicySource.
func (p rosterPolicy) Policy(project, role string) (allocator.Policy, bool) {
	fb, hasFallback := p.fallback.Policy(project, role)
	r, hasRole := p.store.GetRole(project, role)
	if !hasRole {
		return fb, hasFallback
	}
	a := r.Allocation
	pol := allocator.Policy{
		MaxEphemeral:        a.MaxEphemeral,
		MaxPersonal:         a.MaxPersonal,
		MaxPersonalPerOwner: a.MaxPersonalPerOwner,
	}
	for _, s := range a.Standing {
		pol.StandingNames = append(pol.StandingNames, s.Name)
	}
	if pol.MaxEphemeral <= 0 {
		pol.MaxEphemeral = fb.MaxEphemeral // dispatcher fallback (0 without one)
	}
	if pol.MaxEphemeral <= 0 && pol.MaxPersonal <= 0 && pol.MaxPersonalPerOwner <= 0 && len(pol.StandingNames) == 0 {
		return pol, false // the role sets nothing and there is no fallback
	}
	return pol, true
}

// newRosterPolicy builds the Allocator's roster-sourced policy. The dispatcher's
// max-concurrent seeds the ephemeral fallback for its own (project, role) only
// when a dispatcher is configured (dc != nil); without one the roster alone
// decides. The dispatcher project is normalized (see dispatcherProject) so
// grants and releases land on the same (project, role) stream.
func newRosterPolicy(store roleReader, dc *dispatcherConfig) rosterPolicy {
	p := rosterPolicy{store: store}
	if dc != nil {
		p.fallback = allocator.StaticPolicy{{Project: dispatcherProject(dc), Role: dc.Role}: {MaxEphemeral: dc.MaxConcurrent}}
	}
	return p
}

// dispatcherProject is the dispatcher's project, normalized: grants use this value
// (via dispatcher.Config.Project → Grant), and the Supervisor stores
// inst.Project = orDefaultProject(spec.Project) = harbor.DefaultProject when
// empty, which is what RecordRelease keys off on teardown. Leaving it as
// dc.Project ("") would split them across "/role" and "default/role" — they'd
// never reconcile.
func dispatcherProject(dc *dispatcherConfig) string {
	if dc.Project == "" {
		return harbor.DefaultProject
	}
	return dc.Project
}

// personalAllocator adapts *allocator.Allocator to harbor.SessionAllocator (harbor
// does not import allocator): a personal grant is a SessionPersonal request owned
// by the requester, and the allocator's no-ledger error becomes
// harbor.ErrNeedsLedger so the admin route can answer 409.
type personalAllocator struct{ a *allocator.Allocator }

var _ harbor.SessionAllocator = personalAllocator{}

// GrantPersonal implements harbor.SessionAllocator.
func (p personalAllocator) GrantPersonal(ctx context.Context, project, role, reservationID, owner string) (bool, error) {
	ok, err := p.a.Grant(ctx, allocator.Request{
		Project: project, Role: role, ReservationID: reservationID,
		Kind: allocator.SessionPersonal, Owner: owner,
	})
	if errors.Is(err, allocator.ErrNeedsLedger) {
		return false, harbor.ErrNeedsLedger
	}
	return ok, err
}

// RecordRelease implements harbor.SessionAllocator.
func (p personalAllocator) RecordRelease(ctx context.Context, project, role, reservationID string) error {
	return p.a.RecordRelease(ctx, project, role, reservationID)
}
