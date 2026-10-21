package jam

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/studio"
)

// chainAdmin is an admin handler over a store holding web-dev v1, my-app and
// other-kit following it, and pinned-kit pinned to web-dev@v1.
func chainAdmin(t *testing.T) (http.Handler, Store) {
	t.Helper()
	h, st := newTestAdmin(t)
	mustEnsure(t, st, "web-dev", chainKit("", "base"))
	mustEnsure(t, st, "my-app", chainKit("web-dev", "app"))
	mustEnsure(t, st, "other-kit", chainKit("web-dev", "other"))
	mustEnsure(t, st, "pinned-kit", chainKit("web-dev@v1", "pinned"))
	return h, st
}

func TestAdminKitRmGuardedByChildren(t *testing.T) {
	h, st := chainAdmin(t)
	rec := doReq(t, h, "DELETE", "/admin/kits/web-dev", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rm parent = %d %s", rec.Code, rec.Body)
	}
	if got, want := strings.TrimSpace(rec.Body.String()), "kit web-dev is the parent of my-app, other-kit, pinned-kit"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if _, ok := st.GetKit("web-dev"); !ok {
		t.Error("kit removed despite 409")
	}
	// Leaves can go, then the parent.
	for _, n := range []string{"my-app", "other-kit", "pinned-kit", "web-dev"} {
		if rec := doReq(t, h, "DELETE", "/admin/kits/"+n, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("rm %s = %d %s", n, rec.Code, rec.Body)
		}
	}
}

func TestAdminKitRmRoleGuardUnchanged(t *testing.T) {
	h, st := chainAdmin(t)
	mustCreateProject(t, st, "acme")
	if err := st.PutRole("acme", Role{Name: "impl", Kit: "my-app"}); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, h, "DELETE", "/admin/kits/my-app", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "is referenced by role acme/impl") {
		t.Fatalf("rm = %d %s", rec.Code, rec.Body)
	}
}

func TestAdminKitPinGuardedByFollowers(t *testing.T) {
	h, st := chainAdmin(t)
	mustEnsure(t, st, "web-dev", chainKit("", "base2")) // v2
	rec := doJSON(t, h, "POST", "/admin/kits/web-dev/pin", PinBody{Version: 1})
	if rec.Code != http.StatusConflict {
		t.Fatalf("pin = %d %s", rec.Code, rec.Body)
	}
	want := "kit web-dev is followed by my-app, other-kit (pin those to a web-dev version, or point them elsewhere)"
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if k, _ := st.GetKit("web-dev"); k.Current != 2 {
		t.Errorf("current = %d, want unchanged 2", k.Current)
	}
}

func TestAdminKitPinAllowedWithOnlyPinnedChildren(t *testing.T) {
	h, st := newTestAdmin(t)
	mustEnsure(t, st, "web-dev", chainKit("", "base"))
	mustEnsure(t, st, "web-dev", chainKit("", "base2"))
	mustEnsure(t, st, "pinned-kit", chainKit("web-dev@v1", "p"))
	if rec := doJSON(t, h, "POST", "/admin/kits/web-dev/pin", PinBody{Version: 1}); rec.Code != http.StatusNoContent {
		t.Fatalf("pin = %d %s", rec.Code, rec.Body)
	}
	// An unreferenced kit pins freely, and a missing one is still 404.
	mustEnsure(t, st, "solo", chainKit("", "s"))
	if rec := doJSON(t, h, "POST", "/admin/kits/nope/pin", PinBody{Version: 1}); rec.Code != http.StatusNotFound {
		t.Errorf("pin missing = %d", rec.Code)
	}
}

func TestAdminKitGetChainFields(t *testing.T) {
	h, st := chainAdmin(t)
	mustEnsure(t, st, "web-dev", chainKit("", "base2")) // v2; my-app was pushed against v1
	var res KitConfigResult
	getJSON(t, h, "/admin/kits/my-app", &res)
	if len(res.Chain) != 1 || res.Chain[0] != (KitChainLink{Name: "web-dev", Version: 2, Follow: true}) {
		t.Errorf("chain = %+v", res.Chain)
	}
	if res.Parent == nil || *res.Parent != (KitParentRef{Name: "web-dev", Version: 1}) {
		t.Errorf("parent = %+v", res.Parent)
	}
	if !strings.Contains(res.Effective, "base2") || !strings.Contains(res.Effective, "app") {
		t.Errorf("effective = %q", res.Effective)
	}
	if strings.Contains(res.Effective, "web-dev") {
		t.Errorf("effective still names the parent: %q", res.Effective)
	}
	if _, err := studio.ParseStudioKit([]byte(res.Effective)); err != nil {
		t.Errorf("effective does not parse: %v", err)
	}
	var parent KitConfigResult
	getJSON(t, h, "/admin/kits/web-dev", &parent)
	if got := strings.Join(parent.UsedByKits, ","); got != "my-app,other-kit,pinned-kit" {
		t.Errorf("used_by_kits = %v", parent.UsedByKits)
	}
	if len(parent.Chain) != 0 || parent.Effective == "" {
		t.Errorf("root: chain=%v effective=%q", parent.Chain, parent.Effective)
	}
}

func TestAdminKitListParentColumn(t *testing.T) {
	h, _ := chainAdmin(t)
	var out []KitSummary
	getJSON(t, h, "/admin/kits", &out)
	got := map[string]string{}
	for _, k := range out {
		got[k.Name] = k.Parent
	}
	want := map[string]string{"web-dev": "", "my-app": "web-dev", "other-kit": "web-dev", "pinned-kit": "web-dev@v1"}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("parent[%s] = %q, want %q", n, got[n], w)
		}
	}
}
