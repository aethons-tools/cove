// Package githubissues is a second scheduler.Tracker backed by a GitHub repo's
// Issues, at parity with internal/dispatch/linear. Lifecycle states are status
// labels (status:ready, status:in-progress, …) and Done is an issue being closed;
// blocker gating is a body convention (Depends on #N / Blocked by #N, same-repo).
//
// The read half (New, ListReady, ListInProgress, Comments) landed in COV-109; the
// write half (Transition, PostComment) in COV-110 completes the type so it fully
// satisfies scheduler.Tracker. Live calls are exercised by the integration-tagged
// test.
package githubissues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
)

const api = "https://api.github.com"

// Client talks to one GitHub repo's Issues. When complete it will satisfy
// scheduler.Tracker; this part implements the read methods.
type Client struct {
	http   *http.Client
	token  string
	repo   string                    // "owner/name"
	prefix string                    // class label prefix, e.g. "class:"
	labels map[scheduler.Role]string // role → status label
}

// New resolves the target repo (tracker.github.repo, else
// source-control.github.project) and builds the role→label map and class prefix
// from config. It errors on a missing tracker.github block, an unresolvable repo,
// or an empty token. Unlike Linear, no network call is made at construction — the
// lifecycle states are labels, not ids to be looked up.
func New(cfg kit.Config, token string, httpc *http.Client) (*Client, error) {
	if cfg.Tracker == nil || cfg.Tracker.GitHub == nil {
		return nil, fmt.Errorf("githubissues: kit declares no tracker.github")
	}
	if token == "" {
		return nil, fmt.Errorf("githubissues: empty tracker token")
	}
	gt := cfg.Tracker.GitHub
	repo := strings.TrimSpace(gt.Repo)
	if repo == "" {
		if cfg.SourceControl == nil || cfg.SourceControl.GitHub == nil {
			return nil, fmt.Errorf("githubissues: no repo (set tracker.github.repo or source-control.github)")
		}
		repo = cfg.SourceControl.GitHub.Project
	}
	if repo == "" {
		return nil, fmt.Errorf("githubissues: could not resolve a repo")
	}
	prefix := gt.ClassLabelPrefix
	if prefix == "" {
		prefix = "class:"
	}
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Client{
		http: httpc, token: token, repo: repo, prefix: prefix,
		labels: map[scheduler.Role]string{
			scheduler.RoleReady:      gt.States.Ready,
			scheduler.RoleInProgress: gt.States.InProgress,
			scheduler.RoleInReview:   gt.States.InReview,
			scheduler.RoleNeedsInput: gt.States.NeedsInput,
			scheduler.RoleBlocked:    gt.States.Blocked,
		},
	}, nil
}

// do issues a REST call and returns the status, body, and any transport error.
// It mirrors internal/dispatch/github's helper: Bearer auth, GitHub media type.
func (c *Client) do(ctx context.Context, method, u string, payload []byte) (int, []byte, error) {
	var body *bytes.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	return resp.StatusCode, raw.Bytes(), nil
}

// getJSON performs a GET and unmarshals a 200 body into out, erroring otherwise.
func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	code, raw, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("githubissues: GET %s: http %d: %s", u, code, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// ghIssue is the shared projection of a search hit / issue payload.
type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (c *Client) toIssue(n ghIssue) scheduler.Issue {
	class := ""
	for _, l := range n.Labels {
		if strings.HasPrefix(l.Name, c.prefix) {
			class = strings.TrimPrefix(l.Name, c.prefix)
			break
		}
	}
	num := strconv.Itoa(n.Number)
	return scheduler.Issue{
		ID: num, Identifier: "#" + num, Title: n.Title,
		Description: n.Body, Class: class,
	}
}

// search runs a GitHub issue search for the given status label in this repo,
// scoped to open issues.
func (c *Client) search(ctx context.Context, label string) ([]ghIssue, error) {
	q := fmt.Sprintf("repo:%s is:issue is:open label:%q", c.repo, label)
	u := api + "/search/issues?" + url.Values{"q": {q}}.Encode()
	var out struct {
		Items []ghIssue `json:"items"`
	}
	if err := c.getJSON(ctx, u, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// blockerRef matches a body blocker marker: "Depends on #N" / "Blocked by #N"
// (same repo), or a cross-repo "owner/repo#N" whose optional group 1 is set.
var blockerRef = regexp.MustCompile(`(?i)(?:depends on|blocked by)\s+([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)?#(\d+)`)

// parseBlockers extracts the same-repo blocker issue numbers referenced in body.
// A cross-repo marker (owner/repo#N) is out of scope for v1 and is dropped with a
// logged note (documented limitation); a self-reference is ignored.
func (c *Client) parseBlockers(ctx context.Context, body string, self int) []int {
	var nums []int
	for _, m := range blockerRef.FindAllStringSubmatch(body, -1) {
		if m[1] != "" {
			logging.From(ctx).Warn("ignoring cross-repo blocker reference (same-repo only in v1)",
				slog.String("repo", c.repo), slog.Int("issue", self), slog.String("ref", m[1]+"#"+m[2]))
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil || n == self {
			continue
		}
		nums = append(nums, n)
	}
	return nums
}

// closedBlockers resolves the given issue numbers in one pass and returns the set
// that is closed — the authoritative satisfied-dependency check (mirrors Linear's
// doneBlockers). A number that can't be resolved (404 / missing) is treated as NOT
// closed, so a dependent stays held rather than releasing on a phantom blocker.
func (c *Client) closedBlockers(ctx context.Context, nums map[int]struct{}) (map[int]bool, error) {
	closed := make(map[int]bool, len(nums))
	for n := range nums {
		code, raw, err := c.do(ctx, http.MethodGet, api+"/repos/"+c.repo+"/issues/"+strconv.Itoa(n), nil)
		if err != nil {
			return nil, err
		}
		if code == http.StatusNotFound {
			continue // unresolved blocker → treated as still blocking
		}
		if code != http.StatusOK {
			return nil, fmt.Errorf("githubissues: get issue #%d: http %d: %s", n, code, strings.TrimSpace(string(raw)))
		}
		var out struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		if out.State == "closed" {
			closed[n] = true
		}
	}
	return closed, nil
}

// ListReady returns open issues carrying the READY status label whose same-repo
// blockers (Depends on #N / Blocked by #N in the body) are all closed, or which
// have none. Blocker doneness is resolved by a separate authoritative lookup over
// the deduped union of referenced numbers — the same discipline as Linear's
// doneBlockers (COV-56): the search projection is never trusted for it.
func (c *Client) ListReady(ctx context.Context) ([]scheduler.Issue, error) {
	items, err := c.search(ctx, c.labels[scheduler.RoleReady])
	if err != nil {
		return nil, err
	}
	blockersOf := make(map[int][]int, len(items))
	union := map[int]struct{}{}
	for _, n := range items {
		for _, b := range c.parseBlockers(ctx, n.Body, n.Number) {
			blockersOf[n.Number] = append(blockersOf[n.Number], b)
			union[b] = struct{}{}
		}
	}
	closed, err := c.closedBlockers(ctx, union)
	if err != nil {
		return nil, err
	}
	var issues []scheduler.Issue
	for _, n := range items {
		dispatchable := true
		for _, b := range blockersOf[n.Number] {
			if !closed[b] {
				dispatchable = false
				break
			}
		}
		if dispatchable {
			issues = append(issues, c.toIssue(n))
		}
	}
	return issues, nil
}

// ListInProgress returns open issues carrying the IN PROGRESS status label paired
// with the time each entered that state, for the stale-claim reaper. The time is
// the most recent `labeled` timeline event for that label. An issue with no such
// event (or an unparseable timestamp) is skipped rather than reaped, since a
// missing time can't establish that a claim is stale.
func (c *Client) ListInProgress(ctx context.Context) ([]scheduler.InProgressIssue, error) {
	label := c.labels[scheduler.RoleInProgress]
	items, err := c.search(ctx, label)
	if err != nil {
		return nil, err
	}
	var issues []scheduler.InProgressIssue
	for _, n := range items {
		started, ok, err := c.enteredAt(ctx, n.Number, label)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		issues = append(issues, scheduler.InProgressIssue{Issue: c.toIssue(n), StartedAt: started})
	}
	return issues, nil
}

// enteredAt reads an issue's timeline and returns the created-at of the most
// recent `labeled` event for label. ok is false when there is no such event.
func (c *Client) enteredAt(ctx context.Context, number int, label string) (time.Time, bool, error) {
	u := api + "/repos/" + c.repo + "/issues/" + strconv.Itoa(number) + "/timeline"
	var events []struct {
		Event     string `json:"event"`
		CreatedAt string `json:"created_at"`
		Label     struct {
			Name string `json:"name"`
		} `json:"label"`
	}
	if err := c.getJSON(ctx, u, &events); err != nil {
		return time.Time{}, false, err
	}
	var latest time.Time
	found := false
	for _, e := range events {
		if e.Event != "labeled" || e.Label.Name != label {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.CreatedAt)
		if err != nil {
			continue
		}
		if !found || t.After(latest) {
			latest, found = t, true
		}
	}
	return latest, found, nil
}

// Transition realizes a role change on an issue. For scheduler.RoleDone the issue
// is closed (a closed issue IS done); for every other role its status label is set
// — the role's label is added and the sibling status:* labels are removed, so an
// issue never carries two. Both paths are idempotent: closing an already-closed
// issue and adding a present label are no-ops, and removing an absent label is
// tolerated. issueID is the numeric issue number (as set on Issue.ID).
func (c *Client) Transition(ctx context.Context, issueID string, role scheduler.Role) error {
	if role == scheduler.RoleDone {
		return c.setState(ctx, issueID, "closed")
	}
	label := c.labels[role]
	if label == "" {
		return fmt.Errorf("githubissues: no status label for role %d", role)
	}
	if err := c.addLabel(ctx, issueID, label); err != nil {
		return err
	}
	for r, sib := range c.labels {
		if r == role || sib == "" || sib == label {
			continue
		}
		if err := c.removeLabel(ctx, issueID, sib); err != nil {
			return err
		}
	}
	return nil
}

// setState PATCHes an issue's state (e.g. "closed"). GitHub returns 200 whether or
// not the state actually changed, so closing a closed issue is a no-op.
func (c *Client) setState(ctx context.Context, issueID, state string) error {
	payload, err := json.Marshal(map[string]string{"state": state})
	if err != nil {
		return err
	}
	u := api + "/repos/" + c.repo + "/issues/" + issueID
	code, raw, err := c.do(ctx, http.MethodPatch, u, payload)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("githubissues: PATCH %s: http %d: %s", u, code, strings.TrimSpace(string(raw)))
	}
	return nil
}

// addLabel adds one label to an issue. GitHub merges into the existing set, so
// adding a label the issue already carries is a no-op (still 200).
func (c *Client) addLabel(ctx context.Context, issueID, label string) error {
	payload, err := json.Marshal(map[string][]string{"labels": {label}})
	if err != nil {
		return err
	}
	u := api + "/repos/" + c.repo + "/issues/" + issueID + "/labels"
	code, raw, err := c.do(ctx, http.MethodPost, u, payload)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("githubissues: POST %s: http %d: %s", u, code, strings.TrimSpace(string(raw)))
	}
	return nil
}

// removeLabel removes one label from an issue. Removing a label the issue does not
// carry returns 404 — a no-op for the "never two status labels" invariant, so it
// is treated as success.
func (c *Client) removeLabel(ctx context.Context, issueID, label string) error {
	u := api + "/repos/" + c.repo + "/issues/" + issueID + "/labels/" + url.PathEscape(label)
	code, raw, err := c.do(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	if code == http.StatusOK || code == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("githubissues: DELETE %s: http %d: %s", u, code, strings.TrimSpace(string(raw)))
}

// PostComment creates an issue comment. issueID is the numeric issue number.
func (c *Client) PostComment(ctx context.Context, issueID, body string) error {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	u := api + "/repos/" + c.repo + "/issues/" + issueID + "/comments"
	code, raw, err := c.do(ctx, http.MethodPost, u, payload)
	if err != nil {
		return err
	}
	if code != http.StatusCreated && code != http.StatusOK {
		return fmt.Errorf("githubissues: POST %s: http %d: %s", u, code, strings.TrimSpace(string(raw)))
	}
	return nil
}

// Comments lists an issue's comments as scheduler.Comment{Author, Body}. issueID
// is the numeric issue number (as set on Issue.ID).
func (c *Client) Comments(ctx context.Context, issueID string) ([]scheduler.Comment, error) {
	u := api + "/repos/" + c.repo + "/issues/" + issueID + "/comments"
	var out []struct {
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := c.getJSON(ctx, u, &out); err != nil {
		return nil, err
	}
	cs := make([]scheduler.Comment, 0, len(out))
	for _, n := range out {
		cs = append(cs, scheduler.Comment{Author: n.User.Login, Body: n.Body})
	}
	return cs, nil
}

// Compile-time proof the client satisfies the full tracker interface.
var _ scheduler.Tracker = (*Client)(nil)
