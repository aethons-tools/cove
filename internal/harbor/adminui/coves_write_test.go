package adminui_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

// fakeLauncher implements the exported harbor.Launcher for hermetic cove tests.
type fakeLauncher struct{}

func (fakeLauncher) Raise(_ context.Context, spec harbor.RaiseSpec, _ harbor.LaunchCreds) (string, error) {
	return "loc-" + spec.ActorID, nil
}
func (fakeLauncher) Teardown(_ context.Context, _ harbor.Instance) error { return nil }
func (fakeLauncher) Probe(_ context.Context, _ harbor.Instance) (harbor.Liveness, error) {
	return harbor.LivenessAlive, nil
}
func (fakeLauncher) Pause(_ context.Context, _ harbor.Instance) error   { return nil }
func (fakeLauncher) Unpause(_ context.Context, _ harbor.Instance) error { return nil }

func newSup(t *testing.T, store harbor.Store) *harbor.Supervisor {
	t.Helper()
	return harbor.NewSupervisor(store, fakeLauncher{}, "test-holder",
		60*time.Second, 30*time.Second, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// covePost issues a form POST with a matching Origin (passes CSRF).
func covePost(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRaiseCove(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), newSup(t, store))
	rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-1"}, "project": {"acme"}, "role": {"worker"}, "prompt": {"do the thing"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("raise = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, i := range store.ListInstances() {
		if i.ActorID == "cove-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("instance cove-1 was not registered")
	}
	if !strings.Contains(rec.Body.String(), "cove-1") {
		t.Errorf("coves fragment should list the new cove; got:\n%s", rec.Body.String())
	}
}

func TestRaiseCoveValidation(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), newSup(t, store))
	rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-x"}}) // missing role
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "role is required") {
		t.Fatalf("missing role = %d %q, want 400 + inline error", rec.Code, rec.Body.String())
	}
}

func TestRaiseCoveNoRuntime503(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil) // no supervisor
	rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-1"}, "role": {"worker"}})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("raise with no runtime = %d, want 503", rec.Code)
	}
	// And the Coves page hides the raise form.
	page := get(t, h, "/ui/coves").Body.String()
	if strings.Contains(page, `hx-post="/ui/coves"`) {
		t.Error("read-only Coves page must not render the raise form")
	}
}

func TestTeardownCove(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), newSup(t, store))
	// Raise one first.
	if rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-2"}, "project": {"acme"}, "role": {"worker"}}); rec.Code != http.StatusOK {
		t.Fatalf("setup raise = %d", rec.Code)
	}
	// Teardown it.
	req := httptest.NewRequest(http.MethodDelete, "/ui/coves/cove-2", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("teardown = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	// The instance is gone or transitioning (fake Teardown → supervisor drops/marks it).
	for _, i := range store.ListInstances() {
		if i.ActorID == "cove-2" && i.Phase != harbor.PhaseTerminating && i.Phase != harbor.PhaseGone {
			t.Errorf("cove-2 still %s after teardown, want terminating/gone/removed", i.Phase)
		}
	}
}

func TestTeardownCoveNoRuntime503(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil)
	req := httptest.NewRequest(http.MethodDelete, "/ui/coves/x", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("teardown with no runtime = %d, want 503", rec.Code)
	}
}

func TestCovesControlsRenderWithSupervisor(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), newSup(t, store))

	// Raise form is present on the Coves page.
	page := get(t, h, "/ui/coves").Body.String()
	if !strings.Contains(page, `hx-post="/ui/coves"`) {
		t.Error("Coves page with a supervisor must render the raise form")
	}

	// Seed a live cove, then check the per-row Teardown control appears.
	if rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-3"}, "project": {"acme"}, "role": {"worker"}}); rec.Code != http.StatusOK {
		t.Fatalf("setup raise = %d", rec.Code)
	}
	page = get(t, h, "/ui/coves").Body.String()
	if !strings.Contains(page, `hx-delete="/ui/coves/`) {
		t.Errorf("Coves page with a live cove must render the Teardown button; got:\n%s", page)
	}

	// The dashboard stays view-only even with a supervisor configured: no
	// Teardown buttons on the shared coves-table there (Fix A).
	dash := get(t, h, "/ui/").Body.String()
	if strings.Contains(dash, `hx-delete="/ui/coves/`) {
		t.Error("dashboard must not render Teardown buttons even when a supervisor is configured")
	}
}

func TestRaiseCoveCSRF(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), newSup(t, store))
	req := httptest.NewRequest(http.MethodPost, "/ui/coves", strings.NewReader("id=x&role=worker"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin raise = %d, want 403", rec.Code)
	}
}
