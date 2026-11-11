---
summary: The tools for converting docs/ to AsciiDoc for the architecture book — manual/tools/docs-to-adoc.js (Markdown → AsciiDoc, into a parameterised Antora layout) and the docs-audit checker's AsciiDoc and mixed-tree support — how to run them and what to review by hand.
read_when: You are converting a doc in docs/ from Markdown to AsciiDoc, auditing a docs tree that mixes .md and .adoc, or changing the converter or the checker's AsciiDoc handling.
owns: manual/tools/docs-to-adoc.js (what it converts, its layout options, its warnings), how docs-audit is run on a mixed tree (--exclude, --repo-url), and their tests (just test-docs-audit, the manual's npm test)
prereqs: manual-books.md for the team site's books; the docs-audit skill (studio-image payload) for the checker itself
tier: leaf
updated: 2026-11-11
---

# Converting docs to AsciiDoc

`docs/` is becoming the *architecture* book on the team site
([manual-books.md](manual-books.md)): an Antora component in AsciiDoc,
converted doc by doc, so for a while the tree mixes `.md` and `.adoc`.
Two tools carry that: a converter, and the docs-audit checker, which
reads both formats. `docs/superpowers/` stays Markdown and outside the book.

## The converter

`node manual/tools/docs-to-adoc.js [options] <doc.md>...` (or
`just docs-to-adoc …`) converts each doc and prints it, or writes it with
`-o FILE` or `--out-dir DIR`, which puts each page where the layout says
(`DIR/modules/<module>/pages/<path>.adoc`). It has no dependencies (no pandoc).
It works line by line, so prose keeps its line breaks:

- front matter → header attributes after the `= Title` (the first `#` heading):
  `:summary:`, `:read_when:`, `:owns:`, `:prereqs:`, `:tier:`, `:updated:`,
  values verbatim (YAML quotes removed; `{name}` escaped);
- headings, nested lists (`*`/`.` repeated per level; a block in an item
  attaches with `+`), pipe tables → `[%header]` `|===` with their alignment,
  fences → `[source,lang]` `----`, quotes → `____`, `> **Note:**` and
  `> [!NOTE]` → `NOTE:` admonitions, `---` → `'''`, images, HTML comments and
  `<a id>` anchors;
- inline code stays `` `code` ``, or becomes a literal `` `+code+` `` when
  AsciiDoc would format inside it; emphasis is doubled inside a word; text
  AsciiDoc would read as markup (a line starting `. ` or `[`) is protected
  with `{empty}`;
- links: a doc in the tree → an `xref:` in the layout; `#anchor` → `<<anchor>>`;
  anything else in the repo (code, kept Markdown) → its GitHub URL
  (`--repo-url`, default `https://github.com/aethons-tools/cove/blob/main`).
  A heading that some `.md` doc links to by anchor gets that anchor as an
  explicit `[#id]`, so inbound links keep working.

The **layout is a parameter**: by default one `ROOT` module holding the tree's
paths; `--module usage=usage` sends `docs/usage/**` to module `usage` (prefix
dropped), and xrefs between modules carry it (`xref:usage:jam-colima.adoc[]`);
`--plain` targets a plain AsciiDoc tree (relative xrefs). `--keep-md GLOB`
names docs that stay Markdown (`superpowers/**`, and docs not converted yet),
which get GitHub links. `--docs-root` and `--repo-root` default to `docs` and `.`.

Its warnings on stderr name what it can't convert faithfully (reference-style
links, raw HTML, a missing or second level-1 heading, a skipped heading level,
an anchor no heading has, an image outside the module's `images/`).
Review every converted page rendered (Antora, or Asciidoctor) regardless.

## Auditing a mixed tree

The docs-audit checker (`docs_audit.py`, payload of the studio image under
`images/jam-studio-base/image-files/…/skills/docs-audit/`; its SKILL.md has the
flags) reads `.adoc` header attributes as metadata, `xref:`, `<<…>>` and
`link:` targets as links, resolves xrefs the Antora way when the docs dir
holds an `antora.yml`, and takes the component's start page (or `INDEX.adoc`)
as the map. For this repo mid-conversion:

```bash
python3 <skill>/scripts/docs_audit.py docs --exclude 'superpowers/*' \
  --repo-url https://github.com/aethons-tools/cove/blob/main
```

`--repo-url` makes a page's GitHub link to a not-yet-converted `.md` doc count
as a link, so those docs stay reachable from the new map.

## Tests

- `manual/tools/test/docs-to-adoc.test.js` (the manual's `npm test`, gate's
  web leg): converts `fixture/docs/sample.md` to the golden `fixture/sample.adoc`,
  renders it with Asciidoctor (`@asciidoctor/core`, pinned) expecting no
  warnings, checks the layouts and the CLI, and audits a converted tree with
  `docs_audit.py`. After an intended change, regenerate the golden file with
  the command above the test's first case.
- `images/jam-studio-base/docs_audit_test.py` (`just test-docs-audit`, gate's
  go leg): the checker on Markdown, AsciiDoc and mixed Antora trees.
