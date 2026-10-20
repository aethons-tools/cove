package browserauth

import "net/http"

// SessionToken selects which token from the login exchange backs the session
// cookie (and is re-verified by the gate on each request).
type SessionToken int

const (
	// AccessTokenSession stores the access token — the operator plane, where the
	// gate re-verifies it via jam.OIDCAuthenticator (aud = API audience + require-scope).
	AccessTokenSession SessionToken = iota
	// IDTokenSession stores the ID token — the participant plane, where the gate
	// verifies it with the browser verifier (aud = ClientID) and maps its subject
	// to a roster human. No API audience or scope is required.
	IDTokenSession
)

// Mount describes one browser-login subtree: the URL prefix it serves, the name
// of its session cookie, and which token backs the session. Two mounts (operator
// "/ui" and participant "/me") share one audited OIDC implementation; their
// cookies are path-scoped to the prefix so they never collide.
//
// A mount may instead hand the login result to its owner: when Complete is
// set, the callback calls it with both verified-exchange tokens and the bound
// return_to instead of setting SessionCookie and redirecting (SessionCookie
// and Session are then unused, and there is no logout route). When ReturnTo is
// set it decides which login return_to values are kept, replacing the
// same-site prefix check (a refused value collapses to "<prefix>/" as before).
// HostPrefixed names the login round trip's temp cookies "__Host-…" (Path=/,
// Secure, no Domain), so sibling hosts under a shared parent domain cannot
// toss them.
type Mount struct {
	Prefix        string // e.g. "/ui" or "/me"
	SessionCookie string // e.g. "jam_session" or "jam_participant"
	Session       SessionToken

	Complete func(w http.ResponseWriter, r *http.Request, idToken, accessToken, returnTo string)
	ReturnTo func(returnTo string) bool

	HostPrefixed bool
}

func (m Mount) authPrefix() string   { return m.Prefix + "/auth" }
func (m Mount) loginPath() string    { return m.Prefix + "/auth/login" }
func (m Mount) callbackPath() string { return m.Prefix + "/auth/callback" }

// OperatorUIMount is the standard operator "/ui" mount.
func OperatorUIMount() Mount {
	return Mount{Prefix: "/ui", SessionCookie: "jam_session", Session: AccessTokenSession}
}

// ParticipantMount is the standard participant "/me" mount.
func ParticipantMount() Mount {
	return Mount{Prefix: "/me", SessionCookie: "jam_participant", Session: IDTokenSession}
}

// PreviewMount is the previews' login mount, served on the login host
// (jam.<previews domain>) under "/__jam": its callback hands the tokens to
// complete (which issues the preview session itself), and returnTo vets the
// absolute preview URL a login returns to. Its temp cookies are __Host-
// prefixed: untrusted preview apps share the parent domain.
func PreviewMount(complete func(w http.ResponseWriter, r *http.Request, idToken, accessToken, returnTo string), returnTo func(string) bool) Mount {
	return Mount{Prefix: "/__jam", Complete: complete, ReturnTo: returnTo, HostPrefixed: true}
}
