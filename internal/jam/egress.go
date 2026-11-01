package jam

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// EgressView is the GET /admin/roles/{project}/{role}/egress response. Managed
// false means the role has no policy and its coves get the kit's default list.
type EgressView struct {
	Managed bool     `json:"managed"`
	Domains []string `json:"domains"`
}

// egressDomainRE is the egress domain syntax, shared with the sealed in-box
// apply-role-egress.sh: at least two labels; no scheme, port, path, glob or
// whitespace; an optional leading dot means "and subdomains".
var egressDomainRE = regexp.MustCompile(`^\.?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// NormalizeEgress lowercases, validates, dedupes, sorts, and drops entries
// covered by another wildcard in the list. The error names the first bad domain.
// The result is never nil (an empty policy is a valid one).
func NormalizeEgress(domains []string) ([]string, error) {
	seen := map[string]bool{}
	for _, d := range domains {
		n := strings.ToLower(strings.TrimSpace(d))
		if !egressDomainRE.MatchString(n) {
			return nil, fmt.Errorf("invalid egress domain %q: want a hostname like example.com or .example.com (no scheme, port, path or *)", d)
		}
		seen[n] = true
	}
	out := []string{}
	for d := range seen {
		covered := false
		for c := range seen {
			if c != d && EgressCovers(c, d) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out, nil
}

// EgressCovers reports whether allow-list entry c covers requested domain r:
// r == c, or c is a leading-dot wildcard and r is its apex or ends with it. An
// exact host never covers a wildcard. The in-box ceiling check uses the same rule.
func EgressCovers(c, r string) bool {
	if r == c {
		return true
	}
	return strings.HasPrefix(c, ".") && (r == c[1:] || strings.HasSuffix(r, c))
}

// EgressFingerprint canonically names an egress policy, so the supervisor can
// tell whether a cove runs under its role's current one: "kit" for the kit
// default (nil), "none" for a set-but-empty policy, else "d:" + the domains
// joined by commas (already normalized and sorted by NormalizeEgress). It holds
// the domain list, so it is never logged.
func EgressFingerprint(p *EgressPolicy) string {
	if p == nil {
		return "kit"
	}
	if len(p.Domains) == 0 {
		return "none"
	}
	return "d:" + strings.Join(p.Domains, ",")
}

// registerEgress mounts the read route for a role's egress policy; the policy
// changes only in git.
func registerEgress(mux *http.ServeMux, store Store) {
	notFound := func(w http.ResponseWriter, project, role string) {
		http.Error(w, fmt.Sprintf("role %s/%s does not exist", project, role), http.StatusNotFound)
	}

	mux.HandleFunc("GET /admin/roles/{project}/{role}/egress", func(w http.ResponseWriter, r *http.Request) {
		project, roleName := r.PathValue("project"), r.PathValue("role")
		role, ok := store.GetRole(project, roleName)
		if !ok {
			notFound(w, project, roleName)
			return
		}
		view := EgressView{Domains: []string{}}
		if role.Scope.Egress != nil {
			view.Managed = true
			view.Domains = append(view.Domains, role.Scope.Egress.Domains...)
		}
		writeJSON(w, http.StatusOK, view)
	})
}
