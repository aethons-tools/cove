# Config in git, edited on branches — design

**Status:** agreed with the user 2026-10-31. Phases 1 (following), 2 (the branch editor) and 3 (no live edits, while `config.github` is set; drift detection and force kept, release refused rather than removed; a Jam without a repo still edits live — not yet the "always a repo" read-only views) built on branch `config-branch-editor`; docs: `docs/usage/jam/config-git-sources.md`, `config-branches.md`.
**Context:**
- **Today a Jam can follow a config repo, but it reads it and never writes it** ([config-git.md](../../usage/jam/config-git.md), [config-git-sources.md](../../usage/jam/config-git-sources.md)). Every admin edit changes the live config. None is ever refused: each shows up as **drift** a minute later.
- **The user's real moment:** an agent (admin-spider) asked to run in a different kit. The user changed it in the admin console, and it took effect. After that the Jam could no longer sync with the repo because of the drift. The only way back is `config write --all`, which can overwrite edits that are in the repo. The user calls this "a tortured hell" of a hand-edited repo versus an easy UI, with drift making them unmergeable.

## Intent

**Git is the only source of truth for a Jam's configuration.**
- The admin console is a **branch editor** on the config repo. It is easy to use, but every save is a commit on a branch, never a live edit.
- The live Jam runs exactly what the branch it follows says. Drift can't happen.
- Review, diffs and history are left to git tooling (GitHub, a checkout). Jam doesn't rebuild them ("we don't need to keep recreating it here").

## Goals

- **The quick change stays quick.** Changing admin-spider's kit means: open your branch, edit, Merge, and it applies within moments.
- **Merge or PR.** When a set of changes is done, the user presses **Merge** or **PR**.
  - A Merge that would conflict is refused; the user can then use PR.
  - Links go to the branch and to its PR.
- **Hand edits are first class.** Pushing a commit to the followed branch, or merging a PR on GitHub, takes effect on its own, with no sync button.
- **No drift, and no `write --all` needed after the cutover.**

## Non-goals (this design)

- **Kits from their own repos** (`kits/<name>.source.yml`, and standalone `kit track|sync`). Deferred by the user ("there are plenty of questions there"). They stay as they are, and the editor shows them read-only. My call: kits that aren't inline in the repo (standalone-tracked, or behind a `.source.yml`) are **never removed by following**, and `kit track|sync` stays allowed until remote kits are designed.
- **The VS Code side.** VS Code will open the user's branch as a folder, with the same branch, Merge and PR. It is the next design, on top of this one.
- **Trying a branch in a session** before merging (a session raised from an unmerged branch). The user said it would be "convenient". It's a later phase.
- **Diff, history or review views in Jam.**
- **A special cutover.** The user reconciles once by hand with `config write --all`, which stays "for a short bit" and is removed later.

## Anti-goals

- **No path that changes configuration live.** If any edit route bypasses the repo, drift comes back.
- **No half-applied config.** A commit that fails to apply leaves the last good config running.
- **Never overwrite someone else's commits.** No force pushes and no `write --all`-style overwrites from Jam.

## Decided

### Where the repo is configured (user's)

The tracked repo moves out of the store and into the **serve config**:

```yaml
config:
  github:
    url: https://github.com/acme/jam-config
    cred: jam-git-tracking
```

- **The key names the hosting service** (user's, 2026-10-31): `config.github` now; `config.gitlab` follows quickly after.
  - Exactly one of them is set.
  - The kind decides how PR works: a GitHub pull request, or a GitLab merge request. Clone, push and merge are plain git either way.
  - My call: `url` may be any host of that kind, including GitHub Enterprise or a self-hosted GitLab. The API base is derived from it.
- `cred` names a credential **demand** ([credentials.md](../../usage/jam/credentials.md)). It must be able to push to the repo and open pull requests (merge requests on GitLab). My call: any credential source (value, command, mint), so a GitHub App token can be minted. That differs from `kits.git`, which is static only.
- My call: optional `branch:`, the branch the Jam follows, defaulting to the repo's default branch. Optional `dir:`, the repo subdirectory holding the config, defaulting to the root.
- **Removed:** the store's tracked source, `config track|untrack`, and `PUT|DELETE /admin/config/source`. The Tracking page shows the source read-only, from the serve config.

### Always a repo (user's)

There is **no live editing of configuration, ever**, even on a Jam with no repo.
- **A Jam without `config.github` or `config.gitlab`:** the config views are read-only and say how to set it (my call).
- **Serve still starts,** so an existing Jam keeps running its stored config until a repo is set.

### Following the branch (my call, agreed in outline)

- Jam keeps a **persistent clone** of the repo in its state dir and checks the followed branch for new commits about once a minute.
- **A new commit** is planned and applied in one transaction, as an apply is today.
- **The first apply needs a confirm (my call).** When the repo setting is first set, or changed, Jam plans the repo against the live config but doesn't apply it. The Tracking page shows the plan, removals included, and waits for an operator to confirm. Only then does following apply on its own. This keeps a partial repo, or a skipped `write --all`, from deleting live roles and projects.
- **On apply, the live config becomes exactly what the repo says:** objects of a config kind that aren't in the repo are removed. With git as the only truth, there are no "unmanaged" config objects. That's why the user's one-time `write --all` comes first.
- **A commit today's guards refuse** (removing a role with a live instance, a project with members) fails as a whole, like any failure (my call). It is retried at each check, so it applies once the obstacle is gone, e.g. when the instance ends.
- **A commit that fails to plan or apply** (for example, a role naming a kit that doesn't exist) leaves the last good config running. It flags the failure, with the commit, on the Tracking page and as a condition. The next commit that plans cleanly is applied.
- **What disappears:** drift detection, `--force` and `config release`. There is nothing to drift from.

### The branch editor (user's)

- **Every config view is in the context of one branch.**
  - **The followed branch** is the default context, and it is **read-only**: it's what's running.
  - **Create branch** branches from the followed branch. Its name defaults to one derived from the operator's name; my call: `jam/<operator>`. The context switches to it.
  - **Open branch** lists the repo's branches and switches to the one picked: yours, another's, or one pushed by hand.
- **On a branch, the console's existing edit forms save as commits** on that branch, one commit per save. My call:
  - The commit author is the operator and the committer is Jam.
  - The commit message names the object and the change, e.g. `role pair/admin-spider: kit`.
- **A save based on a stale branch head is refused** ("reload to get the newer version"): someone else, or a hand push, moved it.
- **Generally one ongoing branch per person** (user's). After a Merge you stay on it, now level with the followed branch.
- **The context bar** shows the branch, with links to it and to its open PR, and the **Merge** and **PR** buttons.

### Merge and PR (user's)

- **Merge** merges the branch into the followed branch and pushes. My call: fast-forward when possible, else a merge commit.
  - It is **refused on a conflict**, and the user can press PR instead.
  - My call: the merged tree is planned **before** pushing. A merge that wouldn't apply is refused, with the plan's errors, and nothing is pushed.
  - After the push, Jam applies the result at once, without waiting for the next check.
- **PR** opens a pull request from the branch into the followed branch and links to it.
  - On GitHub, a pull request through the GitHub API; on GitLab (the fast follow), a merge request through the GitLab API. Both use the same credential.
  - Jam applies it when it's merged, through following.

### The commands (my call)

- **The CLI's config-editing verbs are refused** while config lives in git (for example `context set`, `kit push`, `destination add`, the role, project and user writes). The error points at the repo. CLI users have git: they edit a checkout and push, and following applies it.
- **Kept:** `config write --all`, temporarily.
- **Kept, unchanged:** `config diff --dir`, as a dry run against a checkout.

### Kit drafts (my call)

- **Editing an inline kit** (`kits/<n>/`) on a branch replaces the kit draft: its files are edited as commits.
- **Draft publish is removed,** since it is a live edit.
- **Draft test builds and draft studios** come back with the "trying a branch" phase.

### The repo's `jam.yml` (my call)

It stays compare-only, as today, and read-only in the editor. Applying it would need serve reloads, and it would be circular: the repo setting and its credential live in the serve config.

### Who may do what (my call)

The existing operator gate covers it all: any operator may open any branch, save to it, and Merge or PR. Commit authors record who did what.

### Live state stays live

Objects with no file kind stay outside git and editable as today:
- grants, accounts and OIDC identities, app tokens, Studio Managers, enrollments and coves, personal sessions, credential values;
- standing-session operations: reset, upgrade, restart.

## Phases (my call)

The phases are slices of one branch and one PR, merged at the end as the hub was. No Jam ever runs a phase where live edits are silently reverted.


1. **Following:** `config.github` in the serve config; the persistent clone and push credential; following and applying the branch; the Tracking page read-only; `track|untrack` removed.
2. **The branch editor:** the branch context; Create/Open branch; saves as commits; Merge and PR.
3. **No live edits:** every config admin route and CLI verb refused outside a branch; drift, force and release removed.
4. **Fast follow:** `config.gitlab`, with merge requests for PR (built).
5. **Later:**
   - VS Code: the branch as a folder.
   - Trying a branch in a session.
   - Removing `config write --all`.
   - Remote kits.

## Left to the builder

- How saves map onto files. Most likely: render the object as `config write` does, and commit just the changed files.
- The check interval, and whether a webhook is added later.
- Clone storage, fetch depth, and locking between saves, merges and following.
- The branch list's order and filtering.
