---
summary: Kits from git — importing a studio kit from a repository (`kit import`), tracking it and pulling new commits by hand (`kit track|sync|untrack`), the `<repo>[//<dir>]@<ref>` source syntax, version provenance, `kits.git` credentials, the fetch limits, and what each fetch error means.
read_when: You want a kit's definition to live in a git repo instead of a file you `kit push`, are writing a `--git` source, giving Jam a token for a private kit repo, wondering why a sync said `unchanged`, or an import/track/sync failed (auth, ref or kit.yml not found, escape refused, limit exceeded).
owns: the git kit-source story — the source syntax and its refusals, the in-repo kit.yml layout, import vs track/sync semantics and the unchanged rules, version provenance in `versions`/`show`, kits.git credential matching + username rules + anonymous fetch, the fetch limits, the kit-source admin API status codes, and kit-source troubleshooting
prereqs: kits.md for the StudioKit schema, versions and the `kit` verbs; serve.md for where the `kits` block sits in the serve config
tier: leaf
updated: 2026-10-21
---

# Kits from git

Instead of `kit push`ing a file, a kit can be **imported from a git
repository**: Jam fetches the repo itself (a shallow, read-only checkout over
https), reads one `kit.yml`, and records it as a new version of the kit — with
the repo, dir, ref and commit as that version's **provenance**. A kit can also
**track** a source, so `kit sync` pulls the newest commit of its ref when an
operator asks. Nothing polls: syncs are manual.

```
at-jam kit import --git https://github.com/acme/kits//web@v1.4 [--name web]   # once, not tracked
at-jam kit track web --git https://github.com/acme/kits//web@main             # import + track
at-jam kit sync web                                                          # re-import the tracked source
at-jam kit untrack web                                                       # stop tracking; versions kept
```

The admin UI does the same from the Kits list (**Import from git**) and each
kit's **Source** card ([ui-pages.md](ui-pages.md#kit-pages)).

## The source: `<repo>[//<dir>]@<ref>`

- **`<repo>`** — the clone URL: `https://`, or plain `http://` only to a
  loopback host (`localhost`, `127.0.0.1`, `[::1]`). The host must be a
  **dotted DNS name or loopback**: a single-label intranet host
  (`https://gitserver/…`) and a non-loopback IPv6 literal are refused — use the
  FQDN. No userinfo, query, fragment or `.`/`..` segments; scheme and host are
  lowercased.
- **`//<dir>`** — optional: the directory holding `kit.yml`, relative to the
  repo root (a clean path; `//.` or none means the root).
- **`@<ref>`** — a branch, a tag (annotated tags are peeled), or a full
  40-hex commit id (no short ids). A name that is both a branch and a tag
  means the branch.

| Source | Means |
|---|---|
| `https://github.com/acme/kits//web@main` | GitHub, dir `web`, branch `main` |
| `https://gitlab.example.com/platform/kits.git//studio/api@v2.0` | GitLab, nested dir, tag `v2.0` |
| `https://git.example.org/kits.git@0123…cdef` (40 hex) | a plain git server, kit at the root, one commit |
| `https://github.com/acme/web-kit@release/2026-10` | a ref with `/` (needs a portless host, below) |

**Credentials never go in the source** — `https://user:token@host/…` is
refused (put the token in [`kits.git`](#credentials-kitsgit)). Because a
source is typed text that may be a pasted clone URL with a token in it, the
parser also refuses shapes that could be one: more than one `@`; a host with
an explicit port, or a ref whose first segment looks like a host name, when
the ref contains `/` (so slash branches on a ported server must be named by
tag or commit). Its errors are fixed text and **never echo the input**, so a
mistyped secret is not repeated back. One shape can't be told apart: a
dotted head — a token or a user name such as `john.doe` — read as the host,
as in `https://john.doe/SECRET@…`. That is accepted as a repo URL and later
quoted in its fetch error, so paste clone URLs only without credentials.

## The kit in the repo

`<dir>/kit.yml` is an ordinary [StudioKit](kits.md#the-studiokit). Paths in
it — `base.context-dir` and each note's `file` — are relative to `<dir>` and
must **stay inside the checkout**: an absolute path, a `..` escape, or a
symlink out of the checkout is refused. `context-dir` is packed into `context`
exactly as `kit push` packs it (same caps, `.dockerignore` honored), so the
stored kit is self-contained; the checkout is deleted after each fetch.

A `kit.yml` may build on a registry kit (`base: { kit: web-dev }`,
[kits-inheritance.md](kits-inheritance.md#kits-from-git)) or on another kit in
the same repo by path (`base: { kit: ../web }`, imported with it as one set:
[kits-path-parents.md](kits-path-parents.md)).

The kit's name, when `import` gets no `--name`: the file's legacy `name:`
field, else the base of `<dir>`, else the repo's name (`kits.git` → `kits`).
`track` and `sync` use the kit named on the command line.

## Import vs track and sync

- **`import`** records the fetched kit as a new version and makes it current
  — or, when it equals the current version, reports `web unchanged (current
  v3, <commit>)` and adds nothing (the current version keeps its provenance).
  It does **not** track. Importing the very source the kit already tracks
  (same repo, dir and ref) does move the tracked commit along.
- **`track <name>`** is `import` that also records the source, at the fetched
  commit, as the kit's tracked source (one per kit; tracking another replaces
  it).
- **`sync <name>`** re-imports the tracked source. It first resolves the ref
  (no checkout); when the ref still points at the last synced commit **and**
  the current version came from the same repo, dir and ref (at any commit),
  it fetches nothing and prints `web unchanged (<commit>)`. So pinning a
  tracked kit back to an older version (`kit pin`) and then syncing reports
  `unchanged` and keeps the pin until the next upstream commit. Otherwise it fetches and imports the new commit
  (identical content reuses the current version) and advances the tracked
  commit. So after a `kit push` replaced current, `sync` brings the
  source's content back as a new version. A source pinned to a commit id
  never changes.
- **`untrack <name>`** forgets the source; versions and their provenance stay.

Every failure leaves the kit as it was. Operations on one kit are serialized,
and fetches of one repo queue behind each other.

## Provenance

`kit versions web` lists every version with its source and commit (`local`
and `-` for a pushed one):

```
VERSION  CURRENT  PUSHED                SOURCE                                  COMMIT
v1                2026-10-01T10:00:00Z  local                                   -
v2       *        2026-10-02T10:00:00Z  https://github.com/acme/kits//web@main  0123456789ab
```

`kit show web` starts with the shown version's origin and the tracked source:

```
# source: https://github.com/acme/kits//web@main (0123456789ab)
# tracking: https://github.com/acme/kits//web@main (last synced 0123456789ab)
```

(`# source: local` for a pushed version; no `# tracking:` line when untracked.)

## Credentials (`kits.git`)

Public repos need nothing: a repo no entry matches is fetched **anonymously**.
For a private repo, the [serve config](serve.md)'s `kits.git` maps repo-URL
**prefixes** to a demanded static credential (supplied by the
[credentials file](credentials.md)):

```yaml
credentials:
  gh-kits:
kits:
  git:
    - url: https://github.com/acme/
      cred: gh-kits
```

- **Matching:** the longest prefix that matches the repo URL wins; scheme and
  host compare case-insensitively, the path exactly, and only at a `/`
  boundary (`…/acme/` never matches `…/acmeevil/`).
- **Resolution:** the credential is resolved at serve start (a broken supply
  fails serve) and again on **every fetch**, so a minted (GitHub App) or
  rotated token is always current — no restart needed.
- **Username:** `x-access-token` on github.com hosts and `oauth2` on hosts
  whose name contains `gitlab` (the whole credential value is the password
  there); elsewhere a value of `user:token` sends `user`, else `git`.
- The token stays in Jam's memory — never in a URL, `.git/config`, a log, an
  error or the admin API.

## Limits

Per fetch: **50 MiB** transferred, **64 MiB** of checked-out files, **60 s**.
A fetch (or sync) may also wait behind another fetch of the same repo, so the
CLI allows 90 s per call. The packed context then meets the usual
[`context` caps](kits.md#the-studiokit), and `kit.yml` is read only up to
4 MiB.

## Errors and troubleshooting

The CLI refuses a malformed source itself (exit 2); otherwise it exits 1
with the admin API's status and message. The API answers
**400** for a bad source or an invalid kit, **404** for an unknown kit,
**409** for `sync` of an untracked kit or a refused
[set ancestor](kits-path-parents.md#refusals), **502** for a fetch, auth or limit
failure (the message is already redacted), and **500** for a store failure.

| Message | Fix |
|---|---|
| `auth failed for <prefix>: the token was rejected (401; …)` | The named credential's token is wrong or expired. |
| `auth failed for <prefix>: the token was accepted but may not read this repo (403; …)` | Valid token without access: a fine-grained PAT must be owned by the org with **Contents: read** (and org-approved); a classic PAT needs `repo` and, under SAML SSO, SSO authorization. |
| `kits.git cred <name> could not be resolved` | The credential's supply (`credentials.yml` command, minter) failed at fetch time; check it on the Jam host. |
| `auth required for <host> (add a kits.git entry)` | A private repo with no matching prefix: add one. |
| `repository … not found (if private, add a kits.git entry)` | Wrong URL, or private (forges often answer "not found" without auth). |
| `repository … not found (or kits.git cred <name> cannot read it)` | Wrong URL, or the named credential's token cannot read this repo. |
| `ref <ref> not found in …` | No such branch or tag; check spelling and that it is pushed. |
| `commit … not found in … (or the server does not serve commits by id)` | Unknown id, or the server refuses fetch-by-commit: use a branch or tag. |
| `file "<dir>/kit.yml": is missing or resolves outside the checkout` | No `kit.yml` there (check `//<dir>`), or it is a symlink out. |
| `base.context-dir "…"` / `note "…" file "…"`: `does not resolve to a path inside the checkout` | An escape was refused: keep paths relative and inside the repo. |
| `exceeded the <n>-byte transfer limit` / `checkout too large` / `timed out after …` | The repo is too big or too slow for one fetch: move the kit to a smaller repo. |
| `kit has no tracked source (use kit track)` | `sync` needs `track` first. |

## In a config repo

A config repo can reference a kit by its source (`kits/<name>.source.yml`);
`config sync` then fetches and tracks it as a managed kit — see
[config-git-sources.md](config-git-sources.md). A push or `kit sync` to content
the config repo lacks, or a `kit untrack`, shows as drift on the next config
diff.

## Future work

Not built yet: an attention signal when a tracked source has new commits
(a poll that only reports, never imports) — see the design,
[`../../superpowers/specs/2026-10-20-kit-git-sources-and-inheritance-design.md`](../../superpowers/specs/2026-10-20-kit-git-sources-and-inheritance-design.md).
