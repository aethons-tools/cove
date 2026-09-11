// Package adminclient is a typed HTTP client for harbor's loopback admin API.
// Kept dependency-light so a future at-harborctl can reuse it; today it imports
// internal/harbor only for the wire types (harbor is stdlib-only).
package adminclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// Client talks to a running harbor's admin API (e.g. http://127.0.0.1:8081).
type Client struct {
	base  string
	token string
	httpc *http.Client
}

// New returns a Client for the admin base URL (no trailing slash needed). token
// (may be "") is sent as a bearer on every request — required against an
// OIDC-gated harbor, ignored by a loopback-gated one.
func New(baseURL, token string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, httpc: &http.Client{Timeout: 10 * time.Second}}
}

// EnrollParams are the inputs to an enrollment.
type EnrollParams struct {
	ID           string
	Project      string
	Role         string
	Destinations []string
	Repos        []string
	TTL          time.Duration
}

func (c *Client) do(method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("admin API unreachable at %s (is `at-harbor serve` running?): %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("admin API %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Enroll(p EnrollParams) (harbor.EnrollResult, error) {
	var res harbor.EnrollResult
	err := c.do("POST", "/admin/enrollments", harbor.EnrollBody{
		ID: p.ID, Project: p.Project, Role: p.Role,
		Destinations: p.Destinations, Repos: p.Repos, TTLSeconds: int64(p.TTL / time.Second),
	}, &res)
	return res, err
}

func (c *Client) Revoke(id string) error {
	return c.do("DELETE", "/admin/enrollments/"+id, nil, nil)
}

func (c *Client) AddDestination(d harbor.Destination) error {
	return c.do("POST", "/admin/destinations", d, nil)
}

func (c *Client) ListDestinations() ([]harbor.Destination, error) {
	var out []harbor.Destination
	err := c.do("GET", "/admin/destinations", nil, &out)
	return out, err
}

func (c *Client) RemoveDestination(name string) error {
	return c.do("DELETE", "/admin/destinations/"+name, nil, nil)
}

// LoginConfig fetches harbor's public device-flow client parameters from
// GET /admin/login-config. It needs no token (the endpoint is auth-exempt); a
// 404 means the harbor is not OIDC-gated.
func (c *Client) LoginConfig() (harbor.OperatorLoginConfig, error) {
	var lc harbor.OperatorLoginConfig
	err := c.do("GET", "/admin/login-config", nil, &lc)
	return lc, err
}
