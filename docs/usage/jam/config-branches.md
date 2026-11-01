---
summary: The config branch editor — the only way to change config in the admin console; its pages work on a branch of the followed config repo; each save is a commit to it; Create/Open branch, Merge (planned first, refused on a per-file conflict, applied at once) and PR (a GitHub pull request or GitLab merge request); the X-Jam-Config-Branch header, branch workspaces, no live edits, and what stays live.
read_when: You want to change a Jam's config (add a role, edit a kit, a user, a project) in the admin console, are merging a branch or opening a PR, wondering why a save, merge or live write was refused (stale branch, conflict, checked-out branch, read-only), or changing the branch editor (the router, workspaces, configrepo).
owns: the branch context and its header, branch workspaces (scratch stores) and saves as commits, Create/Open/Merge/PR and their API, the per-file merge rule, the refusal of live config writes, what config paths a branch serves and what stays live, the bare clone in the state dir
prereqs: config-git-sources.md (following config.github, config.gitlab or config.git); ui-config.md (the Tracking page); config-git.md (the repo layout)
tier: leaf
updated: 2026-11-01
---

# The config branch editor

Config changes only by a commit to the followed repo ([config-git-sources.md](config-git-sources.md)). In the admin console, the **Config** pages work on one branch of it at a time. The followed branch (`main`) is what's running and is the default view, read-only; editing happens on a branch and reaches the Jam only by **Merge**, or a merge in git (a pull request, or by hand).
The design is in [the config-in-git spec](../../superpowers/specs/2026-10-31-config-branch-editor-design.md).

## The branch bar

Above every Config page:

- **On the followed branch:** "Viewing main: what's running (read-only)", with **Create branch** (default name `jam/<operator>`) and **Open branch** (the repo's other branches).
- **On a branch:** its name and head, a link to it (and to its PR, once open), **Merge**, **PR**, **Open branch** and **Back to main**. With `config.git` there is no PR button and no links.
- **No config repo:** a **Read-only** notice: `This Jam has no config repo. Set config.github, config.gitlab or config.git in the serve config to edit its config on branches.` No form or button that changes config is shown.

Switching branches reloads every page from the Config root: objects are addressed by ids that differ per branch.

## Saving

Each successful write on a branch (any form's save, delete, kit draft publish…) becomes **one commit** on it, authored by the operator (`<id> <id>@jam`), committed by `Jam`, with the message `jam: <METHOD> <api path>`. The write is answered only after the commit is pushed; a save that changes no file makes no commit.

How: the branch is loaded into a **workspace**, a scratch store holding its config tree (`ApplyConfigTree`), cached while the branch stays at that head. The forms edit the workspace; the save exports it (`ExportConfigTree`, all objects) and writes it over the branch's files as `jam config write` does, so kit-dir mirroring and `.dockerignore` rules are the same. Files outside the config dir, and `jam.yml`, are never touched.

- **Someone pushed meanwhile.** A branch that moved since it was read is picked up before the next request; a push that lands between read and save refuses the save (409 "the branch moved since it was read; reload it") and the workspace is reloaded.
- **A refused write** (a validation error, a stale draft save) that changed nothing keeps the workspace as it is. One that left a partial change, and a refused save, reload the workspace from the branch. Open kit drafts carry over a reload (see [kits-drafts.md](kits-drafts.md)).
- **A save that would leave the branch unable to load** (say, an escalation chain naming a user who doesn't exist) is refused before anything is committed: 409 "not saved: the branch would no longer load: …" naming the refusals. Each save plans the saved tree as a load would.
- **A branch that doesn't plan** (a broken file pushed by hand) can't be opened: 409 naming the refusals. Fix it in git.
- **A checked-out branch** of a local `config.git` repo is never moved: a save or Merge onto it is refused 409 (the message: [config-git-sources.md](config-git-sources.md#a-local-or-ssh-repo-configgit)).

## Merge and PR

**Merge** merges the branch into the followed branch and pushes, then the Jam applies it at once (the follower runs now).

- **Planned first.** The merged config is planned against the live Jam; refusals refuse the merge and nothing is pushed.
- **Per-file conflicts.** Jam merges with go-git alone: a file changed on both sides differently (a delete against an edit included) refuses the merge ("Merge refused: merge conflict in: …"). Open a PR instead, where git merges line by line. A fast-forward and an already-merged branch need no merge commit; otherwise the merge commit has the followed head and the branch as parents, and the branch moves to it too.

**PR** (on GitLab, **MR**) opens or finds a GitHub pull request, or a GitLab merge request, from the branch into the followed branch with the same credential; the follower applies it once it is merged in git.

## No live edits

Nothing changes config live, whether or not a repo is set:

- **The admin API and the `jam` CLI** have no config writes. Destinations, model specs, connections, users (name, logins, delete), projects (members, rooms, escalation, chat service), roles (egress, standing declarations), the Jam context and kits (push, pin, delete, drafts) are read-only there; `config write|diff|sync|status` remain. A snapshot import (`jam import`, `POST /admin/config`) on a Jam following a repo is 409 `config is in git: edit it on a branch in the admin console, or in the repo` ([backup.md](backup.md)).
- **The admin console** refuses a config write off a branch: 409 `the config is in git: open a branch to edit it`, or on the followed branch `the followed branch is read-only; create or open a branch`. With no config repo: 409 `this Jam has no config repo, so its config is read-only: set config.github, config.gitlab or config.git in the serve config`.
- **Still live:** sessions and grants, accounts, users' OIDC identities and app tokens, enrollments, coves, personal sessions, Studio Managers, standing resets, upgrades and restarts, a role's Request, kits from their own git sources (`kit import|track|untrack|sync`), and `jam import` on a Jam with no repo.
- **Kit drafts** are edited on a branch, where publishing is a commit ([kits-drafts.md](kits-drafts.md)).

## What a branch serves

The header `X-Jam-Config-Branch: <branch>` (the console sends it while a branch is open) sends these admin UI API paths to the workspace: `tree`, `users`, `connections`, `projects`, `roles`, `destinations`, `model-specs`, `kits`, `jam/context`. Live state under them stays live: a user's accounts, OIDC identities and app tokens; a project's agents; a role's Request. Every other path ignores the header. Naming the followed branch is a 400.

Branch API (operator-only, audit-logged as `ui config branch created|merged|pr`): `GET|POST /app/admin/api/config/branches`, `POST …/config/branches/merge` `{branch, head}`, `POST …/config/branches/pr` `{branch}`, `GET …/config/branches/pr?branch=`.

Jam keeps a bare clone of the repo at `<state-dir>/config-repo`, fetched before every operation.
