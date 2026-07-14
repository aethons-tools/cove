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

// mintAnthropic mints an sk-ant-oat01 via two hops: an Auth0 client-credentials
// JWT (hop 1) exchanged through Anthropic federation (hop 2).
func mintAnthropic(ctx context.Context, httpc *http.Client, in anthropicInput) (string, error) {
	if in.Tenant == "" || in.ClientID == "" || in.Audience == "" || in.ClientSecret == "" {
		return "", fmt.Errorf("auth0 tenant, client-id, audience and client secret are required")
	}
	if in.Org == "" || in.Rule == "" || in.ServiceAccount == "" {
		return "", fmt.Errorf("anthropic org, rule and service-account are required")
	}
	assertion, err := auth0Token(ctx, httpc, in)
	if err != nil {
		return "", err
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
	return tok, nil
}

func anthropicExchange(ctx context.Context, httpc *http.Client, in anthropicInput, assertion string) (string, error) {
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
		return "", err
	}
	tok, err := postForToken(ctx, httpc, "https://api.anthropic.com/v1/oauth/token",
		map[string]string{"anthropic-version": "2023-06-01"}, body)
	if err != nil {
		return "", fmt.Errorf("anthropic federation exchange: %w", err)
	}
	return tok, nil
}

// postForToken POSTs a JSON body and returns the response's access_token, failing
// closed on any non-2xx or a missing token.
func postForToken(ctx context.Context, httpc *http.Client, url string, extraHeaders map[string]string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	res, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(buf.String()))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("response contained no access_token")
	}
	return out.AccessToken, nil
}
