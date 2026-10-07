package adminui

// projectSection is one of a project's tabs: its Overview page or one of its
// sections. A role page sits under sectionRoles.
type projectSection string

const (
	sectionOverview   projectSection = "overview"
	sectionMembers    projectSection = "members"
	sectionAgents     projectSection = "agents"
	sectionRoles      projectSection = "roles"
	sectionIntercom   projectSection = "intercom"
	sectionEscalation projectSection = "escalation"
)

// projectSections is a project's tab order, with each tab's label.
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
