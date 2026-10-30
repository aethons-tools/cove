package appui

import (
	"io/fs"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
	"github.com/aethons-tools/cove/internal/jam/webapp"
)

// Deps are the parts of the browser app Handler composes.
type Deps struct {
	Gate       browserauth.Gate
	AuthRoutes http.Handler // /app/auth/*; nil = no browser login
	// AppTokenRoutes serves the app-token consent and exchange
	// (/app/auth/app-token, ungated like the login); nil = none.
	AppTokenRoutes http.Handler
	User           http.Handler // serves /app/user/*; nil = none
	Admin          http.Handler // serves /app/admin/*
	TrustedOrigins []string
	Shell          Shell // title-bar context for the pages
	App            fs.FS // the Jam app bundle (its assets/ at /app/assets/); nil = uiassets.AppFS()
	// Bearer authenticates /app/user requests carrying an app token; nil =
	// bearers go through the cookie gate like any request.
	Bearer BearerFunc
	// SessionCookies are the session cookie names a bearer request must not
	// also carry.
	SessionCookies []string
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
	// The Jam app's hashed bundle files, for both areas: code, not data.
	app := d.App
	if app == nil {
		app = uiassets.AppFS()
	}
	mux.Handle("GET "+webapp.AssetsPrefix, webapp.Assets(app))
	// Installable-app files are public (the browser fetches them without
	// credentials); exact paths, so nothing else under /app/ but the static
	// and bundle assets is ungated.
	mux.Handle("GET /app/manifest.webmanifest", manifestHandler(d.Shell.Brand))
	mux.Handle("GET /app/sw.js", serviceWorker())
	if d.AuthRoutes != nil {
		mux.Handle("/app/auth/", d.AuthRoutes)
	}
	if d.AppTokenRoutes != nil {
		mux.Handle("/app/auth/app-token", d.AppTokenRoutes)
		mux.Handle("/app/auth/app-token/", d.AppTokenRoutes)
	}
	if d.User != nil {
		user := WithShell(d.User, d.Shell)
		mux.Handle("/app/user/", withBearer(d.Bearer, d.SessionCookies,
			refuseOnBearer(bearerRefusedPaths, RequireUser(user)),
			d.Gate.Wrap(RequireUser(browserauth.RequireSameOrigin(d.TrustedOrigins, user)))))
	}
	// The Jam app's pre-cutover home: the same path, now under /app/user/.
	mux.Handle("/app/next/", LegacyRedirect("/app/next/", "/app/user/"))
	if d.Admin != nil {
		mux.Handle("/app/admin/", RefuseBearer(d.Gate.Wrap(RequireAdmin(WithShell(d.Admin, d.Shell)))))
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
