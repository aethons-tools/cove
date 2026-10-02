package adminui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

func credAny(string) bool { return true }

// seedDestinations stores the legacy anthropic and git destinations, the gh
// pair with env, and gh-alt whose GH_HOST conflicts with github-api. Role dev
// holds github-api and gh-alt (a conflict); ops holds only github-api.
func seedDestinations(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	for _, d := range []jam.Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: jam.ApplyBearer, CredName: "anth-key", Apply: jam.ApplyBearer, OAuthBeta: true},
		{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: jam.ApplyBasicPassword, CredName: "git-pat", Apply: jam.ApplyBasicPassword},
		{Name: "github-api", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: jam.ApplyBearer, CredName: "gh-pat", Apply: jam.ApplyBearer,
			Env: map[string]string{"GH_HOST": "{host}", "GH_ENTERPRISE_TOKEN": "{token}"}},
		{Name: "gh-alt", Route: "/alt/", Upstream: "https://alt.example", Env: map[string]string{"GH_HOST": "{base}"}},
	} {
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []jam.Role{
		{Name: "dev", Scope: jam.Scope{Destinations: []string{"github-api", "gh-alt"}, Credentials: map[string]string{"github-api": "gh-pat-acme"}}},
		{Name: "ops", Scope: jam.Scope{Destinations: []string{"github-api", "anthropic"}}},
	} {
		if err := store.PutRole("acme", r); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestDestinationsListShowsConnectorAndUse(t *testing.T) {
	body := get(t, adminui.Handler(seedDestinations(t), testLogger(), nil, nil, credAny, nil), "/ui/destinations").Body.String()
	for _, want := range []string{
		`href="/ui/destinations/github-api"`,
		"gh-pat",             // default credential
		"GH_HOST",            // env key chip
		"ANTHROPIC_BASE_URL", // legacy-implied env still listed
		"oauth-beta",         // flag
		`data-used-by="2"`,   // github-api is used by dev and ops
		"bearer → bearer",    // identity-in → apply
	} {
		if !strings.Contains(body, want) {
			t.Errorf("destinations list missing %q", want)
		}
	}
}

func TestDestinationDetailEnvRolesAndConflicts(t *testing.T) {
	rec := get(t, adminui.Handler(seedDestinations(t), testLogger(), nil, nil, credAny, nil), "/ui/destinations/github-api")
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<h1>github-api</h1>", "/api/v3/", "https://api.github.com",
		"GH_HOST", "{host}", "GH_ENTERPRISE_TOKEN", "{token}",
		`href="/ui/roles/acme/dev"`, `href="/ui/roles/acme/ops"`,
		"gh-pat-acme", // dev's own mapping
		`class="banner error conflict"`, "gh-alt",
		`aria-current="page">Destinations`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("github-api detail missing %q", want)
		}
	}
	if n := strings.Count(body, `class="conflict-role mono"`); n != 1 {
		t.Errorf("want exactly one conflicting role (dev), got %d", n)
	}
}

func TestDestinationDetailLegacyDefaults(t *testing.T) {
	h := adminui.Handler(seedDestinations(t), testLogger(), nil, nil, credAny, nil)
	anth := get(t, h, "/ui/destinations/anthropic").Body.String()
	for _, want := range []string{"ANTHROPIC_BASE_URL", "{base}/anthropic", "ANTHROPIC_AUTH_TOKEN", "implied by the /anthropic/ route", "oauth-beta"} {
		if !strings.Contains(anth, want) {
			t.Errorf("anthropic detail missing %q", want)
		}
	}
	git := get(t, h, "/ui/destinations/git").Body.String()
	if !strings.Contains(git, "implied by the /git/ route") {
		t.Errorf("git detail should explain implied git routing")
	}
}

func TestDestinationDetailNotFound(t *testing.T) {
	rec := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, credAny, nil), "/ui/destinations/nope")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<nav") {
		t.Fatalf("missing destination = %d", rec.Code)
	}
}

func TestDestinationEditFormPrefilled(t *testing.T) {
	body := get(t, adminui.Handler(seedDestinations(t), testLogger(), nil, nil, credAny, nil), "/ui/destinations/github-api").Body.String()
	for _, want := range []string{
		`hx-post="/ui/destinations/github-api"`,
		"GH_ENTERPRISE_TOKEN={token}\nGH_HOST={host}",
		`name="upstream" value="https://api.github.com"`,
		`name="cred-name" value="gh-pat"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("edit form missing %q", want)
		}
	}
}

func TestCreateDestinationAllFields(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credAny, nil)
	rec := post(t, h, "/ui/destinations", url.Values{
		"name": {"gh"}, "route": {"/api/v3/"}, "upstream": {"https://api.github.com"},
		"identity-in": {"bearer"}, "cred-name": {"gh-pat"}, "apply": {"bearer"},
		"env": {"GH_HOST={host}\r\n\r\nGH_ENTERPRISE_TOKEN={token}\n"}, "git": {"on"}, "oauth-beta": {"on"},
	})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/destinations/gh" {
		t.Fatalf("create = %d redirect=%q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	d := store.ListDestinations()[0]
	if d.Env["GH_HOST"] != "{host}" || d.Env["GH_ENTERPRISE_TOKEN"] != "{token}" || len(d.Env) != 2 || !d.Git || !d.OAuthBeta {
		t.Fatalf("stored = %+v", d)
	}
	if rec := post(t, h, "/ui/destinations", url.Values{"name": {"gh"}, "route": {"/x/"}, "upstream": {"https://x"}}); rec.Code != http.StatusConflict {
		t.Errorf("re-create = %d, want 409", rec.Code)
	}
	for name, env := range map[string]string{"no equals": "GH_HOST", "reserved": "AT_JAM_X=1", "bad placeholder": "A={nope}", "duplicate": "A=1\nA=2"} {
		if rec := post(t, h, "/ui/destinations", url.Values{"name": {"n"}, "route": {"/n/"}, "upstream": {"https://n"}, "env": {env}}); rec.Code != http.StatusBadRequest {
			t.Errorf("env %s = %d, want 400", name, rec.Code)
		}
	}
}

func TestEditDestinationReplacesEveryField(t *testing.T) {
	store := seedDestinations(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credAny, nil)
	rec := post(t, h, "/ui/destinations/github-api", url.Values{
		"route": {"/api/v3/"}, "upstream": {"https://ghe.example/api/v3"},
		"identity-in": {"bearer"}, "cred-name": {"other"}, "apply": {"bearer"},
		"env": {"GH_HOST={host}"}, // git and oauth-beta unchecked
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="dest"`) {
		t.Fatalf("edit = %d: %s", rec.Code, rec.Body.String())
	}
	var got jam.Destination
	for _, d := range store.ListDestinations() {
		if d.Name == "github-api" {
			got = d
		}
	}
	if got.Upstream != "https://ghe.example/api/v3" || got.CredName != "other" || len(got.Env) != 1 || got.Git || got.OAuthBeta {
		t.Fatalf("after edit = %+v", got)
	}
	if rec := post(t, h, "/ui/destinations/ghost", url.Values{"route": {"/g/"}, "upstream": {"https://g"}}); rec.Code != http.StatusNotFound {
		t.Errorf("edit missing = %d, want 404", rec.Code)
	}
}
