package githubissues

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
)

// rtFunc is a fake http.RoundTripper returning canned responses per request.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// router dispatches by (method, path[, query]) so tests declare responses by the
// REST endpoint hit, independent of request order.
type router map[string]string

func (rt router) RoundTrip(r *http.Request) (*http.Response, error) {
	// Try path+query first (for the search endpoint), then bare path.
	if body, ok := rt[r.URL.Path+"?"+r.URL.RawQuery]; ok {
		return jsonResp(200, body), nil
	}
	if body, ok := rt[r.URL.Path]; ok {
		return jsonResp(200, body), nil
	}
	return jsonResp(404, `{"message":"not found"}`), nil
}

func testCfg() kit.Config {
	return kit.Config{
		Tracker: &kit.Tracker{GitHub: &kit.GitHubTracker{
			Repo:             "acme/board",
			ClassLabelPrefix: "class:",
			States: kit.StateMap{
				Ready: "status:ready", InProgress: "status:in-progress",
				InReview: "status:in-review", NeedsInput: "status:needs-input",
				Blocked: "status:blocked",
			},
		}},
	}
}

func newTestClient(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	c, err := New(testCfg(), "tok", &http.Client{Transport: rt})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRejectsEmptyToken(t *testing.T) {
	if _, err := New(testCfg(), "", nil); err == nil {
		t.Fatal("New with empty token: want error, got nil")
	}
}

func TestNewRequiresTrackerGitHub(t *testing.T) {
	if _, err := New(kit.Config{}, "tok", nil); err == nil {
		t.Fatal("New without tracker.github: want error, got nil")
	}
}

func TestNewResolvesRepoOverrideAndInheritance(t *testing.T) {
	// Explicit tracker.github.repo wins.
	c, err := New(testCfg(), "tok", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.repo != "acme/board" {
		t.Fatalf("repo = %q; want acme/board (override)", c.repo)
	}

	// With no override, the repo is inherited from source-control.github.project.
	cfg := kit.Config{
		SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/code"}},
		Tracker:       &kit.Tracker{GitHub: &kit.GitHubTracker{States: testCfg().Tracker.GitHub.States}},
	}
	c2, err := New(cfg, "tok", nil)
	if err != nil {
		t.Fatalf("New (inherit): %v", err)
	}
	if c2.repo != "acme/code" {
		t.Fatalf("repo = %q; want acme/code (inherited)", c2.repo)
	}
	// Class prefix defaults to "class:" when the config leaves it empty.
	if c2.prefix != "class:" {
		t.Fatalf("prefix = %q; want class:", c2.prefix)
	}
}

func TestNewSetsBearerAuthHeader(t *testing.T) {
	var sawAuth, sawAccept string
	c := newTestClient(t, rtFunc(func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		sawAccept = r.Header.Get("Accept")
		return jsonResp(200, `{"items":[]}`), nil
	}))
	if _, err := c.ListReady(context.Background()); err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if sawAuth != "Bearer tok" {
		t.Fatalf("Authorization = %q; want Bearer tok", sawAuth)
	}
	if sawAccept != "application/vnd.github+json" {
		t.Fatalf("Accept = %q; want application/vnd.github+json", sawAccept)
	}
}

func TestListReadyParsesClassAndIdentity(t *testing.T) {
	var sawQuery string
	c := newTestClient(t, rtFunc(func(r *http.Request) (*http.Response, error) {
		sawQuery = r.URL.Query().Get("q")
		return jsonResp(200, `{"items":[
		 {"number":42,"title":"T1","body":"no markers here","labels":[{"name":"class:implementor"},{"name":"status:ready"}]}
		]}`), nil
	}))
	got, err := c.ListReady(context.Background())
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if !strings.Contains(sawQuery, "repo:acme/board") || !strings.Contains(sawQuery, "status:ready") {
		t.Fatalf("search q = %q; want repo + status:ready", sawQuery)
	}
	if len(got) != 1 {
		t.Fatalf("ListReady = %+v; want one issue", got)
	}
	if got[0].ID != "42" || got[0].Identifier != "#42" || got[0].Class != "implementor" {
		t.Fatalf("issue = %+v; want ID=42 Identifier=#42 Class=implementor", got[0])
	}
}

// runReady drives ListReady with a search response plus per-blocker issue-state
// responses (keyed by their REST path).
func runReady(t *testing.T, search string, blockerStates map[string]string) []scheduler.Issue {
	t.Helper()
	rt := router{"/search/issues": search}
	for path, body := range blockerStates {
		rt[path] = body
	}
	c := newTestClient(t, rt)
	got, err := c.ListReady(context.Background())
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	return got
}

func TestListReadyExcludesOnOpenBlocker(t *testing.T) {
	const search = `{"items":[
	 {"number":1,"title":"has open blocker","body":"Depends on #9","labels":[{"name":"status:ready"}]}
	]}`
	got := runReady(t, search, map[string]string{
		"/repos/acme/board/issues/9": `{"number":9,"state":"open"}`,
	})
	if len(got) != 0 {
		t.Fatalf("ListReady = %+v; want none (blocker #9 is open)", got)
	}
}

func TestListReadyIncludesWhenAllBlockersClosed(t *testing.T) {
	const search = `{"items":[
	 {"number":2,"title":"blockers done","body":"Depends on #9 and Blocked by #10","labels":[{"name":"status:ready"}]}
	]}`
	got := runReady(t, search, map[string]string{
		"/repos/acme/board/issues/9":  `{"number":9,"state":"closed"}`,
		"/repos/acme/board/issues/10": `{"number":10,"state":"closed"}`,
	})
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("ListReady = %+v; want #2 (all blockers closed)", got)
	}
}

func TestListReadyIncludesWhenNoMarker(t *testing.T) {
	const search = `{"items":[
	 {"number":3,"title":"no deps","body":"just a plain description","labels":[{"name":"status:ready"}]}
	]}`
	// No blocker fetch should be needed; the router 404s any stray issue GET.
	got := runReady(t, search, nil)
	if len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("ListReady = %+v; want #3 (no blocker markers)", got)
	}
}

func TestListReadyIgnoresCrossRepoBlockerWithLog(t *testing.T) {
	const search = `{"items":[
	 {"number":4,"title":"cross-repo dep","body":"Depends on other/repo#7","labels":[{"name":"status:ready"}]}
	]}`
	// A cross-repo marker is out of scope for v1: it must NOT gate, and must be
	// logged. The router 404s any issue GET, proving no same-repo #7 lookup happens.
	var logged strings.Builder
	lg, err := logging.New(logging.Options{Mode: logging.Unattended, Stderr: &logged})
	if err != nil {
		t.Fatalf("logging.New: %v", err)
	}
	rt := router{"/search/issues": search}
	c := newTestClient(t, rt)
	got, err := c.ListReady(logging.Into(context.Background(), lg))
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if len(got) != 1 || got[0].ID != "4" {
		t.Fatalf("ListReady = %+v; want #4 (cross-repo marker ignored)", got)
	}
	if !strings.Contains(logged.String(), "other/repo#7") {
		t.Fatalf("expected a logged note mentioning the cross-repo ref; log = %q", logged.String())
	}
}

func TestListReadyHoldsOnMissingBlocker(t *testing.T) {
	const search = `{"items":[
	 {"number":5,"title":"phantom blocker","body":"Blocked by #99","labels":[{"name":"status:ready"}]}
	]}`
	// #99 resolves to 404 (deleted/missing) → treated as still blocking, so #5 is held.
	got := runReady(t, search, nil)
	if len(got) != 0 {
		t.Fatalf("ListReady = %+v; want none (blocker #99 unresolved)", got)
	}
}

func TestListInProgressDerivesTimestampFromTimeline(t *testing.T) {
	rt := router{
		"/search/issues": `{"items":[
		 {"number":8,"title":"WIP","body":"","labels":[{"name":"class:implementor"},{"name":"status:in-progress"}]}
		]}`,
		// Two labelings of the status:in-progress label; the reaper wants the most recent.
		"/repos/acme/board/issues/8/timeline": `[
		 {"event":"labeled","created_at":"2026-07-01T10:00:00Z","label":{"name":"status:in-progress"}},
		 {"event":"labeled","created_at":"2026-07-15T09:00:00Z","label":{"name":"status:in-progress"}},
		 {"event":"labeled","created_at":"2026-08-01T09:00:00Z","label":{"name":"status:blocked"}},
		 {"event":"commented","created_at":"2026-08-05T09:00:00Z","label":{"name":""}}
		]`,
	}
	c := newTestClient(t, rt)
	got, err := c.ListInProgress(context.Background())
	if err != nil {
		t.Fatalf("ListInProgress: %v", err)
	}
	if len(got) != 1 || got[0].ID != "8" || got[0].Class != "implementor" {
		t.Fatalf("ListInProgress = %+v; want one #8 with class implementor", got)
	}
	want := "2026-07-15T09:00:00Z"
	if got[0].StartedAt.UTC().Format("2006-01-02T15:04:05Z") != want {
		t.Fatalf("StartedAt = %s; want %s (most recent status:in-progress labeling)", got[0].StartedAt, want)
	}
}

func TestListInProgressSkipsWhenNoLabelEvent(t *testing.T) {
	rt := router{
		"/search/issues": `{"items":[
		 {"number":8,"title":"WIP","body":"","labels":[{"name":"status:in-progress"}]}
		]}`,
		// No labeled event for status:in-progress → can't establish staleness → skipped.
		"/repos/acme/board/issues/8/timeline": `[
		 {"event":"labeled","created_at":"2026-07-01T10:00:00Z","label":{"name":"status:blocked"}}
		]`,
	}
	c := newTestClient(t, rt)
	got, err := c.ListInProgress(context.Background())
	if err != nil {
		t.Fatalf("ListInProgress: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListInProgress = %+v; want none (no in-progress labeling)", got)
	}
}

// recordedReq is one HTTP call the recorder saw.
type recordedReq struct {
	method string
	path   string
	body   string
}

// recorder captures every request and returns a canned status per (method,path),
// defaulting to 200 — enough to assert the write methods' REST shape hermetically.
type recorder struct {
	reqs   []recordedReq
	status func(method, path string) int
}

func (rec *recorder) RoundTrip(r *http.Request) (*http.Response, error) {
	body := ""
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}
	rec.reqs = append(rec.reqs, recordedReq{r.Method, r.URL.Path, body})
	code := 200
	if rec.status != nil {
		code = rec.status(r.Method, r.URL.Path)
	}
	return jsonResp(code, `{}`), nil
}

func TestTransitionDoneClosesIssue(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(t, rec)
	if err := c.Transition(context.Background(), "42", scheduler.RoleDone); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if len(rec.reqs) != 1 {
		t.Fatalf("Done should issue one call; got %+v", rec.reqs)
	}
	got := rec.reqs[0]
	if got.method != http.MethodPatch || got.path != "/repos/acme/board/issues/42" {
		t.Fatalf("Done call = %s %s; want PATCH /repos/acme/board/issues/42", got.method, got.path)
	}
	if !strings.Contains(got.body, `"state":"closed"`) {
		t.Fatalf("Done body = %q; want state=closed", got.body)
	}
}

func TestTransitionNonDoneSetsLabelRemovesSiblings(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(t, rec)
	if err := c.Transition(context.Background(), "42", scheduler.RoleInProgress); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	var added []string
	removed := map[string]bool{}
	for _, r := range rec.reqs {
		switch r.method {
		case http.MethodPost:
			if r.path != "/repos/acme/board/issues/42/labels" {
				t.Fatalf("add label call to wrong path: %s", r.path)
			}
			if strings.Contains(r.body, "status:in-progress") {
				added = append(added, "status:in-progress")
			}
		case http.MethodDelete:
			const p = "/repos/acme/board/issues/42/labels/"
			if !strings.HasPrefix(r.path, p) {
				t.Fatalf("remove label call to wrong path: %s", r.path)
			}
			removed[strings.TrimPrefix(r.path, p)] = true
		default:
			t.Fatalf("unexpected %s to %s", r.method, r.path)
		}
	}
	if len(added) != 1 {
		t.Fatalf("want status:in-progress added exactly once; got %v", added)
	}
	// Every other status label must be removed, and the target never removed.
	for _, sib := range []string{"status:ready", "status:in-review", "status:needs-input", "status:blocked"} {
		if !removed[sib] {
			t.Fatalf("sibling %q not removed; removed=%v", sib, removed)
		}
	}
	if removed["status:in-progress"] {
		t.Fatalf("target label status:in-progress must not be removed")
	}
}

func TestTransitionIsIdempotent(t *testing.T) {
	// Closing an already-closed issue still returns 200; removing an absent sibling
	// label returns 404. Both must be tolerated as no-ops.
	rec := &recorder{status: func(method, path string) int {
		if method == http.MethodDelete {
			return http.StatusNotFound
		}
		return http.StatusOK
	}}
	c := newTestClient(t, rec)
	if err := c.Transition(context.Background(), "42", scheduler.RoleDone); err != nil {
		t.Fatalf("Transition Done (already closed): %v", err)
	}
	if err := c.Transition(context.Background(), "42", scheduler.RoleInReview); err != nil {
		t.Fatalf("Transition InReview (siblings absent): %v", err)
	}
}

func TestPostCommentCreatesComment(t *testing.T) {
	rec := &recorder{status: func(method, path string) int { return http.StatusCreated }}
	c := newTestClient(t, rec)
	if err := c.PostComment(context.Background(), "42", "hello there"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if len(rec.reqs) != 1 {
		t.Fatalf("PostComment should issue one call; got %+v", rec.reqs)
	}
	got := rec.reqs[0]
	if got.method != http.MethodPost || got.path != "/repos/acme/board/issues/42/comments" {
		t.Fatalf("PostComment call = %s %s; want POST .../issues/42/comments", got.method, got.path)
	}
	if !strings.Contains(got.body, `"body":"hello there"`) {
		t.Fatalf("PostComment body = %q; want the comment body", got.body)
	}
}

func TestCommentsMapsAuthorAndBody(t *testing.T) {
	rt := router{
		"/repos/acme/board/issues/8/comments": `[
		 {"body":"first","user":{"login":"brent"}},
		 {"body":"second","user":{"login":"agent"}}
		]`,
	}
	c := newTestClient(t, rt)
	got, err := c.Comments(context.Background(), "8")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(got) != 2 || got[0].Author != "brent" || got[0].Body != "first" || got[1].Author != "agent" {
		t.Fatalf("Comments = %+v", got)
	}
}
