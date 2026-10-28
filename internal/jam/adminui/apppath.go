package adminui

import (
	"net/url"
	"slices"
	"strings"
)

// appPath is an old admin page's URL (a bookmark, or one the view builders
// still make for attention items and search hits) as a
// path under the admin app's mount — segments kept escaped, the query kept
// — or "" (the dashboard) for one it doesn't know.
func appPath(old string) string {
	u, err := url.Parse(old)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}
	rest, ok := strings.CutPrefix(u.EscapedPath(), "/app/admin")
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return ""
	}
	s := strings.FieldsFunc(rest, func(r rune) bool { return r == '/' })
	if slices.ContainsFunc(s, func(x string) bool { // escaped dots are dots to a browser
		d, err := url.PathUnescape(x)
		return err != nil || d == "." || d == ".."
	}) {
		return ""
	}
	q := u.Query()
	path := ""
	switch {
	case len(s) == 0:
		return ""
	case s[0] == "config" && len(s) == 1:
		path = "config/tracking"
	case s[0] == "users" || s[0] == "destinations" || s[0] == "model-specs":
		if len(s) > 2 {
			return ""
		}
		path = "config/" + strings.Join(s, "/")
	case s[0] == "kits":
		if len(s) > 3 || (len(s) == 3 && s[2] != "draft") {
			return ""
		}
		path = "config/" + strings.Join(s[:min(len(s), 2)], "/")
		if len(s) == 3 { // the Draft is a section of the kit page, its file and diff renamed
			for old, now := range map[string]string{"file": "dfile", "diff": "ddiff"} {
				if v, ok := q[old]; ok {
					q[now] = v
					delete(q, old)
				}
			}
		}
	case s[0] == "projects":
		switch {
		case len(s) <= 2:
			path = "config/" + strings.Join(s, "/")
		case len(s) == 3 && slices.Contains([]string{"members", "agents", "intercom", "escalation"}, s[2]):
			path = "config/" + strings.Join(s, "/")
		case len(s) == 3 && s[2] == "roles":
			path = "config/projects/" + s[1] + "/agents"
		case len(s) >= 4 && s[2] == "roles":
			path = "config/projects/" + s[1] + "/agents/" + s[3]
		default:
			return ""
		}
	case s[0] == "agents":
		q.Del("project")                    // the Agents tab has no project scope
		if len(s) == 3 && s[2] == "setup" { // an agent's Setup is a section of its page
			s = s[:2]
		}
		path = strings.Join(s, "/")
	case s[0] == "coves" && (len(s) <= 2 || len(s) == 3 && s[2] == "session"), s[0] == "actors" && len(s) == 1: // the Agents tab's older names
		s[0] = "agents"
		path = strings.Join(s, "/")
	case s[0] == "specs" && len(s) == 1:
		path = "config/kits"
	case s[0] == "roles" && len(s) == 1:
		return ""
	case s[0] == "roles" && len(s) == 3:
		path = "config/projects/" + s[1] + "/agents/" + s[2]
	case s[0] == "intercom" && len(s) == 1:
		path = "health/intercom"
	case (s[0] == "health" || s[0] == "search") && len(s) == 1:
		path = s[0]
	default:
		return ""
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path
}
