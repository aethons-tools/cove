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
// Anthropic and git through harbor. The token is the caller's identity for both
// connectors; harbor swaps it for the real credentials.
func RenderEnrollSnippet(baseURL, token string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%s/anthropic\n", baseURL)
	fmt.Fprintf(&b, "export ANTHROPIC_AUTH_TOKEN=%s\n", token)
	fmt.Fprintf(&b, "git config --global url.%q.insteadOf https://github.com/\n", baseURL+"/git/")
	fmt.Fprintf(&b, "# git credential: username 'x-access-token', password = ANTHROPIC_AUTH_TOKEN (your harbor identity)\n")
	return b.String()
}
