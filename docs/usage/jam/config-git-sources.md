---
summary: A Jam following a config repo in git — `config track|sync|untrack`, `config diff|apply --git`, kits referenced by `kits/<name>.source.yml`, provenance, locks, routes and errors.
read_when: You want a Jam to follow a config repo (track, sync, untrack, `--git`), are referencing a kit by its own git source (`kits/<name>.source.yml`), or a config sync failed to fetch, refused a source, or flagged a source kit as drifted.
owns: the tracked config source (record, sync, untrack), the `--git` verbs, the `/admin/config/source|sync` routes and `{source}` bodies, `kits/<name>.source.yml` and how it is fetched, planned and written, config-applied kit provenance and tracking
prereqs: config-git.md (layout, plan actions, marks); kits-git.md (source syntax, kits.git credentials, fetch limits)
tier: leaf
updated: 2026-10-22
---

# Config from git: the tracked source and kit references

A Jam can follow **one** config repo in git. Sources use the kit syntax,
`<repo>[//<dir>]@<ref>` ([kits-git.md](kits-git.md#the-source-repodirref)),
and the same fetcher and `kits.git` credentials. Jam's git access stays
read-only: `config write` still fills your checkout ([config-git.md](config-git.md)).

## Verbs

| Command | Does |
|---|---|
| `config track --git <src>` | Fetch and plan `<src>`, print the plan, and record `<src>` as the Jam's config source, even when the plan has refusals. A fetch failure records nothing. Re-tracking the same source keeps its last commit; another source replaces it. |
| `config sync [--yes] [--force]` | `apply` from the tracked source: print the plan, prompt (unless `--yes`), apply. |
| `config untrack` | Forget the source. Objects and their managed marks stay. |
| `config diff\|apply --git <src>` | Plan or apply any source once, without tracking it. |
| `config diff\|apply` (no flag) | Use the tracked source; exit 1 with `no config source is tracked` if there is none. |

`--git` and `--dir` are mutually exclusive (exit 2). A git run prints
`config <src> @ <commit[:7]>` first, then one `kit-source  <path>  <src> @
<commit[:7]>` line per kit reference. An apply from the tracked source
records the commit and time on it, in the same transaction as the plan.
That covers `sync`, a no-flag `apply`, and `--git` naming the same repo, dir
and ref. A sync whose repos moved without changing anything is all
`unchanged`: no new version, mark or audit entry, but the recorded commits
advance. The tracked source, like the marks, is **not** part of
`at-jam export`: a restored Jam tracks nothing until `config track`.

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
- `no config source is tracked (use config track --git <source>, or --dir)` (409).

Logs name a source as `repo//dir@ref` (an upload: `dir`), never with userinfo.
Audit: `config tracked` (operator, source, commit), `config untracked`
(operator, source), and `config applied` as for `--dir`.

## Routes

| Route | Does |
|---|---|
| `POST /admin/config/diff`, `/apply` | As in config-git.md; a `{"source": "<src>"}` JSON body (`""` = the tracked source) instead of a tar. |
| `PUT /admin/config/source` | `{source}`: fetch, plan (the answer) and track it. |
| `DELETE /admin/config/source` | Untrack (204, also when none). |
| `POST /admin/config/sync?force=1&plan=<digest>` | Apply the tracked source; body `{}` (any other Content-Type is 415). |

Statuses: refused (including the 16-source cap), stale or untracked 409; bad source or dir 400; fetch,
auth, limit or timeout (`config fetch timed out`) failure 502; a serve with
no fetcher 503.

The admin UI's Config tab tracks, untracks, diffs and applies the tracked source too: [ui-config.md](ui-config.md).

## Locks

A config run holds `configMu` from its first fetch to its write, and
`kitMu` only for the plan and the write. It never takes a per-kit lock;
kit operations never take `configMu`. Release and untrack take it too, so they wait behind a running apply or sync, up to the 4-minute fetch deadline. Two config runs therefore never
interleave, and a kit push that lands between a config fetch and its plan
shows as drift.
