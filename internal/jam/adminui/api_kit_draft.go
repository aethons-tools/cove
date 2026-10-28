package adminui

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// The kit draft JSON API: the Draft section's twin of the kit_draft*.go
// routes, through the same jam draft functions (each write sends the draft's
// etag as If-Match). Uploads stay multipart, through draftForm.

func draftRows(rows []draftRow) []DraftRow {
	out := []DraftRow{}
	for _, r := range rows {
		out = append(out, DraftRow{Depth: r.Depth, Name: r.Name, Dir: r.Dir, Path: r.Raw, Label: r.Path, Mode: r.Mode, Size: r.Size, Change: r.Change})
	}
	return out
}

func filesDiffOf(x *ctxDiffView) *FilesDiff {
	if x == nil {
		return nil
	}
	out := &FilesDiff{From: x.From, To: x.To, Rows: []FileChange{}, More: x.More, Unchanged: x.Unchanged}
	for _, r := range x.Rows {
		out.Rows = append(out.Rows, FileChange{Change: r.Change, Path: r.Raw, Label: r.Path, Mode: r.Mode, Size: r.Size})
	}
	return out
}

func draftPageOf(p draftPage) DraftPage {
	out := DraftPage{Name: p.Name, Current: p.Current, Versions: append([]int{}, p.Versions...)}
	if v := p.Draft; v != nil {
		d := &DraftView{Base: v.Base, Current: v.Current, Superseded: v.Superseded, ETag: v.ETag, Actor: v.Actor, Updated: v.Updated,
			Managed: v.Managed, DriftWarning: v.DriftWarning, DriftConfirm: v.DriftConfirm, PinDrift: append([]string{}, v.PinDrift...),
			PinDriftNote: v.PinDriftNote, MaxFile: v.MaxFile, Problems: []string{}, Size: v.Size, Tree: draftRows(v.Tree), Removed: draftRows(v.Removed),
			FileMissing: v.FileMissing, Changes: filesDiffOf(v.Changes), FileDiff: kitFileOf(v.FileDiff)}
		for _, pr := range v.Problems {
			d.Problems = append(d.Problems, jam.SetupDisplayText(pr))
		}
		if e := v.Editor; e != nil {
			d.Editor = &DraftEditor{Path: e.RawPath, Label: e.Path, Mode: e.Mode, Exec: e.Exec, Size: e.Size, ETag: e.ETag, Editable: e.Editable,
				Text: e.Text, Invisible: e.Invisible, Note: e.Note}
		}
		if y := v.YAMLDiff; y != nil {
			d.YAMLDiff = &DraftYAMLDiff{From: y.From, Lines: diffLines(y.Lines), TooLarge: y.TooLarge, Hidden: y.Hidden, Note: y.Note}
		}
		out.Draft = d
	}
	if pb := p.Published; pb != nil {
		out.Published = &Published{Version: pb.Version, Base: pb.Base, Pinned: append([]string{}, pb.Pinned...), Warning: pb.Warning, Drift: pb.Drift, ConfigFiles: []ConfigFileRow{}}
		for _, f := range pb.ConfigFiles {
			out.Published.ConfigFiles = append(out.Published.ConfigFiles, ConfigFileRow{Path: f.Path, Change: f.Change})
		}
	}
	return out
}

// draftFailJSON is draftUIFail for the JSON API: a jam.WriteError's own
// status and fixed text, else a fixed 500, logged.
func draftFailJSON(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if we, ok := errors.AsType[*jam.WriteError](err); ok {
		writeAPIError(w, we.Status, apiCode(we.Status), we.Msg)
		return
	}
	log.Error("ui kit draft: store error", "operator", jam.OperatorID(r), "method", r.Method, "route", r.Pattern, "err", err.Error())
	writeAPIError(w, http.StatusInternalServerError, "internal", "internal error")
}

// draftJSON runs a JSON draft write's preamble: the kit's existence (404),
// the body cap (413) and the decode into v.
func draftJSON(w http.ResponseWriter, r *http.Request, store jam.Store, v any) (string, bool) {
	name := r.PathValue("name")
	if _, ok := store.GetKit(name); !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", fmt.Sprintf("kit %q does not exist", name))
		return "", false
	}
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "bad_request", "send JSON (Content-Type: application/json)")
		return "", false
	}
	limit := draftFormLimit()
	if r.ContentLength > limit {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "too_large", draftTooLarge(name))
		return "", false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		if _, big := errors.AsType[*http.MaxBytesError](err); big {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "too_large", draftTooLarge(name))
			return "", false
		}
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return "", false
	}
	return name, true
}

// draftMultipart runs an upload's preamble through draftForm (its origin
// check already done by the API), answering its refusals as JSON.
func draftMultipart(w http.ResponseWriter, r *http.Request, store jam.Store) (string, bool) {
	rec := &errorCapture{ResponseWriter: w}
	name, ok := draftForm(rec, r, store, func(http.ResponseWriter, *http.Request) bool { return true }, true)
	if !ok {
		writeAPIError(w, rec.status, apiCode(rec.status), rec.text())
	}
	return name, ok
}

// errorCapture holds an HTML refusal (renderError's) so it can be answered
// as JSON instead.
type errorCapture struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (c *errorCapture) WriteHeader(status int) { c.status = status }
func (c *errorCapture) Write(b []byte) (int, error) {
	c.body = append(c.body, b...)
	return len(b), nil
}
func (c *errorCapture) Header() http.Header { return http.Header{} }

// text is renderError's message, unwrapped from its <p class="error">.
func (c *errorCapture) text() string {
	s := strings.TrimSuffix(strings.TrimPrefix(string(c.body), `<p class="error">`), "</p>")
	return html.UnescapeString(s)
}

func apiKitDraft(api *http.ServeMux, store jam.Store, log *slog.Logger) {
	base := apiPrefix + "kits/{name}/draft"
	answer := func(w http.ResponseWriter, r *http.Request, name string, q draftQuery, msg string, published *publishView) {
		p, ok, err := buildDraftPage(store, name, q)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "kit no longer exists")
			return
		}
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		p.Published = published
		writeJSON(w, http.StatusOK, DraftResult{Page: draftPageOf(p), Message: msg})
	}
	api.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		q := draftQuery{File: r.URL.Query().Get("file"), Diff: r.URL.Query().Get("diff") == "1"}
		p, ok, err := buildDraftPage(store, name, q)
		switch {
		case !ok:
			writeAPIError(w, http.StatusNotFound, "not_found", "no such kit")
		case err != nil:
			draftFailJSON(w, r, log, err)
		default:
			writeJSON(w, http.StatusOK, draftPageOf(p))
		}
	})
	api.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ From int }
		name, ok := draftJSON(w, r, store, &body)
		if !ok {
			return
		}
		d, err := jam.OpenKitDraft(store, name, body.From, jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		log.Info("ui kit draft opened", "operator", jam.OperatorID(r), "kit", name, "base", d.Base, "tree", shortDigest(d.Tree))
		answer(w, r, name, draftQuery{}, fmt.Sprintf("opened a draft of %s from v%d", name, d.Base), nil)
	})
	api.HandleFunc("POST "+base+"/discard", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ETag string }
		name, ok := draftJSON(w, r, store, &body)
		if !ok {
			return
		}
		d, err := jam.DiscardKitDraft(store, log, name, body.ETag)
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		log.Info("ui kit draft discarded", "operator", jam.OperatorID(r), "kit", name, "tree", shortDigest(d.Tree))
		answer(w, r, name, draftQuery{}, "discarded the draft of "+name, nil)
	})
	api.HandleFunc("POST "+base+"/save", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ETag, Path, Content string
			Executable          bool
		}
		name, ok := draftJSON(w, r, store, &body)
		if !ok {
			return
		}
		s, err := jam.PutKitDraftFile(store, log, name, body.Path, modeOf(body.Executable), []byte(editorText(body.Content)), body.ETag, jam.OperatorID(r), time.Now().UTC())
		if err != nil && jam.WriteStatus(err, http.StatusInternalServerError) == http.StatusPreconditionFailed {
			// Nothing saved; the app keeps the typed text in the editor.
			writeAPIError(w, http.StatusPreconditionFailed, "stale", err.Error()+": "+editorKeptText)
			return
		}
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		answer(w, r, name, draftQuery{File: body.Path}, savedMsg(body.Path, s), nil)
	})
	api.HandleFunc("POST "+base+"/rename", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ETag, From, To string }
		name, ok := draftJSON(w, r, store, &body)
		if !ok {
			return
		}
		to := strings.TrimSpace(body.To)
		s, err := jam.RenameKitDraftFile(store, log, name, body.From, to, body.ETag, jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		answer(w, r, name, draftQuery{File: to}, "renamed "+pathText(body.From)+" to "+pathText(to), nil)
	})
	api.HandleFunc("POST "+base+"/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ETag, Path string }
		name, ok := draftJSON(w, r, store, &body)
		if !ok {
			return
		}
		s, err := jam.RemoveKitDraftFile(store, log, name, body.Path, body.ETag, jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		answer(w, r, name, draftQuery{}, "removed "+pathText(body.Path), nil)
	})
	api.HandleFunc("POST "+base+"/mode", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ETag, Path string
			Executable bool
		}
		name, ok := draftJSON(w, r, store, &body)
		if !ok {
			return
		}
		mode := modeOf(body.Executable)
		data, _, _, err := jam.KitDraftFile(store, name, body.Path)
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		s, err := jam.PutKitDraftFile(store, log, name, body.Path, mode, data, body.ETag, jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		msg := pathText(body.Path) + " is now " + fileModeText(mode)
		if len(s.Changed) == 0 {
			msg = savedMsg(body.Path, s)
		}
		answer(w, r, name, draftQuery{File: body.Path}, msg, nil)
	})
	api.HandleFunc("POST "+base+"/add", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftMultipart(w, r, store)
		if !ok {
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		p := strings.TrimSpace(r.FormValue("path"))
		data, _, err := uploadedFile(r)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "the upload could not be read")
			return
		}
		s, err := jam.AddKitDraftFile(store, log, name, p, data, formMode(r), r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		answer(w, r, name, draftQuery{File: p}, "added "+pathText(p), nil)
	})
	api.HandleFunc("POST "+base+"/replace", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftMultipart(w, r, store)
		if !ok {
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		data, sent, err := uploadedFile(r)
		switch {
		case err != nil:
			writeAPIError(w, http.StatusBadRequest, "bad_request", "the upload could not be read")
			return
		case !sent:
			writeAPIError(w, http.StatusBadRequest, "bad_request", "choose a file to upload")
			return
		}
		p := r.FormValue("path")
		s, err := jam.PutKitDraftFile(store, log, name, p, formMode(r), data, r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		answer(w, r, name, draftQuery{File: p}, savedMsg(p, s), nil)
	})
	api.HandleFunc("POST "+base+"/publish", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ETag       string
			Pin, Drift bool
		}
		name, ok := draftJSON(w, r, store, &body)
		if !ok {
			return
		}
		if !body.Drift {
			if jam.KitManaged(store, name) {
				writeAPIError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("kit %s is managed by the config repo: tick “%s” to publish", name, draftDriftConfirm))
				return
			}
			if managed := managedFollowersToPin(store, name); body.Pin && len(managed) > 0 {
				writeAPIError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("pinning followers drifts config-managed %s from the config repo: tick “%s” to publish", strings.Join(managed, ", "), draftDriftConfirm))
				return
			}
		}
		k, _ := store.GetKit(name)
		prev := k.Current
		res, err := jam.PublishKitDraft(store, log, name, body.Pin, body.ETag)
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		log.Info("ui kit draft published", "operator", jam.OperatorID(r), "kit", name, "version", res.Version, "base", res.Base, "tree", shortDigest(res.Tree), "pinned", len(res.Pinned), "drift", res.Drift)
		answer(w, r, name, draftQuery{}, fmt.Sprintf("published %s v%d", name, res.Version), newPublishView(store, name, prev, res))
	})
	api.HandleFunc("GET "+base+"/download", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		data, _, _, err := jam.KitDraftFile(store, r.PathValue("name"), p)
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		writeDraftDownload(w, p, data)
	})
}

// modeOf is an Executable flag as a file mode.
func modeOf(exec bool) fs.FileMode {
	if exec {
		return 0o755
	}
	return 0o644
}
