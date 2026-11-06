# Release branch and the published manual — design

**Status:** draft for the user's review, 2026-11-06. Decisions the user made are marked *(user)*; the rest are proposals.
**Context:** today every push to `main` publishes a release (`.github/workflows/release.yml`, [the release pipeline](../../DEVELOPMENT.md#ci-the-release-pipeline)), and the user's approval of each merge was a habit, not a rule (`main`'s ruleset requires the `gate` check and no approvals). Under the manual-first model ([design](2026-11-05-manual-first-operating-model-design.md)), the user wants to approve **releases**, not merges, and to publish the manual, all of its versions, as a site.

## Intent

- The user approves what ships, once per release, by looking at what changes for users. *(user)*
- Agents merge to `main` on their own once checks and review pass. *(user)*
- A merge from `main` into a `release` branch starts the release. *(user)*
- The manual is published with all three versions, opening on the released one. *(user)*
- Any manual content can be marked internal, and the published site leaves it out. *(user)*

## Non-goals

- Changing what a release builds, how it picks legs from the diff, or the `<N>-<MMDD>` versioning.
- Hiding anything that is already public in the repo. The repo is public; "internal" means "not on the site", not "secret".
- Deploying Jam hosts. A release publishes artifacts as today; hosts update with `jam update`.

## Anti-goals

- `release` drifting from `main`: commits that exist only on `release`, beyond merge commits.
- A published page that silently describes unreleased behavior.
- Internal notes leaking onto the public site because someone forgot a step.

## 1. Branches

| Branch | Who merges | How | A push does |
|---|---|---|---|
| `main` | agents, when `gate` is green and review passed | squash (unchanged) | build + smoke every touched leg, **publish nothing** |
| `release` | **the user only** *(user)* | **merge commit** from `main`; never squash, never direct pushes | today's publish: images, binaries, GitHub release `<N>-<MMDD>` |
| `goal` | the user (goal proposals) | squash or merge | nothing but the site rebuild |

`release` takes merge commits so its history contains `main`'s commits: the next release PR has no conflicts, and the diff since the last release is exact. `N` (`git rev-list --count HEAD`) is counted on `release`, so it stays monotonic. It jumps past `main`'s count by the merge commits, which is fine because `N` only has to increase.

**Enforcement.** Agents push with the user's credential, so GitHub can't tell them apart. "Only the user merges into `release`" is a rule in `AGENTS.md` and in the role contexts, not a GitHub permission. GitHub does enforce the rest:

- Repo setting: allow merge commits (squash stays allowed).
- `main` ruleset: unchanged (squash only, `gate`, up to date).
- New `release` ruleset: PR required, allowed merge methods = **merge** only, no deletion, no force-push, required check `gate`.

These are admin changes; the user (or an admin) applies them. The implementation records the exact settings in `docs/DEVELOPMENT.md`.

## 2. The release PR

One PR from `main` into `release` is always open while they differ, titled `Release <N>` (the `N` it would produce). A small workflow (`release-pr.yml`, on push to `main` and `release`) opens or refreshes it. Its description, regenerated each time:

1. **What changes for users:** `git diff release main -- manual/`, summarized per page, with links to those pages on the **Next** version of the site. This is what the user approves.
2. **What it rebuilds:** the legs `release.yml`'s path filter would pick (base, studio image, binaries/Jam image).
3. **Merged PRs** since the last release (titles + links).

The user approves by merging it. Nothing else releases.

## 3. `release.yml` changes

- Triggers: `pull_request` (unchanged), `push` to `main` **and** `release`.
- Every step guarded by `github.event_name == 'push'` becomes guarded by `github.ref == 'refs/heads/release'`. A push to `main` then does exactly what a PR does: build and smoke, no publish.
- The `changes` job's path filter on a `release` push diffs the previous `release` tip against the new one, so a release rebuilds every leg touched since the last release.
- Concurrency stays one group per ref, serialized for `release`.
- `gate.yml` also runs on PRs into `release` (it is that ruleset's required check).
- The one-off re-cut step marked "removable once the re-cut has run on main" is removed in the same change.

## 4. The published manual

**Versions come from the branch, not the files.** Each branch's `manual/antora.yml` stops hard-coding `version:`. The playbook maps branch to version on the content source, so manual files are identical on every branch and merges never touch version names. The `goal` branch's `version: goal` commit is reverted.

| Branch | Version | Shown as | Banner |
|---|---|---|---|
| `release` | `released` | **Released** (default) | none |
| `main` | `next` | **Next** | "Merged, not released yet." |
| `goal` | `goal` | **Goal** | "How the product should work, not necessarily how it works today." |

- The site opens on **Released**; the version switcher shows all three.
- `manual-site.yml` builds all three on any push to `release`, `main` or `goal`, from one checkout with full history, and deploys to **GitHub Pages**. Deploys are serialized (one concurrency group, newest wins).
- The public build leaves `internal` unset (§5). `just manual` / `manual-site` keep building the **team manual** (internal shown) for previews.
- Diffs the site can show later: `goal` vs `release` is all the work left; `main` vs `release` is what the next release ships.

## 5. Marking content internal *(user: "any content, anywhere")*

One attribute, `internal`, set only in team builds. Every granularity uses it:

| What | How | Public build |
|---|---|---|
| A passage | `ifdef::internal[]` … `endif::[]` | removed |
| A sentence or phrase | single-line `ifdef::internal[text]` | removed |
| A whole page | `:page-internal:` in the page header | page, its nav entry and its search entry dropped |
| A whole area | `internal: true` for the module in the extension's config | every page in the module dropped |
| An image or attachment | used only from internal content | not published (unreferenced files are dropped) |

- **In the team build, internal content is visibly marked**: passages render in a labelled "Team" box; internal pages and areas carry a "Team only" badge in the nav. You always know what readers won't see.
- **A small Antora extension** in the repo (`manual/extensions/internal.js`) drops internal pages, modules and nav entries, and the files only they reference, when `internal` is unset.
- **Leaks fail the public build:** a public page that links to internal content is an error there (the extension checks xrefs, and the public build runs with `--log-failure-level=warn`).

## Docs to update in the implementation

- `docs/DEVELOPMENT.md` (owns the release pipeline): the branch table, the release PR, the repo settings.
- `docs/manual.md`: versions by branch, the site and Pages, marking content internal.
- `AGENTS.md`: agents merge to `main` when it's green and reviewed; only the user merges into `release`.
- `docs/OVERVIEW.md`'s "cuts on every push to `main`" → "on every merge into `release`".

## Left to the builder

- How the release PR summarizes manual changes per page (headings touched vs. a short prose summary).
- The banner wording and the Team box styling.
- Whether `release-pr.yml` is a workflow or a step in `release.yml`.

## Open

- Which GitHub account the Pages site is served under, and whether it needs a custom domain.
