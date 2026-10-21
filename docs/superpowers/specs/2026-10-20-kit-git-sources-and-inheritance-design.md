# Kits from git, and kits that build on kits

**Status:** design approved in brainstorming (2026-10-20); spec, pre-plan.
**Owner:** Jam service (admin-spider).
**Builds on:** the studio-kit registry ([`kits.md`](../../usage/jam/kits.md); [`2026-09-30-studio-kit-standalone-design.md`](2026-09-30-studio-kit-standalone-design.md)).

## Summary

Refining the kit-definition experience starts with two prongs. Kits can now
come **from a git repo**, imported once or tracked and synced on demand, and
each version records where it came from. Kits can also **build on other kits**:
a child inherits its parent's image and definition and layers its own on top.
A repo of related kits imports and syncs as one consistent unit. The local
author/build/test loop comes later and builds on this.

## Decisions (brainstorming record)

1. **Git: import first, tracked sources on top.** `kit import` is one-shot;
   `kit track` records a source that `kit sync` re-resolves.
2. **Inheritance is both image and definition, as layers.** A child builds
   `FROM` its parent's image and merges its parent's definition.
3. **Version references.** A child follows its parent's `current` by default;
   `@vN` pins. `kit rm` and `kit pin` on a parent are refused while children
   reference it, as they already are while roles do.
4. **Jam fetches.** The fetch runs on the Jam side, so import and sync share
   one fetcher. It uses generic git smart HTTP and covers GitHub, GitLab and
   plain HTTP git.
5. **Sync is manual only.** `kit sync` or the UI's Sync button. A poll that
   only raises an attention condition is future work.
6. **A repo is a unit.** A parent named by a relative path resolves in the
   same checkout at the same commit, and the child is pinned to that parent
   version.

## 1. Git sources

**Source:** `<repo-url>[//<dir>]@<ref>`. `<repo-url>` is an `https://` git URL
(plain `http://` only for loopback or test hosts). `<dir>` defaults to the repo
root. `<ref>` is a branch, a tag or a full commit SHA. In `<dir>`, Jam reads
**`kit.yml`**, in today's studio-kit format (`kind: studio`). Its `context-dir`
and note `file:` paths resolve inside the checkout. A path that leaves the
checkout (`..` past the root, absolute paths, or symlinks out) is refused. All
current limits apply unchanged: encoded context ≤ 1 MiB, decompressed ≤ 64 MiB,
≤ 2000 entries, prompt ≤ 800 bytes, notes ≤ 20.

**Verbs** (admin API, `at-jam`, and buttons on the kit page):

| Verb | Effect |
|---|---|
| `at-jam kit import <name> --git <source>` | Fetch, validate and push a new version once. No source is recorded on the kit. |
| `at-jam kit track <name> --git <source>` | Record `<source>` as the kit's tracked source, then import. |
| `at-jam kit untrack <name>` | Forget the tracked source. Versions are kept. |
| `at-jam kit sync <name>` | Re-resolve the tracked ref. Push a version only if the commit moved *and* the resulting kit differs; otherwise answer `unchanged` (as `push` does today). |

`<name>` defaults to `kit.yml`'s `name:`, then to the directory name. Admin
routes: `POST /admin/kits/{name}/import` `{source}`; `PUT` and `DELETE
/admin/kits/{name}/source`; `POST /admin/kits/{name}/sync`. Each returns the
existing `KitResult{name, version, unchanged}` plus `commit`.

**Provenance:** every version stores `source`: either `{repo, dir, ref,
commit}` or `local` (a `push` from a file). A kit's tracked source is stored on
the kit, not on a version. `kit show` and `kit versions` display both.

**Fetcher:**
- A shallow, single-ref fetch over git smart HTTP using a pure-Go client
  (go-git), into a temporary directory that is always removed afterwards.
- Limits: 50 MiB transferred and 60 s per fetch.
- Fetches of the same repo are serialized.
- A failed fetch, parse or validation is a clear error and changes nothing.

**Credentials** (serve config):
```yaml
kits:
  git:
    - { url: "https://github.com/acme/", cred: gh-kits }
    - { url: "https://gitlab.example.com/", cred: gl-kits }
```
- `cred` names a demanded, plain static credential supplied by
  `credentials.yml`. It is sent as HTTP Basic (`x-access-token:<token>` for
  GitHub, `oauth2:<token>` for GitLab, or `user:token` if the value contains a
  colon).
- The longest matching prefix wins. With no match, the fetch is anonymous.
- Tokens stay in memory and never appear in logs, errors or provenance. Repo
  URLs with embedded userinfo are refused.

**Audit:** `kit imported` / `kit synced` (kit, repo, dir, ref, commit,
version, outcome), logged with no token.

## 2. Kits building on kits

**Syntax:** `base: { kit: <ref> }`, a new `base` variant.
- **The parent reference:** `<ref>` is `<name>`, which follows the parent's
  `current`; `<name>@vN`, which pins; or a relative path (§3).
- **The child's own build context:** it may also carry `context-dir`,
  `context` or `context-files`. Its Dockerfile builds
  `FROM ${COVE_BASE_IMAGE}`, which then resolves to the parent's image.
- **No context:** the child reuses its parent's image as is and only adds
  definition.

**Image build.** The chain resolves bottom-up to its **root**, a kit whose base
is the blessed default, a gated `image:`, or a plain context. Each kit's
**base stage** is the image before harness and hardening. Each child base stage
builds `FROM` its parent's base stage. The harness and the sealed hardening
layer are applied **once, last**, on the final kit's base stage, so a parent can
never weaken or duplicate hardening. The provenance gate applies to the root, as
it does today. Base stages are cached by their own digest, so siblings share a
parent's build.

**Definition merge.** Parent first, then child:

| Field | Rule |
|---|---|
| `egress` | union |
| `mcp-servers` | merged by name; a child entry replaces the parent's entry of the same name |
| `secrets` (declarations) | union by name; the child's description wins |
| `build-args` | each kit's args apply to its own base-stage build only |
| `prompt`, `notes` | layered in order (root first) within the session context's existing kit-layer budget; overflow spills to leaves as today |

**Versions and digest.**
- **Recorded parent version:** each child version records the parent version
  it was pushed against.
- **Build-time resolution:** a following parent resolves to its `current` at
  build and raise time; a pinned parent stays fixed.
- **`kit show`:** displays the resolved chain and the merged definition (`--own`
  shows only the kit's own fields), e.g.
  `follows web-dev (now v5, pushed against v3)`.
- **Digest:** `BuildDigest` covers the whole resolved chain. A change to a
  following parent therefore marks running children `image=stale`, and
  `standing upgrade` applies it. A pinned parent's later versions do not affect
  the child.

**Guards.**
- **On push:** an unknown parent, a cycle, or a chain deeper than 4 kits is
  refused.
- **On a parent:** `kit rm` and `kit pin` are refused while children reference
  it, and the error lists those children.
- **Roles:** a role binds to a child like any other kit.

## 3. Same-repo parents ("a repo is a unit")

A `base.kit` value that starts with `.` is a **path**. It resolves relative to
the child's directory, in the same checkout at the same commit, and may not
leave the checkout. Importing a kit whose ancestors are path references works
as follows:

1. **Ancestors first, from the same commit.** Each path parent is imported,
   recursively and in topological order. Its registry name comes from its
   `kit.yml` `name:`, then from its directory name.
2. **Then the child, pinned.** The child is pushed pinned to the exact parent
   version that commit produced.
3. **`track`:** records the source on the child and each path parent.
   **`sync`:** re-resolves the child's ref and syncs the ancestors at the new
   commit first. Each answers `unchanged` when it did not move.
4. **Refusals:**
   - a path parent whose registry name already belongs to a kit with a
     *different* tracked source, or with any `local` version newer than its
     last import from this source, is refused (no overwrite);
   - a path that leaves the checkout is refused.
5. **Atomicity:** a failure on an ancestor aborts the whole import or sync, and
   no version is pushed for any kit in the set.

A parent outside the repo, such as a shared `web-dev` kit owned elsewhere, is
referenced by name and follows or pins as in §2.

## 4. Surfaces

- **Kit page:**
  - the source (repo, dir, ref and current commit, linked)
  - **Sync**, **Track** and **Untrack** buttons (htmx, `originGuard`)
  - **Built on:** the parent chain, with version and follow/pin state
  - **Used by:** roles and child kits
- **`at-jam kit versions`:** gains `SOURCE` and `COMMIT` columns.
- **`at-jam kit show`:** the merged view by default; `--own` for the kit's own
  fields only.

## 5. Errors

Errors are specific and actionable and never contain tokens. For example:

- `ref main not found in https://…`
- `auth failed for https://github.com/acme/ (check kits.git cred gh-kits)`
- `kit.yml not found in <dir> at <commit>`
- `parent kit2 belongs to another source (https://…)`
- `kit chain too deep (max 4): a → b → c → d → e`
- `cycle: a → b → a`
- `context over 1 MiB after packing <dir>/image`
- `fetch exceeded 50 MiB`

## 6. Testing

- **Fetcher (hermetic):** an in-process smart-HTTP git server (go-git server
  side, or `git http-backend` behind the `integration` tag). Covers branch,
  tag and commit refs, `//dir`, Basic auth, the size and time limits, a missing
  ref, refusal of escaping paths, and no token in errors or logs.
- **Chain resolver and merge (pure functions):** union, override by name,
  prompt and notes layering, cycles, depth, follow vs pin.
- **Build planning:** chain build order; base-stage reuse; hardening applied
  exactly once; a change to a following parent alters the child's digest and a
  later version of a pinned parent does not.
- **Same-repo import:** a two- and three-kit repo imports as a pinned set;
  re-sync is `unchanged`; a foreign-source name collision is refused; an
  ancestor failure leaves the registry untouched.
- **Guards:** `rm` and `pin` on a parent with children are refused.
- **Integration:** a real `docker build` of a two-level chain, gated like the
  existing launcher integration tests.

## Slices

1. **Git sources:** `kit import|track|untrack|sync`, the fetcher,
   `kits.git` credentials, provenance, kit-page source panel, docs.
2. **Kits on kits:** `base.kit` by name (follow or pin), chain build and
   base-stage cache, merge rules, digest and staleness, guards, *Built on* /
   *Used by*, docs.
3. **Same-repo path parents:** "a repo is a unit" import and sync, docs.

## Future work

- **Source-behind poll:** a periodic `ls-remote` that only raises an attention
  condition `kit.source.behind:<name>` (info) and never syncs.
- **Webhooks:** GitHub and GitLab push hooks.
- **Context limit:** a larger build-context limit for git sources, which would
  need blob storage instead of inline base64.
- **Authoring loop:** the local kit author/build/test experience.

## Out of scope

- SSH git URLs.
- Submodules.
- Git LFS.
- Signed-commit verification.
- Multiple inheritance (a kit with more than one parent).
