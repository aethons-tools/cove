# Templates

Copy-paste starting points. They exist so every new doc is born with correct,
front-loaded frontmatter. The *rules* behind the fields live at
`/agent-data/reference/progressive-disclosure.md` — this file is only the
boilerplate.

## New leaf doc

```markdown
---
summary: <one sentence: what this doc IS>
read_when: <the reader's situation that should make them open this — not the topic>
owns: <the subject(s) this doc is the single source of truth for>
prereqs: <docs to read first, or "none">
tier: leaf
updated: <YYYY-MM-DD>
---

# <Title>

<First sentence: what this is and the single most important takeaway, so a reader
can stop here if that's all they needed.>

<Body. One subject. Stay under ~200 lines. Link to owners instead of restating.>
```

## Root map (`docs/INDEX.md`, Tier 0 — scaffold this first in a new repo)

```markdown
---
summary: Root map of this repo's documentation — start here, route from here.
read_when: You need information from the docs. Always start here; open only the rows that match your task.
owns: routing for all docs
prereqs: none
tier: index
updated: <YYYY-MM-DD>
---

# Documentation index

This is a **map, not a manual**. Find the row whose "Read when" matches your task,
open only that doc, and stop when you have enough. Don't read the whole tree.

| Doc | Read when |
|-----|-----------|
| [<Doc title>](<relative/path>.md) | <one-line read-when trigger> |
```

## Section index (Tier 1 — add only when a branch outgrows INDEX.md)

```markdown
---
summary: Map of everything under <area>.
read_when: You're working anywhere in <area> and need to find the right doc.
owns: routing for <area>
prereqs: none
tier: section
updated: <YYYY-MM-DD>
---

# <Area>

<One short orientation paragraph: what this area covers and how its docs relate.>

| Doc | Read when |
|-----|-----------|
| [<Leaf>](<leaf>.md) | <one-line trigger> |
```

## INDEX.md row

Mirror the target doc's `read_when` so routing happens from the map alone:

```markdown
| [<Doc title>](<relative/path>.md) | <one-line read-when trigger> |
```
