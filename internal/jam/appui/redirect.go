package appui

import (
	"net/http"
	"strings"
)

// LegacyRedirect permanently redirects GET/HEAD under the old prefix from to
// the same path under to, keeping the query. Any other method is 404: a write
// to an old URL is never replayed elsewhere.
func LegacyRedirect(from, to string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		u := to + strings.TrimPrefix(r.URL.Path, from)
		if r.URL.RawQuery != "" {
			u += "?" + r.URL.RawQuery
		}
		// Bounded cache: a browser must not pin a 308 forever, or a rollback
		// would leave old bookmarks bouncing to a gone /app.
		w.Header().Set("Cache-Control", "max-age=3600")
		http.Redirect(w, r, u, http.StatusPermanentRedirect)
	})
}
