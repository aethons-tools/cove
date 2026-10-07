package adminui

import (
	"net/url"
	"strconv"

	"github.com/aethons-tools/cove/internal/jam"
)

// projectSection is one node of a project's tree: its Overview page or one of
// its sections. A role page sits under sectionRoles.
type projectSection string

const (
	sectionOverview   projectSection = "overview"
	sectionMembers    projectSection = "members"
	sectionAgents     projectSection = "agents"
	sectionRoles      projectSection = "roles"
	sectionIntercom   projectSection = "intercom"
	sectionEscalation projectSection = "escalation"
)

// projectSections is the tree's order, with each node's label.
var projectSections = []struct {
	Section projectSection
	Label   string
}{
	{sectionOverview, "Overview"},
	{sectionMembers, "Members"},
	{sectionAgents, "Agents"},
	{sectionRoles, "Roles"},
	{sectionIntercom, "Intercom"},
	{sectionEscalation, "Escalation"},
}

// projectSectionURL is the page path of a project's section ("" or overview:
// the project page itself).
func projectSectionURL(project string, s projectSection) string {
	base := projectURL(project)
	if s == "" || s == sectionOverview {
		return base
	}
	return base + "/" + string(s)
}

// treeLeaf is one child under a tree node: a role, an agent or a room.
type treeLeaf struct {
	Label, Href, Phase string
	Current            bool
}

// treeNode is one section in the tree with its children (none for leaf
// sections). Open renders the branch expanded: it holds the current page.
type treeNode struct {
	Section  projectSection
	Label    string
	Href     string
	Count    string // shown after the label, e.g. the live agent count
	Current  bool   // this section's own page is the current page
	Open     bool
	Children []treeLeaf
}

// projectTree is the left-side navigation on every project page.
type projectTree struct {
	Project string
	Href    string
	Current string // the current node's label, for the narrow-screen summary
	Nodes   []treeNode
	OOB     bool // rendered as an out-of-band swap of #ptree (after a write)
}

// buildProjectTree lays out project's tree with current marked: section is the
// page's section and role, on a role page, the role's name.
func buildProjectTree(store jam.Store, img jam.ImageResolver, project string, section projectSection, role string) projectTree {
	t := projectTree{Project: project, Href: projectURL(project)}
	p, _ := store.GetProject(project)
	for _, s := range projectSections {
		n := treeNode{Section: s.Section, Label: s.Label, Href: projectSectionURL(project, s.Section)}
		switch s.Section {
		case sectionRoles:
			for _, r := range store.ListRoles(project) {
				cur := section == sectionRoles && role == r.Name
				n.Children = append(n.Children, treeLeaf{Label: r.Name, Href: roleURL(project, r.Name), Current: cur})
			}
		case sectionAgents:
			live := 0
			for _, c := range jam.CoveSummaries(store, img) {
				if orDefaultProject(c.Project) != project {
					continue
				}
				if ph := jam.Phase(c.Phase); ph == jam.PhaseLive || ph == jam.PhaseRaising {
					live++
					n.Children = append(n.Children, treeLeaf{Label: c.ID, Href: "/ui/coves/" + url.PathEscape(c.ID), Phase: c.Phase})
				}
			}
			if live > 0 {
				n.Count = strconv.Itoa(live)
			}
		case sectionIntercom:
			for _, rm := range jam.ListRooms(store, p) {
				n.Children = append(n.Children, treeLeaf{Label: rm.Name, Href: "/ui/intercom?participant=" + url.QueryEscape(string(rm.ID))})
			}
		}
		n.Current = s.Section == section && role == ""
		n.Open = s.Section == section
		if n.Open {
			t.Current = s.Label
			if role != "" {
				t.Current += " ▸ " + role
			}
		}
		t.Nodes = append(t.Nodes, n)
	}
	return t
}
