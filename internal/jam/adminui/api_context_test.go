package adminui_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// contextScope is one context endpoint, with how to read its card out of a
// write's answer.
type contextScope struct {
	name, path string
	panel      func(body []byte) adminui.ContextPanel
}

func decodePanel[T any](get func(T) adminui.ContextPanel) func([]byte) adminui.ContextPanel {
	return func(b []byte) adminui.ContextPanel {
		var v T
		_ = json.Unmarshal(b, &v)
		return get(v)
	}
}

var allContextScopes = []contextScope{
	{"jam", "/app/admin/api/jam/context", decodePanel(func(p adminui.ContextPanel) adminui.ContextPanel { return p })},
	{"project", "/app/admin/api/projects/acme/context", decodePanel(func(d adminui.ProjectDetail) adminui.ContextPanel { return d.Context })},
	{"role", "/app/admin/api/projects/acme/roles/coder/context", decodePanel(func(r adminui.RoleResult) adminui.ContextPanel { return r.Role.Context })},
}

func TestContextAPIStructuredSave(t *testing.T) {
	seed := newStore(t)
	mustCreateProject(t, seed, "acme")
	h := editOn(t, editSetup{seed: seed}).H
	if rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles", map[string]any{"name": "coder"}, nil); rec.Code != 201 {
		t.Fatalf("create role = %d %s", rec.Code, rec.Body)
	}
	for _, s := range allContextScopes {
		save := adminui.ContextSave{Core: "Ship it.", Leaves: []adminui.ContextLeafInput{{Name: "release.md", ReadWhen: "you cut a release", Body: "Tag from main."}}}
		if s.name == "project" {
			save.Resources = []adminui.ContextResource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove"}}
		}
		rec := apiDo(t, h, "POST", s.path, save, nil)
		if rec.Code != 200 {
			t.Fatalf("%s: structured save = %d %s", s.name, rec.Code, rec.Body)
		}
		p := s.panel(rec.Body.Bytes())
		if p.Core != "Ship it." || len(p.Leaves) != 1 || p.Leaves[0].Body != "Tag from main." || p.Leaves[0].Bytes != len("Tag from main.") || p.LeafBytes != p.Leaves[0].Bytes {
			t.Errorf("%s: panel = %+v", s.name, p)
		}
		if p.Limits.CoreBudget != p.Budget || p.Limits.MaxReadWhen != sessionctx.MaxReadWhen || p.Limits.MaxLeaves != sessionctx.MaxLeaves || p.Limits.MaxLeafBytes != sessionctx.MaxLeafBytes {
			t.Errorf("%s: limits = %+v", s.name, p.Limits)
		}
		if wantRes := map[bool]int{true: sessionctx.MaxResources}[s.name == "project"]; p.Limits.MaxResources != wantRes {
			t.Errorf("%s: maxResources = %d, want %d", s.name, p.Limits.MaxResources, wantRes)
		}
		// The delivered core adds a pointer line per leaf (and the project's resources leaf).
		pointers := len(fmt.Sprintf("- %s/release.md — read when you cut a release\n", s.name))
		if s.name == "project" {
			pointers += len("- project/resources.md — read when you need the project's repos, docs or trackers\n")
			if len(p.Resources) != 1 {
				t.Errorf("project: resources = %+v", p.Resources)
			}
		}
		if want := p.CoreBytes + 1 + pointers; p.DeliveredBytes != want {
			t.Errorf("%s: deliveredBytes = %d, want %d", s.name, p.DeliveredBytes, want)
		}

		// YAML still saves, and replaces the structured layer.
		rec = apiDo(t, h, "POST", s.path, map[string]string{"yaml": "core: from yaml\n"}, nil)
		if p := s.panel(rec.Body.Bytes()); rec.Code != 200 || p.Core != "from yaml" || len(p.Leaves) != 0 {
			t.Errorf("%s: yaml save = %d %s", s.name, rec.Code, rec.Body)
		}
		// Both at once, or nothing at all, is refused.
		wantErr(t, apiDo(t, h, "POST", s.path, map[string]string{"yaml": "core: x", "core": "y"}, nil), 400, "bad_request")
		wantErr(t, apiDo(t, h, "POST", s.path, map[string]any{"leaves": []any{}}, nil), 400, "bad_request")
	}
}

func TestContextAPIFieldErrors(t *testing.T) {
	seed := newStore(t)
	mustCreateProject(t, seed, "acme")
	h := editOn(t, editSetup{seed: seed}).H
	if rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles", map[string]any{"name": "coder"}, nil); rec.Code != 201 {
		t.Fatalf("create role = %d %s", rec.Code, rec.Body)
	}
	for _, s := range allContextScopes {
		save := adminui.ContextSave{
			Core: strings.Repeat("x", sessionctx.BudgetProject+1),
			Leaves: []adminui.ContextLeafInput{
				{Name: "a.md", ReadWhen: "w"},
				{Name: "a.md", ReadWhen: "w"},
				{Name: "ok.md", ReadWhen: strings.Repeat("w", sessionctx.MaxReadWhen+1)},
			},
			Resources: []adminui.ContextResource{{Name: "r", Kind: "wiki", Ref: "x"}},
		}
		want := []string{"core", "leaves[1].name", "leaves[2].readWhen", "resources[0].kind"}
		if s.name != "project" {
			want = []string{"resources", "core", "leaves[1].name", "leaves[2].readWhen"}
		}
		rec := apiDo(t, h, "POST", s.path, save, nil)
		var e adminui.APIError
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || rec.Code != 400 || e.Code != "bad_request" {
			t.Fatalf("%s: = %d %s", s.name, rec.Code, rec.Body)
		}
		var got []string
		for _, f := range e.Errors {
			if f.Message == "" {
				t.Errorf("%s: %s has no message", s.name, f.Field)
			}
			got = append(got, f.Field)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: fields = %v, want %v", s.name, got, want)
		}
		if !strings.Contains(e.Error, "appears twice") {
			t.Errorf("%s: error = %q, want every problem", s.name, e.Error)
		}
	}
}

func TestContextAPIYAMLFieldErrors(t *testing.T) {
	h := editOn(t, editSetup{}).H
	rec := apiDo(t, h, "POST", "/app/admin/api/jam/context", map[string]string{"yaml": "core: hi\nleaves:\n  - {name: Bad.md}\n"}, nil)
	var e adminui.APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || rec.Code != 400 {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
	var got []string
	for _, f := range e.Errors {
		got = append(got, f.Field)
	}
	if want := "[leaves[0].name leaves[0].readWhen]"; fmt.Sprint(got) != want {
		t.Errorf("fields = %v, want %s", got, want)
	}
}
