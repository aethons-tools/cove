// Package trackertest holds hermetic httptest fakes of the GitHub REST and
// Linear GraphQL APIs. They cover enough of each for Jam's tracker.Tracker,
// plus Both, which seeds one Case per tracker for shared-interface tests.
// Tests only.
package trackertest

import (
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/tracker"
)

// Token is the credential both fakes accept.
const Token = "trackertest-token"

// api is the GitHub API root the client is hardwired to.
const api = "https://api.github.com"

// Rewrite is an *http.Client that sends every request to srv, whatever its
// URL's host — so a client with a fixed API root reaches the fake. The
// client resolves redirects and Link URLs against the real host first, and
// this transport rewrites them in turn.
func Rewrite(srv *httptest.Server) *http.Client {
	u, _ := url.Parse(srv.URL)
	return &http.Client{Transport: rewrite{u}}
}

type rewrite struct{ to *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host, req.Host = r.to.Scheme, r.to.Host, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Case is one tracker under a shared-interface test.
type Case struct {
	Kind    string          // tracker.KindLinear | tracker.KindGitHub
	Tracker tracker.Tracker // the real client, against the fake
	Key     string          // ticket key of the seeded dispatch-labeled ready issue
	Self    string          // what Viewer returns
	State   func() string   // the seeded issue's role: "ready", "in-progress", …, "done"
	Posted  func() []string // bodies the client posted on the seeded issue
	// Comment adds a person's comment on the seeded issue, created at at.
	Comment func(author, body string, at time.Time)
	GitHub  *GitHub // set for the github case
	Linear  *Linear // set for the linear case
}

// Both seeds and returns a Linear case (team ACME, viewer "Jam") and a
// GitHub case (repo acme/board, login "jam-bot"). Each holds:
//   - a ready issue tagged dispatch:go and class:impl, "Fix the build" (Key)
//   - a ready issue with no dispatch label, "Someday"
//   - on GitHub only, a pull request #3 labelled like the first, which must
//     never surface
func Both(t testing.TB) []Case {
	t.Helper()
	lin := NewLinear(t, "ACME", "Jam")
	lin.AddIssue(LinIssue{ID: "lin-1", Identifier: "ACME-1", Title: "Fix the build", Description: "It is red.", State: "Ready", Labels: []string{"dispatch:go", "class:impl"}})
	lin.AddIssue(LinIssue{ID: "lin-2", Identifier: "ACME-2", Title: "Someday", State: "Ready"})
	gh := NewGitHub(t, "acme/board", "jam-bot")
	gh.AddIssue(GHIssue{Number: 1, Title: "Fix the build", Body: "It is red.", Labels: []string{"status:ready", "dispatch:go", "class:impl"}})
	gh.AddIssue(GHIssue{Number: 2, Title: "Someday", Labels: []string{"status:ready"}})
	gh.AddIssue(GHIssue{Number: 3, Title: "A pull request", Labels: []string{"status:ready", "dispatch:go"}, PullRequest: true})
	linRole := map[string]string{"Ready": "ready", "In Progress": "in-progress", "In Review": "in-review", "Done": "done", "Needs Input": "needs-input", "Blocked": "blocked"}
	return []Case{{
		Kind: tracker.KindLinear, Tracker: lin.Client(t), Key: "ACME-1", Self: "Jam", Linear: lin,
		State: func() string { return linRole[lin.IssueState("lin-1")] },
		Posted: func() []string {
			var out []string
			for _, c := range lin.CommentsOn("lin-1") {
				if c.Author == lin.Viewer {
					out = append(out, c.Body)
				}
			}
			return out
		},
		Comment: func(author, body string, at time.Time) {
			lin.AddComment(LinComment{IssueID: "lin-1", Body: body, Author: author, AuthorID: "u-" + author, CreatedAt: at})
		},
	}, {
		Kind: tracker.KindGitHub, Tracker: gh.Client(t), Key: "acme/board#1", Self: "jam-bot", GitHub: gh,
		State: func() string { return gh.State(1) },
		Posted: func() []string {
			var out []string
			for _, c := range gh.CommentsOn(1) {
				if c.Login == gh.Login {
					out = append(out, c.Body)
				}
			}
			return out
		},
		Comment: func(author, body string, at time.Time) {
			gh.AddComment(GHComment{Issue: 1, Body: body, Login: author, UserID: int64(crc32.ChecksumIEEE([]byte(author))), CreatedAt: at})
		},
	}}
}

// trimStatus is a GitHub status label's role ("status:in-review" → "in-review").
func trimStatus(label string) (string, bool) { return strings.CutPrefix(label, "status:") }
