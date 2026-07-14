package usersecret

import "fmt"

// Minter is a named minting profile: a tagged union over the code host / token
// kind. Exactly one provider is set. Parsed and validated here; the actual
// minting (running at-mint) is wired in later plans.
type Minter struct {
	GitHub    *GitHubMinter    `yaml:"github,omitempty"`
	Anthropic *AnthropicMinter `yaml:"anthropic,omitempty"`
}

// GitHubMinter mints a repo-scoped GitHub App installation token.
type GitHubMinter struct {
	AppID     string `yaml:"app-id"`
	InstallID string `yaml:"install-id"`
	AppKey    Source `yaml:"app-key"` // PEM: a value (path/content) | command | global
}

// AnthropicMinter mints an Anthropic sk-ant-oat01 via an OIDC IdP JWT (hop 1)
// exchanged through Anthropic federation (hop 2).
type AnthropicMinter struct {
	OIDC       OIDC       `yaml:"oidc"`
	Federation Federation `yaml:"federation"`
}

// OIDC is a tagged union over the identity provider that mints the upstream JWT.
type OIDC struct {
	Auth0 *Auth0 `yaml:"auth0,omitempty"`
}

// Auth0 mints an upstream JWT via the client-credentials grant.
type Auth0 struct {
	Tenant       string `yaml:"tenant"`
	ClientID     string `yaml:"client-id"`
	Audience     string `yaml:"audience"`
	ClientSecret Source `yaml:"client-secret"`
}

// Federation carries the Anthropic-side exchange identifiers.
type Federation struct {
	Org            string `yaml:"org"`
	Rule           string `yaml:"rule"`            // fdrl_...
	ServiceAccount string `yaml:"service-account"` // svac_...
	Workspace      string `yaml:"workspace,omitempty"`
}

// Validate checks the provider union and each provider's required fields.
func (m Minter) Validate() error {
	set := 0
	if m.GitHub != nil {
		set++
	}
	if m.Anthropic != nil {
		set++
	}
	if set != 1 {
		return fmt.Errorf("a minter must set exactly one provider (github|anthropic), set %d", set)
	}
	if g := m.GitHub; g != nil {
		if g.AppID == "" || g.InstallID == "" {
			return fmt.Errorf("github minter: app-id and install-id are required")
		}
		if _, err := g.AppKey.Kind(); err != nil {
			return fmt.Errorf("github minter: app-key: %w", err)
		}
	}
	if a := m.Anthropic; a != nil {
		if a.OIDC.Auth0 == nil {
			return fmt.Errorf("anthropic minter: oidc must set exactly one IdP (auth0)")
		}
		z := a.OIDC.Auth0
		if z.Tenant == "" || z.ClientID == "" || z.Audience == "" {
			return fmt.Errorf("anthropic minter: oidc.auth0 requires tenant, client-id, audience")
		}
		if _, err := z.ClientSecret.Kind(); err != nil {
			return fmt.Errorf("anthropic minter: oidc.auth0.client-secret: %w", err)
		}
		if a.Federation.Org == "" || a.Federation.Rule == "" || a.Federation.ServiceAccount == "" {
			return fmt.Errorf("anthropic minter: federation requires org, rule, service-account")
		}
	}
	return nil
}
