// Package snippet renders the connector contract a client uses to route Anthropic
// and git through harbor. It is stdlib-only — deliberately separate from
// internal/harbor (which pulls go-oidc) so the at-cove binary can reuse the
// contract without that dependency.
//
// The contract has two parts, and there is ONE source for each:
//   - Env: the environment variables the client sets (Anthropic base URL + the
//     identity on x-api-key, plus the token the git helper reads).
//   - GitConfig: the `git config --global` commands that rewrite github.com → the
//     harbor git connector and install a credential helper reading the env-only token.
//
// Render composes both into a single sourceable shell snippet (what `at-harbor
// enroll` prints for a Guest); at-cove consumes Env + GitConfig directly.
package snippet

import (
	"fmt"
	"strings"
)

const (
	// tokenVar is the env var holding the identity token; the git credential helper
	// reads it at run time so the token never lands in gitconfig on disk.
	tokenVar      = "AT_HARBOR_IDENTITY_TOKEN"
	anthropicPath = "/anthropic"
	gitPath       = "/git/"
	// gitHelper is a `!`-prefixed shell helper git runs with the operation as $1;
	// it emits the identity as username + the env-only token as password, only for
	// `get`. Single-quoted so nothing expands until git invokes it in the cove.
	gitHelper = `'!f() { test "$1" = get && echo username=x-access-token && echo password=$` + tokenVar + `; }; f'`
)

// Env returns the environment variables a client sets to route Anthropic through
// harbor at baseURL with the given identity token. ANTHROPIC_API_KEY (not
// AUTH_TOKEN): Claude Code then sends the identity on the x-api-key header, which
// is how the Anthropic API authenticates keys and how harbor's anthropic
// destination expects the identity (identity_in: x-api-key).
func Env(baseURL, token string) map[string]string {
	baseURL = strings.TrimRight(baseURL, "/")
	return map[string]string{
		tokenVar:             token,
		"ANTHROPIC_BASE_URL": baseURL + anthropicPath,
		"ANTHROPIC_API_KEY":  token,
	}
}

// GitConfig returns the `git config --global` commands (a shell script, one per
// line) that route git through harbor's git connector at baseURL. It contains NO
// secret — the credential helper reads the token from the environment at run time.
func GitConfig(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "git config --global url.%q.insteadOf https://github.com/\n", baseURL+gitPath)
	fmt.Fprintf(&b, "git config --global credential.%q.helper %s\n", baseURL, gitHelper)
	return b.String()
}

// Render renders the full env + gitconfig a client sources to route Anthropic and
// git through harbor at baseURL. The identity token is exported once (as
// AT_HARBOR_IDENTITY_TOKEN) and both connectors reference it; harbor swaps it for
// the real credentials. The token stays env-only — see gitHelper — so `git clone`
// through harbor works headlessly without the token ever landing in gitconfig.
func Render(baseURL, token string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "export %s=%s\n", tokenVar, token)
	fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%s%s\n", baseURL, anthropicPath)
	fmt.Fprintf(&b, "export ANTHROPIC_API_KEY=$%s\n", tokenVar)
	b.WriteString(GitConfig(baseURL))
	return b.String()
}
