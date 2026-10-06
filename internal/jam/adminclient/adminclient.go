// Package adminclient is a typed HTTP client for Jam's loopback admin API.
// Kept dependency-light so a future at-jamctl can reuse it; today it imports
// internal/jam only for the wire types (Jam is stdlib-only).
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

	"github.com/aethons-tools/cove/internal/jam"
)

// ErrNotFound wraps a 404 from the admin API, so callers can distinguish "this
// endpoint/resource isn't there" (e.g. a non-OIDC Jam has no login-config)
// from a transport failure or another HTTP error.
var ErrNotFound = errors.New("not found")

// ErrConflict wraps a 409 from the admin API — e.g. importing config into a
// store that is not empty.
var ErrConflict = errors.New("conflict")

// Client talks to a running Jam's admin API (e.g. http://127.0.0.1:8081).
type Client struct {
	base  string
	token string
	httpc *http.Client
}

// New returns a Client for the admin base URL (no trailing slash needed). token
// (may be "") is sent as a bearer on every request — required against an
// OIDC-gated Jam, ignored by a loopback-gated one.
func New(baseURL, token string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, httpc: &http.Client{Timeout: 10 * time.Second}}
}

// EnrollParams are the inputs to an enrollment. Scope (destinations/TTL)
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
		return fmt.Errorf("admin API unreachable at %s (is `at-jam serve` running?): %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("admin API %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
		if resp.StatusCode == http.StatusNotFound {
			err = fmt.Errorf("%w: %s", ErrNotFound, err)
		}
		if resp.StatusCode == http.StatusConflict {
			err = fmt.Errorf("%w: %s", ErrConflict, err)
		}
		return err
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Enroll(p EnrollParams) (jam.EnrollResult, error) {
	var res jam.EnrollResult
	err := c.do("POST", "/admin/enrollments", jam.EnrollBody{
		ID: p.ID, Project: p.Project, Role: p.Role,
	}, &res)
	return res, err
}

func (c *Client) Revoke(id string) error {
	return c.do("DELETE", "/admin/enrollments/"+id, nil, nil)
}

func (c *Client) AddDestination(d jam.Destination) error {
	return c.do("POST", "/admin/destinations", d, nil)
}

func (c *Client) ListDestinations() ([]jam.Destination, error) {
	var out []jam.Destination
	err := c.do("GET", "/admin/destinations", nil, &out)
	return out, err
}

func (c *Client) RemoveDestination(name string) error {
	return c.do("DELETE", "/admin/destinations/"+name, nil, nil)
}

// CreateModelSpec adds a new model-spec (ErrConflict if the name exists).
func (c *Client) CreateModelSpec(m jam.ModelSpec) error {
	return c.do("POST", "/admin/model-specs", m, nil)
}

// UpdateModelSpec replaces an existing model-spec (ErrNotFound if absent).
func (c *Client) UpdateModelSpec(m jam.ModelSpec) error {
	return c.do("PUT", "/admin/model-specs/"+url.PathEscape(m.Name), m, nil)
}

// GetModelSpec fetches one model-spec (ErrNotFound if absent).
func (c *Client) GetModelSpec(name string) (jam.ModelSpec, error) {
	var out jam.ModelSpec
	err := c.do("GET", "/admin/model-specs/"+url.PathEscape(name), nil, &out)
	return out, err
}

// ListModelSpecs lists every model-spec, sorted by name.
func (c *Client) ListModelSpecs() ([]jam.ModelSpec, error) {
	var out []jam.ModelSpec
	err := c.do("GET", "/admin/model-specs", nil, &out)
	return out, err
}

// DeleteModelSpec removes a model-spec (ErrNotFound if absent).
func (c *Client) DeleteModelSpec(name string) error {
	return c.do("DELETE", "/admin/model-specs/"+url.PathEscape(name), nil, nil)
}

// PutRole creates or replaces a role within project. project == "" resolves to
// jam.DefaultProject server-side.
func (c *Client) PutRole(project string, r jam.Role) error {
	return c.do("POST", "/admin/roles", jam.RoleBody{
		Project: project, Name: r.Name,
		Destinations: r.Scope.Destinations, Credentials: r.Scope.Credentials, Addressing: r.Scope.Addressing,
		TTLSeconds:          int64(r.Scope.TTL / time.Second),
		Kit:                 r.Kit,
		ModelSpec:           r.ModelSpec,
		MaxEphemeral:        r.Allocation.MaxEphemeral,
		MaxPersonal:         r.Allocation.MaxPersonal,
		MaxPersonalPerOwner: r.Allocation.MaxPersonalPerOwner,
		IdleAfterSeconds:    int64(r.Allocation.IdleAfter / time.Second),
		NagEverySeconds:     int64(r.Allocation.NagEvery / time.Second),
		ReclaimAfterSeconds: int64(r.Allocation.ReclaimAfter / time.Second),
		IdleTimeoutSeconds:  int64(r.TurnEnd.IdleTimeout / time.Second),
		OnIdle:              r.TurnEnd.OnIdle,
		TimeZone:            r.TurnEnd.TimeZone,
	}, nil)
}

// ListRoles lists the roles configured for project ("" lists DefaultProject).
func (c *Client) ListRoles(project string) ([]jam.Role, error) {
	var out []jam.RoleSummary
	path := "/admin/roles"
	if project != "" {
		path += "?project=" + url.QueryEscape(project)
	}
	if err := c.do("GET", path, nil, &out); err != nil {
		return nil, err
	}
	roles := make([]jam.Role, 0, len(out))
	for _, rs := range out {
		roles = append(roles, jam.Role{Name: rs.Name, Kit: rs.Kit, ModelSpec: rs.ModelSpec, Scope: jam.Scope{
			Destinations: rs.Destinations, Credentials: rs.Credentials, Addressing: rs.Addressing, TTL: time.Duration(rs.TTLSeconds) * time.Second,
		}, Allocation: jam.RoleAllocation{
			MaxEphemeral: rs.MaxEphemeral, MaxPersonal: rs.MaxPersonal, MaxPersonalPerOwner: rs.MaxPersonalPerOwner,
			IdleAfter:    time.Duration(rs.IdleAfterSeconds) * time.Second,
			NagEvery:     time.Duration(rs.NagEverySeconds) * time.Second,
			ReclaimAfter: time.Duration(rs.ReclaimAfterSeconds) * time.Second,
		}, TurnEnd: jam.TurnEndPolicy{IdleTimeout: time.Duration(rs.IdleTimeoutSeconds) * time.Second, OnIdle: rs.OnIdle, TimeZone: rs.TimeZone}})
		roles[len(roles)-1].Scope.Egress = rs.Egress
	}
	return roles, nil
}

// RemoveRole deletes a role from project.
func (c *Client) RemoveRole(project, name string) error {
	return c.do("DELETE", "/admin/roles/"+project+"/"+name, nil, nil)
}

// ListProjects lists every project record.
func (c *Client) ListProjects() ([]string, error) {
	var out []string
	err := c.do("GET", "/admin/projects", nil, &out)
	return out, err
}

// CreateProject records a new, empty project (ErrConflict if it exists).
func (c *Client) CreateProject(name string) error {
	return c.do("POST", "/admin/projects", jam.ProjectBody{Name: name}, nil)
}

// RemoveProject deletes a project; ErrConflict while a role or grant still
// references it, ErrNotFound if absent.
func (c *Client) RemoveProject(name string) error {
	return c.do("DELETE", "/admin/projects/"+url.PathEscape(name), nil, nil)
}

// Roster lists every enrolled actor with its resolved effective grants — never
// a token or hash.
func (c *Client) Roster() ([]jam.ActorSummary, error) {
	var out []jam.ActorSummary
	err := c.do("GET", "/admin/roster", nil, &out)
	return out, err
}

// AddGrant assigns g to the actor identified by actorID.
func (c *Client) AddGrant(actorID string, g jam.Grant) error {
	return c.do("POST", "/admin/actors/"+actorID+"/grants", jam.GrantBody{
		Project: g.Project, Role: g.Role, Overrides: g.Overrides,
	}, nil)
}

// RemoveGrant removes actorID's grant of role within project.
func (c *Client) RemoveGrant(actorID, project, role string) error {
	return c.do("DELETE", "/admin/actors/"+actorID+"/grants/"+project+"/"+role, nil, nil)
}

// LoginConfig fetches Jam's public device-flow client parameters from
// GET /admin/login-config. It needs no token (the endpoint is auth-exempt); a
// 404 means the Jam is not OIDC-gated.
func (c *Client) LoginConfig() (jam.OperatorLoginConfig, error) {
	var lc jam.OperatorLoginConfig
	err := c.do("GET", "/admin/login-config", nil, &lc)
	return lc, err
}

// PushKit pushes a new version of a kit config, returning the new version number.
func (c *Client) PushKit(name, config string) (jam.KitResult, error) {
	var res jam.KitResult
	err := c.do("POST", "/admin/kits", jam.KitBody{Name: name, Config: config}, &res)
	return res, err
}

// ListKits lists every kit in the registry.
func (c *Client) ListKits() ([]jam.KitSummary, error) {
	var out []jam.KitSummary
	err := c.do("GET", "/admin/kits", nil, &out)
	return out, err
}

// GetKit fetches a kit's config. version == 0 fetches the current version.
func (c *Client) GetKit(name string, version int) (jam.KitConfigResult, error) {
	var out jam.KitConfigResult
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
	return c.do("POST", "/admin/kits/"+name+"/pin", jam.PinBody{Version: version}, nil)
}

// RemoveKit deletes a kit from the registry. Fails (409 from the server) if a
// role still references it.
func (c *Client) RemoveKit(name string) error {
	return c.do("DELETE", "/admin/kits/"+name, nil, nil)
}

// AddHuman upserts a roster human (by name) within project.
func (c *Client) AddHuman(project string, h jam.Human) error {
	return c.do("POST", "/admin/projects/"+url.PathEscape(project)+"/humans", h, nil)
}

// AddChannel upserts a roster channel (by name) within project.
func (c *Client) AddChannel(project string, ch jam.Channel) error {
	return c.do("POST", "/admin/projects/"+url.PathEscape(project)+"/channels", ch, nil)
}

// GetRoster fetches project's addressable roster (humans + channels).
func (c *Client) GetRoster(project string) (jam.Roster, error) {
	var rr jam.Roster
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
func (c *Client) SetEscalationPolicy(project, category string, tiers []jam.EscalationTier) error {
	return c.do("PUT", "/admin/projects/"+url.PathEscape(project)+"/escalation", jam.EscalationBody{Category: category, Tiers: tiers}, nil)
}

// GetEscalationPolicy fetches project's escalation policy: the default chain
// plus any category overrides.
func (c *Client) GetEscalationPolicy(project string) (jam.EscalationView, error) {
	var v jam.EscalationView
	err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/escalation", nil, &v)
	return v, err
}

// SetChatService sets (or clears, with "") the chat service backing project's
// human DMs.
func (c *Client) SetChatService(project, service string) error {
	return c.do("PUT", "/admin/projects/"+url.PathEscape(project)+"/chat-service", jam.ChatServiceBody{Service: service}, nil)
}

// GetChatService returns project's configured chat service ("" if none).
func (c *Client) GetChatService(project string) (string, error) {
	var v jam.ChatServiceView
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
func (c *Client) RaiseCove(p CoveRaiseParams) (jam.CoveRaiseResult, error) {
	var res jam.CoveRaiseResult
	err := c.do("POST", "/admin/coves", jam.CoveRaiseBody{ID: p.ID, Project: p.Project, Role: p.Role, Unit: p.Unit, Prompt: p.Prompt}, &res)
	return res, err
}

// ListCoves lists the managed-cove runtime registry (never a token or hash).
func (c *Client) ListCoves() ([]jam.CoveSummary, error) {
	var out []jam.CoveSummary
	err := c.do("GET", "/admin/coves", nil, &out)
	return out, err
}

// ListRoleCoves lists one project/role's managed coves (GET /admin/coves?role=),
// so Jam resolves only that role's image status. An older Jam ignores the
// filter and returns every cove; callers match by id either way.
func (c *Client) ListRoleCoves(project, role string) ([]jam.CoveSummary, error) {
	var out []jam.CoveSummary
	q := url.Values{"project": {project}, "role": {role}}
	err := c.do("GET", "/admin/coves?"+q.Encode(), nil, &out)
	return out, err
}

// ReportCoveStatus reports a cove's activity (running|holding|waiting|blocked|done).
func (c *Client) ReportCoveStatus(id, activity string) error {
	return c.do("POST", "/admin/coves/"+url.PathEscape(id)+"/status", jam.CoveStatusBody{Activity: activity}, nil)
}

// TeardownCove tears a managed cove down and deregisters it.
func (c *Client) TeardownCove(id string) error {
	return c.do("DELETE", "/admin/coves/"+url.PathEscape(id), nil, nil)
}

// RequestPersonalSession asks Jam for a personal session of role in project
// ("" = the default project), owned by the roster human linked to the caller's
// login. The prompt travels in the request body, never on argv.
func (c *Client) RequestPersonalSession(project, role, prompt string) (jam.PersonalSessionResult, error) {
	var res jam.PersonalSessionResult
	err := c.do("POST", "/admin/sessions/personal", jam.PersonalSessionBody{Project: project, Role: role, Prompt: prompt}, &res)
	return res, err
}

// ListPersonalSessions lists the caller's own personal sessions in project
// ("" = the default project).
func (c *Client) ListPersonalSessions(project string) ([]jam.PersonalSessionSummary, error) {
	var out []jam.PersonalSessionSummary
	path := "/admin/sessions/personal"
	if project != "" {
		path += "?project=" + url.QueryEscape(project)
	}
	err := c.do("GET", path, nil, &out)
	return out, err
}

// ReleasePersonalSession releases (tears down) one of the caller's personal
// sessions; only its owner may.
func (c *Client) ReleasePersonalSession(id string) error {
	return c.do("DELETE", "/admin/sessions/personal/"+url.PathEscape(id), nil, nil)
}

func standingPath(project, role string) string {
	return "/admin/roles/" + url.PathEscape(project) + "/" + url.PathEscape(role) + "/standing"
}

// AddStanding declares a named standing session on project's role; Jam then
// keeps one cove running for it. The prompt travels in the request body, never
// on argv.
func (c *Client) AddStanding(project, role string, s jam.StandingSession) error {
	return c.do("POST", standingPath(project, role), s, nil)
}

// ListStanding lists the standing sessions declared on project's role, prompts
// included, each with its pending upgrade state ("" none).
func (c *Client) ListStanding(project, role string) ([]jam.StandingStatus, error) {
	var out []jam.StandingStatus
	err := c.do("GET", standingPath(project, role), nil, &out)
	return out, err
}

// RemoveStanding dismisses a standing session; Jam tears its cove down and
// deletes its persisted state.
func (c *Client) RemoveStanding(project, role, name string) error {
	return c.do("DELETE", standingPath(project, role)+"/"+url.PathEscape(name), nil, nil)
}

// ResetStanding tears a declared standing session's cove down and deletes its
// persisted state, keeping the declaration: Jam raises a fresh session on its
// next standing pass. Pending in the result means Jam is still finishing it
// (it retries every pass). An undeclared name is ErrNotFound.
func (c *Client) ResetStanding(project, role, name string) (jam.StandingResetResult, error) {
	var out jam.StandingResetResult
	err := c.do("POST", standingPath(project, role)+"/"+url.PathEscape(name)+"/reset", nil, &out)
	return out, err
}

// UpgradeStanding queues a re-raise of a declared standing session on the
// image a raise would run now, keeping its conversation and workspace: Pending
// with its State (watch ListStanding's Upgrade) when queued; not Pending with
// Reason "already current" when it already runs that image. Jam prepares the
// image and waits for the session to be idle; force skips the wait and the
// already-current check. A pending reset is ErrConflict; an undeclared name
// ErrNotFound.
func (c *Client) UpgradeStanding(project, role, name string, force bool) (jam.StandingUpgradeResult, error) {
	var out jam.StandingUpgradeResult
	path := standingPath(project, role) + "/" + url.PathEscape(name) + "/upgrade"
	if force {
		path += "?force=true"
	}
	err := c.do("POST", path, nil, &out)
	return out, err
}

func egressPath(project, role string) string {
	return "/admin/roles/" + url.PathEscape(project) + "/" + url.PathEscape(role) + "/egress"
}

// SetEgress sets project's role's egress policy to domains (nil or empty = a
// set-but-empty policy: nothing beyond the sealed base + the kit's infra
// domains). Jam normalizes the list; a bad domain is a 400 naming it. It takes
// effect at the role's next raise.
func (c *Client) SetEgress(project, role string, domains []string) error {
	if domains == nil {
		domains = []string{}
	}
	return c.do("PUT", egressPath(project, role), jam.EgressPolicy{Domains: domains}, nil)
}

// ShowEgress returns project's role's egress policy; Managed false means the
// kit's default list.
func (c *Client) ShowEgress(project, role string) (jam.EgressView, error) {
	var out jam.EgressView
	err := c.do("GET", egressPath(project, role), nil, &out)
	return out, err
}

// ClearEgress reverts project's role to the kit's default egress list.
func (c *Client) ClearEgress(project, role string) error {
	return c.do("DELETE", egressPath(project, role), nil, nil)
}

// ContextScope names one authored session-context layer: a role (Project+Role),
// a project (Project only) or the Jam (Jam).
type ContextScope struct {
	Project, Role string
	Jam           bool
}

func contextPath(s ContextScope) string {
	switch {
	case s.Jam:
		return "/admin/jam/context"
	case s.Role != "":
		return "/admin/roles/" + url.PathEscape(s.Project) + "/" + url.PathEscape(s.Role) + "/context"
	default:
		return "/admin/projects/" + url.PathEscape(s.Project) + "/context"
	}
}

// GetContext returns the authored context layer for s.
func (c *Client) GetContext(s ContextScope) (jam.ContextBody, error) {
	var out jam.ContextBody
	err := c.do("GET", contextPath(s), nil, &out)
	return out, err
}

// SetContext replaces the authored context layer for s; Jam validates budgets.
func (c *Client) SetContext(s ContextScope, b jam.ContextBody) error {
	return c.do("PUT", contextPath(s), b, nil)
}

// ClearContext removes the authored context layer for s.
func (c *Client) ClearContext(s ContextScope) error {
	return c.do("DELETE", contextPath(s), nil, nil)
}

// ExportConfig fetches a full config snapshot (GET /admin/config).
func (c *Client) ExportConfig() (jam.ConfigSnapshot, error) {
	var s jam.ConfigSnapshot
	err := c.do("GET", "/admin/config", nil, &s)
	return s, err
}

// ImportConfig restores a snapshot (POST /admin/config). A non-empty target
// surfaces as ErrConflict.
func (c *Client) ImportConfig(s jam.ConfigSnapshot) error {
	return c.do("POST", "/admin/config", s, nil)
}
