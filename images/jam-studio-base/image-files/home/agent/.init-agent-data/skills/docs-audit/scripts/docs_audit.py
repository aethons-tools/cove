#!/usr/bin/env python3
"""
docs_audit.py — verify a docs tree is aggressively progressively disclosed.

Checks the invariants from /agent-data/reference/progressive-disclosure.md:
  ERRORS (fail the build):
    - missing or incomplete metadata (Markdown: YAML frontmatter;
      AsciiDoc: header attributes after the title, e.g. `:read_when: …`)
    - orphan docs (unreachable from the index)
    - dangling links (a .md/.adoc target or #anchor that doesn't resolve)
  WARNINGS (advisory):
    - tier budget overruns (split candidates)
    - weak/empty read_when triggers
    - duplicated content across docs (single-source-of-truth risk)

Docs are Markdown (.md) and AsciiDoc (.adoc); a tree may mix both (e.g. while
it is being converted). Links between docs, in either direction:
  Markdown  [text](x.md#anchor), [text](x.adoc#anchor)
  AsciiDoc  xref:x.adoc#anchor[text], <<x.adoc#anchor,text>>, <<anchor>>,
            link:x.md[text]
and, with --repo-url, URLs of files in the repository (how a converted page
links a doc not converted yet). When the docs dir holds an antora.yml (an
Antora component), its .adoc docs are the pages under modules/*/pages/, and an
xref resolves as Antora does: [module:]path.adoc relative to the module's
pages (./ and ../ relative to the page); one naming another component or a
version is not checked. Otherwise an xref is relative to the file.

The index is the root map: --index, else the first of INDEX.md, INDEX.adoc,
the component's start_page, modules/ROOT/pages/INDEX.adoc or index.adoc.

Stdlib only. Usage:
    python3 docs_audit.py [DOCS_DIR] [--index PATH]
        [--exclude GLOB]... [--repo-url URL]... [--repo-root DIR]
        [--budget-index N] [--budget-section N] [--budget-leaf N]
        [--dup-min-len N] [--strict]   # --strict: treat warnings as errors
Exit code 0 if no errors (and, with --strict, no warnings), else 1.
"""
from __future__ import annotations
import argparse, fnmatch, os, re, sys
from pathlib import Path
from collections import defaultdict

REQUIRED_FIELDS = ["summary", "read_when", "owns", "prereqs", "tier", "updated"]
FRONTMATTER_RE = re.compile(r"^---\n(.*?)\n---\n", re.DOTALL)
MD_LINK_RE = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
HEADING_RE = re.compile(r"^#{1,6}\s+(.*)$", re.MULTILINE)

ADOC_ATTR_RE = re.compile(r"^:([\w][\w-]*)!?:(?:\s+(.*))?$")
ADOC_TITLE_RE = re.compile(r"^=+\s+(.*)$", re.MULTILINE)
ADOC_SECTION_RE = re.compile(r"^(={2,6})\s+(.*?)\s*$")
ADOC_DELIMS = re.compile(r"^(-{4,}|\.{4,}|\+{4,}|/{4,})\s*$")
ADOC_IDS_RE = re.compile(
    r"^\[\[([\w:.-]+)(?:,[^\]]*)?\]\]|\[\[([\w:.-]+)(?:,[^\]]*)?\]\]|"
    r"^\[#([\w:.-]+)[^\]]*\]$|^\[[^\]]*\bid=\"?([\w:.-]+)\"?[^\]]*\]$|anchor:([\w:.-]+)\[",
    re.MULTILINE)
XREF_RE = re.compile(r"xref:([^\s\[]+)\[")
ANGLE_XREF_RE = re.compile(r"<<([^,>\s]+)(?:,[^>]*)?>>")
LINK_MACRO_RE = re.compile(r"link:(?!//)([^\s\[]+)\[")
URL_RE = re.compile(r"(?<!link:)\bhttps?://[^\s\[\]<>\"')]+")

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


def strip_adoc_literals(text: str) -> str:
    """Drop AsciiDoc listing/literal/passthrough/comment blocks and comment lines."""
    out, delim = [], None
    for line in text.splitlines():
        m = ADOC_DELIMS.match(line)
        if m and (delim is None or m.group(1) == delim):
            delim = None if delim else m.group(1)
            continue
        if delim is None and not line.startswith("//"):
            out.append(line)
    return "\n".join(out)


def strip_inline_code(text: str) -> str:
    """Drop inline code spans so example links inside them aren't policed."""
    return re.sub(r"`+[^`\n]*`+", "", text)


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


def parse_adoc_header(text: str) -> dict | None:
    """The attribute entries of an AsciiDoc document header: the lines from the
    top (comments allowed) through the title to the first blank line. None
    when the document has no header attributes."""
    attrs: dict[str, str] = {}
    lines = text.splitlines()
    i = 0
    while i < len(lines) and (not lines[i].strip() or lines[i].startswith("//")):
        i += 1
    while i < len(lines) and lines[i].strip():
        line = lines[i]
        i += 1
        m = ADOC_ATTR_RE.match(line)
        if not m:
            continue
        value = (m.group(2) or "").strip()
        while value.endswith(" \\") and i < len(lines):  # soft-wrapped value
            value = value[:-2].rstrip() + " " + lines[i].strip()
            i += 1
        attrs[m.group(1)] = value
    return attrs or None


def adoc_auto_id(title: str, prefix: str, sep: str) -> str:
    """The section id Asciidoctor generates from a title (Section.generate_id),
    after dropping inline markup the title's HTML wouldn't keep as text."""
    t = re.sub(r"pass:[a-z,]*\[(.*?)\]", r"\1", title)
    t = re.sub(r"[`*+]|\{[\w-]+\}", "", t)
    t = re.sub(r"<[^>]+>|&(?:[a-z][a-z]+\d{0,2}|#\d\d\d{0,4}|#x[\da-f]{3,4});", "", t.lower())
    t = prefix + re.sub(r"[^ \w.-]+", "", t)
    if sep == "":
        return t.replace(" ", "")
    squeeze = " .-" if sep in "-." else f" {re.escape(sep)}.-"
    t = re.sub(f"[{squeeze}]+", sep, t)
    if t.endswith(sep):
        t = t[: -len(sep)]
    if prefix == "" and t.startswith(sep):
        t = t[len(sep):]
    return t


class Component:
    """An Antora component rooted at the docs dir (antora.yml)."""

    def __init__(self, root: Path):
        self.root = root
        self.name, self.start_page, self.attrs = None, None, {}
        section = None
        for line in (root / "antora.yml").read_text(encoding="utf-8").splitlines():
            m = re.match(r"^(\s*)([\w-]+):\s*(.*?)\s*$", line)
            if not m:
                continue
            indent, key, value = len(m.group(1)), m.group(2), m.group(3).strip("'\"")
            if indent == 0:
                section = key
                if key == "name":
                    self.name = value
                elif key == "start_page":
                    self.start_page = value
            elif section == "asciidoc" and key in ("idprefix", "idseparator"):
                self.attrs[key] = value

    def module_of(self, page: Path) -> tuple[str, Path] | None:
        """(module, pages dir) of a page, or None if it isn't one."""
        try:
            parts = page.relative_to(self.root / "modules").parts
        except ValueError:
            return None
        if len(parts) < 3 or parts[1] != "pages":
            return None
        return parts[0], self.root / "modules" / parts[0] / "pages"

    def resolve(self, target: str, page: Path) -> Path | None:
        """The file an xref target (no fragment) names from page; None when it
        points outside this component's current version."""
        if "@" in target:
            return None
        parts = target.split(":")
        if len(parts) > 3 or (len(parts) == 3 and parts[0] != self.name):
            return None
        here = self.module_of(page)
        module = parts[-2] if len(parts) >= 2 else (here[0] if here else "ROOT")
        path = parts[-1]
        if path.startswith(("./", "../")) and len(parts) == 1 and here:
            return (page.parent / path).resolve()
        return (self.root / "modules" / module / "pages" / path).resolve()


class Doc:
    def __init__(self, path: Path, root: Path, component: Component | None):
        self.path = path
        self.rel = path.relative_to(root).as_posix()
        self.adoc = path.suffix == ".adoc"
        self.text = path.read_text(encoding="utf-8")
        if self.adoc:
            self.prose = strip_adoc_literals(self.text)
            self.meta = parse_adoc_header(self.text)
        else:
            self.prose = strip_code_fences(self.text)
            self.meta = parse_frontmatter(self.text)
        self._anchors: set[str] | None = None
        self._component = component

    @property
    def anchors(self) -> set[str]:
        if self._anchors is None:
            self._anchors = self._adoc_anchors() if self.adoc else {slugify(h) for h in HEADING_RE.findall(self.prose)}
        return self._anchors

    def _adoc_anchors(self) -> set[str]:
        attrs = dict(self._component.attrs) if self._component else {}
        attrs.update(self.meta or {})
        prefix = attrs.get("idprefix", "_")
        sep = attrs.get("idseparator", "_")
        ids = {next(g for g in m.groups() if g) for m in ADOC_IDS_RE.finditer(self.prose)}
        lines = self.prose.splitlines()
        for i, line in enumerate(lines):
            m = ADOC_SECTION_RE.match(line)
            if not m:
                continue
            prev = lines[i - 1] if i else ""
            if re.match(r"^\[(?:#|\[|[^\]]*\bid=)", prev):
                continue  # an explicit id replaces the generated one
            base, n = adoc_auto_id(m.group(2), prefix, sep), 2
            sid = base
            while sid in ids:
                sid, n = f"{base}{sep or '_'}{n}", n + 1
            ids.add(sid)
        return ids

    def links(self) -> list[tuple[str, str]]:
        """(kind, href) of every link in the prose: kind is md, xref, link or url."""
        out: list[tuple[str, str]] = []
        if not self.adoc:
            for raw in MD_LINK_RE.findall(self.prose):
                out.append(("md", raw.split()[0].strip()))
            return out
        prose = strip_inline_code(self.prose)
        out += [("xref", t) for t in XREF_RE.findall(prose)]
        out += [("xref", t) for t in ANGLE_XREF_RE.findall(prose)]
        out += [("link", t) for t in LINK_MACRO_RE.findall(prose)]
        out += [("url", u) for u in URL_RE.findall(prose)]
        return out


def find_repo_root(start: Path) -> Path:
    for p in [start, *start.parents]:
        if (p / ".git").exists():
            return p
    return start.parent


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("docs_dir", nargs="?", default="docs")
    ap.add_argument("--index", default=None, help="the root map, relative to DOCS_DIR (default: see above)")
    ap.add_argument("--exclude", action="append", default=[], help="glob (relative to DOCS_DIR) of files that are not docs, e.g. 'superpowers/*'")
    ap.add_argument("--repo-url", action="append", default=[], help="URL prefix of the repository's files (e.g. https://github.com/o/r/blob/main)")
    ap.add_argument("--repo-root", default=None, help="the repository root --repo-url maps to (default: the enclosing git checkout)")
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
    component = Component(root) if (root / "antora.yml").is_file() else None
    repo_root = Path(args.repo_root).resolve() if args.repo_root else find_repo_root(root)

    def is_doc(p: Path) -> bool:
        rel = p.relative_to(root).as_posix()
        if any(fnmatch.fnmatch(rel, g) for g in args.exclude):
            return False
        if p.suffix == ".adoc":
            if p.name.startswith("_") or p.name == "nav.adoc":
                return False
            if component and not component.module_of(p):
                return False
        return True

    paths = sorted(p for ext in ("*.md", "*.adoc") for p in root.rglob(ext) if is_doc(p))
    docs = {p: Doc(p, root, component) for p in paths}

    candidates = [args.index] if args.index else ["INDEX.md", "INDEX.adoc"]
    if not args.index and component:
        if component.start_page:
            sp = component.start_page.split(":")
            candidates.append(f"modules/{sp[0] if len(sp) > 1 else 'ROOT'}/pages/{sp[-1]}")
        candidates += ["modules/ROOT/pages/INDEX.adoc", "modules/ROOT/pages/index.adoc"]
    index_path = next(((root / c).resolve() for c in candidates if (root / c).is_file()), None)
    if index_path is None:
        print(f"error: index not found: tried {', '.join(candidates)} in {root}", file=sys.stderr)
        return 1
    index_name = index_path.relative_to(root).as_posix()
    if index_path not in docs:
        docs[index_path] = Doc(index_path, root, component)

    budgets = {"index": args.budget_index, "section": args.budget_section, "leaf": args.budget_leaf}
    errors: list[str] = []
    warnings: list[str] = []

    # --- metadata, triggers, budgets ---
    for p, d in docs.items():
        what = "header attributes" if d.adoc else "frontmatter"
        if d.meta is None:
            errors.append(f"{d.rel}: missing {'AsciiDoc header attributes' if d.adoc else 'YAML frontmatter'}")
            continue
        for f in REQUIRED_FIELDS:
            if f not in d.meta or not d.meta[f]:
                errors.append(f"{d.rel}: {what} missing required field '{f}'")
        rw = d.meta.get("read_when", "").strip().lower().rstrip(".")
        if rw in WEAK_TRIGGERS or len(rw) < 12:
            warnings.append(f"{d.rel}: weak read_when trigger — phrase it as the reader's situation")
        tier = d.meta.get("tier", "leaf" if p != index_path else "index")
        budget = budgets.get(tier, budgets["leaf"])
        n = d.text.count("\n") + 1
        if n > budget:
            warnings.append(f"{d.rel}: {n} lines exceeds {tier} budget of {budget} — split candidate")

    # --- link integrity (dangling targets / anchors) + reachability graph ---
    def resolve(d: Doc, kind: str, href: str) -> tuple[Path | None, str]:
        """(target file, anchor) of a link; target None when it isn't policed."""
        if kind == "url" or (kind in ("md", "link") and re.match(r"^[a-z][a-z0-9+.-]*:", href)):
            for prefix in args.repo_url:
                prefix = prefix.rstrip("/") + "/"
                if href.startswith(prefix):
                    target, _, anchor = href[len(prefix):].partition("#")
                    return (repo_root / target).resolve(), anchor
            return None, ""
        target, _, anchor = href.partition("#")
        if target == "":
            return d.path, anchor
        if kind == "xref":
            if not target.endswith(".adoc"):
                # <<id>> or xref:id[]: an anchor in this file (Asciidoctor's shorthand)
                return (d.path, target) if not anchor and "/" not in target and "." not in target else (None, "")
            if component and component.module_of(d.path):
                return component.resolve(target, d.path), anchor
        return (d.path.parent / target).resolve(), anchor

    graph: dict[Path, set[Path]] = defaultdict(set)
    for p, d in docs.items():
        for kind, href in d.links():
            tgt, anchor = resolve(d, kind, href)
            if tgt is None or tgt.suffix not in (".md", ".adoc"):
                continue  # only police doc cross-links
            if not tgt.is_file():
                errors.append(f"{d.rel}: dangling link -> {href}")
                continue
            if tgt in docs:
                graph[p].add(tgt)
                t = docs[tgt]
                want = anchor if t.adoc else slugify(anchor)
                if anchor and want not in t.anchors:
                    errors.append(f"{d.rel}: link to missing anchor -> {href}")

    # reachability from the index
    seen: set[Path] = set()
    stack = [index_path]
    while stack:
        cur = stack.pop()
        if cur in seen:
            continue
        seen.add(cur)
        stack.extend(graph.get(cur, ()))
    for p, d in docs.items():
        if p not in seen:
            errors.append(f"{d.rel}: orphan — not reachable from {index_name}")

    # --- duplication heuristic (single-source-of-truth) ---
    line_homes: dict[str, set[Path]] = defaultdict(set)
    for p, d in docs.items():
        in_code = False
        for line in (d.prose if d.adoc else d.text).splitlines():
            s = line.strip()
            if not d.adoc and s.startswith("```"):
                in_code = not in_code
                continue
            if in_code or s.startswith(("|", "#", ">", "-", "*")):
                continue
            if d.adoc and s.startswith(("=", ".", ":", "[", "+", "_")):
                continue
            if len(s) >= args.dup_min_len:
                line_homes[s].add(p)
    for s, homes in line_homes.items():
        if len(homes) > 1:
            where = ", ".join(sorted(docs[h].rel for h in homes))
            warnings.append(f"duplicated line in [{where}] — link to one owner instead:\n    {s[:80]}…")

    # --- report ---
    for w in warnings:
        print(f"WARN  {w}")
    for e in errors:
        print(f"ERROR {e}")
    print(f"\n{len(docs)} docs checked — {len(errors)} error(s), {len(warnings)} warning(s).")
    return 1 if errors or (args.strict and warnings) else 0


if __name__ == "__main__":
    raise SystemExit(main())
