---
summary: The Jam manual in manual/ — the user-facing manual in AsciiDoc built with Antora, taking its screenshots (`just manual-shot`), its released, next and goal versions (from the release, main and goal branches), the page shape and conventions attributes, the in-repo UI, marking content internal (team-only pages, areas and passages the customer build drops), and `just manual` / `manual-site` / `manual-check`.
read_when: You are changing anything a Jam user can see (its manual page changes in the same PR), writing or editing a manual page, taking or refreshing a screenshot, marking content team-only, proposing a change on the goal branch, or building the manual.
owns: the manual/ tree, its screenshots (just manual-shot, web/app/src/shots), its build (antora playbooks, manual/ui, just manual and manual-site), the branch-to-version model, the page attributes (page-area, page-id, page-conventions), and marking content internal (manual/extensions, just manual-check)
prereqs: superpowers/specs/2026-11-05-manual-first-operating-model-design.md for why the manual leads the work
tier: leaf
updated: 2026-11-11
---

# The Jam manual (`manual/`)

`manual/` is the **user-facing** manual: what each part of the Jam app does,
for the person using it. `docs/` stays the developer documentation.
It is AsciiDoc, built into a static site with [Antora](https://antora.org).

## Three versions, one diff

The version comes from the **branch**, not the files: `manual/antora.yml` has
no `version`, and the playbooks map branch to version on the content source
(`release` is `released`, `main` is `next`, `goal` is `goal`; any other
branch builds under its own name). Manual files are identical on every
branch, so merges never touch version names.

- **Released** — `manual/` on `release`. What the latest release does.
  The default version; no banner.
- **Next** — `manual/` on `main`. Merged, not released yet. It describes the
  product as it works today and changes **in the same PR** as the code it
  describes. A page that no longer matches the app is a bug.
- **Goal** — `manual/` on the long-lived **`goal`** branch. It describes the
  product we are steering toward. A feature is proposed as a PR **into
  `goal`**, from a branch named `manual/goal-<topic>` (git can't nest
  branches under `goal/`); merging it means "agreed".
- **The work to do** is `git diff origin/release origin/goal -- manual/`;
  `git diff origin/release origin/main -- manual/` is what the next release
  ships. When a feature ships, its page catches up and drops out of the diff.
- After merges to `main`, `main` is merged into `goal` so the diff only shows
  real gaps. When that merge conflicts on a page that has shipped, `goal`
  takes `main`'s page and deletes the page's goal-only files (its mockups
  and prototype attachment), so nothing of it is left in the diff.

## Pages

Pages live in `manual/modules/<area>/pages/`, images in
`manual/modules/<area>/images/`, clickable mockups (HTML) in
`manual/modules/<area>/attachments/`. Each area has a `nav.adoc`, listed in
`antora.yml`. Every page sets:

```asciidoc
= Telling a project's agents what they need to know
:page-area: projects
:page-id: projects/agent-context
:page-conventions: sentence-case
```

`page-id` is what Linear tickets carry as a `page:<id>` label;
`page-conventions` names the anchors in the Conventions area
(`modules/conventions/pages/index.adoc`) the page follows.
Write one clause per line (semantic line breaks) so diffs read clause by clause.
Content for the team only is marked internal (below).
On `main`, images are real screenshots; on `goal`, mockups.

## Screenshots

A PR that changes a page refreshes its screenshots with `just manual-shot`
(`scripts/manual-shot.sh` → `web/app/scripts/shot.mjs`):

```sh
just manual-shot 'admin/config/projects/acme?branch=jam/docs' \
  --selector 'main > .mantine-Stack-root' --width 1000 \
  --out manual/modules/projects/images/project-page.png
just manual-shot 'admin/config/projects/acme?branch=jam/docs' \
  --selector '.mantine-Card-root:has(.jam-ctx-rows)' --width 1200 \
  --click '[data-action="edit"]' --click '[data-leaf="manual.md"] .toggle' \
  --out manual/modules/projects/images/agent-context-edit.png
```

- It serves the app with Vite on mock data (`web/app/vite.shot.config.ts`):
  the API answers from `web/app/src/shots/mocks.ts`, a map of
  `"METHOD /path"` (query optional) to JSON built from the tests'
  `fixtures.ts`. A request with no entry answers 404, is logged as
  `mock-api: no mock for …` and fails the shot (non-zero exit, no PNG
  written): add the entry. `?branch=<name>` in the route
  opens a config branch, so edit controls show.
- It shoots with the kit's headless Chrome
  (`/opt/chrome-headless-shell/chrome-headless-shell`; `JAM_BROWSER`
  overrides), which resolves no host but loopback.
- Flags: `--out` (from the repo root), `--selector` (crop to the first match,
  plus `--pad`, default 8 CSS px; without it, the whole page), `--width`
  (default 1280) and `--height` (800; grown to the page), `--scale` (device
  pixels per CSS pixel, default 2), `--click <css>` (click the first match
  once the view is ready, before the shot; repeat it to click in order — a
  card's Edit button, a row's toggle).
- Output is deterministic: light scheme, no animations or caret, fonts
  (the bundled IBM Plex) loaded, the network quiet. Rerunning on unchanged
  code and mocks, with the same Chrome, gives the same bytes.
- To refresh, rerun the command that made the image; record a new image's
  command in the PR.

## Marking content internal

One site attribute, `internal`, decides: the team playbooks set it (the team
manual shows internal content, marked); the customer build leaves it unset and
drops internal content. The customer build hard-unsets it, so a component's
`antora.yml` or a page header setting `internal` changes nothing. Any granularity:

| What | How | Customer build |
|---|---|---|
| A block or section | `[.internal]` on it | removed |
| A phrase | `[.internal]#phrase#` (or `*…*`, `_…_`, with an id too), titles included | removed |
| A nav entry | `* [.internal]#entry#` as the whole entry in a `nav.adoc` | removed |
| A passage | `ifdef::internal[]` … `endif::[]` | removed |
| A sentence or phrase | single-line `ifdef::internal[text]` | removed |
| A whole page | `:page-internal:` in the page header | page, its nav entry and aliases dropped |
| A whole area | the module under `ext.internal.modules` in `manual/antora.yml` (the one list; every playbook reads it) | every page in the module, and its nav menu, dropped |
| An image or attachment | used only from internal content | not published (unreferenced files are dropped) |

- **Prefer `[.internal]`**, because it is marked: the team build shows it in a
  dashed "Team" box, or with a dotted underline inline. `ifdef::internal[]`
  content shows unmarked. Internal pages, modules and nav entries get a
  "Team only" badge in the nav.
- **Leaks fail the customer build**, each an error naming the page: a kept page
  that links to a dropped page, or to an id on itself that no element has (an
  `<<id>>` to a dropped section or block, whose title the link text would
  carry); an internal site or component start page; a kept section with
  `[.internal]#…#` in its title and no explicit id (its generated id is a slug
  of the whole title: give it one, `[#some-id]`); and, as a backstop, any
  published page still carrying the class `internal` (for example
  `[.internal]#…#` inside a nav entry's text: mark the whole entry).
- **How**: `manual/extensions/internal.js` (Antora extension: pages, modules,
  phrases, nav, files, the leak check) and `manual/extensions/internal-blocks.js`
  (Asciidoctor tree processor: `[.internal]` blocks). A playbook registers both:
  `antora.extensions: [{require: ./extensions/internal.js}]` and
  `asciidoc.extensions: [./extensions/internal-blocks.js]`; `internal.js` fails
  the build if the second is missing, or if the playbook lists `modules:` itself.
- **Tests**: `just manual-check` builds a fixture
  (`manual/extensions/test/fixture/`) as customer and as team.

## Building

- `just manual` builds your working tree (uncommitted edits included) into
  `manual/build/site/` — one version, the one checked out (named from the
  branch, so `main` builds as *Next*).
- `just manual-site` builds the **team site**: `release`, `main` and `goal`
  together, with a *Released* / *Next* / *Goal* switcher, opening on Released:
  your local branch of each name if you have one (the checked-out one from the
  working tree), else origin's. Fetch first. It adds the process book when you
  have a clone of jam-config ([manual-books.md](manual-books.md)), else warns and builds
  without it.
- The **team site** is the same build, deployed to Cloudflare Pages at
  <https://aethons-tools-jam.pages.dev> (behind Cloudflare Access) by
  `.github/workflows/manual-site.yml` on every push to `release`, `main` or
  `goal` (of cove; a push to jam-config redeploys nothing until the next one,
  or a manual run). It needs the secret `CLOUDFLARE_API_TOKEN` and the repository
  variable `CLOUDFLARE_ACCOUNT_ID`, and `JAM_CONFIG_DEPLOY_KEY` for the process book; the deploy runs `wrangler`, pinned in the
  workflow (not in `manual/package.json`, so manual builds never install it).
  The build always uses `main`'s playbook, UI and extensions, whichever branch
  pushed. A branch that does not exist yet is skipped.
  **The deployed site includes internal content and is safe only because
  Cloudflare Access gates it.** Never remove the Access policy (it must cover
  the production host `aethons-tools-jam.pages.dev`, not just preview
  deployments), and never deploy the customer playbook there.
- The **customer manual** (Released only, without `internal`) is built by
  `scripts/manual.sh` from `manual/antora-playbook.jam.yml` into
  `internal/jam/uiassets/manual/` (git-ignored), which `jam` embeds and serves
  at `/app/manual/` ([web-app.md](web-app.md)). The playbook sets no `internal`
  attribute, so the extensions drop internal content and fail the build on a
  leak, and warnings fail it too (`failure_level: warn`). `just build` and the
  release run it; it reads your working tree's `HEAD` but names the version
  `released`.

Both need Node/npm (Antora is pinned in `manual/package.json`). The site's look
is `manual/ui/` — layouts, two helpers and a stylesheet — not Antora's default
UI, which would be downloaded from gitlab.com at build time.

The team site's books (the process book included) and search: [manual-books.md](manual-books.md).
