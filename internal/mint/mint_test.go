package mint

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/usersecret"
)

func strptr(s string) *string { return &s }

func TestGithubProfilePathKeyIsFlagNotEnv(t *testing.T) {
	m := usersecret.Minter{GitHub: &usersecret.GitHubMinter{
		AppID: "123", InstallID: "456", AppKey: usersecret.Source{Value: strptr("/etc/cove/gh.pem")},
	}}
	spec, err := Expander(&runner.Fake{}, nil, "acme/widgets")("gh", m, "AT_TASK_GIT_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(spec.Command, " ")
	if got != "at-mint github --app-id 123 --install-id 456 --repo acme/widgets --app-key-file /etc/cove/gh.pem" {
		t.Fatalf("argv = %q", got)
	}
	if len(spec.Env) != 0 {
		t.Fatalf("path key must not set env, got %v", spec.Env)
	}
	if spec.Name != "AT_TASK_GIT_TOKEN" {
		t.Fatalf("name = %q", spec.Name)
	}
}

func TestGithubProfileCommandKeyGoesToEnv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "PEMCONTENT\n"}}}
	m := usersecret.Minter{GitHub: &usersecret.GitHubMinter{
		AppID: "1", InstallID: "2", AppKey: usersecret.Source{Command: []string{"cat", "/k"}},
	}}
	spec, err := Expander(f, nil, "o/r")("gh", m, "AT_TASK_GIT_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(spec.Command, " "), "PEMCONTENT") {
		t.Fatal("key content leaked into argv")
	}
	if spec.Env["AT_MINT_GITHUB_APP_KEY"] != "PEMCONTENT" {
		t.Fatalf("env = %v", spec.Env)
	}
	if strings.Contains(strings.Join(spec.Command, " "), "--app-key-file") {
		t.Fatal("command-sourced key must not use --app-key-file")
	}
}

func TestAnthropicProfileFlagsAndSecretEnv(t *testing.T) {
	m := usersecret.Minter{Anthropic: &usersecret.AnthropicMinter{
		OIDC: usersecret.OIDC{Auth0: &usersecret.Auth0{
			Tenant: "t.us.auth0.com", ClientID: "cid", Audience: "aud",
			ClientSecret: usersecret.Source{Value: strptr("shh")},
		}},
		Federation: usersecret.Federation{Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1", Workspace: "wrkspc_1"},
	}}
	spec, err := Expander(&runner.Fake{}, nil, "")("a", m, "ANTHROPIC_AUTH_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(spec.Command, " ")
	want := "at-mint anthropic --auth0-tenant t.us.auth0.com --auth0-client-id cid --auth0-audience aud " +
		"--anthropic-org o --anthropic-rule fdrl_1 --anthropic-service-account svac_1 --anthropic-workspace wrkspc_1"
	if got != want {
		t.Fatalf("argv = %q", got)
	}
	if strings.Contains(got, "shh") {
		t.Fatal("client secret leaked into argv")
	}
	if spec.Env["AT_MINT_AUTH0_CLIENT_SECRET"] != "shh" {
		t.Fatalf("env = %v", spec.Env)
	}
}

func TestAnthropicOmitsEmptyWorkspace(t *testing.T) {
	m := usersecret.Minter{Anthropic: &usersecret.AnthropicMinter{
		OIDC:       usersecret.OIDC{Auth0: &usersecret.Auth0{Tenant: "t", ClientID: "c", Audience: "a", ClientSecret: usersecret.Source{Value: strptr("s")}}},
		Federation: usersecret.Federation{Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"},
	}}
	spec, _ := Expander(&runner.Fake{}, nil, "")("a", m, "T")
	if strings.Contains(strings.Join(spec.Command, " "), "--anthropic-workspace") {
		t.Fatal("empty workspace must be omitted")
	}
}

func TestGlobalDelegationResolvesSecret(t *testing.T) {
	globals := map[string]usersecret.Source{"shared": {Value: strptr("fromglobal")}}
	m := usersecret.Minter{Anthropic: &usersecret.AnthropicMinter{
		OIDC:       usersecret.OIDC{Auth0: &usersecret.Auth0{Tenant: "t", ClientID: "c", Audience: "a", ClientSecret: usersecret.Source{Global: "shared"}}},
		Federation: usersecret.Federation{Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"},
	}}
	spec, err := Expander(&runner.Fake{}, globals, "")("a", m, "T")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["AT_MINT_AUTH0_CLIENT_SECRET"] != "fromglobal" {
		t.Fatalf("global delegation failed: %v", spec.Env)
	}
}
