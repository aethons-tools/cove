---
name: docs-audit
description: Check that a repository's documentation is still aggressively progressively disclosed — no orphan docs, no broken links, no oversized docs, no duplicated content, valid frontmatter. Use this skill before merging any change that touches docs, when docs feel bloated or hard to navigate, or on a schedule to catch drift. It runs a deterministic checker and explains how to fix what it finds, rather than relying on eyeballing.
---

# docs-audit

Verify the docs tree still upholds the invariants in
`/agent-data/reference/progressive-disclosure.md`. Prefer the script over
manual review — it's deterministic and won't miss a dangling link the way a skim
will.

## Run it

From the repository root (so `docs` resolves to the repo's docs tree):

```bash
python3 ${CLAUDE_SKILL_DIR}/scripts/docs_audit.py docs
```

`${CLAUDE_SKILL_DIR}` resolves to this skill's own directory wherever it's
installed, so the command works from any repo without hardcoding the path.

Useful flags:

- `--strict` — treat warnings as failures (good for CI gating).
- `--budget-leaf N` / `--budget-section N` / `--budget-index N` — override the
  default size budgets if this repo tuned them.
- `--index PATH` — if the root map isn't found by default (the first of
  `INDEX.md`, `INDEX.adoc`, or, in an Antora component, its `start_page`,
  `modules/ROOT/pages/INDEX.adoc` or `index.adoc`).
- `--exclude GLOB` — files under the docs dir that aren't docs (e.g.
  `'superpowers/*'`); repeatable.
- `--repo-url URL` — the URL prefix of the repo's files (e.g.
  `https://github.com/o/r/blob/main`), so a link to a doc by its URL counts as
  a link (`--repo-root` if the docs dir isn't in a git checkout); repeatable.

Docs may be Markdown (`.md`, YAML frontmatter) or AsciiDoc (`.adoc`, the same
fields as header attributes after the title, e.g. `:read_when: …`), and a tree
may mix both. AsciiDoc links are `xref:`, `<<…>>` and `link:` targets; when the
docs dir holds an `antora.yml`, its docs are the pages under
`modules/*/pages/` and xrefs resolve as Antora resolves them.

Exit code is non-zero when there are errors (or, with `--strict`, warnings), so it
drops straight into a pre-commit hook or CI step.

## What it reports, and how to fix each

**ERROR — missing/incomplete frontmatter (or header attributes).** Add the
field. Every doc needs `summary`, `read_when`, `owns`, `prereqs`, `tier`, `updated`. Use the templates in
the **docs-author** skill.

**ERROR — orphan (unreachable from the index).** Add a row linking to it in
the index (or the relevant section index). A doc no one can route to may as well
not exist.

**ERROR — dangling link / missing anchor.** The target file or heading moved or was
renamed. Fix the link, or restore the target. Often a leftover from a rename that
didn't update inbound links.

**WARN — budget overrun.** The doc is too big for its tier. Split it via
**docs-author**: carve out a subject into a new leaf, leave a link behind, add the
new row to the map.

**WARN — weak read_when.** The trigger is a topic, not a situation, so readers
can't route on it. Rewrite it as "you are doing X and need Y."

**WARN — duplicated line across docs.** A fact has two homes. Pick the owner, and
replace the copy with a link. This is the single most important warning to act on —
duplication is what makes docs rot.

## After fixing

Re-run until clean. If you changed structure, also re-check that `INDEX.md` still
reads as a pure map (no prose creeping in) and stays under its budget.
