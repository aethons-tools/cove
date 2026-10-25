package adminui

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// registerDraftPaths mounts Add file, Rename and Delete.
func registerDraftPaths(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	// Add: the path from the form (trimmed), an empty text file or the
	// upload. It never overwrites (jam.AddKitDraftFile): a file already at
	// the path is refused (409), a form loaded before another save is stale.
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
		s, err := jam.AddKitDraftFile(store, log, name, p, data, formMode(r), etag, jam.OperatorID(r), time.Now().UTC())
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
