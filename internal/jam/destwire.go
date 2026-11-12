package jam

import (
	"encoding/json"
	"errors"
	"reflect"

	"gopkg.in/yaml.v3"
)

// A destination has two encodings, in JSON (the store's documents, backups,
// the admin API) and YAML (config-repo files) alike:
//
//   - the flat form, the only one before endpoints existed: the endpoint's
//     fields inline beside the destination's, meaning one endpoint named after
//     the destination;
//   - the list form: the destination's fields and an endpoints: list.
//
// Both decode. A destination with one endpoint named after it (or none)
// encodes flat, byte for byte as before endpoints, so existing files, store
// documents and backups neither change nor read as changed; any other encodes
// as a list.

// destinationFlat is the flat form, its fields in their historical order.
type destinationFlat struct {
	Name            string            `json:"name"                       yaml:"name"`
	Subdomain       string            `json:"subdomain,omitempty"        yaml:"subdomain,omitempty"`
	Route           string            `json:"route"                      yaml:"route"`
	Upstream        string            `json:"upstream"                   yaml:"upstream"`
	IdentityIn      ApplyMethod       `json:"identity_in"                yaml:"identity_in"`
	CredName        string            `json:"cred_name"                  yaml:"cred_name"`
	Apply           ApplyMethod       `json:"apply"                      yaml:"apply"`
	IdentityInSpec  *InboundSpec      `json:"identity_in_spec,omitempty" yaml:"identity_in_spec,omitempty"`
	ApplySpec       *OutboundSpec     `json:"apply_spec,omitempty"       yaml:"apply_spec,omitempty"`
	LegacyOAuthBeta bool              `json:"oauth_beta,omitempty"       yaml:"oauth_beta,omitempty"`
	Env             map[string]string `json:"env,omitempty"              yaml:"env,omitempty"`
	Git             bool              `json:"git,omitempty"              yaml:"git,omitempty"`
	AllowPaths      []string          `json:"allow_paths,omitempty"      yaml:"allow_paths,omitempty"`
	Note            string            `json:"note,omitempty"             yaml:"note,omitempty"`
	LogHeadersIn    HeaderLog         `json:"log_headers_in,omitempty"   yaml:"log_headers_in,omitempty"`
	LogHeadersOut   HeaderLog         `json:"log_headers_out,omitempty"  yaml:"log_headers_out,omitempty"`
	OperatorNotes   string            `json:"operator_notes,omitempty"   yaml:"operator-notes,omitempty"`
}

// destinationList is the list form: Destination's own fields and tags.
type destinationList Destination

// destinationWire decodes either form.
type destinationWire struct {
	destinationFlat `yaml:",inline"`
	Endpoints       []Endpoint `json:"endpoints,omitempty" yaml:"endpoints,omitempty"`
}

// errFlatAndEndpoints refuses a destination mixing the two forms.
var errFlatAndEndpoints = errors.New("route, upstream and the other endpoint fields go on an endpoint when endpoints: is given")

// encodesFlat reports whether d encodes in the flat form.
func (d Destination) encodesFlat() bool {
	return len(d.Endpoints) == 0 || len(d.Endpoints) == 1 && (d.Endpoints[0].Name == "" || d.Endpoints[0].Name == d.Name)
}

// wire is d in the form it encodes as.
func (d Destination) wire() any {
	if !d.encodesFlat() {
		return destinationList(d)
	}
	e := d.Primary()
	return destinationFlat{
		Name: d.Name, Subdomain: e.Subdomain, Route: e.Route, Upstream: e.Upstream, IdentityIn: e.IdentityIn,
		CredName: d.CredName, Apply: e.Apply, IdentityInSpec: e.IdentityInSpec, ApplySpec: e.ApplySpec,
		LegacyOAuthBeta: d.LegacyOAuthBeta, Env: d.Env, Git: e.Git, AllowPaths: e.AllowPaths, Note: d.Note,
		LogHeadersIn: e.LogHeadersIn, LogHeadersOut: e.LogHeadersOut, OperatorNotes: d.OperatorNotes,
	}
}

// destination is the decoded destination, normalized: a flat form is one
// endpoint (none when its endpoint fields are all empty), named after the
// destination once its name is known (normalize).
func (w destinationWire) destination() (Destination, error) {
	f := w.destinationFlat
	d := Destination{
		Name: f.Name, CredName: f.CredName, LegacyOAuthBeta: f.LegacyOAuthBeta, Env: f.Env,
		Note: f.Note, OperatorNotes: f.OperatorNotes, Endpoints: w.Endpoints,
	}
	flat := Endpoint{
		Subdomain: f.Subdomain, Route: f.Route, Upstream: f.Upstream, IdentityIn: f.IdentityIn, Apply: f.Apply,
		IdentityInSpec: f.IdentityInSpec, ApplySpec: f.ApplySpec, Git: f.Git, AllowPaths: f.AllowPaths,
		LogHeadersIn: f.LogHeadersIn, LogHeadersOut: f.LogHeadersOut,
	}
	hasFlat := !reflect.ValueOf(flat).IsZero()
	switch {
	case hasFlat && len(w.Endpoints) > 0:
		return Destination{}, errFlatAndEndpoints
	case hasFlat:
		d.Endpoints = []Endpoint{flat}
	}
	d.normalize()
	return d, nil
}

// MarshalJSON encodes d flat when it can (see the top of this file).
func (d Destination) MarshalJSON() ([]byte, error) { return json.Marshal(d.wire()) }

// UnmarshalJSON decodes either form.
func (d *Destination) UnmarshalJSON(b []byte) error {
	var w destinationWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	out, err := w.destination()
	if err != nil {
		return err
	}
	*d = out
	return nil
}

// MarshalYAML encodes d flat when it can (see the top of this file).
func (d Destination) MarshalYAML() (any, error) { return d.wire(), nil }

// UnmarshalYAML decodes either form. (A config file is decoded strictly by
// ParseDestinationFile instead.)
func (d *Destination) UnmarshalYAML(n *yaml.Node) error {
	var w destinationWire
	if err := n.Decode(&w); err != nil {
		return err
	}
	out, err := w.destination()
	if err != nil {
		return err
	}
	*d = out
	return nil
}
