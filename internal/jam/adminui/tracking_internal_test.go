package adminui

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestObjectPath(t *testing.T) {
	st := jam.NewMemStore()
	bob, err := st.CreateUser(jam.User{Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ kind, name, want string }{
		{jam.ConfigKindDestination, "gh", "config/destinations/gh"},
		{jam.ConfigKindModelSpec, "fast", "config/model-specs/fast"},
		{jam.ConfigKindUser, "bob", "config/users/" + string(bob.ID)},
		{jam.ConfigKindUser, "gone", "config/users/gone"},
		{jam.ConfigKindKit, "web dev", "config/kits/web%20dev"},
		{jam.ConfigKindProject, "app", "config/projects/app"},
		{jam.ConfigKindRole, "app/ic one", "config/projects/app/agents/ic%20one"},
		{jam.ConfigKindJamContext, "jam", ""},
	} {
		if got := objectPath(st, c.kind, c.name); got != c.want {
			t.Errorf("objectPath(%s, %s) = %q, want %q", c.kind, c.name, got, c.want)
		}
	}
}
