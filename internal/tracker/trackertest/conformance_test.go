package trackertest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/tracker"
)

// TestTrackersShareTheInterface drives Linear and GitHub through the same
// tracker.Tracker calls Jam makes, against their httptest fakes.
func TestTrackersShareTheInterface(t *testing.T) {
	ctx := context.Background()
	for _, c := range Both(t) {
		t.Run(c.Kind, func(t *testing.T) {
			ready, err := c.Tracker.ListReady(ctx)
			if err != nil || len(ready) != 2 { // GitHub's PR #3 is never listed
				t.Fatalf("ListReady = %+v, %v; want the two open ready issues", ready, err)
			}
			i := slices.IndexFunc(ready, func(is scheduler.Issue) bool { return is.Identifier == c.Key })
			if i < 0 || !ready[i].DispatchLabeled || ready[i].Class != "impl" || ready[i].Title != "Fix the build" {
				t.Fatalf("ready = %+v; want %s dispatch-labeled, class impl", ready, c.Key)
			}
			if other := ready[1-i]; other.DispatchLabeled {
				t.Fatalf("unlabeled issue = %+v", other)
			}
			id, err := c.Tracker.IssueByIdentifier(ctx, c.Key)
			if err != nil || id != ready[i].ID {
				t.Fatalf("IssueByIdentifier(%s) = %q, %v; want %q", c.Key, id, err, ready[i].ID)
			}
			for _, step := range []struct {
				role scheduler.Role
				want string
			}{{scheduler.RoleInProgress, "in-progress"}, {scheduler.RoleInReview, "in-review"}, {scheduler.RoleDone, "done"}} {
				if err := c.Tracker.Transition(ctx, id, step.role); err != nil || c.State() != step.want {
					t.Fatalf("Transition → %s: state %q, %v", step.want, c.State(), err)
				}
			}
			at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
			c.Comment("brent", "looks good", at)
			if err := c.Tracker.PostComment(ctx, id, "on it"); err != nil || !slices.Contains(c.Posted(), "on it") {
				t.Fatalf("PostComment: posted %q, %v", c.Posted(), err)
			}
			cs, err := c.Tracker.Comments(ctx, id)
			if err != nil || !slices.Contains(cs, scheduler.Comment{Author: "brent", Body: "looks good"}) {
				t.Fatalf("Comments = %+v, %v", cs, err)
			}
			feed, err := c.Tracker.CommentFeed(ctx, at.Add(-time.Second), 100)
			if err != nil {
				t.Fatalf("CommentFeed: %v", err)
			}
			j := slices.IndexFunc(feed, func(f tracker.FeedComment) bool { return f.Body == "looks good" })
			if j < 0 || feed[j].IssueIdentifier != c.Key || feed[j].Author != "brent" || feed[j].AuthorID == "" || !feed[j].CreatedAt.Equal(at) {
				t.Fatalf("feed = %+v", feed)
			}
			if later, _ := c.Tracker.CommentFeed(ctx, at.Add(time.Second), 100); slices.ContainsFunc(later, func(f tracker.FeedComment) bool { return f.Body == "looks good" }) {
				t.Fatal("CommentFeed returned a comment created before since")
			}
			if self, err := c.Tracker.Viewer(ctx); err != nil || self != c.Self {
				t.Fatalf("Viewer = %q, %v; want %q", self, err, c.Self)
			}
		})
	}
}

// TestFeedCursorBothPaths polls each tracker's feed the way the relay does:
// through tracker.CursorFeed when the tracker offers it (GitHub), else with
// a cursor derived from the comments returned (Linear). Either way a poll
// picks up where the last left off — nothing lost across pages, nothing
// read twice beyond the inclusive boundary — and the cursor never moves back.
func TestFeedCursorBothPaths(t *testing.T) {
	ctx := context.Background()
	for _, c := range Both(t) {
		t.Run(c.Kind, func(t *testing.T) {
			cf, isCursor := c.Tracker.(tracker.CursorFeed)
			if want := c.Kind == tracker.KindGitHub; isCursor != want {
				t.Fatalf("%s implements tracker.CursorFeed = %v; want %v", c.Kind, isCursor, want)
			}
			if c.GitHub != nil {
				c.GitHub.PerPage = 1 // every comment on its own page: the cursor must span them
				// A pull request's conversation moves the cursor but never surfaces.
				c.GitHub.AddComment(GHComment{Issue: 3, Body: "on the PR", Login: "brent", UserID: 7, OnPull: true,
					CreatedAt: time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)})
			}
			base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			poll := func(since time.Time, limit int) ([]tracker.FeedComment, time.Time) {
				t.Helper()
				if isCursor {
					out, next, err := cf.CommentFeedCursor(ctx, since, limit)
					if err != nil {
						t.Fatalf("CommentFeedCursor: %v", err)
					}
					return out, next
				}
				out, err := c.Tracker.CommentFeed(ctx, since, limit)
				if err != nil {
					t.Fatalf("CommentFeed: %v", err)
				}
				next := since
				for _, f := range out {
					if f.CreatedAt.After(next) {
						next = f.CreatedAt
					}
				}
				return out, next
			}
			for k, body := range []string{"one", "two", "three"} {
				c.Comment("brent", body, base.Add(time.Duration(k+1)*time.Minute))
			}
			cursor := base
			var seen []string
			for range 5 {
				out, next := poll(cursor, 2)
				if next.Before(cursor) {
					t.Fatalf("cursor moved back: %v → %v", cursor, next)
				}
				for _, f := range out {
					if f.Body == "on the PR" {
						t.Fatalf("a pull request comment surfaced: %+v", f)
					}
					if !slices.Contains(seen, f.Body) {
						seen = append(seen, f.Body)
					}
				}
				cursor = next
			}
			if want := []string{"one", "two", "three"}; !slices.Equal(seen, want) {
				t.Fatalf("seen %q; want %q", seen, want)
			}
			// A comment after the cursor is picked up by the next poll.
			c.Comment("brent", "four", time.Now().UTC().Truncate(time.Second))
			out, next := poll(cursor, 100)
			if !slices.ContainsFunc(out, func(f tracker.FeedComment) bool { return f.Body == "four" }) || next.Before(cursor) {
				t.Fatalf("after the cursor: %+v, next %v (cursor %v)", out, next, cursor)
			}
		})
	}
}
