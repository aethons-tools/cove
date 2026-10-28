package adminui

import "testing"

func TestAppPath(t *testing.T) {
	for old, want := range map[string]string{
		"/app/admin/":                                  "",
		"/app/admin":                                   "",
		"/app/admin/config":                            "config/tracking",
		"/app/admin/users":                             "config/users",
		"/app/admin/users/usr_1":                       "config/users/usr_1",
		"/app/admin/projects":                          "config/projects",
		"/app/admin/projects/acme":                     "config/projects/acme",
		"/app/admin/projects/acme/members":             "config/projects/acme/members",
		"/app/admin/projects/acme/agents":              "config/projects/acme/agents",
		"/app/admin/projects/acme/intercom":            "config/projects/acme/intercom",
		"/app/admin/projects/acme/escalation":          "config/projects/acme/escalation",
		"/app/admin/projects/acme/roles/coder":         "config/projects/acme/agents/coder",
		"/app/admin/projects/a%2Fb/roles/c":            "config/projects/a%2Fb/agents/c",
		"/app/admin/kits":                              "config/kits",
		"/app/admin/kits/base":                         "config/kits/base",
		"/app/admin/kits/base/draft":                   "config/kits/base",
		"/app/admin/destinations/gh":                   "config/destinations/gh",
		"/app/admin/model-specs":                       "config/model-specs",
		"/app/admin/model-specs/opus":                  "config/model-specs/opus",
		"/app/admin/agents":                            "agents",
		"/app/admin/agents?status=running":             "agents?status=running",
		"/app/admin/agents/x?project=p&status=running": "agents/x?status=running",
		"/app/admin/agents/x?project=p":                "agents/x",
		"/app/admin/agents/x/session":                  "agents/x/session",
		"/app/admin/agents/x/setup":                    "agents/x",
		"/app/admin/projects/p/roles/r/setup":          "config/projects/p/agents/r",
		"/app/admin/specs":                             "config/kits",
		"/app/admin/roles":                             "",
		"/app/admin/roles/p/r":                         "config/projects/p/agents/r",
		"/app/admin/projects/p/roles":                  "config/projects/p/agents",
		"/app/admin/kits/k/draft?file=a&diff=1&x=y":    "config/kits/k?ddiff=1&dfile=a&x=y",
		"/app/admin/coves/c/session":                   "agents/c/session",
		"/app/admin/coves/c/other":                     "",
		"/app/admin/actors":                            "agents",
		"/app/admin/intercom?q=hi+there":               "health/intercom?q=hi+there",
		"/app/admin/health":                            "health",
		"/app/admin/search?q=x":                        "search?q=x",
		"/elsewhere":                                   "",
		"/app/admin/../user":                           "",
		"/app/admin/agents/%2e%2e/%2e%2e/x":            "",
		"/app/admin/users/.%2E":                        "",
		"/app/admin/nonsense/thing":                    "",
		"https://evil.example/app/admin/users/u":       "",
	} {
		if got := appPath(old); got != want {
			t.Errorf("appPath(%q) = %q, want %q", old, got, want)
		}
	}
}
