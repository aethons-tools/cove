package adminui_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// managerImages is an image reader that knows its managers (as the
// router does): it has no image, and only names are registered.
type managerImages struct{ names []string }

func (m managerImages) ImageFactsOn(context.Context, string, string) (jam.ImageFacts, error) {
	return jam.ImageFacts{}, jam.ErrImageNotPresent
}

func (m managerImages) ManagerErr(name string) error {
	if slices.Contains(m.names, name) {
		return nil
	}
	return fmt.Errorf("%w: %s", jam.ErrUnknownManager, name)
}

// A studio's setup takes ?diff=role only; any other diff is refused, as is a
// role's ?diff at all.
func TestSetupDiffQuery(t *testing.T) {
	h, _ := setupStudioFixture(t)
	rec := apiDo(t, h, "GET", "/app/admin/api/agents/sess-1/setup?diff=kit", nil, nil)
	wantErr(t, rec, 400, "bad_request")
	if !strings.Contains(rec.Body.String(), `diff: want \"role\"`) {
		t.Errorf("bad diff = %s", rec.Body)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/projects/acme/roles/dev/setup?diff=role", nil, nil), 400, "bad_request")
}

// WithSetupManagers offers a role's setup the managers to read image facts
// from when there are two or more: the asked one is current, else the
// first; with one there is no choice.
func TestRoleSetupManagers(t *testing.T) {
	build := func(names ...string) http.Handler {
		store := newStore(t)
		mustCreateProject(t, store, "acme")
		if err := store.PutRole("acme", jam.Role{Name: "dev"}); err != nil {
			t.Fatal(err)
		}
		return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil,
			adminui.WithSetupDeps(jam.SetupDeps{Store: store, Images: managerImages{names}}),
			adminui.WithSetupManagers(func() []string { return names }))
	}
	const path = "/app/admin/api/projects/acme/roles/dev/setup"
	h := build("local", "m1")
	var s adminui.SetupResponse
	if rec := apiDo(t, h, "GET", path+"?manager=m1", nil, &s); rec.Code != 200 {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	if strings.Join(s.Managers, ",") != "local,m1" || s.Manager != "m1" {
		t.Errorf("manager=m1: managers %v current %q", s.Managers, s.Manager)
	}
	wantErr(t, apiDo(t, h, "GET", path+"?manager=nope", nil, nil), 404, "not_found")
	s = adminui.SetupResponse{}
	apiDo(t, h, "GET", path, nil, &s)
	if s.Manager != "local" || len(s.Managers) != 2 {
		t.Errorf("no manager chosen: managers %v current %q, want the first", s.Managers, s.Manager)
	}
	one := build("local")
	s = adminui.SetupResponse{}
	apiDo(t, one, "GET", path, nil, &s)
	if len(s.Managers) != 0 || s.Manager != "" {
		t.Errorf("one manager: managers %v current %q, want no choice", s.Managers, s.Manager)
	}
}
