package jam

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func credIs(names ...string) func(string) bool {
	return func(n string) bool {
		for _, x := range names {
			if n == x {
				return true
			}
		}
		return false
	}
}

func TestValidateDestination(t *testing.T) {
	ok := Destination{Name: "gh", Endpoints: []Endpoint{{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com"}}, CredName: "gh-pat", Env: map[string]string{"GH_HOST": "{host}"}}
	if err := ValidateDestination(ok, credIs("gh-pat"), false); err != nil {
		t.Fatalf("valid destination refused: %v", err)
	}
	for name, tc := range map[string]struct {
		d    Destination
		want string
	}{
		"no name":      {Destination{Endpoints: []Endpoint{{Route: "/x/", Upstream: "https://x"}}}, "required"},
		"no route":     {Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Upstream: "https://x"}}}, "required"},
		"no upstream":  {Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Route: "/x/"}}}, "required"},
		"unknown cred": {Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Route: "/x/", Upstream: "https://x"}}, CredName: "ghost"}, `"ghost"`},
		"reserved env": {Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Route: "/x/", Upstream: "https://x"}}, Env: map[string]string{"JAM_X": "1"}}, "reserved"},
		"bad template": {Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Route: "/x/", Upstream: "https://x"}}, Env: map[string]string{"A": "{nope}"}}, "placeholder"},
		"long note":    {Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Route: "/x/", Upstream: "https://x"}}, Note: strings.Repeat("n", MaxDestinationNote+1)}, "note"},
	} {
		err := ValidateDestination(tc.d, credIs("gh-pat"), false)
		if WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want 400 mentioning %q", name, err, tc.want)
		}
	}
}

func TestCreateAndUpdateDestination(t *testing.T) {
	st := newRoleStore(t)
	d := Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Route: "/x/", Upstream: "https://x", Git: true}}}
	must(t, CreateDestination(st, d, credIs(), false))
	if err := CreateDestination(st, d, credIs(), false); WriteStatus(err, 0) != http.StatusConflict {
		t.Fatalf("second create = %v, want 409", err)
	}
	d.Endpoints[0].Upstream = "https://y"
	must(t, UpdateDestination(st, d, credIs(), false))
	if got := st.ListDestinations(); len(got) != 1 || got[0].Endpoints[0].Upstream != "https://y" || !got[0].Endpoints[0].Git {
		t.Fatalf("after update: %+v", got)
	}
	if err := UpdateDestination(st, Destination{Name: "ghost", Endpoints: []Endpoint{{Name: "ghost", Route: "/g/", Upstream: "https://g"}}}, credIs(), false); WriteStatus(err, 0) != http.StatusNotFound {
		t.Fatalf("update missing = %v, want 404", err)
	}
	if err := UpdateDestination(st, Destination{Name: "x"}, credIs(), false); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("invalid update = %v, want 400", err)
	}
}

func TestValidateDestinationAcceptsNoteAtLimit(t *testing.T) {
	d := Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Route: "/x/", Upstream: "https://x"}}, Note: strings.Repeat("n", MaxDestinationNote)}
	if err := ValidateDestination(d, credIs("gh-pat"), false); err != nil {
		t.Fatalf("a %d-byte note must pass: %v", MaxDestinationNote, err)
	}
}

// A refusal names every problem at once, each at its admin API field path:
// the destination's fields, an endpoint's, and a header spec's own.
func TestDestinationRefusalFields(t *testing.T) {
	st := NewMemStore()
	must(t, CreateDestination(st, Destination{Name: "gh", Endpoints: []Endpoint{ep("rest", "/api/v3/")}}, credIs(), false))
	d := Destination{Name: "x", CredName: "ghost", Env: map[string]string{"JAM_X": "1"}, Endpoints: []Endpoint{
		{Name: "a", Route: "/api/v3/", Upstream: "https://x"},
		{Name: "a", Upstream: "https://x"},
		{Name: "c", Route: "/c/", Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "X-Token", Template: "nope"}, LogHeadersOut: "raw"},
	}}
	err := CreateDestination(st, d, credIs(), false)
	we, ok := errors.AsType[*WriteError](err)
	if !ok || we.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want a 400 WriteError", err)
	}
	var got []string
	for _, f := range we.Fields {
		got = append(got, f.Field)
	}
	want := []string{"credName", "env", "endpoints[1].name", "endpoints[1].route", "endpoints[2].upstream",
		"endpoints[2].applySpec.template", "endpoints[2].logHeadersOut", "endpoints[0].route"}
	if !slices.Equal(got, want) {
		t.Fatalf("fields = %q, want %q", got, want)
	}
	if !strings.Contains(err.Error(), `endpoint "c": apply_spec: template must contain {cred}`) || !strings.Contains(err.Error(), `destination "gh"'s endpoint "rest"`) {
		t.Fatalf("message = %q", err.Error())
	}
	if we.Fields[5].Message != "apply_spec: template must contain {cred} exactly once" {
		t.Fatalf("field message = %q (no endpoint prefix)", we.Fields[5].Message)
	}
}

// UpdateDestination replaces every field but the operator notes, which have
// their own write; endpoints can be added, removed and their specs edited.
func TestUpdateDestinationKeepsOperatorNotes(t *testing.T) {
	st := NewMemStore()
	must(t, CreateDestination(st, Destination{Name: "gl", Endpoints: []Endpoint{ep("gl", "/gl/")}, OperatorNotes: "rotate monthly"}, credIs(), false))
	spec := &OutboundSpec{Header: "Private-Token", Template: "{cred}"}
	edit := Destination{Name: "gl", Endpoints: []Endpoint{ep("api", "/gl/api/"), {Name: "git", Route: "/gl/", Upstream: "https://gl", Apply: ApplyCustom, ApplySpec: spec}}}
	must(t, UpdateDestination(st, edit, credIs(), false))
	got, _ := findDestination(st, "gl")
	if got.OperatorNotes != "rotate monthly" || len(got.Endpoints) != 2 || *got.Endpoints[1].ApplySpec != *spec {
		t.Fatalf("stored %+v", got)
	}
}

// A lone unnamed endpoint is stored named after its destination, on create
// and on update.
func TestDestinationWriteNamesLoneEndpoint(t *testing.T) {
	st := NewMemStore()
	must(t, CreateDestination(st, Destination{Name: "x", Endpoints: []Endpoint{ep("", "/x/")}}, credIs(), false))
	if got, _ := findDestination(st, "x"); got.Endpoints[0].Name != "x" {
		t.Fatalf("created %+v", got.Endpoints)
	}
	must(t, UpdateDestination(st, Destination{Name: "x", Endpoints: []Endpoint{ep("", "/y/")}}, credIs(), false))
	if got, _ := findDestination(st, "x"); got.Endpoints[0].Name != "x" || got.Endpoints[0].Route != "/y/" {
		t.Fatalf("updated %+v", got.Endpoints)
	}
}
