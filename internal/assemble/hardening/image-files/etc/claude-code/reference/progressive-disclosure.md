---
summary: The complete specification for aggressively progressively disclosed documentation.
read_when: You are authoring, restructuring, or auditing docs and need the full rules, tiers, budgets, or frontmatter schema. Skip this if you only need to *read* docs to do an unrelated task — start at INDEX.md instead.
owns: progressive-disclosure doctrine, tier definitions, size budgets, frontmatter schema, invariants
prereqs: none
tier: leaf
updated: 2026-06-30
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
INDEX.md  (Tier 0)        ← always cheap to load; pure map, no prose
  ├─ section index        (Tier 1)  ← orientation + links, loaded after routing in
  │    ├─ leaf doc         (Tier 2)  ← the actual detail, single subject, bounded
  │    └─ leaf doc
  └─ leaf doc
```

A reader descends only the branch the task needs, and stops the instant they have
enough. They never glob-read the tree.

## The five invariants

These must hold at all times. Every authoring and audit decision serves them.

1. **Tiny entry point.** `INDEX.md` is a map, never a manual. Hard budget below.
2. **Front-loaded triggers.** Every doc's first lines (its frontmatter + opening
   sentence) state what it is and *when to read it*, so a reader can decide to open
   or skip it without reading the body.
3. **Single source of truth.** Every fact lives in exactly one doc. Everything else
   *links* to it; nothing copies it. Duplication is the root cause of doc rot.
4. **Self-containment within budget.** A leaf doc covers one subject, stands on its
   own (or names its prerequisites explicitly), and stays under its size budget. If
   it overflows, split it — don't cram.
5. **No orphans, no dangling.** Every doc is reachable from `INDEX.md`; every link
   and anchor resolves. The map and the tree never drift apart.

## Tiers and budgets

Budgets are deliberately tight. Hitting a budget is the signal to split, not to
shrink the font. Treat these as defaults; a repo may tune them in `INDEX.md`
frontmatter, but smaller is always safer.

| Tier | Role | Budget | Contains | Never contains |
|------|------|--------|----------|----------------|
| 0 — `INDEX.md` | Root map | ≤ 50 lines | One row per doc: link + one-line `read_when` | Explanatory prose, examples, anything not a route |
| 1 — section index | Branch map | ≤ 120 lines | A short orientation paragraph + links to leaves | Reference detail that belongs in a leaf |
| 2 — leaf doc | The content | ≤ 200 lines | One subject, in full | A second subject (split it instead) |

A repo small enough not to need Tier 1 should skip it: `INDEX.md` links straight to
leaves. Add Tier 1 only when a branch has enough leaves that the root map would
blow its budget.

## Frontmatter schema

Every doc starts with YAML frontmatter. This is what lets a reader route from
metadata alone, without loading the body.

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

`read_when` is the load-bearing field. Compare:

- Bad (topic): `read_when: Information about authentication.`
- Good (situation): `read_when: You are adding a login flow or debugging a 401, and
  need to know how tokens are issued and refreshed.`

The good version lets a reader match their *task* against it and decide in one pass.

## What good looks like — a row in INDEX.md

`INDEX.md` mirrors each doc's `read_when` so routing happens from the map alone:

```markdown
| Doc | Read when |
|-----|-----------|
| [Auth](reference/auth.md) | Adding a login flow or debugging a 401. |
| [Deploys](reference/deploys.md) | Shipping to staging/prod or a deploy failed. |
```

A reader scans this table, opens *only* the matching row's doc, and ignores the rest.

## When to split, merge, or link

- **Split** a leaf when it exceeds its budget *or* starts covering a second subject.
  The new leaf gets its own frontmatter and an `INDEX.md` row in the same change.
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
