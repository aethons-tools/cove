package adminui

import "github.com/aethons-tools/cove/internal/jam"

// stats is the dashboard's summary counts. Attention is studios a human may
// need to look at: lost (reconciler declared dead) or terminating (teardown in
// flight).
type stats struct {
	Studios, Live, Raising, Idled, Attention int
	Projects, Agents, Users, Specs           int // Specs: kits + destinations + model-specs
}

// dashboardStats counts the fleet by phase and the control-plane objects.
func dashboardStats(store jam.Store) stats {
	var s stats
	for _, c := range jam.CoveSummaries(store, nil) {
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
	s.Projects = len(store.ListProjects())
	s.Agents = len(agentRows(store, nil, ""))
	for _, u := range store.ListUsers() {
		if u.Status != jam.StatusRemoved {
			s.Users++
		}
	}
	s.Specs = len(store.ListKits()) + len(store.ListDestinations()) + len(store.ListModelSpecs())
	return s
}
