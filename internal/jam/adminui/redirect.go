package adminui

import (
	"net/http"
	"strings"
)

// oldRoots are the old admin pages' first path segments; a GET under one
// whose page moved is redirected (agents' pages mostly kept their paths).
var oldRoots = map[string]bool{"users": true, "projects": true, "kits": true, "destinations": true, "model-specs": true,
	"specs": true, "roles": true, "intercom": true, "agents": true}

// redirectOld answers a GET for an old admin page — or the preview's
// next/ path — with a 302 to its page in the app, the query kept, and
// reports whether it did.
func redirectOld(w http.ResponseWriter, r *http.Request) bool {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), appMount)
	here := rest
	if r.URL.RawQuery != "" {
		here += "?" + r.URL.RawQuery
	}
	var to string
	if next, ok := strings.CutPrefix(rest, "next"); ok && (next == "" || strings.HasPrefix(next, "/")) {
		to = strings.TrimPrefix(next, "/")
		if r.URL.RawQuery != "" {
			to += "?" + r.URL.RawQuery
		}
	} else {
		first, _, _ := strings.Cut(rest, "/")
		if !oldRoots[first] && rest != "config" {
			return false
		}
		if to = appPath(appMount + here); to == here {
			return false
		}
	}
	http.Redirect(w, r, appMount+to, http.StatusFound)
	return true
}
