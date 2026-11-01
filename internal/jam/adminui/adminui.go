// Package adminui serves Jam's admin console: the admin area of the Jam app
// (web/app) at /app/admin/, its JSON API at /app/admin/api/ and the
// type-ahead's /app/admin/suggest. Its view builders read a jam.Store
// directly; its writes call the same jam functions as the CLI. The old
// server-rendered pages' paths redirect to the app (redirectOld). It exposes
// an unauthenticated http.Handler; the browser app mounts it behind the
// admin gate.
package adminui

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// roleRow is one project/role pair flattened for the roles table.
type roleRow struct {
	Project      string
	Name         string
	Destinations []string
	Credentials  map[string]string // destination → credential name (a reference, not a secret)
	TTL          time.Duration
	Kit          string
}

func roleRows(store jam.Store) []roleRow {
	var out []roleRow
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			out = append(out, roleRow{
				Project: p, Name: r.Name,
				Destinations: r.Scope.Destinations,
				Credentials:  r.Scope.Credentials,
				TTL:          r.Scope.TTL, Kit: r.Kit,
			})
		}
	}
	return out
}

// fmtDur renders a duration without trailing zero units: "1h", "1h30m", "45s".
func fmtDur(d time.Duration) string { return jam.FormatDuration(d) }

// Option configures Handler.
type Option func(*options)

type options struct {
	trustedOrigins  []string
	sessStore       sessionevents.Store
	sessHub         *sessionevents.Hub
	credNames       []string
	poolConfigured  bool
	conds           *condition.Tracker
	alertmanagerURL string
	live            *collabhttp.Live
	exposures       func(actorID string) []jam.Exposure
	previewURL      func(actorID, name string) (string, error)
	kitFetcher      jam.KitFetcher
	configFetcher   jam.KitFetcher // nil = kitFetcher (WithConfigFetcher)
	configEnv       jam.ConfigEnv
	setupManagers   func() []string            // nil: no manager selector on a role's Setup tab (WithSetupManagers)
	setup           *jam.SetupDeps             // nil: no Setup tabs (WithSetupDeps)
	managers        func() []jam.ManagerStatus // nil: no Managers panel (WithManagers)
	appTokenRevoke  func(ident.ID)             // nil: revoking an app token ends no requests (WithAppTokenRevoke)
	branches        *jam.ConfigBranches        // nil: no branch editor (WithConfigBranches)
	branchHandler   func(jam.Store) http.Handler
}

// WithConditions shows operator-attention conditions: open critical/warning
// ones as Jam attention items, and all of them on the Health tab, which links
// alertmanagerURL's silences when set.
func WithConditions(t *condition.Tracker, alertmanagerURL string) Option {
	return func(o *options) { o.conds, o.alertmanagerURL = t, strings.TrimRight(alertmanagerURL, "/") }
}

// WithSessions enables the live session-event timeline (/app/admin/agents/{id}/session).
func WithSessions(store sessionevents.Store, hub *sessionevents.Hub) Option {
	return func(o *options) { o.sessStore, o.sessHub = store, hub }
}

// WithKitFetcher enables the kit pages' git-source controls (import, track,
// sync, untrack) using f, the same fetcher the admin API uses. Without it the
// pages only show provenance.
func WithKitFetcher(f jam.KitFetcher) Option {
	return func(o *options) { o.kitFetcher = f }
}

// WithConfigFetcher is the fetcher the Config page's diff and apply read
// the followed repo through (jam.FollowedFetcher); unset, they use
// WithKitFetcher's. The kit pages always use WithKitFetcher's.
func WithConfigFetcher(f jam.KitFetcher) Option {
	return func(o *options) { o.configFetcher = f }
}

// WithManagers shows a read-only Managers panel on the dashboard, one row per
// status list returns. Rotating and revoking stay on the CLI and admin API.
func WithManagers(list func() []jam.ManagerStatus) Option {
	return func(o *options) { o.managers = list }
}

// WithConfigEnv gives the Config page the planner's view of the serve — the
// env the admin API's WithConfig gets: its portable serve config, and the
// conditions the planner raises on. Unset fields
// default to the Handler's credExists and WithPoolConfigured.
func WithConfigEnv(env jam.ConfigEnv) Option {
	return func(o *options) { o.configEnv = env }
}

// WithTrustedOrigins adds exact origins (scheme://host[:port]) the CSRF write
// check accepts besides the request's own Host — e.g. a dev live-reload proxy
// that fronts the admin listener on another port (the serve config's
// ui-origins).
func WithTrustedOrigins(origins ...string) Option {
	return func(o *options) { o.trustedOrigins = append(o.trustedOrigins, origins...) }
}

// WithAppTokenRevoke is called with each app token an operator revokes, to
// end its open requests (apptoken.Live.Revoke).
func WithAppTokenRevoke(revoke func(ident.ID)) Option {
	return func(o *options) { o.appTokenRevoke = revoke }
}

// Handler returns the UI mux (no auth wrap). store is the primary dependency:
// every read view is an in-process read. log records write-action outcomes
// (never secret values — see writes.go). Config writes are made on a branch
// (WithConfigBranches); without one, on a Jam with no config repo, they are
// refused: its config is read-only.
func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.SessionAllocator, credExists func(string) bool, msgs SquawkReader, opts ...Option) http.Handler {
	mux, o := newMux(store, log, sup, alloc, credExists, msgs, opts)
	if o.branches != nil && o.branchHandler != nil {
		return &branchRouter{live: mux, b: o.branches, factory: o.branchHandler}
	}
	return readOnlyRouter{live: mux}
}

// newMux is Handler's mux, before the config-write routing, and its options.
func newMux(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.SessionAllocator, credExists func(string) bool, msgs SquawkReader, opts []Option) (*http.ServeMux, options) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	src := frameSource{store: store, img: sup, conds: o.conds, appTokenRevoke: o.appTokenRevoke}
	mux := http.NewServeMux()
	registerSuggest(mux, store, o.credNames)
	cfgEnv := o.configEnv
	if cfgEnv.CredExists == nil {
		cfgEnv.CredExists = credExists
	}
	cfgEnv.PoolConfigured = cfgEnv.PoolConfigured || o.poolConfigured
	cfgFetcher := o.configFetcher
	if cfgFetcher == nil {
		cfgFetcher = o.kitFetcher
	}
	registerApp(mux, src, msgs, roleAPI{sup: sup, alloc: alloc, credExists: credExists, setup: o.setup, managers: o.setupManagers},
		specUI{store: store, credExists: credExists, credNames: o.credNames, pool: o.poolConfigured},
		agentAPI{sup: sup, alloc: alloc, msgs: msgs, sess: o.sessStore, hub: o.sessHub, acc: accessDeps{o.live, o.exposures, o.previewURL}, setup: o.setup, credExists: credExists},
		dashAPI{managers: o.managers, sup: sup, amURL: o.alertmanagerURL}, o.trustedOrigins, fetchers{kits: o.kitFetcher, config: cfgFetcher}, cfgEnv, o.branches, log)
	return mux, o
}
