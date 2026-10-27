package mint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/usersecret"
)

func strptr(s string) *string { return &s }

// withExecPath points the execPath seam at a fake for the duration of a test,
// restoring the real os.Executable afterwards.
func withExecPath(t *testing.T, fn func() (string, error)) {
	t.Helper()
	prev := execPath
	execPath = fn
	t.Cleanup(func() { execPath = prev })
}

// withSelf points execPath at an executable "jam" in a temp dir and returns
// its path: what jamBinary should resolve to.
func withSelf(t *testing.T) string {
	t.Helper()
	self := filepath.Join(t.TempDir(), "jam")
	if err := os.WriteFile(self, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withExecPath(t, func() (string, error) { return self, nil })
	return self
}

func TestJamBinaryIsSelf(t *testing.T) {
	self := withSelf(t)
	if got := jamBinary(); got != self {
		t.Fatalf("jamBinary() = %q, want the running jam %q", got, self)
	}
}

func TestJamBinaryFallsBackToPATH(t *testing.T) {
	withExecPath(t, func() (string, error) { return "", errors.New("boom") })
	if got := jamBinary(); got != "jam" {
		t.Fatalf("jamBinary() = %q, want bare fallback %q", got, "jam")
	}
}

// TestJamBinaryAfterReplaceUsesTheInstalledPath: once `jam update` replaces the
// file under a running jam, Linux reports "<path> (deleted)"; the path itself
// holds the new jam, so minting keeps working without a restart.
func TestJamBinaryAfterReplaceUsesTheInstalledPath(t *testing.T) {
	self := filepath.Join(t.TempDir(), "jam")
	if err := os.WriteFile(self, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withExecPath(t, func() (string, error) { return self + " (deleted)", nil })
	if got := jamBinary(); got != self {
		t.Fatalf("jamBinary() = %q, want the replaced path %q", got, self)
	}
}

func TestJamBinaryDeletedAndGoneFallsBackToPATH(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "jam")
	withExecPath(t, func() (string, error) { return gone + " (deleted)", nil })
	if got := jamBinary(); got != "jam" {
		t.Fatalf("jamBinary() = %q, want bare fallback %q", got, "jam")
	}
}

func TestJamBinaryNonExecutableFallsBackToPATH(t *testing.T) {
	self := filepath.Join(t.TempDir(), "jam")
	if err := os.WriteFile(self, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	withExecPath(t, func() (string, error) { return self, nil })
	if got := jamBinary(); got != "jam" {
		t.Fatalf("jamBinary() = %q, want bare fallback %q", got, "jam")
	}
}

func TestGithubProfilePathKeyIsFlagNotEnv(t *testing.T) {
	self := withSelf(t)
	m := usersecret.Minter{GitHub: &usersecret.GitHubMinter{
		AppID: "123", InstallID: "456", AppKey: usersecret.Source{Value: strptr("/etc/cove/gh.pem")},
	}}
	spec, err := Expander(&runner.Fake{}, nil, "acme/widgets")("gh", m, "GIT_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(spec.Command, " ")
	if got != self+" mint github --app-id 123 --install-id 456 --repo acme/widgets --app-key-file /etc/cove/gh.pem" {
		t.Fatalf("argv = %q", got)
	}
	if len(spec.Env) != 0 {
		t.Fatalf("path key must not set env, got %v", spec.Env)
	}
	if spec.Name != "GIT_TOKEN" {
		t.Fatalf("name = %q", spec.Name)
	}
}

func TestGithubProfileCommandKeyGoesToEnv(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "PEMCONTENT\n"}}}
	m := usersecret.Minter{GitHub: &usersecret.GitHubMinter{
		AppID: "1", InstallID: "2", AppKey: usersecret.Source{Command: []string{"cat", "/k"}},
	}}
	spec, err := Expander(f, nil, "o/r")("gh", m, "GIT_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(spec.Command, " "), "PEMCONTENT") {
		t.Fatal("key content leaked into argv")
	}
	if spec.Env["JAM_MINT_GITHUB_APP_KEY"] != "PEMCONTENT" {
		t.Fatalf("env = %v", spec.Env)
	}
	if strings.Contains(strings.Join(spec.Command, " "), "--app-key-file") {
		t.Fatal("command-sourced key must not use --app-key-file")
	}
}

func TestAnthropicProfileFlagsAndSecretEnv(t *testing.T) {
	self := withSelf(t)
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
	want := self + " mint anthropic --auth0-tenant t.us.auth0.com --auth0-client-id cid --auth0-audience aud " +
		"--anthropic-org o --anthropic-rule fdrl_1 --anthropic-service-account svac_1 --anthropic-workspace wrkspc_1"
	if got != want {
		t.Fatalf("argv = %q", got)
	}
	if strings.Contains(got, "shh") {
		t.Fatal("client secret leaked into argv")
	}
	if spec.Env["JAM_MINT_AUTH0_CLIENT_SECRET"] != "shh" {
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
	if spec.Env["JAM_MINT_AUTH0_CLIENT_SECRET"] != "fromglobal" {
		t.Fatalf("global delegation failed: %v", spec.Env)
	}
}
