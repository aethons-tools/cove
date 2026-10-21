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
	fail := func(w http.ResponseWriter, err error) {
		status := http.StatusBadGateway
		if errors.Is(err, ErrNotTracked) {
			status = http.StatusConflict
		} else {
			status = WriteStatus(err, status)
		}
		http.Error(w, err.Error(), status)
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
				fail(w, err)
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
				fail(w, err)
				return
			}
			log.Info("admin kit tracked", "operator", OperatorID(r), "kit", res.Name, "source", src.String(), "version", res.Version)
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("DELETE /admin/kits/{name}/source", func(w http.ResponseWriter, r *http.Request) {
			if err := UntrackKit(store, log, r.PathValue("name")); err != nil {
				fail(w, err)
				return
			}
			log.Info("admin kit untracked", "operator", OperatorID(r), "kit", r.PathValue("name"))
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("POST /admin/kits/{name}/sync", func(w http.ResponseWriter, r *http.Request) {
			res, err := SyncKit(r.Context(), store, f, log, r.PathValue("name"), time.Now())
			if err != nil {
				fail(w, err)
				return
			}
			log.Info("admin kit synced", "operator", OperatorID(r), "kit", res.Name, "version", res.Version, "unchanged", res.Unchanged)
			writeJSON(w, http.StatusOK, res)
		})
	}
}
