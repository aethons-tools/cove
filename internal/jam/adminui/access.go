package adminui

import (
	"time"

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
