package adminui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
	"gopkg.in/yaml.v3"
)

// maxFileShow caps a packed-context file the page shows or diffs (64 KiB).
const maxFileShow = 64 << 10

// maxCtxEntries caps how many packed-context files a version's list shows.
const maxCtxEntries = 500

// ctxRead is one version's packed base.context, read once per request.
type ctxRead struct {
	size   string // encoded size label
	sum    string // "sha256:" + 8 hex of SHA-256 over the stored base64
	read   bool   // the files were listed (false: not unpacked, or unreadable)
	err    string // fixed reason the context is unreadable; "" = readable
	files  []studio.ContextEntry
	byPath map[string]int
}

// readContext lists b64's files (keeping want's first bytes) when unpack is
// set. An unreadable context gets a fixed reason, never the scan's message
// (which may quote an entry name).
func readContext(b64 string, unpack bool, want string) *ctxRead {
	h := sha256.Sum256([]byte(b64))
	c := &ctxRead{size: sizeLabel(len(b64)), sum: "sha256:" + hex.EncodeToString(h[:4])}
	if !unpack {
		return c
	}
	files, err := studio.ContextFiles(b64, want, maxFileShow+1)
	switch {
	case errors.Is(err, studio.ErrContextBase64):
		c.err = "not valid base64"
	case err != nil:
		c.err = "not a safe, readable tar.gz"
	default:
		c.read, c.files, c.byPath = true, files, make(map[string]int, len(files))
		for i, f := range files {
			c.byPath[f.Path] = i
		}
	}
	return c
}

// placeholder stands in for the base64 blob in shown and diffed YAML; its
// short hash makes any change to the blob show as a changed line.
func (c *ctxRead) placeholder() string {
	switch {
	case c.err != "":
		return fmt.Sprintf("«tar.gz, unreadable, %s, %s»", c.size, c.sum)
	case !c.read:
		return fmt.Sprintf("«tar.gz, %s, %s»", c.size, c.sum)
	case len(c.files) == 1:
		return fmt.Sprintf("«tar.gz, 1 file, %s, %s»", c.size, c.sum)
	}
	return fmt.Sprintf("«tar.gz, %d files, %s, %s»", len(c.files), c.size, c.sum)
}

func (c *ctxRead) get(p string) (studio.ContextEntry, bool) {
	if c == nil || !c.read {
		return studio.ContextEntry{}, false
	}
	i, ok := c.byPath[p]
	if !ok {
		return studio.ContextEntry{}, false
	}
	return c.files[i], true
}

// storedContextNode finds base.context in stored text that parses as YAML
// but not as a studio kit (a legacy row): the document and the scalar, or a
// nil scalar when there is none.
func storedContextNode(stored string) (*yaml.Node, *yaml.Node) {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(stored), &doc) != nil || len(doc.Content) != 1 {
		return nil, nil
	}
	base := studio.MapValue(doc.Content[0], "base")
	if base == nil {
		return nil, nil
	}
	if c := studio.MapValue(base, "context"); c != nil && c.Kind == yaml.ScalarNode {
		return &doc, c
	}
	return nil, nil
}

// pathText is a tar name as shown: SetupDisplayText's escaping, with \n and
// \t made visible too (a name is one line).
func pathText(p string) string {
	return strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(jam.SetupDisplayText(p))
}

func modeText(e studio.ContextEntry) string { return fmt.Sprintf("%04o", uint32(e.Mode)) }

func bytesText(n int64) string { return strconv.FormatInt(n, 10) + " B" }

// fileHref links one context file: ?v=N[&diff=M]&file=<path>.
func fileHref(kit string, v, diff int, p string) string {
	h := kitURL(kit) + "?v=" + strconv.Itoa(v)
	if diff > 0 {
		h += "&diff=" + strconv.Itoa(diff)
	}
	return h + "&file=" + url.QueryEscape(p)
}

// ctxRow is one file of a version's packed context.
type ctxRow struct {
	Path, Href, Mode string
	Size             int64
	Dup              bool // the tar packs this name more than once (the last is shown, as extraction keeps)
}

// ctxListView is the viewed version's packed-context file list.
type ctxListView struct {
	N      int
	Size   string // encoded size
	Err    string // the context is unreadable: why (fixed text)
	Rows   []ctxRow
	Hidden int // files past maxCtxEntries, not listed
}

func ctxList(kit string, v kitView) *ctxListView {
	if v.ctx == nil {
		return nil
	}
	l := &ctxListView{N: v.N, Size: v.ctx.size, Err: v.ctx.err}
	count := map[string]int{}
	for _, f := range v.ctx.files {
		count[f.Path]++
	}
	paths := slices.Sorted(maps.Keys(count))
	for i, p := range paths {
		if i == maxCtxEntries {
			l.Hidden = len(paths) - i
			break
		}
		f, _ := v.ctx.get(p)
		l.Rows = append(l.Rows, ctxRow{Path: pathText(p), Href: fileHref(kit, v.N, 0, p), Mode: modeText(f), Size: f.Size, Dup: count[p] > 1})
	}
	return l
}

// ctxChange is one changed file between two versions' packed contexts.
type ctxChange struct {
	Change     string // added | removed | modified | mode
	Path, Href string
	Mode, Size string // "0644 → 0755" / "2 B → 5 B" when changed
}

// ctxDiffView compares two versions' packed contexts file by file.
type ctxDiffView struct {
	From, To  int
	Err       string // a side is unreadable: which and why
	Rows      []ctxChange
	More      int // changed files past maxCtxEntries, not listed
	Unchanged int
}

// ctxDiff compares from's and to's packed contexts; nil when neither has
// one. A version without one compares as empty.
func ctxDiff(kit string, from, to kitView) *ctxDiffView {
	if from.ctx == nil && to.ctx == nil {
		return nil
	}
	d := &ctxDiffView{From: from.N, To: to.N}
	var errs []string
	for _, v := range []kitView{from, to} {
		if v.ctx != nil && v.ctx.err != "" {
			errs = append(errs, fmt.Sprintf("v%d %s", v.N, v.ctx.err))
		}
	}
	if len(errs) > 0 {
		d.Err = strings.Join(errs, "; ")
		return d
	}
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

// ctxFileView is one packed-context file: its content (version view) or its
// line diff (diff view), or a note when it can't be shown.
type ctxFileView struct {
	Path     string // display text
	Diff     bool
	From, To int
	Lines    []diffLine
	TooLarge bool
	Content  string
	Note     string // binary or oversize: why no content, with sizes
	Missing  bool   // in neither version
	Err      string // no version's context is readable: why (fixed text)
}

// fileView shows file p of to (and, when diff, compares it with from's).
func fileView(p string, from, to *ctxRead, diff bool) *ctxFileView {
	a, inA := from.get(p)
	b, inB := to.get(p)
	if !diff {
		inA = false
	}
	if !inA && !inB {
		if e := unreadable(from, to, diff); e != "" {
			return &ctxFileView{Diff: diff, Err: e}
		}
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

// unreadable is why a file can't be looked up: every context there is to read
// (to's, and from's when diff) is unreadable; "" when one is readable or none
// exists.
func unreadable(from, to *ctxRead, diff bool) string {
	sides := []*ctxRead{to}
	if diff {
		sides = append(sides, from)
	}
	var why string
	for _, c := range sides {
		switch {
		case c == nil:
		case c.err == "":
			return ""
		default:
			why = c.err
		}
	}
	return why
}

func sideSize(e studio.ContextEntry, ok bool) string {
	if !ok {
		return "absent"
	}
	return bytesText(e.Size)
}

// isBinary reports content shown only as a note: invalid UTF-8 or a NUL.
func isBinary(b []byte) bool { return !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 }
