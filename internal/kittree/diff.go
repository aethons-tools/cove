package kittree

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/linediff"
	"github.com/aethons-tools/cove/internal/textesc"
)

// Change kinds.
const (
	ChangeAdded    = "added"
	ChangeRemoved  = "removed"
	ChangeModified = "modified"
	ChangeMode     = "mode"
)

// Change is one file that differs between two trees. From or To is the zero
// Entry on the side without the file.
type Change struct {
	Path string
	Kind string
	From Entry
	To   Entry
}

// Changes lists, in path order, the files that differ from a to b, and counts
// the rest.
func Changes(a, b Tree) (changes []Change, unchanged int) {
	i, j := 0, 0
	for i < len(a.Entries) || j < len(b.Entries) {
		switch {
		case j == len(b.Entries) || (i < len(a.Entries) && a.Entries[i].Path < b.Entries[j].Path):
			changes = append(changes, Change{Path: a.Entries[i].Path, Kind: ChangeRemoved, From: a.Entries[i]})
			i++
		case i == len(a.Entries) || b.Entries[j].Path < a.Entries[i].Path:
			changes = append(changes, Change{Path: b.Entries[j].Path, Kind: ChangeAdded, To: b.Entries[j]})
			j++
		default:
			x, y := a.Entries[i], b.Entries[j]
			switch {
			case x.SHA256 != y.SHA256:
				changes = append(changes, Change{Path: x.Path, Kind: ChangeModified, From: x, To: y})
			case x.Mode != y.Mode:
				changes = append(changes, Change{Path: x.Path, Kind: ChangeMode, From: x, To: y})
			default:
				unchanged++
			}
			i, j = i+1, j+1
		}
	}
	return changes, unchanged
}

// MaxDiffFile is the largest side a file's line diff reads.
const MaxDiffFile = 64 << 10

// IsText reports content a diff shows as lines: valid UTF-8 with no control
// character but \t, \n and \r. Anything else is shown as binary.
func IsText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// DiffText renders the version diff a → b for a terminal or a plan: kit.yml's
// line diff, then one row per other changed file (added, removed, modified,
// mode). A modified text file of at most MaxDiffFile bytes a side is followed
// by its line diff. Paths are CheckPath-safe; no content is shown but text
// lines.
func DiffText(a, b Tree, blobs Blobs) string {
	var sb strings.Builder
	changes, unchanged := Changes(a, b)
	var rows []Change
	for _, c := range changes {
		if c.Path == KitFile {
			fmt.Fprintf(&sb, "kit.yml:%s\n", modeNote(c))
			if c.Kind == ChangeModified {
				from, to := blobOf(blobs, c.From), blobOf(blobs, c.To)
				if note := undiffable(c, from, to); note != "" {
					sb.WriteString("  " + note + "\n")
				} else {
					writeLines(&sb, "  ", from, to)
				}
			}
			continue
		}
		rows = append(rows, c)
	}
	if _, kitUnchanged := a.Lookup(KitFile); kitUnchanged && !hasPath(changes, KitFile) {
		unchanged--
	}
	if len(changes) == 0 {
		return "no changes\n"
	}
	if len(rows) == 0 {
		return sb.String()
	}
	fmt.Fprintf(&sb, "files: %d changed, %d unchanged\n", len(rows), unchanged)
	for _, c := range rows {
		switch c.Kind {
		case ChangeAdded:
			fmt.Fprintf(&sb, "  added     %s  %04o  %d B\n", c.Path, c.To.Mode, c.To.Size)
		case ChangeRemoved:
			fmt.Fprintf(&sb, "  removed   %s  %04o  %d B\n", c.Path, c.From.Mode, c.From.Size)
		case ChangeMode:
			fmt.Fprintf(&sb, "  mode      %s  %04o → %04o\n", c.Path, c.From.Mode, c.To.Mode)
		case ChangeModified:
			mode := fmt.Sprintf("%04o", c.To.Mode)
			if c.From.Mode != c.To.Mode {
				mode = fmt.Sprintf("%04o → %04o", c.From.Mode, c.To.Mode)
			}
			fmt.Fprintf(&sb, "  modified  %s  %s  %d B → %d B", c.Path, mode, c.From.Size, c.To.Size)
			from, to := blobOf(blobs, c.From), blobOf(blobs, c.To)
			if note := undiffable(c, from, to); note != "" {
				sb.WriteString("  " + note + "\n")
			} else {
				sb.WriteString("\n")
				writeLines(&sb, "     ", from, to)
			}
		}
	}
	return sb.String()
}

// undiffable is why a modified file gets no line diff ("" when it gets one).
func undiffable(c Change, from, to []byte) string {
	switch {
	case c.From.Size > MaxDiffFile || c.To.Size > MaxDiffFile:
		return "(too large to diff)"
	case !IsText(from) || !IsText(to):
		return "(binary)"
	}
	return ""
}

func hasPath(cs []Change, p string) bool {
	for _, c := range cs {
		if c.Path == p {
			return true
		}
	}
	return false
}

func modeNote(c Change) string {
	if c.Kind == ChangeModified && c.From.Mode == c.To.Mode || c.Kind == ChangeAdded || c.Kind == ChangeRemoved {
		return ""
	}
	return fmt.Sprintf(" %04o → %04o", c.From.Mode, c.To.Mode)
}

func blobOf(blobs Blobs, e Entry) []byte {
	if e.SHA256 == "" {
		return nil
	}
	b, _ := blobs(e.SHA256)
	return b
}

// writeLines writes linediff lines indented: "<op><text>", a collapsed run as
// "… <text>". The text is escaped as the setup text is (textesc), so a line
// can neither drive a terminal nor reorder what is shown (\r, bidi overrides,
// line separators).
func writeLines(sb *strings.Builder, indent string, a, b []byte) {
	lines, tooLarge := linediff.Diff(string(a), string(b))
	if tooLarge {
		sb.WriteString(indent + "(too large to diff)\n")
		return
	}
	for _, l := range lines {
		if l.Op == "…" {
			fmt.Fprintf(sb, "%s… %s\n", indent, textesc.Escape(l.Text, true))
		} else {
			fmt.Fprintf(sb, "%s%s%s\n", indent, l.Op, textesc.Escape(l.Text, true))
		}
	}
}
