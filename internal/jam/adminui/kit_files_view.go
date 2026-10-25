package adminui

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"gopkg.in/yaml.v3"
)

// maxFileShow caps a file the page shows or diffs (64 KiB, as the CLI diff).
const maxFileShow = kittree.MaxDiffFile

// maxCtxEntries caps how many files a version's list or diff shows.
const maxCtxEntries = 500

// ctxRead is one tree version's files, read once per request (treeRead).
type ctxRead struct {
	size   string // the tree's total size
	files  []ctxEntry
	byPath map[string]int
}

func (c *ctxRead) get(p string) (ctxEntry, bool) {
	if c == nil {
		return ctxEntry{}, false
	}
	i, ok := c.byPath[p]
	if !ok {
		return ctxEntry{}, false
	}
	return c.files[i], true
}

// hiddenContext stands in for a packed context in a shown kit.yml.
const hiddenContext = "«packed context, not shown»"

// hiddenKitYML is a kit.yml text that does not parse (the migration keeps such
// a text verbatim) as the page shows it: every value of a "context" key,
// anywhere in the document (aliases and merge keys included), replaced by
// hiddenContext, so no packed base64 is shown. Fail-safe: text that is not
// YAML but mentions "context", a re-marshal that fails, or a result that
// still carries a replaced value is hiddenContext alone, never the text.
func hiddenKitYML(text string) string {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		if strings.Contains(text, "context") {
			return hiddenContext
		}
		return text
	}
	var values []string
	var walk func(n *yaml.Node, depth int) bool
	walk = func(n *yaml.Node, depth int) bool {
		if n == nil || depth > 64 {
			return n == nil
		}
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value != "context" {
					continue
				}
				v := n.Content[i+1]
				for v.Kind == yaml.AliasNode && v.Alias != nil {
					v = v.Alias
				}
				values = append(values, v.Value)
				n.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: hiddenContext}
			}
		}
		for _, c := range n.Content {
			if !walk(c, depth+1) {
				return false
			}
		}
		return true
	}
	if !walk(&doc, 0) {
		return hiddenContext
	}
	if len(values) == 0 {
		return text
	}
	b, err := yaml.Marshal(&doc)
	if err != nil {
		return hiddenContext
	}
	for _, v := range values {
		if v == "" || strings.Contains(string(b), v) {
			return hiddenContext
		}
	}
	return string(b)
}

// pathText is a tree path as shown: SetupDisplayText's escaping, with \n and
// \t made visible too (a name is one line).
func pathText(p string) string {
	return strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(jam.SetupDisplayText(p))
}

func modeText(e ctxEntry) string { return fmt.Sprintf("%04o", uint32(e.Mode)) }

func bytesText(n int64) string { return strconv.FormatInt(n, 10) + " B" }

// fileHref links one file of a version: ?v=N[&diff=M]&file=<path>.
func fileHref(kit string, v, diff int, p string) string {
	h := kitURL(kit) + "?v=" + strconv.Itoa(v)
	if diff > 0 {
		h += "&diff=" + strconv.Itoa(diff)
	}
	return h + "&file=" + url.QueryEscape(p)
}

// ctxRow is one file of a version's tree.
type ctxRow struct {
	Path, Href, Mode string
	Size             int64
}

// ctxListView is the viewed version's file list, kit.yml included.
type ctxListView struct {
	N      int
	Size   string // the tree's total size
	Rows   []ctxRow
	Hidden int // files past maxCtxEntries, not listed
}

func ctxList(kit string, v kitView) *ctxListView {
	if v.ctx == nil {
		return nil
	}
	l := &ctxListView{N: v.N, Size: v.ctx.size}
	for i, f := range v.ctx.files {
		if i == maxCtxEntries {
			l.Hidden = len(v.ctx.files) - i
			break
		}
		l.Rows = append(l.Rows, ctxRow{Path: pathText(f.Path), Href: fileHref(kit, v.N, 0, f.Path), Mode: modeText(f), Size: f.Size})
	}
	return l
}

// ctxChange is one changed file between two versions' trees.
type ctxChange struct {
	Change     string // added | removed | modified | mode
	Path, Href string
	Mode, Size string // "0644 → 0755" / "2 B → 5 B" when changed
}

// ctxDiffView compares two versions' trees file by file.
type ctxDiffView struct {
	From, To  int
	Rows      []ctxChange
	More      int // changed files past maxCtxEntries, not listed
	Unchanged int
}

// ctxDiff compares from's and to's trees; nil when neither has one. A
// version without one compares as empty.
func ctxDiff(kit string, from, to kitView) *ctxDiffView {
	if from.ctx == nil && to.ctx == nil {
		return nil
	}
	d := &ctxDiffView{From: from.N, To: to.N}
	var paths []string
	for _, v := range []kitView{from, to} {
		if v.ctx != nil {
			for _, f := range v.ctx.files {
				paths = append(paths, f.Path)
			}
		}
	}
	slices.Sort(paths)
	for _, p := range slices.Compact(paths) {
		a, inA := from.ctx.get(p)
		b, inB := to.ctx.get(p)
		row := ctxChange{Path: pathText(p), Href: fileHref(kit, to.N, from.N, p)}
		switch {
		case !inA:
			row.Change, row.Mode, row.Size = "added", modeText(b), bytesText(b.Size)
		case !inB:
			row.Change, row.Mode, row.Size = "removed", modeText(a), bytesText(a.Size)
		case a.Sum == b.Sum && a.Mode == b.Mode:
			d.Unchanged++
			continue
		default:
			row.Change, row.Mode, row.Size = "modified", modeText(b), bytesText(b.Size)
			if a.Mode != b.Mode {
				row.Mode = modeText(a) + " → " + modeText(b)
				if a.Sum == b.Sum {
					row.Change = "mode"
				}
			}
			if a.Sum != b.Sum {
				row.Size = bytesText(a.Size) + " → " + bytesText(b.Size)
			}
		}
		if len(d.Rows) == maxCtxEntries {
			d.More++
			continue
		}
		d.Rows = append(d.Rows, row)
	}
	return d
}

// ctxFileView is one file of a version's tree: its content (version view) or
// its line diff (diff view), or a note when it can't be shown.
type ctxFileView struct {
	Path     string // display text
	Diff     bool
	From, To int
	Lines    []diffLine
	TooLarge bool
	Content  string
	Note     string // binary or oversize: why no content, with sizes
	Missing  bool   // in neither version
}

// fileView shows file p of to (and, when diff, compares it with from's).
func fileView(p string, from, to *ctxRead, diff bool) *ctxFileView {
	a, inA := from.get(p)
	b, inB := to.get(p)
	if !diff {
		inA = false
	}
	if !inA && !inB {
		return &ctxFileView{Missing: true, Diff: diff}
	}
	f := &ctxFileView{Path: pathText(p), Diff: diff}
	if !diff {
		switch {
		case b.Size > maxFileShow:
			f.Note = "too large to show (" + bytesText(b.Size) + ")"
		case isBinary(b.Data):
			f.Note = "binary file (" + bytesText(b.Size) + "), content not shown"
		default:
			f.Content = jam.SetupDisplayText(string(b.Data))
		}
		return f
	}
	sizes := sideSize(a, inA) + " → " + sideSize(b, inB)
	switch {
	case (inA && a.Size > maxFileShow) || (inB && b.Size > maxFileShow):
		f.Note = "too large to diff (" + sizes + ")"
	case isBinary(a.Data) || isBinary(b.Data):
		f.Note = "binary file (" + sizes + "), content not shown"
	default:
		lines, tooLarge := lineDiff(string(a.Data), string(b.Data))
		for i := range lines {
			lines[i].Text = jam.SetupDisplayText(lines[i].Text)
		}
		f.Lines, f.TooLarge = lines, tooLarge
	}
	return f
}

func sideSize(e ctxEntry, ok bool) string {
	if !ok {
		return "absent"
	}
	return bytesText(e.Size)
}

// isBinary reports content shown only as a note: not text as the CLI diff
// reads it (kittree.IsText: invalid UTF-8, or a control character other than
// tab, newline and carriage return).
func isBinary(b []byte) bool { return !kittree.IsText(b) }
