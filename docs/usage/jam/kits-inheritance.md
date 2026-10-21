---
summary: Kits building on kits — `base: { kit: <name>[@vN] }`, following vs pinning a parent, the chain rules and errors, how a chain's definition merges, how its image builds stage by stage, staleness, the `rm`/`pin` guards, and where the chain shows (`kit show`, `kit list`, the kit page).
read_when: You want one kit to build on another (a role bound to `my-app` gets `default → web-dev → my-app`), are choosing between following and pinning a parent, a push or raise failed with a parent/cycle/too-deep error, a `kit rm` or `kit pin` was refused because of child kits, or a child studio went `image=stale` after its parent changed.
owns: the kit-inheritance story — the `base.kit` syntax and its refusals, follow vs pin, the chain cap/cycle/missing-parent errors, the merge rules, push-time checks on the merged kit, the stage-by-stage chain build and stage cache, chain staleness, the recorded raise chain, the rm/pin chain guards, and the chain in `kit show`/`kit list`
prereqs: kits.md for the StudioKit schema, versions and the `kit` verbs
tier: leaf
updated: 2026-10-21
---

# Kits building on kits

A studio kit can name another registered kit as its **parent**. Its image then
builds on the parent's image, and its definition (egress, MCP servers, build
args, secrets, prompt, notes) is merged on top of the parent's. A role bound to
the child gets the whole chain — for example `default → web-dev → my-app`.

```yaml
kind: studio
base:
  kit: web-dev            # follow web-dev's current version; or web-dev@v3 to pin
  context-files:          # OPTIONAL — this kit's own stage, built FROM web-dev's
    dockerfile: |
      FROM ${COVE_BASE_IMAGE}
      RUN apt-get update && apt-get install -y postgresql-client
egress: [api.acme.dev]
prompt: "You work on my-app …"
```

## The parent reference

- **`kit: <name>`** follows the parent's *current* version. It resolves to
  whatever is current at push, raise, build and staleness time.
- **`kit: <name>@v<N>`** pins version N (N ≥ 1). The parent's later pushes
  don't affect the child.
- **The child's own stage:** `kit` combines with at most one of
  `context-files`, `context` or `context-dir`. That context's Dockerfile builds
  `FROM ${COVE_BASE_IMAGE}`, which now resolves to the parent's stage.
- **No context:** the child reuses its parent's image as it is and only adds
  definition.
- `kit` and `image` are mutually exclusive.
- A value starting with `.` (a path parent in the same repo) is refused for
  now: `path parents are not supported yet; use the parent kit's registry name`.

**The chain** runs from the child up to its **root**, a kit whose base is not
`kit:` (the blessed default, a gated `image:` or a plain context). It holds at
most **4 kits**, counting the kit itself. These fail at push and, fail-closed,
at raise. A raise never falls back to another base:

| Error | Means |
|---|---|
| `kit web-dev (parent of my-app) is missing` | The parent isn't in the registry (or has no current version). |
| `kit web-dev has no version v9 (parent of my-app)` | A pin names a version that doesn't exist. |
| `kit web-dev@v3 (parent of my-app) is invalid: …` | The parent's stored config isn't a valid studio kit. |
| `cycle: a → b → a` | The chain loops, including a kit naming itself. |
| `kit chain too deep (max 4): …` | More than 4 kits. |

## How the definition merges

Root first, the kit itself last:

| Field | Rule |
|---|---|
| `egress` | union, sorted, deduplicated (still capped by the [ceiling](kits.md#the-egress-ceiling-cov-208)) |
| `mcp-servers` | by name; the child's entry replaces the parent's |
| `secrets` | union by name; the child's description wins |
| `build-args` | by key; the child wins |
| `prompt` | the non-empty prompts joined by a blank line, root first |
| `notes` | by name, in order of first appearance; a child's note of the same name replaces the earlier one in place |

**At push** the kit's own fields are validated as usual, then its chain is
resolved. The **merged** prompt must fit 800 bytes and the merged notes must
number 20 or fewer. Each pushed version records the parent version it was
pushed against.

The chain resolves even when the definition is unchanged. So re-pushing an
unchanged child whose parent is now missing or invalid fails with a 400; it does
not report `unchanged`.

A parent whose prompt grows after a child was pushed doesn't block the child's
raise. The session context truncates the merged prompt with its usual warning
([session-context.md](session-context.md)).

**Build args** reach the final image build (the harness and hardening layers)
and the session's `kit/tools.md`, as for an unchained kit. They are **not**
passed to any kit's own base-stage build, so a context Dockerfile can't read
them as `ARG`s.

## How the image builds

Each kit's **base stage** is its image before harness and hardening. The
launcher builds the chain from the root down:

1. The root resolves as an unchained kit does: the blessed default, a gated
   `image:`, or a context built `FROM` the blessed base.
2. Each later kit with its own context builds `FROM` the previous stage. Every
   stage it builds is provenance-gated, so the chain must still descend from
   the blessed base. A kit with no context reuses the previous stage.
3. The harness and the sealed hardening layer are applied **once**, on the last
   stage.

A stage is cached as `cove-kit-base:<32 hex>`, keyed on the parent stage and
the kit's own `base`. So siblings of one parent share its stage, and a pinned
parent's stage survives the parent's later pushes. A following and a pinned
child with identical contexts still build separate stages, because their
`base.kit` text differs.

> **Note:** a root `image:` with a mutable tag (`foo:latest`) keys its stages by
> the tag text. When the tag moves upstream, cached child stages are reused, not
> rebuilt. The same holds for an unchained kit's cached image. Pin roots by
> digest.

## Staleness and running studios

A chained kit's build digest covers every link's build-affecting fields
(`base`, `egress`, `build-args`, `mcp-servers`) in order, plus the harness.
When a parent that a kit follows changes one of those, directly or further up
the chain, its running studios show
[`image=stale`](coves.md#the-studio-verbs);
[`standing upgrade`](standing-sessions.md#upgrading-a-standing-session) moves a
standing one to the new image. A pinned parent's later versions don't make a
child stale. A pin fixes only its own link, though: if the pinned version
itself follows *its* parent, changes further up still reach the child. A
prompt-only or notes-only change upstream never makes a child stale.

A studio records the chain it was raised from (each link's id and version). Its
[refreshed session context](session-context.md#refresh) reports the running
image's tools and egress, read from that recorded chain, while its prompt and
notes come from the current chain.

## Guards

- **`kit rm <parent>`** is refused (409) while any kit's *current* version
  names it as parent, whether it follows or pins. The error lists the children
  (`kit web-dev is the parent of my-app, ops`). The role guard is unchanged.
- **`kit pin <parent> <N>`** is refused (409) while any kit's current version
  *follows* it, since those children would silently move. The error lists them;
  pin them to a version or point them elsewhere first. Children that pin a
  version don't block. A version that doesn't exist is a 404.
- A role binds a chained kit like any other kit.

## Seeing the chain

- **`kit show <name>`** prints the **effective** (merged) kit, with a header
  `# built on: default@v1 → web-dev (follows, now v5; pushed against v3)`. The
  direct parent is shown with how it's tracked; a pinned one reads
  `web-dev@v3 (pinned)`. A chained kit's effective output carries the comment
  `# effective view (base.kit flattened); use --own to edit and re-push`.
  `--own` prints only the kit's own stored YAML. A chain
  that doesn't resolve prints `# chain error: …` and falls back to the kit's own
  YAML.
- **`kit list`** adds `parent=<name>[@vN]` to a chained kit's row.
- **The kit page** shows *Built on*, *Effective* and child kits under *Used by*
  ([ui-pages.md](ui-pages.md#kit-pages)).

## Kits from git

A kit imported or tracked from git ([kits-git.md](kits-git.md)) may name a
registry parent (`kit: web-dev`). The parent is resolved, and the checks above
run, whenever an import or sync records the fetched kit (a sync that finds
nothing new fetches nothing and checks nothing). A same-repo path parent
(`kit: ../web-dev`) is a later slice. The design is in
[`../../superpowers/specs/2026-10-20-kit-git-sources-and-inheritance-design.md`](../../superpowers/specs/2026-10-20-kit-git-sources-and-inheritance-design.md)
§2–§3.
