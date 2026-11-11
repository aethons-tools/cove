---
name: docs-navigate
description: Find and read the *minimum* documentation needed to do a task, in a repo whose docs are progressively disclosed. Use this skill whenever you need information from the repo's docs to answer a question, implement a change, or understand a system — i.e. any time you'd otherwise be tempted to open lots of doc files or grep the docs/ tree. Routes from the docs map (docs/INDEX.md, or an Antora book's start page), opens only matching docs, and stops early. Use it even when the user doesn't say "read the docs," as long as the answer plausibly lives in them.
---

# docs-navigate

Read docs the way the system is built to be read: top-down, minimal, early-exit.
The goal is to load the fewest tokens that fully answer the task — not to be
thorough by reading everything.

## Protocol

1. **Load only the map.** Find it this way (the repo's `AGENTS.md` usually
   names it — trust that first):
   - `docs/antora.yml` exists → the docs are an Antora book; the map is the page
     its `start_page` names, under `docs/modules/ROOT/pages/` (e.g.
     `start_page: INDEX.adoc` → `docs/modules/ROOT/pages/INDEX.adoc`).
   - otherwise → `docs/INDEX.md`.

   Do not glob, grep, or open the `docs/` tree wholesale — that defeats the
   entire system and burns context on docs you don't need.

2. **Match your task against the `read_when` column.** Each row says *when* to
   open that doc, phrased as a reader's situation. Pick only the rows whose trigger
   matches what you're actually doing right now.

3. **Open the smallest matching set.** Usually one doc. If two genuinely match,
   open both, but resist opening "related-looking" docs on spec — their triggers
   would have matched if they were relevant.

4. **Read top-down and stop early.** Every doc front-loads its purpose and key
   content. The moment you have what the task needs, stop. You do not have to
   reach the end of a doc.

5. **Follow a `prereq` or in-body link only if you actually hit a gap.** Links are
   there for when you need them, not to be pre-fetched. One hop at a time.

## Following links

- Markdown: `[text](path.md)` is a path relative to the current file.
- AsciiDoc in an Antora book: `xref:usage/x.adoc[]` is the file
  `docs/modules/ROOT/pages/usage/x.adoc` — always relative to the module's
  `pages/` directory, not to the current file. `xref:mod:x.adoc[]` names
  module `mod`: `docs/modules/mod/pages/x.adoc`. `xref:x.adoc#id[]` and
  `<<id>>` point at the heading or `[#id]` anchor `id`.
- AsciiDoc outside Antora: `xref:` and `link:` targets are relative to the
  current file.
- Metadata is the YAML frontmatter (`.md`) or the `:read_when:`-style header
  attributes under the `= Title` (`.adoc`).

If the repo publishes its docs as a site with search, that site is the human
view; you read the files.

## If the map doesn't resolve your task

- **No row matches** → the doc may not exist yet. Say so rather than guessing or
  reading unrelated docs. If you're about to create the missing knowledge, switch
  to the **docs-author** skill.
- **A row matches but the doc is thin, stale, or wrong** → answer with what's
  there, flag the gap, and consider fixing it via **docs-author**.
- **Several rows look plausible and you can't tell them apart** → the triggers are
  weak; that's an authoring bug. Open the most likely one, and note the ambiguity
  so it can be fixed.

## Why it works this way

Front-loaded triggers and a pure-map root mean you can decide what to read
*before* paying to read it. Reading the whole tree "to be safe" is the failure
mode this system exists to prevent: it's slow, it fills context with noise, and it
makes you act on stale or duplicated copies instead of the single source of truth.

The full rules behind this (tiers, budgets, the metadata schema) are in
`/agent-data/reference/progressive-disclosure.md` — but you rarely need them
just to read.
