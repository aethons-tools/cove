// Package deviceflow implements the OAuth 2.0 device authorization grant
// (RFC 8628) as a public client — no client secret, no redirect server. It is
// stdlib-only and does NOT import go-oidc (harbor's server-side token verifier);
// it merely obtains a token that harbor later verifies.
package deviceflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPDoer is the subset of *http.Client the flow needs (injected for tests).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Config is the public client configuration harbor advertises.
type Config struct {
	Issuer   string
	Audience string
	ClientID string
	Scope    string
}

// DeviceCode is the device authorization response (RFC 8628 §3.2) plus the
// token endpoint discovered alongside it, so the caller can poll without
// re-running discovery.
type DeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	TokenEndpoint           string
	Interval                int // seconds
	ExpiresIn               int // seconds
}

// Token is the minted access token (only the bearer + its lifetime are needed).
type Token struct {
	AccessToken string
	ExpiresIn   int
}

type discoveryDoc struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

func discover(ctx context.Context, doer HTTPDoer, issuer string) (discoveryDoc, error) {
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return discoveryDoc{}, err
	}
	resp, err := doer.Do(req)
	if err != nil {
		return discoveryDoc{}, fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return discoveryDoc{}, fmt.Errorf("oidc discovery: %s", resp.Status)
	}
	var d discoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return discoveryDoc{}, fmt.Errorf("oidc discovery decode: %w", err)
	}
	if d.DeviceAuthorizationEndpoint == "" || d.TokenEndpoint == "" {
		return discoveryDoc{}, fmt.Errorf("issuer %q does not advertise device-flow endpoints", issuer)
	}
	return d, nil
}

// RequestDeviceCode starts the flow: discovery, then POST the device
// authorization endpoint with client_id/scope/audience.
func RequestDeviceCode(ctx context.Context, doer HTTPDoer, cfg Config) (DeviceCode, error) {
	d, err := discover(ctx, doer, cfg.Issuer)
	if err != nil {
		return DeviceCode{}, err
	}
	form := url.Values{"client_id": {cfg.ClientID}, "scope": {cfg.Scope}, "audience": {cfg.Audience}}
	var out struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
		ExpiresIn               int    `json:"expires_in"`
		Error                   string `json:"error"`
		ErrorDescription        string `json:"error_description"`
	}
	if status, err := postForm(ctx, doer, d.DeviceAuthorizationEndpoint, form, &out); err != nil {
		return DeviceCode{}, fmt.Errorf("device authorization: %w", err)
	} else if status != http.StatusOK {
		return DeviceCode{}, fmt.Errorf("device authorization: %s", oauthError(status, out.Error, out.ErrorDescription))
	}
	interval := out.Interval
	if interval <= 0 {
		interval = 5 // RFC 8628 default
	}
	return DeviceCode{
		DeviceCode: out.DeviceCode, UserCode: out.UserCode,
		VerificationURI: out.VerificationURI, VerificationURIComplete: out.VerificationURIComplete,
		TokenEndpoint: d.TokenEndpoint, Interval: interval, ExpiresIn: out.ExpiresIn,
	}, nil
}

// PollToken polls the token endpoint until the user approves, honoring
// authorization_pending (keep waiting), slow_down (widen the interval), and
// treating access_denied/expired_token as terminal. sleep is injected for tests.
func PollToken(ctx context.Context, doer HTTPDoer, sleep func(time.Duration), tokenEndpoint, clientID, deviceCode string, interval int) (Token, error) {
	if interval <= 0 {
		interval = 5
	}
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode}, "client_id": {clientID},
	}
	for {
		if err := ctx.Err(); err != nil {
			return Token{}, err
		}
		var out struct {
			AccessToken      string `json:"access_token"`
			ExpiresIn        int    `json:"expires_in"`
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		status, err := postForm(ctx, doer, tokenEndpoint, form, &out)
		if err != nil {
			return Token{}, err
		}
		switch {
		case status == http.StatusOK && out.AccessToken != "":
			return Token{AccessToken: out.AccessToken, ExpiresIn: out.ExpiresIn}, nil
		case out.Error == "authorization_pending":
			// keep polling
		case out.Error == "slow_down":
			interval += 5
		case out.Error == "access_denied":
			return Token{}, fmt.Errorf("login was denied")
		case out.Error == "expired_token":
			return Token{}, fmt.Errorf("login timed out")
		default:
			return Token{}, fmt.Errorf("device token poll failed: %s", oauthError(status, out.Error, out.ErrorDescription))
		}
		sleep(time.Duration(interval) * time.Second)
	}
}

// oauthError renders an actionable message from an OAuth error response,
// preferring the provider's error_description (e.g. Auth0's "Grant type
// 'device_code' not allowed for the client") over a bare HTTP status.
func oauthError(status int, code, desc string) string {
	switch {
	case desc != "" && code != "":
		return fmt.Sprintf("%s (%s)", desc, code)
	case desc != "":
		return desc
	case code != "":
		return fmt.Sprintf("%s (status %d)", code, status)
	default:
		return fmt.Sprintf("status %d", status)
	}
}

// postForm POSTs a urlencoded form and JSON-decodes the body into out (when
// non-empty). It returns the HTTP status so callers can branch on it.
func postForm(ctx context.Context, doer HTTPDoer, endpoint string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doer.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if len(body) > 0 && out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}
