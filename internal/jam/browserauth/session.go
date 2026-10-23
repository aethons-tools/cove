package browserauth

import "net/http"

// Temp cookie names for the authorize→callback round trip. They are path-scoped
// to each mount's /auth prefix, so the same names never collide across mounts.
const (
	stateCookie = "jam_oauth_state"
	nonceCookie = "jam_oauth_nonce"
	pkceCookie  = "jam_oauth_pkce"
)

// setCookie stores a session token in cookie name, scoped to the mount prefix
// so different mounts' sessions are distinct.
func (m Mount) setCookie(w http.ResponseWriter, name, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: token, Path: m.Prefix,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func (m Mount) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: m.Prefix,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func (m Mount) setSession(w http.ResponseWriter, token string, secure bool) {
	m.setCookie(w, m.SessionCookie, token, secure)
}

func (m Mount) clearSession(w http.ResponseWriter) {
	if m.Session == BothTokensSession {
		m.clearCookie(w, m.IDCookie())
		m.clearCookie(w, m.AccessCookie())
		return
	}
	m.clearCookie(w, m.SessionCookie)
}

// tempName is the mount's name for temp cookie n: "__Host-"-prefixed for a
// HostPrefixed mount, so a sibling host cannot toss one with a Domain attribute.
func (m Mount) tempName(n string) string {
	if m.HostPrefixed {
		return "__Host-" + n
	}
	return n
}

// tempPath scopes temp cookies to the mount's /auth prefix — or "/" for a
// HostPrefixed mount, as the __Host- prefix requires.
func (m Mount) tempPath() string {
	if m.HostPrefixed {
		return "/"
	}
	return m.authPrefix()
}

// tempCookie sets a short-lived HttpOnly cookie (see tempName, tempPath), used
// only across the authorize→callback round trip. A HostPrefixed mount's
// cookies are always Secure, as the __Host- prefix requires.
func (m Mount) tempCookie(w http.ResponseWriter, name, value string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: m.tempName(name), Value: value, Path: m.tempPath(),
		HttpOnly: true, Secure: secure || m.HostPrefixed, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
}

func (m Mount) clearTemp(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: m.tempName(name), Value: "", Path: m.tempPath(), HttpOnly: true, Secure: m.HostPrefixed, MaxAge: -1})
}
