package adminui

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// draftURL is the path of a kit's Draft tab.
func draftURL(name string) string { return kitURL(name) + "/draft" }

// draftFileHref opens file p in the Draft tab: in the editor, or with diff
// as its diff against the draft's base.
func draftFileHref(kit, p string, diff bool) string {
	h := draftURL(kit) + "?file=" + url.QueryEscape(p)
	if diff {
		h += "&diff=1"
	}
	return h
}

// draftQuery is what the Draft tab shows besides the draft: one file
// (?file=), in the editor or, with Diff (?diff=1), as its diff against the base.
type draftQuery struct {
	File string
	Diff bool
}

// draftPage is the Draft tab payload.
type draftPage struct {
	Title       string
	Name        string
	Current     int
	Versions    []int        // the open form's choices
	Draft       *draftView   // nil: the kit has no draft
	Published   *publishView // set right after a publish
	NotFound    bool
	NotFoundFor string
}

// draftView is the kit's open draft, ready to render.
type draftView struct {
	Base, Current int
	Superseded    bool   // the kit's current version is not the draft's base
	ETag          string // the draft's ETag at this read: the forms send it back
	Actor         string
	Updated       string
	Managed       bool     // config-managed: publishing drifts
	DriftWarning  string   // jam.DraftDriftWarning, shown when Managed
	DriftConfirm  string   // the box a drifting publish ticks (draftDriftConfirm)
	PinDrift      []string // config-managed followers Pin followers would rewrite
	PinDriftNote  string   // the warning naming them, next to the Pin box
	MaxFile       string   // the per-file limit, for the upload hints
	Problems      []string
	Size          string       // the draft's total size
	Tree          []draftRow   // its files, nested by directory
	Removed       []draftRow   // files of the base the draft no longer has
	Editor        *draftEditor // the file asked for (?file=), opened; nil = none
	FileMissing   bool         // ?file= named no file of the draft (or an unsafe path)
	Changes       *ctxDiffView // the files against the base
	YAMLDiff      *kitDiff     // kit.yml's line diff; nil when kit.yml is unchanged
	FileDiff      *ctxFileView // ?file=…&diff=1: that file against the base
}

// draftRow is one row of the Draft tab's file tree: a directory or a file.
type draftRow struct {
	Pad    string // two spaces per level
	Depth  int    // its directory depth
	Raw    string // the stored path (the JSON API's file= value); "" for a directory
	Name   string // the last segment (display text)
	Dir    bool
	Path   string // the whole path (display text)
	Href   string
	Mode   string
	Size   int64
	Change string // against the base: added, modified, mode, removed; "" unchanged
}

// draftTree nests sorted entries by directory: a row for each directory the
// first time a file under it appears, then the file, each indented by its
// depth. Sorted paths keep a directory's files together.
func draftTree(kit string, entries []jam.DraftEntry, kinds map[string]string) []draftRow {
	var out []draftRow
	var prev []string
	for _, e := range entries {
		segs := strings.Split(e.Path, "/")
		dirs := segs[:len(segs)-1]
		same := 0
		for same < len(prev) && same < len(dirs) && prev[same] == dirs[same] {
			same++
		}
		for i := same; i < len(dirs); i++ {
			out = append(out, draftRow{Pad: strings.Repeat("  ", i), Depth: i, Name: pathText(dirs[i]), Dir: true, Path: pathText(strings.Join(dirs[:i+1], "/"))})
		}
		out = append(out, draftRow{
			Pad: strings.Repeat("  ", len(dirs)), Depth: len(dirs), Raw: e.Path, Name: pathText(segs[len(segs)-1]), Path: pathText(e.Path),
			Href: draftFileHref(kit, e.Path, false), Mode: e.Mode, Size: e.Size, Change: kinds[e.Path],
		})
		prev = dirs
	}
	return out
}

// newDraftView shapes draft status st of kit for display; base is the tree
// of its base version (where removed files are read from).
func newDraftView(kit string, st jam.KitDraftStatus, base kittree.Tree) *draftView {
	v := &draftView{
		Base: st.Base, Current: st.Current, Superseded: st.Base != st.Current,
		ETag: st.ETag, Actor: st.Actor, Updated: st.UpdatedAt.UTC().Format("2006-01-02 15:04"),
		Managed: st.Managed, DriftWarning: jam.DraftDriftWarning, DriftConfirm: draftDriftConfirm, Problems: st.Problems,
		MaxFile: kittree.SizeText(kittree.MaxFile),
	}
	var total int64
	for _, e := range st.Entries {
		total += e.Size
	}
	v.Size = sizeLabel(int(total))
	kinds := make(map[string]string, len(st.Changes))
	for _, c := range st.Changes {
		kinds[c.Path] = c.Kind
	}
	v.Tree = draftTree(kit, st.Entries, kinds)
	for _, c := range st.Changes {
		if c.Kind != kittree.ChangeRemoved {
			continue
		}
		e, _ := base.Lookup(c.Path)
		v.Removed = append(v.Removed, draftRow{
			Name: pathText(c.Path), Path: pathText(c.Path), Raw: c.Path, Href: draftFileHref(kit, c.Path, true),
			Mode: fileModeText(e.Mode), Size: e.Size, Change: kittree.ChangeRemoved,
		})
	}
	return v
}

// buildDraftPage gathers kit name's Draft tab showing q. ok is false when the
// kit does not exist; a kit with no draft has Draft nil. err is a store fault.
func buildDraftPage(store jam.Store, name string, q draftQuery) (draftPage, bool, error) {
	k, ok := store.GetKit(name)
	if !ok {
		return draftPage{}, false, nil
	}
	p := draftPage{Title: "Kits", Name: name, Current: k.Current, Versions: k.VersionNumbers()}
	st, err := jam.KitDraftStatusOf(store, name)
	if err != nil {
		if jam.WriteStatus(err, http.StatusInternalServerError) == http.StatusNotFound {
			return p, true, nil // no draft (or the kit went since)
		}
		return p, true, err
	}
	base, ok := jam.LoadKitVersion(store, name, st.Base)
	if !ok || base.Files == nil {
		// A draft's base is always a stored tree: its absence is a fault.
		return p, true, fmt.Errorf("kit %s draft: base v%d's tree could not be read", name, st.Base)
	}
	p.Draft = newDraftView(name, st, base.Tree)
	if managed := managedFollowersToPin(store, name); len(managed) > 0 {
		p.Draft.PinDrift = managed
		p.Draft.PinDriftNote = "Pinning rewrites config-managed " + strings.Join(managed, ", ") + ": with Pin followers, this publish drifts from the config repo."
	}
	if q.File != "" && !q.Diff {
		if err := openDraftFile(store, name, q.File, p.Draft); err != nil {
			return p, true, err
		}
	}
	if err := draftDiff(store, name, st, base, q, p.Draft); err != nil {
		return p, true, err
	}
	return p, true, nil
}
