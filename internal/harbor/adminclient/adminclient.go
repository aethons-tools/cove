// Package adminclient is a typed HTTP client for harbor's loopback admin API.
// Kept dependency-light so a future at-harborctl can reuse it; today it imports
// internal/harbor only for the wire types (harbor is stdlib-only).
package adminclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// ErrNotFound wraps a 404 from the admin API, so callers can distinguish "this
// endpoint/resource isn't there" (e.g. a non-OIDC harbor has no login-config)
// from a transport failure or another HTTP error.
var ErrNotFound = errors.New("not found")

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
		err := fmt.Errorf("admin API %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
		if resp.StatusCode == http.StatusNotFound {
			err = fmt.Errorf("%w: %s", ErrNotFound, err)
		}
		return err
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

// PutRole creates or replaces a role within project. project == "" resolves to
// harbor.DefaultProject server-side.
func (c *Client) PutRole(project string, r harbor.Role) error {
	return c.do("POST", "/admin/roles", harbor.RoleBody{
		Project: project, Name: r.Name,
		Destinations: r.Scope.Destinations, Repos: r.Scope.Repos,
		TTLSeconds: int64(r.Scope.TTL / time.Second),
	}, nil)
}

// ListRoles lists the roles configured for project ("" lists DefaultProject).
func (c *Client) ListRoles(project string) ([]harbor.Role, error) {
	var out []harbor.RoleSummary
	path := "/admin/roles"
	if project != "" {
		path += "?project=" + url.QueryEscape(project)
	}
	if err := c.do("GET", path, nil, &out); err != nil {
		return nil, err
	}
	roles := make([]harbor.Role, 0, len(out))
	for _, rs := range out {
		roles = append(roles, harbor.Role{Name: rs.Name, Scope: harbor.Scope{
			Destinations: rs.Destinations, Repos: rs.Repos, TTL: time.Duration(rs.TTLSeconds) * time.Second,
		}})
	}
	return roles, nil
}

// RemoveRole deletes a role from project.
func (c *Client) RemoveRole(project, name string) error {
	return c.do("DELETE", "/admin/roles/"+project+"/"+name, nil, nil)
}

// ListProjects lists every project that has at least one role or actor grant.
func (c *Client) ListProjects() ([]string, error) {
	var out []string
	err := c.do("GET", "/admin/projects", nil, &out)
	return out, err
}

// Roster lists every enrolled actor with its resolved effective grants — never
// a token or hash.
func (c *Client) Roster() ([]harbor.ActorSummary, error) {
	var out []harbor.ActorSummary
	err := c.do("GET", "/admin/roster", nil, &out)
	return out, err
}

// AddGrant assigns g to the actor identified by actorID.
func (c *Client) AddGrant(actorID string, g harbor.Grant) error {
	return c.do("POST", "/admin/actors/"+actorID+"/grants", harbor.GrantBody{
		Project: g.Project, Role: g.Role, Overrides: g.Overrides,
	}, nil)
}

// RemoveGrant removes actorID's grant of role within project.
func (c *Client) RemoveGrant(actorID, project, role string) error {
	return c.do("DELETE", "/admin/actors/"+actorID+"/grants/"+project+"/"+role, nil, nil)
}

// LoginConfig fetches harbor's public device-flow client parameters from
// GET /admin/login-config. It needs no token (the endpoint is auth-exempt); a
// 404 means the harbor is not OIDC-gated.
func (c *Client) LoginConfig() (harbor.OperatorLoginConfig, error) {
	var lc harbor.OperatorLoginConfig
	err := c.do("GET", "/admin/login-config", nil, &lc)
	return lc, err
}
