# Release branch and published manual Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Releases come from merging `main` into `release`. The team manual (three versions, internal content shown) deploys to Cloudflare Pages. The customer manual (Released only, internal content removed) is embedded in `jam` at `/app/manual/`.

**Architecture:** The manual's version comes from the git branch (Antora content-source `version` map), so `manual/` is identical on every branch. An Antora extension plus an Asciidoctor tree processor drop internal content when the `internal` attribute is unset. `release.yml` publishes only on pushes to `release`; `main` builds and validates like a PR. Two new workflows keep the release PR current and deploy the team site.

**Tech Stack:** Antora 3.2.2 (Node), Asciidoctor.js extensions, Go (`net/http`, `embed`), GitHub Actions, dorny/paths-filter, wrangler (Cloudflare Pages).

**Spec:** `docs/superpowers/specs/2026-11-06-release-branch-and-manual-site-design.md`

## Global Constraints

- Branch → version: `release` → `released` (default, no banner), `main` → `next` (banner "Merged, not released yet."), `goal` → `goal` (banner "How the product should work, not necessarily how it works today.").
- `manual/antora.yml` carries **no** `version:` key on any branch.
- Cloudflare: Pages project `aethons-tools-jam`, `https://aethons-tools-jam.pages.dev`; deploy with `wrangler pages deploy … --project-name aethons-tools-jam --branch main`; secret `CLOUDFLARE_API_TOKEN`, variable `CLOUDFLARE_ACCOUNT_ID`.
- Customer manual served at `/app/manual/` to any signed-in principal (member **or** admin); signed-out visitors get the usual sign-in redirect.
- Customer build leaves `internal` unset and fails on any warning (`--log-failure-level=warn`); team builds set `internal: ''`.
- A push to `main` publishes nothing: no image push, no GitHub release, no tag.
- Only the user merges into `release` (AGENTS.md rule); agents merge to `main` when `gate` is green and review passed.
- Pin every new npm tool in `manual/package.json` (exact versions) and install with `scripts/npm-ci.sh`.

## Review Focus

1. **Internal content leaking into the customer build.** Inline `[.internal]#…#`, an internal page's nav entry, an image only internal content uses, or a public page linking to an internal page must never reach `/app/manual/`. Task 2's fixture test covers each one.
2. **`main` still publishing.** A forgotten `event_name == 'push'` guard would release from `main`. Task 4's step greps the workflow for every publish step's guard.
3. **jam built without npm.** `go build` and `APP_OPTIONAL=1` builds must still work; `/app/manual/` then answers "not built". Task 3 tests the unbuilt FS.
4. **Unauthenticated `/app/manual/`.** It must be gated like `/app/user/`, but admit admin-only principals. Task 3's table test covers both.
5. **Release PR with nothing to release** (`release` already equals `main`). The workflow must not open an empty PR, and must close a stale one. Task 5's script test covers it.

---

### Task 1: Versions from the branch, three-version team build

**Files:**
- Modify: `manual/antora.yml` (drop `version`, `display_version`; update the comment)
- Modify: `manual/antora-playbook.yml` (team: branches `[release, main, goal]`, version map, `start_page: released@jam:ROOT:index.adoc`)
- Modify: `manual/antora-playbook.local.yml` (HEAD, the same version map)
- Modify: `manual/ui/layouts/default.hbs`, `manual/ui/css/site.css` (banners per version, capitalized version names)
- Modify: `manual/modules/ROOT/pages/index.adoc` (three versions: Released, Next, Goal)
- Modify: `docs/manual.md`, `justfile` (`manual-site` comment)

**Interfaces:**
- Produces: a content-source `version` map `{release: released, main: next, goal: goal}` (unmatched refs fall back to the branch name), used in every multi-branch playbook. Banner copy lives in `default.hbs` only.

- [ ] **Step 1:** In a scratch clone (`git clone -q . /tmp/mv && cd /tmp/mv`), create local branches `release`, `main` and `goal` from HEAD. Build the team playbook. Expect it to FAIL today (`antora.yml` still hard-codes `version`, so all three collide).
- [ ] **Step 2:** Make the changes above. The version map goes on the content source as `version:` (Antora reads it when `antora.yml` has none). Banners: `next` → "Merged, not released yet.", `goal` → the goal copy above, `released` → none. The version switcher and banner show the version name capitalized (CSS `text-transform: capitalize`).
- [ ] **Step 3:** Rebuild in the scratch clone. Expected: `build/site/jam/{released,next,goal}/` exist, `build/site/index.html` redirects to `jam/released/index.html`, a `next` page contains "Merged, not released yet.", and a `released` page has no banner.
- [ ] **Step 4:** `just manual` in the repo builds (version = your branch name, or `next` on `main`).
- [ ] **Step 5:** Update `docs/manual.md`: versions by branch, three versions, the team vs. customer builds (customer = Task 3), and bump `updated`. Commit: `manual: versions come from the branch (released, next, goal)`.

### Task 2: Marking content internal

**Files:**
- Create: `manual/extensions/internal.js` (Antora extension)
- Create: `manual/extensions/internal-blocks.js` (Asciidoctor tree processor)
- Create: `manual/extensions/test/internal.test.js`, `manual/extensions/test/fixture/` (a tiny component plus two playbooks)
- Modify: `manual/package.json` (`"test": "node --test extensions/test/"`), both team playbooks (register both extensions, `internal: ''`), `manual/ui/css/site.css` (team marking), `manual/ui/partials/nav-tree.hbs` (a "Team only" badge)
- Modify: `docs/manual.md` (a "Marking content internal" section), `justfile` (`manual-check: cd manual && ../scripts/npm-ci.sh && npm test`)

**Interfaces:**
- Produces: the authoring syntax that later tasks and authors use:
  - `[.internal]` on any block or section, and `[.internal]#phrase#` inline
  - `ifdef::internal[]` (still works)
  - `:page-internal:` in a page header
  - playbook `antora.extensions: [{require: ./extensions/internal.js, modules: [<module>…]}]` for whole modules
- `internal.js` exports `register()` for Antora. When the site's `internal` attribute is unset, it:
  - removes pages with `page-internal`, and every page in a listed module
  - removes `<span class="internal">…</span>` from page HTML
  - prunes nav items whose page was removed
  - removes images and attachments no remaining page references
  - logs an error naming the page when a kept page links to a removed page's URL

  When `internal` is set, it removes nothing. Internal pages and modules get a nav flag that the UI renders as a "Team only" badge.
- `internal-blocks.js` exports `register(registry)`. Its tree processor removes every block or section with role `internal` when the document lacks the `internal` attribute.

- [ ] **Step 1: Write the failing tests** in `internal.test.js` (`node:test`). Each builds the fixture with Antora's `generateSite` from a temp output dir.
  - `customer build drops internal content`: the output lacks
    - `internal-page.html`
    - the text `SECRET-BLOCK` (an `[.internal]` paragraph)
    - `SECRET-INLINE` (`[.internal]#…#`)
    - `SECRET-IFDEF` (`ifdef::internal[]`)
    - `SECRET-MODULE` (a page in the module listed as internal)
    - the nav link to `internal-page.html`
    - `_images/internal-only.png`

    It keeps `public.html` and `_images/shared.png`.
  - `customer build fails on a leak`: the fixture variant where `public.html` xrefs `internal-page.adoc` makes `generateSite` reject with an error mentioning `internal-page`.
  - `team build keeps everything`: all the `SECRET-*` texts are present, and `internal-page.html` exists with the nav badge class `team-only`.
- [ ] **Step 2:** `cd manual && npm test`. Expected: FAIL (the extensions don't exist).
- [ ] **Step 3:** Implement both extensions. Antora events: `contentClassified` to register the Asciidoctor extension through `asciidoc.extensions`, or put it in the playbook's `asciidoc.extensions`; then `documentsConverted` for page removal, inline spans and the leak check; `navigationBuilt` for nav pruning; `beforePublish` for unreferenced images and attachments.
- [ ] **Step 4:** `npm test`. Expected: PASS. Then `just manual` still builds.
- [ ] **Step 5:** Team CSS: `.internal` blocks get a dashed border and a "Team" label, inline `.internal` gets a dotted underline, and `.team-only` is a nav badge. Document the syntax table from the spec §5 in `docs/manual.md`. Commit: `manual: mark any content internal; the customer build drops it`.

### Task 3: The customer manual embedded in jam

**Files:**
- Create: `manual/antora-playbook.jam.yml` (HEAD only, `version: released`, no `internal`, both extensions, `runtime.log.failure_level: warn`, output `../internal/jam/uiassets/manual.tmp`)
- Create: `scripts/manual.sh` (modelled on `scripts/app.sh`: npm check honoring `APP_OPTIONAL=1`, build into `manual.tmp`, keep `.keep`, swap into `internal/jam/uiassets/manual/`)
- Create: `internal/jam/uiassets/manual/.keep`; modify `.gitignore` (same four lines as `app/`)
- Modify: `internal/jam/uiassets/uiassets.go`: `//go:embed all:manual`, `func ManualFS() fs.FS`, `func ManualBuilt() bool` (true when `index.html` exists)
- Create: `internal/jam/appui/manual.go`: `func manualHandler(m fs.FS) http.Handler` serves `m` under `/app/manual/`. When not built, it answers 404 "The manual isn't built into this Jam." CSP header: `default-src 'self'; script-src 'none'; object-src 'none'`
- Modify: `internal/jam/appui/mux.go`: `Deps.Manual fs.FS` (nil = `uiassets.ManualFS()`), mount `GET /app/manual/` as `d.Gate.Wrap(RequireSignedIn(manualHandler(…)))`
- Create: `RequireSignedIn(next http.Handler) http.Handler` in `internal/jam/appui/gate.go`. It admits `p.User != nil || p.Admin`; otherwise `forbidNeither`.
- Modify: `scripts/build.sh` (run `scripts/manual.sh` after `scripts/app.sh`), `.goreleaser.yaml` (a before-hook `bash scripts/manual.sh` after app.sh), `.github/workflows/release.yml` (`go` filter gains `'manual/**'` and `'scripts/manual.sh'`)
- Modify: `docs/web-app.md` (`/app/manual/`, `scripts/manual.sh`), `docs/manual.md` (the customer build)
- Test: `internal/jam/appui/mux_test.go`, `internal/jam/appui/manual_test.go`

**Interfaces:**
- Consumes: Task 1's version naming and Task 2's extensions. The jam playbook names `released` explicitly.
- Produces: `uiassets.ManualFS()`, `uiassets.ManualBuilt()`, `appui.Deps.Manual`, `appui.RequireSignedIn`.

- [ ] **Step 1: Write failing Go tests**
  - Add rows to `TestAreasGated`:
    - `{"manual, member", "GET", "/app/manual/jam/released/index.html", "alice", "", "", 200, "manual page"}`
    - `{"manual, admin only", …, "stranger", "ok", …, 200, "manual page"}`
    - `{"manual, signed out", "GET", "/app/manual/", "", "", "", 302, "return_to=%2Fapp%2Fmanual%2F"}`

    `testMux` sets `Deps.Manual` to an `fstest.MapFS` holding `jam/released/index.html` = `manual page` and `index.html`.
  - `TestManualNotBuilt`: an empty MapFS gives a 404 whose body contains "isn't built".
  - `TestManualCSP`: a 200 response carries the CSP above.
- [ ] **Step 2:** `go test ./internal/jam/appui/`. Expected: FAIL (`Deps.Manual` undefined).
- [ ] **Step 3:** Implement the uiassets, appui and gate changes.
- [ ] **Step 4:** `go test ./internal/jam/...`. Expected: PASS. Then `go build ./cmd/jam` works with only `.keep` present.
- [ ] **Step 5:** `scripts/manual.sh`, then `ls internal/jam/uiassets/manual/jam/released/index.html` exists, and `grep -r SECRET internal/jam/uiassets/manual` finds nothing (the real manual has none). `APP_OPTIONAL=1 PATH=/usr/bin:/bin scripts/manual.sh` without npm skips with a warning.
- [ ] **Step 6:** Update the docs, then `just test`. Commit: `jam: serve the customer manual at /app/manual/`.

### Task 4: Release pipeline publishes only from `release`

**Files:**
- Create: `.github/release-filters.yml` (the `base`, `studio_image` and `go` filters moved verbatim from `release.yml`, plus Task 3's additions)
- Modify: `.github/workflows/release.yml`:
  - `on.push.branches: [main, release]`
  - filters from `.github/release-filters.yml`
  - every `github.event_name == 'push'` guard → `github.ref == 'refs/heads/release'`
  - every `github.event_name == 'pull_request'` guard → `github.ref != 'refs/heads/release'`
  - concurrency `cancel-in-progress: ${{ github.ref != 'refs/heads/release' }}`
  - delete the `recut-bases` job and its `needs` entry
  - update the header comment
- Modify: `.github/workflows/gate.yml`: `pull_request.branches: [main, release]`, `push.branches: [main, release]`, header comment
- Modify: `docs/DEVELOPMENT.md` (the release-pipeline section: branches table, who merges, the repo settings including "Allow merge commits" and "Allow GitHub Actions to create pull requests"), `docs/OVERVIEW.md` ("on every push to `main`" → "on every merge into `release`"), `AGENTS.md` (the merge rule from the Global Constraints)

**Interfaces:**
- Produces: `.github/release-filters.yml`, which Task 5's workflow also reads.

- [ ] **Step 1:** Make the edits.
- [ ] **Step 2: Verify the guards.** Run `grep -n "event_name" .github/workflows/release.yml`; expected: no matches. Run `grep -n "refs/heads/release" .github/workflows/release.yml`; expected: one line per publish or push step (the image push, publish-images, Build binaries main, Publish Jam image, Smoke arm64, `gh release create`) plus the concurrency line.
- [ ] **Step 3:** `actionlint` if it's on PATH (otherwise `python3 -c 'import yaml,sys;[yaml.safe_load(open(f)) for f in sys.argv[1:]]' .github/workflows/*.yml .github/release-filters.yml`). Expected: no errors.
- [ ] **Step 4:** Commit: `ci: publish releases only from the release branch`.

### Task 5: The release PR

**Files:**
- Create: `scripts/release/pr-body.sh <base> <head> <legs-json>`: prints the PR body markdown to stdout, or nothing when `<head>` has no commits beyond `<base>`
- Create: `scripts/release/pr-body.test.sh` (hermetic: a temp git repo)
- Create: `.github/workflows/release-pr.yml`
- Modify: `docs/DEVELOPMENT.md` (the release PR)

**Interfaces:**
- Consumes: `.github/release-filters.yml`.
- Produces: the PR titled `Release <N>`, where `N = $(git rev-list --count origin/release) + $(git rev-list --count origin/release..origin/main) + 1` (the count after the merge commit).

- [ ] **Step 1: Write the failing test** `pr-body.test.sh`. It builds a repo with `release` and `main` where `main` adds a commit `feat: x (#12)` that edits `manual/modules/projects/pages/agent-context.adoc` and a commit touching `internal/foo.go`. Assert the output:
  - contains `## What changes for users`
  - contains the page link `https://aethons-tools-jam.pages.dev/jam/next/projects/agent-context.html`
  - contains `## What it rebuilds` followed by `binaries` (from legs-json `{"base":"false","studio_image":"false","binaries":"true"}`)
  - contains `## Merged` with `#12`

  With `main == release`, output is empty. With no `manual/` changes, the users section reads "No user-visible changes."
- [ ] **Step 2:** `bash scripts/release/pr-body.test.sh`. Expected: FAIL (no script).
- [ ] **Step 3:** Implement `pr-body.sh`. Page URL: `manual/modules/<m>/pages/<p>.adoc` → `jam/next/<m>/<p>.html` (module `ROOT` → `jam/next/<p>.html`). Include deleted pages as plain text, not links.
- [ ] **Step 4:** Run the test again. Expected: PASS.
- [ ] **Step 5:** `release-pr.yml`:
  - on push to `main` and `release`, plus `workflow_dispatch`
  - permissions: `contents: read`, `pull-requests: write`
  - checkout with `fetch-depth: 0`
  - dorny/paths-filter with `filters: .github/release-filters.yml`, `base: release`, `ref: main`, and the legs fan-out copied from `release.yml`
  - `pr-body.sh origin/release origin/main '<legs>'`
  - then `gh pr list --base release --head main --state open`
  - empty body → close any open one; otherwise `gh pr edit` or `gh pr create --base release --head main --title "Release $N" --body-file`

  Concurrency group `release-pr`, cancel in progress.
- [ ] **Step 6:** Document it in `docs/DEVELOPMENT.md`, including that the gate check on the PR's head is the one from `main`'s push. Commit: `ci: keep a release PR (main → release) open with what changes for users`.

### Task 6: Deploy the team manual to Cloudflare

**Files:**
- Create: `.github/workflows/manual-site.yml`
- Modify: `manual/package.json` (add an exact `wrangler` devDependency, refresh the lock)
- Modify: `docs/manual.md` (where the team site is and what triggers it)

- [ ] **Step 1:** Workflow:
  - on push to `release`, `main` and `goal`, plus `workflow_dispatch`
  - concurrency group `manual-site`, `cancel-in-progress: true`
  - checkout with `fetch-depth: 0`, then `git fetch origin release main goal`; a branch that doesn't exist yet is skipped by Antora, so the first runs still build
  - `actions/setup-node` (Node 22)
  - `cd manual && ../scripts/npm-ci.sh && npx antora antora-playbook.yml`
  - `npx wrangler pages deploy build/site --project-name aethons-tools-jam --branch main` with `CLOUDFLARE_API_TOKEN: ${{ secrets.CLOUDFLARE_API_TOKEN }}` and `CLOUDFLARE_ACCOUNT_ID: ${{ vars.CLOUDFLARE_ACCOUNT_ID }}`

  Permissions: `contents: read`.
- [ ] **Step 2:** Locally, `cd manual && npx antora antora-playbook.yml` with only `main` present still builds (the missing branches are skipped, or a clear error). If Antora errors on a missing branch, set the playbook branches to patterns (`[release, main, goal]` already match nothing quietly; confirm).
- [ ] **Step 3:** YAML-lint as in Task 4. Commit: `ci: deploy the team manual to Cloudflare Pages`.

### After merge (operator steps, not code)

- Merge `main` into `goal`, resolving `manual/antora.yml` to `main`'s version (no `version:`).
- Watch the first `manual-site` run and `release-pr` run. Check the site opens behind Access at `https://aethons-tools-jam.pages.dev`, and that the release PR exists.
