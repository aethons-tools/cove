package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

func getHX(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthTabAndRailBadge(t *testing.T) {
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "cred.unavailable:vertex-gcp", Severity: condition.Critical, Summary: "credential vertex-gcp cannot be resolved", Fix: "gcloud auth application-default login"})
	tr.Raise(condition.Condition{Key: "image.stale:cove-ic", Severity: condition.Info, Summary: "kit cove-ic image is stale"})
	tr.Raise(condition.Condition{Key: "pool.account.refresh:a", Severity: condition.Warning, Summary: "pool account a cannot refresh"})
	tr.Clear("pool.account.refresh:a")
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, credAny, nil, adminui.WithConditions(tr, "http://localhost:9093"))

	body := get(t, h, "/ui/health").Body.String()
	for _, want := range []string{
		"credential vertex-gcp cannot be resolved", "gcloud auth application-default login", // open + fix
		"kit cove-ic image is stale",       // info shown on Health
		"pool account a cannot refresh",    // resolved history
		`href="/ui/health"`,                // the Health tab
		"http://localhost:9093/#/silences", // silences link
	} {
		if !strings.Contains(body, want) {
			t.Errorf("health page missing %q", want)
		}
	}
	dash := get(t, h, "/ui/").Body.String()
	if !strings.Contains(dash, "credential vertex-gcp cannot be resolved") {
		t.Error("critical condition not in the dashboard's Needs attention card")
	}
	if strings.Contains(dash, "kit cove-ic image is stale") {
		t.Error("info condition must not be an attention item")
	}
}

func TestHealthWithoutConditions(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, credAny, nil)
	rec := get(t, h, "/ui/health")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Nothing needs attention") {
		t.Fatalf("health without a tracker: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestHealthPollFragment(t *testing.T) {
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "k:x", Severity: condition.Warning, Summary: "polled"})
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, credAny, nil, adminui.WithConditions(tr, ""))
	rec := getHX(t, h, "/ui/health")
	if !strings.Contains(rec.Body.String(), "polled") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("poll should return only the fragment:\n%s", rec.Body.String())
	}
}
