package adminui

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// methods are the ApplyMethod presets an endpoint's identity-in / apply
// offers; "custom" (an expression: the endpoint's header spec) is the other
// choice.
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
		GitRouted:  d.GitRouted(), GitImplied: d.GitRouted() && !slices.ContainsFunc(d.Endpoints, func(e jam.Endpoint) bool { return e.Git }),
		Uses: destUses(store, name),
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out.Env = append(out.Env, envRow{Key: k, Template: env[k]})
	}
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
			if gitClash(d, o) {
				out.Conflicts = append(out.Conflicts, destConflict{Project: u.Project, Role: u.Role, Other: on, What: "both route git"})
			}
		}
	}
	return out, true
}

// gitClash reports whether d and o both route git on different addresses, which
// jam.ConnectorFor refuses in one scope.
func gitClash(d, o jam.Destination) bool {
	for _, e := range d.GitEndpoints() {
		for _, f := range o.GitEndpoints() {
			if e.Subdomain != f.Subdomain || e.Route != f.Route {
				return true
			}
		}
	}
	return false
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

// destFieldsOf is d's editable fields as the API reads them: the declared
// env by key, each endpoint's every field ("none" for header logging off,
// "raw" for the raw encoding).
func destFieldsOf(d jam.Destination) DestFields {
	out := DestFields{CredName: d.CredName, Note: d.Note, Env: []EnvRow{}, Endpoints: []DestEndpoint{}}
	for _, k := range slices.Sorted(maps.Keys(d.Env)) {
		out.Env = append(out.Env, EnvRow{Key: k, Template: d.Env[k]})
	}
	for _, e := range d.Endpoints {
		x := DestEndpoint{Name: e.Name, Subdomain: e.Subdomain, Route: e.Route, Upstream: e.Upstream,
			IdentityIn: string(e.IdentityIn), Apply: string(e.Apply), AllowPaths: append([]string{}, e.AllowPaths...), Git: e.Git,
			LogHeadersIn: headerLogText(e.LogHeadersIn), LogHeadersOut: headerLogText(e.LogHeadersOut)}
		if s := e.IdentityInSpec; s != nil {
			x.IdentityInSpec = &DestInboundSpec{Header: s.Header, Prefixes: append([]string{}, s.Prefixes...), Encoding: encodingText(s.Encoding)}
		}
		if s := e.ApplySpec; s != nil {
			x.ApplySpec = &DestOutboundSpec{Header: s.Header, Template: s.Template, Encoding: encodingText(s.Encoding), BasicUser: s.BasicUser}
		}
		out.Endpoints = append(out.Endpoints, x)
	}
	return out
}

// headerLogText is a header-logging level as the API reads it: "none" when off.
func headerLogText(h jam.HeaderLog) string {
	if h == "" {
		return string(jam.HeaderLogNone)
	}
	return string(h)
}

// encodingText is a header spec's encoding as the API reads it: "raw" for the
// default.
func encodingText(e jam.HeaderEncoding) string {
	if e == jam.EncodingRaw {
		return "raw"
	}
	return string(e)
}

// destOf is the destination f writes, named name; text fields are trimmed,
// "none" header logging and the "raw" encoding are stored unset, an empty
// env is none (the legacy default). A blank or repeated env key is refused
// at its row (env[i].key); every other rule is jam.ValidateDestination's.
func destOf(f DestFields, name string) (jam.Destination, error) {
	d := jam.Destination{Name: name, CredName: strings.TrimSpace(f.CredName), Note: strings.TrimSpace(f.Note)}
	var fe sessionctx.FieldErrors
	for i, r := range f.Env {
		k := strings.TrimSpace(r.Key)
		switch _, dup := d.Env[k]; {
		case k == "":
			fe = append(fe, sessionctx.FieldError{Field: fmt.Sprintf("env[%d].key", i), Message: "env key is required"})
		case dup:
			fe = append(fe, sessionctx.FieldError{Field: fmt.Sprintf("env[%d].key", i), Message: "env key " + k + " is set twice"})
		default:
			if d.Env == nil {
				d.Env = map[string]string{}
			}
			d.Env[k] = strings.TrimSpace(r.Template)
		}
	}
	if len(fe) > 0 {
		return jam.Destination{}, &jam.WriteError{Status: http.StatusBadRequest, Msg: fe.Error(), Fields: fe}
	}
	for _, x := range f.Endpoints {
		e := jam.Endpoint{Name: strings.TrimSpace(x.Name), Subdomain: strings.TrimSpace(x.Subdomain), Route: strings.TrimSpace(x.Route),
			Upstream: strings.TrimSpace(x.Upstream), IdentityIn: jam.ApplyMethod(strings.TrimSpace(x.IdentityIn)), Apply: jam.ApplyMethod(strings.TrimSpace(x.Apply)),
			Git: x.Git, LogHeadersIn: headerLogOf(x.LogHeadersIn), LogHeadersOut: headerLogOf(x.LogHeadersOut)}
		for _, p := range x.AllowPaths {
			if p = strings.TrimSpace(p); p != "" {
				e.AllowPaths = append(e.AllowPaths, p)
			}
		}
		if s := x.IdentityInSpec; s != nil {
			e.IdentityInSpec = &jam.InboundSpec{Header: strings.TrimSpace(s.Header), Prefixes: slices.Clone(s.Prefixes), Encoding: encodingOf(s.Encoding)}
		}
		if s := x.ApplySpec; s != nil {
			e.ApplySpec = &jam.OutboundSpec{Header: strings.TrimSpace(s.Header), Template: s.Template, Encoding: encodingOf(s.Encoding), BasicUser: strings.TrimSpace(s.BasicUser)}
		}
		d.Endpoints = append(d.Endpoints, e)
	}
	return d, nil
}

// headerLogOf reads a header-logging level: "none" (or empty) is unset.
func headerLogOf(v string) jam.HeaderLog {
	if v = strings.TrimSpace(v); v == string(jam.HeaderLogNone) {
		return ""
	}
	return jam.HeaderLog(v)
}

// encodingOf reads a header spec's encoding: "raw" (or empty) is unset.
func encodingOf(v string) jam.HeaderEncoding {
	if v = strings.TrimSpace(v); v == "raw" {
		return jam.EncodingRaw
	}
	return jam.HeaderEncoding(v)
}
