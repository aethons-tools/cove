---
summary: Parents in the same repo — a git kit's `base.kit: ../web` path parent, how import/track/sync bring a repo's kit set in as one unit (root first, each child pinned to the exact `name@vN` its parent got), naming, atomicity, the set refusals and their messages, locking, and how a set shows in the CLI and the kit page.
read_when: A kit in a git repo builds on another kit in the same repo (`base: { kit: ../web }`), you are importing, tracking or syncing such a kit and want to know which kits it writes and at which versions, a set import was refused (not from this source, local push newer, leaves the checkout, no kit.yml, cycle, too deep, name used twice), or a kit push said `path parents resolve only in a git import`.
owns: the kit-set story — path-parent syntax and resolution, set naming/order/pinning, the stored name@vN rewrite, set atomicity, set track/sync, the set refusals and their texts, set locking and its accepted race, the `also` result, the CLI ancestor lines, the UI flash suffix and the kit page's set note
prereqs: kits-inheritance.md for `base.kit`, follow vs pin and the chain rules; kits-git.md for sources, import vs track/sync and provenance
tier: leaf
updated: 2026-10-24
---

# Parents in the same repo

A kit in a git repo can build on another kit **in the same repo** by path. The
repo is then a unit: importing, tracking or syncing the child brings in its
path ancestors from the same commit, as one **kit set**.

```
kits/                         # https://github.com/acme/kits
  web/kit.yml                 # name: web-dev   (or no name: → "web")
  app/kit.yml                 # base: { kit: ../web }
```

```
$ at-jam kit track app --git https://github.com/acme/kits//app@main
  web-dev v1 (with app)
imported app v1 from https://github.com/acme/kits//app@main (0123456789ab)
tracking https://github.com/acme/kits//app@main
```

## The path reference

- A `base.kit` value that starts with `.` is a **path**. Any other value is a
  registry name and follows or pins as in
  [kits-inheritance.md](kits-inheritance.md#the-parent-reference).
- It resolves relative to the child's directory in the checkout, after
  `path.Clean`. It must stay inside the checkout, and the target directory
  must hold a `kit.yml`.
- Syntax: a non-empty relative slash path of at most 200 bytes, with no
  backslash. Otherwise the kit is invalid: `base.kit: not a valid parent path
  (…)`.
- Paths resolve only in a git `import`, `track` or `sync`. `kit push` of a
  file with a path parent is refused: `path parents resolve only in a git
  import (kit import/track --git)`.
- A set's root may itself name a registry parent (`kit: web-dev`), so a set
  can sit on a kit owned elsewhere. The whole chain, set and registry
  ancestors together, is still capped at 4 kits.

## Names, order and pinning

- **Names:** the top kit is named as in any import
  ([kits-git.md](kits-git.md#the-kit-in-the-repo)). A path parent is named by
  its `kit.yml` `name:`, else its directory's base name (the repo's name for
  the repo root). Two kits in one set may not share a name.
- **Order:** the set is imported **root first**, the top kit last.
- **Pinning:** each path reference is **stored** as `<name>@v<N>`, rewritten
  in place in the stored `kit.yml`: only that value changes, comments and
  layout stay. `base.kit` must be a one-line plain or quoted value. `<N>` is
  the version that commit produced for the parent: its new version, or its
  current one when its directory is unchanged. `kit show app --own` therefore shows
  `kit: web-dev@v1`, not `../web`, and the child pins its parent exactly.
- **Unchanged:** each kit reports `unchanged`, and gets no new version, when
  its directory (after the rewrite) is identical. A changed child re-pinned to an
  unchanged parent gets a new version; the parent keeps its version. A changed
  parent always gives the child a new version too, even with identical child
  text, because its stored `@vN` moved.
- **Provenance:** each version written records its own repo, **its own dir**,
  the ref and the commit ([kits-git.md](kits-git.md#provenance)).

## Atomicity

The whole set is validated first: each kit against the set's pending versions,
including the merged prompt and notes budgets. Then every changed kit is
written in one store write. If any check fails, **nothing** is written, not
even ancestors that were valid on their own.

## Track and sync

- **`track`** sets the tracked source on every kit in the set, each with its
  own dir (`web-dev` tracks `…//web@main`).
- **`sync app`** resolves app's ref. If it moved, the whole set is re-imported
  at the new commit, and every tracked commit advances. If it did not move,
  and app's current version came from that source, nothing is fetched: the
  result is `app unchanged` with no ancestor lines, and the ancestors are not
  checked.
- **`sync web-dev`** imports web-dev, with its own path parents, alone.
- **`import`** of a set tracks nothing new. An ancestor that already tracks
  exactly this repo, dir and ref has its tracked commit moved along.

## Refusals

These apply to the path **ancestors** only. The top kit is imported as any
git import is ([kits-git.md](kits-git.md#import-vs-track-and-sync)). The admin
API answers **409** for the first two and **400** for the rest.

| Message | Means |
|---|---|
| `kit web-dev exists and is not from <repo>//web; rename the kit or untrack it first` | The name belongs to a kit that tracks another repo or dir, or that has never been imported from this repo and dir. That kit is untouched. |
| `kit web-dev has a local push newer than its last import from <repo>//web; re-push or untrack it first` | A `kit push` came after its last import from here. |
| `parent path ../x (of app) leaves the checkout` | The path climbs out of the repo. |
| `parent path ../x (of app) has no kit.yml` | No readable `kit.yml` there, or it resolves outside the checkout. |
| `cycle: app → web-dev → app` | The paths loop. |
| `kit chain too deep (max 4): …` | More than 4 kits. |
| `kit name web-dev is used by both web and web2 in this set` | Two directories give the same name; rename one with `name:`. |

"From this source" means the same repo and dir, at **any ref**. So importing a
set through another ref (`…//app@v2`) adds versions from that ref to
ancestors imported from `main`. An ancestor already tracking that repo and dir
at a **different ref** keeps its tracked ref: a track or sync through another
ref gives it a new version with its own provenance but does not retarget it.

A parent path and the dirs it reaches follow the source-dir rules: no space,
quote, control or non-printable character, or the path is refused with a fixed message.

## Concurrency

An import, track or sync holds one kit lock, its top kit's, across the fetch.
The set's resolve and write run under Jam's single kit-write lock. **Accepted
residual:** a sync of `app` and a sync of `web-dev` that fetch different
commits can leave `web-dev` at the older commit's content. Its next sync puts
it right.

## Where a set shows

- **Admin API:** the import, track or sync result gains `also`, the ancestors
  imported or checked with the kit (`name`, `version`, `unchanged`, `commit`),
  root first. It is absent for an unchained kit and for a sync that fetched
  nothing.
- **CLI:** one indented line per ancestor, root first, before the kit's own
  line: `  web-dev v2 (with app)`, or `  web-dev unchanged (v1)`.
- **Kit page:** the track and sync flash ends with `With: web-dev v2` (adding
  `(unchanged)` per ancestor). The Source card notes `Part of a set with
  web-dev — imported from the same repo.` It lists the chain parents, of the
  viewed version, whose resolved version came from the same repo at another
  dir ([ui-pages.md](ui-pages.md#kit-pages)).

The design is in
[`../../superpowers/specs/2026-10-20-kit-git-sources-and-inheritance-design.md`](../../superpowers/specs/2026-10-20-kit-git-sources-and-inheritance-design.md)
§3.
