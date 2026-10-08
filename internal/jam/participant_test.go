package jam

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

// participantStore is a MemStore holding projects alpha and beta and the
// users the participant tests resolve: alice (member of both — a global
// person), bob (alpha), carol (beta; the same subject string as alice at a
// different issuer) and dave (bound, but a member of no project).
func participantStore(t *testing.T, idp string) Store {
	t.Helper()
	s := NewMemStore()
	mustCreateProject(t, s, "alpha", "beta")
	for _, u := range []struct {
		name, issuer, sub string
		projects          []string
	}{
		{"alice", idp, "sub-alice", []string{"alpha", "beta"}},
		{"bob", idp, "sub-bob", []string{"alpha"}},
		{"carol", "https://other", "sub-alice", []string{"beta"}},
		{"dave", idp, "sub-dave", nil},
	} {
		created, err := s.CreateUser(User{Name: u.name, OIDC: []OIDCIdentity{{Issuer: u.issuer, Subject: u.sub}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range u.projects {
			p, _ := s.GetProject(name)
			if err := s.AddMember(p.ID, created.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s
}

func TestParticipantByIdentity(t *testing.T) {
	const idp = "https://idp.example"
	store := participantStore(t, idp)

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
		{"a user in no project is no participant", idp, "sub-dave", false, nil, ""},
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
			if id, _ := store.LookupName(ident.User, tc.wantName); p.UserID != id {
				t.Errorf("UserID = %q, want %q", p.UserID, id)
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
