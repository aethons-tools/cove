package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/studio"
)

// anyCred is a permissive credExists stub for tests that don't exercise
// cred-name validation.
func anyCred(string) bool { return true }

func uiHandler(t *testing.T, store jam.Store) http.Handler {
	t.Helper()
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
}

func TestPushKit(t *testing.T) {
	store := newStore(t)
	h := uiHandler(t, store)
	rec := post(t, h, "/app/admin/kits", url.Values{"name": {"base"}, "config": {"kind: studio\n"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("push kit = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := store.GetKit("base"); !ok {
		t.Fatal("kit base not created")
	}
	if !strings.Contains(rec.Body.String(), "base") {
		t.Errorf("kits fragment should list the kit; got:\n%s", rec.Body.String())
	}
	// a config that isn't a studio kit is refused, not stored
	if rec := post(t, h, "/app/admin/kits", url.Values{"name": {"y"}, "config": {"listen: :443"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-studio config = %d, want 400", rec.Code)
	}
	if _, ok := store.GetKit("y"); ok {
		t.Fatal("a refused config must not be stored")
	}
	// missing config → 400
	bad := post(t, h, "/app/admin/kits", url.Values{"name": {"x"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("missing config = %d, want 400", bad.Code)
	}
}

func TestPinKit(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("base", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PushKit("base", "v2"); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	rec := post(t, h, "/app/admin/kits/base/pin", url.Values{"version": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("pin = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if k, _ := store.GetKit("base"); k.Current != 1 {
		t.Errorf("current = %d, want 1 after pin", k.Current)
	}
	// non-integer version → 400
	bad := post(t, h, "/app/admin/kits/base/pin", url.Values{"version": {"notanint"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad version = %d, want 400", bad.Code)
	}
}

func TestDeleteKit(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("base", "v1"); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/app/admin/kits/base", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete kit = %d, want 200", rec.Code)
	}
	if _, ok := store.GetKit("base"); ok {
		t.Error("kit should be gone after delete")
	}
}

// TestKitWriteCSRF mirrors TestEnrollRejectsCrossOrigin / TestRaiseCoveCSRF:
// a cross-origin POST to /app/admin/kits must be refused, and the kit must not be
// created.
func TestKitWriteCSRF(t *testing.T) {
	store := newStore(t)
	h := uiHandler(t, store)
	req := httptest.NewRequest(http.MethodPost, "/app/admin/kits", strings.NewReader("name=base&config=listen%3A+%3A443"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin push kit = %d, want 403", rec.Code)
	}
	if _, ok := store.GetKit("base"); ok {
		t.Error("kit must not be created by a cross-origin request")
	}
	if len(store.ListKits()) != 0 {
		t.Error("no kit should exist after a cross-origin push")
	}
}

// TestPushKitNoConfigLeak asserts the kits-table fragment returned by a
// successful push summarizes the kit, never echoing the config text (the
// kit's own page shows it).
func TestPushKitNoConfigLeak(t *testing.T) {
	store := newStore(t)
	h := uiHandler(t, store)
	const secretConfig = "SECRET-KIT-CONFIG-XYZ"
	rec := post(t, h, "/app/admin/kits", url.Values{"name": {"base"}, "config": {"kind: studio\nprompt: " + secretConfig + "\n"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("push kit = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretConfig) {
		t.Errorf("push response must not leak kit config; got:\n%s", rec.Body.String())
	}
}

func TestDeleteKitReferenced409(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if _, err := store.PushKit("base", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "worker", Kit: "base"}); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/app/admin/kits/base", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete referenced kit = %d, want 409", rec.Code)
	}
	if _, ok := store.GetKit("base"); !ok {
		t.Error("referenced kit must not be deleted")
	}
}

// chainedStore holds web-dev (v1, v2) with my-app following it.
func chainedStore(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	for _, p := range []string{"base", "base2"} {
		sk := studio.StudioKit{Kind: studio.Kind, Prompt: p}
		if _, err := jam.EnsureStudioKit(store, "web-dev", sk); err != nil {
			t.Fatal(err)
		}
	}
	child := studio.StudioKit{Kind: studio.Kind, Prompt: "app"}
	child.Base.Kit = "web-dev"
	if _, err := jam.EnsureStudioKit(store, "my-app", child); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestUIKitRmGuardedByChildren(t *testing.T) {
	store := chainedStore(t)
	h := uiHandler(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/app/admin/kits/web-dev", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "kit web-dev is the parent of my-app") {
		t.Fatalf("rm = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := store.GetKit("web-dev"); !ok {
		t.Error("kit removed despite 409")
	}
}

func TestUIKitPinGuardedByFollowers(t *testing.T) {
	store := chainedStore(t)
	h := uiHandler(t, store)
	rec := post(t, h, "/app/admin/kits/web-dev/pin", url.Values{"version": {"1"}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "kit web-dev is followed by my-app") {
		t.Fatalf("pin = %d %s", rec.Code, rec.Body.String())
	}
	if k, _ := store.GetKit("web-dev"); k.Current != 2 {
		t.Errorf("current = %d, want 2", k.Current)
	}
}
