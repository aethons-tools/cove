package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/jam"
)

// roleReader is the slice of jam.Store the roster policy reads.
type roleReader interface {
	GetRole(project, name string) (jam.Role, bool)
	GetProject(ref string) (jam.Project, bool)
}

// rosterPolicy is the Allocator's PolicySource: the roster Role is the source of
// truth (read live from the memory-cached store on each grant, so a role edit
// takes effect on the next grant with no restart); the Requisitioner's
// max-concurrent is the fallback ephemeral cap for its own (project, role) when
// the Role sets no max-ephemeral (fallback is empty when Jam serves without a
// Requisitioner). The personal caps and the standing names come only from the
// Role. No role and no fallback ⇒ no policy ⇒ fail closed.
type rosterPolicy struct {
	store    roleReader
	fallback allocator.StaticPolicy
}

var _ allocator.PolicySource = rosterPolicy{}

// Policy implements allocator.PolicySource.
func (p rosterPolicy) Policy(project, role string) (allocator.Policy, bool) {
	// The fallback is keyed by the Requisitioner project's id (by name in a
	// hand-built table).
	fb, hasFallback := p.fallback.Policy(project, role)
	if id, ok := jam.ProjectIDOf(p.store, project); ok && !hasFallback {
		if fb, hasFallback = p.fallback.Policy(string(id), role); !hasFallback {
			fb, hasFallback = p.fallback.Policy(jam.ProjectName(p.store, project), role)
		}
	}
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
		pol.MaxEphemeral = fb.MaxEphemeral // Requisitioner fallback (0 without one)
	}
	if pol.MaxEphemeral <= 0 && pol.MaxPersonal <= 0 && pol.MaxPersonalPerOwner <= 0 && len(pol.StandingNames) == 0 {
		return pol, false // the role sets nothing and there is no fallback
	}
	return pol, true
}

// newRosterPolicy builds the Allocator's roster-sourced policy. The Requisitioner's
// max-concurrent seeds the ephemeral fallback for its own (project, role) only
// when a Requisitioner is configured (dc != nil); without one the roster alone
// decides. The Requisitioner project is normalized (see requisitionerProject) so
// grants and releases land on the same (project, role) stream.
// project is the Requisitioner's project as resolveRequisitionerProject
// returns it.
func newRosterPolicy(store roleReader, dc *requisitionerConfig, project string) rosterPolicy {
	p := rosterPolicy{store: store}
	if dc != nil {
		p.fallback = allocator.StaticPolicy{{Project: project, Role: dc.Role}: {MaxEphemeral: dc.MaxConcurrent}}
	}
	return p
}

// projectCreator is the slice of jam.Store resolveRequisitionerProject uses.
type projectCreator interface {
	GetProject(ref string) (jam.Project, bool)
	CreateProject(name string) error
}

// resolveRequisitionerProject is the Requisitioner's project as its id —
// stable across a rename while serve runs — creating the default project if
// the config names it (or none) and it doesn't exist yet. A named project
// that doesn't exist stays as named (its raises fail until it is created).
func resolveRequisitionerProject(st projectCreator, dc *requisitionerConfig, log *slog.Logger) string {
	name := requisitionerProject(dc)
	if _, ok := st.GetProject(name); !ok && name == jam.DefaultProject {
		if err := st.CreateProject(name); err != nil && log != nil {
			log.Warn("requisitioner: creating the default project failed", "err", err.Error())
		}
	}
	if id, ok := jam.ProjectIDOf(st, name); ok {
		return string(id)
	}
	if log != nil {
		log.Warn("requisitioner: its project doesn't exist; raises fail until it is created", "project", name)
	}
	return name
}

// requisitionerProject is the Requisitioner's project by name, normalized
// ("" is jam.DefaultProject): it keys the fallback policy. Ledger streams are
// keyed by the project's id either way (the Allocator's project key), so a
// grant by name and a release by the instance's id meet on one stream.
func requisitionerProject(dc *requisitionerConfig) string {
	if dc.Project == "" {
		return jam.DefaultProject
	}
	return dc.Project
}

// personalAllocator adapts *allocator.Allocator to jam.SessionAllocator (Jam
// does not import allocator): a personal grant is a SessionPersonal request owned
// by the requester, and the allocator's no-ledger error becomes
// jam.ErrNeedsLedger so the admin route can answer 409.
type personalAllocator struct{ a *allocator.Allocator }

var _ jam.SessionAllocator = personalAllocator{}

// GrantPersonal implements jam.SessionAllocator.
func (p personalAllocator) GrantPersonal(ctx context.Context, project, role, reservationID, owner string) (bool, error) {
	ok, err := p.a.Grant(ctx, allocator.Request{
		Project: project, Role: role, ReservationID: reservationID,
		Kind: allocator.SessionPersonal, Owner: owner,
	})
	if errors.Is(err, allocator.ErrNeedsLedger) {
		return false, jam.ErrNeedsLedger
	}
	return ok, err
}

// RecordRelease implements jam.SessionAllocator.
func (p personalAllocator) RecordRelease(ctx context.Context, project, role, reservationID string) error {
	return p.a.RecordRelease(ctx, project, role, reservationID)
}

// ledgerRefs maps every project's and live user's name to its id, for
// re-keying allocation events an older Jam recorded by name.
func ledgerRefs(st jam.Store) (projects, owners map[string]string) {
	projects, owners = map[string]string{}, map[string]string{}
	for _, name := range st.ListProjects() {
		if p, ok := st.GetProject(name); ok && p.ID != "" {
			projects[name] = string(p.ID)
		}
	}
	for _, u := range st.ListUsers() {
		if u.Status == jam.StatusLive {
			owners[u.Name] = string(u.ID)
		}
	}
	return projects, owners
}

// personalOwners maps each personal session's reservation (its actor id) to
// its owner's user id, so the ledger counts it against the right owner
// whatever name it was recorded under.
func personalOwners(st jam.Store) map[string]string {
	out := map[string]string{}
	for _, inst := range st.ListInstances() {
		if inst.SessionKind == jam.SessionKindPersonal && inst.OwnerID != "" {
			out[inst.ActorID] = string(inst.OwnerID)
		}
	}
	return out
}
