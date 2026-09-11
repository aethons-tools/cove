// Package snippet renders the env + gitconfig a client (a Guest cove, or an
// at-cove session) sources to route Anthropic and git through harbor. It is
// stdlib-only — deliberately separate from internal/harbor (which pulls go-oidc)
// so the at-cove binary can reuse the connector contract without that dependency.
package snippet

import (
	"fmt"
	"strings"
)

// Render returns the shell a client sources to route Anthropic and git through
// harbor at baseURL. The identity token is exported once (as
// AT_HARBOR_IDENTITY_TOKEN) and both connectors reference it; harbor swaps it for
// the real credentials.
//
// The token stays **env-only**: the git credential helper reads it from
// $AT_HARBOR_IDENTITY_TOKEN at run time, so the token never lands in gitconfig on
// disk (only the env-var name does). This makes `git clone` through harbor work
// headlessly — no prompt — matching the repo's existing env-only-token pattern.
func Render(baseURL, token string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	// A `!`-prefixed helper is a shell command git runs with the operation as $1;
	// it emits the identity as username + the env-only token as password, only for
	// `get`. Single-quoted so nothing expands until git invokes it in the cove.
	const helper = `'!f() { test "$1" = get && echo username=x-access-token && echo password=$AT_HARBOR_IDENTITY_TOKEN; }; f'`
	var b strings.Builder
	fmt.Fprintf(&b, "export AT_HARBOR_IDENTITY_TOKEN=%s\n", token)
	fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%s/anthropic\n", baseURL)
	// ANTHROPIC_API_KEY (not AUTH_TOKEN): Claude Code then sends the identity on the
	// x-api-key header, which is how the Anthropic API authenticates keys and how
	// harbor's anthropic destination expects the identity (identity_in: x-api-key).
	fmt.Fprintf(&b, "export ANTHROPIC_API_KEY=$AT_HARBOR_IDENTITY_TOKEN\n")
	fmt.Fprintf(&b, "git config --global url.%q.insteadOf https://github.com/\n", baseURL+"/git/")
	fmt.Fprintf(&b, "git config --global credential.%q.helper %s\n", baseURL, helper)
	return b.String()
}
