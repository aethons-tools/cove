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
	if tr.Source == nil || tr.Source.Label != "https://github.com/acme/jam@main" || tr.Source.Commit != "" || !tr.CanGit || len(tr.Rows) != 0 || tr.Plan != nil {
		t.Fatalf("before the first apply = %+v", tr)
	}
	if tr.Follow.State != jam.ConfigFollowOff {
		t.Fatalf("follow before a check = %+v", tr.Follow)
	}
	for _, m := range []string{"POST", "DELETE"} {
		if rec := apiDo(t, h, m, "/app/admin/api/tracking/source", map[string]string{"source": "https://github.com/acme/jam@main"}, nil); rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%s tracking/source = %d, want gone", m, rec.Code)
		}
	}
	var plan adminui.Plan
	apiDo(t, h, "POST", "/app/admin/api/tracking/diff", map[string]bool{"force": false}, &plan)
	if plan.Changes != 1 || len(plan.Ops) != 1 || plan.Ops[0].File != "users/bob.yml" || len(store.ListUsers()) != 0 {
		t.Fatalf("diff = %+v", plan)
	}
	var res adminui.TrackingResult
	if rec := apiDo(t, h, "POST", "/app/admin/api/tracking/apply", map[string]any{"plan": plan.Digest}, &res); rec.Code != 200 {
		t.Fatalf("apply = %d %s", rec.Code, rec.Body)
	}
	bob, _ := jam.ResolveRegistryRef(store, ident.User, "bob")
	if res.Message != "applied config @abc1234: 1 created, 0 updated, 0 deleted, 0 adopted, 0 forgotten" || len(res.Tracking.Rows) != 1 ||
		res.Tracking.Rows[0].Path != "config/users/"+string(bob) || res.Tracking.Plan != nil {
		t.Errorf("apply result = %+v", res)
	}
	if s := res.Tracking.Source; s == nil || s.Commit != "abc1234" || s.Applied == "" {
		t.Errorf("source after apply = %+v", s)
	}
	if fs := res.Tracking.Follow; fs.State != jam.ConfigFollowInSync || fs.Applied != "abc1234" {
		t.Errorf("follow after the confirm = %+v", fs)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/tracking/apply", map[string]any{}, nil), 400, "bad_request")
}

// Without config.github the page has no source, and diff/apply answer 409.
func TestTrackingAPINoRepo(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA}
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, adminui.WithKitFetcher(f), adminui.WithConfigEnv(jam.ConfigEnv{}))
	var tr adminui.Tracking
	apiDo(t, h, "GET", "/app/admin/api/tracking", nil, &tr)
	if tr.Source != nil || tr.Follow.State != jam.ConfigFollowOff {
		t.Fatalf("no repo = %+v", tr)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/tracking/diff", map[string]bool{}, nil), 409, "conflict")
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

func TestTrackingHasNoRelease(t *testing.T) {
	h, store := managedUI(t)
	trackManaged(t, store)
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/tracking/managed/role/app/ic", nil, nil); rec.Code != 404 && rec.Code != 405 {
		t.Fatalf("release = %d %s, want 404 or 405", rec.Code, rec.Body)
	}
	var tr adminui.Tracking
	apiDo(t, h, "GET", "/app/admin/api/tracking", nil, &tr)
	for _, r := range tr.Rows {
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
