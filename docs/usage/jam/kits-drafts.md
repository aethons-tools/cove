---
summary: A kit's draft on a branch of the config repo — one mutable tree per kit, opened from a version, with its problems listed; the ETag rule; publishing it, which commits the kit's files to the branch. The admin console's Draft section is in kits-drafts-ui.md.
read_when: You want to change a kit's files (Dockerfile, scripts, notes, kit.yml) in the admin console, or a draft save or publish was refused (problems, no changes, a stale draft).
owns: kit drafts — opening one, the draft's status and problems, the ETag rule, publishing a draft (a commit of the kit's files), where a draft lives
prereqs: config-branches.md (branches and saves), kits.md, kits-directory.md
tier: leaf
updated: 2026-11-01
---

# Kit drafts

A kit's files change only by a commit to the config repo. In the admin
console that is a **draft** on a branch ([config-branches.md](config-branches.md)):
you edit the kit's files in the draft, then **publish** it, which commits them
to the branch as `kits/<name>/…`. Merge the branch and the Jam builds the new
version. By hand, edit `kits/<name>/` in a checkout and commit
([config-git.md](config-git.md#layout)). The console's Draft section:
[kits-drafts-ui.md](kits-drafts-ui.md).

## The draft

A kit has at most one draft on a branch: a mutable copy of one version's files
(its tree, see [kits-directory.md](kits-directory.md#stored-as-trees)),
opened from *current* or a version you choose (its **base**). You change
files in the draft, then publish it, or discard it.

- Every save is checked like a kit in the repo: the paths, modes and sizes in
  [limits](kits-directory.md#limits). The draft's own
  [`.dockerignore`](kits-directory.md#dockerignore) applies too: a file it
  excludes is refused (`draft <kit>: <path>: excluded by .dockerignore`), and
  saving a `.dockerignore` drops the files it now excludes.
- `kit.yml` may be missing or invalid while you edit. The draft lists its
  **problems**: `kit <name>: kit.yml missing at the root`, a parse or
  validation error, a missing parent, a `base.context…` field. A draft with
  problems cannot be published.
- A draft is working state in the branch's workspace, not in the repo: a draft
  save makes no commit, and the draft is lost when the workspace is dropped
  (the branch moved, a refused save). `kit <name> already has a draft`
  (409) refuses a second one.
- Another version may land while a draft is open: the draft then shows
  `base: 1, current: 2`. Publishing still makes the draft's tree the kit's
  files, without the other version's changes (no merge).

## Concurrency

The draft's **ETag** is its tree hash. Every write sends back the ETag it
read: a stale one is refused 412 `draft <kit> changed since you loaded it
(reload)`. Nothing is merged: reload the draft and make the edit again.

## Publish

Publish checks the draft as a kit in the repo is checked, makes its tree the
kit's next version on the branch, commits the kit's files, and closes the
draft.

- No change from *current*: 409 `draft <kit> has no changes` (draft kept);
  problems: 400 `draft <kit> has problems: …`.
- Kits that follow this one build on the new version once the branch is
  merged. **Pin followers** does not work on a branch: the repo names a parent
  without a version ([config-git.md](config-git.md#layout)), so the publish fails. Leave it off.
- The commit is the branch save's ([config-branches.md](config-branches.md#saving)):
  one commit, authored by the operator.

There is no test build or draft studio: a kit's image is built when the
merged version is raised.
