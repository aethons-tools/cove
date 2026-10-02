package jam

import (
	"net/http"
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
	ok := Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", CredName: "gh-pat", Env: map[string]string{"GH_HOST": "{host}"}}
	if err := ValidateDestination(ok, credIs("gh-pat")); err != nil {
		t.Fatalf("valid destination refused: %v", err)
	}
	for name, tc := range map[string]struct {
		d    Destination
		want string
	}{
		"no name":      {Destination{Route: "/x/", Upstream: "https://x"}, "required"},
		"no route":     {Destination{Name: "x", Upstream: "https://x"}, "required"},
		"no upstream":  {Destination{Name: "x", Route: "/x/"}, "required"},
		"unknown cred": {Destination{Name: "x", Route: "/x/", Upstream: "https://x", CredName: "ghost"}, `"ghost"`},
		"reserved env": {Destination{Name: "x", Route: "/x/", Upstream: "https://x", Env: map[string]string{"AT_JAM_X": "1"}}, "reserved"},
		"bad template": {Destination{Name: "x", Route: "/x/", Upstream: "https://x", Env: map[string]string{"A": "{nope}"}}, "placeholder"},
		"long note":    {Destination{Name: "x", Route: "/x/", Upstream: "https://x", Note: strings.Repeat("n", MaxDestinationNote+1)}, "note"},
	} {
		err := ValidateDestination(tc.d, credIs("gh-pat"))
		if WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want 400 mentioning %q", name, err, tc.want)
		}
	}
}

func TestCreateAndUpdateDestination(t *testing.T) {
	st := newRoleStore(t)
	d := Destination{Name: "x", Route: "/x/", Upstream: "https://x", Git: true}
	must(t, CreateDestination(st, d, credIs()))
	if err := CreateDestination(st, d, credIs()); WriteStatus(err, 0) != http.StatusConflict {
		t.Fatalf("second create = %v, want 409", err)
	}
	d.Upstream = "https://y"
	must(t, UpdateDestination(st, d, credIs()))
	if got := st.ListDestinations(); len(got) != 1 || got[0].Upstream != "https://y" || !got[0].Git {
		t.Fatalf("after update: %+v", got)
	}
	if err := UpdateDestination(st, Destination{Name: "ghost", Route: "/g/", Upstream: "https://g"}, credIs()); WriteStatus(err, 0) != http.StatusNotFound {
		t.Fatalf("update missing = %v, want 404", err)
	}
	if err := UpdateDestination(st, Destination{Name: "x"}, credIs()); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("invalid update = %v, want 400", err)
	}
}

func TestValidateDestinationAcceptsNoteAtLimit(t *testing.T) {
	d := Destination{Name: "x", Route: "/x/", Upstream: "https://x", Note: strings.Repeat("n", MaxDestinationNote)}
	if err := ValidateDestination(d, credIs("gh-pat")); err != nil {
		t.Fatalf("a %d-byte note must pass: %v", MaxDestinationNote, err)
	}
}
