package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestStudioFactsFromScope(t *testing.T) {
	_, store, _ := supTestKit(t, &fakeLauncher{})
	for _, d := range []Destination{
		{Name: "github-api", Route: "/api/v3/", Upstream: "https://api.github.com", CredName: "gh-pat-SECRETNAME",
			Env: map[string]string{"GH_HOST": "{host}", "GH_ENTERPRISE_TOKEN": "{token}"}, Note: "pass -R $GH_HOST/o/r"},
		{Name: "git", Route: "/git/", Upstream: "https://github.com", Git: true},
	} {
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	a := Actor{ID: "w1", Grants: []Grant{{Project: "default", Role: "dev"}}}
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{Destinations: []string{"github-api", "git", "ghost"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	f := studioFacts(store, a, "", nil, []string{"github.com", "api.anthropic.com"}, true, time.Now())
	if len(f.Destinations) != 2 || f.Destinations[0].Name != "git" || f.Destinations[1].Name != "github-api" {
		t.Fatalf("destinations = %+v (sorted, unknown 'ghost' skipped)", f.Destinations)
	}
	gh := f.Destinations[1]
	if strings.Join(gh.EnvKeys, ",") != "GH_ENTERPRISE_TOKEN,GH_HOST" || gh.Note != "pass -R $GH_HOST/o/r" || !f.Destinations[0].Git {
		t.Fatalf("destination facts wrong: %+v", f.Destinations)
	}
	if !f.EgressKnown || strings.Join(f.Egress, ",") != "github.com" {
		t.Fatalf("egress = %v known=%v; want the kit ceiling (anthropic excluded)", f.Egress, f.EgressKnown)
	}
	b := sessionctx.Compile(sessionctx.Inputs{Session: sessionctx.SessionFacts{Kind: "standing", Name: "n"}, Studio: f})
	all := b.Core
	for _, body := range b.Files {
		all += body
	}
	for _, secret := range []string{"gh-pat-SECRETNAME", "{token}", "{host}"} {
		if strings.Contains(all, secret) {
			t.Errorf("bundle leaks %q", secret)
		}
	}
}

func TestStudioFactsEgressSources(t *testing.T) {
	_, store, _ := supTestKit(t, &fakeLauncher{})
	a := Actor{ID: "w1"}
	if f := studioFacts(store, a, "", &EgressPolicy{Domains: []string{"b.com", "a.com"}}, []string{"x.com"}, true, time.Now()); strings.Join(f.Egress, ",") != "a.com,b.com" || !f.EgressKnown {
		t.Errorf("role policy wins, sorted: %v", f.Egress)
	}
	if f := studioFacts(store, a, "", &EgressPolicy{Domains: []string{}}, []string{"x.com"}, true, time.Now()); len(f.Egress) != 0 || !f.EgressKnown {
		t.Errorf("empty policy = known, no hosts: %+v", f)
	}
	if f := studioFacts(store, a, "", nil, nil, false, time.Now()); f.EgressKnown {
		t.Errorf("no policy and no kit = unknown: %+v", f)
	}
}
