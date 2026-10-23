package browserauth

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// devStore has users alice — login-linked and bound to an OIDC identity — and
// bob, who has neither; both members of project "proj".
func devStore(t *testing.T) jam.Store {
	return participantStore(t,
		jam.Human{Name: "alice", Login: "auth0|alice", Identity: []jam.OIDCIdentity{{Issuer: "https://idp/", Subject: "auth0|alice"}}},
		jam.Human{Name: "bob"})
}

// The deprecated {project, human} form resolves through the migration's
// legacy alias, so it acts as exactly the person that roster human became —
// not whoever now holds the name.
func TestDevIdentityLegacyAlias(t *testing.T) {
	s := jam.NewMemStore()
	err := s.ImportConfig(jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion, Projects: []jam.Project{
		{Name: "a", Roster: jam.Roster{Humans: []jam.Human{{Name: "alice", Login: "login-a"}}}},
		{Name: "b", Roster: jam.Roster{Humans: []jam.Human{{Name: "alice", Login: "login-b"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	d := DevIdentity{Store: s, LegacyProject: "b", LegacyHuman: "alice"}
	if got := d.OperatorID(); got != "login-b" {
		t.Fatalf("legacy b/alice acts as %q, want login-b (b's alice, renamed by the migration)", got)
	}
}

func TestDevIdentityParticipantAndOperatorID(t *testing.T) {
	alice := DevIdentity{Store: devStore(t), User: "alice"}
	if p, ok := alice.Participant(); !ok || p.Name != "alice" {
		t.Errorf("alice Participant = %+v, %v", p, ok)
	}
	if got := alice.OperatorID(); got != "auth0|alice" {
		t.Errorf("alice OperatorID = %q", got)
	}
	for _, name := range []string{"bob", "nobody"} {
		d := DevIdentity{Store: devStore(t), User: name}
		if _, ok := d.Participant(); ok {
			t.Errorf("%s: Participant ok without an OIDC identity", name)
		}
		if got := d.OperatorID(); got != "local" {
			t.Errorf("%s OperatorID = %q, want local", name, got)
		}
	}
}
