---
summary: A Jam following its config repo (`config.github`, `config.gitlab` or `config.git` in the serve config) — the three kinds, a local or SSH repo with `config.git`, the follower that checks the branch every minute and applies new commits, the first apply's confirm, `config sync|status`, `config diff --git`, kits referenced by `kits/<name>.source.yml`, provenance, locks, routes and errors.
read_when: You want a Jam to follow a config repo (on GitHub, GitLab, a local path or over SSH), or to know why it applied, or didn't apply, a commit; are confirming the first apply, running `config sync|status|diff --git`, are referencing a kit by its own git source (`kits/<name>.source.yml`), or a config sync or follow failed to fetch, refused a commit or a host key.
owns: the serve config's `config:` block (`config.github`, `config.gitlab`, `config.git`: URL forms, branch, dir, cred, host-keys, the checked-out-branch refusal), following the config repo (the follower, its states, the first-apply confirm, the config.follow condition, the applied-commit record), `config sync|status`, the `--git` verbs, the `/admin/config/sync|follow` routes and `{source}` bodies, `kits/<name>.source.yml` and how it is fetched, planned and written, config-applied kit provenance and tracking
prereqs: config-git.md (layout, plan actions, marks); kits-git.md (source syntax, kits.git credentials, fetch limits)
tier: leaf
updated: 2026-11-01
---

# Config from git: following the repo, and kit references

A Jam's configuration changes only by a commit to its config repo: by hand,
or on a branch in the admin console ([config-branches.md](config-branches.md)).
A Jam follows **one** config repo, named in its serve config
([serve.md](serve.md#the-serve-config)). With none, it runs its stored config, read-only.

```yaml
config:
  github:
    url: https://github.com/acme/jam-config
    cred: jam-git-tracking   # a demanded credential that can read the repo
    branch: main             # optional; default main
    dir: jam                 # optional; the repo dir holding the config, default its root
```

On GitLab (gitlab.com or self-hosted) the same block is `config.gitlab`. A plain git repo, local or over SSH, is `config.git` ([below](#a-local-or-ssh-repo-configgit)). Set exactly one (`config: set one of config.github, config.gitlab and config.git`). The kind decides how the branch editor opens a review: a GitHub pull request or a GitLab merge request. Its token is sent with the username `oauth2` and needs the `write_repository` and `api` scopes. A GitHub token needs contents write and pull requests.

Design: [config in git, edited on branches](../../superpowers/specs/2026-10-31-config-branch-editor-design.md) and [always a repo](../../superpowers/specs/2026-10-31-always-a-repo-design.md).
Sources elsewhere use the kit syntax, `<repo>[//<dir>]@<ref>`
([kits-git.md](kits-git.md#the-source-repodirref)), and the same fetcher;
its `cred` is the credential for exactly its URL, and fetch errors name it as `config.github cred <name>` (or `config.gitlab`). The admin console
edits it on branches and merges them: [config-branches.md](config-branches.md).

## A local or SSH repo: `config.git`

For a repo with no GitHub or GitLab behind it: a dev Jam's local repo, or a git server reached over SSH.

```yaml
config:
  git:
    url: /srv/jam-config.git     # or file:///srv/jam-config.git, ssh://git@host/jam-config.git, git@host:jam-config.git
    branch: main                 # required: what the Jam runs never depends on what is checked out
    dir: jam                     # optional, as for config.github
    cred: jam-config-ssh         # ssh only: a demanded credential holding an OpenSSH (or PEM) private key
    host-keys: |                 # ssh only, required: known_hosts lines
      host.example ssh-ed25519 AAAA…
```

- **URL.** An absolute path or `file://` (a local repo), or `ssh://` or the scp form `user@host:path`. Anything else is refused: `config.git.url: want a local path, file://, ssh:// or user@host:path (for https use config.github or config.gitlab)`; an `ssh://` URL must not carry a password. `config.git.branch is required`.
- **Local.** Jam reads the repo in-process (no `git` binary) and changes only its refs and objects: never the working tree, the index or `HEAD`. `cred` and `host-keys` are refused for a local repo (`… is only for ssh`). A bare repo is easiest: no branch is checked out.
- **Checked-out branches.** A branch editor save or Merge that would move a branch checked out in the repo, or in any of its worktrees, is refused (409) and moves nothing, as git refuses such a push: `<branch> is checked out in <dir>; check out another branch, or merge it yourself` (`<dir>` is the repo, or the worktree, holding it).
- **SSH.** go-git's SSH client with the key from `cred`, which never reaches argv, logs or the store; no `ssh-agent`. `cred` and `host-keys` are required for ssh. Jam writes `host-keys` (public keys only) to `<state-dir>/config-repo-known_hosts` and never trusts a host on first sight. An unknown key fails with `config.git: the host key of <host> is not in host-keys; if you trust it, add: <line>`, a changed one with `config.git: the host key of <host> does not match host-keys (it may have been rotated, or this may be an attack); verify it out of band before replacing the line with: <line>`.
- **No PR and no branch links.** A plain repo has no hosting API or web pages: the branch bar has no **PR** button and no links; Merge, or a merge in git, lands a branch.
- **Only here.** Local paths and SSH are allowed only in `config.git`, which only the operator edits. Kit sources (`kits/<n>.source.yml`, `kit import`) keep [kits-git.md](kits-git.md)'s rules, so a commit cannot point a kit at a file on the Jam host.

A dev Jam set up this way, step by step: [dev/README.md](../../../dev/README.md#config-a-local-repo).

For every kind, the follower, sync and the branch editor read the followed branch through Jam's bare clone at `<state-dir>/config-repo`. A symlink or submodule in the config dir is refused (400 `config repo: <path> is a symlink (or submodule); the config tree holds only regular files`).

## Following

The **follower** checks the branch now and every minute (`ls-remote`) and:

| State | When | Does |
|---|---|---|
| `awaiting-confirm` | No commit from this repo has been applied yet: a new Jam, or the repo setting changed to another repo, branch or dir. | Nothing. `config.follow` (info) asks for the confirm. |
| `in-sync` | The head is the commit last applied. | Nothing; `config.follow` clears. |
| (applies) | A new head. | Applies it. The repo always wins: an object changed in Jam since (a kit from its own source that moved, say) is updated to the repo's copy. |
| `failed` | The check or the apply failed: a fetch or auth error, a refused plan. | Keeps the last good config. `config.follow` (warning) carries the error; the next check retries, even at the same head. |

**Confirming the first apply.** Review the plan and apply it once:
`config sync` (or **Diff**, then **Apply this plan**, on Config › Tracking, [ui-config.md](ui-config.md)).
That apply records the repo, commit and time; from then on the follower applies
on its own. The record is not part of `jam export`: a restored Jam asks again.

## Verbs

| Command | Does |
|---|---|
| `config sync [--yes]` | Apply the followed repo now: print the plan, prompt `Apply N change(s)? [y/N]` (unless `--yes`), apply. The first one is the confirm. |
| `config status` | The follower's last check: state, source, head, applied commit, when, error. |
| `config diff --git <src>` | Plan any source once; nothing is applied or recorded. |
| `config diff` (no flag) | Plan the followed repo; exit 1 with `no config repo is configured` if there is none. |

`--git` and `--dir` are mutually exclusive (exit 2). A git run prints
`config <src> @ <commit[:7]>` first, then one `kit-source  <path>  <src> @
<commit[:7]>` line per kit reference. An apply of the followed repo (the
follower's, or `sync`) records the commit and time, in the same transaction
as the plan. A sync whose repos moved without changing anything is
all `unchanged`: no new version, mark or audit entry, but the recorded commits
advance.

## Kit references: `kits/<name>.source.yml`

```yaml
# kits/api.source.yml
kind: kit-source
version: 1
git: https://github.com/acme/kits//api@main
```

- **Fetching.** Every diff and apply fetches the referenced kits before
  planning, `--dir` uploads included (with the serve's `kits.git` credentials).
  Each distinct repo and ref is fetched once, so two kits from one repo and ref
  share a commit. A tree may name at most 16 distinct kit sources, and one
  run's fetches share a 4-minute limit, counted from when the run starts
  (not while it waits behind another config run).
- **Planning.** The kit and its path parents in its repo are planned
  like inline kits (create, update, adopt, delete) and become managed.
  Each tracks its repo at its own dir: `{repo, dir, ref, commit}`, also recorded
  on every new version. A registry parent (`base: {kit: base}`) may be an
  inline kit of the config repo.
- **Canonical form.** A kit's canonical form is its config plus its tracked
  `repo//dir@ref`. A `kit sync` to content the config repo lacks, or a
  `kit untrack`, therefore shows as an `update` on the next config diff, and
  the next apply puts the repo's copy back. A `kit sync` to the same content
  changes nothing.
- **Inline kits are untracked.** An inline kit (`kits/<n>/kit.yml`) applied
  from git records `{config repo, <config dir>/kits/<n>, ref, commit}` and
  tracks nothing: the config repo owns it.
- **Writing.** `config write` renders every tracked kit as
  `kits/<n>.source.yml` and removes the managed kit's other form. A kit pinned
  to such a kit (`web@v3`) cannot be written: pin it by name (`web`).

## Refusals and errors

- `kits/<n>.source.yml: git: source: …`: an unusable source. A pasted
  credential URL is refused this way and never echoed.
- `kits/<n>.source.yml: kit <n> is also kits/<n>/kit.yml`: keep one form.
- `kits/<n>.source.yml: <fetch error>`: the fetcher's redacted message
  ([kits-git.md](kits-git.md#errors-and-troubleshooting)). This is a refusal,
  never a delete.
- `kits/<n>.source.yml: kit <a> is also <file>`: two references load
  different kits under one name.
- `kits/<n>.source.yml: kit <n>'s base.kit <ref> pins a version; …`: versions are per-Jam.
- `config source: <dir> is not a directory in the repo` (400): `//dir` is
  missing, a file or a symlink.
- `kits: N kit sources (distinct repo and ref); at most 16 per config repo`:
  too many kit references in one tree. A plan refusal (diff shows it, apply
  writes nothing), not a 502.
- `no config repo is configured (config.github, config.gitlab or config.git in the serve config)` (409).

Logs name a source as `repo//dir@ref` (an upload: `dir`), never with userinfo.
Audit: `config applied` as for `--dir` (operator `config-follow` for the
follower's applies); a failed follow logs `config follow: failed`.

## Routes

| Route | Does |
|---|---|
| `POST /admin/config/diff` | As in config-git.md; a `{"source": "<src>"}` JSON body (`""` = the followed repo) instead of a tar. |
| `POST /admin/config/sync?plan=<digest>` | Apply the followed repo (the first apply's confirm), then refresh the follower; body `{}` (any other Content-Type is 415). |
| `GET /admin/config/follow` | The follower's status: `{state, source, head, applied, error, checkedAt}`. |

Statuses: refused (including the 16-source cap), stale or no config repo 409; bad source or dir 400; fetch,
auth, limit or timeout (`config fetch timed out`) failure 502; a serve with
no fetcher 503.

The admin console's Tracking page (Config › Tracking) shows the follower, and diffs and applies the followed repo too: [ui-config.md](ui-config.md).

## Locks

A config run holds `configMu` from its first fetch to its write, and
`kitMu` only for the plan and the write. It never takes a per-kit lock;
kit operations never take `configMu`. Two config runs therefore never
interleave, and a `kit sync` that lands between a config fetch and its plan
is planned over. The follower's applies are config runs too.
