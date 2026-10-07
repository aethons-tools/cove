package adminui

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// destRow is one destination in a role's scope with the credential the broker
// would inject for it: the role's own mapping, else the destination's default.
// Credential names are references, not secret values, so they are shown.
type destRow struct {
	Name       string
	Cred       string
	CredIsRole bool // Cred comes from the role's mapping, not the destination default
	Route      string
	Missing    bool // not a registered destination
}

// standingRow is one declared standing session and its studio's phase, if any.
type standingRow struct {
	Name, Prompt, ActorID, Phase string
	Image                        string // the running studio's image status (CoveSummary.Image)
	Upgrade                      string // its pending upgrade state ("" none)
}

// holderRow is one actor holding a grant on the role.
type holderRow struct {
	ID       string
	Override bool // the grant replaces part of the role's scope
}

// setting is an allocation value with how an unset value reads.
type setting struct {
	Label string
	Value string
	Unset bool
}

// roleDetail is the role page's payload.
type roleDetail struct {
	Title, Project, Name string
	Tree                 projectTree
	Crumbs               []crumb
	WriteBase            string // the role's write endpoints (roleWriteBase)
	RolesHref            string // the project's Roles section (where a delete lands)
	Role                 jam.Role
	Dests                []destRow
	EgressManaged        bool
	Allocation           []setting
	Standing             []standingRow
	Holders              []holderRow
	Coves                []jam.CoveSummary
	Form                 roleForm // current values in the edit forms' syntax
	CanRequest           bool
	CanEdit              bool // always false: the role page's studio table is read-only
	NotFound             bool
	Context              contextPanel // the role's session-context card
}

// buildRoleDetail gathers everything about one role; false when it doesn't exist.
func buildRoleDetail(store jam.Store, img jam.ImageResolver, project, name string) (roleDetail, bool) {
	role, ok := store.GetRole(project, name)
	if !ok {
		return roleDetail{}, false
	}
	project = orDefaultProject(project)
	d := roleDetail{Title: name, Project: project, Name: name, Role: role, EgressManaged: role.Scope.Egress != nil, Form: newRoleForm(role),
		WriteBase: roleWriteBase(project, name), RolesHref: projectSectionURL(project, sectionRoles)}
	d.Context = newContextPanel("role", "/ui/roles/"+project+"/"+name+"/context", "role", role.Context, nil, sessionctx.BudgetRole, false)

	dests := map[string]jam.Destination{}
	for _, x := range store.ListDestinations() {
		dests[x.Name] = x
	}
	for _, n := range role.Scope.Destinations {
		x, known := dests[n]
		row := destRow{Name: n, Route: x.Route, Missing: !known, Cred: x.CredName}
		if c := role.Scope.Credentials[n]; c != "" {
			row.Cred, row.CredIsRole = c, true
		}
		d.Dests = append(d.Dests, row)
	}

	a := role.Allocation
	d.Allocation = []setting{
		count("Max ephemeral sessions", a.MaxEphemeral, "unset (requisitioner limit)"),
		count("Max personal sessions", a.MaxPersonal, "0 (no personal sessions)"),
		count("Max personal per owner", a.MaxPersonalPerOwner, "unset (pool cap only)"),
		duration("Idle after", a.IdleAfter, "default ("+fmtDur(jam.DefaultIdleAfter)+")"),
		duration("Nag every", a.NagEvery, "default ("+fmtDur(jam.DefaultNagEvery)+")"),
		duration("Reclaim after", a.ReclaimAfter, "never"),
		duration("Idle timeout", role.TurnEnd.IdleTimeout, "none"),
		{Label: "On idle", Value: role.TurnEnd.Action()},
		{Label: "Alarm time zone", Value: role.TurnEnd.Location().String()},
	}

	running := map[string]jam.CoveSummary{}
	for _, c := range jam.CoveSummaries(store, img) {
		running[c.ID] = c
		if orDefaultProject(c.Project) == project && c.Role == name {
			d.Coves = append(d.Coves, c)
		}
	}
	for _, s := range a.Standing {
		id := jam.StandingSessionOf(store, project, name, s.Name)
		row := standingRow{Name: s.Name, Prompt: s.Prompt, ActorID: id, Phase: running[id].Phase, Image: running[id].Image}
		if u, ok := img.(interface {
			StandingUpgradeState(project, role, name string) string
		}); ok {
			row.Upgrade = u.StandingUpgradeState(project, name, s.Name)
		}
		d.Standing = append(d.Standing, row)
	}

	for _, act := range store.ListActors() {
		for _, g := range act.Grants {
			if jam.ProjectName(store, g.Project) == project && g.Role == name {
				d.Holders = append(d.Holders, holderRow{ID: act.ID, Override: g.Overrides != nil})
				break
			}
		}
	}
	return d, true
}

func count(label string, n int, unset string) setting {
	if n == 0 {
		return setting{Label: label, Value: unset, Unset: true}
	}
	return setting{Label: label, Value: strconv.Itoa(n)}
}

func duration(label string, v time.Duration, unset string) setting {
	if v == 0 {
		return setting{Label: label, Value: unset, Unset: true}
	}
	return setting{Label: label, Value: fmtDur(v)}
}

// roleURL is the detail page path for a role.
// roleURL is a role's page, under its project.
func roleURL(project, name string) string {
	return projectSectionURL(project, sectionRoles) + "/" + url.PathEscape(name)
}

// roleWriteBase is the prefix of a role's write endpoints, which keep their
// pre-tree paths.
func roleWriteBase(project, name string) string {
	return "/ui/roles/" + url.PathEscape(orDefaultProject(project)) + "/" + url.PathEscape(name)
}

func handleRoleDetail(w http.ResponseWriter, r *http.Request, store jam.Store, img jam.ImageResolver, canRequest bool) {
	project, name := r.PathValue("project"), r.PathValue("name")
	d, ok := buildRoleDetail(store, img, project, name)
	if !ok {
		renderStatus(w, http.StatusNotFound, "role", roleDetail{Title: "Role not found", NotFound: true,
			Project: project, Name: name, RolesHref: projectSectionURL(project, sectionRoles)})
		return
	}
	d.Tree = buildProjectTree(store, img, project, sectionRoles, name)
	d.Crumbs = projectCrumbs(project, sectionRoles, name)
	d.CanRequest = canRequest
	render(w, "role", d)
}
