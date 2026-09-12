package harbor

import (
	"fmt"
	"time"

	"github.com/aethons-tools/cove/internal/harbor/snippet"
)

// Enroll mints a token and records a new Actor whose first grant is (project,
// role). The role must already exist (fail closed); its TTL sets the actor's
// expiry. Only the token hash is stored; the raw token is returned once. project
// == "" defaults to DefaultProject. now is injected for testability.
func Enroll(store Store, id, project, role string, overrides *Override, now time.Time) (string, error) {
	if id == "" {
		return "", fmt.Errorf("identity id is required")
	}
	if role == "" {
		return "", fmt.Errorf("role is required")
	}
	if project == "" {
		project = DefaultProject
	}
	r, ok := store.GetRole(project, role)
	if !ok {
		return "", fmt.Errorf("role %q not found in project %q", role, project)
	}
	tok, err := MintToken()
	if err != nil {
		return "", err
	}
	rec := Actor{
		ID:        id,
		TokenHash: HashToken(tok),
		Grants:    []Grant{{Project: project, Role: role, Overrides: overrides}},
	}
	if r.Scope.TTL > 0 {
		rec.Expiry = now.Add(r.Scope.TTL)
	}
	if err := store.AddActor(rec); err != nil {
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
