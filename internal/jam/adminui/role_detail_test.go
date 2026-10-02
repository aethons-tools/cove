package adminui_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// seedRichRole stores acme/review with every field set, a destination whose
// default credential applies, a holder with an override, a running standing
// session and an ordinary studio of the role.
func seedRichRole(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	for _, d := range []jam.Destination{
		{Name: "git", Route: "/git/", Upstream: "https://git.example", CredName: "git-default"},
		{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", CredName: "anth-key"},
	} {
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	role := jam.Role{
		Name: "review",
		Scope: jam.Scope{
			Destinations: []string{"git", "anthropic"},
			Credentials:  map[string]string{"git": "git-pat"},
			Addressing:   []string{"human:*", "channel:eng"},
			TTL:          90 * time.Minute,
			Egress:       &jam.EgressPolicy{Domains: []string{"pypi.org"}},
		},
		Allocation: jam.RoleAllocation{
			MaxPersonal: 2,
			NagEvery:    2 * time.Hour,
			Standing:    []jam.StandingSession{{Name: "nightly", Prompt: "run the nightly sweep"}},
		},
	}
	if err := store.PutRole("acme", role); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "other"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []jam.Actor{
		{ID: "holder-plain", TokenHash: "h1", Grants: []jam.Grant{{Project: "acme", Role: "review"}}},
		{ID: "holder-ovr", TokenHash: "h2", Grants: []jam.Grant{{Project: "acme", Role: "review", Overrides: &jam.Override{Destinations: []string{"git"}}}}},
		{ID: "not-a-holder", TokenHash: "h3", Grants: []jam.Grant{{Project: "acme", Role: "other"}}},
	} {
		if err := store.AddActor(a); err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range []jam.Instance{
		{ActorID: jam.StandingActorID("acme", "review", "nightly"), Project: "acme", Role: "review", Name: "nightly", Phase: jam.PhaseLive},
		{ActorID: "studio-of-review", Project: "acme", Role: "review", Phase: jam.PhaseLive},
		{ActorID: "studio-of-other", Project: "acme", Role: "other", Phase: jam.PhaseLive},
	} {
		if err := store.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestRoleDetailShowsEverything(t *testing.T) {
	h := adminui.Handler(seedRichRole(t), testLogger(), nil, nil, anyCred, nil)
	rec := get(t, h, "/ui/roles/acme/review")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET role detail = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<h1>review</h1>",
		"git-pat",                // the role's own credential mapping
		"anth-key",               // the destination default that applies
		"human:*", "channel:eng", // addressing
		"pypi.org",     // managed egress
		"1h30m",        // TTL
		"default (4h)", // idle-after unset → default
		"2h",           // nag-every set
		"never",        // reclaim-after unset
		"nightly", "run the nightly sweep",
		"holder-plain", "holder-ovr", "override",
		"studio-of-review",
		`aria-current="page">Roles`, // still under the Roles tab
	} {
		if !strings.Contains(body, want) {
			t.Errorf("role detail missing %q", want)
		}
	}
	for _, gone := range []string{"not-a-holder", "studio-of-other", `hx-trigger="every 3s"`} {
		if strings.Contains(body, gone) {
			t.Errorf("role detail should not contain %q", gone)
		}
	}
}

func TestRoleDetailUnmanagedEgressAndEmptyAddressing(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", jam.Role{Name: "bare"}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/roles/acme/bare").Body.String()
	for _, want := range []string{"Kit default", "No comms targets"} {
		if !strings.Contains(body, want) {
			t.Errorf("bare role detail missing %q", want)
		}
	}
}

func TestRoleDetailNotFound(t *testing.T) {
	rec := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/roles/acme/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing role = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<nav") {
		t.Errorf("not-found should still render the page chrome")
	}
}

func TestRoleLinksFromRolesAndRoster(t *testing.T) {
	h := adminui.Handler(seedRichRole(t), testLogger(), nil, nil, anyCred, nil)
	roles := get(t, h, "/ui/roles").Body.String()
	if !strings.Contains(roles, `href="/ui/roles/acme/review"`) {
		t.Errorf("roles table should link to the detail page")
	}
	if !strings.Contains(roles, "git-pat") {
		t.Errorf("roles table should show the credential mapping")
	}
	roster := get(t, h, "/ui/roster").Body.String()
	if !strings.Contains(roster, `href="/ui/roles/acme/review"`) {
		t.Errorf("roster grant chips should link to the role")
	}
}
