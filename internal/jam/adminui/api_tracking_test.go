package adminui_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

func TestTrackingAPI(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	var tr adminui.Tracking
	apiDo(t, h, "GET", "/app/admin/api/tracking", nil, &tr)
	if tr.Source != nil || !tr.CanGit || len(tr.Rows) != 0 || tr.Plan != nil {
		t.Fatalf("untracked = %+v", tr)
	}
	var res adminui.TrackingResult
	if rec := apiDo(t, h, "POST", "/app/admin/api/tracking/source", map[string]string{"source": "https://github.com/acme/jam@main"}, &res); rec.Code != 200 {
		t.Fatalf("track = %d %s", rec.Code, rec.Body)
	}
	p := res.Tracking.Plan
	if res.Message != "tracking https://github.com/acme/jam@main" || p == nil || len(p.Ops) != 1 || p.Ops[0].Action != "create" || p.Ops[0].File != "users/bob.yml" || len(p.Ops[0].Lines) == 0 {
		t.Fatalf("track result = %+v plan %+v", res, p)
	}
	if res.Tracking.Source == nil || res.Tracking.Source.Repo != "https://github.com/acme/jam" || len(store.ListUsers()) != 0 {
		t.Fatalf("source = %+v users %v", res.Tracking.Source, store.ListUsers())
	}
	var plan adminui.Plan
	apiDo(t, h, "POST", "/app/admin/api/tracking/diff", map[string]bool{"force": false}, &plan)
	if plan.Digest != p.Digest || plan.Changes != 1 {
		t.Errorf("diff = %+v", plan)
	}
	if rec := apiDo(t, h, "POST", "/app/admin/api/tracking/apply", map[string]any{"plan": plan.Digest}, &res); rec.Code != 200 {
		t.Fatalf("apply = %d %s", rec.Code, rec.Body)
	}
	bob, _ := jam.ResolveRegistryRef(store, ident.User, "bob")
	if res.Message != "applied config @abc1234: 1 created, 0 updated, 0 deleted, 0 adopted, 0 forgotten" || len(res.Tracking.Rows) != 1 ||
		res.Tracking.Rows[0].Path != "config/users/"+string(bob) || res.Tracking.Plan != nil {
		t.Errorf("apply result = %+v", res)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/tracking/apply", map[string]any{}, nil), 400, "bad_request")
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/tracking/source", map[string]string{"source": "https://github.com/acme/other@main"}, nil), 409, "conflict")
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/tracking/source", nil, &res); rec.Code != 200 || res.Tracking.Source != nil ||
		!strings.HasPrefix(res.Message, "config source untracked") {
		t.Errorf("untrack = %d %+v", rec.Code, res)
	}
}

func TestTrackingApplyStale(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	trackManaged(t, store)
	rec := apiDo(t, h, "POST", "/app/admin/api/tracking/apply", map[string]any{"plan": "nope"}, nil)
	wantErr(t, rec, http.StatusConflict, "conflict")
	if !strings.Contains(rec.Body.String(), "Run Diff again") {
		t.Errorf("stale message = %s", rec.Body)
	}
}

func TestTrackingRelease(t *testing.T) {
	h, store := managedUI(t)
	trackManaged(t, store)
	var res adminui.TrackingResult
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/tracking/managed/role/app/ic", nil, &res); rec.Code != 200 {
		t.Fatalf("release = %d %s", rec.Code, rec.Body)
	}
	if res.Message != "released role app/ic; it stays in Jam, unmanaged" {
		t.Errorf("message = %q", res.Message)
	}
	for _, r := range res.Tracking.Rows {
		if r.Kind == "role" {
			t.Errorf("role still managed: %+v", r)
		}
	}
	for _, r := range res.Tracking.Rows {
		want := map[string]string{"destination": "config/destinations/gh", "model-spec": "config/model-specs/fast", "kit": "config/kits/web-dev", "project": "config/projects/app"}[r.Kind]
		if want != "" && r.Path != want {
			t.Errorf("%s path = %q, want %q", r.Kind, r.Path, want)
		}
	}
}

func TestTrackingNoGit(t *testing.T) {
	h, _ := adminHandler(t)
	var tr adminui.Tracking
	apiDo(t, h, "GET", "/app/admin/api/tracking", nil, &tr)
	if tr.CanGit {
		t.Error("canGit without a fetcher")
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/tracking/diff", map[string]bool{}, nil), 404, "not_found")
}
