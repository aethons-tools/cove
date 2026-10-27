package jam

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
)

// kitDraftBuildsOff answers a build route (and a draft studio's raise) on a
// Jam whose launcher cannot build images (no runtime.launcher).
const kitDraftBuildsOff = "test builds need a launcher that builds images (runtime.launcher)"

// registerKitDraftBuilds mounts the draft test-build routes
// (docs/usage/jam/kits-drafts.md#test-build) behind the operator gate:
//
//	POST   /admin/kits/{name}/draft/build                       start (application/json {"manager"?}, If-Match; 202)
//	GET    /admin/kits/{name}/draft/build[?since=N][&manager=M]  the latest build (on M), its log from N
//	DELETE /admin/kits/{name}/draft/build[?manager=M]            cancel the running build (on M) (200)
//
// No manager is the default: the first draft-capable one for a start, the
// most recently started build otherwise. An unknown manager is 404 and an
// unavailable one 503 (draftFail).
//
// builds is read per request: it is the supervisor's (SetKitDraftBuilds,
// set once at wiring, before serving), the same jobs a draft studio's raise
// builds through. nil → the routes answer 503.
func registerKitDraftBuilds(mux *http.ServeMux, builds func() *KitDraftBuilds, log *slog.Logger) {
	wired := func(w http.ResponseWriter) (*KitDraftBuilds, bool) {
		b := builds()
		if b == nil {
			http.Error(w, kitDraftBuildsOff, http.StatusServiceUnavailable)
		}
		return b, b != nil
	}
	mux.HandleFunc("POST /admin/kits/{name}/draft/build", func(w http.ResponseWriter, r *http.Request) {
		if !requireContentType(w, r, "application/json") {
			return
		}
		var req struct {
			Manager string `json:"manager"`
		}
		if !decodeOptional(w, r, &req) {
			return
		}
		b, ok := wired(w)
		if !ok {
			return
		}
		name := r.PathValue("name")
		v, err := b.Start(name, r.Header.Get("If-Match"), OperatorID(r), req.Manager)
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		log.Info("kit draft build requested", "operator", OperatorID(r), "kit", name, "manager", v.Manager, "tree", shortTree(v.Tree), "build", v.ID)
		writeJSON(w, http.StatusAccepted, v)
	})
	mux.HandleFunc("GET /admin/kits/{name}/draft/build", func(w http.ResponseWriter, r *http.Request) {
		b, ok := wired(w)
		if !ok {
			return
		}
		var since int64
		if q := r.URL.Query().Get("since"); q != "" {
			n, err := strconv.ParseInt(q, 10, 64)
			if err != nil || n < 0 {
				http.Error(w, "since must be a log offset (0 or more)", http.StatusBadRequest)
				return
			}
			since = n
		}
		v, err := b.Status(r.PathValue("name"), r.URL.Query().Get("manager"), since)
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("DELETE /admin/kits/{name}/draft/build", func(w http.ResponseWriter, r *http.Request) {
		b, ok := wired(w)
		if !ok {
			return
		}
		name := r.PathValue("name")
		v, err := b.Cancel(r.Context(), name, r.URL.Query().Get("manager"))
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// The request went away while the build was stopping: it was
			// cancelled and ends on its own. The name was checked tag-safe.
			log.Info("kit draft build cancelled", "operator", OperatorID(r), "kit", name, "manager", v.Manager, "build", v.ID)
			http.Error(w, "kit "+name+" draft build is still stopping", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		log.Info("kit draft build cancelled", "operator", OperatorID(r), "kit", name, "manager", v.Manager, "build", v.ID)
		writeJSON(w, http.StatusOK, v)
	})
}
