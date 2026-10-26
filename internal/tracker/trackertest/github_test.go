package trackertest

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"
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
