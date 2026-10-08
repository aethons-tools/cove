package condition

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(h http.Handler, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/metrics", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMetricsAuth(t *testing.T) {
	h := MetricsHandler(New(Options{}), "scrape-tok", nil)
	for name, auth := range map[string]string{"none": "", "wrong": "Bearer nope", "cove identity": "Bearer jam-identity-abc", "basic": "Basic c2NyYXBlLXRvaw=="} {
		if rec := scrape(h, auth); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
	}
	if rec := scrape(h, "Bearer scrape-tok"); rec.Code != http.StatusOK {
		t.Fatalf("right token: %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer scrape-tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d, want 405", rec.Code)
	}
}

func TestMetricsExposition(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	tr := New(Options{Now: c.now})
	tr.Raise(Condition{Key: "cred.unavailable:vertex-gcp", Severity: Critical, Summary: "credential vertex-gcp unavailable", Fix: "gcloud auth application-default login"})
	tr.Raise(Condition{Key: "pool.account.refresh:a", Severity: Warning, Summary: "pool a"})
	tr.Raise(Condition{Key: "k:gone", Severity: Warning, Summary: "gone"})
	tr.Clear("k:gone")
	rec := scrape(MetricsHandler(tr, "t", func() []Gauge { return []Gauge{{Name: "jam_studios", Help: "Studios Jam knows of.", Value: 3}} }), "Bearer t")
	body := rec.Body.String()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content-type %q", ct)
	}
	for _, want := range []string{
		"# TYPE jam_up gauge\njam_up 1\n",
		`jam_attention_condition{key="cred.unavailable:vertex-gcp",kind="cred.unavailable",severity="critical",summary="credential vertex-gcp unavailable",fix="gcloud auth application-default login"} 1`,
		`jam_attention_condition{key="pool.account.refresh:a",kind="pool.account.refresh",severity="warning",summary="pool a",fix=""} 1`,
		`jam_attention_open{severity="critical"} 1`,
		`jam_attention_open{severity="warning"} 1`,
		`jam_attention_open{severity="info"} 0`,
		"# HELP jam_studios Studios Jam knows of.\n# TYPE jam_studios gauge\njam_studios 3\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "k:gone") {
		t.Error("resolved condition exported")
	}
}

func TestMetricsEscapesLabelValues(t *testing.T) {
	tr := New(Options{})
	tr.Raise(Condition{Key: "k:x", Severity: Warning, Summary: "a \"quoted\" back\\slash\nnewline", Fix: "run \"x\""})
	body := scrape(MetricsHandler(tr, "t", nil), "Bearer t").Body.String()
	want := `summary="a \"quoted\" back\\slash\nnewline",fix="run \"x\""`
	if !strings.Contains(body, want) {
		t.Fatalf("label values not escaped; want %s in:\n%s", want, body)
	}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "jam_") {
			t.Fatalf("broken exposition line %q", line)
		}
	}
}
