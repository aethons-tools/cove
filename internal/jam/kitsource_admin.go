package jam

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// KitImportBody is the POST /admin/kits/import request. Name is optional (the
// kit's own name, else its directory's or repo's, is used).
type KitImportBody struct {
	Name   string `json:"name,omitempty"`
	Source string `json:"source"`
}

// KitSourceBody is the PUT /admin/kits/{name}/source request.
type KitSourceBody struct {
	Source string `json:"source"`
}

// KitVersionInfo is a GET /admin/kits/{name}/history item: one version with
// its provenance (Source nil = pushed from a local file, or a legacy version).
type KitVersionInfo struct {
	Version  int        `json:"version"`
	PushedAt time.Time  `json:"pushed_at,omitzero"`
	Source   *KitSource `json:"source,omitempty"`
	Current  bool       `json:"current,omitempty"`
}

// WithKitFetcher mounts the git-sourced kit admin API (under the /admin/*
// operator gate):
//
//	POST   /admin/kits/import         {name?, source} import a kit from git
//	PUT    /admin/kits/{name}/source  {source} import and track it
//	DELETE /admin/kits/{name}/source  stop tracking (versions are kept)
//	POST   /admin/kits/{name}/sync    re-import the tracked source
//
// An unparseable source or an invalid kit is 400, an unknown kit 404, a kit
// without a tracked source 409, and a fetch, auth or limit failure 502 with
// the fetcher's already-redacted message. Audit logs carry the operator and
// the source, never a credential.
func WithKitFetcher(store Store, f KitFetcher, log *slog.Logger) AdminOption {
	// fail answers err: validation 400, unknown kit 404, untracked 409, a
	// fetch, auth or limit failure 502 with the fetcher's already-redacted
	// message; anything else is a store failure, 500 with a generic body.
	fail := func(w http.ResponseWriter, r *http.Request, err error) {
		var we *WriteError
		switch {
		case errors.Is(err, ErrNotTracked):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.As(err, &we), errors.Is(err, ErrNotFound):
			http.Error(w, err.Error(), WriteStatus(err, http.StatusInternalServerError))
		case errors.Is(err, gitsource.ErrFetch):
			http.Error(w, err.Error(), http.StatusBadGateway)
		default:
			log.Error("admin kit source: store error", "operator", OperatorID(r), "err", err.Error())
			http.Error(w, "kit store error", http.StatusInternalServerError)
		}
	}
	// source decodes a {source} body and parses it, answering 400 itself.
	source := func(w http.ResponseWriter, s string) (gitsource.Source, bool) {
		src, err := gitsource.Parse(s)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return gitsource.Source{}, false
		}
		return src, true
	}
	return func(mux *http.ServeMux) {
		mux.HandleFunc("POST /admin/kits/import", func(w http.ResponseWriter, r *http.Request) {
			var b KitImportBody
			if !decode(w, r, &b) {
				return
			}
			src, ok := source(w, b.Source)
			if !ok {
				return
			}
			res, err := ImportKit(r.Context(), store, f, log, b.Name, src, time.Now())
			if err != nil {
				fail(w, r, err)
				return
			}
			log.Info("admin kit imported", "operator", OperatorID(r), "kit", res.Name, "source", src.String(), "version", res.Version)
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("PUT /admin/kits/{name}/source", func(w http.ResponseWriter, r *http.Request) {
			var b KitSourceBody
			if !decode(w, r, &b) {
				return
			}
			src, ok := source(w, b.Source)
			if !ok {
				return
			}
			res, err := TrackKit(r.Context(), store, f, log, r.PathValue("name"), src, time.Now())
			if err != nil {
				fail(w, r, err)
				return
			}
			log.Info("admin kit tracked", "operator", OperatorID(r), "kit", res.Name, "source", src.String(), "version", res.Version)
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("DELETE /admin/kits/{name}/source", func(w http.ResponseWriter, r *http.Request) {
			if err := UntrackKit(store, log, r.PathValue("name")); err != nil {
				fail(w, r, err)
				return
			}
			log.Info("admin kit untracked", "operator", OperatorID(r), "kit", r.PathValue("name"))
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("POST /admin/kits/{name}/sync", func(w http.ResponseWriter, r *http.Request) {
			res, err := SyncKit(r.Context(), store, f, log, r.PathValue("name"), time.Now())
			if err != nil {
				fail(w, r, err)
				return
			}
			log.Info("admin kit synced", "operator", OperatorID(r), "kit", res.Name, "version", res.Version, "unchanged", res.Unchanged)
			writeJSON(w, http.StatusOK, res)
		})
	}
}
