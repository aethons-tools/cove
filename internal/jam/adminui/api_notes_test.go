package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

// Each object's notes are a branch write: committed in its file, shown
// rendered on its page, and kept by the form saves that don't carry them.
func TestOperatorNotesAPI(t *testing.T) {
	e := editOn(t, editSetup{opts: []adminui.Option{adminui.WithCredentialNames("anthropic")}})
	h := e.H
	for _, c := range []struct{ path string }{
		{"/app/admin/api/destinations"}, {"/app/admin/api/model-specs"}, {"/app/admin/api/users"}, {"/app/admin/api/projects"},
	} {
		body := map[string]string{"name": "acme"}
		switch c.path {
		case "/app/admin/api/destinations":
			body = destBody(nil)
		case "/app/admin/api/model-specs":
			body = specBody(nil)
		case "/app/admin/api/users":
			body = map[string]string{"name": "bob"}
		}
		if rec := apiDo(t, h, "POST", c.path, body, nil); rec.Code != 201 {
			t.Fatalf("create %s = %d %s", c.path, rec.Code, rec.Body)
		}
	}
	if rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles", map[string]any{"name": "coder", "destinations": "", "ttlSeconds": "3600"}, nil); rec.Code != 201 {
		t.Fatalf("create role = %d %s", rec.Code, rec.Body)
	}

	notes := map[string]string{"notes": "Ask **ops** first."}
	for _, c := range []struct{ api, file string }{
		{"destinations/gh", "config/destinations/gh.yml"},
		{"model-specs/fast", "config/model-specs/fast.yml"},
		{"users/bob", "config/users/bob.yml"},
		{"projects/acme", "config/projects/acme/project.yml"},
		{"projects/acme/roles/coder", "config/projects/acme/roles/coder.yml"},
	} {
		var got adminui.OperatorNotes
		if rec := apiDo(t, h, "POST", "/app/admin/api/"+c.api+"/operator-notes", notes, &got); rec.Code != 200 {
			t.Fatalf("%s = %d %s", c.api, rec.Code, rec.Body)
		}
		if got.Markdown != notes["notes"] || !strings.Contains(got.HTML, "<strong>ops</strong>") {
			t.Errorf("%s answered %+v", c.api, got)
		}
		if f, _ := gittest.Show(t, e.url, editBranch, c.file); !strings.Contains(f, "operator-notes: Ask **ops** first.") {
			t.Errorf("%s committed:\n%s", c.file, f)
		}
		var d struct{ Notes adminui.OperatorNotes }
		apiDo(t, h, "GET", "/app/admin/api/"+c.api, nil, &d)
		if d.Notes != got {
			t.Errorf("%s page shows %+v", c.api, d.Notes)
		}
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/users/nobody/operator-notes", notes, nil), 404, "not_found")

	// The destination and model-spec forms don't carry the notes: a save keeps them.
	var dd adminui.DestDetail
	apiDo(t, h, "POST", "/app/admin/api/destinations/gh", destBody(map[string]string{"note": "use gh"}), &dd)
	var sd adminui.SpecDetail
	apiDo(t, h, "POST", "/app/admin/api/model-specs/fast", specBody(map[string]string{"note": "fast"}), &sd)
	if dd.Fields.Note != "use gh" || dd.Notes.Markdown != notes["notes"] || sd.Fields.Note != "fast" || sd.Notes.Markdown != notes["notes"] {
		t.Fatalf("a form save dropped the notes: %+v / %+v", dd.Notes, sd.Notes)
	}
}

// Off a branch, a notes write is a config write: refused.
func TestOperatorNotesLiveWriteRefused(t *testing.T) {
	h, _, _, _ := branchUI(t)
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/users/bob/operator-notes", map[string]string{"notes": "x"}, nil), 409, "conflict")
}
