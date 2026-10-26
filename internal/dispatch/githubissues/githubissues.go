// Package githubissues is a GitHub repo's Issues as Jam's tracker.Tracker
// (NewJam), at parity with internal/dispatch/linear. Lifecycle states are
// status labels (status:ready, status:in-progress, …) and Done is an issue
// being closed; blocker gating is a body convention (Depends on #N / Blocked
// by #N, same-repo). Live calls are exercised by the integration-tagged test.
package githubissues

import (
	"bytes"
	"cmp"
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
	"github.com/aethons-tools/cove/internal/ticketkey"
	"github.com/aethons-tools/cove/internal/tracker"
)

const api = "https://api.github.com"

// Client talks to one GitHub repo's Issues; it satisfies tracker.Tracker.
type Client struct {
	http           *http.Client
	token          string
	repo           string                    // "owner/name"
	prefix         string                    // class label prefix, e.g. "class:"
	dispatchPrefix string                    // dispatch label prefix: only issues with a label under it are dispatchable
	labels         map[scheduler.Role]string // role → status label
}

// NewJam builds the Client Jam uses. Ticket keys are "owner/repo#N", the
// dispatch-label gate defaults to "dispatch:", the class prefix to "class:",
// and pull requests and already-closed issues are dropped from search hits.
// The repo is required and validated; an empty token is an error. No network
// call is made at construction — the lifecycle states are labels, not ids.
func NewJam(gt kit.GitHubTracker, token string, httpc *http.Client) (*Client, error) {
	repo := strings.TrimSpace(gt.Repo)
	if err := ticketkey.ValidRepo(repo); err != nil {
		return nil, fmt.Errorf("githubissues: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("githubissues: empty tracker token")
	}
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Client{
		http: httpc, token: token, repo: repo,
		prefix:         cmp.Or(gt.ClassLabelPrefix, "class:"),
		dispatchPrefix: cmp.Or(gt.DispatchLabelPrefix, "dispatch:"),
		labels: map[scheduler.Role]string{
			scheduler.RoleReady:      gt.States.Ready,
			scheduler.RoleInProgress: gt.States.InProgress,
			scheduler.RoleInReview:   gt.States.InReview,
			scheduler.RoleNeedsInput: gt.States.NeedsInput,
			scheduler.RoleBlocked:    gt.States.Blocked,
		},
	}, nil
}

// send issues a REST call and returns the status, headers, body and any
// transport error: Bearer auth, GitHub media type. It mirrors
// internal/dispatch/github's helper.
func (c *Client) send(ctx context.Context, method, u string, payload []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	return resp.StatusCode, resp.Header, raw.Bytes(), nil
}

// do is send without the headers. A rate limit is a
// *tracker.RateLimitError (so a write waits it out as a read does).
func (c *Client) do(ctx context.Context, method, u string, payload []byte) (int, []byte, error) {
	code, h, raw, err := c.send(ctx, method, u, payload)
	if err == nil {
		if rl := rateLimit(code, h, raw, time.Now()); rl != nil {
			return code, raw, rl
		}
	}
	return code, raw, err
}

// bodyText is an error body for a message: trimmed, with the token
// redacted should a proxy have echoed the request.
func (c *Client) bodyText(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if c.token == "" {
		return text // NewJam refuses an empty token; never ReplaceAll("")
	}
	return strings.ReplaceAll(text, c.token, "[redacted]")
}

// rateLimit reads a GitHub rate-limit refusal: a 403 or 429 with Retry-After
// (at least a second: Retry-After: 0 is not a busy retry), or with
// X-RateLimit-Remaining: 0 (wait until X-RateLimit-Reset), a 403
// whose body names a secondary rate limit, or any 429 (both: wait a
// minute). Any other 403 is a permissions error: nil.
func rateLimit(code int, h http.Header, body []byte, now time.Time) error {
	if code != http.StatusForbidden && code != http.StatusTooManyRequests {
		return nil
	}
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s >= 0 {
		return &tracker.RateLimitError{Service: tracker.KindGitHub, RetryAfter: max(time.Duration(s)*time.Second, time.Second)}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return &tracker.RateLimitError{Service: tracker.KindGitHub, RetryAfter: max(time.Unix(reset, 0).Sub(now), time.Second)}
		}
	}
	if code == http.StatusTooManyRequests || bytes.Contains(bytes.ToLower(body), []byte("secondary rate limit")) {
		return &tracker.RateLimitError{Service: tracker.KindGitHub, RetryAfter: time.Minute}
	}
	return nil
}

// getJSON performs a GET and unmarshals a 200 body into out, erroring
// otherwise. A rate limit is a *tracker.RateLimitError.
func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	_, err := c.getJSONLink(ctx, u, out)
	return err
}

// getJSONLink is getJSON that also returns the response's Link header.
func (c *Client) getJSONLink(ctx context.Context, u string, out any) (string, error) {
	code, h, raw, err := c.send(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if rl := rateLimit(code, h, raw, time.Now()); rl != nil {
		return "", rl
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("githubissues: GET %s: http %d: %s", u, code, c.bodyText(raw))
	}
	if out != nil {
		return h.Get("Link"), json.Unmarshal(raw, out)
	}
	return h.Get("Link"), nil
}

// ghIssue is the shared projection of a search hit / issue payload.
type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	PullRequest *struct{} `json:"pull_request"` // set on a pull request: GitHub lists PRs as issues
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (c *Client) toIssue(n ghIssue) scheduler.Issue {
	names := make([]string, len(n.Labels))
	for i, l := range n.Labels {
		names[i] = l.Name
	}
	class, dispatchLabeled := scheduler.ClassifyLabels(names, c.prefix, c.dispatchPrefix)
	return scheduler.Issue{
		ID: strconv.Itoa(n.Number), Identifier: c.key(n.Number), Title: n.Title,
		Description: n.Body, Class: class, DispatchLabeled: dispatchLabeled,
	}
}

// key is issue n's identifier: "owner/repo#N".
func (c *Client) key(n int) string { return ticketkey.GitHub(c.repo, n) }

// search runs a GitHub issue search for the given status label in this repo,
// scoped to open issues: 100 a page, following Link rel="next" (on the API
// host only) for at most maxFeedPages pages, then dropping pull requests and
// already-closed hits.
func (c *Client) search(ctx context.Context, label string) ([]ghIssue, error) {
	q := url.Values{"q": {fmt.Sprintf("repo:%s is:issue is:open label:%q", c.repo, label)}}
	q.Set("per_page", "100")
	var items []ghIssue
	u := api + "/search/issues?" + q.Encode()
	for pages := 0; u != "" && pages < maxFeedPages; pages++ {
		var out struct {
			Items []ghIssue `json:"items"`
		}
		link, err := c.getJSONLink(ctx, u, &out)
		if err != nil {
			return nil, err
		}
		// The search index lags: a just-closed issue can still match is:open,
		// and a pull request is an issue to GitHub. Neither is work to dispatch.
		for _, n := range out.Items {
			if n.PullRequest == nil && n.State != "closed" {
				items = append(items, n)
			}
		}
		u = nextPage(link)
	}
	return items, nil
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
			return nil, fmt.Errorf("githubissues: get issue #%d: http %d: %s", n, code, c.bodyText(raw))
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

// ListReady returns open issues carrying the READY status label and a
// dispatch label whose same-repo blockers
// (Depends on #N / Blocked by #N in the body) are all closed, or which have
// none. Blocker doneness is resolved by a separate authoritative lookup over
// the deduped union of referenced numbers — the same discipline as Linear's
// doneBlockers (COV-56): the search projection is never trusted for it.
func (c *Client) ListReady(ctx context.Context) ([]scheduler.Issue, error) {
	items, err := c.search(ctx, c.labels[scheduler.RoleReady])
	if err != nil {
		return nil, err
	}
	// The dispatch gate first: an issue not tagged for dispatch is never
	// raised, so its blockers are not worth a GET each.
	gated := items[:0]
	for _, n := range items {
		if c.toIssue(n).DispatchLabeled {
			gated = append(gated, n)
		}
	}
	items = gated
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
		return fmt.Errorf("githubissues: PATCH %s: http %d: %s", u, code, c.bodyText(raw))
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
		return fmt.Errorf("githubissues: POST %s: http %d: %s", u, code, c.bodyText(raw))
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
	return fmt.Errorf("githubissues: DELETE %s: http %d: %s", u, code, c.bodyText(raw))
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
		return fmt.Errorf("githubissues: POST %s: http %d: %s", u, code, c.bodyText(raw))
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

// IssueByIdentifier resolves a ticket key ("owner/repo#N") to the issue
// number Transition, PostComment and Comments take. The key must name this
// Client's repo (else ticketkey.ErrOtherRepo); GitHub owner/name are
// case-insensitive. A missing issue, or a key that names a pull request, is
// an error.
func (c *Client) IssueByIdentifier(ctx context.Context, identifier string) (string, error) {
	repo, n, err := ticketkey.ParseGitHub(identifier)
	if err != nil {
		return "", err
	}
	key := ticketkey.GitHub(repo, n) // the canonical spelling, for messages
	if !strings.EqualFold(repo, c.repo) {
		return "", fmt.Errorf("githubissues: ticket %s: %w (%s)", key, ticketkey.ErrOtherRepo, c.repo)
	}
	num := strconv.Itoa(n)
	code, raw, err := c.do(ctx, http.MethodGet, api+"/repos/"+c.repo+"/issues/"+num, nil)
	if err != nil {
		return "", err
	}
	if code == http.StatusNotFound {
		return "", fmt.Errorf("githubissues: no issue %s", key)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("githubissues: get issue %s: http %d: %s", key, code, c.bodyText(raw))
	}
	var out ghIssue
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.PullRequest != nil {
		return "", fmt.Errorf("githubissues: %s is a pull request, not an issue", key)
	}
	return num, nil
}

// ghComment is the projection of a repo issue comment.
type ghComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`  // …/issues/N#… or …/pull/N#…
	IssueURL  string `json:"issue_url"` // …/repos/{r}/issues/N (also for a PR)
	User      struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Type  string `json:"type"` // "User" | "Bot" | "Organization"
	} `json:"user"`
}

// maxFeedPages caps the pages one CommentFeed call (or one Jam ready
// search) reads, so a repo busy with PR conversations (read, then skipped)
// can't drain the rate budget in one poll. The cursor (CommentFeedCursor) carries the next poll on.
const maxFeedPages = 10

// linkNext matches a Link header's rel="next" target.
var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextPage is the Link header's next page, if it is on the API host. A
// next link anywhere else is not followed (the token goes with it).
func nextPage(link string) string {
	m := linkNext.FindStringSubmatch(link)
	if m == nil || !strings.HasPrefix(m[1], api+"/") {
		return ""
	}
	return m[1]
}

// CommentFeed returns the repo's issue comments created at or after since,
// oldest first, at most limit (100 when limit <= 0). It is CommentFeedCursor
// without the cursor.
func (c *Client) CommentFeed(ctx context.Context, since time.Time, limit int) ([]tracker.FeedComment, error) {
	out, _, err := c.CommentFeedCursor(ctx, since, limit)
	return out, err
}

// CommentFeedCursor calls GET /repos/{r}/issues/comments?since=&sort=created
// &direction=asc, follows Link rel="next" for at most maxFeedPages pages, and
// returns the issue comments created at or after since, at most limit.
// GitHub's since filters by update time, so an edited old comment comes back:
// one created before since is dropped. One created exactly at since is kept
// (the relay dedups by id). Comments on pull requests are skipped.
//
// next is where the following poll should start: the latest created_at of
// every comment this call consumed — returned or skipped — and never before
// since. A comment read past the limit is not consumed, so it is read again.
// (created_at, not updated_at: an edited comment's updated_at can be later
// than an unread comment's created_at, and since=updated_at would lose it.)
// On an error next is since. A rate limit is a *tracker.RateLimitError.
func (c *Client) CommentFeedCursor(ctx context.Context, since time.Time, limit int) (out []tracker.FeedComment, next time.Time, err error) {
	if limit <= 0 {
		limit = 100
	}
	next, from := since, since // from: an error's cursor (nothing consumed)
	// GitHub's since is whole seconds: ask from, and keep, the whole second
	// since falls in, or a comment earlier in that second would be dropped.
	since = since.Truncate(time.Second)
	q := url.Values{"since": {since.UTC().Format(time.RFC3339)}, "sort": {"created"}, "direction": {"asc"}, "per_page": {"100"}}
	u := api + "/repos/" + c.repo + "/issues/comments?" + q.Encode()
	for pages := 0; u != "" && pages < maxFeedPages && len(out) < limit; pages++ {
		code, h, raw, err := c.send(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, from, err
		}
		if rl := rateLimit(code, h, raw, time.Now()); rl != nil {
			return nil, from, rl
		}
		if code != http.StatusOK {
			return nil, from, fmt.Errorf("githubissues: GET %s: http %d: %s", u, code, c.bodyText(raw))
		}
		var page []ghComment
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, from, err
		}
		for _, n := range page {
			if len(out) >= limit {
				break // unconsumed: the cursor stays before it
			}
			at, err := time.Parse(time.RFC3339, n.CreatedAt)
			if err != nil {
				continue
			}
			if at.After(next) {
				next = at
			}
			if fc, ok := c.feedComment(n, at, since); ok {
				out = append(out, fc)
			}
		}
		u = nextPage(h.Get("Link"))
	}
	return out, next, nil
}

// feedComment maps a repo comment created at at, or reports false for one
// the feed skips.
func (c *Client) feedComment(n ghComment, at, since time.Time) (tracker.FeedComment, bool) {
	if onPullRequest(n.HTMLURL) {
		return tracker.FeedComment{}, false // a pull request's conversation, not an issue's
	}
	if at.Before(since) {
		return tracker.FeedComment{}, false // an old comment, edited since
	}
	num, err := strconv.Atoi(n.IssueURL[strings.LastIndex(n.IssueURL, "/")+1:])
	if err != nil || num < 1 {
		return tracker.FeedComment{}, false
	}
	return tracker.FeedComment{
		ID: strconv.FormatInt(n.ID, 10), Body: n.Body, CreatedAt: at,
		Author: n.User.Login, AuthorID: strconv.FormatInt(n.User.ID, 10), AuthorBot: n.User.Type == "Bot",
		IssueIdentifier: c.key(num),
	}, true
}

// onPullRequest reports whether a comment's html_url
// (https://github.com/{owner}/{repo}/{issues|pull}/N#…) is on a pull request:
// the path segment after owner/repo, so a repo or owner named "pull" is not
// mistaken for one. An unparseable URL counts as a pull request (skipped).
func onPullRequest(htmlURL string) bool {
	u, err := url.Parse(htmlURL)
	if err != nil {
		return true
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(seg) < 3 || seg[2] != "issues"
}

// Viewer returns the login the token authenticates as (GET /user) — the
// relay's self-filter. A GitHub App installation token cannot call it
// (403 "Resource not accessible by integration"): that is
// tracker.ErrNoViewer, and the relay then relies on dropping Bot authors.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	u := api + "/user"
	code, h, raw, err := c.send(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if rl := rateLimit(code, h, raw, time.Now()); rl != nil {
		return "", rl
	}
	if code == http.StatusForbidden && bytes.Contains(raw, []byte("Resource not accessible by integration")) {
		return "", fmt.Errorf("githubissues: GET %s: %w", u, tracker.ErrNoViewer)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("githubissues: GET %s: http %d: %s", u, code, c.bodyText(raw))
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Login, nil
}

// Jam's tracker contract.
var _ tracker.Tracker = (*Client)(nil)

// The client places its own feed cursor (pages it reads and skips).
var _ tracker.CursorFeed = (*Client)(nil)
