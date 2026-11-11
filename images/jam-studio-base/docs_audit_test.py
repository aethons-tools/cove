#!/usr/bin/env python3
"""Tests for the docs-audit skill's checker, payload of this image:
image-files/home/agent/.init-agent-data/skills/docs-audit/scripts/docs_audit.py.

Hermetic: each test writes a small docs tree (Markdown, AsciiDoc, or both, as
in a tree being converted) to a temp dir and runs the checker on it.
Run: python3 images/jam-studio-base/docs_audit_test.py  (just test-docs-audit)
"""
from __future__ import annotations
import subprocess, sys, tempfile, textwrap, unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "image-files/home/agent/.init-agent-data/skills/docs-audit/scripts/docs_audit.py"


def fm(tier="leaf", read_when="You are testing the checker and need this doc."):
    return f"---\nsummary: s\nread_when: {read_when}\nowns: o\nprereqs: none\ntier: {tier}\nupdated: 2026-11-11\n---\n\n"


def hdr(title, tier="leaf", extra=""):
    return (f"= {title}\n:summary: s\n:read_when: You are testing the checker and need this page.\n"
            f":owns: o\n:prereqs: none\n:tier: {tier}\n:updated: 2026-11-11\n{extra}\n")


class Tree:
    def __init__(self, files: dict[str, str]):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / ".git").mkdir()
        for rel, text in files.items():
            p = self.root / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(textwrap.dedent(text) if not text.startswith(("---", "=")) else text)

    def audit(self, *args: str) -> tuple[int, str]:
        r = subprocess.run([sys.executable, str(SCRIPT), str(self.root / "docs"), *args],
                           capture_output=True, text=True)
        return r.returncode, r.stdout + r.stderr


class MarkdownTree(unittest.TestCase):
    """The checker's Markdown behaviour, unchanged by AsciiDoc support."""

    def test_clean_tree_passes(self):
        t = Tree({
            "docs/INDEX.md": fm("index") + "# Index\n\n| Doc | Read when |\n|--|--|\n| [a](a.md) | x |\n",
            "docs/a.md": fm() + "# A\n\n## Some section\n\nSee [b](sub/b.md#b-heading) and [me](#some-section).\n",
            "docs/sub/b.md": fm() + "# B\n\n## B heading\n\n```\n[not a link](nowhere.md)\n```\n",
        })
        code, out = t.audit()
        self.assertEqual(code, 0, out)
        self.assertIn("3 docs checked — 0 error(s), 0 warning(s).", out)

    def test_errors_and_warnings(self):
        long_line = "This sentence is long enough to count as a duplicated fact in two docs."
        t = Tree({
            "docs/INDEX.md": fm("index") + "# Index\n\n[a](a.md) [gone](gone.md) [bad anchor](a.md#nope)\n",
            "docs/a.md": fm(read_when="docs") + "# A\n\n" + long_line + "\n" + "x\n" * 200,
            "docs/b.md": fm() + "# B\n\n" + long_line + "\n",
            "docs/c.md": "# no frontmatter\n\n[b](b.md)\n",
            "docs/d.md": "---\nsummary: s\n---\n# D\n",
        })
        code, out = t.audit()
        self.assertEqual(code, 1)
        for want in [
            "ERROR INDEX.md: dangling link -> gone.md",
            "ERROR INDEX.md: link to missing anchor -> a.md#nope",
            "ERROR c.md: missing YAML frontmatter",
            "ERROR d.md: frontmatter missing required field 'read_when'",
            "ERROR b.md: orphan — not reachable from INDEX.md",
            "WARN  a.md: weak read_when trigger",
            "WARN  a.md: 213 lines exceeds leaf budget of 200",
            "WARN  duplicated line in [a.md, b.md]",
        ]:
            self.assertIn(want, out)

    def test_strict_fails_on_warnings(self):
        t = Tree({"docs/INDEX.md": fm("index", read_when="tbd") + "# Index\n"})
        self.assertEqual(t.audit()[0], 0)
        self.assertEqual(t.audit("--strict")[0], 1)

    def test_exclude(self):
        t = Tree({
            "docs/INDEX.md": fm("index") + "# Index\n\n[record](superpowers/r.md)\n",
            "docs/superpowers/r.md": "# A design record without frontmatter\n",
            "docs/superpowers/plans/p.md": "# Nobody links here\n",
        })
        self.assertEqual(t.audit()[0], 1)
        code, out = t.audit("--exclude", "superpowers/*")
        self.assertEqual(code, 0, out)
        self.assertIn("1 docs checked", out)


class AsciiDocTree(unittest.TestCase):
    """A plain AsciiDoc tree (no antora.yml): xrefs are relative to the file."""

    def test_clean_tree_passes(self):
        t = Tree({
            "docs/INDEX.adoc": hdr("Index", "index") + "|===\n|Doc |Read when\n\n|xref:a.adoc[a] |x\n|===\n",
            "docs/a.adoc": hdr("A") + "== Some section\n\nSee xref:sub/b.adoc#b-heading[b], <<_some_section>>,\n"
                           "<<a.adoc#_some_section,again>> and https://example.com[a site].\n\n"
                           "----\nxref:not-a-link.adoc[]\n----\n",
            "docs/sub/b.adoc": hdr("B") + "[#b-heading]\n== B heading\n\nBack to xref:../a.adoc#_some_section[a].\n",
        })
        code, out = t.audit()
        self.assertEqual(code, 0, out)
        self.assertIn("3 docs checked — 0 error(s), 0 warning(s).", out)

    def test_errors(self):
        t = Tree({
            "docs/INDEX.adoc": hdr("Index", "index") + "xref:a.adoc[] xref:gone.adoc[] xref:a.adoc#nope[] <<missing>>\n",
            "docs/a.adoc": hdr("A") + "== Section\n",
            "docs/b.adoc": "= B\n\nNo header attributes.\n",
            "docs/c.adoc": "= C\n:summary: s\n\nxref:a.adoc[]\n",
        })
        code, out = t.audit()
        self.assertEqual(code, 1)
        for want in [
            "ERROR INDEX.adoc: dangling link -> gone.adoc",
            "ERROR INDEX.adoc: link to missing anchor -> a.adoc#nope",
            "ERROR INDEX.adoc: link to missing anchor -> missing",
            "ERROR b.adoc: missing AsciiDoc header attributes",
            "ERROR c.adoc: header attributes missing required field 'read_when'",
            "ERROR b.adoc: orphan — not reachable from INDEX.adoc",
        ]:
            self.assertIn(want, out)

    def test_auto_ids_follow_the_doc_attributes(self):
        t = Tree({
            "docs/INDEX.adoc": hdr("Index", "index", ":idprefix:\n:idseparator: -\n")
                               + "<<the-image-tree-co>> <<api-get-x>> <<twice>> <<twice-2>>\n\n"
                               "== The image tree & co.\n\n== API: `GET /x`\n\n== Twice\n\n== Twice\n",
        })
        code, out = t.audit()
        self.assertEqual(code, 0, out)


class MixedAntoraTree(unittest.TestCase):
    """docs/ as an Antora component mid-conversion: pages under modules/, the
    rest still Markdown and linked by its GitHub URL."""

    REPO = "https://github.com/o/r/blob/main"

    def tree(self, **extra):
        files = {
            "docs/antora.yml": "name: architecture\ntitle: Architecture\nversion: ~\nstart_page: ROOT:INDEX.adoc\n"
                               "asciidoc:\n  attributes:\n    idprefix: ''\n    idseparator: '-'\n",
            "docs/modules/ROOT/nav.adoc": "* xref:INDEX.adoc[]\n* xref:nowhere.adoc[]\n",
            "docs/modules/ROOT/pages/INDEX.adoc": hdr("Index", "index")
                + "|===\n|xref:OVERVIEW.adoc[] |x\n|xref:usage:INDEX.adoc#the-binaries[] |x\n"
                f"|{self.REPO}/docs/usage/jam/INDEX.md[usage/jam] |not converted yet\n"
                "|xref:other-book:ROOT:page.adoc[] |another component\n|===\n",
            "docs/modules/ROOT/pages/OVERVIEW.adoc": hdr("Overview") + "== Architecture\n\nSee xref:./INDEX.adoc[the map].\n",
            "docs/modules/ROOT/partials/_snippet.adoc": "no header\n",
            "docs/modules/usage/pages/INDEX.adoc": hdr("Usage") + "== The binaries\n\n"
                "xref:ROOT:OVERVIEW.adoc#architecture[] and link:jam-mint.html[a page link]\n",
            "docs/usage/jam/INDEX.md": fm("section") + "# Jam\n\n[serve](serve.md) and "
                "[the overview](../../modules/ROOT/pages/OVERVIEW.adoc#architecture)\n",
            "docs/usage/jam/serve.md": fm() + "# Serve\n",
            "docs/superpowers/specs/s.md": "# a design record\n",
        }
        files.update(extra)
        return Tree(files)

    def test_mixed_tree_passes(self):
        code, out = self.tree().audit("--exclude", "superpowers/*", "--repo-url", self.REPO)
        self.assertEqual(code, 0, out)
        # pages, not nav or partials; the Markdown docs too
        self.assertIn("5 docs checked — 0 error(s), 0 warning(s).", out)

    def test_without_repo_url_the_markdown_docs_are_orphans(self):
        code, out = self.tree().audit("--exclude", "superpowers/*")
        self.assertEqual(code, 1)
        self.assertIn("ERROR usage/jam/INDEX.md: orphan — not reachable from modules/ROOT/pages/INDEX.adoc", out)

    def test_broken_links_across_formats(self):
        t = self.tree(**{
            "docs/usage/jam/serve.md": fm() + "# Serve\n\n[old path](../../OVERVIEW.md) [bad](../../modules/ROOT/pages/OVERVIEW.adoc#nope)\n",
            "docs/modules/usage/pages/INDEX.adoc": hdr("Usage") + "== The binaries\n\n"
                f"xref:OVERVIEW.adoc[] xref:ROOT:OVERVIEW.adoc#nope[] {self.REPO}/docs/usage/jam/gone.md[gone]\n",
        })
        code, out = t.audit("--exclude", "superpowers/*", "--repo-url", self.REPO)
        self.assertEqual(code, 1)
        for want in [
            "ERROR usage/jam/serve.md: dangling link -> ../../OVERVIEW.md",
            "ERROR usage/jam/serve.md: link to missing anchor -> ../../modules/ROOT/pages/OVERVIEW.adoc#nope",
            # a page's own module: usage has no OVERVIEW
            "ERROR modules/usage/pages/INDEX.adoc: dangling link -> OVERVIEW.adoc",
            "ERROR modules/usage/pages/INDEX.adoc: link to missing anchor -> ROOT:OVERVIEW.adoc#nope",
            f"ERROR modules/usage/pages/INDEX.adoc: dangling link -> {self.REPO}/docs/usage/jam/gone.md",
        ]:
            self.assertIn(want, out)

    def test_index_choice(self):
        t = self.tree()
        code, out = t.audit("--exclude", "superpowers/*", "--repo-url", self.REPO, "--index", "usage/jam/serve.md")
        self.assertIn("ERROR modules/ROOT/pages/INDEX.adoc: orphan — not reachable from usage/jam/serve.md", out)
        (t.root / "docs/modules/ROOT/pages/INDEX.adoc").unlink()
        code, out = t.audit()
        self.assertEqual(code, 1)
        self.assertIn("error: index not found: tried INDEX.md, INDEX.adoc, modules/ROOT/pages/INDEX.adoc", out)


if __name__ == "__main__":
    unittest.main()
