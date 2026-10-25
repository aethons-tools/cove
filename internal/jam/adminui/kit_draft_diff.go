package adminui

import (
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strconv"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// draftChangedNote stands in for a diff whose draft file changed (or went)
// between the status read and the file read.
const draftChangedNote = "the draft changed while loading (reload)"

// draftFileData is one draft file read for a diff, with its entry as read.
type draftFileData struct {
	path  string
	data  []byte
	entry kittree.Entry
}

// draftRead is a draft's files in the shape the kit page reads a version's
// (ctxRead), from its status entries. A file read (f, when set) carries its
// first maxFileShow+1 bytes, and its entry as read; a kit.yml is masked
// (maskKitYML), as the kit page shows a stored one. A malformed mode is a
// store fault.
func draftRead(kit string, st jam.KitDraftStatus, f *draftFileData) (*ctxRead, error) {
	c := &ctxRead{byPath: make(map[string]int, len(st.Entries))}
	var total int64
	for i, e := range st.Entries {
		mode, err := strconv.ParseUint(e.Mode, 8, 32)
		if err != nil {
			return nil, fmt.Errorf("kit %s draft: a file has a malformed mode", kit)
		}
		ent := ctxEntry{Path: e.Path, Mode: fs.FileMode(mode), Size: e.Size, Sum: e.SHA256}
		if f != nil && e.Path == f.path {
			ent.Mode, ent.Size, ent.Sum = f.entry.Mode, f.entry.Size, f.entry.SHA256
			ent.Data, ent.Mask = maskedKitData(e.Path, f.data)
		}
		total += e.Size
		c.files = append(c.files, ent)
		c.byPath[e.Path] = i
	}
	c.size = sizeLabel(int(total))
	return c, nil
}

// maskedKitData is file p's data as the Draft tab's diffs read it: its first
// maxFileShow+1 bytes, a kit.yml masked (maskKitYML) first.
func maskedKitData(p string, data []byte) ([]byte, maskKind) {
	how := maskNone
	if p == kittree.KitFile {
		var text string
		text, how = maskKitYML(string(data))
		data = []byte(text)
	}
	return data[:min(len(data), maxFileShow+1)], how
}

// readDiffFile reads file p of the draft whose status is st. changed is true
// when the file went, or the draft's ETag moved, since st was read; err is a
// store fault.
func readDiffFile(store jam.Store, kit, p string, st jam.KitDraftStatus) (f *draftFileData, changed bool, err error) {
	data, e, etag, err := jam.KitDraftFile(store, kit, p)
	switch {
	case err != nil && jam.WriteStatus(err, http.StatusInternalServerError) == http.StatusNotFound:
		return nil, true, nil
	case err != nil:
		return nil, false, err
	case etag != st.ETag:
		return nil, true, nil
	}
	return &draftFileData{path: p, data: data, entry: e}, false, nil
}

// draftDiff fills v's diff against the draft's base version: the changed
// files (each row linking its file diff), kit.yml's line diff when it
// changed, and with q.Diff, file q.File's diff. A file the draft or the base
// lacks compares as absent; one neither has is Missing (never named). A file
// that changed between the status read and the file read shows
// draftChangedNote instead of its lines. err is a store fault.
//
// Both sides' kit.yml are masked the same way (maskKitYML), whether or not
// the base's parses, so a packed context in the base is never shown and
// masking alone never makes a change.
func draftDiff(store jam.Store, kit string, st jam.KitDraftStatus, base jam.KitVersion, q draftQuery, v *draftView) error {
	inDraft := func(p string) bool {
		return slices.ContainsFunc(st.Entries, func(e jam.DraftEntry) bool { return e.Path == p })
	}
	want := ""
	if q.Diff {
		want = q.File
	}
	// The wanted file, read once (kit.yml's serves its line diff too).
	var wanted *draftFileData
	wantChanged := false
	if want != "" && inDraft(want) {
		var err error
		if wanted, wantChanged, err = readDiffFile(store, kit, want, st); err != nil {
			return err
		}
	}
	baseView := viewKit(st.Base, base, true, want)
	if want == kittree.KitFile {
		if c := baseView.ctx; c != nil {
			if i, ok := c.byPath[want]; ok {
				c.files[i].Data, c.files[i].Mask = maskedKitData(want, base.KitYML)
			}
		}
	}
	dctx, err := draftRead(kit, st, wanted)
	if err != nil {
		return err
	}
	draft := kitView{ctx: dctx}
	v.Changes = ctxDiffLinked(baseView, draft, func(p string) string { return draftFileHref(kit, p, true) })
	if slices.ContainsFunc(st.Changes, func(c jam.DraftChange) bool { return c.Path == kittree.KitFile }) {
		yml, ymlChanged := wanted, wantChanged
		if want != kittree.KitFile && inDraft(kittree.KitFile) {
			if yml, ymlChanged, err = readDiffFile(store, kit, kittree.KitFile, st); err != nil {
				return err
			}
		}
		v.YAMLDiff = draftYAMLDiff(st.Base, base.KitYML, yml, ymlChanged)
	}
	if want != "" {
		v.FileDiff = fileView(want, baseView.ctx, draft.ctx, true)
		v.FileDiff.From = st.Base
		if wantChanged && !v.FileDiff.Missing {
			v.FileDiff.Note, v.FileDiff.Lines, v.FileDiff.TooLarge = draftChangedNote, nil, false
		}
	}
	return nil
}

// draftYAMLDiff is kit.yml's line diff from base version n's (from) to the
// draft's (to; nil: the draft has none, compared as empty), both masked
// alike; changed: the draft moved while loading, so the note alone.
func draftYAMLDiff(n int, from []byte, to *draftFileData, changed bool) *kitDiff {
	d := &kitDiff{From: n}
	if changed {
		d.Note = draftChangedNote
		return d
	}
	var toData []byte
	if to != nil {
		toData = to.data
	}
	fromText, fromHow := maskKitYML(string(from))
	toText, toHow := maskKitYML(string(toData))
	d.Lines, d.TooLarge = yamlDiff(fromText, toText)
	d.Hidden = fromHow == maskWhole || toHow == maskWhole
	return d
}
