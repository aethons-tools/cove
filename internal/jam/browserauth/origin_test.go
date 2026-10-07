package browserauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireSameOrigin(t *testing.T) {
	h := RequireSameOrigin([]string{"https://dev.example:8443"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, c := range []struct {
		method, origin, referer string
		want                    int
	}{
		{"GET", "", "", http.StatusNoContent}, // reads aren't checked
		{"POST", "https://jam.example", "", http.StatusNoContent},
		{"POST", "https://dev.example:8443", "", http.StatusNoContent},
		{"POST", "", "https://jam.example/me/", http.StatusNoContent},
		{"POST", "https://evil.example", "", http.StatusForbidden},
		{"POST", "https://sub.jam.example", "", http.StatusForbidden}, // a sibling subdomain
		{"POST", "", "", http.StatusForbidden},                        // fail closed
		{"DELETE", "https://evil.example", "", http.StatusForbidden},
	} {
		r := httptest.NewRequest(c.method, "https://jam.example/me/send", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.referer != "" {
			r.Header.Set("Referer", c.referer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s origin=%q referer=%q = %d, want %d", c.method, c.origin, c.referer, w.Code, c.want)
		}
	}
}
