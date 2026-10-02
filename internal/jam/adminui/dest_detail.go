package adminui

import (
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// methods are the ApplyMethod choices for identity-in / apply selects.
var methods = []jam.ApplyMethod{jam.ApplyBearer, jam.ApplyBasicPassword, jam.ApplyXAPIKey}

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
	Env string // KEY=TEMPLATE lines, the declared env only
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
func destURL(name string) string { return "/ui/destinations/" + url.PathEscape(name) }

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

// destFromForm reads every destination field but the name from the form.
func destFromForm(r *http.Request, name string) (jam.Destination, error) {
	env, err := parseEnv(r.FormValue("env"))
	if err != nil {
		return jam.Destination{}, err
	}
	return jam.Destination{
		Name:       name,
		Route:      strings.TrimSpace(r.FormValue("route")),
		Upstream:   strings.TrimSpace(r.FormValue("upstream")),
		IdentityIn: jam.ApplyMethod(strings.TrimSpace(r.FormValue("identity-in"))),
		CredName:   strings.TrimSpace(r.FormValue("cred-name")),
		Apply:      jam.ApplyMethod(strings.TrimSpace(r.FormValue("apply"))),
		Env:        env,
		Git:        r.FormValue("git") != "",
		OAuthBeta:  r.FormValue("oauth-beta") != "",
		Note:       strings.TrimSpace(r.FormValue("note")),
	}, nil
}

func destTableData(store jam.Store) map[string]any {
	return map[string]any{
		"Destinations": destListRows(store),
		// New seeds the create form: no name yet, bearer → bearer by default.
		"New": destDetail{Methods: methods, Dest: jam.Destination{IdentityIn: jam.ApplyBearer, Apply: jam.ApplyBearer}},
	}
}

func registerDestinations(mux *http.ServeMux, store jam.Store, log *slog.Logger, credExists func(string) bool, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("GET /ui/destinations", func(w http.ResponseWriter, r *http.Request) {
		data := destTableData(store)
		data["Title"] = "Destinations"
		render(w, "destinations", data)
	})

	mux.HandleFunc("GET /ui/destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := buildDestDetail(store, r.PathValue("name"))
		if !ok {
			renderStatus(w, http.StatusNotFound, "destination", destDetail{Title: "Destinations", NotFound: true, NotFoundFor: r.PathValue("name")})
			return
		}
		render(w, "destination", d)
	})

	// Create only: an existing destination is edited on its page, where every
	// field is pre-filled (so nothing the form can't see is dropped).
	mux.HandleFunc("POST /ui/destinations", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		d, err := destFromForm(r, name)
		if err == nil {
			err = jam.CreateDestination(store, d, credExists)
		}
		if err != nil {
			msg := err.Error()
			if jam.WriteStatus(err, 0) == http.StatusConflict {
				msg += "; edit it on its page"
			}
			renderError(w, jam.WriteStatus(err, http.StatusInternalServerError), msg)
			return
		}
		log.Info("ui destination added", "operator", jam.OperatorID(r), "name", d.Name, "route", d.Route, "upstream", d.Upstream)
		w.Header().Set("HX-Redirect", destURL(d.Name))
		renderFragment(w, "destinations", "destinations-table", destTableData(store))
	})

	mux.HandleFunc("POST /ui/destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		d, err := destFromForm(r, r.PathValue("name"))
		if err == nil {
			err = jam.UpdateDestination(store, d, credExists)
		}
		if err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusInternalServerError), err.Error())
			return
		}
		log.Info("ui destination updated", "operator", jam.OperatorID(r), "name", d.Name, "route", d.Route, "upstream", d.Upstream)
		detail, ok := buildDestDetail(store, d.Name)
		if !ok {
			renderError(w, http.StatusNotFound, "destination no longer exists")
			return
		}
		renderFragment(w, "destination", "dest-body", detail)
	})

	mux.HandleFunc("DELETE /ui/destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := store.RemoveDestination(r.PathValue("name")); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui destination removed", "operator", jam.OperatorID(r), "name", r.PathValue("name"))
		renderFragment(w, "destinations", "destinations-table", destTableData(store))
	})
}
