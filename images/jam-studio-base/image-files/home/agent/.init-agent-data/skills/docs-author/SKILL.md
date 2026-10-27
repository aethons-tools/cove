---
name: docs-author
description: Add, edit, restructure, or split repository documentation while keeping it aggressively progressively disclosed. Use this skill whenever you create a new doc, change an existing one, document a new feature or decision, or notice docs that have grown bloated, duplicated, or hard to navigate. Enforces single-source-of-truth, size budgets, front-loaded triggers, and keeping docs/INDEX.md in sync. Use it even when the user just says "document this" or "update the README" — routing the change to the right doc is part of the job.
---

# docs-author

Write docs that stay a routing tree, not a book. Every change preserves the five
invariants in `/agent-data/reference/progressive-disclosure.md` — read that
spec first if you haven't; it owns the tiers, budgets, and frontmatter schema this
skill assumes.

## Before writing: find the home

Most "new docs" aren't new. A fact belongs in exactly one doc, and writing it
twice is the main way these systems rot.

1. Run **docs-navigate** logic: load `docs/INDEX.md`, find the doc whose `owns`
   field already covers this subject.
2. **If an owner exists → edit it.** Do not create a parallel doc.
3. **If no owner exists → create one new leaf** for the subject.
4. **If you're about to restate something owned elsewhere → link to it instead.**

## Writing or editing a leaf

- **Frontmatter first.** Fill every field (`summary`, `read_when`, `owns`,
  `prereqs`, `tier`, `updated`). `read_when` is load-bearing — phrase it as the
  reader's *situation*, not the topic. See the schema in the spec.
- **Front-load the body.** First sentence after frontmatter states what this is and
  the key takeaway. A reader must be able to bail early.
- **One subject only.** If you find yourself documenting a second subject, that's a
  new leaf, not a new section here.
- **Stay in budget** (≤ 200 lines for a leaf). Hitting the budget means *split*,
  not shrink — see below.
- **Link, don't copy.** Reference owners by relative link. If the thing you'd link
  to doesn't exist yet, create that leaf.

## Splitting an oversized or two-subject doc

1. Carve the second subject into a new leaf with its own frontmatter.
2. Replace the moved content in the original with a one-line link to the new leaf.
3. Add the new leaf's row to `INDEX.md` (or its section index).
4. If a branch now has too many leaves for `INDEX.md` to stay under budget,
   introduce a Tier-1 section index and point `INDEX.md` at it instead.

## Always, in the same change

- **Update `INDEX.md`.** New doc → add a row (link + a `read_when` one-liner that
  mirrors the doc's frontmatter). Renamed/moved/deleted doc → fix the row. A doc
  change that leaves the map stale is an incomplete change.
- **Fix inbound links** to any doc you moved or renamed.
- **Bump `updated`** on docs you changed.

## Finish by checking your work

Run the **docs-audit** skill (or its script directly) before considering the
change done. It catches orphans, dangling links, missing frontmatter, budget
overruns, and duplication — the exact failure modes this skill is preventing.

## Templates

Copy-paste starting points for a new leaf, a section index, and an `INDEX.md` row
are in [`references/templates.md`](references/templates.md). Use them so new docs
are born with correct frontmatter and triggers. When a repo has no `docs/` tree
yet, scaffold one: create `docs/INDEX.md` from the index template and add leaves
under it.
