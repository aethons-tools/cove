package browserauth

import "testing"

// Cookie names carry the product name (Jam; formerly harbor_*, with no alias —
// admin UI users log in again once; see docs/usage/jam/renamed-from-harbor.md).
func TestCookieNames(t *testing.T) {
	for got, want := range map[string]string{
		OperatorUIMount().SessionCookie:  "jam_session",
		ParticipantMount().SessionCookie: "jam_participant",
		stateCookie:                      "jam_oauth_state",
		nonceCookie:                      "jam_oauth_nonce",
		pkceCookie:                       "jam_oauth_pkce",
	} {
		if got != want {
			t.Errorf("cookie = %q, want %q", got, want)
		}
	}
}
