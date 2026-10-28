package adminui

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// oldRoots are the old admin pages' first path segments; a GET under one
// whose page moved is redirected (agents' pages mostly kept their paths).
var oldRoots = map[string]bool{"users": true, "projects": true, "kits": true, "destinations": true, "model-specs": true,
	"specs": true, "roles": true, "intercom": true, "agents": true, "coves": true, "actors": true}

// redirectOld answers a GET for an old admin page — or the preview's
// next/ path — with a 302 to its page in the app, the query kept, and
// reports whether it did.
func redirectOld(w http.ResponseWriter, r *http.Request) bool {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), appMount)
	var to string
	if next, ok := strings.CutPrefix(rest, "next"); ok && (next == "" || strings.HasPrefix(next, "/")) {
		if to = strings.TrimPrefix(next, "/"); !plainPath(to) {
			to = ""
		} else if r.URL.RawQuery != "" {
			to += "?" + r.URL.RawQuery
		}
	} else {
		first, _, _ := strings.Cut(rest, "/")
		if !oldRoots[first] && rest != "config" {
			return false
		}
		old := appMount + rest
		if r.URL.RawQuery != "" {
			old += "?" + r.URL.RawQuery
		}
		to = appPath(old)
		toPath, toQuery, _ := strings.Cut(to, "?")
		if toPath == rest && sameQuery(toQuery, r.URL.RawQuery) { // already the app's page
			return false
		}
	}
	http.Redirect(w, r, appMount+to, http.StatusFound)
	return true
}

// plainPath reports whether p (escaped) has no dot segments, escaped or not.
func plainPath(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if d, err := url.PathUnescape(seg); err != nil || d == "." || d == ".." {
			return false
		}
	}
	return true
}

// sameQuery reports whether two raw queries carry the same values, in any
// order.
func sameQuery(a, b string) bool {
	qa, errA := url.ParseQuery(a)
	qb, errB := url.ParseQuery(b)
	return errA == nil && errB == nil && maps.EqualFunc(qa, qb, slices.Equal)
}
