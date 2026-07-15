package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type anthropicInput struct {
	Tenant         string
	ClientID       string
	Audience       string
	ClientSecret   string
	Org            string
	Rule           string
	ServiceAccount string
	Workspace      string
}

// tokenResp is the subset of an OAuth token response we care about. ExpiresIn is
// the token's lifetime in seconds (0 when the server omits it); RefreshToken is
// non-empty only when the server offers one.
type tokenResp struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// mintAnthropic mints an sk-ant-oat01 via two hops: an Auth0 client-credentials
// JWT (hop 1) exchanged through Anthropic federation (hop 2). It returns the full
// token response so the caller can surface the lifetime (expires_in), which the
// federation rule sets server-side — at-mint requests no TTL.
func mintAnthropic(ctx context.Context, httpc *http.Client, in anthropicInput) (tokenResp, error) {
	if in.Tenant == "" || in.ClientID == "" || in.Audience == "" || in.ClientSecret == "" {
		return tokenResp{}, fmt.Errorf("auth0 tenant, client-id, audience and client secret are required")
	}
	if in.Org == "" || in.Rule == "" || in.ServiceAccount == "" {
		return tokenResp{}, fmt.Errorf("anthropic org, rule and service-account are required")
	}
	assertion, err := auth0Token(ctx, httpc, in)
	if err != nil {
		return tokenResp{}, err
	}
	return anthropicExchange(ctx, httpc, in, assertion)
}

func auth0Token(ctx context.Context, httpc *http.Client, in anthropicInput) (string, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     in.ClientID,
		"client_secret": in.ClientSecret,
		"audience":      in.Audience,
	})
	if err != nil {
		return "", err
	}
	url := "https://" + in.Tenant + "/oauth/token"
	tok, err := postForToken(ctx, httpc, url, nil, body)
	if err != nil {
		return "", fmt.Errorf("auth0 client-credentials: %w", err)
	}
	return tok.AccessToken, nil
}

func anthropicExchange(ctx context.Context, httpc *http.Client, in anthropicInput, assertion string) (tokenResp, error) {
	payload := map[string]string{
		"grant_type":         "urn:ietf:params:oauth:grant-type:jwt-bearer",
		"assertion":          assertion,
		"federation_rule_id": in.Rule,
		"service_account_id": in.ServiceAccount,
		"organization_id":    in.Org,
	}
	if in.Workspace != "" {
		payload["workspace_id"] = in.Workspace
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return tokenResp{}, err
	}
	tok, err := postForToken(ctx, httpc, "https://api.anthropic.com/v1/oauth/token",
		map[string]string{"anthropic-version": "2023-06-01"}, body)
	if err != nil {
		return tokenResp{}, fmt.Errorf("anthropic federation exchange: %w", err)
	}
	return tok, nil
}

// postForToken POSTs a JSON body and returns the parsed token response, failing
// closed on any non-2xx or a missing access_token. expires_in / refresh_token are
// preserved so callers can reason about the token's lifetime.
func postForToken(ctx context.Context, httpc *http.Client, url string, extraHeaders map[string]string, body []byte) (tokenResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return tokenResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	res, err := httpc.Do(req)
	if err != nil {
		return tokenResp{}, err
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		return tokenResp{}, fmt.Errorf("reading response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return tokenResp{}, fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(buf.String()))
	}
	var out tokenResp
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return tokenResp{}, fmt.Errorf("parse token response: %w", err)
	}
	if out.AccessToken == "" {
		return tokenResp{}, fmt.Errorf("response contained no access_token")
	}
	return out, nil
}
