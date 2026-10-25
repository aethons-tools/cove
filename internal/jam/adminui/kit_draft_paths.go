package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// sameDraftETag reports whether two draft ETags name one tree, read as jam
// reads If-Match: surrounding space trimmed, one balanced pair of quotes
// optional. An empty or unbalanced tag matches nothing.
func sameDraftETag(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		if inner, quoted := strings.CutPrefix(s, `"`); quoted {
			if s, quoted = strings.CutSuffix(inner, `"`); !quoted {
				return ""
			}
		}
		return s
	}
	na := norm(a)
	return na != "" && na == norm(b)
}

// registerDraftPaths mounts Add file, Rename and Delete.
func registerDraftPaths(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	// Add: the path from the form (trimmed), an empty text file or the
	// upload. It never overwrites: a file already at the path, at the etag
	// this form was loaded with, is refused; at another etag the put is stale.
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/add", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, true)
		if !ok {
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		p, etag := strings.TrimSpace(r.FormValue("path")), r.FormValue("etag")
		data, _, err := uploadedFile(r)
		if err != nil {
			renderError(w, http.StatusBadRequest, "the upload could not be read")
			return
		}
		_, _, at, err := jam.KitDraftFile(store, name, p)
		switch status := jam.WriteStatus(err, http.StatusInternalServerError); {
		case err == nil && sameDraftETag(at, etag):
			renderError(w, http.StatusConflict, fmt.Sprintf("draft %s already has a file %s", name, pathText(p)))
			return
		case err != nil && status != http.StatusNotFound:
			draftUIFail(w, r, log, err) // an unsafe path: jam's fixed text, unechoed
			return
		}
		s, err := jam.PutKitDraftFile(store, log, name, p, formMode(r), data, etag, jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{File: p}, "added "+pathText(p))
	})

	// Rename: from is the editor's hidden field, to the input (trimmed);
	// jam checks both paths before any text names them.
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/rename", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		from, to := r.FormValue("from"), strings.TrimSpace(r.FormValue("to"))
		s, err := jam.RenameKitDraftFile(store, log, name, from, to, r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{File: to}, "renamed "+pathText(from)+" to "+pathText(to))
	})

	// Delete: the form asks for confirmation (hx-confirm) and carries the
	// etag the file was read at.
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/delete", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		p := r.FormValue("path")
		s, err := jam.RemoveKitDraftFile(store, log, name, p, r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{}, "removed "+pathText(p))
	})
}
