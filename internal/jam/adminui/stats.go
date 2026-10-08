package adminui

import (
	"slices"

	"github.com/aethons-tools/cove/internal/jam"
)

// stats is the dashboard's summary counts: studios by status group (Attention
// is lost or terminating — a human may need to look) and the control-plane
// objects.
type stats struct {
	Studios, Running, Waiting, SettingUp, Idled, Attention int
	Projects, Agents, Users, Specs                         int // Specs: kits + destinations + model-specs
}

// dashboardStats counts the fleet by status group (statusGroups) and the
// control-plane objects.
func dashboardStats(store jam.Store) stats {
	var s stats
	count := map[string]*int{"running": &s.Running, "waiting": &s.Waiting, "setting-up": &s.SettingUp, "idled": &s.Idled, "attention": &s.Attention}
	for _, c := range jam.CoveSummaries(store, nil) {
		s.Studios++
		st := jam.StatusOf(jam.Phase(c.Phase), jam.Activity(c.Activity), true)
		for g, members := range statusGroups {
			if slices.Contains(members, st) {
				*count[g]++
			}
		}
	}
	s.Projects = len(store.ListProjects())
	s.Agents = len(agentRows(store, nil, ""))
	s.Users = len(store.ListUsers()) // ListUsers returns live users only
	s.Specs = len(store.ListKits()) + len(store.ListDestinations()) + len(store.ListModelSpecs())
	return s
}
