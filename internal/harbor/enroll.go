package harbor

import (
	"fmt"
	"strings"
	"time"
)

// Enroll mints a token, records the identity (storing only the token hash), and
// returns the raw token once for the caller to hand to the Guest. ttl == 0 means
// no expiry. now is injected for testability.
func Enroll(store Store, id, project, role string, dests, repos []string, ttl time.Duration, now time.Time) (string, error) {
	if id == "" {
		return "", fmt.Errorf("identity id is required")
	}
	tok, err := MintToken()
	if err != nil {
		return "", err
	}
	rec := Identity{
		ID: id, TokenHash: HashToken(tok), Project: project, Role: role,
		Destinations: dests, Repos: repos,
	}
	if ttl > 0 {
		rec.Expiry = now.Add(ttl)
	}
	if err := store.Add(rec); err != nil {
		return "", err
	}
	return tok, nil
}

// RenderEnrollSnippet renders the env + gitconfig a Guest cove sources to route
// Anthropic and git through harbor. The identity token is exported once (as
// AT_HARBOR_IDENTITY_TOKEN) and both connectors reference it; harbor swaps it for
// the real credentials.
//
// The token stays **env-only**: the git credential helper reads it from
// $AT_HARBOR_IDENTITY_TOKEN at run time, so the token never lands in gitconfig on
// disk (only the env-var name does). This makes `git clone` through harbor work
// headlessly — no prompt — matching the repo's existing env-only-token pattern.
func RenderEnrollSnippet(baseURL, token string) string {
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
