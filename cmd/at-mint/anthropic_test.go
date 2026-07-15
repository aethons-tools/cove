package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMintAnthropicTwoHops(t *testing.T) {
	var hop1Body, hop2Body map[string]any
	var hop2URL string
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.String(), "tenant.us.auth0.com/oauth/token"):
			_ = json.Unmarshal(b, &hop1Body)
			return resp(200, `{"access_token":"upstream.jwt.sig","token_type":"Bearer","expires_in":900}`), nil
		case strings.Contains(r.URL.String(), "api.anthropic.com/v1/oauth/token"):
			hop2URL = r.URL.String()
			_ = json.Unmarshal(b, &hop2Body)
			return resp(200, `{"access_token":"sk-ant-oat01-xyz","expires_in":600}`), nil
		default:
			t.Fatalf("unexpected URL %s", r.URL)
			return nil, nil
		}
	})}
	in := anthropicInput{
		Tenant: "tenant.us.auth0.com", ClientID: "cid", Audience: "urn:cove:wif", ClientSecret: "shh",
		Org: "org-uuid", Rule: "fdrl_1", ServiceAccount: "svac_1", Workspace: "wrkspc_1",
	}
	tok, err := mintAnthropic(context.Background(), c, in)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "sk-ant-oat01-xyz" {
		t.Fatalf("token = %q", tok.AccessToken)
	}
	// expires_in from the hop-2 response is preserved, not discarded.
	if tok.ExpiresIn != 600 {
		t.Fatalf("expires_in = %d; want 600", tok.ExpiresIn)
	}
	// hop 1: client-credentials with audience + secret
	if hop1Body["grant_type"] != "client_credentials" || hop1Body["client_id"] != "cid" ||
		hop1Body["client_secret"] != "shh" || hop1Body["audience"] != "urn:cove:wif" {
		t.Fatalf("hop1 body = %v", hop1Body)
	}
	// hop 2: jwt-bearer with the upstream assertion + federation ids
	if hop2URL != "https://api.anthropic.com/v1/oauth/token" {
		t.Fatalf("hop2 url = %q", hop2URL)
	}
	if hop2Body["grant_type"] != "urn:ietf:params:oauth:grant-type:jwt-bearer" ||
		hop2Body["assertion"] != "upstream.jwt.sig" || hop2Body["federation_rule_id"] != "fdrl_1" ||
		hop2Body["service_account_id"] != "svac_1" || hop2Body["organization_id"] != "org-uuid" ||
		hop2Body["workspace_id"] != "wrkspc_1" {
		t.Fatalf("hop2 body = %v", hop2Body)
	}
}

func TestMintAnthropicHop1FailsClosed(t *testing.T) {
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return resp(401, `{"error":"access_denied"}`), nil
	})}
	in := anthropicInput{Tenant: "t.auth0.com", ClientID: "c", Audience: "a", ClientSecret: "s", Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"}
	if _, err := mintAnthropic(context.Background(), c, in); err == nil {
		t.Fatal("hop1 non-2xx must be an error")
	}
}

func TestMintAnthropicOmitsEmptyWorkspace(t *testing.T) {
	var hop2Body map[string]any
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.String(), "auth0.com") {
			return resp(200, `{"access_token":"j"}`), nil
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &hop2Body)
		return resp(200, `{"access_token":"sk-ant-oat01-z"}`), nil
	})}
	in := anthropicInput{Tenant: "t.auth0.com", ClientID: "c", Audience: "a", ClientSecret: "s", Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"} // no Workspace
	if _, err := mintAnthropic(context.Background(), c, in); err != nil {
		t.Fatal(err)
	}
	if _, present := hop2Body["workspace_id"]; present {
		t.Fatal("workspace_id must be omitted when Workspace is empty")
	}
}

// anthropicArgs is the minimal valid flag set for `at-mint anthropic`.
func anthropicArgs() []string {
	return []string{
		"--auth0-tenant", "tenant.us.auth0.com", "--auth0-client-id", "cid",
		"--auth0-audience", "urn:cove:wif", "--anthropic-org", "org-uuid",
		"--anthropic-rule", "fdrl_1", "--anthropic-service-account", "svac_1",
	}
}

// envFunc builds a lookup func over a map, matching the shape doAnthropic expects.
func envFunc(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// anthropicClient returns a fake HTTP client whose hop-2 response advertises the
// given expires_in.
func anthropicClient(expiresIn int) *http.Client {
	return &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.String(), "auth0.com") {
			return resp(200, `{"access_token":"j"}`), nil
		}
		return resp(200, fmt.Sprintf(`{"access_token":"sk-ant-oat01-xyz","expires_in":%d}`, expiresIn)), nil
	})}
}

func TestDoAnthropicFailsClosedWhenTokenExpiresBeforeRunEnds(t *testing.T) {
	var out, errb bytes.Buffer
	env := envFunc(map[string]string{"AT_MINT_AUTH0_CLIENT_SECRET": "shh", "COVE_RUN_TIMEOUT": "90m0s"})
	code := doAnthropic(anthropicArgs(), env, anthropicClient(90), &out, &errb)
	if code == 0 {
		t.Fatalf("expected non-zero exit when the token TTL (90s) is below the run timeout (90m)")
	}
	if out.Len() != 0 {
		t.Fatalf("no token must be printed on a fail-closed exit; got %q", out.String())
	}
	if !strings.Contains(errb.String(), "shorter than the run timeout") {
		t.Fatalf("stderr must explain the TTL/timeout mismatch; got %q", errb.String())
	}
}

func TestDoAnthropicAllowsTokenThatOutlastsRun(t *testing.T) {
	var out, errb bytes.Buffer
	env := envFunc(map[string]string{"AT_MINT_AUTH0_CLIENT_SECRET": "shh", "COVE_RUN_TIMEOUT": "90m0s"})
	code := doAnthropic(anthropicArgs(), env, anthropicClient(7200), &out, &errb)
	if code != 0 {
		t.Fatalf("expected exit 0 when TTL (7200s) exceeds the run timeout; stderr=%q", errb.String())
	}
	if strings.TrimSpace(out.String()) != "sk-ant-oat01-xyz" {
		t.Fatalf("token = %q", out.String())
	}
	if !strings.Contains(errb.String(), "expires_in=7200s") {
		t.Fatalf("stderr should surface expires_in; got %q", errb.String())
	}
}

func TestDoAnthropicSkipsTTLCheckWithoutRunTimeout(t *testing.T) {
	var out, errb bytes.Buffer
	env := envFunc(map[string]string{"AT_MINT_AUTH0_CLIENT_SECRET": "shh"}) // no COVE_RUN_TIMEOUT (e.g. chat/manual)
	code := doAnthropic(anthropicArgs(), env, anthropicClient(90), &out, &errb)
	if code != 0 {
		t.Fatalf("without COVE_RUN_TIMEOUT the TTL check must not fire; exit=%d stderr=%q", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "sk-ant-oat01-xyz" {
		t.Fatalf("token = %q", out.String())
	}
}
