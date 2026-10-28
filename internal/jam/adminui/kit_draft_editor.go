package adminui

import (
	"bytes"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// maxDraftEditBytes is the largest file the Draft tab edits as text (spec
// §3: "text editor for UTF-8 files ≤ 1 MiB"); a larger one is downloaded
// and replaced.
const maxDraftEditBytes = 1 << 20

// draftEditor is one draft file, opened: its text in the editor, or why not.
type draftEditor struct {
	Path         string // display text
	RawPath      string // the path itself, for the forms' hidden fields
	Mode         string
	Exec         bool
	Size         int64
	ETag         string // the draft's ETag the file was read at: its forms send it back
	Editable     bool
	Text         string // the file's exact text (Editable)
	Invisible    bool   // Text holds control or format characters (a BOM, a bidi override)
	Note         string // why the file is not editable
	Stale        bool   // a refused stale save: Text is the submitted text, unsaved
	StaleMsg     string
	ReloadHref   string
	DownloadHref string
	DiffHref     string
}

func draftDownloadHref(kit, p string) string {
	return draftURL(kit) + "/download?path=" + url.QueryEscape(p)
}

// draftEditorFor reads file p of kit's draft for the editor. Text is edited
// only when it is valid text (kittree.IsText) of at most maxDraftEditBytes
// with no CR: a save folds every CR into LF (editorText), so a CR could not
// survive the round trip.
func draftEditorFor(store jam.Store, kit, p string) (*draftEditor, error) {
	data, e, etag, err := jam.KitDraftFile(store, kit, p)
	if err != nil {
		return nil, err
	}
	ed := &draftEditor{
		Path: pathText(p), RawPath: p, Mode: fileModeText(e.Mode), Exec: e.Mode == 0o755, Size: e.Size, ETag: etag,
		DownloadHref: draftDownloadHref(kit, p), DiffHref: draftFileHref(kit, p, true),
	}
	switch {
	case len(data) > maxDraftEditBytes:
		ed.Note = "over 1 MiB (" + bytesText(e.Size) + "): too large to edit here — Download it, or Replace it with an upload"
	case p == kittree.KitFile && maskedKitYML(data):
		ed.Note = "This kit.yml holds a packed context; download, remove it, and replace"
	case !kittree.IsText(data):
		ed.Note = "binary file (" + bytesText(e.Size) + "): not editable here — Download it, or Replace it with an upload"
	case bytes.IndexByte(data, '\r') >= 0:
		ed.Note = "contains carriage returns (CRLF line ends): the browser editor would rewrite them as LF — Download it, edit it locally, and Replace it"
	default:
		ed.Editable, ed.Text = true, string(data)
		ed.Invisible = jam.SetupDisplayText(ed.Text) != ed.Text
	}
	return ed, nil
}

// maskedKitYML reports whether kit.yml text data may hold a packed context
// the editor must not show: one maskKitYML masks in place, or a text it hides
// whole that also carries a long base64 run. A text hidden whole only for
// mentioning "context" (say a half-edited draft that doesn't parse) opens.
func maskedKitYML(data []byte) bool {
	switch _, how := maskKitYML(string(data)); how {
	case maskPartial:
		return true
	case maskWhole:
		return base64Run.Match(data)
	}
	return false
}

// base64Run matches a run of base64 long enough to be a packed context.
var base64Run = regexp.MustCompile(`[A-Za-z0-9+/]{48,}={0,2}`)

// openDraftFile sets v's editor to file p, or FileMissing when the draft has
// no such file or p is unsafe (never named); another error is a store fault.
func openDraftFile(store jam.Store, kit, p string, v *draftView) error {
	ed, err := draftEditorFor(store, kit, p)
	switch status := jam.WriteStatus(err, http.StatusInternalServerError); {
	case err == nil:
		v.Editor = ed
	case status == http.StatusNotFound || status == http.StatusBadRequest:
		v.FileMissing = true
	default:
		return err
	}
	return nil
}

// staleEditor is the editor after a refused stale save of p: the submitted
// text kept, the form's own stale etag kept (so Save stays refused), and the
// fixed message with a reload link. Nothing was saved.
func staleEditor(kit, p, text, etag string, mode fs.FileMode, msg string) *draftEditor {
	return &draftEditor{
		Path: pathText(p), RawPath: p, Mode: fileModeText(mode), Exec: mode == 0o755, Size: int64(len(text)), ETag: etag,
		Editable: true, Text: text, Invisible: jam.SetupDisplayText(text) != text,
		Stale: true, StaleMsg: msg, ReloadHref: draftFileHref(kit, p, false),
		DownloadHref: draftDownloadHref(kit, p), DiffHref: draftFileHref(kit, p, true),
	}
}

// editorText is a submitted textarea's text as the file's bytes, with each
// CRLF, and any lone CR, as LF. htmx 2 submits the textarea's value, whose
// line breaks the browser normalises to LF; a native form submission sends
// them as CRLF. Folding both makes either submission store the same bytes.
func editorText(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// formMode is a form's Executable checkbox as a file mode.
func formMode(r *http.Request) fs.FileMode {
	if r.FormValue("executable") != "" {
		return 0o755
	}
	return 0o644
}

// savedMsg is a file write's flash message.
func savedMsg(p string, s jam.KitDraftSave) string {
	if len(s.Changed) == 0 {
		return pathText(p) + " unchanged"
	}
	return "saved " + pathText(p)
}

// logDraftSave is a UI draft write's audit line, only when it changed the
// tree: never content or a path.
func logDraftSave(log *slog.Logger, r *http.Request, name string, s jam.KitDraftSave) {
	if len(s.Changed) == 0 {
		return
	}
	log.Info("ui kit draft saved", "operator", jam.OperatorID(r), "kit", name, "tree", shortDigest(s.Tree), "changed", len(s.Changed))
}

// registerDraftEditor mounts the editor's save and the file download.
func registerDraftEditor(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/save", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		p, etag, mode := r.FormValue("path"), r.FormValue("etag"), formMode(r)
		text := editorText(r.FormValue("content"))
		s, err := jam.PutKitDraftFile(store, log, name, p, mode, []byte(text), etag, jam.OperatorID(r), time.Now().UTC())
		if err != nil && jam.WriteStatus(err, http.StatusInternalServerError) == http.StatusPreconditionFailed {
			renderStaleSave(w, r, store, log, name, p, text, etag, mode, err)
			return
		}
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{File: p}, savedMsg(p, s))
	})

	mux.HandleFunc("GET /app/admin/kits/{name}/draft/download", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		data, _, _, err := jam.KitDraftFile(store, r.PathValue("name"), p)
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		writeDraftDownload(w, p, data)
	})
}

// writeDraftDownload answers a draft file's download: its bytes as an
// attachment named after the file, never sniffed or cached.
func writeDraftDownload(w http.ResponseWriter, p string, data []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	disp := mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(p)})
	if disp == "" {
		disp = "attachment"
	}
	h.Set("Content-Disposition", disp)
	_, _ = w.Write(data)
}

// editorKeptText tells the operator a refused save left their text unsaved
// in the editor.
const editorKeptText = "your text is still in the editor — copy it before reloading"

// renderStaleSave answers a save refused as stale (412) with the #draft body
// as it is now, its editor holding the submitted text unsaved (staleEditor).
// It answers 200, since htmx swaps only 2xx. Should the draft be gone by
// now, the 412 itself is answered, and htmx leaves the typed text in place.
func renderStaleSave(w http.ResponseWriter, r *http.Request, store jam.Store, log *slog.Logger, name, p, text, etag string, mode fs.FileMode, stale error) {
	pg, ok := loadDraftPage(w, r, store, log, name, draftQuery{})
	if !ok {
		return
	}
	if pg.Draft == nil {
		renderError(w, http.StatusPreconditionFailed, stale.Error()+": "+editorKeptText)
		return
	}
	pg.Draft.Editor = staleEditor(name, p, text, etag, mode, stale.Error()+": "+editorKeptText)
	writeDraftBody(w, r, pg, "")
}
