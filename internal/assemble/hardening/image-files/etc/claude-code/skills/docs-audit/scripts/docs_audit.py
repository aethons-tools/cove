#!/usr/bin/env python3
"""
docs_audit.py — verify a docs tree is aggressively progressively disclosed.

Checks the invariants from /etc/claude-code/reference/progressive-disclosure.md:
  ERRORS (fail the build):
    - missing or incomplete frontmatter
    - orphan docs (unreachable from INDEX.md)
    - dangling links (relative .md target or #anchor that doesn't resolve)
  WARNINGS (advisory):
    - tier budget overruns (split candidates)
    - weak/empty read_when triggers
    - duplicated content across docs (single-source-of-truth risk)

Stdlib only. Usage:
    python3 docs_audit.py [DOCS_DIR] [--index INDEX.md]
        [--budget-index N] [--budget-section N] [--budget-leaf N]
        [--dup-min-len N] [--strict]   # --strict: treat warnings as errors
Exit code 0 if no errors (and, with --strict, no warnings), else 1.
"""
from __future__ import annotations
import argparse, re, sys
from pathlib import Path
from collections import defaultdict

REQUIRED_FIELDS = ["summary", "read_when", "owns", "prereqs", "tier", "updated"]
FRONTMATTER_RE = re.compile(r"^---\n(.*?)\n---\n", re.DOTALL)
MD_LINK_RE = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
HEADING_RE = re.compile(r"^#{1,6}\s+(.*)$", re.MULTILINE)

WEAK_TRIGGERS = {"", "info", "information", "docs", "documentation", "tbd", "todo"}


def strip_code_fences(text: str) -> str:
    """Drop fenced ``` blocks so illustrative example links/headings aren't policed."""
    out, in_code = [], False
    for line in text.splitlines():
        if line.lstrip().startswith("```"):
            in_code = not in_code
            continue
        if not in_code:
            out.append(line)
    return "\n".join(out)


def slugify(heading: str) -> str:
    s = heading.strip().lower()
    s = re.sub(r"[^\w\s-]", "", s)
    return re.sub(r"\s+", "-", s)


def parse_frontmatter(text: str) -> dict | None:
    m = FRONTMATTER_RE.match(text)
    if not m:
        return None
    fm: dict[str, str] = {}
    for line in m.group(1).splitlines():
        if ":" in line and not line.startswith(" "):
            k, _, v = line.partition(":")
            fm[k.strip()] = v.strip()
    return fm


def anchors_of(text: str) -> set[str]:
    return {slugify(h) for h in HEADING_RE.findall(text)}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("docs_dir", nargs="?", default="docs")
    ap.add_argument("--index", default="INDEX.md")
    ap.add_argument("--budget-index", type=int, default=50)
    ap.add_argument("--budget-section", type=int, default=120)
    ap.add_argument("--budget-leaf", type=int, default=200)
    ap.add_argument("--dup-min-len", type=int, default=60)
    ap.add_argument("--strict", action="store_true")
    args = ap.parse_args()

    root = Path(args.docs_dir).resolve()
    if not root.is_dir():
        print(f"error: docs dir not found: {root}", file=sys.stderr)
        return 1
    index_path = root / args.index
    if not index_path.is_file():
        print(f"error: index not found: {index_path}", file=sys.stderr)
        return 1

    md_files = sorted(p for p in root.rglob("*.md"))
    text = {p: p.read_text(encoding="utf-8") for p in md_files}
    prose = {p: strip_code_fences(t) for p, t in text.items()}
    budgets = {"index": args.budget_index, "section": args.budget_section, "leaf": args.budget_leaf}

    errors: list[str] = []
    warnings: list[str] = []

    # --- frontmatter, triggers, budgets ---
    fm_by: dict[Path, dict] = {}
    for p in md_files:
        rel = p.relative_to(root)
        fm = parse_frontmatter(text[p])
        if fm is None:
            errors.append(f"{rel}: missing YAML frontmatter")
            continue
        fm_by[p] = fm
        for f in REQUIRED_FIELDS:
            if f not in fm or not fm[f]:
                errors.append(f"{rel}: frontmatter missing required field '{f}'")
        rw = fm.get("read_when", "").strip().lower().rstrip(".")
        if rw in WEAK_TRIGGERS or len(rw) < 12:
            warnings.append(f"{rel}: weak read_when trigger — phrase it as the reader's situation")
        tier = fm.get("tier", "leaf" if p != index_path else "index")
        budget = budgets.get(tier, budgets["leaf"])
        n = text[p].count("\n") + 1
        if n > budget:
            warnings.append(f"{rel}: {n} lines exceeds {tier} budget of {budget} — split candidate")

    # --- link integrity (dangling targets / anchors) + reachability graph ---
    graph: dict[Path, set[Path]] = defaultdict(set)
    for p in md_files:
        for raw in MD_LINK_RE.findall(prose[p]):
            href = raw.split()[0].strip()
            if href.startswith(("http://", "https://", "mailto:")):
                continue
            target, _, anchor = href.partition("#")
            rel = p.relative_to(root)
            if target == "":  # same-file anchor
                tgt_path = p
            else:
                tgt_path = (p.parent / target).resolve()
                if not str(tgt_path).endswith(".md"):
                    continue  # only police .md cross-links
                if not tgt_path.is_file():
                    errors.append(f"{rel}: dangling link -> {href}")
                    continue
            if tgt_path in text:
                graph[p].add(tgt_path)
                if anchor and slugify(anchor) not in anchors_of(prose[tgt_path]):
                    errors.append(f"{rel}: link to missing anchor -> {href}")

    # reachability from INDEX.md
    seen: set[Path] = set()
    stack = [index_path]
    while stack:
        cur = stack.pop()
        if cur in seen:
            continue
        seen.add(cur)
        stack.extend(graph.get(cur, ()))
    for p in md_files:
        if p not in seen:
            errors.append(f"{p.relative_to(root)}: orphan — not reachable from {args.index}")

    # --- duplication heuristic (single-source-of-truth) ---
    line_homes: dict[str, set[Path]] = defaultdict(set)
    for p in md_files:
        in_code = False
        for line in text[p].splitlines():
            s = line.strip()
            if s.startswith("```"):
                in_code = not in_code
                continue
            if in_code or s.startswith(("|", "#", ">", "-", "*")):
                continue
            if len(s) >= args.dup_min_len:
                line_homes[s].add(p)
    for s, homes in line_homes.items():
        if len(homes) > 1:
            where = ", ".join(sorted(h.relative_to(root).as_posix() for h in homes))
            warnings.append(f"duplicated line in [{where}] — link to one owner instead:\n    {s[:80]}…")

    # --- report ---
    for w in warnings:
        print(f"WARN  {w}")
    for e in errors:
        print(f"ERROR {e}")
    print(f"\n{len(md_files)} docs checked — {len(errors)} error(s), {len(warnings)} warning(s).")
    return 1 if errors or (args.strict and warnings) else 0


if __name__ == "__main__":
    raise SystemExit(main())
