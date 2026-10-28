package adminui

import (
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// methods are the ApplyMethod choices for identity-in / apply selects: the
// presets. "custom" is offered (selected) only on a destination that already
// uses it — custom specs are set via the admin API / `destination import` and
// shown read-only here.
var methods = jam.Presets

// destListRow is one destinations-table row.
type destListRow struct {
	jam.Destination
	EnvKeys   []string // keys of the effective client env (declared or legacy-implied)
	GitRouted bool
	UsedBy    int // roles whose scope lists it
}

// envRow is one effective client-env entry, {url} already resolved.
type envRow struct{ Key, Template string }

// destUse is one role that lists the destination, with the credential it
// injects there.
type destUse struct {
	Project, Role, Cred string
	CredIsRole          bool
}

// destConflict is one role whose scope would fail connector assembly because
// of this destination: another destination in the scope sets an env key
// differently, or also routes git.
type destConflict struct {
	Project, Role, Other, What string
}

// destForm is the destination in the edit form's input syntax.
type destForm struct {
	Env        string // KEY=TEMPLATE lines, the declared env only
	AllowPaths string // one pattern per line
}

// destDetail is the destination page payload.
type destDetail struct {
	Title       string
	Dest        jam.Destination
	Env         []envRow
	EnvImplied  bool // no env declared; the route's legacy default applies
	GitRouted   bool
	GitImplied  bool // routed only by the legacy /git/ rule
	Uses        []destUse
	Conflicts   []destConflict
	Form        destForm
	Methods     []jam.ApplyMethod
	NotFound    bool
	NotFoundFor string
}

// destUses lists every role whose scope includes name, project then role order.
func destUses(store jam.Store, name string) []destUse {
	var out []destUse
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			if slices.Contains(r.Scope.Destinations, name) {
				c := r.Scope.Credentials[name]
				out = append(out, destUse{Project: p, Role: r.Name, Cred: c, CredIsRole: c != ""})
			}
		}
	}
	return out
}

func destListRows(store jam.Store) []destListRow {
	used := map[string]int{}
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			for _, d := range r.Scope.Destinations {
				used[d]++
			}
		}
	}
	var out []destListRow
	for _, d := range store.ListDestinations() {
		out = append(out, destListRow{
			Destination: d,
			EnvKeys:     slices.Sorted(maps.Keys(d.ClientEnv())),
			GitRouted:   d.GitRouted(),
			UsedBy:      used[d.Name],
		})
	}
	return out
}

// buildDestDetail gathers one destination's page; false when it doesn't exist.
func buildDestDetail(store jam.Store, name string) (destDetail, bool) {
	all := map[string]jam.Destination{}
	for _, d := range store.ListDestinations() {
		all[d.Name] = d
	}
	d, ok := all[name]
	if !ok {
		return destDetail{}, false
	}
	env := d.ClientEnv()
	out := destDetail{
		Title: "Destinations", Dest: d, Methods: methods,
		EnvImplied: d.Env == nil && len(env) > 0,
		GitRouted:  d.GitRouted(), GitImplied: d.GitRouted() && !d.Git,
		Uses: destUses(store, name),
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out.Env = append(out.Env, envRow{Key: k, Template: env[k]})
	}
	var lines []string
	for _, k := range slices.Sorted(maps.Keys(d.Env)) {
		lines = append(lines, k+"="+d.Env[k])
	}
	out.Form.Env = strings.Join(lines, "\n")
	out.Form.AllowPaths = strings.Join(d.AllowPaths, "\n")

	// Flag (never block) roles whose connector assembly this destination breaks,
	// mirroring jam.ConnectorFor over the role's own scope.
	for _, u := range out.Uses {
		r, _ := store.GetRole(u.Project, u.Role)
		for _, on := range r.Scope.Destinations {
			o, ok := all[on]
			if !ok || on == name {
				continue
			}
			oenv := o.ClientEnv()
			for _, k := range slices.Sorted(maps.Keys(env)) {
				if v, set := oenv[k]; set && v != env[k] {
					out.Conflicts = append(out.Conflicts, destConflict{Project: u.Project, Role: u.Role, Other: on, What: "both set " + k + " (to different values)"})
				}
			}
			if d.GitRouted() && o.GitRouted() && d.Route != o.Route {
				out.Conflicts = append(out.Conflicts, destConflict{Project: u.Project, Role: u.Role, Other: on, What: "both route git"})
			}
		}
	}
	return out, true
}

// ConflictRoles is the distinct roles among the conflicts, for marking each once.
func (d destDetail) ConflictRoles() []string {
	var out []string
	for _, c := range d.Conflicts {
		if k := c.Project + "/" + c.Role; !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// destURL is the detail page path for a destination.
func destURL(name string) string { return "/app/admin/destinations/" + url.PathEscape(name) }

// parseEnv reads the env textarea: one KEY=TEMPLATE per line, blank lines
// skipped. Empty means no declared env (the route's legacy default applies).
func parseEnv(s string) (map[string]string, error) {
	var env map[string]string
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, badRequest("env line " + `"` + line + `"` + " is not KEY=TEMPLATE")
		}
		if _, dup := env[k]; dup {
			return nil, badRequest("env key " + k + " is set twice")
		}
		if env == nil {
			env = map[string]string{}
		}
		env[k] = strings.TrimSpace(v)
	}
	return env, nil
}

// parseAllowPaths reads the allow-paths textarea: one pattern per line, blank
// lines skipped. Empty means any path.
func parseAllowPaths(s string) []string {
	var out []string
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// destFromFields reads every destination field but the name from its form
// fields (get reads one by its form name).
func destFromFields(get func(string) string, name string) (jam.Destination, error) {
	env, err := parseEnv(get("env"))
	if err != nil {
		return jam.Destination{}, err
	}
	return jam.Destination{
		Name:       name,
		Route:      strings.TrimSpace(get("route")),
		Upstream:   strings.TrimSpace(get("upstream")),
		IdentityIn: jam.ApplyMethod(strings.TrimSpace(get("identity-in"))),
		CredName:   strings.TrimSpace(get("cred-name")),
		Apply:      jam.ApplyMethod(strings.TrimSpace(get("apply"))),
		Env:        env,
		AllowPaths: parseAllowPaths(get("allow-paths")),
		Git:        get("git") != "",
		Note:       strings.TrimSpace(get("note")),
		// "none" is the default; store it as unset.
		LogHeadersIn:  headerLogField(get("log-headers-in")),
		LogHeadersOut: headerLogField(get("log-headers-out")),
	}, nil
}

// headerLogField reads a header-logging select: "none" (or empty) is unset.
func headerLogField(v string) jam.HeaderLog {
	if v = strings.TrimSpace(v); v == string(jam.HeaderLogNone) {
		return ""
	}
	return jam.HeaderLog(v)
}
