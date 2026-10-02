// Package snippet renders the connector contract a client uses to route Anthropic
// and git through Jam. It is stdlib-only — deliberately separate from
// internal/jam (which pulls go-oidc) so the at-cove binary can reuse the
// contract without that dependency.
//
// The contract has two parts, and there is ONE source for each:
//   - Env: the environment variables the client sets (Anthropic base URL + the
//     identity on x-api-key, plus the token the git helper reads).
//   - GitConfig: the `git config --global` commands that rewrite github.com → the
//     Jam git connector and install a credential helper reading the env-only token.
//
// Render composes both into a single sourceable shell snippet (what `at-jam
// enroll` prints for a Guest); at-cove consumes Env + GitConfig directly.
package snippet


const (
	// tokenVar is the env var holding the identity token; the git credential helper
	// reads it at run time so the token never lands in gitconfig on disk.
	tokenVar = "AT_JAM_IDENTITY_TOKEN"
	// legacyTokenVar is tokenVar's pre-rename name. Writers still set it (older
	// cove images read it) for one release — see
	// docs/usage/jam/renamed-from-harbor.md.
	legacyTokenVar = "AT_HARBOR_IDENTITY_TOKEN"
	anthropicPath  = "/anthropic"
	gitPath        = "/git/"
	// gitHelper is a `!`-prefixed shell helper git runs with the operation as $1;
	// it emits the identity as username + the env-only token as password, only for
	// `get`. Single-quoted so nothing expands until git invokes it in the cove. It
	// reads the new name, falling back to the deprecated one.
	gitHelper = `'!f() { test "$1" = get && echo username=x-access-token && echo password=${` + tokenVar + `:-$` + legacyTokenVar + `}; }; f'`
)

// Env returns the environment variables a client sets to route Anthropic through
// Jam at baseURL with the given identity token — the Legacy contract, expanded.
// ANTHROPIC_API_KEY (not AUTH_TOKEN): Claude Code then sends the identity on the
// x-api-key header, which is how Jam's legacy anthropic destination expects it.
func Env(baseURL, token string) map[string]string {
	return Legacy(false).Expand(baseURL, token)
}

// RenderSubscription routes a brokered subscription-pool cove: it sets the
// identity on ANTHROPIC_AUTH_TOKEN, so claude sends it as a **static**
// `Authorization: Bearer` with no OAuth session — no .credentials.json, no
// refresh, no `platform.claude.com`, and so no self-destruct on a 401 (probed
// 2026-09-29; see the pool spec's Revision B). It deliberately does NOT set
// ANTHROPIC_API_KEY (which would force x-api-key mode). The broker reads the
// identity from the bearer, swaps in the real pool subscription token, and adds
// the `oauth-2025-04-20` beta on the way to Anthropic.
func RenderSubscription(baseURL, token string) string {
	return Legacy(true).Render(baseURL, token)
}

// GitConfig returns the `git config --global` commands (a shell script, one per
// line) that route git through Jam's git connector at baseURL. It contains NO
// secret — the credential helper reads the token from the environment at run time.
func GitConfig(baseURL string) string {
	return Legacy(false).GitConfig(baseURL)
}

// Render renders the full env + gitconfig a client sources to route Anthropic and
// git through Jam at baseURL (the Legacy contract). The identity token is written
// once (as AT_JAM_IDENTITY_TOKEN) and everything else references it — the
// deprecated AT_HARBOR_IDENTITY_TOKEN is exported from it, not from the raw
// value; Jam swaps it for the real credentials. The token stays env-only — see
// gitHelper — so `git clone` through Jam works headlessly without the token ever
// landing in gitconfig.
func Render(baseURL, token string) string {
	return Legacy(false).Render(baseURL, token)
}
