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
// max-concurrent is the fallback for its own (project, role) when the Role sets
// no max-ephemeral. No role policy and no fallback ⇒ no policy ⇒ fail closed.
type rosterPolicy struct {
	store    roleReader
	fallback allocator.StaticPolicy
}

var _ allocator.PolicySource = rosterPolicy{}

// Policy implements allocator.PolicySource.
func (p rosterPolicy) Policy(project, role string) (allocator.Policy, bool) {
	if r, ok := p.store.GetRole(project, role); ok && r.Allocation.MaxEphemeral > 0 {
		return allocator.Policy{MaxEphemeral: r.Allocation.MaxEphemeral}, true
	}
	return p.fallback.Policy(project, role)
}
