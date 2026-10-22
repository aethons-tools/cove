package adminui

import (
	"cmp"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/collabwire"
)

// WithStudioAccess shows the agent page's Access card — who can reach a
// running studio and why, its share grants (with an Unshare button) and its
// open SSH connections — over live, the registry the collaboration routes
// share. Without it (SSH collaboration off) the card says so.
func WithStudioAccess(live *collabhttp.Live) Option {
	return func(o *options) { o.live = live }
}

// WithStudioPreviews lists a studio's exposed previews, with their URLs, in
// the Access card (exposures is Supervisor.Exposures; urlFor builds a
// preview's URL). Without it the card says previews are off.
func WithStudioPreviews(exposures func(actorID string) []jam.Exposure, urlFor func(actorID, name string) (string, error)) Option {
	return func(o *options) { o.exposures, o.previewURL = exposures, urlFor }
}

// accessCard is the Access card's payload.
type accessCard struct {
	ID      string
	Enabled bool
	View    collabwire.AccessView
}

// accessDeps is what the Access card reads beyond the store.
type accessDeps struct {
	live      *collabhttp.Live
	exposures func(actorID string) []jam.Exposure
	urlFor    func(actorID, name string) (string, error)
}

func buildAccessCard(store jam.Store, a accessDeps, inst jam.Instance) accessCard {
	live := a.live
	c := accessCard{ID: inst.ActorID, Enabled: live != nil}
	if c.Enabled {
		c.View = collabhttp.AccessOf(store, live, time.Now(), inst, a.exposures, a.urlFor)
	}
	return c
}

// registerAccess serves the Unshare button: DELETE
// /app/admin/agents/{id}/shares/{user} (a CSRF-guarded write), which removes the
// grant and closes the user's SSH connections when it was their way in.
func registerAccess(mux *http.ServeMux, store jam.Store, live *collabhttp.Live, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	if live == nil {
		return
	}
	mux.HandleFunc("DELETE /app/admin/agents/{id}/shares/{user}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		user, err := ident.Parse(r.PathValue("user"))
		if err != nil || user.Kind() != ident.User {
			renderError(w, http.StatusNotFound, "no such user")
			return
		}
		grantor := "op:" + cmp.Or(jam.OperatorID(r), "local")
		g, found := collabhttp.GrantOf(store, r.PathValue("id"), user)
		if !found {
			renderError(w, http.StatusNotFound, "no such share on this studio")
			return
		}
		removed, err := collabhttp.Unshare(store, live, log, time.Now(), grantor, g)
		switch {
		case err != nil:
			renderError(w, http.StatusInternalServerError, "could not remove the grant")
			return
		case !removed:
			renderError(w, http.StatusConflict, "the grant changed while being removed; reload and retry")
			return
		}
		// The agent page reloads on success.
		w.WriteHeader(http.StatusOK)
	})
}
