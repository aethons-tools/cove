package browserauth

import (
	"net/http"
	"net/url"
)

// SameOrigin reports whether a state-changing request's Origin (or, absent
// that, Referer) is the request's own Host, or exactly one of the trusted
// extra origins (scheme://host[:port], e.g. a dev proxy fronting the
// listener). Fail-closed: neither header → false.
func SameOrigin(r *http.Request, trusted map[string]bool) bool {
	check := func(v string) (bool, bool) {
		if v == "" {
			return false, false
		}
		u, err := url.Parse(v)
		if err != nil {
			return false, true
		}
		return u.Host == r.Host || trusted[u.Scheme+"://"+u.Host], true
	}
	if ok, present := check(r.Header.Get("Origin")); present {
		return ok
	}
	if ok, present := check(r.Header.Get("Referer")); present {
		return ok
	}
	return false
}

// RequireSameOrigin wraps next so every state-changing request (anything but
// GET, HEAD and OPTIONS) must come from the same origin or a trusted one
// (SameOrigin); others get a 403. A cookie-authenticated surface needs this:
// SameSite=Lax alone lets a sibling subdomain post as the user.
func RequireSameOrigin(trustedOrigins []string, next http.Handler) http.Handler {
	trusted := map[string]bool{}
	for _, o := range trustedOrigins {
		trusted[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !SameOrigin(r, trusted) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
