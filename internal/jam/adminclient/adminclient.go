// Package adminclient is a typed HTTP client for Jam's loopback admin API.
// Kept dependency-light so a future jamctl can reuse it; today it imports
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
	"github.com/aethons-tools/cove/internal/jam/collabwire"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

// ErrNotFound wraps a 404 from the admin API, so callers can distinguish "this
// endpoint/resource isn't there" (e.g. a non-OIDC Jam has no login-config)
// from a transport failure or another HTTP error.
var ErrNotFound = errors.New("not found")

// ErrConflict wraps a 409 from the admin API — e.g. importing config into a
// store that is not empty.
var ErrConflict = errors.New("conflict")

// StatusError is a non-2xx answer from the admin API, for callers that map a
// status code to their own message (errors.AsType). Body is the server's
// trimmed response text.
type StatusError struct {
	Method, Path, Status string
	Code                 int
	Body                 string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("admin API %s %s: %s: %s", e.Method, e.Path, e.Status, e.Body)
}

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
	return c.doWith(c.httpc, method, path, body, out)
}

// gitTimeout bounds a call that makes Jam fetch from git (import, track,
// sync); the 10 s default of do is too short for a clone.
const gitTimeout = 90 * time.Second

// doGit is do with the git-fetch timeout.
func (c *Client) doGit(method, path string, body any, out any) error {
	hc := *c.httpc // keeps the Transport
	hc.Timeout = gitTimeout
	return c.doWith(&hc, method, path, body, out)
}

// configGitTimeout bounds a config call that may fetch a config repo and its
// kit sources (jam.ConfigFetchTimeout plus the plan).
const configGitTimeout = 5 * time.Minute

// doConfig is do with the config-fetch timeout.
func (c *Client) doConfig(method, path string, body any, out any) error {
	hc := *c.httpc // keeps the Transport
	hc.Timeout = configGitTimeout
	return c.doWith(&hc, method, path, body, out)
}

// configPath is a config route with its plan query ("" = none).
func configPath(path, planDigest string) string {
	if planDigest != "" {
		path += "?" + url.Values{"plan": {planDigest}}.Encode()
	}
	return path
}

func (c *Client) doWith(httpc *http.Client, method, path string, body any, out any) error {
	var buf []byte
	contentType := ""
	if body != nil {
		var err error
		if buf, err = json.Marshal(body); err != nil {
			return err
		}
		contentType = "application/json"
	}
	return c.send(httpc, method, path, contentType, buf, out)
}

// doRaw sends body as-is with contentType ("" = no body), under the
// config-fetch timeout: a config tree upload is planned (and maybe applied)
// before the answer, and its .source.yml kits are fetched too.
func (c *Client) doRaw(method, path, contentType string, body []byte, out any) error {
	hc := *c.httpc // keeps the Transport
	hc.Timeout = configGitTimeout
	return c.send(&hc, method, path, contentType, body, out)
}

// send issues one request; contentType "" sends no body.
func (c *Client) send(httpc *http.Client, method, path, contentType string, body []byte, out any) error {
	_, err := c.sendHeaders(httpc, method, path, contentType, body, nil, out)
	return err
}

// sendHeaders is send with extra request headers (hdr may be nil), returning
// the answer's headers. out may be a *[]byte to take a 2xx body raw.
func (c *Client) sendHeaders(httpc *http.Client, method, path, contentType string, body []byte, hdr http.Header, out any) (http.Header, error) {
	var r io.Reader
	if contentType != "" {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("admin API unreachable at %s (is `jam serve` running?): %w", c.base, err)
	}
	return resp.Header, finish(resp, method, path, out)
}

// finish maps a non-2xx answer to a *StatusError (wrapping ErrNotFound for
// 404 and ErrConflict for 409) and decodes a 2xx JSON answer into out (nil:
// discarded; a *[]byte takes the body raw). It closes the body.
func finish(resp *http.Response, method, path string, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		var err error = &StatusError{Method: method, Path: path, Status: resp.Status, Code: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
		if resp.StatusCode == http.StatusNotFound {
			err = fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		if resp.StatusCode == http.StatusConflict {
			err = fmt.Errorf("%w: %w", ErrConflict, err)
		}
		return err
	}
	if raw, ok := out.(*[]byte); ok {
		b, err := io.ReadAll(resp.Body)
		*raw = b
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

func (c *Client) ListDestinations() ([]jam.Destination, error) {
	var out []jam.Destination
	err := c.do("GET", "/admin/destinations", nil, &out)
	return out, err
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
		}, TurnEnd: jam.TurnEndPolicy{IdleTimeout: time.Duration(rs.IdleTimeoutSeconds) * time.Second, OnIdle: rs.OnIdle, TimeZone: rs.TimeZone},
			Collaborate: jam.CollaboratePolicy{Preview: rs.CollaboratePreview, SSH: rs.CollaborateSSH}})
		roles[len(roles)-1].Scope.Egress = rs.Egress
	}
	return roles, nil
}

// RoleImages lists the current image of project/role on each connected manager.
func (c *Client) RoleImages(project, role string) ([]jam.RoleImage, error) {
	var out struct {
		Images []jam.RoleImage `json:"images"`
	}
	err := c.do("GET", "/admin/roles/"+url.PathEscape(project)+"/"+url.PathEscape(role)+"/images", nil, &out)
	return out.Images, err
}

// ListProjects lists every project record.
func (c *Client) ListProjects() ([]string, error) {
	var out []string
	err := c.do("GET", "/admin/projects", nil, &out)
	return out, err
}

// Actors lists every enrolled actor with its resolved effective grants — never
// a token or hash.
func (c *Client) Actors() ([]jam.ActorSummary, error) {
	var out []jam.ActorSummary
	err := c.do("GET", "/admin/actors", nil, &out)
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

// ListRooms lists project's rooms.
func (c *Client) ListRooms(project string) ([]jam.RoomView, error) {
	var out []jam.RoomView
	err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/rooms", nil, &out)
	return out, err
}

// GetEscalationPolicy fetches project's escalation policy: the default chain
// plus any category overrides.
func (c *Client) GetEscalationPolicy(project string) (jam.EscalationView, error) {
	var v jam.EscalationView
	err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/escalation", nil, &v)
	return v, err
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
	// Managers, in preference order, are where the raise may land (empty = any);
	// PlacementTimeout is a Go duration for how long to look ("" = do not wait).
	Managers         []string
	PlacementTimeout string
}

// placementMargin is the time a request keeps beyond its placement timeout for
// the rest of the raise (launch, enrollment, the answer).
const placementMargin = 30 * time.Second

// withPlacementWait returns a client whose timeout is the larger of floor and
// the placement timeout plus placementMargin, so the client does not give up on
// a raise Jam is still placing. An unparsable timeout leaves floor (Jam answers
// 400).
func (c *Client) withPlacementWait(floor time.Duration, placementTimeout string) *http.Client {
	hc := *c.httpc // keeps the Transport
	hc.Timeout = floor
	if d, err := time.ParseDuration(placementTimeout); err == nil && d > 0 && d+placementMargin > floor {
		hc.Timeout = d + placementMargin
	}
	return &hc
}

// RaiseCove raises a managed cove for a role and returns its runtime result
// (including the identity token, once).
func (c *Client) RaiseCove(p CoveRaiseParams) (jam.CoveRaiseResult, error) {
	var res jam.CoveRaiseResult
	body := jam.CoveRaiseBody{ID: p.ID, Project: p.Project, Role: p.Role, Unit: p.Unit, Prompt: p.Prompt, Managers: p.Managers, PlacementTimeout: p.PlacementTimeout}
	httpc := c.withPlacementWait(c.httpc.Timeout, p.PlacementTimeout)
	err := c.doWith(httpc, "POST", "/admin/coves", body, &res)
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

// RequestPersonalSession asks Jam for a personal session of b.Role in b.Project
// ("" = the default project), owned by the roster human linked to the caller's
// login. The prompt travels in the request body, never on argv.
func (c *Client) RequestPersonalSession(b jam.PersonalSessionBody) (jam.PersonalSessionResult, error) {
	var res jam.PersonalSessionResult
	err := c.doWith(c.withPlacementWait(c.httpc.Timeout, b.PlacementTimeout), "POST", "/admin/sessions/personal", b, &res)
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

// ListStanding lists the standing sessions declared on project's role, prompts
// included, each with its pending upgrade state ("" none).
func (c *Client) ListStanding(project, role string) ([]jam.StandingStatus, error) {
	var out []jam.StandingStatus
	err := c.do("GET", standingPath(project, role), nil, &out)
	return out, err
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

// ShowEgress returns project's role's egress policy; Managed false means the
// kit's default list.
func (c *Client) ShowEgress(project, role string) (jam.EgressView, error) {
	var out jam.EgressView
	err := c.do("GET", egressPath(project, role), nil, &out)
	return out, err
}

// setupTimeout bounds a setup inspect: a first read of an image's seed runs
// docker create/cp on the launcher host.
const setupTimeout = 60 * time.Second

// setupPath is a setup route with its sections and diff query.
func setupPath(path string, sections []string, diff bool, manager string) string {
	q := url.Values{}
	if manager != "" {
		q.Set("manager", manager)
	}
	if len(sections) > 0 {
		q.Set("sections", strings.Join(sections, ","))
	}
	if diff {
		q.Set("diff", "role")
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path
}

func (c *Client) doSetup(path string, out *jam.StudioSetup) error {
	hc := *c.httpc // keeps the Transport
	hc.Timeout = setupTimeout
	return c.doWith(&hc, "GET", path, nil, out)
}

// RoleSetup is what a studio of project/role raised now would get (sections
// nil = all; manager "" = the default) read from manager's image.
func (c *Client) RoleSetup(project, role string, sections []string, manager string) (jam.StudioSetup, error) {
	var out jam.StudioSetup
	err := c.doSetup(setupPath("/admin/roles/"+url.PathEscape(project)+"/"+url.PathEscape(role)+"/setup", sections, false, manager), &out)
	return out, err
}

// StudioSetup is what studio (an id or a live manual label) got; diff adds
// each section's line diff against its role's render.
func (c *Client) StudioSetup(studio string, sections []string, diff bool) (jam.StudioSetup, error) {
	var out jam.StudioSetup
	err := c.doSetup(setupPath("/admin/instances/"+url.PathEscape(studio)+"/setup", sections, diff, ""), &out)
	return out, err
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

// ListUsers lists the live users.
func (c *Client) ListUsers() ([]jam.UserView, error) {
	var out []jam.UserView
	err := c.do("GET", "/admin/users", nil, &out)
	return out, err
}

// GetUser fetches a user by name or id.
func (c *Client) GetUser(user string) (jam.UserView, error) {
	var out jam.UserView
	err := c.do("GET", "/admin/users/"+url.PathEscape(user), nil, &out)
	return out, err
}

// SetUserOIDC replaces a user's OIDC bindings.
func (c *Client) SetUserOIDC(user string, ids []jam.OIDCIdentity) error {
	return c.do("PUT", "/admin/users/"+url.PathEscape(user)+"/oidc", jam.OIDCBody{OIDC: ids}, nil)
}

// ListMembers lists project's members.
func (c *Client) ListMembers(project string) ([]jam.MemberView, error) {
	var out []jam.MemberView
	err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/members", nil, &out)
	return out, err
}

// ListConnections lists the live connections.
func (c *Client) ListConnections() ([]jam.Connection, error) {
	var out []jam.Connection
	err := c.do("GET", "/admin/connections", nil, &out)
	return out, err
}

// ListAccounts lists accounts, on one connection (by name or id) or all ("").
func (c *Client) ListAccounts(connection string) ([]jam.AccountView, error) {
	path := "/admin/accounts"
	if connection != "" {
		path += "?connection=" + url.QueryEscape(connection)
	}
	var out []jam.AccountView
	err := c.do("GET", path, nil, &out)
	return out, err
}

// AddAccount upserts an account, linking it when b.User is set.
func (c *Client) AddAccount(b jam.AccountBody) (jam.AccountView, error) {
	var out jam.AccountView
	err := c.do("POST", "/admin/accounts", b, &out)
	return out, err
}

// LinkAccount links an account to a user.
func (c *Client) LinkAccount(account, user string) error {
	return c.do("PUT", "/admin/accounts/"+url.PathEscape(account)+"/user", jam.LinkBody{User: user}, nil)
}

// UnlinkAccount unlinks an account from its user.
func (c *Client) UnlinkAccount(account string) error {
	return c.do("DELETE", "/admin/accounts/"+url.PathEscape(account)+"/user", nil, nil)
}

// ListConditions lists operator-attention conditions (state: open|resolved|all).
func (c *Client) ListConditions(state string) ([]condition.Condition, error) {
	var out []condition.Condition
	err := c.do("GET", "/admin/attention?state="+url.QueryEscape(state), nil, &out)
	return out, err
}

// IssueSSHCert asks Jam to certify pub (an authorized_keys line) for SSH into
// studio, on the plane at prefix ("/admin" for an operator, "/me" for a
// participant), returning the certificate line and when it expires.
func (c *Client) IssueSSHCert(prefix, studio string, pub []byte) (cert []byte, expires time.Time, err error) {
	var out struct {
		Certificate string `json:"certificate"`
		ExpiresAt   string `json:"expires_at"`
	}
	if err := c.do("POST", prefix+"/studios/"+url.PathEscape(studio)+"/ssh/cert", struct {
		PublicKey string `json:"public_key"`
	}{string(pub)}, &out); err != nil {
		return nil, time.Time{}, err
	}
	expires, err = time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("admin API returned an unreadable certificate expiry: %w", err)
	}
	return []byte(out.Certificate), expires, nil
}

// ShareParams is a share grant request: user is a name, user:<name> or a
// usr_ id; For is a Go duration or whole days ("7d"), "" for Jam's default.
type ShareParams struct {
	User   string   `json:"user"`
	Rights []string `json:"rights"`
	For    string   `json:"for,omitempty"`
}

// ShareStudio grants a user rights on studio, on the plane at prefix ("/admin"
// or "/me").
func (c *Client) ShareStudio(prefix, studio string, p ShareParams) (collabwire.GrantView, error) {
	var out collabwire.GrantView
	err := c.do("POST", prefix+"/studios/"+url.PathEscape(studio)+"/shares", p, &out)
	return out, err
}

// UnshareStudio removes user's share grant on studio.
func (c *Client) UnshareStudio(prefix, studio, user string) error {
	return c.do("DELETE", prefix+"/studios/"+url.PathEscape(studio)+"/shares/"+url.PathEscape(user), nil, nil)
}

// StudioAccess reports who can reach studio and why, and who is connected.
func (c *Client) StudioAccess(prefix, studio string) (collabwire.AccessView, error) {
	var out collabwire.AccessView
	err := c.do("GET", prefix+"/studios/"+url.PathEscape(studio)+"/access", nil, &out)
	return out, err
}

// ImportKit imports a kit from a git source (<repo>[//<dir>]@<ref>) as a new
// version of the kit called name ("" = the kit's own name). It does not track.
func (c *Client) ImportKit(name, source string) (jam.KitSourceResult, error) {
	var res jam.KitSourceResult
	err := c.doGit("POST", "/admin/kits/import", jam.KitImportBody{Name: name, Source: source}, &res)
	return res, err
}

// TrackKit imports the git source into kit name and tracks it for SyncKit.
func (c *Client) TrackKit(name, source string) (jam.KitSourceResult, error) {
	var res jam.KitSourceResult
	err := c.doGit("PUT", "/admin/kits/"+url.PathEscape(name)+"/source", jam.KitSourceBody{Source: source}, &res)
	return res, err
}

// UntrackKit stops tracking kit name's source; its versions are kept.
func (c *Client) UntrackKit(name string) error {
	return c.do("DELETE", "/admin/kits/"+url.PathEscape(name)+"/source", nil, nil)
}

// SyncKit re-imports kit name from its tracked source.
func (c *Client) SyncKit(name string) (jam.KitSourceResult, error) {
	var res jam.KitSourceResult
	err := c.doGit("POST", "/admin/kits/"+url.PathEscape(name)+"/sync", nil, &res)
	return res, err
}

// KitHistory lists a kit's versions with their provenance, ascending.
func (c *Client) KitHistory(name string) ([]jam.KitVersionInfo, error) {
	var out []jam.KitVersionInfo
	err := c.do("GET", "/admin/kits/"+url.PathEscape(name)+"/history", nil, &out)
	return out, err
}

// ConfigDiff plans a packed config tree (configtar.Pack) against Jam without
// writing.
func (c *Client) ConfigDiff(tree []byte) (jam.ConfigApplyResult, error) {
	var res jam.ConfigApplyResult
	err := c.doRaw("POST", "/admin/config/diff", "application/gzip", tree, &res)
	return res, err
}

// ConfigApply applies a packed config tree. planDigest (may be "") is the
// reviewed plan's digest — a different plan is a 409 (ErrConflict), as is a
// plan with refusals.
func (c *Client) ConfigApply(tree []byte, planDigest string) (jam.ConfigApplyResult, error) {
	var res jam.ConfigApplyResult
	err := c.doRaw("POST", configPath("/admin/config/apply", planDigest), "application/gzip", tree, &res)
	return res, err
}

// ConfigDiffSource plans a git config source ("" = the followed repo) without
// writing.
func (c *Client) ConfigDiffSource(source string) (jam.ConfigApplyResult, error) {
	var res jam.ConfigApplyResult
	err := c.doConfig("POST", "/admin/config/diff", jam.ConfigSourceBody{Source: source}, &res)
	return res, err
}

// ConfigApplySource applies a git config source ("" = the followed repo); a
// different plan than planDigest, or one with refusals, is ErrConflict.
func (c *Client) ConfigApplySource(source, planDigest string) (jam.ConfigApplyResult, error) {
	var res jam.ConfigApplyResult
	err := c.doConfig("POST", configPath("/admin/config/apply", planDigest), jam.ConfigSourceBody{Source: source}, &res)
	return res, err
}

// ConfigFollow is the config follower's status.
func (c *Client) ConfigFollow() (jam.ConfigFollowStatus, error) {
	var out jam.ConfigFollowStatus
	err := c.do("GET", "/admin/config/follow", nil, &out)
	return out, err
}

// ConfigSync applies the followed config repo (ErrConflict when none, or
// for a stale planDigest or refusals).
func (c *Client) ConfigSync(planDigest string) (jam.ConfigApplyResult, error) {
	var res jam.ConfigApplyResult
	err := c.doConfig("POST", configPath("/admin/config/sync", planDigest), jam.ConfigSourceBody{}, &res)
	return res, err
}

// ConfigExportTree renders Jam's managed objects (every live one with all)
// as a config tree: its files, the paths to remove, and each kit directory's
// files at their modes (Kits).
func (c *Client) ConfigExportTree(all bool) (jam.ConfigTreeExport, error) {
	path := "/admin/config/export-tree"
	if all {
		path += "?all=1"
	}
	var exp jam.ConfigTreeExport
	err := c.do("GET", path, nil, &exp)
	return exp, err
}

// KitDiff fetches kit name's version diff from → to (0 = the defaults: the
// version before to, and current).
func (c *Client) KitDiff(name string, from, to int) (jam.KitDiffResult, error) {
	q := url.Values{}
	if from > 0 {
		q.Set("from", strconv.Itoa(from))
	}
	if to > 0 {
		q.Set("to", strconv.Itoa(to))
	}
	path := "/admin/kits/" + url.PathEscape(name) + "/diff"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var d jam.KitDiffResult
	err := c.do("GET", path, nil, &d)
	return d, err
}

// Managers lists the registered Studio Managers and their status.
func (c *Client) Managers() ([]jam.ManagerStatus, error) {
	var out []jam.ManagerStatus
	err := c.do("GET", "/admin/managers", nil, &out)
	return out, err
}

// AddManager registers a Studio Manager; the returned token is shown once
// (ErrConflict if the name exists).
func (c *Client) AddManager(name string) (jam.ManagerToken, error) {
	var out jam.ManagerToken
	err := c.do("POST", "/admin/managers", map[string]string{"name": name}, &out)
	return out, err
}

// RotateManager replaces a manager's token (ErrNotFound if unknown).
func (c *Client) RotateManager(name string) (jam.ManagerToken, error) {
	var out jam.ManagerToken
	err := c.do("POST", "/admin/managers/"+url.PathEscape(name)+"/rotate", nil, &out)
	return out, err
}

// RevokeManager removes a manager; without force it is refused (ErrConflict)
// while the manager still hosts studios.
func (c *Client) RevokeManager(name string, force bool) error {
	p := "/admin/managers/" + url.PathEscape(name)
	if force {
		p += "?force=true"
	}
	return c.do("DELETE", p, nil, nil)
}

// VSCodeExtension is the VS Code extension the Jam serves, on the plane at
// prefix ("/admin" or "/me"): whether it has one and its version.
func (c *Client) VSCodeExtension(prefix string) (available bool, version string, err error) {
	var out struct {
		Available bool   `json:"available"`
		Version   string `json:"version"`
	}
	err = c.do("GET", prefix+"/vscode", nil, &out)
	return out.Available, out.Version, err
}

// vsixTimeout bounds downloading the .vsix (a few MB), longer than do's 10 s.
const vsixTimeout = 2 * time.Minute

// DownloadVSCodeExtension downloads the .vsix the Jam serves on the plane at
// prefix.
func (c *Client) DownloadVSCodeExtension(prefix string) ([]byte, error) {
	hc := *c.httpc // keeps the Transport
	hc.Timeout = vsixTimeout
	var out []byte
	err := c.doWith(&hc, "GET", prefix+"/vscode/jam.vsix", nil, &out)
	return out, err
}
