package githubissues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/aethons-tools/cove/internal/ticketkey"
)

// Pull requests, as the PR watcher reads them (docs/modules/ROOT/pages/usage/jam/work-units.adoc#pr-watcher):
// any repo the token can read, not only the tracker's.

// ErrNoPullRequest is a pull request GitHub doesn't show the token: none
// with that number, or a repo it can't read.
var ErrNoPullRequest = errors.New("githubissues: no such pull request")

// The states of a pull request.
const (
	PullOpen   = "open"
	PullMerged = "merged"
	PullClosed = "closed"
)

// User is a GitHub account.
type User struct {
	Login string
	ID    int64
	Bot   bool // user type "Bot"
}

// PullRequest is a pull request's state: open, merged or closed, and By who
// merged or closed it (zero while open).
type PullRequest struct {
	Number  int
	Title   string
	State   string
	By      User
	HTMLURL string
}

// Review is one submitted review of a pull request (State APPROVED,
// CHANGES_REQUESTED, COMMENTED or DISMISSED; PENDING is not submitted).
type Review struct {
	ID      int64
	State   string
	User    User
	HTMLURL string
}

// NewReader builds a Client that reads pull requests only: no tracker repo.
func NewReader(token string, httpc *http.Client) (*Client, error) {
	if token == "" {
		return nil, fmt.Errorf("githubissues: empty token")
	}
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Client{http: httpc, token: token}, nil
}

type ghUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

func (u *ghUser) user() User {
	if u == nil {
		return User{}
	}
	return User{Login: u.Login, ID: u.ID, Bot: u.Type == "Bot"}
}

// getPull is getJSON with a 404 as ErrNoPullRequest.
func (c *Client) getPull(ctx context.Context, u string, out any) (string, error) {
	code, h, raw, err := c.send(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if rl := rateLimit(code, h, raw, time.Now()); rl != nil {
		return "", rl
	}
	if code == http.StatusNotFound {
		return "", fmt.Errorf("githubissues: GET %s: %w", u, ErrNoPullRequest)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("githubissues: GET %s: http %d: %s", u, code, c.bodyText(raw))
	}
	return h.Get("Link"), json.Unmarshal(raw, out)
}

// PullRequest reads pull request n of repo ("owner/name"). A closed one that
// isn't merged is read again as an issue, for who closed it.
func (c *Client) PullRequest(ctx context.Context, repo string, n int) (PullRequest, error) {
	if err := ticketkey.ValidRepo(repo); err != nil {
		return PullRequest{}, fmt.Errorf("githubissues: %w", err)
	}
	base := api + "/repos/" + repo
	var pr struct {
		Number   int     `json:"number"`
		Title    string  `json:"title"`
		State    string  `json:"state"`
		Merged   bool    `json:"merged"`
		MergedBy *ghUser `json:"merged_by"`
		HTMLURL  string  `json:"html_url"`
	}
	if _, err := c.getPull(ctx, base+"/pulls/"+strconv.Itoa(n), &pr); err != nil {
		return PullRequest{}, err
	}
	out := PullRequest{Number: pr.Number, Title: pr.Title, State: PullOpen, HTMLURL: pr.HTMLURL}
	switch {
	case pr.Merged:
		out.State, out.By = PullMerged, pr.MergedBy.user()
	case pr.State == "closed":
		var is struct {
			ClosedBy *ghUser `json:"closed_by"`
		}
		if _, err := c.getPull(ctx, base+"/issues/"+strconv.Itoa(n), &is); err != nil {
			return PullRequest{}, err
		}
		out.State, out.By = PullClosed, is.ClosedBy.user()
	}
	return out, nil
}

// PullReviews lists pull request n's reviews, oldest first, following Link
// rel="next" for at most maxFeedPages pages. A pending review is left out.
func (c *Client) PullReviews(ctx context.Context, repo string, n int) ([]Review, error) {
	if err := ticketkey.ValidRepo(repo); err != nil {
		return nil, fmt.Errorf("githubissues: %w", err)
	}
	u := api + "/repos/" + repo + "/pulls/" + strconv.Itoa(n) + "/reviews?" + url.Values{"per_page": {"100"}}.Encode()
	var out []Review
	for pages := 0; u != "" && pages < maxFeedPages; pages++ {
		var page []struct {
			ID      int64   `json:"id"`
			State   string  `json:"state"`
			User    *ghUser `json:"user"`
			HTMLURL string  `json:"html_url"`
		}
		link, err := c.getPull(ctx, u, &page)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			if r.State != "PENDING" {
				out = append(out, Review{ID: r.ID, State: r.State, User: r.User.user(), HTMLURL: r.HTMLURL})
			}
		}
		u = nextPage(link)
	}
	return out, nil
}
