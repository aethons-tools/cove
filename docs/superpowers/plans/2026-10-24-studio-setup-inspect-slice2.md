# Studio setup inspection — slice 2 (the UI Setup tab) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator sees, in the admin UI, everything an agent is set up with. A **Setup** tab on a running studio's agent page (`/app/admin/agents/<id>/setup`) and on a role page (`/app/admin/projects/<project>/roles/<name>/setup`) shows slice 1's setup: one collapsible block per section with its source, `stale vs role` and snapshot time, the context files raw at their real paths, and — on a studio — a `stale vs role` badge that opens that section's diff against a raise of its role now. It is read-only, behind the same admin auth, and escaped through html/template.

**Architecture:**
- **The UI calls slice 1's renderers in-process.** `jam.RoleSetup` and `jam.InstanceSetup` are called with a `jam.SetupDeps` the serve hands to the UI through a new option, `adminui.WithSetupDeps`, the way `WithConfigEnv` hands it the planner's env. Serve builds one `SetupDeps` for the admin API (`jam.WithSetupInspect`) and the UI, so both show the same thing. Without the option the tab is hidden and its routes are not mounted.
- **The tab is a sub-page, loaded lazily.** `…/setup` renders a shell (crumbs, an **Overview · Setup** strip, section filter chips, **Diff vs role** on a studio) without inspecting anything. Its `#setup-sections` div fetches `…/setup/sections` with `hx-trigger="load"`, so the image reads happen after the page paints and never on the agent or role page itself.
- **Per-section diff.** A stale section's badge is a button that fetches `/app/admin/agents/{id}/setup/sections/{section}` — `InstanceSetup` for that one section with `diffRole` — and swaps the section's `<details>` (`hx-swap="outerHTML"`). `?diff=role` on the page diffs every section at once.
- **Display text is escaped twice over.** Bodies are plain text (already redacted by slice 1). The view model passes every body, reason, path and diff line through `jam.SetupDisplayText` (the text render's escaping without its indent: controls, bidi overrides and invalid UTF-8 become `\x1b`, `\u202e`, `\xff`), and html/template escapes the HTML. Nothing is ever `template.HTML`.
- **Context files come from the body's own format.** `jam.SplitContextBody` (next to `contextBody`, the format's owner) splits a context body into its head and its files using the file table. The UI shows each file in its own `<details>`, raw.
- **One diff partial.** The Config and kit pages' line-diff markup moves into a shared `diff-lines` template in `layout.html` (with its line CSS made global), and the Setup tab uses it.

**Tech Stack:** Go 1.26; `html/template` + htmx (adminui); slice 1's `jam` setup renderers (`RoleSetup`, `InstanceSetup`, `SetupDeps`), `linediff`.

**Spec:** [`docs/superpowers/specs/2026-10-24-studio-setup-inspect-design.md`](../specs/2026-10-24-studio-setup-inspect-design.md) §4 slice 2 (and §1 for the sections and sources, §5 for errors). It builds on [`2026-10-24-studio-setup-inspect-slice1.md`](2026-10-24-studio-setup-inspect-slice1.md).

## Spec deviations and interpretations

1. **"The studio page" is the agent page**, `/app/admin/agents/{id}`. Its Setup tab is shown only while the agent has a running studio (`agentDetail.Running`). `InstanceSetup` is 404 for a gone studio, and an enrolled identity with no studio has nothing to inspect. `/app/admin/agents/{id}/setup` for an unknown or gone studio is a 404 page ("No running studio").
2. **"A Setup tab" is a sub-page with its own URL**, not an in-page JavaScript tab.
   - Both pages get an **Overview · Setup** strip (`nav.subtabs`, the existing pill style) under their crumbs. Project pages already put each tab at its own URL, and the agent page already links its session timeline as a sub-page.
   - The strip sits above the role page's `role-body`, which section writes swap, so a write never drops the tab.
   - On an agent page linked with `?project=`, both strip links keep the scope (`agentHref`). The setup page's filter and loader URLs keep the whole query.
3. **Lazy load.** The shell never calls `RoleSetup`/`InstanceSetup`. The sections load from `…/setup/sections` with `hx-trigger="load"`. The agent and role pages themselves never inspect. A distinct fragment path, rather than an `HX-Request` branch on the page URL, keeps a cached fragment out of browser history.
4. **Context files are shown raw only, never rendered as markdown.** The spec allows "rendered (markdown) or raw".
   - The tab's purpose is to show what the agent reads, which is the raw text.
   - The repo's safe renderer (`squawkrender.Body`, goldmark without `WithUnsafe`) drops raw HTML and HTML comments. A rendered view would therefore hide exactly the injected text an operator inspects for.
   - Rendered markdown would also fetch remote images in the operator's browser, and the admin UI sends no CSP.
5. **Display escaping.** Every shown string goes through the new `jam.SetupDisplayText` = `escapeText(s, true)`, the CLI text render's escaping without the indent. A Trojan-source bidi override in a context file therefore shows as `\u202e`, as it does in the CLI.
6. **Splitting the context body.** `jam.SplitContextBody` reads the `FILE` table at the top of the body and cuts the body at each listed path's `\n--- <path>\n` marker in table order.
   - A truncated snapshot yields the files it still holds, the last one carrying the truncation marker.
   - A body that does not start with the table (`unavailable`, empty) shows as one raw block.
   - A file whose text contains the very next file's marker line would split early. That is cosmetic: every byte is still shown.
7. **The `stale vs role` badge.**
   - It shows only on stale sections (`SetupSection.Stale`). It is a button that loads that section's diff in place, from `GET /app/admin/agents/{id}/setup/sections/{section}`. Once diffed it becomes a plain chip.
   - **Diff vs role** (`?diff=role`) diffs every section. That also shows sections that differ without being flagged stale (for example `policy`, with a studio's idle override or alarms).
   - A section header counts the stale sections ("N section(s) stale vs role").
   - **The agent page's Overview carries no stale badge.** Computing staleness is a full inspect, image reads included, and the tab exists to keep that off the main page.
   - A studio whose role is gone shows the render's note (`the role no longer exists: no diff`) in place of the diff.
8. **The diff direction and labels are slice 1's:** "This studio (−) against a raise of its role now (+)". `identity` is never diffed. A role's Setup has no diff, and `?diff=` on a role's sections is 400.
9. **The section filter** is a row of chips: **All** plus one per section (`?sections=<name>`). `?sections=a,b` typed by hand works too (`jam.ParseSetupSections`), with no chip current. A single-section view opens its block. Otherwise only `identity` opens by default, along with any section whose diff changed.
10. **Off without `WithSetupDeps`.** The tab is hidden and its routes are not mounted (404). The handler does not build deps from its own store and supervisor: the sealed egress list and the image reader exist only in serve, and the tests' zero `jam.Supervisor{}` has no store to render with.
11. **Audit.** Each sections fragment load logs `ui setup inspected` with the operator, subject, sections and diff, the same fields as the API's `setup inspected`, and never a body. The shell does not log, because it inspects nothing.
12. **Errors.**
    - An unknown role or studio is 404: the page shows a banner, and a fragment answers with `renderError`, which the layout's `htmx:responseError` hook shows as a flash.
    - A bad `?sections=` or `?diff=` is 400. The page shows the error in a banner instead of the loader, so a bad bookmark is not a blank page.
    - A render error is 500 `setup unavailable`, and its text is logged.
13. **Shared diff partial.** The `<pre class="diff">…</pre>` markup in `config.html` and `kit.html` moves into a `diff-lines` template in `layout.html`. `pre.diff span`, `.d-add`, `.d-del` and `.d-skip` become global CSS in `layout.html`. The markup each page renders is unchanged byte for byte.
14. **Docs.** `inspect.md` owns setup inspection and gains a short `## In the admin UI` section, staying within its 200-line budget, so no new leaf is needed. `ui-pages.md` (already over budget) gets one pointer line under Agent pages, and `ui-projects.md` gets one under Role pages. The `inspect.md` row in `docs/usage/jam/INDEX.md` names the tab.

## Global Constraints

- Spec §4 slice 2, verbatim: "A **Setup** tab on the studio page and the role page: collapsible sections, context files shown rendered (markdown) or raw, a "stale vs role" badge that opens the diff. Read-only; escaping through html/template; same admin auth."
- Spec §1: each section shows `{name, source, stale, at}`. `source` is `snapshot` | `recomputed` | `image` | `config` | `unavailable`. `stale` is for studios only.
- Spec §5: "Inspect never fails as a whole … An unknown role or instance is 404. No error echoes a secret or unvalidated repo text."
- **Read-only.** The tab adds only `GET` routes. No form, no write, no `originGuard`.
- **No new rendering of setup data.** Bodies, reasons, sources and diffs come only from `jam.RoleSetup`/`jam.InstanceSetup`. The UI never re-reads the store for setup content and never un-redacts.
- **Escaping.** Every shown string is passed through `jam.SetupDisplayText` and then html/template. No `template.HTML`, and no markdown rendering of setup bodies.
- **Lazy.** Neither the agent page, the role page nor the setup shell calls `RoleSetup`/`InstanceSetup` or reads an image.
- **Logs** never carry a body: `ui setup inspected` (operator, subject, sections, diff); `ui setup inspect failed` (subject, err) on a 500.
- **Tests** are hermetic (MemStore, a fake `ImageFactsReader`). Use `GOPROXY=https://proxy.golang.org,direct`.
- **Docs** change in the same change as the behaviour. `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` reports no more than **723** errors: 721 on this branch's base (`3ae4a46`) plus 2 for this plan file.

## Review Focus

1. **Hostile text in a context file or snapshot body** — `<script>`, a bidi override (U+202E), an escape sequence. It must show as inert text (`&lt;script&gt;`, `\u202e`), never as markup or a reordered line. *(Task 1: `TestSetupDisplayText`; Task 4: `TestStudioSetupContextFilesRawAndEscaped`.)*
2. **A slow or hung docker daemon on the launcher host.** The agent page, the role page and the setup shell must render without any image read. Only the lazy fragment reads. *(Task 4: `TestSetupShellReadsNoImage`.)*
3. **A studio whose role was deleted.** Its Setup must render, and a stale badge's diff shows the note instead of failing. *(Task 4: `TestStudioSetupDiffRoleGone`.)*
4. **A bookmarked or hand-edited URL with a bad `?sections=` or `?diff=`.** The page must explain itself (a 400 banner), not hang on "Loading…". *(Task 3: `TestRoleSetupErrors`; Task 4: `TestStudioSetupErrors`.)*
5. **An agent page opened from a project (`?project=acme`).** Overview, Setup, the filter chips and the loader must keep the project scope. *(Task 4: `TestStudioSetupKeepsProjectScope`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/setup.go` (modify) | `SetupDisplayText` |
| `internal/jam/setup_parts.go` (modify) | `ContextBodyFile`, `SplitContextBody` |
| `internal/jam/setup_split_test.go` (create) | tests for both |
| `internal/jam/adminui/templates/layout.html`, `config.html`, `kit.html` (modify) | the shared `diff-lines` partial and its global CSS |
| `internal/jam/adminui/diff_partial_internal_test.go` (create) | the partial on every page |
| `internal/jam/adminui/setup_page.go` (create) | `WithSetupDeps`, the page/fragment views, query helpers, `registerSetup` (role and studio routes) |
| `internal/jam/adminui/templates/setup.html` (create) | the Setup shell, `setup-sections`, `setup-section` |
| `internal/jam/adminui/adminui.go` (modify) | `options.setup`, the `role-setup`/`agent-setup` pages, `registerSetup`, the Setup flags passed to the role and agent handlers |
| `internal/jam/adminui/role_detail.go`, `templates/role.html` (modify) | `roleDetail.SetupEnabled`, the Overview · Setup strip |
| `internal/jam/adminui/agent.go`, `templates/agent.html` (modify) | `agentDetail.SetupEnabled`, the strip |
| `internal/jam/adminui/setup_page_test.go` (create) | role and studio Setup tests |
| `cmd/at-jam/main.go` (modify) | one `setupDeps` for `WithSetupInspect` and `adminui.WithSetupDeps` |
| `docs/usage/jam/inspect.md`, `INDEX.md`, `ui-pages.md`, `ui-projects.md` (modify) | docs |

---

### Task 1: `SetupDisplayText` and `SplitContextBody`

**Files:**
- Modify: `internal/jam/setup.go` (after `flat`), `internal/jam/setup_parts.go` (after `contextBody`)
- Test: `internal/jam/setup_split_test.go` (create, package `jam`)

**Interfaces:**
- Consumes: slice 1's `escapeText`, `contextBody`, `sessionctx.Dir`.
- Produces:
```go
func SetupDisplayText(s string) string
type ContextBodyFile struct{ Path, Body string }
func SplitContextBody(body string) (head string, files []ContextBodyFile, ok bool)
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/setup_split_test.go`:

```go
package jam

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// The UI's text is the text render's, minus the indent: layout kept, every
// control, bidi override and invalid byte made visible.
func TestSetupDisplayText(t *testing.T) {
	got := SetupDisplayText("a\tb\nc\u202ed\x1b[31m\xff")
	if want := "a\tb\nc\\u202ed\\x1b[31m\\xff"; got != want {
		t.Fatalf("SetupDisplayText = %q, want %q", got, want)
	}
}

// A context body splits back into its head and its files, in the table's
// order; a file may hold a line that looks like a marker.
func TestSplitContextBody(t *testing.T) {
	b := sessionctx.Bundle{
		Core:     "# Core\n\n--- not a marker\n",
		Files:    map[string]string{"INDEX.md": "index\n", "role/dev.md": "role leaf"},
		Warnings: []string{"w1"},
	}
	body := contextBody(b)
	head, files, ok := SplitContextBody(body)
	if !ok {
		t.Fatalf("not split:\n%s", body)
	}
	if !strings.HasPrefix(head, "FILE") || !strings.HasSuffix(head, "warning: w1\n") || strings.Contains(head, "--- ") {
		t.Errorf("head = %q", head)
	}
	want := []ContextBodyFile{
		{Path: sessionctx.Dir + "/CORE.md", Body: "# Core\n\n--- not a marker\n"},
		{Path: sessionctx.Dir + "/INDEX.md", Body: "index\n"},
		{Path: sessionctx.Dir + "/role/dev.md", Body: "role leaf\n"},
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files = %+v\nwant  %+v", files, want)
	}
}

// A truncated snapshot keeps the files it still holds; the last carries the
// truncation marker.
func TestSplitContextBodyTruncated(t *testing.T) {
	body := contextBody(sessionctx.Bundle{Core: "core\n", Files: map[string]string{"INDEX.md": "index\n", "role/dev.md": "leaf\n"}})
	cut := strings.Index(body, "\n--- "+sessionctx.Dir+"/role/dev.md")
	_, files, ok := SplitContextBody(body[:cut] + "\n… truncated: 99 bytes in all (cap 64)\n")
	if !ok || len(files) != 2 || !strings.Contains(files[1].Body, "truncated") || !strings.HasPrefix(files[1].Body, "index\n") {
		t.Fatalf("ok=%v files=%+v", ok, files)
	}
}

func TestSplitContextBodyNotAContextBody(t *testing.T) {
	for _, body := range []string{"", "policy: kit default\n", "FILE  BYTES\n"} {
		if _, _, ok := SplitContextBody(body); ok {
			t.Errorf("SplitContextBody(%q) ok", body)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestSetupDisplayText|TestSplitContextBody' -v`
Expected: FAIL to compile: `undefined: SetupDisplayText`, `undefined: SplitContextBody`, `undefined: ContextBodyFile`.

- [ ] **Step 3: Implement**

In `internal/jam/setup.go`, after `func flat(s string) string { … }`, add:

```go
// SetupDisplayText is a setup body (or reason, path or diff line) as the
// admin UI shows it: the text render's escaping without its indent — \n and
// \t kept; every other control or format character (bidi overrides included)
// and every invalid UTF-8 byte made visible (\x1b, \u202e, \xff). HTML
// escaping is the template's.
func SetupDisplayText(s string) string { return escapeText(s, true) }
```

In `internal/jam/setup_parts.go`, after `contextBody`, add:

```go
// ContextBodyFile is one file of a context section body: its real path and
// its text.
type ContextBodyFile struct{ Path, Body string }

// SplitContextBody splits a context section body (contextBody's output, a
// snapshot or a recompute) back into its head — the file table, the budgets,
// the warnings — and its files, in the table's order. Only the marker of the
// next listed path ends a file, so a file may hold any other "--- " line. A
// truncated body yields the files it still holds (the last one carrying the
// truncation marker). ok is false when body does not start with the table
// or holds none of its files (an unavailable or empty section).
func SplitContextBody(body string) (head string, files []ContextBodyFile, ok bool) {
	lines := strings.Split(body, "\n")
	if !strings.HasPrefix(lines[0], "FILE") {
		return "", nil, false
	}
	var paths []string
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) != 2 || !strings.HasPrefix(f[0], sessionctx.Dir+"/") {
			break
		}
		paths = append(paths, f[0])
	}
	start := -1 // where the current file's text begins
	for _, p := range paths {
		marker := "\n--- " + p + "\n"
		from := max(start, 0)
		i := strings.Index(body[from:], marker)
		if i < 0 {
			break // truncated before this file
		}
		i += from
		if start < 0 {
			head = body[:i]
		} else {
			files[len(files)-1].Body = body[start:i]
		}
		files = append(files, ContextBodyFile{Path: p})
		start = i + len(marker)
	}
	if start < 0 {
		return "", nil, false
	}
	files[len(files)-1].Body = body[start:]
	return head, files, true
}
```

(`contextBody` writes the table with `writeTable`: a `FILE  BYTES` header, then one `<path>  <bytes>` row per file; each file follows as `\n--- <path>\n<text>`, its text ending in a newline, `ensureNL`. Paths are `sessionctx.Dir + "/" + flat(p)` in both places, and leaf names never hold spaces (`leafNameRE`).)

- [ ] **Step 4: Run them to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestSetupDisplayText|TestSplitContextBody' -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/jam/setup.go internal/jam/setup_parts.go internal/jam/setup_split_test.go
git commit -m "feat(jam): SetupDisplayText and SplitContextBody for the setup UI"
```

---

### Task 2: One `diff-lines` partial for every page

**Files:**
- Modify: `internal/jam/adminui/templates/layout.html` (the `<style>` block that ends near line 148; the partials after line 394), `templates/config.html:14-18,100`, `templates/kit.html:31-35,87`
- Test: `internal/jam/adminui/diff_partial_internal_test.go` (create, package `adminui`)

**Interfaces:**
- Consumes: `diffLine` (= `linediff.Line`, `kit_detail.go:134`).
- Produces: the template `{{template "diff-lines" <[]diffLine>}}`, which renders `<pre class="diff">…</pre>` and is available on every page (it is defined in `layout.html`). The global CSS classes are `pre.diff span`, `.d-add`, `.d-del` and `.d-skip`.

- [ ] **Step 1: Write the failing test**

Create `internal/jam/adminui/diff_partial_internal_test.go`:

```go
package adminui

import (
	"bytes"
	"testing"
)

// Every page set carries the one line-diff partial (the Config, kit and Setup
// pages render through it), escaping each line's text.
func TestDiffLinesPartialOnEveryPage(t *testing.T) {
	lines := []diffLine{{Op: " ", Text: "same"}, {Op: "-", Text: "old <b>"}, {Op: "+", Text: "new"}, {Op: "…", Text: "4 unchanged lines"}}
	want := `<pre class="diff"><span>  same</span><span class="d-del">- old &lt;b&gt;</span><span class="d-add">+ new</span><span class="d-skip">  … 4 unchanged lines</span></pre>`
	for name, p := range pages {
		t2, err := p.t.Clone()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := t2.ExecuteTemplate(&buf, "diff-lines", lines); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if buf.String() != want {
			t.Errorf("%s: diff-lines =\n%s\nwant\n%s", name, buf.String(), want)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ -run TestDiffLinesPartialOnEveryPage -v`
Expected: FAIL: `html/template: no such template "diff-lines"`.

- [ ] **Step 3: Implement**

In `templates/layout.html`, after the `{{define "managed-badge"}}…{{end}}` line, add:

```html
{{/* diff-lines is a line diff ([]linediff.Line): one span per line, "+"/"-"/"…" marked. */}}
{{define "diff-lines"}}<pre class="diff">{{range .}}{{if eq .Op "+"}}<span class="d-add">+ {{.Text}}</span>{{else if eq .Op "-"}}<span class="d-del">- {{.Text}}</span>{{else if eq .Op "…"}}<span class="d-skip">  … {{.Text}}</span>{{else}}<span>  {{.Text}}</span>{{end}}{{end}}</pre>{{end}}
```

In the same file, just before the `</style>` that closes the layout's style block (near line 148), add:

```css
    /* Line diffs (the diff-lines partial); each page sizes its own pre.diff. */
    pre.diff span{display:block;white-space:pre}
    .d-add{background:var(--live-soft);color:var(--live)}
    .d-del{background:var(--bad-soft);color:var(--bad)}
    .d-skip{color:var(--faint);font-style:italic}
```

In `templates/config.html`, delete these four style lines (keep `#config pre.diff{…}` above them):

```css
  #config pre.diff span{display:block;white-space:pre}
  #config .d-add{background:var(--live-soft);color:var(--live)}
  #config .d-del{background:var(--bad-soft);color:var(--bad)}
  #config .d-skip{color:var(--faint);font-style:italic}
```

and in the `config-plan` template replace

```html
{{else}}<pre class="diff">{{range .Lines}}{{if eq .Op "+"}}<span class="d-add">+ {{.Text}}</span>{{else if eq .Op "-"}}<span class="d-del">- {{.Text}}</span>{{else if eq .Op "…"}}<span class="d-skip">  … {{.Text}}</span>{{else}}<span>  {{.Text}}</span>{{end}}{{end}}</pre>{{end}}
```

with

```html
{{else}}{{template "diff-lines" .Lines}}{{end}}
```

In `templates/kit.html`, delete these four style lines (keep `pre.yaml,pre.diff{…}` above them):

```css
  pre.diff span{display:block;white-space:pre}
  .d-add{background:var(--live-soft);color:var(--live)}
  .d-del{background:var(--bad-soft);color:var(--bad)}
  .d-skip{color:var(--faint);font-style:italic}
```

and replace the `<pre class="diff">{{range .Lines}}…{{end}}</pre>` line under `{{if .TooLarge}}…{{else}}` with

```html
      {{template "diff-lines" .Lines}}
```

- [ ] **Step 4: Run the package tests**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/`
Expected: PASS, including the new test, `kits_page_test.go`'s diff assertions (`class="d-del"`, `class="d-add"`) and the Config page tests.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/adminui/templates/layout.html internal/jam/adminui/templates/config.html internal/jam/adminui/templates/kit.html internal/jam/adminui/diff_partial_internal_test.go
git commit -m "refactor(adminui): one diff-lines partial for the Config and kit pages"
```

---

### Task 3: The Setup tab machinery and the role page's Setup tab

**Files:**
- Create: `internal/jam/adminui/setup_page.go`, `internal/jam/adminui/templates/setup.html`, `internal/jam/adminui/setup_page_test.go` (package `adminui_test`)
- Modify: `internal/jam/adminui/adminui.go` (`options`, `pages`, `Handler`), `internal/jam/adminui/role_detail.go` (`roleDetail`, `handleRoleDetail`), `internal/jam/adminui/templates/role.html` (after `{{template "crumbs" .Crumbs}}`)

**Interfaces:**
- Consumes: `jam.RoleSetup(ctx, jam.SetupDeps, project, role string, sections []string) (jam.StudioSetup, error)`, `jam.ParseSetupSections`, `jam.SetupSectionNames`, `jam.ErrNotFound`, `jam.ErrUnknownSection`, `jam.SectionIdentity`, `jam.SetupDisplayText` (Task 1), `diff-lines` (Task 2); adminui's `subTab`, `crumb`, `projectCrumbs`, `sectionAgents`, `orDefaultProject`, `render`/`renderStatus`/`renderFragment`, `renderError`, `diffLine`.
- Produces:
```go
func WithSetupDeps(d jam.SetupDeps) Option
type setupPage struct { Title string; Studio bool; ID, Project, Name, ConfigName string; Crumbs []crumb;
	Filter []subTab; DiffHref string; Diffing bool; SectionsURL, Error string; NotFound bool }
type setupView struct { Note string; Stale int; Sections []setupSectionView }
type setupSectionView struct { Name, Source, Reason string; Stale bool; At, AtISO, Body string; Open bool;
	DiffURL string; Diffed, DiffTooLarge, DiffSame bool; Diff []diffLine; DiffNote string }
func setupQuery(q url.Values, studio bool) (sections []string, diff bool, err error)
func fillSetupPage(p *setupPage, r *http.Request) int
func newSetupView(s jam.StudioSetup, diffBase string) setupView
func newSectionView(sec jam.SetupSection, diffBase string) setupSectionView
func auditSetup(log *slog.Logger, r *http.Request, subject string, sections []string, diff bool)
func setupFail(w http.ResponseWriter, log *slog.Logger, err error, subject, notFound string)
func registerSetup(mux *http.ServeMux, store jam.Store, d *jam.SetupDeps, log *slog.Logger)
// pages "role-setup" (projPage) and "agent-setup" (jamPage("agents")), both setup.html;
// templates "setup-sections" (setupView) and "setup-section" (setupSectionView).
func handleRoleDetail(w http.ResponseWriter, r *http.Request, store jam.Store, img jam.ImageResolver, canRequest, setupOn bool)
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/adminui/setup_page_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ -run 'TestRoleSetup|TestSetupTabOff' -v`
Expected: FAIL to compile: `undefined: adminui.WithSetupDeps`.

- [ ] **Step 3: Write `setup_page.go`**

Create `internal/jam/adminui/setup_page.go`:

```go
package adminui

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// WithSetupDeps enables the Setup tab of a running studio's agent page and of
// a role page: it renders jam.InstanceSetup / jam.RoleSetup in-process with
// d — the deps serve gives the admin API's jam.WithSetupInspect. Without it
// the tab is hidden and its routes are not mounted.
func WithSetupDeps(d jam.SetupDeps) Option {
	return func(o *options) { o.setup = &d }
}

// setupPage is the Setup tab's shell: the subject's head, the Overview ·
// Setup strip, the section filter, the diff toggle (a studio's) and the lazy
// loader. It inspects nothing: SectionsURL is fetched once it has painted.
type setupPage struct {
	Title       string
	Studio      bool    // a studio's tab; else a role's
	ID          string  // studio: its session id (the agent id)
	Project     string  // role: the project, as in its URL
	Name        string  // role: the role's name
	ConfigName  string  // role: "<project>/<role>" (the managed badge)
	Crumbs      []crumb // role: Agents / <role> / Setup
	Filter      []subTab
	DiffHref    string // studio: this page with ?diff=role toggled
	Diffing     bool   // studio: ?diff=role is on
	SectionsURL string // the fragment the loader fetches, with this page's query
	Error       string // a bad ?sections= or ?diff=, shown instead of the loader
	NotFound    bool
}

// setupView is the sections fragment.
type setupView struct {
	Note     string // the render's note (a studio whose role is gone: no diff)
	Stale    int    // sections stale vs role
	Sections []setupSectionView
}

// setupSectionView is one collapsible section. Every string in it has been
// through jam.SetupDisplayText; html/template escapes the rest.
type setupSectionView struct {
	Name, Source, Reason string
	Stale                bool
	At, AtISO            string // the snapshot time ("" without one)
	Body                 string
	Open                 bool
	DiffURL              string // a stale, undiffed studio section: its diff fragment
	Diffed, DiffTooLarge bool
	DiffSame             bool // diffed, and identical to a raise now
	Diff                 []diffLine
	DiffNote             string // why there is no diff (the render's note)
}

var errSetupDiff = errors.New(`diff: want "role"`)

// setupQuery reads ?sections= (empty = all) and, on a studio, ?diff=role.
func setupQuery(q url.Values, studio bool) (sections []string, diff bool, err error) {
	sections, err = jam.ParseSetupSections(q.Get("sections"))
	if err != nil {
		return nil, false, err
	}
	switch d := q.Get("diff"); {
	case d == "":
		return sections, false, nil
	case d == "role" && studio:
		return sections, true, nil
	default:
		return nil, false, errSetupDiff
	}
}

func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

func cloneQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = v
	}
	return out
}

// setupFilter is the section chips: All, then one per section, each keeping
// the rest of the query (?project=, ?diff=).
func setupFilter(base string, q url.Values) []subTab {
	cur := q.Get("sections")
	all := cloneQuery(q)
	all.Del("sections")
	out := []subTab{{Label: "All", Href: withQuery(base, all), Current: cur == ""}}
	for _, n := range jam.SetupSectionNames() {
		one := cloneQuery(q)
		one.Set("sections", n)
		out = append(out, subTab{Label: n, Href: withQuery(base, one), Current: cur == n})
	}
	return out
}

// fillSetupPage completes the shell from its request; a bad query is p.Error
// and 400.
func fillSetupPage(p *setupPage, r *http.Request) int {
	base, q := r.URL.EscapedPath(), r.URL.Query()
	_, diff, err := setupQuery(q, p.Studio)
	if err != nil {
		p.Error = err.Error()
		return http.StatusBadRequest
	}
	p.Diffing = diff
	p.Filter = setupFilter(base, q)
	if p.Studio {
		t := cloneQuery(q)
		if diff {
			t.Del("diff")
		} else {
			t.Set("diff", "role")
		}
		p.DiffHref = withQuery(base, t)
	}
	p.SectionsURL = withQuery(base+"/sections", q)
	return http.StatusOK
}

// newSetupView is the fragment of s; diffBase ("" = none) is where a stale
// section's diff loads from (diffBase + "/" + name).
func newSetupView(s jam.StudioSetup, diffBase string) setupView {
	v := setupView{Note: jam.SetupDisplayText(s.Note)}
	for _, sec := range s.Sections {
		sv := newSectionView(sec, diffBase)
		if sv.Stale {
			v.Stale++
		}
		v.Sections = append(v.Sections, sv)
	}
	if len(v.Sections) == 1 {
		v.Sections[0].Open = true
	}
	return v
}

func newSectionView(sec jam.SetupSection, diffBase string) setupSectionView {
	sv := setupSectionView{
		Name: sec.Name, Source: sec.Source, Reason: jam.SetupDisplayText(sec.Reason), Stale: sec.Stale,
		Body: jam.SetupDisplayText(sec.Body), Diffed: sec.Diffed, DiffTooLarge: sec.DiffTooLarge,
		Open: sec.Name == jam.SectionIdentity,
	}
	if !sec.At.IsZero() {
		sv.At, sv.AtISO = sec.At.UTC().Format("2006-01-02 15:04:05 UTC"), sec.At.UTC().Format(time.RFC3339)
	}
	if sec.Diffed {
		changed := false
		for _, l := range sec.Diff {
			sv.Diff = append(sv.Diff, diffLine{Op: l.Op, Text: jam.SetupDisplayText(l.Text)})
			changed = changed || l.Op == "+" || l.Op == "-"
		}
		sv.DiffSame = !sec.DiffTooLarge && !changed
		sv.Open = sv.Open || changed || sec.DiffTooLarge
	}
	if sec.Stale && !sec.Diffed && diffBase != "" {
		sv.DiffURL = diffBase + "/" + url.PathEscape(sec.Name)
	}
	return sv
}

// auditSetup logs an inspection as the admin API does — never a body.
func auditSetup(log *slog.Logger, r *http.Request, subject string, sections []string, diff bool) {
	log.Info("ui setup inspected", "operator", jam.OperatorID(r), "subject", subject, "sections", strings.Join(sections, ","), "diff", diff)
}

// setupFail maps a render error to its fixed-text response.
func setupFail(w http.ResponseWriter, log *slog.Logger, err error, subject, notFound string) {
	switch {
	case errors.Is(err, jam.ErrNotFound):
		renderError(w, http.StatusNotFound, notFound)
	case errors.Is(err, jam.ErrUnknownSection):
		renderError(w, http.StatusBadRequest, err.Error())
	default:
		log.Warn("ui setup inspect failed", "subject", subject, "err", err.Error())
		renderError(w, http.StatusInternalServerError, "setup unavailable")
	}
}

// registerSetup mounts the Setup tabs (nothing without deps):
//
//	GET /app/admin/projects/{project}/roles/{name}/setup[/sections]
func registerSetup(mux *http.ServeMux, store jam.Store, d *jam.SetupDeps, log *slog.Logger) {
	if d == nil {
		return
	}
	deps := *d
	mux.HandleFunc("GET /app/admin/projects/{project}/roles/{name}/setup", func(w http.ResponseWriter, r *http.Request) {
		project, name := r.PathValue("project"), r.PathValue("name")
		p := setupPage{Title: name + " setup", Project: project, Name: name, ConfigName: orDefaultProject(project) + "/" + name,
			Crumbs: append(projectCrumbs(project, sectionAgents, name), crumb{Label: "Setup"})}
		if _, ok := store.GetRole(project, name); !ok {
			p.NotFound = true
			renderStatus(w, r, http.StatusNotFound, "role-setup", p)
			return
		}
		renderStatus(w, r, fillSetupPage(&p, r), "role-setup", p)
	})
	mux.HandleFunc("GET /app/admin/projects/{project}/roles/{name}/setup/sections", func(w http.ResponseWriter, r *http.Request) {
		sections, _, err := setupQuery(r.URL.Query(), false)
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		project, name := r.PathValue("project"), r.PathValue("name")
		s, err := jam.RoleSetup(r.Context(), deps, project, name, sections)
		if err != nil {
			setupFail(w, log, err, "role "+project+"/"+name, "role "+project+"/"+name+" does not exist")
			return
		}
		auditSetup(log, r, s.Subject, sections, false)
		renderFragment(w, r, "role-setup", "setup-sections", newSetupView(s, ""))
	})
}
```

- [ ] **Step 4: Write `setup.html`**

Create `internal/jam/adminui/templates/setup.html`:

```html
{{define "content"}}
<style>
  #setup-page .crumbs{font-size:12.5px;color:var(--faint);margin-bottom:4px}
  #setup-page .crumbs a{color:var(--muted);text-decoration:none}
  #setup-page .filter{display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin-bottom:12px}
  #setup-page .filter .spacer{flex:1}
  #setup-page details.sec{border:1px solid var(--border);border-radius:12px;background:var(--surface);margin-bottom:10px}
  #setup-page details.sec>summary{cursor:pointer;display:flex;gap:8px;align-items:baseline;flex-wrap:wrap;padding:10px 16px}
  #setup-page details.sec>summary .name{font-weight:600}
  #setup-page details.sec>.body{padding:0 16px 12px}
  #setup-page details.file{margin-top:8px}
  #setup-page details.file>summary{cursor:pointer;font-size:13px}
  #setup-page pre.body,#setup-page pre.diff{margin:6px 0 0;padding:8px 12px;overflow-x:auto;font:12.5px/1.55 "IBM Plex Mono",ui-monospace,monospace;background:var(--surface-2);border-radius:8px}
  #setup-page pre.body{white-space:pre}
  #setup-page .chip.stale{background:var(--wait-soft);color:var(--wait);border:0}
  #setup-page button.chip.stale{cursor:pointer}
  #setup-page .chip.src-unavailable{color:var(--faint)}
  #setup-page .unset{color:var(--faint)}
  #setup-page h3.sub{margin:10px 0 0;font-size:12.5px;font-weight:600}
</style>
<div id="setup-page">
{{if .Studio}}{{$f := frame}}<div class="crumbs"><a href="{{if $f.Project}}{{projectURL $f.Project}}/agents{{else}}/app/admin/agents{{end}}">Agents</a> / <a href="{{agentHref .ID ""}}">{{.ID}}</a> / <span>Setup</span></div>
{{else}}{{template "crumbs" .Crumbs}}{{end}}
{{if .NotFound}}
<div class="page-head"><h1>Not found</h1></div>
<div class="banner">{{if .Studio}}No running studio <span class="mono">{{.ID}}</span> — only a running studio has a setup to inspect. <a href="{{agentHref .ID ""}}">Back to the agent</a>{{else}}No role <span class="mono">{{.Name}}</span> in project <span class="mono">{{.Project}}</span>.{{end}}</div>
{{else}}
{{if .Studio}}<nav class="subtabs" aria-label="Agent views"><a href="{{agentHref .ID ""}}">Overview</a><a href="{{agentHref .ID "/setup"}}" aria-current="page">Setup</a></nav>
{{else}}<nav class="subtabs" aria-label="Role views"><a href="{{roleURL .Project .Name}}">Overview</a><a href="{{roleURL .Project .Name}}/setup" aria-current="page">Setup</a></nav>{{end}}
<div class="page-head">
  {{if .Studio}}<h1 class="mono">{{.ID}}</h1>{{else}}<h1>{{.Name}}</h1>{{template "managed-badge" (managed "role" .ConfigName)}}{{end}}
  <span class="sub">{{if .Studio}}what this studio got — its snapshots (else a labelled recompute), config as of now, its image{{else}}what a studio of this role raised now would get{{end}}</span>
</div>
{{if .Error}}<div class="banner error">{{.Error}}</div>
{{else}}
<div class="filter">
  {{range .Filter}}<a class="chip{{if .Current}} accent{{end}}" href="{{.Href}}">{{.Label}}</a>{{end}}
  <span class="spacer"></span>
  {{if .Studio}}<a class="btn small" href="{{.DiffHref}}">{{if .Diffing}}Hide diff{{else}}Diff vs role{{end}}</a>{{end}}
</div>
<div id="setup-sections" hx-get="{{.SectionsURL}}" hx-trigger="load" hx-swap="innerHTML">
  <p class="unset">Loading the setup… (a first read of the image on the launcher host can take a few seconds)</p>
</div>
{{end}}
{{end}}
</div>
{{end}}

{{/* setup-sections is the lazily loaded fragment (setupView). */}}
{{define "setup-sections"}}
{{with .Note}}<div class="banner">{{.}}</div>{{end}}
{{if .Stale}}<p class="sub">{{.Stale}} section(s) stale vs role — a raise now would deliver something else. Open a <span class="chip stale">stale vs role</span> badge for that section's diff.</p>{{end}}
{{range .Sections}}{{template "setup-section" .}}{{end}}
{{end}}

{{/* setup-section is one section (setupSectionView); a stale badge swaps it for its diffed self. */}}
{{define "setup-section"}}
<details class="sec" id="setup-sec-{{.Name}}"{{if .Open}} open{{end}}>
  <summary><span class="name mono">{{.Name}}</span> <span class="chip src-{{.Source}}">{{.Source}}</span>
    {{- if .DiffURL}} <button type="button" class="chip stale" hx-get="{{.DiffURL}}" hx-target="#setup-sec-{{.Name}}" hx-swap="outerHTML" title="Show this section's diff against a raise of its role now">stale vs role</button>
    {{- else if .Stale}} <span class="chip stale">stale vs role</span>{{end}}
    {{- with .At}} <time class="sub" datetime="{{$.AtISO}}">{{.}}</time>{{end}}
  </summary>
  <div class="body">
    {{with .Reason}}<p class="unset">{{.}}</p>{{end}}
    {{if .Body}}<pre class="body">{{.Body}}</pre>{{end}}
    {{with .DiffNote}}<p class="unset">{{.}}</p>{{end}}
    {{if .Diffed}}
      <h3 class="sub">This studio (−) against a raise of its role now (+)</h3>
      {{if .DiffTooLarge}}<p class="unset">Too large to diff here.</p>{{else if .DiffSame}}<p class="unset">Same as a raise now.</p>{{else}}{{template "diff-lines" .Diff}}{{end}}
    {{end}}
  </div>
</details>
{{end}}
```

- [ ] **Step 5: Wire the option, the pages and the role page's strip**

In `internal/jam/adminui/adminui.go`:
- Add `setup *jam.SetupDeps` to `options`, after `configEnv` (comment: `// nil: no Setup tabs (WithSetupDeps)`).
- Add two entries to `pages`, after `"config"`:

```go
	"role-setup":   mustParse(projPage, "setup.html"),
	"agent-setup":  mustParse(jamPage("agents"), "setup.html"),
```

- In `Handler`, change the role page route's call to `handleRoleDetail(w, r, store, sup, canEdit, o.setup != nil)`.
- In `Handler`, after `registerAccess(mux, store, o.live, log, guardWrite)`, add `registerSetup(mux, store, o.setup, log)`.

In `internal/jam/adminui/role_detail.go`, add the field `SetupEnabled bool // the Setup tab is mounted (WithSetupDeps)` to `roleDetail` after `CanEdit`. Change `handleRoleDetail` (its only caller is `Handler`; check with `grep -n handleRoleDetail internal/jam/adminui/*.go`) to:

```go
func handleRoleDetail(w http.ResponseWriter, r *http.Request, store jam.Store, img jam.ImageResolver, canRequest, setupOn bool) {
	project, name := r.PathValue("project"), r.PathValue("name")
	d, ok := buildRoleDetail(store, img, project, name)
	if !ok {
		renderStatus(w, r, http.StatusNotFound, "role", roleDetail{Title: "Role not found", NotFound: true,
			Project: project, Name: name, RolesHref: projectSectionURL(project, sectionAgents)})
		return
	}
	d.Crumbs = projectCrumbs(project, sectionAgents, name)
	d.CanRequest = canRequest
	d.SetupEnabled = setupOn
	render(w, r, "role", d)
}
```

In `internal/jam/adminui/templates/role.html`, replace `{{template "crumbs" .Crumbs}}` (in `content`, just above `{{template "role-body" .}}`) with:

```html
{{template "crumbs" .Crumbs}}
{{if .SetupEnabled}}<nav class="subtabs" aria-label="Role views"><a href="{{roleURL .Project .Name}}" aria-current="page">Overview</a><a href="{{roleURL .Project .Name}}/setup">Setup</a></nav>{{end}}
```

(The strip is outside `role-body`, which every section write re-renders without `SetupEnabled`.)

- [ ] **Step 6: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ -run 'TestRoleSetup|TestSetupTabOff|TestRoleDetail|TestRolePage' -v`
Expected: PASS. Then run the whole package with `go test ./internal/jam/adminui/` (PASS).

- [ ] **Step 7: Commit**

```bash
git add internal/jam/adminui/setup_page.go internal/jam/adminui/templates/setup.html internal/jam/adminui/setup_page_test.go internal/jam/adminui/adminui.go internal/jam/adminui/role_detail.go internal/jam/adminui/templates/role.html
git commit -m "feat(adminui): Setup tab on the role page (lazy-loaded setup sections)"
```

---

### Task 4: The studio's Setup tab — stale badges, diffs and context files

**Files:**
- Modify: `internal/jam/adminui/setup_page.go` (`registerSetup`, `setupSectionView`, `newSectionView`), `internal/jam/adminui/templates/setup.html` (`setup-section`), `internal/jam/adminui/agent.go` (`agentDetail`, `registerAgent`), `internal/jam/adminui/templates/agent.html` (after the crumbs line), `internal/jam/adminui/adminui.go` (the `registerAgent` call)
- Test: `internal/jam/adminui/setup_page_test.go`

**Interfaces:**
- Consumes: `jam.InstanceSetup(ctx, jam.SetupDeps, ref string, sections []string, diffRole bool) (jam.StudioSetup, error)`, `jam.ResolveSession`, `jam.PhaseGone`, `jam.SectionContext`, `jam.SplitContextBody` (Task 1); Task 3's `setupPage`, `fillSetupPage`, `newSetupView`, `setupQuery`, `auditSetup`, `setupFail`, the `agent-setup` page.
- Produces:
```go
type setupFileView struct{ Path, Body string; Bytes int }
// setupSectionView gains: Head string; Files []setupFileView
// routes: GET /app/admin/agents/{id}/setup, …/setup/sections (?sections=, ?diff=role), …/setup/sections/{section} (one section, diffed)
func registerAgent(mux *http.ServeMux, store jam.Store, msgs SquawkReader, sess sessionevents.Store, acc accessDeps, canEdit, setupOn bool)
// agentDetail gains SetupEnabled bool
```

- [ ] **Step 1: Write the failing tests**

Add `"context"`, `"sync/atomic"` and `"time"` to the imports of `internal/jam/adminui/setup_page_test.go`. Then append:

```go
// countingImages is an image reader that counts its reads (and finds nothing).
type countingImages struct{ n atomic.Int32 }

func (c *countingImages) ImageFacts(context.Context, string) (jam.ImageFacts, error) {
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
// studio of a deleted role.
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
	wantAll(t, "setup page", get(t, h, "/app/admin/agents/sess-1/setup").Body.String(),
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
// source show; the count of stale sections leads the fragment.
func TestStudioSetupStaleBadge(t *testing.T) {
	h, _ := setupStudioFixture(t)
	body := getHX(t, h, "/app/admin/agents/sess-1/setup/sections").Body.String()
	wantAll(t, "sections", body,
		"1 section(s) stale vs role",
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

// ?diff=role diffs every section but identity.
func TestStudioSetupDiffAll(t *testing.T) {
	h, _ := setupStudioFixture(t)
	body := getHX(t, h, "/app/admin/agents/sess-1/setup/sections?diff=role").Body.String()
	if n, want := strings.Count(body, "This studio (−)"), len(jam.SetupSectionNames())-1; n != want {
		t.Errorf("%d diffed sections, want %d", n, want)
	}
	wantAll(t, "diff all", body, `<span class="d-add">+ new.example.com`, "Same as a raise now.")
}

// A studio whose role is gone still renders; its diff is the note.
func TestStudioSetupDiffRoleGone(t *testing.T) {
	h, _ := setupStudioFixture(t)
	rec := getHX(t, h, "/app/admin/agents/ghost-1/setup/sections/egress")
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
	wantAll(t, "role gone", rec.Body.String(), "the role no longer exists: no diff", "policy: kit default")
	if strings.Contains(rec.Body.String(), "d-del") {
		t.Error("a diff against a deleted role")
	}
}

// Context files show one by one, raw, at their real paths; hostile text is
// inert and a bidi override is visible.
func TestStudioSetupContextFilesRawAndEscaped(t *testing.T) {
	h, _ := setupStudioFixture(t)
	body := getHX(t, h, "/app/admin/agents/sess-1/setup/sections?sections=context").Body.String()
	wantAll(t, "context", body,
		`<details class="file"><summary><span class="mono">/agent-data/context/CORE.md</span>`,
		`<details class="file"><summary><span class="mono">/agent-data/context/INDEX.md</span>`,
		`# Rules &lt;script&gt;alert(1)&lt;/script&gt;`,
		`evil\u202etxt`)
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "\u202e") {
		t.Errorf("hostile text reached the page raw:\n%s", body)
	}
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
		"/app/admin/agents/sess-1/setup?diff=yes":           http.StatusBadRequest,
		"/app/admin/agents/sess-1/setup/sections?diff=yes":  http.StatusBadRequest,
		"/app/admin/agents/sess-1/setup/sections/bogus":     http.StatusBadRequest,
		"/app/admin/agents/nope/setup":                      http.StatusNotFound,
		"/app/admin/agents/nope/setup/sections":             http.StatusNotFound,
		"/app/admin/agents/nope/setup/sections/egress":      http.StatusNotFound,
	} {
		if rec := get(t, h, path); rec.Code != want {
			t.Errorf("%s = %d, want %d: %s", path, rec.Code, want, rec.Body)
		}
	}
	wantAll(t, "gone studio", get(t, h, "/app/admin/agents/nope/setup").Body.String(), "No running studio")
	if body := get(t, h, "/app/admin/agents/nope").Body.String(); strings.Contains(body, ">Setup</a>") {
		t.Error("Setup tab on an agent with no studio")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ -run 'TestStudioSetup|TestSetupShellReadsNoImage' -v`
Expected: FAIL. The studio routes are 404, the agent page has no Setup link, and the context section is one block.

- [ ] **Step 3: The studio routes**

In `internal/jam/adminui/setup_page.go`, extend `registerSetup`'s doc comment with

```go
//	GET /app/admin/agents/{id}/setup[/sections]          — a running studio (?diff=role)
//	GET /app/admin/agents/{id}/setup/sections/{section}  — one section, diffed (the stale badge)
```

and append inside `registerSetup`, after the role routes:

```go
	mux.HandleFunc("GET /app/admin/agents/{id}/setup", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p := setupPage{Title: id + " setup", Studio: true, ID: id}
		if inst, ok := store.GetInstance(jam.ResolveSession(store, id)); !ok || inst.Phase == jam.PhaseGone {
			p.NotFound = true
			renderStatus(w, r, http.StatusNotFound, "agent-setup", p)
			return
		}
		renderStatus(w, r, fillSetupPage(&p, r), "agent-setup", p)
	})
	mux.HandleFunc("GET /app/admin/agents/{id}/setup/sections", func(w http.ResponseWriter, r *http.Request) {
		sections, diff, err := setupQuery(r.URL.Query(), true)
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		id := r.PathValue("id")
		s, err := jam.InstanceSetup(r.Context(), deps, id, sections, diff)
		if err != nil {
			setupFail(w, log, err, "studio "+id, "studio "+id+" not found")
			return
		}
		auditSetup(log, r, s.Subject, sections, diff)
		renderFragment(w, r, "agent-setup", "setup-sections", newSetupView(s, agentURL(id)+"/setup/sections"))
	})
	mux.HandleFunc("GET /app/admin/agents/{id}/setup/sections/{section}", func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("section")
		s, err := jam.InstanceSetup(r.Context(), deps, id, []string{name}, true)
		if err != nil {
			setupFail(w, log, err, "studio "+id, "studio "+id+" not found")
			return
		}
		auditSetup(log, r, s.Subject, []string{name}, true)
		v := newSetupView(s, "")
		sec := v.Sections[0] // InstanceSetup validated name: exactly one section
		sec.DiffNote = v.Note
		renderFragment(w, r, "agent-setup", "setup-section", sec)
	})
```

- [ ] **Step 4: Context files**

In `setup_page.go`, add after `setupSectionView`:

```go
// setupFileView is one context file at its real path; Bytes is its shown size.
type setupFileView struct {
	Path, Body string
	Bytes      int
}
```

Add two fields to `setupSectionView`, after `Body`:

```go
	Head                 string          // context: the file table, budgets and warnings
	Files                []setupFileView // context: each file, raw (none: Body is shown whole)
```

and in `newSectionView`, before `if sec.Diffed {`:

```go
	if sec.Name == jam.SectionContext {
		if head, files, ok := jam.SplitContextBody(sec.Body); ok {
			sv.Head = jam.SetupDisplayText(head)
			for _, f := range files {
				sv.Files = append(sv.Files, setupFileView{Path: jam.SetupDisplayText(f.Path), Body: jam.SetupDisplayText(f.Body), Bytes: len(f.Body)})
			}
		}
	}
```

In `templates/setup.html`'s `setup-section`, replace

```html
    {{if .Body}}<pre class="body">{{.Body}}</pre>{{end}}
```

with

```html
    {{if .Files}}<pre class="body">{{.Head}}</pre>
      {{range .Files}}<details class="file"><summary><span class="mono">{{.Path}}</span> <span class="unset">{{.Bytes}} B</span></summary><pre class="body">{{.Body}}</pre></details>{{end}}
    {{else if .Body}}<pre class="body">{{.Body}}</pre>{{end}}
```

- [ ] **Step 5: The agent page's strip**

In `internal/jam/adminui/agent.go`, add `SetupEnabled bool // the Setup tab is mounted (WithSetupDeps); shown while Running` to `agentDetail` after `CanEdit`. Change `registerAgent` to:

```go
func registerAgent(mux *http.ServeMux, store jam.Store, msgs SquawkReader, sess sessionevents.Store, acc accessDeps, canEdit, setupOn bool) {
	mux.HandleFunc("GET /app/admin/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := buildAgentDetail(store, msgs, sess, acc, r.PathValue("id"), canEdit)
		if !ok {
			d.NotFound = true
			renderStatus(w, r, http.StatusNotFound, "agent", d)
			return
		}
		d.SetupEnabled = setupOn
		render(w, r, "agent", d)
	})
}
```

In `adminui.go`, change the call to `registerAgent(mux, store, msgs, o.sessStore, accessDeps{o.live, o.exposures, o.previewURL}, canEdit, o.setup != nil)`.

In `templates/agent.html`, directly after the line that starts `{{$f := frame}}<div class="crumbs">`, add:

```html
{{if and .Running .SetupEnabled}}<nav class="subtabs" aria-label="Agent views"><a href="{{agentHref .ID ""}}" aria-current="page">Overview</a><a href="{{agentHref .ID "/setup"}}">Setup</a></nav>{{end}}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ -run 'TestStudioSetup|TestSetupShellReadsNoImage|TestStudioPage|TestRoleSetup' -v`
Expected: PASS. Then run `gofmt -l internal/jam/adminui` (no output; `gofmt -w` realigns the struct fields) and `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/...` (PASS).

- [ ] **Step 7: Commit**

```bash
git add internal/jam/adminui/setup_page.go internal/jam/adminui/templates/setup.html internal/jam/adminui/setup_page_test.go internal/jam/adminui/agent.go internal/jam/adminui/templates/agent.html internal/jam/adminui/adminui.go
git commit -m "feat(adminui): studio Setup tab — stale vs role badges, per-section diff, context files"
```

---

### Task 5: Serve wiring and docs

**Files:**
- Modify: `cmd/at-jam/main.go` (the `uiOpts` line near 2692; the `sealed`/`setupDeps` block near 2738-2742)
- Modify: `docs/usage/jam/inspect.md`, `docs/usage/jam/INDEX.md`, `docs/usage/jam/ui-pages.md`, `docs/usage/jam/ui-projects.md`

**Interfaces:**
- Consumes: `adminui.WithSetupDeps` (Task 3), `jam.WithSetupInspect` (slice 1).
- Produces: one `setupDeps` value for the admin API and the UI; the docs.

- [ ] **Step 1: Record the audit baseline**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: `… 723 error(s) …`: 721 at `3ae4a46` plus 2 for this plan file. Note the number.

- [ ] **Step 2: Wire serve**

In `cmd/at-jam/main.go`, cut the block

```go
		sealed, err := assemble.SealedEgressBase()
		if err != nil {
			log.Warn("setup inspect: sealed egress base unreadable; egress shows no sealed rows", "err", err.Error())
		}
		setupDeps := jam.SetupDeps{Store: st, Sup: sup, Images: images, SealedEgress: sealed}
```

from above `admin := jam.NewAdminHandler(…)`, and paste it directly above the `uiOpts := []adminui.Option{…}` line, preceded by the comment

```go
		// Setup inspection: the admin API (WithSetupInspect) and the UI's Setup
		// tabs (WithSetupDeps) render with the same deps.
```

Append `adminui.WithSetupDeps(setupDeps)` to the `uiOpts` literal, after `adminui.WithConfigEnv(cfgEnv)`. `jam.WithSetupInspect(setupDeps, log)` stays as it is.

- [ ] **Step 3: Build and test**

Run: `GOPROXY=https://proxy.golang.org,direct go build ./... && GOPROXY=https://proxy.golang.org,direct go vet ./cmd/at-jam/ ./internal/jam/... && GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ ./internal/jam/...`
Expected: no output from build/vet; tests PASS.

- [ ] **Step 4: Docs — `inspect.md`**

In `docs/usage/jam/inspect.md`, change the frontmatter:
- `summary:` to `Seeing everything a studio's agent is set up with from outside the container — `at-jam role inspect` / `studio inspect [--diff]`, their admin routes and the admin UI's Setup tab, the sections and sources, snapshots, image facts, and redaction with its known limits.`
- `read_when:` append ` — in the CLI, the admin API or the Setup tab of an agent or role page.`, so it ends `…or why a studio differs from its role — in the CLI, the admin API or the Setup tab of an agent or role page.`
- `owns:` append `, the admin UI Setup tab (…/setup on agent and role pages)`

Then add, after the routes list and before `## Sections`:

```markdown
## In the admin UI

A running studio's agent page and a role page carry an **Overview · Setup**
strip. **Setup** (`/app/admin/agents/<id>/setup`,
`/app/admin/projects/<project>/roles/<name>/setup`) shows the sections below,
one collapsible block each, headed with its source, `stale vs role` and the
snapshot time. `context` lists each file at its real path, **raw** — never
rendered as markdown, so nothing in it is hidden or fetched. Text is escaped
as in the CLI (a bidi override shows as `\u202e`). The sections load after the
page opens, so a slow image read never holds up the agent or role page. Chips
filter to one section (`?sections=`). On a studio, a `stale vs role` badge
opens that section's diff against a raise now, and **Diff vs role**
(`?diff=role`) diffs every section. Read-only, behind the same admin auth;
each load is logged `ui setup inspected` (operator, subject, sections).
```

- [ ] **Step 5: Docs — index row and pointers**

In `docs/usage/jam/INDEX.md`, in the `inspect.md` row, replace `` `role inspect`, `studio inspect --diff`. |`` with `` `role inspect`, `studio inspect --diff`, or an agent or role page's Setup tab. |``.

In `docs/usage/jam/ui-pages.md`, at the end of the first paragraph of `## Agent pages` (after "…and **Revoke** when it is enrolled."), add a new line:

```markdown
While it has a studio, its **Setup** tab shows what the agent is set up with — see [inspect.md](inspect.md#in-the-admin-ui).
```

In `docs/usage/jam/ui-projects.md`, after the paragraph that starts `**Request agent** and **Delete**` in `## Role pages`, add:

```markdown
The page's **Setup** tab shows what a studio of the role raised now would get — see [inspect.md](inspect.md#in-the-admin-ui).
```

Set `updated: 2026-10-24` in the frontmatter of the four docs.

- [ ] **Step 6: Audit**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: errors ≤ the Step 1 number (723). `inspect.md` must not be oversize (it stays under 200 lines; check with `wc -l docs/usage/jam/inspect.md`), and neither new link may dangle. `ui-pages.md` was already over budget, so its one added line changes no count.

- [ ] **Step 7: Lint and the full suite**

Run: `just lint && GOPROXY=https://proxy.golang.org,direct go test ./...`
Expected: both pass.

- [ ] **Step 8: Commit**

```bash
git add cmd/at-jam/main.go docs/usage/jam/inspect.md docs/usage/jam/INDEX.md docs/usage/jam/ui-pages.md docs/usage/jam/ui-projects.md
git commit -m "feat(at-jam): wire the UI Setup tab; docs"
```

---

## Self-review

**Spec coverage**

| Spec requirement (§4 slice 2) | Task |
|---|---|
| A Setup tab on the studio page and the role page | 3 (role), 4 (studio) |
| Collapsible sections, with source/stale/at (§1) | 3 (`setup-section`), 4 (snapshot time, stale) |
| Context files shown rendered (markdown) or raw | 1 (`SplitContextBody`), 4 (raw per file); deviation 4 |
| A "stale vs role" badge that opens the diff | 4 (badge, per-section route, `?diff=role`), 2 (shared diff partial) |
| Read-only; html/template escaping; same admin auth | 3/4 (GET only, `SetupDisplayText` + templates; mounted inside the existing `/app/admin` gate) |
| §5 errors: never fails as a whole, 404s, fixed reasons | 3 (`setupFail`, `TestRoleSetupErrors`), 4 (`TestStudioSetupErrors`, role gone) |
| The UI reads through `SetupDeps` wired in serve | 3 (`WithSetupDeps`), 5 (`main.go`) |
| Lazy load | 3 (shell + loader), 4 (`TestSetupShellReadsNoImage`) |
| Docs | 5 |

**Placeholder scan.** Every code step carries its code. The template and file edits name their exact anchors. No step defers a decision.

**Type consistency.**
- `setupPage`, `setupView` and `setupSectionView` are defined in Task 3. Task 4 adds `Head`, `Files` and `setupFileView`, and uses the rest unchanged.
- `newSetupView(s, diffBase)` has the same signature in both tasks.
- `handleRoleDetail` gains `setupOn` in Task 3 and `registerAgent` gains it in Task 4. Each has a single caller in `Handler`.
- The pages are `role-setup`/`agent-setup` and the templates `setup-sections`/`setup-section`, the same names in the Go and in the HTML.
- `jam.SplitContextBody` returns `(string, []ContextBodyFile, bool)` and `jam.SetupDisplayText` is `func(string) string`, both as Task 1 defines them.

**Review Focus check.** Each line names its test in its owning task: Tasks 1 and 4 for hostile text, Task 4 for lazy loading, the deleted role and the project scope, and Tasks 3 and 4 for bad queries.
