# Always a repo — design

**Status:** agreed with the user 2026-10-31; building on branch `always-a-repo`.
**Builds on:** [the branch editor design](2026-10-31-config-branch-editor-design.md), merged in #594. That design's §"Always a repo" said a Jam without a repo becomes read-only. #594 built the branch editor, and no live edits *while a repo is set*. This design finishes it.
**Context:**
- #594 left every live config write path in place for a Jam without a repo, and kept drift detection, `--force` and release, refused rather than removed.
- The user, 2026-10-31: "We can just do that all at once. There are no deployments right now that do not have a repo behind them."
- Dev and test Jams have no GitHub or GitLab repo. The user: "Let's add a `config.git` that is just a git repo and has to be reachable from the Jam server. That can be local or a repo that wants SSH access."

## Intent

There is **no path that changes configuration live**: config changes only by a commit on the followed branch, by hand or through the branch editor. A Jam with no repo runs its stored config, read-only.

## Decided

### `config.git` (user's; details my calls, agreed)

A third kind next to `config.github` and `config.gitlab`. Exactly one of the three is set.

```yaml
config:
  git:
    url: /srv/jam-config            # or file:///srv/jam-config, ssh://git@host/x.git, git@host:x.git
    branch: main                    # required
    dir: config
    cred: jam-config-ssh            # ssh only: a demanded credential holding an OpenSSH private key
    host-keys: |                    # ssh only, required: known_hosts lines
      host ssh-ed25519 AAAA…
```

- **What it accepts:** a local repo (an absolute path or `file://`) or SSH (`ssh://` or the scp form). For https, use `config.github` or `config.gitlab`.
- **No PR button and no branch links.** A plain repo has no hosting API and no web page.
- **`branch` is required.** What the Jam runs never depends on what is checked out.

**A local repo:**
- Jam keeps its bare clone in the state dir and treats the repo as a remote. go-git's `file://` client runs the `git` binary, which Jam doesn't have. So the `file` protocol is served by go-git's built-in server, which runs inside the process, with a loader that opens the repo through `git.PlainOpen`. go-git's own loader opens a checkout with a working tree wrongly. Tested in a throwaway test: fetch, and a push of a new branch.
- Jam changes refs and objects only: never the working tree, the index or `HEAD`.
- **A save, Merge or branch create that would move a branch checked out** in the repo, or in any of its worktrees, is refused: `jam/<op> is checked out in /srv/jam-config; check out another branch, or merge it yourself`. Git refuses such a push for the same reason.

**SSH:**
- go-git's SSH client, with the private key from `cred`. The key never reaches argv, logs or the store.
- `host-keys` is required. It is written to `<state-dir>/config-repo-known_hosts`, which holds only public keys, and checked with `knownhosts`. Jam doesn't trust a host on first sight. On a mismatch, the error shows the line for the key the host offered.
- No `ssh-agent`: it is unclear whose agent a serve would be using.

**Safety:** local paths and SSH are allowed **only in `config.git`**, which only the operator edits. gitsource's rules stay as they are for kit sources, so a commit to the config repo cannot point a `.source.yml` at a file on the Jam host.

### One reader for the followed branch (my call)

The follower and the sync read the followed branch through Jam's bare clone (`configrepo.Repo`), for all three kinds. This is the clone the branch editor already uses. Every other source, the kits' `.source.yml` included, still goes through the kit fetcher. In code, a fetcher wrapper sends the followed source to the clone and passes the rest on.

### No live edits (user's; the scope is my call)

**Removed, everywhere and whether or not a repo is set:**
- **`/admin` config-write routes:** destinations, model specs, users (name, logins, delete), connections, projects (create, rename, delete, members, rooms, escalation, chat service), roles (put, delete, egress, standing declare and dismiss), context at all three layers, kits (push, versions, pin, delete), kit drafts (open, edit, files, publish, build), `config apply` (`--dir` and `--git`), and `config/managed` release.
- **Their adminclient methods and CLI verbs:** destination, role, project lifecycle/escalation/chat-service/member, kit push|pin|rm, kit draft, standing add|rm, egress, user add|rename|rm|login, connection, room, context set|clear, model-spec add|update|delete, config apply, and config release.
- **The guards** (`config_ingit.go`, and the branch router's refusal list): nothing is left for them to refuse.
- **Live writes in the console:** the console's config routes run only inside a branch workspace. Without the branch header they are refused, repo or not.

**Kept live:** grants, accounts and OIDC, app tokens, enrollments, coves, personal sessions, Studio Managers, collab, standing reset/upgrade/restart, the Request, kit import/track/untrack/sync, `jam import` (a snapshot restore), `config write|diff|sync|status`, and `export`.
- Kits from their own repos stay deferred, as in #594.
- `config sync` stays as the first apply's confirm, along with the Tracking page's Confirm and apply.

**Kit drafts:**
- On a branch, the console's draft editor keeps working: its publish is a commit of the kit's files.
- Live drafts go, with their test build and draft studios. Both come back with "trying a branch".

### Drift, force and release go (user's, from #594's list)

- **Removed:** the drift check and its condition (`config.drift`), `--force` on diff, apply and sync, `config release`, the managed badges, the "this will drift" notes, the draft publish drift checkbox, and Tracking's Release button and Force box.
- **Marks stay** as internal bookkeeping: an apply deletes only the objects it once created, and marks say which those are.

### Objects Jam creates itself (my calls)

- **A repo object that matches an unmanaged live object of the same key is applied over it** (adopted, or updated), never refused. With no live edits, an unmanaged object is either one Jam created itself or one left over from before the cutover, and the first apply's confirm shows each update. `seededConfigObjects` and the "exists and is not managed" refusal go.
- **Unmanaged objects the repo doesn't hold are never deleted** (true today): the default studio kit, the default model spec, the requisitioner's default project, and the relay's author connections.
- **Jam's own ensures skip an object the repo manages:** `EnsureDefaultStudioKit`, `EnsureDefaultModelSpec`, and the requisitioner project. Otherwise they would be live edits to an object the repo owns.
- **Room connections** are created by a room add, which now runs only in a branch workspace, so they are committed with the room.
- **The deprecated `bot-token-cred` and `tracker-token-cred`** bind only an **existing** connection of their kind whose cred matches. They no longer create or rebind one live. Otherwise serve refuses to start and points at `connections/<kind>.yml` in the repo, or at `connection:`. (Revised while planning: an existing deployment keeps starting.)
- Error texts that name a removed verb (for example `jam connection add`) point at the config repo instead.

### The console, read-only (my call)

- **The followed branch, and a Jam without a repo:** every config form and button is hidden; live-state controls (Request, reset/upgrade, accounts, kit import/sync) stay. Pages read a `useConfigEditable()` (a branch is open).
- **Without a repo,** the branch bar says the config is read-only and how to set `config.github`, `config.gitlab` or `config.git`. It no longer renders nothing.

## Phases (my call)

The phases are slices of one branch and one PR, merged at the end, like #594:
1. `config.git` and the one reader.
2. The planner: adopt-over-unmanaged; drift, force and release removed; Jam's ensures skip managed objects; the deprecated `*-token-cred` dropped.
3. The `/admin` routes, adminclient and CLI removed; the tests moved to store seeding.
4. The console: live writes refused without a branch; read-only views; badges and drift UI removed.
5. Docs.

## Left to the builder

- The tests that seed through the removed routes: seed the store directly, or apply a config tree.
- How `config.git` URLs are labelled and redacted. The scp form carries `git@`, which is a user name, not a secret.
