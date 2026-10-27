package adminui_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// countingImages is an image reader that counts its reads (and finds nothing).
type countingImages struct{ n atomic.Int32 }

func (c *countingImages) ImageFactsOn(context.Context, string, string) (jam.ImageFacts, error) {
	c.n.Add(1)
	return jam.ImageFacts{}, jam.ErrImageNotPresent
}

// contextSnap is a context snapshot body in contextBody's form, with hostile
// text in CORE.md.
const contextSnap = "FILE                          BYTES\n" +
	"/agent-data/context/CORE.md   44\n" +
	"/agent-data/context/INDEX.md  6\n" +
	"\n--- /agent-data/context/CORE.md\n# Rules <script>alert(1)</script>\nevil\u202etxt\n" +
	"\n--- /agent-data/context/INDEX.md\nindex\n"

// setupStudioFixture: acme/dev (egress new.example.com) and its running studio
// sess-1 (image cove-ic:1), whose egress snapshot predates the role's change
// (stale) and whose context snapshot holds contextSnap; ghost-1 is a running
// studio of a deleted role (gone-1 a gone studio), whose context snapshot is not the file table.
func setupStudioFixture(t *testing.T) (http.Handler, *countingImages) {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "dev", Scope: jam.Scope{Egress: &jam.EgressPolicy{Domains: []string{"new.example.com"}}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, inst := range []jam.Instance{
		{ActorID: "sess-1", Project: "acme", Role: "dev", Phase: jam.PhaseLive, RaisedAt: now, LastSeen: now, ImageTag: "cove-ic:1"},
		{ActorID: "ghost-1", Project: "acme", Role: "ghost", Phase: jam.PhaseLive, RaisedAt: now, LastSeen: now},
		{ActorID: "gone-1", Project: "acme", Role: "dev", Phase: jam.PhaseGone, RaisedAt: now, LastSeen: now},
	} {
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 24, 9, 30, 0, 0, time.UTC)
	for _, s := range []jam.SetupSnapshot{
		{ActorID: "sess-1", Part: jam.SetupPartEgress, At: at, Fingerprint: "egress-before",
			Body: "policy: role (1 domain(s))\nkit ceiling: unknown\nHOST             FROM\nold.example.com  role\n"},
		{ActorID: "sess-1", Part: jam.SetupPartContext, At: at, Fingerprint: "ctx", Body: contextSnap},
		{ActorID: "ghost-1", Part: jam.SetupPartEgress, At: at, Fingerprint: "x", Body: "policy: kit default\n"},
		{ActorID: "ghost-1", Part: jam.SetupPartContext, At: at, Fingerprint: "y", Body: "no context files\n--- /agent-data/context/CORE.md\nnot a file\n"},
	} {
		if err := store.PutSetupSnapshot(s); err != nil {
			t.Fatal(err)
		}
	}
	imgs := &countingImages{}
	// No supervisor: a zero jam.Supervisor{} panics resolving the image status
	// of a studio with an image tag (the attention items read it).
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil,
		adminui.WithSetupDeps(jam.SetupDeps{Store: store, Images: imgs}))
	return h, imgs
}

// The agent page links Setup while its studio runs; the shell offers the
// diff toggle.
func TestStudioSetupTab(t *testing.T) {
	h, _ := setupStudioFixture(t)
	wantAll(t, "agent page", get(t, h, "/app/admin/agents/sess-1").Body.String(),
		`<a href="/app/admin/agents/sess-1" aria-current="page">Overview</a><a href="/app/admin/agents/sess-1/setup">Setup</a>`)
	rec := get(t, h, "/app/admin/agents/sess-1/setup")
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("setup page Cache-Control = %q, want no-store", cc)
	}
	wantAll(t, "setup page", rec.Body.String(),
		`<a href="/app/admin/agents/sess-1/setup" aria-current="page">Setup</a>`,
		`hx-get="/app/admin/agents/sess-1/setup/sections" hx-trigger="load"`,
		`<a class="btn small" href="/app/admin/agents/sess-1/setup?diff=role">Diff vs role</a>`)
	wantAll(t, "diffing page", get(t, h, "/app/admin/agents/sess-1/setup?diff=role").Body.String(),
		`hx-get="/app/admin/agents/sess-1/setup/sections?diff=role"`,
		`<a class="btn small" href="/app/admin/agents/sess-1/setup">Hide diff</a>`)
}

// The page, the agent page and the shell never read an image; the fragment does.
func TestSetupShellReadsNoImage(t *testing.T) {
	h, imgs := setupStudioFixture(t)
	get(t, h, "/app/admin/agents/sess-1")
	get(t, h, "/app/admin/agents/sess-1/setup")
	get(t, h, "/app/admin/agents/sess-1/setup?diff=role")
	if n := imgs.n.Load(); n != 0 {
		t.Fatalf("%d image read(s) before the fragment loaded", n)
	}
	if rec := getHX(t, h, "/app/admin/agents/sess-1/setup/sections"); rec.Code != http.StatusOK {
		t.Fatalf("sections = %d %s", rec.Code, rec.Body)
	}
	if imgs.n.Load() == 0 {
		t.Fatal("the fragment did not read the image")
	}
}

// A stale section carries a badge that loads its diff; the snapshot time and
// source show; the count of stale sections leads the fragment, with the hint
// to open a badge.
func TestStudioSetupStaleBadge(t *testing.T) {
	h, _ := setupStudioFixture(t)
	rec := getHX(t, h, "/app/admin/agents/sess-1/setup/sections")
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("sections Cache-Control = %q, want no-store", cc)
	}
	body := rec.Body.String()
	wantAll(t, "sections", body,
		"1 section(s) stale vs role",
		`Open a <span class="chip stale">stale vs role</span> badge`,
		`<span class="chip src-snapshot">snapshot</span> <button type="button" class="chip stale" hx-get="/app/admin/agents/sess-1/setup/sections/egress" hx-target="#setup-sec-egress" hx-swap="outerHTML"`,
		`<time class="sub" datetime="2026-10-24T09:30:00Z">2026-10-24 09:30:00 UTC</time>`,
		"image not present on the launcher host") // the seed, through the fake reader
	if strings.Contains(body, "This studio (−)") {
		t.Error("diffs rendered before one was asked for")
	}
}

// The badge's fragment is that one section, open, diffed against a raise now.
func TestStudioSetupSectionDiff(t *testing.T) {
	h, _ := setupStudioFixture(t)
	rec := getHX(t, h, "/app/admin/agents/sess-1/setup/sections/egress")
	if rec.Code != http.StatusOK {
		t.Fatalf("section diff = %d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("section diff Cache-Control = %q, want no-store", cc)
	}
	body := rec.Body.String()
	wantAll(t, "section diff", body,
		`<details class="sec" id="setup-sec-egress" open>`,
		`<span class="chip stale">stale vs role</span>`,
		"This studio (−) against a raise of its role now (+)",
		`<span class="d-del">- old.example.com`,
		`<span class="d-add">+ new.example.com`)
	if strings.Count(body, `<details class="sec"`) != 1 || strings.Contains(body, "hx-get=") {
		t.Errorf("want just the diffed section, its badge no longer a button:\n%s", body)
	}
}

// ?diff=role diffs every section but identity; with nothing left to open,
// the badge hint is gone.
func TestStudioSetupDiffAll(t *testing.T) {
	h, _ := setupStudioFixture(t)
	body := getHX(t, h, "/app/admin/agents/sess-1/setup/sections?diff=role").Body.String()
	if n, want := strings.Count(body, "This studio (−)"), len(jam.SetupSectionNames())-1; n != want {
		t.Errorf("%d diffed sections, want %d", n, want)
	}
	wantAll(t, "diff all", body, `<span class="d-add">+ new.example.com`, "Same as a raise now.", "1 section(s) stale vs role")
	if strings.Contains(body, "Open a ") {
		t.Error("the badge hint shown with every section diffed")
	}
}

// A studio whose role is gone still renders; its diff is the note, in the
// section the badge loads.
func TestStudioSetupDiffRoleGone(t *testing.T) {
	h, _ := setupStudioFixture(t)
	rec := getHX(t, h, "/app/admin/agents/ghost-1/setup/sections/egress")
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
	wantAll(t, "role gone", rec.Body.String(), `<p class="unset">the role no longer exists: no diff</p>`, "policy: kit default")
	if strings.Contains(rec.Body.String(), "d-del") {
		t.Error("a diff against a deleted role")
	}
	all := getHX(t, h, "/app/admin/agents/ghost-1/setup/sections?diff=role")
	if all.Code != http.StatusOK {
		t.Fatalf("diff all = %d %s", all.Code, all.Body)
	}
	wantAll(t, "role gone, all", all.Body.String(), "the role no longer exists: no diff")
}

// Context files show one by one, raw, at their real paths, under the file
// table; hostile text is inert and a bidi override is visible.
func TestStudioSetupContextFilesRawAndEscaped(t *testing.T) {
	h, _ := setupStudioFixture(t)
	body := getHX(t, h, "/app/admin/agents/sess-1/setup/sections?sections=context").Body.String()
	wantAll(t, "context", body,
		`<pre class="body">FILE                          BYTES`,
		`<details class="file"><summary><span class="mono">/agent-data/context/CORE.md</span>`,
		`<details class="file"><summary><span class="mono">/agent-data/context/INDEX.md</span>`,
		`# Rules &lt;script&gt;alert(1)&lt;/script&gt;`,
		`evil\u202etxt`)
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "\u202e") {
		t.Errorf("hostile text reached the page raw:\n%s", body)
	}
}

// A context body that is not the file table shows whole, as one block.
func TestStudioSetupContextUnsplittable(t *testing.T) {
	h, _ := setupStudioFixture(t)
	rec := getHX(t, h, "/app/admin/agents/ghost-1/setup/sections?sections=context")
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, `<details class="file">`) {
		t.Errorf("an unsplittable context body was split:\n%s", body)
	}
	wantAll(t, "unsplittable", body, `<details class="sec" id="setup-sec-context" open>`,
		"<pre class=\"body\">no context files\n--- /agent-data/context/CORE.md\nnot a file\n</pre>")
}

// Opened from a project, every link of the tab keeps the project scope.
func TestStudioSetupKeepsProjectScope(t *testing.T) {
	h, _ := setupStudioFixture(t)
	wantAll(t, "agent page", get(t, h, "/app/admin/agents/sess-1?project=acme").Body.String(),
		`<a href="/app/admin/agents/sess-1/setup?project=acme">Setup</a>`)
	wantAll(t, "setup page", get(t, h, "/app/admin/agents/sess-1/setup?project=acme").Body.String(),
		`<a href="/app/admin/projects/acme/agents">Agents</a>`,
		`<a href="/app/admin/agents/sess-1?project=acme">Overview</a>`,
		`hx-get="/app/admin/agents/sess-1/setup/sections?project=acme"`,
		`href="/app/admin/agents/sess-1/setup?project=acme&amp;sections=egress">egress</a>`,
		`href="/app/admin/agents/sess-1/setup?diff=role&amp;project=acme">Diff vs role</a>`)
}

func TestStudioSetupErrors(t *testing.T) {
	h, _ := setupStudioFixture(t)
	for path, want := range map[string]int{
		"/app/admin/agents/sess-1/setup?diff=yes":          http.StatusBadRequest,
		"/app/admin/agents/sess-1/setup/sections?diff=yes": http.StatusBadRequest,
		"/app/admin/agents/sess-1/setup/sections/bogus":    http.StatusBadRequest,
		"/app/admin/agents/nope/setup":                     http.StatusNotFound,
		"/app/admin/agents/nope/setup/sections":            http.StatusNotFound,
		"/app/admin/agents/nope/setup/sections/egress":     http.StatusNotFound,
		"/app/admin/agents/gone-1/setup":                   http.StatusNotFound,
		"/app/admin/agents/gone-1/setup/sections":          http.StatusNotFound,
	} {
		if rec := get(t, h, path); rec.Code != want {
			t.Errorf("%s = %d, want %d: %s", path, rec.Code, want, rec.Body)
		}
	}
	wantAll(t, "gone studio", get(t, h, "/app/admin/agents/nope/setup").Body.String(), "No running studio")
	if body := get(t, h, "/app/admin/agents/nope").Body.String(); strings.Contains(body, ">Setup</a>") {
		t.Error("Setup tab on an agent with no studio")
	}
	if body := get(t, h, "/app/admin/agents/gone-1").Body.String(); strings.Contains(body, ">Setup</a>") {
		t.Error("Setup tab on a gone studio")
	}
}

// A role's Setup tab offers a manager selector only with two or more managers;
// the current one is bold and the choice rides on the sections request.
func TestRoleSetupTabManagerSelector(t *testing.T) {
	build := func(names ...string) http.Handler {
		store := newStore(t)
		mustCreateProject(t, store, "acme")
		if err := store.PutRole("acme", jam.Role{Name: "dev"}); err != nil {
			t.Fatal(err)
		}
		return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil,
			adminui.WithSetupDeps(jam.SetupDeps{Store: store, Images: &countingImages{}}),
			adminui.WithSetupManagers(func() []string { return names }))
	}
	h := build("local", "m1")
	body := get(t, h, "/app/admin/projects/acme/roles/dev/setup?manager=m1").Body.String()
	wantAll(t, "selector", body,
		`<b class="chip accent">m1</b>`,
		`<a class="chip" href="/app/admin/projects/acme/roles/dev/setup?manager=local">local</a>`,
		`hx-get="/app/admin/projects/acme/roles/dev/setup/sections?manager=m1"`)
	if body := get(t, h, "/app/admin/projects/acme/roles/dev/setup").Body.String(); !strings.Contains(body, `<b class="chip accent">local</b>`) {
		t.Errorf("no manager chosen: the first should be current:\n%s", body)
	}
	if body := get(t, build("local"), "/app/admin/projects/acme/roles/dev/setup").Body.String(); strings.Contains(body, "Image manager") {
		t.Error("selector shown with one manager")
	}
}
