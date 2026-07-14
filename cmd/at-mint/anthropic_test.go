package main

import (
	"context"
	"encoding/json"
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
	if tok != "sk-ant-oat01-xyz" {
		t.Fatalf("token = %q", tok)
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
