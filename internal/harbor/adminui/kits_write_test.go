package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

// anyCred is a permissive credExists stub for tests that don't exercise
// cred-name validation.
func anyCred(string) bool { return true }

func uiHandler(t *testing.T, store harbor.Store) http.Handler {
	t.Helper()
	return adminui.Handler(store, testLogger(), nil, anyCred, nil)
}

func TestPushKit(t *testing.T) {
	store := newStore(t)
	h := uiHandler(t, store)
	rec := post(t, h, "/ui/kits", url.Values{"name": {"base"}, "config": {"listen: :443"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("push kit = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := store.GetKit("base"); !ok {
		t.Fatal("kit base not created")
	}
	if !strings.Contains(rec.Body.String(), "base") {
		t.Errorf("kits fragment should list the kit; got:\n%s", rec.Body.String())
	}
	// missing config → 400
	bad := post(t, h, "/ui/kits", url.Values{"name": {"x"}})
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
	rec := post(t, h, "/ui/kits/base/pin", url.Values{"version": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("pin = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if k, _ := store.GetKit("base"); k.Current != 1 {
		t.Errorf("current = %d, want 1 after pin", k.Current)
	}
	// non-integer version → 400
	bad := post(t, h, "/ui/kits/base/pin", url.Values{"version": {"notanint"}})
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
	req := httptest.NewRequest(http.MethodDelete, "/ui/kits/base", nil)
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
// a cross-origin POST to /ui/kits must be refused, and the kit must not be
// created.
func TestKitWriteCSRF(t *testing.T) {
	store := newStore(t)
	h := uiHandler(t, store)
	req := httptest.NewRequest(http.MethodPost, "/ui/kits", strings.NewReader("name=base&config=listen%3A+%3A443"))
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
// successful push shows only name/version/count, never the raw config text.
func TestPushKitNoConfigLeak(t *testing.T) {
	store := newStore(t)
	h := uiHandler(t, store)
	const secretConfig = "SECRET-KIT-CONFIG-XYZ"
	rec := post(t, h, "/ui/kits", url.Values{"name": {"base"}, "config": {secretConfig}})
	if rec.Code != http.StatusOK {
		t.Fatalf("push kit = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretConfig) {
		t.Errorf("push response must not leak kit config; got:\n%s", rec.Body.String())
	}
}

func TestDeleteKitReferenced409(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("base", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", harbor.Role{Name: "worker", Kit: "base"}); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/ui/kits/base", nil)
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
