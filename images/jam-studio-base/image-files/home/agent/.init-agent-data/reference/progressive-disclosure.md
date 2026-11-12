---
summary: The complete specification for aggressively progressively disclosed documentation.
read_when: You are authoring, restructuring, or auditing docs and need the full rules, tiers, budgets, the map rule, or the metadata schema (Markdown or AsciiDoc). Skip this if you only need to *read* docs to do an unrelated task — start at the repo's map instead.
owns: progressive-disclosure doctrine, tier definitions, size budgets, the map rule, the metadata schema (YAML frontmatter and AsciiDoc header attributes), invariants
prereqs: none
tier: leaf
updated: 2026-11-11
---

# Aggressively Progressively Disclosed Documentation

## The problem this solves

An agent (or human) has a small, expensive attention budget. Most docs trees are
written to be *read front-to-back by someone with infinite patience*. That forces
a reader to load far more than the task needs, buries the one relevant paragraph
under ten irrelevant ones, and rots the moment any fact is duplicated.

Progressive disclosure inverts the default: **the reader loads the minimum, and
each thing they load tells them exactly what to load next.** "Aggressive" means we
push the always-on surface as close to zero as possible and defer *everything* else
behind explicit pointers.

## The one mental model

Documentation is a **routing tree**, not a book.

```
the map   (Tier 0)        ← always cheap to load; pure map, no prose
  ├─ section index        (Tier 1)  ← orientation + links, loaded after routing in
  │    ├─ leaf doc         (Tier 2)  ← the actual detail, single subject, bounded
  │    └─ leaf doc
  └─ leaf doc
```

A reader descends only the branch the task needs, and stops the instant they have
enough. They never glob-read the tree.

## Formats and the map

Docs are Markdown (`.md`) or AsciiDoc (`.adoc`); a tree may mix both. The
metadata is the same in either (see the schema below); only the syntax differs.

**The map** (the Tier-0 root) is `docs/INDEX.md`, or, when `docs/antora.yml`
exists (the docs are an Antora book), the page its `start_page` names, under
`docs/modules/ROOT/pages/` — e.g. `start_page: INDEX.adoc` is
`docs/modules/ROOT/pages/INDEX.adoc`. A repo's `AGENTS.md` says which. In an
Antora book every page is also listed in its `nav.adoc` (the human reader's
menu); the map is still what agents route from. A site built from the docs,
with search, is the human view; agents read the files.

## The five invariants

These must hold at all times. Every authoring and audit decision serves them.

1. **Tiny entry point.** The map is a map, never a manual. Hard budget below.
2. **Front-loaded triggers.** Every doc's first lines (its metadata + opening
   sentence) state what it is and *when to read it*, so a reader can decide to open
   or skip it without reading the body.
3. **Single source of truth.** Every fact lives in exactly one doc. Everything else
   *links* to it; nothing copies it. Duplication is the root cause of doc rot.
4. **Self-containment within budget.** A leaf doc covers one subject, stands on its
   own (or names its prerequisites explicitly), and stays under its size budget. If
   it overflows, split it — don't cram.
5. **No orphans, no dangling.** Every doc is reachable from the map; every link
   (or `xref:`) and anchor resolves. The map and the tree never drift apart.

## Tiers and budgets

Budgets are deliberately tight. Hitting a budget is the signal to split, not to
shrink the font. Treat these as defaults; a repo may tune them in the map's
metadata, but smaller is always safer.

| Tier | Role | Budget | Contains | Never contains |
|------|------|--------|----------|----------------|
| 0 — the map | Root map | ≤ 50 lines | One row per doc: link + one-line `read_when` | Explanatory prose, examples, anything not a route |
| 1 — section index | Branch map | ≤ 120 lines | A short orientation paragraph + links to leaves | Reference detail that belongs in a leaf |
| 2 — leaf doc | The content | ≤ 200 lines | One subject, in full | A second subject (split it instead) |

A repo small enough not to need Tier 1 should skip it: the map links straight to
leaves. Add Tier 1 only when a branch has enough leaves that the root map would
blow its budget.

## Metadata schema

Every doc starts with the same six fields. This is what lets a reader route from
metadata alone, without loading the body. In Markdown they are YAML frontmatter:

```yaml
---
summary: One sentence. What this doc *is*.
read_when: The trigger. The conditions under which a reader should open this doc.
           Write it as the reader's situation, not the doc's topic.
owns: The subjects this doc is the single source of truth for. Used by audits to
      catch duplication and by authors to find the right home for a new fact.
prereqs: Docs to read first, or "none". Keeps leaves self-contained without copying.
tier: index | section | leaf
updated: YYYY-MM-DD
---
```

In AsciiDoc they are header attributes, directly under the `= Title` line (one
line each; no blank line between title and attributes):

```asciidoc
= Title
:summary: One sentence. What this doc *is*.
:read_when: The trigger, as the reader's situation.
:owns: The subjects this doc is the single source of truth for.
:prereqs: Docs to read first, or "none".
:tier: index | section | leaf
:updated: YYYY-MM-DD
```

`read_when` is the load-bearing field. Compare:

- Bad (topic): `read_when: Information about authentication.`
- Good (situation): `read_when: You are adding a login flow or debugging a 401, and
  need to know how tokens are issued and refreshed.`

The good version lets a reader match their *task* against it and decide in one pass.

## What good looks like — a row in the map

The map mirrors each doc's `read_when` so routing happens from the map alone:

```markdown
| Doc | Read when |
|-----|-----------|
| [Auth](reference/auth.md) | Adding a login flow or debugging a 401. |
| [Deploys](reference/deploys.md) | Shipping to staging/prod or a deploy failed. |
```

The same rows in an AsciiDoc map (in an Antora book, `xref:` targets are paths
under the module's `pages/`):

```asciidoc
[%header]
|===
| Doc | Read when
| xref:reference/auth.adoc[Auth] | Adding a login flow or debugging a 401.
| xref:reference/deploys.adoc[Deploys] | Shipping to staging/prod or a deploy failed.
|===
```

A reader scans this table, opens *only* the matching row's doc, and ignores the rest.

## When to split, merge, or link

- **Split** a leaf when it exceeds its budget *or* starts covering a second subject.
  The new leaf gets its own metadata and a map row in the same change.
- **Merge** two leaves when neither stands alone and they're always read together —
  two half-subjects are worse than one whole one.
- **Link, never copy.** If you're about to restate a fact that lives elsewhere,
  link to its owner instead. If no doc owns it yet, that's a signal to create the
  leaf that will.

## The anti-patterns this system exists to kill

- A `README` that grew into a 2,000-line everything-doc.
- The same configuration steps pasted into four guides, now three of them stale.
- A doc you can't tell whether to read without reading it.
- A "docs" folder where finding the right file means opening half of them.
- Detail living at Tier 0, so every reader pays for every topic.
