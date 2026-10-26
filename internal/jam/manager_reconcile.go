package jam

import (
	"cmp"
	"slices"
)

// ReconcileManager compares a connecting manager's inventory with Jam's
// records for it. Live or Idled instances the manager no longer reports (by
// actor and location) are marked Lost, so the next Reconcile tears them down.
// Raising instances are left to the raise path. Unknown studios are returned
// as strays and never torn down. Pinned standing sessions whose state the
// manager lacks are returned in MissingState; the pin is kept.
func (s *Supervisor) ReconcileManager(name string, inv ManagerInventory) ManagerReconcile {
	var out ManagerReconcile
	reported := make(map[ManagedStudio]bool, len(inv.Studios))
	for _, st := range inv.Studios {
		reported[st] = true
	}

	known := map[ManagedStudio]bool{} // non-Gone instances on name
	raising := map[string]bool{}      // actors whose raise is in flight
	var candidates []Instance
	for _, inst := range s.store.ListInstances() {
		if inst.Manager != name || inst.Phase == PhaseGone {
			continue
		}
		switch inst.Phase {
		case PhaseRaising:
			raising[inst.ActorID] = true
			continue
		case PhaseLive, PhaseIdled:
			candidates = append(candidates, inst)
		}
		known[ManagedStudio{ActorID: inst.ActorID, Location: inst.Location}] = true
	}

	slices.SortFunc(candidates, func(a, b Instance) int { return cmp.Compare(a.ActorID, b.ActorID) })
	for _, inst := range candidates {
		if reported[ManagedStudio{ActorID: inst.ActorID, Location: inst.Location}] {
			continue
		}
		cur, ok := s.patchInstance(inst.ActorID, func(c *Instance) {
			if c.Phase == PhaseLive || c.Phase == PhaseIdled {
				c.Phase = PhaseLost
			}
		})
		if !ok || cur.Phase != PhaseLost {
			continue
		}
		out.Lost = append(out.Lost, inst.ActorID)
		s.warn("studio missing from its manager's inventory; marked lost", "id", inst.ActorID, "manager", name)
	}

	for _, st := range inv.Studios {
		if known[st] || raising[st.ActorID] {
			continue
		}
		out.Strays = append(out.Strays, st)
		s.warn("manager reports a studio Jam does not know; left running", "manager", name, "actor", st.ActorID, "location", st.Location)
	}
	slices.SortFunc(out.Strays, func(a, b ManagedStudio) int {
		return cmp.Or(cmp.Compare(a.ActorID, b.ActorID), cmp.Compare(a.Location, b.Location))
	})

	owners := make(map[string]bool, len(inv.StateOwners))
	for _, o := range inv.StateOwners {
		owners[o] = true
	}
	for _, ref := range s.store.ListStandingSessions() {
		if ref.Manager != name || owners[ref.SessionID] {
			continue
		}
		out.MissingState = append(out.MissingState, ref.SessionID)
		s.warn("pinned standing session's state is missing on its manager; it will start fresh there", "manager", name, "session", ref.SessionID)
	}
	slices.Sort(out.MissingState)
	return out
}
