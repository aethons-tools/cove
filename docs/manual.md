---
summary: The Jam manual in manual/ — the user-facing manual in AsciiDoc built with Antora, its released, next and goal versions (from the release, main and goal branches), the page shape and conventions attributes, the in-repo UI, and `just manual` / `manual-site`.
read_when: You are changing anything a Jam user can see (its manual page changes in the same PR), writing or editing a manual page, proposing a change on the goal branch, or building the manual.
owns: the manual/ tree, its build (antora playbooks, manual/ui, just manual and manual-site), the branch-to-version model, and the page attributes (page-area, page-id, page-conventions)
prereqs: superpowers/specs/2026-11-05-manual-first-operating-model-design.md for why the manual leads the work
tier: leaf
updated: 2026-11-06
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
Notes for the team only go in `ifdef::internal[]` blocks: the team playbooks set
`internal` (the team manual); a build for users leaves it unset.
On `main`, images are real screenshots; on `goal`, mockups.

## Building

- `just manual` builds your working tree (uncommitted edits included) into
  `manual/build/site/` — one version, the one checked out (named from the
  branch, so `main` builds as *Next*).
- `just manual-site` builds the **team manual**: `release`, `main` and `goal`
  together, with a *Released* / *Next* / *Goal* switcher, opening on Released:
  your local branch of each name if you have one (the checked-out one from the
  working tree), else origin's. Fetch first.
- The **customer manual** (Released only, without `internal`, embedded in
  `jam`) is a later step; see the release-branch design spec.

Both need Node/npm (Antora is pinned in `manual/package.json`). The site's look
is `manual/ui/` — layouts, one helper and a stylesheet — not Antora's default
UI, which would be downloaded from gitlab.com at build time.
