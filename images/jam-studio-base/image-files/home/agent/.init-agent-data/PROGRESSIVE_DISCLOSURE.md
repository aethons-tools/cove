# Documentation rules for agents

Repositories on this machine keep their docs **aggressively progressively
disclosed**: a tiny map at the top, all detail deferred behind pointers loaded
only when a task needs them. Keep them that way.

Docs are Markdown (`.md`, YAML frontmatter) or AsciiDoc (`.adoc`, the same
fields as header attributes: `:summary:`, `:read_when:`, …). **The map** is
`docs/INDEX.md`, or, when `docs/antora.yml` exists (the docs are an Antora
book), the page its `start_page` names — e.g.
`docs/modules/ROOT/pages/INDEX.adoc`. A repo's `AGENTS.md` says which.

## Always hold these invariants

1. The map is a map, not a manual — one row per doc, no prose.
2. Every doc's metadata says **what it is** and **when to read it**, up front.
3. Every fact lives in exactly one doc; everything else links to it. Never copy.
4. Every doc is reachable from the map; every link (or `xref:`) resolves.

## How to work with docs

- **Reading docs to do a task** → use the **docs-navigate** skill. Start at
  the map, open only the rows whose "read when" matches your task, stop when
  you have enough. Never glob-read the tree. If the repo publishes its docs as
  a site with search, that's the human view; you read the files.
- **Adding or changing docs** → use the **docs-author** skill. Find the doc that
  *owns* the subject and edit there, in the doc's own format; create a new leaf
  only if none owns it; update the map (and an Antora book's `nav.adoc`) in the
  same change.
- **Checking docs health** → use the **docs-audit** skill before merging doc
  changes. It runs a deterministic checker for orphans, dangling links, oversize
  docs, and duplication, in either format.

The full doctrine — tiers, size budgets, the metadata schema, when to
split/merge — lives at `/agent-data/reference/progressive-disclosure.md`.
Read it before authoring or restructuring; you don't need it just to read docs.
