---
summary: The config branch editor — the admin console's config pages work on a branch of the followed config repo; each save is a commit to it; Create/Open branch, Merge (planned first, refused on a per-file conflict, applied at once) and PR (a GitHub pull request or GitLab merge request); the X-Jam-Config-Branch header, branch workspaces, and what stays live.
read_when: You are editing a Jam's config in the admin console on a branch, merging it or opening a PR, wondering why a save or merge was refused (stale branch, conflict, refusals), or changing the branch editor (the router, workspaces, configrepo).
owns: the branch context and its header, branch workspaces (scratch stores) and saves as commits, Create/Open/Merge/PR and their API, the per-file merge rule, what config paths a branch serves and what stays live, the bare clone in the state dir
prereqs: config-git-sources.md (following config.github or config.gitlab); ui-config.md (the Tracking page); config-git.md (the repo layout)
tier: leaf
updated: 2026-10-31
---

# The config branch editor

With `config.github` or `config.gitlab` set ([config-git-sources.md](config-git-sources.md)), the admin console's **Config** pages work on one branch of the followed repo at a time. The followed branch (`main`) is what's running and is the default view; editing happens on a branch and reaches the Jam only by **Merge**, or a pull request merged in git.
The design is in [the config-in-git spec](../../superpowers/specs/2026-10-31-config-branch-editor-design.md).

## The branch bar

Above every Config page:

- **On the followed branch:** "Viewing main: what's running (read-only)", with **Create branch** (default name `jam/<operator>`) and **Open branch** (the repo's other branches).
- **On a branch:** its name and head, a link to it (and to its PR, once open), **Merge**, **PR**, **Open branch** and **Back to main**.

Switching branches reloads every page from the Config root: objects are addressed by ids that differ per branch.

## Saving

Each successful write on a branch (any form's save, delete, kit draft publish…) becomes **one commit** on it, authored by the operator (`<id> <id>@jam`), committed by `Jam`, with the message `jam: <METHOD> <api path>`. The write is answered only after the commit is pushed; a save that changes no file makes no commit.

How: the branch is loaded into a **workspace**, a scratch store holding its config tree (`ApplyConfigTree`), cached while the branch stays at that head. The forms edit the workspace; the save exports it (`ExportConfigTree`, all objects) and writes it over the branch's files as `jam config write` does, so kit-dir mirroring and `.dockerignore` rules are the same. Files outside the config dir, and `jam.yml`, are never touched.

- **Someone pushed meanwhile.** A branch that moved since it was read is picked up before the next request; a push that lands between read and save refuses the save (409 "the branch moved since it was read; reload it") and the workspace is dropped.
- **A branch that doesn't plan** (a broken file pushed by hand) can't be opened: 409 naming the refusals. Fix it in git.

## Merge and PR

**Merge** merges the branch into the followed branch and pushes, then the Jam applies it at once (the follower runs now).

- **Planned first.** The merged config is planned against the live Jam; refusals refuse the merge and nothing is pushed.
- **Per-file conflicts.** Jam merges with go-git alone: a file changed on both sides differently (a delete against an edit included) refuses the merge ("Merge refused: merge conflict in: …"). Open a PR instead, where git merges line by line. A fast-forward and an already-merged branch need no merge commit; otherwise the merge commit has the followed head and the branch as parents, and the branch moves to it too.

**PR** (on GitLab, **MR**) opens or finds a GitHub pull request, or a GitLab merge request, from the branch into the followed branch with the same credential; the follower applies it once it is merged in git.

## No live edits

With a config repo set the repo is the only source of the Jam's configuration, so nothing changes it live:

- **The admin API** (`jam` CLI, any client) refuses every config-object write with 409 `config is in git (config.github): edit it on a branch in the admin console, or in the repo`: destinations, model specs, connections, users (create, delete, logins, name), projects, roles (standing declarations included), the Jam context, kits (push, pin, versions, delete, draft publish), the snapshot import (`POST /admin/config`) and `config release`. `config apply` applies only the followed repo (`--dir` and another `--git` source are refused); `config diff`, `config sync`, `config status` and `config write` work as before.
- **The admin console** refuses a config write off a branch (409 "the config is in git: open a branch to edit it") and shows no Release on Config › Tracking.
- **Still live:** sessions and grants, accounts, users' OIDC identities and app tokens, enrollments, standing resets and upgrades, kit draft test builds, and kits from their own git sources (`kit import|track|sync`, deferred). Through the admin API (`jam kit draft …`) a kit draft is editable until publish; the console edits drafts on a branch, where publishing is a commit.
- **Kept, for now:** drift detection (a safety signal, since kits from their own sources can still move) and `--force` (the follower applies with it). A Jam without a config repo still edits its config live.

## What a branch serves

The header `X-Jam-Config-Branch: <branch>` (the console sends it while a branch is open) sends these admin UI API paths to the workspace: `tree`, `users`, `connections`, `projects`, `roles`, `destinations`, `model-specs`, `kits`, `jam/context`. Live state under them stays live: a user's accounts, OIDC identities and app tokens; a project's agents; a role's Request; a kit draft's test build. Every other path ignores the header. Naming the followed branch is a 400.

Branch API (operator-only, audit-logged as `ui config branch created|merged|pr`): `GET|POST /app/admin/api/config/branches`, `POST …/config/branches/merge` `{branch, head}`, `POST …/config/branches/pr` `{branch}`, `GET …/config/branches/pr?branch=`.

Jam keeps a bare clone of the repo at `<state-dir>/config-repo`, fetched before every operation.
