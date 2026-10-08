package adminui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// Every field that names another entity is a type-ahead. The attributes are the
// component's contract: data-ta (the suggestion kind), data-ta-list (complete
// the comma-separated token under the cursor), data-ta-eq (the kind after "="
// within a token), data-ta-project (the project to scope by; "@form" = the
// form's own project field).
func TestReferenceFieldsAreTypeaheads(t *testing.T) {
	store := seedProjects(t)
	if err := store.AddDestination(jam.Destination{Name: "git", Route: "/git/", Upstream: "https://github.com"}); err != nil {
		t.Fatal(err)
	}
	log := newIntercomLog(t, intercom.LegacySquawk{From: human("alice"), To: []intercom.Target{actor("studio-acme")}, Body: "hi", At: time.Now(), Project: "acme"})
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, log)
	for page, wants := range map[string][]string{
		"/ui/agents": { // the Raise and Enroll forms
			`name="project" data-ta="projects"`,
			`name="role" data-ta="roles" data-ta-project="@form"`,
			`name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials"`,
		},
		"/ui/projects/acme/agents": {
			`name="kit" data-ta="kits"`,
			`name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials"`,
		},
		"/ui/projects/acme/roles/dev": {
			`name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials"`,
			`name="addressing" data-ta="targets" data-ta-list data-ta-project="acme"`,
			`name="kit" data-ta="kits"`,
		},
		"/ui/destinations": {
			`name="cred-name" data-ta="credentials"`,
		},
		"/ui/projects/acme/escalation": {
			`name="tiers" data-ta="targets" data-ta-list data-ta-project="acme"`,
		},
		"/ui/projects/acme/intercom": {
			`name="service" data-ta="services"`,
		},
		"/ui/intercom": {
			`name="project" data-ta="projects"`,
			`name="participant" data-ta="participants"`,
		},
	} {
		body := get(t, h, page).Body.String()
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing type-ahead %q", page, want)
			}
		}
	}
}

// Every admin page ships the type-ahead component once, in the layout.
func TestTypeaheadComponentInLayout(t *testing.T) {
	body := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/").Body.String()
	for _, want := range []string{"/ui/suggest?kind=", `"role", "combobox"`, "aria-activedescendant"} {
		if !strings.Contains(body, want) {
			t.Errorf("layout missing type-ahead piece %q", want)
		}
	}
}
