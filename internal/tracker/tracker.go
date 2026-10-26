// Package tracker is Jam's one tracker contract: the methods the
// Requisitioner, ticket reports, wake-on close and the comment relay use.
// Both *linear.Client and *githubissues.Client implement it; serve builds
// one from runtime.requisitioner and hands the same value to all four.
package tracker

import (
	"context"
	"errors"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
)

// The tracker kinds — also the connection kinds and relay service names.
const (
	KindLinear = "linear"
	KindGitHub = "github"
)

// Tracker is what Jam needs from an issue tracker. Ids are the tracker's
// own (a Linear uuid, a GitHub issue number); identifiers are ticket keys
// ("AET-42", "owner/repo#12").
type Tracker interface {
	// ListReady returns READY issues whose blockers are all done (or that
	// have none). Blocked-ness lives in the issue's relations, not its
	// state: nothing promotes BLOCKED to READY, and a READY issue still
	// waiting on a blocker is simply not returned (COV-65).
	ListReady(ctx context.Context) ([]scheduler.Issue, error)
	Comments(ctx context.Context, issueID string) ([]scheduler.Comment, error)
	Transition(ctx context.Context, issueID string, role scheduler.Role) error
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	PostComment(ctx context.Context, issueID, body string) error
	// CommentFeed lists comments created after since (GitHub: at or after),
	// oldest first, at most limit — the relay's ingress.
	CommentFeed(ctx context.Context, since time.Time, limit int) ([]FeedComment, error)
	// Viewer is the identity the token posts as (the relay's self-filter).
	Viewer(ctx context.Context) (string, error)
}

// CursorFeed is the optional feed a tracker offers when the comments it
// returns can't place the next poll: GitHub reads pages of comments it then
// skips (pull request conversations, edited old ones) and stops at a page
// cap, so only the client knows how far it read. next is where the
// following poll starts; it never moves before since. The relay uses it when
// the tracker implements it (GitHub) and otherwise derives its cursor from
// the comments CommentFeed returns (Linear).
type CursorFeed interface {
	CommentFeedCursor(ctx context.Context, since time.Time, limit int) (out []FeedComment, next time.Time, err error)
}

// FeedComment is one comment from a tracker's comment feed.
type FeedComment struct {
	ID              string
	Body            string
	CreatedAt       time.Time
	Author          string // display name (Linear) or login (GitHub)
	AuthorID        string // the service's immutable user id
	AuthorBot       bool   // a bot account (GitHub user type "Bot"); never a person
	IssueIdentifier string // the ticket key
	ParentID        string // the comment replied to ("" = top level; GitHub has none)
}

// ErrNoViewer is Viewer's answer when the token has no user to name (a
// GitHub App installation token can't call GET /user). The relay then
// relies on dropping bot authors rather than treating it as an outage.
var ErrNoViewer = errors.New("tracker: the token has no viewer identity")

// RateLimitError is a tracker's refusal to answer until RetryAfter has
// passed. Callers back off rather than retry at once.
type RateLimitError struct {
	Service    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return e.Service + ": rate limited; retry after " + e.RetryAfter.String()
}
