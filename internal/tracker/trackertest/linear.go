package trackertest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/kit"
)

// LinIssue is one issue on the fake team.
type LinIssue struct {
	ID, Identifier, Title, Description string
	State                              string // workflow state name (LinearStates)
	Labels                             []string
}

// LinComment is one comment on the fake team.
type LinComment struct {
	ID, IssueID, Body, Author, AuthorID string // ID "" : assigned
	CreatedAt                           time.Time
}

// Linear is a fake Linear GraphQL API for one team. It answers the queries
// *linear.Client sends, matched by distinctive substrings: workflow states,
// the ready list, blocker lookup, issue lookup, updates, comments, the team
// comment feed and the viewer.
type Linear struct {
	Team, Viewer string

	srv      *httptest.Server
	mu       sync.Mutex
	issues   []*LinIssue
	comments []LinComment
	seq      int
}

var linearStateType = map[string]string{"Ready": "unstarted", "In Progress": "started", "In Review": "started",
	"Done": "completed", "Needs Input": "unstarted", "Blocked": "backlog"}

// LinearStates is the state map the fake's client is built with.
func LinearStates() kit.StateMap {
	return kit.StateMap{Ready: "Ready", InProgress: "In Progress", InReview: "In Review", Done: "Done", NeedsInput: "Needs Input", Blocked: "Blocked"}
}

func NewLinear(t testing.TB, team, viewer string) *Linear {
	l := &Linear{Team: team, Viewer: viewer}
	l.srv = httptest.NewServer(http.HandlerFunc(l.serve))
	t.Cleanup(l.srv.Close)
	return l
}

// HTTPClient reaches the fake in place of api.linear.app.
func (l *Linear) HTTPClient() *http.Client { return Rewrite(l.srv) }

// Client is Jam's Linear client against the fake.
func (l *Linear) Client(t testing.TB) *linear.Client {
	t.Helper()
	c, err := linear.New(kit.Config{Tracker: &kit.Tracker{Linear: &kit.LinearTracker{
		Team: l.Team, ClassLabelPrefix: "class:", DispatchLabelPrefix: "dispatch:", States: LinearStates()}}}, Token, l.HTTPClient())
	if err != nil {
		t.Fatalf("linear.New: %v", err)
	}
	return c
}

func (l *Linear) AddIssue(i LinIssue) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i.Labels = slices.Clone(i.Labels)
	l.issues = append(l.issues, &i)
}

func (l *Linear) AddComment(c LinComment) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addComment(c)
}

func (l *Linear) addComment(c LinComment) {
	if c.ID == "" {
		l.seq++
		c.ID = fmt.Sprintf("c-%d", l.seq)
	}
	l.comments = append(l.comments, c)
}

func (l *Linear) IssueState(id string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i := l.find(id); i != nil {
		return i.State
	}
	return ""
}

func (l *Linear) CommentsOn(id string) []LinComment {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []LinComment
	for _, c := range l.comments {
		if c.IssueID == id {
			out = append(out, c)
		}
	}
	return out
}

// find looks an issue up by id or identifier (Linear's issue(id:) takes both).
func (l *Linear) find(ref string) *LinIssue {
	for _, i := range l.issues {
		if i.ID == ref || i.Identifier == ref {
			return i
		}
	}
	return nil
}

func gqlError(w http.ResponseWriter, code int, m string) {
	writeJSON(w, code, map[string]any{"errors": []map[string]string{{"message": m}}})
}

func (l *Linear) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != Token {
		gqlError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		gqlError(w, http.StatusBadRequest, "bad request")
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	q := req.Query
	str := func(k string) string { s, _ := req.Variables[k].(string); return s }
	var data any
	switch {
	case strings.Contains(q, "workflowStates"):
		nodes := []map[string]string{}
		for name, typ := range linearStateType {
			nodes = append(nodes, map[string]string{"id": "st-" + name, "name": name, "type": typ})
		}
		data = map[string]any{"workflowStates": map[string]any{"nodes": nodes}}
	case strings.Contains(q, "issueUpdate"):
		i := l.find(str("id"))
		name, ok := strings.CutPrefix(str("stateId"), "st-")
		if ok = ok && i != nil; ok {
			i.State = name
		}
		data = map[string]any{"issueUpdate": map[string]bool{"success": ok}}
	case strings.Contains(q, "commentCreate"):
		i := l.find(str("issueId"))
		if i != nil {
			l.addComment(LinComment{IssueID: i.ID, Body: str("body"), Author: l.Viewer, AuthorID: "u-self", CreatedAt: time.Now().UTC()})
		}
		data = map[string]any{"commentCreate": map[string]bool{"success": i != nil}}
	case strings.Contains(q, "viewer"):
		data = map[string]any{"viewer": map[string]string{"displayName": l.Viewer}}
	case strings.Contains(q, "comments(filter"):
		since, _ := time.Parse(time.RFC3339Nano, str("since"))
		first, _ := req.Variables["first"].(float64)
		cs := slices.Clone(l.comments)
		slices.SortStableFunc(cs, func(a, b LinComment) int { return a.CreatedAt.Compare(b.CreatedAt) })
		nodes := []map[string]any{}
		for _, c := range cs {
			if !c.CreatedAt.After(since) || len(nodes) >= int(first) {
				continue
			}
			nodes = append(nodes, map[string]any{"id": c.ID, "body": c.Body, "createdAt": c.CreatedAt.UTC().Format(time.RFC3339Nano),
				"user":  map[string]string{"id": c.AuthorID, "displayName": c.Author},
				"issue": map[string]string{"identifier": l.find(c.IssueID).Identifier}, "parent": nil})
		}
		data = map[string]any{"comments": map[string]any{"nodes": nodes}}
	case strings.Contains(q, "issue(id:$id){id}"):
		if i := l.find(str("id")); i != nil {
			data = map[string]any{"issue": map[string]string{"id": i.ID}}
		} else {
			data = map[string]any{"issue": nil}
		}
	case strings.Contains(q, "issue(id:$id){comments"):
		nodes := []map[string]any{}
		for _, c := range l.comments {
			if c.IssueID == str("id") {
				nodes = append(nodes, map[string]any{"body": c.Body, "user": map[string]string{"displayName": c.Author}})
			}
		}
		data = map[string]any{"issue": map[string]any{"comments": map[string]any{"nodes": nodes}}}
	case strings.Contains(q, "issues(filter:{id:{in"):
		data = map[string]any{"issues": map[string]any{"nodes": []any{}}} // fake issues have no blockers
	case strings.Contains(q, "issues(filter:{team"):
		nodes := []map[string]any{}
		for _, i := range l.issues {
			if i.State != str("state") {
				continue
			}
			labels := []map[string]string{}
			for _, n := range i.Labels {
				labels = append(labels, map[string]string{"name": n})
			}
			nodes = append(nodes, map[string]any{"id": i.ID, "identifier": i.Identifier, "title": i.Title, "description": i.Description,
				"labels": map[string]any{"nodes": labels}, "inverseRelations": map[string]any{"nodes": []any{}}})
		}
		data = map[string]any{"issues": map[string]any{"nodes": nodes}}
	default:
		gqlError(w, http.StatusOK, "trackertest: unsupported query")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}
