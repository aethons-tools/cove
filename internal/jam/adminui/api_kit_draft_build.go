package adminui

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// The kit draft test build API (docs/usage/jam/kits-drafts.md#test-build):
// start, follow and cancel a build of the draft in this handler's store (a
// branch's workspace). A build commits nothing.

// draftBuildCancelWait bounds how long a cancel waits for the build to end.
const draftBuildCancelWait = 10 * time.Second

// buildFail answers a build call's error: no builder or manager (503
// unavailable), an unknown manager (404), else as a draft write's.
func buildFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	switch {
	case errors.Is(err, jam.ErrUnknownManager):
		writeAPIError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, jam.ErrManagerUnavailable):
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
	default:
		draftFailJSON(w, r, log, err)
	}
}

func apiKitDraftBuild(api *http.ServeMux, store jam.Store, builds *jam.KitDraftBuilds, log *slog.Logger) {
	base := apiPrefix + "kits/{name}/draft/build"
	off := func(w http.ResponseWriter) bool {
		if builds == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "unavailable", jam.KitDraftBuildsOff)
			return true
		}
		return false
	}
	src := func(r *http.Request) jam.DraftSource { return jam.DraftSource{Branch: branchOf(r), Store: store} }
	api.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		if off(w) {
			return
		}
		var body struct {
			ETag    string `json:"etag"`
			Manager string `json:"manager"`
		}
		name, ok := draftJSON(w, r, store, &body)
		if !ok {
			return
		}
		v, err := builds.StartOn(src(r), name, body.ETag, jam.OperatorID(r), body.Manager)
		if err != nil {
			buildFail(w, r, log, err)
			return
		}
		log.Info("ui kit draft build", "operator", jam.OperatorID(r), "kit", name, "branch", branchOf(r), "build", v.ID)
		writeJSON(w, http.StatusOK, v)
	})
	api.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		if off(w) {
			return
		}
		var since int64
		if s := r.URL.Query().Get("since"); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "bad_request", "since must be a log offset (0 or more)")
				return
			}
			since = n
		}
		v, err := builds.StatusOn(src(r), r.PathValue("name"), r.URL.Query().Get("manager"), since)
		if err != nil {
			buildFail(w, r, log, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	api.HandleFunc("POST "+base+"/cancel", func(w http.ResponseWriter, r *http.Request) {
		if off(w) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), draftBuildCancelWait)
		defer cancel()
		v, err := builds.CancelOn(ctx, src(r), r.PathValue("name"), r.URL.Query().Get("manager"))
		if err != nil && v.ID == "" {
			buildFail(w, r, log, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
}
