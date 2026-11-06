# Release branch and the published manual — design

**Status:** agreed and merged 2026-11-06 (#623); hosting revised the same day (§4). Decisions the user made are marked *(user)*; the rest are proposals.
**Context:** today every push to `main` publishes a release (`.github/workflows/release.yml`, [the release pipeline](../../DEVELOPMENT.md#ci-the-release-pipeline)), and the user's approval of each merge was a habit, not a rule (`main`'s ruleset requires the `gate` check and no approvals). Under the manual-first model ([design](2026-11-05-manual-first-operating-model-design.md)), the user wants to approve **releases**, not merges, and to publish the manual, all of its versions, as a site.

## Intent

- The user approves what ships, once per release, by looking at what changes for users. *(user)*
- Agents merge to `main` on their own once checks and review pass. *(user)*
- A merge from `main` into a `release` branch starts the release. *(user)*
- **The full manual** (Released, Next and Goal, internal content included) is hosted on **Cloudflare**, for the team. *(user)*
- **The customer-facing manual** (Released only, internal content left out) is **served by Jam itself**. *(user)*
- Any manual content can be marked internal; the customer-facing build leaves it out. *(user)*

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

Two builds, two hosts *(user)*:

| Build | Versions | `internal` | Host | Who sees it |
|---|---|---|---|---|
| **Team manual** | Released, Next, Goal | set: shown, marked | **Cloudflare** behind Cloudflare Access | the team |
| **Customer manual** | Released only | unset: left out | **Jam**, at `/app/manual/` | anyone signed in to that Jam |

**Versions come from the branch, not the files.** Each branch's `manual/antora.yml` stops hard-coding `version:`. The playbook maps branch to version on the content source, so manual files are identical on every branch and merges never touch version names. The `goal` branch's `version: goal` commit is reverted.

| Branch | Version | Shown as | Banner |
|---|---|---|---|
| `release` | `released` | **Released** (default) | none |
| `main` | `next` | **Next** | "Merged, not released yet." |
| `goal` | `goal` | **Goal** | "How the product should work, not necessarily how it works today." |

### Team manual on Cloudflare

- `manual-site.yml` builds all three versions on any push to `release`, `main` or `goal`, from one checkout with full history, and uploads the static site to Cloudflare (Workers static assets or Pages; the builder's call) with `wrangler`. Deploys are serialized (one concurrency group, newest wins).
- It opens on **Released**; the switcher shows all three.
- **Cloudflare Access** in front: only the team gets in. The site contains internal content and the goal manual, so it is never public.
- Needs, from an admin: a Cloudflare account and project, an API token and account id as repo secrets, and the Access policy (who may sign in).
- Diffs it can show later: `goal` vs `release` is all the work left; `main` vs `release` is what the next release ships.

### Customer manual in Jam

- Built from the commit being built, without `internal`, as one version, into `internal/jam/uiassets/manual/` (git-ignored, like the app bundle) and **embedded in `jam`**. Each Jam carries exactly the manual for its own release: no host to keep in step, nothing to go stale.
- Served at **`/app/manual/`** to any signed-in user, alongside the user area and admin console. The app can link to a page by its `page-id` (a Help link per page is a later step).
- The build runs where `scripts/app.sh` builds the app (`just build` and the release's binaries leg); `release.yml`'s `go` path filter gains `manual/**`, so a manual-only change rebuilds the binaries on the next release.
- Off a `release` build (a PR, `main`, a local `just build`), the embedded manual is that commit's manual. Only a release build ships to customers.

## 5. Marking content internal *(user: "any content, anywhere")*

One attribute, `internal`, set only in team builds (Cloudflare, `just manual`, previews). Every granularity uses it:

| What | How | Customer build |
|---|---|---|
| A passage | `ifdef::internal[]` … `endif::[]` | removed |
| A sentence or phrase | single-line `ifdef::internal[text]` | removed |
| A whole page | `:page-internal:` in the page header | page, its nav entry and its search entry dropped |
| A whole area | `internal: true` for the module in the extension's config | every page in the module dropped |
| An image or attachment | used only from internal content | not published (unreferenced files are dropped) |

- **In the team build, internal content is visibly marked**: passages render in a labelled "Team" box; internal pages and areas carry a "Team only" badge in the nav. You always know what readers won't see.
- **A small Antora extension** in the repo (`manual/extensions/internal.js`) drops internal pages, modules and nav entries, and the files only they reference, when `internal` is unset (the customer build).
- **Leaks fail the customer build:** a customer page that links to internal content is an error there (the extension checks xrefs, and that build runs with `--log-failure-level=warn`).

## Docs to update in the implementation

- `docs/DEVELOPMENT.md` (owns the release pipeline): the branch table, the release PR, the repo settings.
- `docs/manual.md`: versions by branch, the two builds and their hosts, marking content internal.
- `docs/web-app.md` (owns `/app/*` serving and `scripts/app.sh`): the embedded manual at `/app/manual/`.
- `AGENTS.md`: agents merge to `main` when it's green and reviewed; only the user merges into `release`.
- `docs/OVERVIEW.md`'s "cuts on every push to `main`" → "on every merge into `release`".

## Left to the builder

- How the release PR summarizes manual changes per page (headings touched vs. a short prose summary).
- The banner wording and the Team box styling.
- Whether `release-pr.yml` is a workflow or a step in `release.yml`.

## Open

- The Cloudflare account, the site's hostname, and the Access policy (which identities may sign in).
