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
	"strconv"
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

// EnrollParams are the inputs to an enrollment. Scope (destinations/repos/TTL)
// comes from the named role, not from enrollment-time params.
type EnrollParams struct {
	ID      string
	Project string
	Role    string
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
		Destinations: r.Scope.Destinations, Repos: r.Scope.Repos, Addressing: r.Scope.Addressing,
		TTLSeconds:   int64(r.Scope.TTL / time.Second),
		Kit:          r.Kit,
		MaxEphemeral: r.Allocation.MaxEphemeral,
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
		roles = append(roles, harbor.Role{Name: rs.Name, Kit: rs.Kit, Scope: harbor.Scope{
			Destinations: rs.Destinations, Repos: rs.Repos, Addressing: rs.Addressing, TTL: time.Duration(rs.TTLSeconds) * time.Second,
		}, Allocation: harbor.RoleAllocation{MaxEphemeral: rs.MaxEphemeral}})
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

// PushKit pushes a new version of a kit config, returning the new version number.
func (c *Client) PushKit(name, config string) (int, error) {
	var res harbor.KitResult
	err := c.do("POST", "/admin/kits", harbor.KitBody{Name: name, Config: config}, &res)
	return res.Version, err
}

// ListKits lists every kit in the registry.
func (c *Client) ListKits() ([]harbor.KitSummary, error) {
	var out []harbor.KitSummary
	err := c.do("GET", "/admin/kits", nil, &out)
	return out, err
}

// GetKit fetches a kit's config. version == 0 fetches the current version.
func (c *Client) GetKit(name string, version int) (harbor.KitConfigResult, error) {
	var out harbor.KitConfigResult
	path := "/admin/kits/" + name
	if version > 0 {
		path += "?version=" + strconv.Itoa(version)
	}
	err := c.do("GET", path, nil, &out)
	return out, err
}

// KitVersions lists a kit's version numbers, ascending.
func (c *Client) KitVersions(name string) ([]int, error) {
	var out []int
	err := c.do("GET", "/admin/kits/"+name+"/versions", nil, &out)
	return out, err
}

// PinKit rolls a kit's current pointer to an existing version.
func (c *Client) PinKit(name string, version int) error {
	return c.do("POST", "/admin/kits/"+name+"/pin", harbor.PinBody{Version: version}, nil)
}

// RemoveKit deletes a kit from the registry. Fails (409 from the server) if a
// role still references it.
func (c *Client) RemoveKit(name string) error {
	return c.do("DELETE", "/admin/kits/"+name, nil, nil)
}

// AddHuman upserts a roster human (by name) within project.
func (c *Client) AddHuman(project string, h harbor.Human) error {
	return c.do("POST", "/admin/projects/"+url.PathEscape(project)+"/humans", h, nil)
}

// AddChannel upserts a roster channel (by name) within project.
func (c *Client) AddChannel(project string, ch harbor.Channel) error {
	return c.do("POST", "/admin/projects/"+url.PathEscape(project)+"/channels", ch, nil)
}

// GetRoster fetches project's addressable roster (humans + channels).
func (c *Client) GetRoster(project string) (harbor.Roster, error) {
	var rr harbor.Roster
	err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/roster", nil, &rr)
	return rr, err
}

// RemoveHuman removes a human (by name) from project's roster.
func (c *Client) RemoveHuman(project, name string) error {
	return c.do("DELETE", "/admin/projects/"+url.PathEscape(project)+"/humans/"+url.PathEscape(name), nil, nil)
}

// RemoveChannel removes a channel (by name) from project's roster.
func (c *Client) RemoveChannel(project, name string) error {
	return c.do("DELETE", "/admin/projects/"+url.PathEscape(project)+"/channels/"+url.PathEscape(name), nil, nil)
}

// SetEscalationPolicy replaces the tier chain for project's category wholesale
// ("" targets the default/uncategorized chain).
func (c *Client) SetEscalationPolicy(project, category string, tiers []harbor.EscalationTier) error {
	return c.do("PUT", "/admin/projects/"+url.PathEscape(project)+"/escalation", harbor.EscalationBody{Category: category, Tiers: tiers}, nil)
}

// GetEscalationPolicy fetches project's escalation policy: the default chain
// plus any category overrides.
func (c *Client) GetEscalationPolicy(project string) (harbor.EscalationView, error) {
	var v harbor.EscalationView
	err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/escalation", nil, &v)
	return v, err
}

// SetChatService sets (or clears, with "") the chat service backing project's
// human DMs.
func (c *Client) SetChatService(project, service string) error {
	return c.do("PUT", "/admin/projects/"+url.PathEscape(project)+"/chat-service", harbor.ChatServiceBody{Service: service}, nil)
}

// GetChatService returns project's configured chat service ("" if none).
func (c *Client) GetChatService(project string) (string, error) {
	var v harbor.ChatServiceView
	if err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/chat-service", nil, &v); err != nil {
		return "", err
	}
	return v.Service, nil
}

// CoveRaiseParams are the inputs to raising a managed cove. Scope/kit come from
// the named role.
type CoveRaiseParams struct {
	ID      string
	Project string
	Role    string
	Unit    string
	Prompt  string // workload prompt for the raised cove's agent; read from a file host-side, never argv
}

// RaiseCove raises a managed cove for a role and returns its runtime result
// (including the identity token, once).
func (c *Client) RaiseCove(p CoveRaiseParams) (harbor.CoveRaiseResult, error) {
	var res harbor.CoveRaiseResult
	err := c.do("POST", "/admin/coves", harbor.CoveRaiseBody{ID: p.ID, Project: p.Project, Role: p.Role, Unit: p.Unit, Prompt: p.Prompt}, &res)
	return res, err
}

// ListCoves lists the managed-cove runtime registry (never a token or hash).
func (c *Client) ListCoves() ([]harbor.CoveSummary, error) {
	var out []harbor.CoveSummary
	err := c.do("GET", "/admin/coves", nil, &out)
	return out, err
}

// ReportCoveStatus reports a cove's activity (running|waiting|blocked|done).
func (c *Client) ReportCoveStatus(id, activity string) error {
	return c.do("POST", "/admin/coves/"+url.PathEscape(id)+"/status", harbor.CoveStatusBody{Activity: activity}, nil)
}

// TeardownCove tears a managed cove down and deregisters it.
func (c *Client) TeardownCove(id string) error {
	return c.do("DELETE", "/admin/coves/"+url.PathEscape(id), nil, nil)
}
