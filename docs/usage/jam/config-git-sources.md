---
summary: A Jam following its config repo in git (`config.github` in the serve config) — the follower that checks the branch every minute and applies new commits, the first apply's confirm, `config sync|status`, `config diff|apply --git`, kits referenced by `kits/<name>.source.yml`, provenance, locks, routes and errors.
read_when: You want a Jam to follow a config repo (or to know why it applied, or didn't apply, a commit), are confirming the first apply, running `config sync|status|diff --git`, are referencing a kit by its own git source (`kits/<name>.source.yml`), or a config sync or follow failed to fetch, refused a commit, or flagged a source kit as drifted.
owns: following the config repo (the follower, its states, the first-apply confirm, the config.follow condition, the applied-commit record), `config sync|status`, the `--git` verbs, the `/admin/config/sync|follow` routes and `{source}` bodies, `kits/<name>.source.yml` and how it is fetched, planned and written, config-applied kit provenance and tracking
prereqs: config-git.md (layout, plan actions, marks); kits-git.md (source syntax, kits.git credentials, fetch limits)
tier: leaf
updated: 2026-10-31
---

# Config from git: following the repo, and kit references

A Jam follows **one** config repo in git, named in its serve config
([serve.md](serve.md#the-serve-config)):

```yaml
config:
  github:
    url: https://github.com/acme/jam-config
    cred: jam-git-tracking   # a demanded credential that can read the repo
    branch: main             # optional; default main
    dir: jam                 # optional; the repo dir holding the config, default its root
```

Design: [config in git, edited on branches](../../superpowers/specs/2026-10-31-config-branch-editor-design.md).
Sources elsewhere use the kit syntax, `<repo>[//<dir>]@<ref>`
([kits-git.md](kits-git.md#the-source-repodirref)), and the same fetcher;
`config.github.cred` is the credential for exactly its URL. Jam's git access
is read-only for now: `config write` still fills your checkout ([config-git.md](config-git.md)).

## Following

The **follower** checks the branch now and every minute (`ls-remote`) and:

| State | When | Does |
|---|---|---|
| `awaiting-confirm` | No commit from this repo has been applied yet: a new Jam, or `config.github` changed to another repo, branch or dir. | Nothing. `config.follow` (info) asks for the confirm. |
| `in-sync` | The head is the commit last applied. | Nothing; `config.follow` clears. |
| (applies) | A new head. | Applies it, the repo winning over drift (a forced apply). |
| `failed` | The check or the apply failed: a fetch or auth error, a refused plan. | Keeps the last good config. `config.follow` (warning) carries the error; the next check retries, even at the same head. |

**Confirming the first apply.** Review the plan and apply it once:
`config sync` (or Diff and Apply on Config › Tracking, [ui-config.md](ui-config.md)).
That apply records the repo, commit and time; from then on the follower applies
on its own. The record is not part of `jam export`: a restored Jam asks again.

## Verbs

| Command | Does |
|---|---|
| `config sync [--yes] [--force]` | `apply` from the followed repo now: print the plan, prompt (unless `--yes`), apply. The first one is the confirm. |
| `config status` | The follower's last check: state, source, head, applied commit, when, error. |
| `config diff\|apply --git <src>` | Plan or apply any source once. It records nothing unless it names the followed repo, dir and branch, when an apply counts as the confirm too. |
| `config diff\|apply` (no flag) | Use the followed repo; exit 1 with `no config repo is configured` if there is none. |

`--git` and `--dir` are mutually exclusive (exit 2). A git run prints
`config <src> @ <commit[:7]>` first, then one `kit-source  <path>  <src> @
<commit[:7]>` line per kit reference. An apply from the followed repo
records the commit and time, in the same transaction as the plan.
That covers the follower, `sync`, a no-flag `apply`, and `--git` naming the
same repo, dir and ref. A sync whose repos moved without changing anything is
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
  like inline kits (create, update, drift, adopt, delete) and become managed.
  Each tracks its repo at its own dir: `{repo, dir, ref, commit}`, also recorded
  on every new version. A registry parent (`base: {kit: base}`) may be an
  inline kit of the config repo.
- **Canonical form.** A kit's canonical form is its config plus its tracked
  `repo//dir@ref`. A managed kit therefore drifts when it changes away from
  the repo: a push or `kit sync` to content the config repo lacks, or a
  `kit untrack`, shows `update … (drift)` on the next config diff. A
  `kit sync` to the same content is no drift.
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
- `no config repo is configured (config.github in the serve config)` (409).

Logs name a source as `repo//dir@ref` (an upload: `dir`), never with userinfo.
Audit: `config applied` as for `--dir` (operator `config-follow` for the
follower's applies); a failed follow logs `config follow: failed`.

## Routes

| Route | Does |
|---|---|
| `POST /admin/config/diff`, `/apply` | As in config-git.md; a `{"source": "<src>"}` JSON body (`""` = the followed repo) instead of a tar. |
| `POST /admin/config/sync?force=1&plan=<digest>` | Apply the followed repo (the first apply's confirm), then refresh the follower; body `{}` (any other Content-Type is 415). |
| `GET /admin/config/follow` | The follower's status: `{state, source, head, applied, error, checkedAt}`. |

Statuses: refused (including the 16-source cap), stale or no config repo 409; bad source or dir 400; fetch,
auth, limit or timeout (`config fetch timed out`) failure 502; a serve with
no fetcher 503.

The admin console's Tracking page (Config › Tracking) shows the follower and diffs and applies the followed repo too: [ui-config.md](ui-config.md).

## Locks

A config run holds `configMu` from its first fetch to its write, and
`kitMu` only for the plan and the write. It never takes a per-kit lock;
kit operations never take `configMu`. Two config runs therefore never
interleave, and a kit push that lands between a config fetch and its plan
shows as drift. The follower's applies are config runs too. Release takes `configMu`, so they wait behind a
running apply or sync, up to the 4-minute fetch deadline.
