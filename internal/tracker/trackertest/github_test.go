package trackertest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/tracker"
)

// TestGitHubSearchServesPullRequests pins that the fake's search returns
// Both's pull request #3 (as GitHub's index can), so the conformance test's
// "never listed" is the client's filter at work, not the fake's.
func TestGitHubSearchServesPullRequests(t *testing.T) {
	gh := Both(t)[1].GitHub
	q := url.Values{"q": {`repo:acme/board is:issue is:open label:"status:ready"`}}
	req, _ := http.NewRequest(http.MethodGet, api+"/search/issues?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+Token)
	resp, err := gh.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	type item struct {
		Number      int `json:"number"`
		PullRequest any `json:"pull_request"`
	}
	var out struct {
		Items []item `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(out.Items, func(it item) bool { return it.Number == 3 })
	if i < 0 || out.Items[i].PullRequest == nil {
		t.Fatalf("search items = %+v; want pull request #3 among them", out.Items)
	}
}

// Final fix 8: the fake's feed filters since by updated_at, as GitHub does,
// so an old comment edited since comes back — and the client's own
// created-before-since drop is what keeps it out of the feed.
func TestGitHubFeedFiltersSinceByUpdatedAt(t *testing.T) {
	c := Both(t)[1]
	gh := c.GitHub
	since := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	gh.AddComment(GHComment{Issue: 1, Body: "edited", Login: "brent", UserID: 7,
		CreatedAt: since.Add(-time.Hour), UpdatedAt: since.Add(time.Minute)})
	gh.AddComment(GHComment{Issue: 1, Body: "stale", Login: "brent", UserID: 7, CreatedAt: since.Add(-time.Hour)})
	q := url.Values{"since": {since.Format(time.RFC3339)}}
	req, _ := http.NewRequest(http.MethodGet, api+"/repos/acme/board/issues/comments?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+Token)
	resp, err := gh.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw []struct {
		Body      string `json:"body"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, r := range raw {
		bodies = append(bodies, r.Body)
	}
	if !slices.Equal(bodies, []string{"edited"}) || raw[0].UpdatedAt != since.Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("raw feed = %+v; want only the edited comment, its updated_at", raw)
	}
	feed, err := c.Tracker.CommentFeed(context.Background(), since, 100)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(feed, func(f tracker.FeedComment) bool { return f.Body == "edited" }) {
		t.Fatalf("the client surfaced an old comment edited since: %+v", feed)
	}
}
