package jam

import (
	"bytes"
	"context"
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

// ConfigStatus maps a config diff, apply, track or sync error to an HTTP
// status and a message safe to show: a refused or stale plan and an
// untracked Jam 409; a bad source, kit or checkout 400; a fetch, auth or
// limit failure 502 with the fetcher's redacted message; anything else 500
// with a generic message (log the error itself).
func ConfigStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrConfigRefused), errors.Is(err, ErrConfigPlanChanged), errors.Is(err, ErrConfigNotTracked):
		return http.StatusConflict, err.Error()
	}
	status, msg := KitSourceStatus(err)
	if status == http.StatusInternalServerError {
		msg = configStoreError
	}
	return status, msg
}

// ConfigSourceBody is the JSON body of POST /admin/config/diff and /apply
// (Source "" = the tracked source), PUT /admin/config/source and POST
// /admin/config/sync ({}).
type ConfigSourceBody struct {
	Source string `json:"source,omitempty"`
}

// maxConfigSourceBody caps a {source} JSON body.
const maxConfigSourceBody = 64 << 10

// WithConfig mounts the config-in-git admin API (under the /admin/*
// operator gate):
//
//	POST   /admin/config/diff?force=1                plan a tree or a source (writes nothing)
//	POST   /admin/config/apply?force=1&plan=<digest> apply it
//	PUT    /admin/config/source                      {source}: fetch and plan it, then track it
//	DELETE /admin/config/source                      untrack (objects and marks stay)
//	POST   /admin/config/sync?force=1&plan=<digest>  {}: apply the tracked source
//	GET    /admin/config/export-tree?all=1           render the managed (all: every) object
//
// diff and apply take a configtar body (application/gzip) or a {source}
// body (application/json; "" = the tracked source); sync takes {} — any
// other Content-Type is 415, so a browser's no-cors POST cannot trigger
// one. An oversize upload is 413, a malformed or unsafe one or a bad source
// 400, before anything is fetched or loaded. A refused or stale plan, or no
// tracked source, is 409; a fetch, auth or limit failure 502 with the
// fetcher's redacted message. f is the serve's one kit fetcher (kits.git
// credentials); it fetches config repos and kits/<name>.source.yml kits.
// env.Serve is the running serve's portable config.
func WithConfig(store Store, f KitFetcher, env ConfigEnv, log *slog.Logger) AdminOption {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	fail := func(w http.ResponseWriter, r *http.Request, what string, err error) {
		status, msg := ConfigStatus(err)
		if status == http.StatusInternalServerError {
			if errors.Is(err, context.Canceled) {
				log.Debug("admin config "+what+": request cancelled", "operator", OperatorID(r))
			} else {
				log.Error("admin config "+what+": store error", "operator", OperatorID(r), "err", err.Error())
			}
		}
		http.Error(w, msg, status)
	}
	// upload reads and unpacks a configtar body into a temp dir and loads it,
	// answering the request itself on failure.
	upload := func(w http.ResponseWriter, r *http.Request) (*ConfigTree, bool) {
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
	// source decodes a {source} body ("" = the tracked one, unless required)
	// and parses it; Parse's errors never quote the input. It answers 400 itself.
	source := func(w http.ResponseWriter, r *http.Request, required bool) (gitsource.Source, bool) {
		var b ConfigSourceBody
		r.Body = http.MaxBytesReader(w, r.Body, maxConfigSourceBody)
		if !decode(w, r, &b) {
			return gitsource.Source{}, false
		}
		if b.Source == "" {
			if required {
				http.Error(w, "source is required (<repo>[//<dir>]@<ref>)", http.StatusBadRequest)
				return gitsource.Source{}, false
			}
			return gitsource.Source{}, true
		}
		src, err := gitsource.Parse(b.Source)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return gitsource.Source{}, false
		}
		return src, true
	}
	isJSON := func(r *http.Request) bool {
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		return ct == "application/json"
	}
	// run plans (opt.DryRun) or applies the request's tree or source and answers.
	run := func(w http.ResponseWriter, r *http.Request, what string, opt ConfigApplyOptions) {
		var res ConfigApplyResult
		var err error
		switch ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct {
		case "application/gzip":
			tr, ok := upload(w, r)
			if !ok {
				return
			}
			opt.Source, opt.Commit = gitsource.Source{Repo: ConfigDirRepo}, ConfigDirCommit
			res, err = ApplyConfigTree(r.Context(), store, f, tr, env, opt, log, OperatorID(r))
		case "application/json":
			src, ok := source(w, r, false)
			if !ok {
				return
			}
			res, err = ApplyConfigSource(r.Context(), store, f, src, env, opt, log, OperatorID(r))
		default: // e.g. a browser's no-cors text/plain POST to the loopback admin port
			http.Error(w, "config tree: Content-Type must be application/gzip (a tree) or application/json (a source)", http.StatusUnsupportedMediaType)
			return
		}
		if err != nil {
			fail(w, r, what, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
	return func(mux *http.ServeMux) {
		mux.HandleFunc("POST /admin/config/diff", func(w http.ResponseWriter, r *http.Request) {
			// force plans as a forced apply would, so the digests match.
			run(w, r, "diff", ConfigApplyOptions{DryRun: true, Force: r.URL.Query().Get("force") == "1", Now: time.Now()})
		})
		mux.HandleFunc("POST /admin/config/apply", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			run(w, r, "apply", ConfigApplyOptions{Force: q.Get("force") == "1", WantDigest: q.Get("plan"), Now: time.Now()})
		})
		mux.HandleFunc("POST /admin/config/sync", func(w http.ResponseWriter, r *http.Request) {
			if !isJSON(r) {
				http.Error(w, "config sync: Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			q := r.URL.Query()
			res, err := ApplyConfigSource(r.Context(), store, f, gitsource.Source{}, env,
				ConfigApplyOptions{Force: q.Get("force") == "1", WantDigest: q.Get("plan"), Now: time.Now()}, log, OperatorID(r))
			if err != nil {
				fail(w, r, "sync", err)
				return
			}
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("PUT /admin/config/source", func(w http.ResponseWriter, r *http.Request) {
			src, ok := source(w, r, true)
			if !ok {
				return
			}
			res, err := TrackConfigSource(r.Context(), store, f, src, env, time.Now(), log, OperatorID(r))
			if err != nil {
				fail(w, r, "track", err)
				return
			}
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("DELETE /admin/config/source", func(w http.ResponseWriter, r *http.Request) {
			if err := UntrackConfigSource(store, log, OperatorID(r)); err != nil {
				fail(w, r, "untrack", err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
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
