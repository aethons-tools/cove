package trackertest

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
	"github.com/aethons-tools/cove/internal/kit"
)

// GHIssue is one issue (or pull request) in the fake repo.
type GHIssue struct {
	Number      int
	Title, Body string
	Closed      bool
	Labels      []string
	PullRequest bool
}

// GHComment is one comment in the fake repo.
type GHComment struct {
	ID        int64 // 0: assigned
	Issue     int
	Body      string
	Login     string
	UserID    int64
	Bot       bool // user type "Bot"
	OnPull    bool // a pull request's conversation (html_url …/pull/N)
	CreatedAt time.Time
}

// GitHub is a fake GitHub REST API for one repo. It covers search, issues,
// labels, comments, the repo comment feed (paginated) and /user.
type GitHub struct {
	Repo, Login string
	PerPage     int // feed page size override (0: the request's per_page)

	srv      *httptest.Server
	mu       sync.Mutex
	issues   map[int]*GHIssue
	comments []GHComment
	seq      int64
}

// GitHubStates is the status-label map the fake's client is built with.
func GitHubStates() kit.StateMap {
	return kit.StateMap{Ready: "status:ready", InProgress: "status:in-progress", InReview: "status:in-review",
		NeedsInput: "status:needs-input", Blocked: "status:blocked"}
}

func NewGitHub(t testing.TB, repo, login string) *GitHub {
	g := &GitHub{Repo: repo, Login: login, issues: map[int]*GHIssue{}, seq: 1000}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

// HTTPClient reaches the fake in place of api.github.com.
func (g *GitHub) HTTPClient() *http.Client { return Rewrite(g.srv) }

// Client is Jam's GitHub client against the fake.
func (g *GitHub) Client(t testing.TB) *githubissues.Client {
	t.Helper()
	c, err := githubissues.NewJam(kit.GitHubTracker{Repo: g.Repo, ClassLabelPrefix: "class:", DispatchLabelPrefix: "dispatch:", States: GitHubStates()}, Token, g.HTTPClient())
	if err != nil {
		t.Fatalf("githubissues.NewJam: %v", err)
	}
	return c
}

func (g *GitHub) AddIssue(i GHIssue) {
	g.mu.Lock()
	defer g.mu.Unlock()
	i.Labels = slices.Clone(i.Labels)
	g.issues[i.Number] = &i
}

func (g *GitHub) AddComment(c GHComment) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.ID == 0 {
		g.seq++
		c.ID = g.seq
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	g.comments = append(g.comments, c)
}

// State is issue n's role: "done" when closed, else its status label's.
func (g *GitHub) State(n int) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	i := g.issues[n]
	if i.Closed {
		return "done"
	}
	for _, l := range i.Labels {
		if role, ok := trimStatus(l); ok {
			return role
		}
	}
	return ""
}

func (g *GitHub) CommentsOn(n int) []GHComment {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []GHComment
	for _, c := range g.comments {
		if c.Issue == n {
			out = append(out, c)
		}
	}
	return out
}

func msg(s string) map[string]string { return map[string]string{"message": s} }

func (g *GitHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+Token {
		writeJSON(w, http.StatusUnauthorized, msg("Bad credentials"))
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	prefix := "/repos/" + g.Repo
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		writeJSON(w, http.StatusOK, map[string]any{"login": g.Login, "id": 1, "type": "User"})
	case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
		g.search(w, r.URL.Query().Get("q"))
	case r.Method == http.MethodGet && r.URL.Path == prefix+"/issues/comments":
		g.feed(w, r)
	case strings.HasPrefix(r.URL.Path, prefix+"/issues/"):
		g.issue(w, r, strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/issues/"), "/"))
	default:
		writeJSON(w, http.StatusNotFound, msg("Not Found"))
	}
}

var searchLabel = regexp.MustCompile(`label:"([^"]*)"`)

// search honours repo:, is:open and one quoted label:. It ignores is:issue
// and returns matching pull requests too, so the client's own pull request
// filter is what keeps them out of the ready list.
func (g *GitHub) search(w http.ResponseWriter, q string) {
	m := searchLabel.FindStringSubmatch(q)
	items := []map[string]any{}
	for _, n := range slices.Sorted(maps.Keys(g.issues)) {
		i := g.issues[n]
		switch {
		case !strings.Contains(q, "repo:"+g.Repo), m == nil, !slices.Contains(i.Labels, m[1]):
		case strings.Contains(q, "is:open") && i.Closed:
		default:
			items = append(items, g.issueJSON(i))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (g *GitHub) issueJSON(i *GHIssue) map[string]any {
	labels := []map[string]string{}
	for _, l := range i.Labels {
		labels = append(labels, map[string]string{"name": l})
	}
	state := "open"
	if i.Closed {
		state = "closed"
	}
	out := map[string]any{"number": i.Number, "title": i.Title, "body": i.Body, "state": state, "labels": labels}
	if i.PullRequest {
		out["pull_request"] = map[string]string{"url": api + "/repos/" + g.Repo + "/pulls/" + strconv.Itoa(i.Number)}
	}
	return out
}

func (g *GitHub) commentJSON(c GHComment) map[string]any {
	kind, typ := "issues", "User"
	if c.OnPull {
		kind = "pull"
	}
	if c.Bot {
		typ = "Bot"
	}
	n, id := strconv.Itoa(c.Issue), strconv.FormatInt(c.ID, 10)
	at := c.CreatedAt.UTC().Format(time.RFC3339)
	return map[string]any{"id": c.ID, "body": c.Body, "created_at": at, "updated_at": at,
		"html_url":  "https://github.com/" + g.Repo + "/" + kind + "/" + n + "#issuecomment-" + id,
		"issue_url": api + "/repos/" + g.Repo + "/issues/" + n,
		"user":      map[string]any{"login": c.Login, "id": c.UserID, "type": typ}}
}

func (g *GitHub) issue(w http.ResponseWriter, r *http.Request, seg []string) {
	n, err := strconv.Atoi(seg[0])
	i, ok := g.issues[n]
	if err != nil || !ok {
		writeJSON(w, http.StatusNotFound, msg("Not Found"))
		return
	}
	switch {
	case len(seg) == 1 && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, g.issueJSON(i))
	case len(seg) == 1 && r.Method == http.MethodPatch:
		var b struct {
			State string `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		i.Closed = b.State == "closed"
		writeJSON(w, http.StatusOK, g.issueJSON(i))
	case len(seg) == 2 && seg[1] == "labels" && r.Method == http.MethodPost:
		var b struct {
			Labels []string `json:"labels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		for _, l := range b.Labels {
			if !slices.Contains(i.Labels, l) {
				i.Labels = append(i.Labels, l)
			}
		}
		writeJSON(w, http.StatusOK, g.issueJSON(i)["labels"])
	case len(seg) == 3 && seg[1] == "labels" && r.Method == http.MethodDelete:
		k := slices.Index(i.Labels, seg[2])
		if k < 0 {
			writeJSON(w, http.StatusNotFound, msg("Label does not exist"))
			return
		}
		i.Labels = slices.Delete(i.Labels, k, k+1)
		writeJSON(w, http.StatusOK, g.issueJSON(i)["labels"])
	case len(seg) == 2 && seg[1] == "comments" && r.Method == http.MethodPost:
		var b struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		g.seq++
		c := GHComment{ID: g.seq, Issue: n, Body: b.Body, Login: g.Login, UserID: 1, CreatedAt: time.Now().UTC().Truncate(time.Second)}
		g.comments = append(g.comments, c)
		writeJSON(w, http.StatusCreated, g.commentJSON(c))
	case len(seg) == 2 && seg[1] == "comments" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, c := range g.comments {
			if c.Issue == n {
				out = append(out, g.commentJSON(c))
			}
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeJSON(w, http.StatusNotFound, msg("Not Found"))
	}
}

// feed serves GET /repos/{r}/issues/comments: created at or after since,
// oldest first, paged with an absolute api.github.com Link rel="next".
func (g *GitHub) feed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, _ := time.Parse(time.RFC3339, q.Get("since"))
	per, _ := strconv.Atoi(q.Get("per_page"))
	if g.PerPage > 0 {
		per = g.PerPage
	}
	if per <= 0 {
		per = 30
	}
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	var all []GHComment
	for _, c := range g.comments {
		if !c.CreatedAt.Before(since) {
			all = append(all, c)
		}
	}
	slices.SortStableFunc(all, func(a, b GHComment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	lo := min((page-1)*per, len(all))
	hi := min(lo+per, len(all))
	if hi < len(all) {
		q.Set("page", strconv.Itoa(page+1))
		w.Header().Set("Link", "<"+api+r.URL.Path+"?"+q.Encode()+`>; rel="next"`)
	}
	out := []map[string]any{}
	for _, c := range all[lo:hi] {
		out = append(out, g.commentJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
}
