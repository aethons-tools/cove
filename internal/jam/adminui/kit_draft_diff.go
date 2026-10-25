package adminui

import (
	"io/fs"
	"slices"
	"strconv"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// draftRead is a draft's files in the shape the kit page reads a version's
// (ctxRead), from its status entries. File want carries data's first
// maxFileShow+1 bytes; a kit.yml is masked (maskKitYML), as the kit page
// shows a stored one.
func draftRead(st jam.KitDraftStatus, want string, data []byte) *ctxRead {
	c := &ctxRead{byPath: make(map[string]int, len(st.Entries))}
	var total int64
	for i, e := range st.Entries {
		mode, _ := strconv.ParseUint(e.Mode, 8, 32)
		ent := ctxEntry{Path: e.Path, Mode: fs.FileMode(mode), Size: e.Size, Sum: e.SHA256}
		if e.Path == want && data != nil {
			ent.Data, ent.Mask = maskedKitData(e.Path, data)
		}
		total += e.Size
		c.files = append(c.files, ent)
		c.byPath[e.Path] = i
	}
	c.size = sizeLabel(int(total))
	return c
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

// draftDiff fills v's diff against the draft's base version: the changed
// files (each row linking its file diff), kit.yml's line diff when it
// changed, and with q.Diff, file q.File's diff. A file the draft or the base
// lacks compares as absent; one neither has is Missing (never named).
//
// Both sides' kit.yml are masked the same way (maskKitYML), whether or not
// the base's parses, so a packed context in the base is never shown and
// masking alone never makes a change.
func draftDiff(store jam.Store, kit string, st jam.KitDraftStatus, base jam.KitVersion, q draftQuery, v *draftView) {
	want := ""
	if q.Diff {
		want = q.File
	}
	baseView := viewKit(st.Base, base, true, want)
	if want == kittree.KitFile {
		if c := baseView.ctx; c != nil {
			if i, ok := c.byPath[want]; ok {
				c.files[i].Data, c.files[i].Mask = maskedKitData(want, base.KitYML)
			}
		}
	}
	var data []byte
	if want != "" {
		data, _, _, _ = jam.KitDraftFile(store, kit, want)
	}
	draft := kitView{ctx: draftRead(st, want, data)}
	v.Changes = ctxDiffLinked(baseView, draft, func(p string) string { return draftFileHref(kit, p, true) })
	if slices.ContainsFunc(st.Changes, func(c jam.DraftChange) bool { return c.Path == kittree.KitFile }) {
		yml, _, _, _ := jam.KitDraftFile(store, kit, kittree.KitFile)
		from, _ := maskKitYML(string(base.KitYML))
		to, _ := maskKitYML(string(yml))
		lines, tooLarge := yamlDiff(from, to)
		v.YAMLDiff = &kitDiff{From: st.Base, Lines: lines, TooLarge: tooLarge}
	}
	if want != "" {
		v.FileDiff = fileView(want, baseView.ctx, draft.ctx, true)
		v.FileDiff.From = st.Base
	}
}
