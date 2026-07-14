// Package mint turns a usersecret minter profile into a runnable at-mint
// invocation: non-secret identifiers become flags, resolved secret material
// becomes per-spec env (never argv). It is at-cove's usersecret.MintExpander.
package mint

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/usersecret"
)

// Expander returns a MintExpander bound to a host runner (for resolving a
// profile's command/global-sourced secret fields), the store's global library,
// and the target repo (owner/name) a github minter scopes its token to. repo is
// the kit's source-control.github.project; it is passed to `at-mint github` as
// the non-secret --repo flag (empty for callers that mint no github token).
func Expander(r runner.Runner, globals map[string]usersecret.Source, repo string) usersecret.MintExpander {
	return func(profileName string, m usersecret.Minter, demandName string) (secret.Spec, error) {
		switch {
		case m.GitHub != nil:
			return githubSpec(r, globals, demandName, m.GitHub, repo)
		case m.Anthropic != nil:
			return anthropicSpec(r, globals, demandName, m.Anthropic)
		default:
			return secret.Spec{}, fmt.Errorf("minter %q: no provider set", profileName)
		}
	}
}

func githubSpec(r runner.Runner, globals map[string]usersecret.Source, name string, g *usersecret.GitHubMinter, repo string) (secret.Spec, error) {
	argv := []string{"at-mint", "github", "--app-id", g.AppID, "--install-id", g.InstallID}
	if repo != "" {
		argv = append(argv, "--repo", repo) // the token's scope (non-secret)
	}
	var env map[string]string
	kind, err := g.AppKey.Kind()
	if err != nil {
		return secret.Spec{}, fmt.Errorf("github minter app-key: %w", err)
	}
	if kind == "value" {
		// a literal value is a filesystem path to the PEM (non-secret) -> flag
		argv = append(argv, "--app-key-file", *g.AppKey.Value)
	} else {
		// command/global -> resolved key CONTENT (secret) -> env
		content, err := resolveSource(r, globals, g.AppKey)
		if err != nil {
			return secret.Spec{}, fmt.Errorf("github minter app-key: %w", err)
		}
		env = map[string]string{"AT_MINT_GITHUB_APP_KEY": content}
	}
	return secret.Spec{Name: name, Command: argv, Env: env}, nil
}

func anthropicSpec(r runner.Runner, globals map[string]usersecret.Source, name string, a *usersecret.AnthropicMinter) (secret.Spec, error) {
	z := a.OIDC.Auth0
	if z == nil {
		return secret.Spec{}, fmt.Errorf("anthropic minter: only the auth0 IdP is supported")
	}
	argv := []string{
		"at-mint", "anthropic",
		"--auth0-tenant", z.Tenant,
		"--auth0-client-id", z.ClientID,
		"--auth0-audience", z.Audience,
		"--anthropic-org", a.Federation.Org,
		"--anthropic-rule", a.Federation.Rule,
		"--anthropic-service-account", a.Federation.ServiceAccount,
	}
	if a.Federation.Workspace != "" {
		argv = append(argv, "--anthropic-workspace", a.Federation.Workspace)
	}
	val, err := resolveSource(r, globals, z.ClientSecret)
	if err != nil {
		return secret.Spec{}, fmt.Errorf("anthropic minter client-secret: %w", err)
	}
	return secret.Spec{Name: name, Command: argv, Env: map[string]string{"AT_MINT_AUTH0_CLIENT_SECRET": val}}, nil
}

// resolveSource resolves a minter's secret field to a literal string. A value is
// used as-is (for a client secret it IS the secret; for an app-key the value
// branch is handled by the caller as a path). command runs on the host; global
// delegates to a terminal (value/command) shared supply.
func resolveSource(r runner.Runner, globals map[string]usersecret.Source, src usersecret.Source) (string, error) {
	kind, err := src.Kind()
	if err != nil {
		return "", err
	}
	switch kind {
	case "value":
		return *src.Value, nil
	case "command":
		// nil env: this resolves the profile's own static secret (e.g. a manager
		// lookup), not a per-run credential — no COVE_RUN_* is passed.
		out, err := r.OutputEnv(nil, src.Command[0], src.Command[1:]...)
		if err != nil {
			return "", fmt.Errorf("resolver command failed: %w", err)
		}
		return strings.TrimSuffix(out, "\n"), nil
	case "global":
		g, ok := globals[src.Global]
		if !ok {
			return "", fmt.Errorf("global %q is not defined", src.Global)
		}
		gk, err := g.Kind()
		if err != nil {
			return "", fmt.Errorf("global %q: %w", src.Global, err)
		}
		if gk == "global" || gk == "mint" {
			return "", fmt.Errorf("global %q must be a value or command", src.Global)
		}
		return resolveSource(r, globals, g)
	default:
		return "", fmt.Errorf("a minter secret cannot be a %s source", kind)
	}
}
