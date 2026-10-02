package adminui

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
	"gopkg.in/yaml.v3"
)

// kitListRow is one kits-table row, summarizing the current version.
type kitListRow struct {
	Name           string
	Current, Count int
	Base           string // base kind label; "" when the current config won't parse
	Egress         int
	UsedBy         int
	Invalid        bool
}

// kitVersion is one entry in a kit's version rail.
type kitVersion struct {
	N       int
	Prev    int // the version before N in the rail; 0 for the first
	Current bool
	Digest  string // short build digest; "" when the config won't parse
}

// ctxEntry is one file in a packed base.context.
type ctxEntry struct {
	Path string
	Size int64
	Mode string
}

// kv is a sorted key/value pair for build-args and secret demands.
type kv struct{ Key, Value string }

// kitView is one parsed version, ready to render.
type kitView struct {
	N           int
	ParseErr    string // the stored config isn't a valid studio kit (legacy row)
	Raw         string // the stored text, shown when ParseErr is set
	YAML        string // display YAML (a packed context abbreviated)
	PushYAML    string // full YAML, for pre-filling a new version
	Base        string
	Image       string
	Files       []string // context-files paths
	Context     []ctxEntry
	ContextSize string // encoded size of a packed context
	ContextNote string // why the entry list is partial or missing
	Egress      []string
	Ceiling     []string
	Excluded    []string
	BuildArgs   []kv
	Secrets     []kv
	Prompt      string
	Digest      string
}

// diffLine is one line of a version diff: Op is ' ', '+', '-' or '…' (a
// collapsed run of unchanged lines, Text says how many).
type diffLine struct {
	Op   string
	Text string
}

// kitDiff compares two versions' display YAML.
type kitDiff struct {
	From, To      int
	Lines         []diffLine
	DigestChanged bool
	TooLarge      bool
}

// kitUse is one role bound to the kit. Implicit marks a role with no kit set,
// which raises the built-in default.
type kitUse struct {
	Project, Role string
	Implicit      bool
}

// kitDetail is the kit page payload.
type kitDetail struct {
	Title       string
	Name        string
	Current     int
	Versions    []kitVersion
	View        kitView
	Diff        *kitDiff
	Uses        []kitUse
	IsDefault   bool
	NotFound    bool
	NotFoundFor string
}

func baseLabel(b studio.Base) string {
	k, err := b.Kind()
	if err != nil {
		return "invalid"
	}
	switch k {
	case studio.BaseImage:
		return "image"
	case studio.BaseContextFiles:
		return "context-files"
	case studio.BaseContextZip:
		return "context"
	case studio.BaseContextDir:
		return "context-dir"
	}
	return "blessed default"
}

// kitUses lists the roles bound to name; for the default kit, also the roles
// with no kit set (they raise it implicitly).
func kitUses(store jam.Store, name string) []kitUse {
	var out []kitUse
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			switch {
			case r.Kit == name:
				out = append(out, kitUse{Project: p, Role: r.Name})
			case r.Kit == "" && name == studio.DefaultStudioKitID:
				out = append(out, kitUse{Project: p, Role: r.Name, Implicit: true})
			}
		}
	}
	return out
}

func kitListRows(store jam.Store) []kitListRow {
	var out []kitListRow
	for _, k := range store.ListKits() {
		row := kitListRow{Name: k.Name, Current: k.Current, Count: len(k.Versions), UsedBy: len(kitUses(store, k.Name))}
		if sk, err := studio.ParseStudioKit([]byte(k.Versions[k.Current])); err == nil {
			row.Base, row.Egress = baseLabel(sk.Base), len(sk.Egress)
		} else {
			row.Invalid = true
		}
		out = append(out, row)
	}
	return out
}

// displayKit is sk as shown: no legacy name, and a packed context replaced by
// a one-line summary carrying its digest (so a diff still shows it changed).
func displayKit(sk studio.StudioKit) studio.StudioKit {
	sk.LegacyName = ""
	if sk.Base.Context != "" {
		sk.Base.Context = fmt.Sprintf("<packed tar.gz, %s, build digest %s>", sizeLabel(len(sk.Base.Context)), shortDigest(studio.BuildDigest(sk)))
	}
	return sk
}

func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

func sizeLabel(n int) string {
	if n < 1024 {
		return strconv.Itoa(n) + " B"
	}
	return strconv.Itoa((n+1023)/1024) + " KiB"
}

func toYAML(sk studio.StudioKit) string {
	b, err := yaml.Marshal(sk)
	if err != nil {
		return ""
	}
	return string(b)
}

// maxCtxEntries caps how many packed-context entries the page lists.
const maxCtxEntries = 500

// contextEntries lists a packed base.context's files from the tar headers only
// (no file data is read into memory, nothing is extracted).
func contextEntries(b64 string) ([]ctxEntry, string) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, "not valid base64"
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, "not a readable gzip"
	}
	tr := tar.NewReader(gz)
	var out []ctxEntry
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, ""
		}
		if err != nil {
			return out, "unreadable past this point"
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if len(out) == maxCtxEntries {
			return out, fmt.Sprintf("first %d entries shown", maxCtxEntries)
		}
		out = append(out, ctxEntry{Path: h.Name, Size: h.Size, Mode: fmt.Sprintf("%04o", h.Mode&0o7777)})
	}
}

func treePaths(t studio.ContextTree, prefix string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(t)) {
		n := t[k]
		if n.File != nil {
			out = append(out, prefix+k)
		} else {
			out = append(out, treePaths(n.Dir, prefix+k+"/")...)
		}
	}
	return out
}

// viewKit parses one stored version for display.
func viewKit(n int, stored string) kitView {
	v := kitView{N: n}
	sk, err := studio.ParseStudioKit([]byte(stored))
	if err != nil {
		v.ParseErr, v.Raw = err.Error(), stored
		return v
	}
	full := sk
	full.LegacyName = ""
	v.PushYAML = toYAML(full)
	v.YAML = toYAML(displayKit(sk))
	v.Base, v.Image, v.Digest = baseLabel(sk.Base), sk.Base.Image, studio.BuildDigest(sk)
	if len(sk.Base.ContextFiles) > 0 {
		v.Files = treePaths(sk.Base.ContextFiles, "")
	}
	if sk.Base.Context != "" {
		v.ContextSize = sizeLabel(len(sk.Base.Context))
		v.Context, v.ContextNote = contextEntries(sk.Base.Context)
	}
	v.Egress = sk.Egress
	v.Ceiling, v.Excluded = studio.Ceiling(sk.Egress)
	for _, k := range slices.Sorted(maps.Keys(sk.BuildArgs)) {
		v.BuildArgs = append(v.BuildArgs, kv{k, sk.BuildArgs[k]})
	}
	for _, k := range slices.Sorted(maps.Keys(sk.Secrets)) {
		v.Secrets = append(v.Secrets, kv{k, sk.Secrets[k].Description})
	}
	v.Prompt = sk.Prompt
	return v
}

// buildKitDetail gathers the kit page for version view (0 = current), diffed
// against version diff when diff > 0. false when the kit doesn't exist.
func buildKitDetail(store jam.Store, name string, view, diff int) (kitDetail, bool) {
	k, ok := store.GetKit(name)
	if !ok {
		return kitDetail{}, false
	}
	if _, ok := k.Versions[view]; !ok {
		view = k.Current
	}
	d := kitDetail{Title: "Kits", Name: name, Current: k.Current, IsDefault: name == studio.DefaultStudioKitID, Uses: kitUses(store, name)}
	prev := 0
	for _, n := range slices.Sorted(maps.Keys(k.Versions)) {
		ver := kitVersion{N: n, Prev: prev, Current: n == k.Current}
		prev = n
		if sk, err := studio.ParseStudioKit([]byte(k.Versions[n])); err == nil {
			ver.Digest = shortDigest(studio.BuildDigest(sk))
		}
		d.Versions = append(d.Versions, ver)
	}
	d.View = viewKit(view, k.Versions[view])
	if from, ok := k.Versions[diff]; ok && diff != view {
		fv := viewKit(diff, from)
		a, b := fv.YAML, d.View.YAML
		if fv.ParseErr != "" {
			a = fv.Raw
		}
		if d.View.ParseErr != "" {
			b = d.View.Raw
		}
		lines, tooLarge := lineDiff(a, b)
		d.Diff = &kitDiff{From: diff, To: view, Lines: lines, TooLarge: tooLarge, DigestChanged: fv.Digest != d.View.Digest}
	}
	return d, true
}

// maxDiffCells bounds the LCS table (lines(a) × lines(b)).
const maxDiffCells = 4_000_000

// diffContext is how many unchanged lines are kept around a change.
const diffContext = 3

// lineDiff is a line-level LCS diff of a → b, with long unchanged runs
// collapsed to a "…" marker. tooLarge reports the inputs exceed maxDiffCells.
func lineDiff(a, b string) ([]diffLine, bool) {
	al, bl := splitLines(a), splitLines(b)
	n, m := len(al), len(bl)
	if n*m > maxDiffCells {
		return nil, true
	}
	// lcs[i][j] = LCS length of al[i:], bl[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var raw []diffLine
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && al[i] == bl[j]:
			raw = append(raw, diffLine{" ", al[i]})
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]): // removals before additions
			raw = append(raw, diffLine{"-", al[i]})
			i++
		default:
			raw = append(raw, diffLine{"+", bl[j]})
			j++
		}
	}
	return collapse(raw), false
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// collapse keeps diffContext unchanged lines around each change and replaces
// longer unchanged runs with a single "…" line.
func collapse(lines []diffLine) []diffLine {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op != " " {
			for k := max(0, i-diffContext); k <= min(len(lines)-1, i+diffContext); k++ {
				keep[k] = true
			}
		}
	}
	var out []diffLine
	for i := 0; i < len(lines); {
		if keep[i] {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && !keep[j] {
			j++
		}
		out = append(out, diffLine{"…", fmt.Sprintf("%d unchanged line(s)", j-i)})
		i = j
	}
	return out
}
