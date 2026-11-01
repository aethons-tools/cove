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

// ConfigStatus maps a config diff or sync error to an HTTP
// status and a message safe to show: a refused or stale plan and an
// untracked Jam 409; a bad source, kit or checkout 400; a fetch, auth or
// limit failure 502 with the fetcher's redacted message; anything else 500
// with a generic message (log the error itself).
func ConfigStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrConfigRefused), errors.Is(err, ErrConfigPlanChanged), errors.Is(err, ErrConfigNotTracked):
		return http.StatusConflict, err.Error()
	}
	if status, msg, ok := configBranchStatus(err); ok {
		return status, msg
	}
	status, msg := KitSourceStatus(err)
	if status == http.StatusInternalServerError {
		msg = configStoreError
	}
	return status, msg
}

// ConfigSourceBody is the JSON body of POST /admin/config/diff
// (Source "" = the followed repo) and POST /admin/config/sync ({}).
type ConfigSourceBody struct {
	Source string `json:"source,omitempty"`
}

// maxConfigSourceBody caps a {source} JSON body.
const maxConfigSourceBody = 64 << 10

// WithConfig mounts the config-in-git admin API (under the /admin/*
// operator gate):
//
//	POST   /admin/config/diff                        plan a tree or a source (writes nothing)
//	POST   /admin/config/sync?plan=<digest>          {}: apply the followed repo (config.github); the first apply's confirm
//	GET    /admin/config/follow                      the follower's status (ConfigFollowStatus)
//	GET    /admin/config/export-tree?all=1           render the managed (all: every) object
//
// diff takes a configtar body (application/gzip) or a {source}
// body (application/json; "" = the followed repo); sync takes {} — any
// other Content-Type is 415, so a browser's no-cors POST cannot trigger
// one. An oversize upload is 413, a malformed or unsafe one or a bad source
// 400, before anything is fetched or loaded. A refused or stale plan, or no
// followed repo, is 409; a fetch, auth or limit failure 502 with the
// fetcher's redacted message. f fetches config repos and
// kits/<name>.source.yml kits: the serve's FollowedFetcher, which reads the
// followed repo through its clone and the rest with kits.git credentials.
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
	// source decodes a {source} body ("" = the followed repo) and parses it;
	// Parse's errors never quote the input. It answers 400 itself.
	source := func(w http.ResponseWriter, r *http.Request) (gitsource.Source, bool) {
		var b ConfigSourceBody
		r.Body = http.MaxBytesReader(w, r.Body, maxConfigSourceBody)
		if !decode(w, r, &b) {
			return gitsource.Source{}, false
		}
		if b.Source == "" {
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
	// run plans the request's tree or source (opt.DryRun) and answers.
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
			src, ok := source(w, r)
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
	return func(mux *adminMux) {
		mux.conds = env.Conditions // the config import raises kit.migrate here
		mux.followed = env.Followed
		mux.HandleFunc("POST /admin/config/diff", func(w http.ResponseWriter, r *http.Request) {
			run(w, r, "diff", ConfigApplyOptions{DryRun: true, Now: time.Now()})
		})
		mux.HandleFunc("POST /admin/config/sync", func(w http.ResponseWriter, r *http.Request) {
			if !isJSON(r) {
				http.Error(w, "config sync: Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			res, err := ApplyConfigSource(r.Context(), store, f, gitsource.Source{}, env,
				ConfigApplyOptions{WantDigest: r.URL.Query().Get("plan"), Now: time.Now()}, log, OperatorID(r))
			if err != nil {
				fail(w, r, "sync", err)
				return
			}
			if env.Follower != nil {
				env.Follower.Refresh() // the status leaves awaiting-confirm now, not a minute later
			}
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("GET /admin/config/follow", func(w http.ResponseWriter, r *http.Request) {
			if env.Follower == nil {
				writeJSON(w, http.StatusOK, ConfigFollowStatus{State: ConfigFollowOff, Source: configSourceLabel(env, env.Followed)})
				return
			}
			writeJSON(w, http.StatusOK, env.Follower.Status())
		})
		mux.HandleFunc("GET /admin/config/export-tree", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			exp, err := ExportConfigTree(store.configSnapshot(), store.ListConfigMarks(), env.Serve, q.Get("all") == "1")
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
