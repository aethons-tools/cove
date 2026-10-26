package appui

import (
	"net/http"

	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// Deps are the parts of the browser app Handler composes.
type Deps struct {
	Gate           browserauth.Gate
	AuthRoutes     http.Handler // /app/auth/*; nil = no browser login
	User           http.Handler // serves /app/user/*; nil = none
	Next           http.Handler // serves /app/next/* (the Jam app, web/app); nil = none
	Admin          http.Handler // serves /app/admin/*
	TrustedOrigins []string
	Shell          Shell // title-bar context for the pages
}

// legacy maps the pre-/app browser prefixes to their new homes; the longest
// prefix wins (ServeMux). Delete one release after slice 2 ships.
var legacy = []struct{ from, to string }{
	{"/ui/auth/", "/app/auth/"}, {"/me/auth/", "/app/auth/"},
	{"/ui/static/", "/app/static/"}, {"/me/static/", "/app/static/"},
	{"/ui/", "/app/admin/"}, {"/me/", "/app/user/"},
}

// Handler returns the browser mux: callers may add more routes (cmd adds /me/studios/).
func Handler(d Deps) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /app/static/", uiassets.Handler("/app/static/"))
	// Installable-app files are public (the browser fetches them without
	// credentials); exact paths, so nothing else under /app/ is ungated.
	mux.Handle("GET /app/manifest.webmanifest", manifestHandler(d.Shell.Brand))
	mux.Handle("GET /app/sw.js", serviceWorker())
	if d.AuthRoutes != nil {
		mux.Handle("/app/auth/", d.AuthRoutes)
	}
	if d.User != nil {
		mux.Handle("/app/user/", d.Gate.Wrap(RequireUser(browserauth.RequireSameOrigin(d.TrustedOrigins, WithShell(d.User, d.Shell)))))
	}
	if d.Next != nil {
		// The Jam app shares the user area's gate and its same-origin rule.
		mux.Handle("/app/next/", d.Gate.Wrap(RequireUser(browserauth.RequireSameOrigin(d.TrustedOrigins, WithShell(d.Next, d.Shell)))))
	}
	if d.Admin != nil {
		mux.Handle("/app/admin/", d.Gate.Wrap(RequireAdmin(WithShell(d.Admin, d.Shell))))
	}
	mux.Handle("GET /app/{$}", d.Gate.Wrap(WithShell(http.HandlerFunc(landing), d.Shell)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusFound)
	})
	for _, l := range legacy {
		mux.Handle(l.from, LegacyRedirect(l.from, l.to))
	}
	return mux
}

// landing sends a member to the Inbox, an admin who is not one to the console.
func landing(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r)
	switch {
	case p.User != nil:
		http.Redirect(w, r, "/app/user/", http.StatusFound)
	case p.Admin:
		http.Redirect(w, r, "/app/admin/", http.StatusFound)
	default:
		forbidNeither(w, r)
	}
}
