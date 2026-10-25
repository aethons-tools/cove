package adminui

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// uploadedFile is the multipart form's "file" part: its bytes, read up to
// kittree.MaxFile+1 so an oversize file meets jam's own 413, and whether one
// was sent. Its file name is never read: the path comes from the form.
func uploadedFile(r *http.Request) ([]byte, bool, error) {
	f, _, err := r.FormFile("file")
	if errors.Is(err, http.ErrMissingFile) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, kittree.MaxFile+1))
	return b, true, err
}

// registerDraftUploads mounts Replace (a multipart upload over one draft
// file) and the Executable toggle of a file the editor does not open.
func registerDraftUploads(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/replace", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, true)
		if !ok {
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		data, sent, err := uploadedFile(r)
		switch {
		case err != nil:
			renderError(w, http.StatusBadRequest, "the upload could not be read")
			return
		case !sent:
			renderError(w, http.StatusBadRequest, "choose a file to upload")
			return
		}
		p := r.FormValue("path")
		s, err := jam.PutKitDraftFile(store, log, name, p, formMode(r), data, r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		renderDraftBody(w, r, store, log, name, draftQuery{File: p}, savedMsg(p, s))
	})

	// The file is re-put with its own bytes at the new mode, against the
	// form's etag: a save since the form was loaded makes it stale.
	mux.HandleFunc("POST /app/admin/kits/{name}/draft/mode", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		p, mode := r.FormValue("path"), formMode(r)
		data, _, _, err := jam.KitDraftFile(store, name, p)
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		s, err := jam.PutKitDraftFile(store, log, name, p, mode, data, r.FormValue("etag"), jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		logDraftSave(log, r, name, s)
		msg := pathText(p) + " is now " + fileModeText(mode)
		if len(s.Changed) == 0 {
			msg = savedMsg(p, s)
		}
		renderDraftBody(w, r, store, log, name, draftQuery{File: p}, msg)
	})
}
