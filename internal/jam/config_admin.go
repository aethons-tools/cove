package jam

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"time"

	"github.com/aethons-tools/cove/internal/jam/configtar"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

const configStoreError = "config store error"

// WithConfig mounts the config-in-git admin API (under the /admin/* operator
// gate):
//
//	POST /admin/config/diff?force=1                plan an uploaded tree (writes nothing)
//	POST /admin/config/apply?force=1&plan=<digest> apply it
//	GET  /admin/config/export-tree?all=1           render the managed (all: every) object
//
// diff and apply take a configtar body (application/gzip). An oversize
// upload is 413, a malformed or unsafe one 400 — both before the tree is
// loaded. An apply with refusals or a stale plan digest is 409, as is an
// export-tree of a kit pinned behind its in-tree parent (ErrConfigKitPinBehind). env.Serve is
// the running serve's portable config, compared with the tree's jam.yml.
func WithConfig(store Store, env ConfigEnv, log *slog.Logger) AdminOption {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// tree reads and unpacks the upload into a temp dir and loads it,
	// answering the request itself on failure.
	tree := func(w http.ResponseWriter, r *http.Request) (*ConfigTree, bool) {
		if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct == "application/json" {
			http.Error(w, "config sources (--git) are not supported yet; use --dir", http.StatusBadRequest)
			return nil, false
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, configtar.MaxUpload))
		if err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				http.Error(w, "config tree too large (limit 16 MiB)", http.StatusRequestEntityTooLarge)
				return nil, false
			}
			http.Error(w, "config tree: reading the upload failed", http.StatusBadRequest)
			return nil, false
		}
		dir, err := os.MkdirTemp("", "jam-config-")
		if err != nil {
			log.Error("admin config: temp dir", "operator", OperatorID(r), "err", err.Error())
			http.Error(w, configStoreError, http.StatusInternalServerError)
			return nil, false
		}
		defer os.RemoveAll(dir)
		if err := configtar.Unpack(bytes.NewReader(body), dir); err != nil {
			if errors.Is(err, configtar.ErrTooLarge) {
				http.Error(w, "config tree too large (limit 16 MiB)", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return nil, false
		}
		tr, err := LoadConfigTree(dir)
		if err != nil {
			log.Error("admin config: load", "operator", OperatorID(r), "err", err.Error())
			http.Error(w, configStoreError, http.StatusInternalServerError)
			return nil, false
		}
		return tr, true
	}
	opts := func() ConfigApplyOptions {
		return ConfigApplyOptions{Source: gitsource.Source{Repo: ConfigDirRepo}, Commit: ConfigDirCommit, Now: time.Now()}
	}
	return func(mux *http.ServeMux) {
		mux.HandleFunc("POST /admin/config/diff", func(w http.ResponseWriter, r *http.Request) {
			tr, ok := tree(w, r)
			if !ok {
				return
			}
			opt := opts()
			opt.DryRun = true
			opt.Force = r.URL.Query().Get("force") == "1" // plan as a forced apply would
			res, err := ApplyConfigTree(store, tr, env, opt, log, OperatorID(r))
			if err != nil {
				log.Error("admin config diff: store error", "operator", OperatorID(r), "err", err.Error())
				http.Error(w, configStoreError, http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("POST /admin/config/apply", func(w http.ResponseWriter, r *http.Request) {
			tr, ok := tree(w, r)
			if !ok {
				return
			}
			opt := opts()
			opt.Force = r.URL.Query().Get("force") == "1"
			opt.WantDigest = r.URL.Query().Get("plan")
			res, err := ApplyConfigTree(store, tr, env, opt, log, OperatorID(r))
			switch {
			case errors.Is(err, ErrConfigRefused), errors.Is(err, ErrConfigPlanChanged):
				http.Error(w, err.Error(), http.StatusConflict)
				return
			case err != nil:
				log.Error("admin config apply: store error", "operator", OperatorID(r), "err", err.Error())
				http.Error(w, configStoreError, http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("GET /admin/config/export-tree", func(w http.ResponseWriter, r *http.Request) {
			exp, err := ExportConfigTree(store.ExportConfig(), store.ListConfigMarks(), env.Serve, r.URL.Query().Get("all") == "1")
			if errors.Is(err, ErrConfigKitPinBehind) {
				http.Error(w, err.Error(), http.StatusConflict) // kit names and versions only
				return
			}
			if err != nil {
				log.Error("admin config export-tree: store error", "operator", OperatorID(r), "err", err.Error())
				http.Error(w, configStoreError, http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, exp)
		})
	}
}
