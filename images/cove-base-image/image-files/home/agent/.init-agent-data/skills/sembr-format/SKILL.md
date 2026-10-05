---
name: sembr-reformat
description: Reformat prose using Semantic Line Breaks (SemBr) from https://sembr.org while preserving rendered output and meaning. Use when asked to reflow plain text or compatible markup (Markdown, AsciiDoc, reStructuredText, LaTeX, Org, MediaWiki) into semantic one-thought-per-line formatting, or when improving prose diffs and editorial readability.
---

# Reformat With SemBr

Apply Semantic Line Breaks to prose without changing what readers see.
Preserve wording, punctuation, and intent.

## Reformat Workflow

1. Identify prose regions that support soft-wrapped lines.
2. Skip regions where line breaks are syntactically significant:
   - fenced code blocks and indented code blocks
   - tables
   - YAML frontmatter
   - URLs or markup tokens that must stay contiguous
3. Rewrite only line wrapping, not content.
4. Keep paragraphs and list structures intact.
5. Return full rewritten text unless asked for a targeted excerpt.

## SemBr Rules

Apply these rules in order:

1. Break after every sentence ending in `.`, `!`, or `?`. 
2. Prefer no more than 120 characters per line, when possible.
   For normal prose lines that are beyond this limit, try these rules, in order:
   A. Never break inside a word, a hyphenated word, a URL/URN/URI, or a code span.
   B. Prefer a break after independent clauses ending in `,`, `;`, `:`, or `—`.
   C. Insert a break before an enumerated or itemized list.
   D. Insert a break before one or more hyperlinks, URLs/URNs/URIs, or code spans on the line.
   E. Allow the longer line.

## Quality Checks

Before returning output, verify:

- Rendering-equivalence: no hard-break syntax added accidentally.
- Meaning-equivalence: no word changes or punctuation edits.
- Structure-equivalence: headings, lists, and block boundaries unchanged.
- Diff-friendliness: edits are mostly line-wrap changes.

## Output Style

Use minimal explanation.
Return reformatted text directly.
If any segment cannot be safely reformatted, leave it unchanged.
