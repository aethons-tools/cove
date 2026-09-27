package browserauth

import "net/http"

// SessionCookie holds the API access token for a logged-in browser session.
const SessionCookie = "harbor_session"

const (
	stateCookie = "harbor_oauth_state"
	nonceCookie = "harbor_oauth_nonce"
	pkceCookie  = "harbor_oauth_pkce"
)

func setSession(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/ui",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/ui",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// tempCookie sets a short-lived HttpOnly cookie under /ui/auth used only across
// the authorize→callback round trip.
func tempCookie(w http.ResponseWriter, name, value string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/ui/auth",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
}

func clearTemp(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/ui/auth", HttpOnly: true, MaxAge: -1})
}
