package browserauth

import "net/http"

// Temp cookie names for the authorize→callback round trip. They are path-scoped
// to each mount's /auth prefix, so the same names never collide across mounts.
const (
	stateCookie = "jam_oauth_state"
	nonceCookie = "jam_oauth_nonce"
	pkceCookie  = "jam_oauth_pkce"
)

// setSession stores the session token in the mount's cookie, scoped to the
// mount prefix so operator ("/ui") and participant ("/me") sessions are distinct.
func (m Mount) setSession(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: m.SessionCookie, Value: token, Path: m.Prefix,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func (m Mount) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: m.SessionCookie, Value: "", Path: m.Prefix,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// tempCookie sets a short-lived HttpOnly cookie under the mount's /auth prefix,
// used only across the authorize→callback round trip.
func (m Mount) tempCookie(w http.ResponseWriter, name, value string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: m.authPrefix(),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
}

func (m Mount) clearTemp(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: m.authPrefix(), HttpOnly: true, MaxAge: -1})
}
