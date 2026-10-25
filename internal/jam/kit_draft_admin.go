package jam

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"
)

// registerKitDrafts mounts the kit draft API (docs/usage/jam/kits-drafts.md)
// on the admin mux, behind the same operator gate:
//
//	POST   /admin/kits/{name}/draft   {from?}  open (201, the status)
//	GET    /admin/kits/{name}/draft            status (ETag header)
//	DELETE /admin/kits/{name}/draft            discard (If-Match; 204)
//
// Every write but open needs If-Match (the draft's ETag: 428 without, 412
// stale). Open and publish take JSON only, a file write octet-stream only,
// a tree replace a tar only (415 otherwise, before any body is read), so no
// browser no-cors request can write. Audit logs carry the kit, the operator
// and tree prefixes, never content.
func registerKitDrafts(mux *http.ServeMux, store Store, log *slog.Logger) {
	mux.HandleFunc("POST /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		if !requireContentType(w, r, "application/json") {
			return
		}
		var b KitDraftOpenBody
		if !decodeOptional(w, r, &b) {
			return
		}
		name := r.PathValue("name")
		d, err := OpenKitDraft(store, name, b.From, OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		log.Info("kit draft opened", "operator", OperatorID(r), "kit", name, "base", d.Base, "tree", shortTree(d.Tree))
		writeDraftStatus(w, r, store, log, name, http.StatusCreated)
	})
	mux.HandleFunc("GET /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		writeDraftStatus(w, r, store, log, r.PathValue("name"), http.StatusOK)
	})
	mux.HandleFunc("DELETE /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		d, err := DiscardKitDraft(store, log, name, r.Header.Get("If-Match"))
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		log.Info("kit draft discarded", "operator", OperatorID(r), "kit", name, "tree", shortTree(d.Tree))
		w.WriteHeader(http.StatusNoContent)
	})
}

// writeDraftStatus answers kit name's draft status with its ETag.
func writeDraftStatus(w http.ResponseWriter, r *http.Request, store Store, log *slog.Logger, name string, code int) {
	st, err := KitDraftStatusOf(store, name)
	if err != nil {
		draftFail(w, r, log, err)
		return
	}
	w.Header().Set("ETag", st.ETag)
	writeJSON(w, code, st)
}

// draftInternalError is the fixed answer to a draft failure that is not
// the operator's (a store error): its text goes to the log, never the client.
const draftInternalError = "internal error"

// draftFail answers err: a WriteError's own status and fixed text, else a
// fixed 500, the error logged (it names a kit or a tree prefix, no content).
func draftFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if we, ok := errors.AsType[*WriteError](err); ok {
		http.Error(w, we.Msg, we.Status)
		return
	}
	log.Error("kit draft: store error", "operator", OperatorID(r), "err", err.Error())
	http.Error(w, draftInternalError, http.StatusInternalServerError)
}

// requireContentType refuses (415) a request whose media type is not want,
// before its body is read.
func requireContentType(w http.ResponseWriter, r *http.Request, want string) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != want {
		http.Error(w, "kit draft: Content-Type must be "+want, http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// decodeOptional decodes a small JSON body into v; an empty body leaves v zero.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

// shortTree is a tree hash's 12-hex prefix, for logs.
func shortTree(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
