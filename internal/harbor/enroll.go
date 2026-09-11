package harbor

import (
	"fmt"
	"time"

	"github.com/aethons-tools/cove/internal/harbor/snippet"
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
	return snippet.Render(baseURL, token)
}
