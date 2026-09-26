package main

import (
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
// dispatcher). The personal caps come only from the Role. No role and no
// fallback ⇒ no policy ⇒ fail closed.
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
	if pol.MaxEphemeral <= 0 {
		pol.MaxEphemeral = fb.MaxEphemeral // dispatcher fallback (0 without one)
	}
	if pol == (allocator.Policy{}) {
		return pol, false // the role sets nothing and there is no fallback
	}
	return pol, true
}
