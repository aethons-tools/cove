package adminui_test

import (
	"strings"
	"testing"
)

// The top nav is six sections, in order, and a page highlights its section
// whatever its title: a detail page highlights its list's section.
func TestTopNavSections(t *testing.T) {
	h := projHandler(seedProjects(t))
	body := get(t, h, "/ui/").Body.String()
	nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")]
	last := -1
	for _, label := range []string{">Dashboard<", ">Projects<", ">Users<", ">Agents<", ">Specs<", ">Intercom<"} {
		i := strings.Index(nav, label)
		if i < 0 || i < last {
			t.Fatalf("nav order: %q missing or out of order in\n%s", label, nav)
		}
		last = i
	}
	for _, gone := range []string{">Roles<", ">Actors<", ">Studios<", ">Kits<"} {
		if strings.Contains(nav, gone) {
			t.Errorf("nav still has %s", gone)
		}
	}
	for path, section := range map[string]string{
		"/ui/roles/acme/dev": "Projects",
		"/ui/projects/acme":  "Projects",
		"/ui/coves":          "Agents",
		"/ui/actors":         "Agents",
		"/ui/kits":           "Specs",
		"/ui/model-specs":    "Specs",
		"/ui/users":          "Users",
	} {
		body := get(t, h, path).Body.String()
		nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")]
		if n := strings.Count(nav, `aria-current="page"`); n != 1 || !strings.Contains(nav, `aria-current="page">`+section+"<") {
			t.Errorf("%s: want only %s current in the top nav, got %d marked", path, section, n)
		}
	}
	// search belongs to no section
	if body := get(t, h, "/ui/search?q=acme").Body.String(); strings.Contains(body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")], `aria-current`) {
		t.Errorf("search should highlight no section")
	}
}
