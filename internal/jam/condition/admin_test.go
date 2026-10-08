package condition

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminHandlerStates(t *testing.T) {
	tr := New(Options{})
	tr.Raise(Condition{Key: "k:open", Severity: Critical, Summary: "o"})
	tr.Raise(Condition{Key: "k:done", Severity: Warning, Summary: "d"})
	tr.Clear("k:done")
	h := AdminHandler(tr)
	for state, want := range map[string]int{"": 1, "open": 1, "resolved": 1, "all": 2} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/attention?state="+state, nil))
		var got []Condition
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 || len(got) != want {
			t.Errorf("state %q: code %d len %d err %v", state, rec.Code, len(got), err)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/attention?state=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bogus state: %d", rec.Code)
	}
}
