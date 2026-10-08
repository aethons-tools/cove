package browserauth

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
type Mount struct {
	Prefix        string // e.g. "/ui" or "/me"
	SessionCookie string // e.g. "jam_session" or "jam_participant"
	Session       SessionToken
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
