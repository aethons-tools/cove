# Templates

Copy-paste starting points. They exist so every new doc is born with correct,
front-loaded metadata. Use the doc's own format: Markdown for `.md`,
AsciiDoc for `.adoc` (the AsciiDoc templates are at the end). The *rules* behind the fields live at
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

## Section index (Tier 1 — add only when a branch outgrows the map)

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

## Map row (Markdown)

Mirror the target doc's `read_when` so routing happens from the map alone:

```markdown
| [<Doc title>](<relative/path>.md) | <one-line read-when trigger> |
```

## New leaf doc (AsciiDoc)

The six fields are header attributes directly under the title, with no blank
line between them. In an Antora book, put the page under
`docs/modules/ROOT/pages/` and add it to `nav.adoc` (below).

```asciidoc
= <Title>
:summary: <one sentence: what this doc IS>
:read_when: <the reader's situation that should make them open this — not the topic>
:owns: <the subject(s) this doc is the single source of truth for>
:prereqs: <docs to read first, or "none">
:tier: leaf
:updated: <YYYY-MM-DD>

<First sentence: what this is and the single most important takeaway.>

== <Section>

<Body. One subject. Stay under ~200 lines. Link to owners with
xref:<path/under/pages>.adoc[<text>] instead of restating.>
```

A root map or section index in AsciiDoc is the same header with `:tier: index`
or `:tier: section`, then the orientation line and a map table.

## Map row (AsciiDoc)

The map's table, with one row per doc (mirror its `:read_when:`):

```asciidoc
[%header]
|===
|Doc |Read when

|xref:<path/under/pages>.adoc[<Doc title>] |<one-line read-when trigger>
|===
```

## nav.adoc entry (Antora book)

Each page also gets a line in the module's `nav.adoc`, nested under its index:

```asciidoc
** xref:<path/under/pages>.adoc[<Doc title>]
```
