---
summary: The Jam manual in manual/ — the user-facing manual in AsciiDoc built with Antora, its as-is (main) and goal (goal branch) versions, the page shape and conventions attributes, the in-repo UI, and `just manual` / `manual-site`.
read_when: You are changing anything a Jam user can see (its manual page changes in the same PR), writing or editing a manual page, proposing a change on the goal branch, or building the manual.
owns: the manual/ tree, its build (antora playbooks, manual/ui, just manual and manual-site), the as-is/goal branch model, and the page attributes (page-area, page-id, page-conventions)
prereqs: superpowers/specs/2026-11-05-manual-first-operating-model-design.md for why the manual leads the work
tier: leaf
updated: 2026-11-06
---

# The Jam manual (`manual/`)

`manual/` is the **user-facing** manual: what each part of the Jam app does,
for the person using it. `docs/` stays the developer documentation.
It is AsciiDoc, built into a static site with [Antora](https://antora.org).

## Two versions, one diff

- **As is** — `manual/` on `main`. It describes the product as it works today
  and changes **in the same PR** as the code it describes.
  A page that no longer matches the app is a bug.
- **Goal** — `manual/` on the long-lived **`goal`** branch, with
  `version: goal` in `manual/antora.yml`. It describes the product we are
  steering toward. A feature is proposed as a PR **into `goal`**; merging it
  means "agreed".
- **The work to do** is `git diff origin/main origin/goal -- manual/`.
  When a feature ships, its as-is page catches up and drops out of the diff.
- After merges to `main`, `main` is merged into `goal` so the diff only shows
  real gaps.

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
On `main`, images are real screenshots; on `goal`, mockups.

## Building

- `just manual` builds your working tree (uncommitted edits included) into
  `manual/build/site/` — one version, the one checked out.
- `just manual-site` builds `origin/main` and `origin/goal` together, with an
  *As is* / *Goal* switcher. Run `git fetch` first.

Both need Node/npm (Antora is pinned in `manual/package.json`). The site's look
is `manual/ui/` — layouts, one helper and a stylesheet — not Antora's default
UI, which would be downloaded from gitlab.com at build time.
