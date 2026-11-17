package trackertest

import (
	"context"
	"errors"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
)

// TestPullRequestReads pins the PR watcher's reads against the fake: a merged
// pull request with who merged it, a closed one with who closed it, its
// reviews in id order across pages, and another repo's or a missing PR as
// ErrNoPullRequest.
func TestPullRequestReads(t *testing.T) {
	ctx := context.Background()
	gh := NewGitHub(t, "acme/app", "jam-bot")
	gh.PerPage = 2
	ann, bot := GHUser{Login: "ann", ID: 7}, GHUser{Login: "review-bot[bot]", ID: 9, Bot: true}
	gh.AddPull(GHPull{Number: 5, Title: "Fix it", Merged: true, MergedBy: ann, ClosedBy: ann})
	gh.AddPull(GHPull{Number: 6, Title: "Drop it", Closed: true, ClosedBy: bot})
	gh.AddPull(GHPull{Number: 8, Title: "Open"})
	for _, st := range []string{"COMMENTED", "CHANGES_REQUESTED", "APPROVED"} {
		gh.AddReview(GHReview{Pull: 5, State: st, User: bot})
	}
	c, err := githubissues.NewReader(Token, gh.HTTPClient())
	if err != nil {
		t.Fatal(err)
	}

	p, err := c.PullRequest(ctx, "acme/app", 5)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Fix it" || p.State != githubissues.PullMerged || p.By != (githubissues.User{Login: "ann", ID: 7}) || p.HTMLURL != "https://github.com/acme/app/pull/5" {
		t.Fatalf("merged PR = %+v", p)
	}
	if p, err = c.PullRequest(ctx, "acme/app", 6); err != nil || p.State != githubissues.PullClosed || p.By != (githubissues.User{Login: "review-bot[bot]", ID: 9, Bot: true}) {
		t.Fatalf("closed PR = %+v, %v", p, err)
	}
	if p, err = c.PullRequest(ctx, "acme/app", 8); err != nil || p.State != githubissues.PullOpen || p.By != (githubissues.User{}) {
		t.Fatalf("open PR = %+v, %v", p, err)
	}

	rs, err := c.PullReviews(ctx, "acme/app", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 3 || rs[0].State != "COMMENTED" || rs[2].State != "APPROVED" || rs[0].ID >= rs[1].ID || rs[1].ID >= rs[2].ID {
		t.Fatalf("reviews = %+v, want 3 in id order", rs)
	}
	if rs[2].User.Login != "review-bot[bot]" || !rs[2].User.Bot || rs[2].HTMLURL == "" {
		t.Fatalf("review = %+v", rs[2])
	}

	for _, at := range []struct {
		repo string
		n    int
	}{{"acme/app", 99}, {"other/repo", 5}} {
		if _, err := c.PullRequest(ctx, at.repo, at.n); !errors.Is(err, githubissues.ErrNoPullRequest) {
			t.Errorf("PullRequest(%s#%d) = %v, want ErrNoPullRequest", at.repo, at.n, err)
		}
	}
}
