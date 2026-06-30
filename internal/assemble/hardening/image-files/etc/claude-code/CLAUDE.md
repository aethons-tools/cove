# Documentation rules for agents

Repositories on this machine keep their docs **aggressively progressively
disclosed**: a tiny map at the top, all detail deferred behind pointers loaded
only when a task needs them. Keep them that way.

## Always hold these invariants

1. A repo's `docs/INDEX.md` is a map, not a manual — one row per doc, no prose.
2. Every doc's frontmatter says **what it is** and **when to read it**, up front.
3. Every fact lives in exactly one doc; everything else links to it. Never copy.
4. Every doc is reachable from `INDEX.md`; every link resolves.

## How to work with docs

- **Reading docs to do a task** → use the **docs-navigate** skill. Start at
  `docs/INDEX.md`, open only the rows whose "read when" matches your task, stop
  when you have enough. Never glob-read the tree.
- **Adding or changing docs** → use the **docs-author** skill. Find the doc that
  *owns* the subject and edit there; create a new leaf only if none owns it; update
  `INDEX.md` in the same change.
- **Checking docs health** → use the **docs-audit** skill before merging doc
  changes. It runs a deterministic checker for orphans, dangling links, oversize
  docs, and duplication.

The full doctrine — tiers, size budgets, the frontmatter schema, when to
split/merge — lives at `/etc/claude-code/reference/progressive-disclosure.md`.
Read it before authoring or restructuring; you don't need it just to read docs.

# Sandbox rules for agents
@SANDBOX.md