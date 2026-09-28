package jam

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

// fakeRoster is a participantReader over an in-memory set of project rosters.
type fakeRoster struct{ projects map[string]Roster }

func (f fakeRoster) ListProjects() []string {
	// Deterministic order (ParticipantByIdentity records Projects in this order).
	names := make([]string, 0, len(f.projects))
	for name := range f.projects {
		names = append(names, name)
	}
	// small insertion sort to avoid importing sort in a test helper is overkill;
	// use the stdlib.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	return names
}

func (f fakeRoster) GetRoster(project string) (Roster, bool) {
	r, ok := f.projects[project]
	return r, ok
}

func TestParticipantByIdentity(t *testing.T) {
	const idp = "https://idp.example"
	store := fakeRoster{projects: map[string]Roster{
		"alpha": {Humans: []Human{
			{Name: "alice", Handle: "alice", Identity: []OIDCIdentity{{Issuer: idp, Subject: "sub-alice"}}},
			{Name: "bob", Identity: []OIDCIdentity{{Issuer: idp, Subject: "sub-bob"}}},
		}},
		"beta": {Humans: []Human{
			// same person as alpha/alice — global person, bound in a second project.
			{Name: "alice-b", Handle: "aliceb", Identity: []OIDCIdentity{{Issuer: idp, Subject: "sub-alice"}}},
			// same subject string but a DIFFERENT issuer → a different person.
			{Name: "carol", Identity: []OIDCIdentity{{Issuer: "https://other", Subject: "sub-alice"}}},
		}},
	}}

	tests := []struct {
		name         string
		issuer, sub  string
		wantOK       bool
		wantProjects []string
		wantName     string
	}{
		{"unmapped subject fails closed", idp, "sub-nobody", false, nil, ""},
		{"empty issuer fails closed", "", "sub-alice", false, nil, ""},
		{"empty subject fails closed", idp, "", false, nil, ""},
		{"global person spans projects", idp, "sub-alice", true, []string{"alpha", "beta"}, "alice"},
		{"single project", idp, "sub-bob", true, []string{"alpha"}, "bob"},
		{"issuer must match, not just subject", "https://other", "sub-alice", true, []string{"beta"}, "carol"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := ParticipantByIdentity(store, tc.issuer, tc.sub)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if p.Issuer != tc.issuer || p.Subject != tc.sub {
				t.Errorf("identity = %q/%q, want %q/%q", p.Issuer, p.Subject, tc.issuer, tc.sub)
			}
			if !reflect.DeepEqual(p.Projects, tc.wantProjects) {
				t.Errorf("Projects = %v, want %v", p.Projects, tc.wantProjects)
			}
			if p.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", p.Name, tc.wantName)
			}
		})
	}
}

func TestParticipantContextRoundTrip(t *testing.T) {
	r := httptest.NewRequest("GET", "/me/", nil)
	if _, ok := ParticipantFrom(r); ok {
		t.Fatal("bare request should carry no participant")
	}
	want := Participant{Issuer: "https://idp.example", Subject: "sub-alice", Projects: []string{"alpha"}, Name: "alice"}
	r = WithParticipant(r, want)
	got, ok := ParticipantFrom(r)
	if !ok {
		t.Fatal("participant not found after WithParticipant")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("participant = %+v, want %+v", got, want)
	}
}
