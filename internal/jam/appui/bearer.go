package appui

import (
	"net/http"
	"strings"
)

// BearerFunc authenticates an /app/user request by its bearer token raw (an
// app token, docs/usage/jam/app-tokens.md): rr carries the Principal, and
// done is called when the request ends. An error answers 401.
type BearerFunc func(r *http.Request, raw string) (rr *http.Request, done func(), err error)

// bearerRefusedPaths are the /app/user routes a bearer may not use: Web Push
// subscriptions belong to a browser.
var bearerRefusedPaths = []string{
	"POST /app/user/push/subscriptions",
	"DELETE /app/user/push/subscriptions",
	"GET /app/user/push/key",
}

// bearerToken returns the request's bearer token and whether it has one.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// RefuseBearer answers 401 to any request carrying a bearer token, loopback
// included: app tokens never reach the admin area.
func RefuseBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bearerToken(r); ok {
			http.Error(w, "app tokens can't use the admin area", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withBearer sends a bearer request through bearer to bearerNext, and any
// other request to cookieNext. Bearer requests skip the gate (so never gain
// loopback admin) and the same-origin check (no cookie, no CSRF); one that
// also carries a session cookie is refused.
func withBearer(bearer BearerFunc, cookies []string, bearerNext, cookieNext http.Handler) http.Handler {
	if bearer == nil {
		return cookieNext
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r)
		if !ok {
			cookieNext.ServeHTTP(w, r)
			return
		}
		for _, c := range cookies {
			if _, err := r.Cookie(c); err == nil {
				http.Error(w, "send a bearer token or a session cookie, not both", http.StatusBadRequest)
				return
			}
		}
		rr, done, err := bearer(r, raw)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "invalid or expired app token", http.StatusUnauthorized)
			return
		}
		defer done()
		bearerNext.ServeHTTP(w, rr)
	})
}

// refuseOnBearer answers 403 to the routes in paths ("METHOD /path", exact).
// It sits on the bearer chain only.
func refuseOnBearer(paths []string, next http.Handler) http.Handler {
	refused := map[string]bool{}
	for _, p := range paths {
		refused[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := r.Method
		if m == http.MethodHead { // a GET pattern serves HEAD too
			m = http.MethodGet
		}
		if refused[m+" "+r.URL.Path] {
			http.Error(w, "app tokens can't use this route", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
