package adminui_test

import (
	"encoding/json"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/adminui"
)

const noRepoMsg = "this Jam has no config repo, so its config is read-only: set config.github, config.gitlab or config.git in the serve config"

func TestNoRepoRefusesConfigWrites(t *testing.T) {
	h, store := adminHandler(t)
	user := map[string]string{"name": "carol"}
	rec := apiDo(t, h, "POST", "/app/admin/api/users", user, nil)
	wantNoRepo(t, rec.Code, rec.Body.Bytes())
	// A forged branch header on a Jam with no repo writes nothing either.
	rec = apiDoWith(t, h, onBranch("jam/alice"), "POST", "/app/admin/api/users", user, nil)
	wantNoRepo(t, rec.Code, rec.Body.Bytes())
	if us := store.ListUsers(); len(us) != 0 {
		t.Fatalf("users = %v, want none written", us)
	}
	if rec := apiDo(t, h, "POST", "/app/admin/api/projects/p/roles/r/request", map[string]string{}, nil); rec.Code == 409 {
		t.Fatalf("role request = 409 %s, want it served live", rec.Body)
	}
}

func wantNoRepo(t *testing.T, code int, body []byte) {
	t.Helper()
	var e adminui.APIError
	if code != 409 || json.Unmarshal(body, &e) != nil || e.Code != "conflict" || e.Error != noRepoMsg {
		t.Fatalf("got %d %s, want 409 conflict %q", code, body, noRepoMsg)
	}
}

func TestNoRepoServesReads(t *testing.T) {
	h, _ := adminHandler(t)
	if rec := apiDo(t, h, "GET", "/app/admin/api/tree", nil, nil); rec.Code != 200 {
		t.Fatalf("tree = %d %s", rec.Code, rec.Body)
	}
}

func TestNoRepoTrackingApplyLive(t *testing.T) {
	h, _ := adminHandler(t)
	rec := apiDo(t, h, "POST", "/app/admin/api/tracking/apply", map[string]string{}, nil)
	var e adminui.APIError
	if rec.Code == 409 && json.Unmarshal(rec.Body.Bytes(), &e) == nil && e.Error == noRepoMsg {
		t.Fatalf("tracking/apply refused read-only: %s", rec.Body)
	}
}
