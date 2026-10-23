package appui

import (
	"context"
	"html/template"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// The /app 403 page. One token can expire before the other, and a scope-less
// access token looks like an expired one, so a refusal never redirects (that
// could loop); it says why and, with browser login, offers to sign in again —
// via logout, which clears both cookies and goes to login.
var forbiddenPage = template.Must(template.New("forbidden").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Not authorized · Jam</title><link rel="stylesheet" href="{{.CSS}}"></head>
<body><main class="forbidden"><h1>Not authorized</h1><p>{{.Msg}}</p>
{{- if .Href}}<p><a href="{{.Href}}">{{.Link}}</a></p>{{end}}</main></body></html>
`))

const (
	loginPath  = "/app/auth/login"
	logoutPath = "/app/auth/logout"
)

func forbid(w http.ResponseWriter, msg, href, link string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = forbiddenPage.Execute(w, struct{ CSS, Msg, Href, Link string }{
		uiassets.StylesheetHref("/app/static/"), msg, href, link,
	})
}

// forbidSignedIn refuses a signed-in principal, offering "Sign in again" when
// browser login is configured.
func forbidSignedIn(w http.ResponseWriter, r *http.Request, msg string) {
	if loginConfigured(r) {
		forbid(w, msg, logoutPath, "Sign in again")
		return
	}
	forbid(w, msg, "", "")
}

func forbidNeither(w http.ResponseWriter, r *http.Request) {
	forbidSignedIn(w, r, "You're signed in, but not a member of any project and not an admin.")
}

type loginKey struct{}

// withLogin records on r whether browser login is configured, for the area
// guards' 403 pages (set by the gate's hooks).
func withLogin(r *http.Request, on bool) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), loginKey{}, on))
}

func loginConfigured(r *http.Request) bool {
	on, _ := r.Context().Value(loginKey{}).(bool)
	return on
}
