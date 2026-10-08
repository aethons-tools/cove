package adminui

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// The UI's Delete rule mirrors the store's refusal: whenever projectRef names
// nothing, RemoveProject succeeds, and whenever it names something, it refuses.
func TestProjectRefMirrorsRemoveProject(t *testing.T) {
	setups := map[string]func(t *testing.T, s jam.Store){
		"empty": func(*testing.T, jam.Store) {},
		"a member": func(t *testing.T, s jam.Store) {
			if err := jam.AddPerson(s, "acme", jam.Human{Name: "alice"}); err != nil {
				t.Fatal(err)
			}
		},
		"a standing-session entry": func(t *testing.T, s jam.Store) {
			p, _ := s.GetProject("acme")
			if err := s.PutStandingSession(p.ID, "dev", "bot", "ses_01j9q3zzzzzzzzzzzzzzzzzz"); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			s := jam.NewMemStore()
			if err := s.CreateProject("acme"); err != nil {
				t.Fatal(err)
			}
			setup(t, s)
			ref := projectRef(s, "acme")
			err := s.RemoveProject("acme")
			if (ref == "") != (err == nil) {
				t.Fatalf("projectRef = %q but RemoveProject = %v", ref, err)
			}
		})
	}
}
