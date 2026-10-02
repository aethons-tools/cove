package adminui

import "github.com/aethons-tools/cove/internal/jam"

// stats is the dashboard's summary counts. Attention is studios a human may
// need to look at: lost (reconciler declared dead) or terminating (teardown in
// flight).
type stats struct {
	Studios, Live, Raising, Idled, Attention int
	Actors, Roles, Kits, Destinations        int
}

// dashboardStats counts the fleet by phase and the control-plane objects.
func dashboardStats(store jam.Store) stats {
	var s stats
	for _, c := range jam.CoveSummaries(store) {
		s.Studios++
		switch jam.Phase(c.Phase) {
		case jam.PhaseLive:
			s.Live++
		case jam.PhaseRaising:
			s.Raising++
		case jam.PhaseIdled:
			s.Idled++
		case jam.PhaseLost, jam.PhaseTerminating:
			s.Attention++
		}
	}
	s.Actors = len(store.ListActors())
	for _, p := range store.ListProjects() {
		s.Roles += len(store.ListRoles(p))
	}
	s.Kits = len(store.ListKits())
	s.Destinations = len(store.ListDestinations())
	return s
}
