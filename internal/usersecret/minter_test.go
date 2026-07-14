package usersecret

import "testing"

func gh() *GitHubMinter {
	return &GitHubMinter{AppID: "1", InstallID: "2", AppKey: Source{Value: strptr("k")}}
}
func strptr(s string) *string { return &s }

func TestMinterValidate(t *testing.T) {
	okAnthropic := &AnthropicMinter{
		OIDC:       OIDC{Auth0: &Auth0{Tenant: "t", ClientID: "c", Audience: "a", ClientSecret: Source{Command: []string{"pass", "x"}}}},
		Federation: Federation{Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"},
	}
	cases := []struct {
		name string
		m    Minter
		err  bool
	}{
		{"github-ok", Minter{GitHub: gh()}, false},
		{"anthropic-ok", Minter{Anthropic: okAnthropic}, false},
		{"no-provider", Minter{}, true},
		{"two-providers", Minter{GitHub: gh(), Anthropic: okAnthropic}, true},
		{"github-missing-appid", Minter{GitHub: &GitHubMinter{InstallID: "2", AppKey: Source{Value: strptr("k")}}}, true},
		{"github-bad-appkey-source", Minter{GitHub: &GitHubMinter{AppID: "1", InstallID: "2", AppKey: Source{}}}, true},
		{"anthropic-no-idp", Minter{Anthropic: &AnthropicMinter{Federation: okAnthropic.Federation}}, true},
		{"anthropic-missing-rule", Minter{Anthropic: &AnthropicMinter{OIDC: okAnthropic.OIDC, Federation: Federation{Org: "o", ServiceAccount: "svac_1"}}}, true},
		{"anthropic-bad-secret-source", Minter{Anthropic: &AnthropicMinter{OIDC: OIDC{Auth0: &Auth0{Tenant: "t", ClientID: "c", Audience: "a", ClientSecret: Source{}}}, Federation: okAnthropic.Federation}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.m.Validate()
			if c.err && err == nil {
				t.Fatal("want error, got nil")
			}
			if !c.err && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
