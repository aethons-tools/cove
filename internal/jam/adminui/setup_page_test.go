package adminui_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// setupRoleFixture: project acme with role dev (egress new.example.com) and a
// handler with the Setup tab on; logs captures the audit lines.
func setupRoleFixture(t *testing.T) (http.Handler, *bytes.Buffer) {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "dev", Scope: jam.Scope{Egress: &jam.EgressPolicy{Domains: []string{"new.example.com"}}}}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	return adminui.Handler(store, log, &jam.Supervisor{}, nil, anyCred, nil, adminui.WithSetupDeps(jam.SetupDeps{Store: store})), &logs
}

func wantAll(t *testing.T, what, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("%s: missing %q", what, w)
		}
	}
	if t.Failed() {
		t.Logf("%s body:\n%s", what, body)
	}
}

// The role page links its Setup tab; the tab is a shell that loads its
// sections lazily and shows nothing of the setup itself.
func TestRoleSetupTab(t *testing.T) {
	h, logs := setupRoleFixture(t)
	wantAll(t, "role page", get(t, h, "/app/admin/projects/acme/roles/dev").Body.String(),
		`<a href="/app/admin/projects/acme/roles/dev" aria-current="page">Overview</a><a href="/app/admin/projects/acme/roles/dev/setup">Setup</a>`)
	rec := get(t, h, "/app/admin/projects/acme/roles/dev/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("setup page = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("setup page Cache-Control = %q, want no-store", cc)
	}
	page := rec.Body.String()
	wantAll(t, "setup page", page,
		`<a href="/app/admin/projects/acme/roles/dev">Overview</a><a href="/app/admin/projects/acme/roles/dev/setup" aria-current="page">Setup</a>`,
		`Agents</a> / <a href="/app/admin/projects/acme/roles/dev">dev</a> / <span>Setup</span>`,
		`hx-get="/app/admin/projects/acme/roles/dev/setup/sections" hx-trigger="load"`,
		`<a class="chip accent" href="/app/admin/projects/acme/roles/dev/setup">All</a>`,
		`<a class="chip" href="/app/admin/projects/acme/roles/dev/setup?sections=egress">egress</a>`)
	if strings.Contains(page, "new.example.com") || strings.Contains(page, `id="setup-sec-`) || strings.Contains(page, "Diff vs role") {
		t.Error("the shell rendered setup content or a role diff toggle")
	}
	if strings.Contains(logs.String(), "ui setup inspected") {
		t.Error("the shell logged an inspection it did not do")
	}
}

// The sections fragment: one collapsible block per section with its source,
// identity open, a fixed reason for an unavailable section, the audit line
// and no body in the log.
func TestRoleSetupSections(t *testing.T) {
	h, logs := setupRoleFixture(t)
	rec := getHX(t, h, "/app/admin/projects/acme/roles/dev/setup/sections")
	if rec.Code != http.StatusOK {
		t.Fatalf("sections = %d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("sections Cache-Control = %q, want no-store", cc)
	}
	body := rec.Body.String()
	wantAll(t, "sections", body,
		`<details class="sec" id="setup-sec-identity" open>`,
		`<details class="sec" id="setup-sec-egress">`,
		`<span class="chip src-config">config</span>`,
		`<span class="chip src-unavailable">unavailable</span>`,
		"no supervisor on this Jam", // context without a supervisor: its fixed reason
		"new.example.com")
	if n := strings.Count(body, `<details class="sec"`); n != len(jam.SetupSectionNames()) {
		t.Errorf("%d sections, want %d", n, len(jam.SetupSectionNames()))
	}
	if strings.Contains(body, "stale vs role") || strings.Contains(body, "<!doctype") {
		t.Error("a role's fragment has a stale badge or the page chrome")
	}
	l := logs.String()
	if !strings.Contains(l, "ui setup inspected") || !strings.Contains(l, `subject="role acme/dev"`) {
		t.Errorf("audit line missing: %s", l)
	}
	if strings.Contains(l, "new.example.com") {
		t.Errorf("a body reached the log: %s", l)
	}
}

// A section filter keeps to one section, open, in the page and the fragment.
func TestRoleSetupFilter(t *testing.T) {
	h, _ := setupRoleFixture(t)
	wantAll(t, "filtered page", get(t, h, "/app/admin/projects/acme/roles/dev/setup?sections=egress").Body.String(),
		`<a class="chip accent" href="/app/admin/projects/acme/roles/dev/setup?sections=egress">egress</a>`,
		`hx-get="/app/admin/projects/acme/roles/dev/setup/sections?sections=egress"`)
	body := getHX(t, h, "/app/admin/projects/acme/roles/dev/setup/sections?sections=egress").Body.String()
	if strings.Count(body, `<details class="sec"`) != 1 || !strings.Contains(body, `id="setup-sec-egress" open`) {
		t.Errorf("filtered fragment:\n%s", body)
	}
}

func TestRoleSetupErrors(t *testing.T) {
	h, _ := setupRoleFixture(t)
	for path, want := range map[string]int{
		"/app/admin/projects/acme/roles/dev/setup?sections=bogus":          http.StatusBadRequest,
		"/app/admin/projects/acme/roles/dev/setup/sections?sections=bogus": http.StatusBadRequest,
		"/app/admin/projects/acme/roles/dev/setup/sections?diff=role":      http.StatusBadRequest,
		"/app/admin/projects/acme/roles/nope/setup":                        http.StatusNotFound,
		"/app/admin/projects/acme/roles/nope/setup/sections":               http.StatusNotFound,
	} {
		if rec := get(t, h, path); rec.Code != want {
			t.Errorf("%s = %d, want %d: %s", path, rec.Code, want, rec.Body)
		}
	}
	page := get(t, h, "/app/admin/projects/acme/roles/dev/setup?sections=bogus").Body.String()
	if !strings.Contains(page, `unknown section &#34;bogus&#34;`) || strings.Contains(page, `hx-trigger="load"`) {
		t.Errorf("a bad filter should explain itself instead of loading:\n%s", page)
	}
}

// Without WithSetupDeps there is no tab and no route.
func TestSetupTabOffWithoutDeps(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil)
	if body := get(t, h, "/app/admin/projects/acme/roles/dev").Body.String(); strings.Contains(body, ">Setup</a>") {
		t.Error("Setup tab shown without setup deps")
	}
	if code := get(t, h, "/app/admin/projects/acme/roles/dev/setup").Code; code != http.StatusNotFound {
		t.Errorf("setup route = %d without deps, want 404", code)
	}
}
