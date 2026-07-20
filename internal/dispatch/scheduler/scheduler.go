// Package scheduler is the at-cove dispatch engine: it polls a tracker for ready
// autonomous work, runs each issue's configured dispatch command, and brokers the
// result back as the single writer of tracker state. It is driven by the Tracker
// and Executor interfaces so it can be tested without a network or real commands.
package scheduler

import (
	"context"
	"time"
)

// Role is a lifecycle role the engine transitions issues through. The Tracker maps
// each role to this team's configured state name and then to a tracker state id.
type Role int

const (
	RoleReady Role = iota
	RoleInProgress
	RoleInReview
	RoleNeedsInput
	RoleBlocked
	RoleDone
)

// Issue is the subset of a tracker issue the engine needs.
type Issue struct {
	ID          string // tracker-internal id (for API calls)
	Identifier  string // human key, e.g. "AET-42"
	Title       string
	Description string
	Class       string // parsed from the class label; "" if none
}

// Comment is one entry in an issue's thread.
type Comment struct {
	Author string
	Body   string
}

// InProgressIssue is an IN PROGRESS issue paired with the time it entered that
// state, so the stale-claim reaper can compute its time-in-state.
type InProgressIssue struct {
	Issue
	StartedAt time.Time
}

// Tracker is every tracker operation the engine needs. The implementation owns
// team scoping and the Role→state-id mapping.
type Tracker interface {
	// ListReady returns issues in the READY state that are dispatchable now: all
	// of their "blocks" blockers are Done (or they have none). Blocked-ness lives
	// in the issue relationships, not the state — the scheduler never promotes
	// BLOCKED→READY and never looks at the backlog (COV-65). A human moves work to
	// READY when it should be considered; a still-blocked READY issue simply waits.
	ListReady(ctx context.Context) ([]Issue, error)
	ListInProgress(ctx context.Context) ([]InProgressIssue, error)
	Comments(ctx context.Context, issueID string) ([]Comment, error)
	Transition(ctx context.Context, issueID string, role Role) error
	PostComment(ctx context.Context, issueID, body string) error
}

// Executor runs a dispatch command headlessly with the given environment. ctx
// carries the per-task timeout; Run returns nil on exit 0, else an error.
type Executor interface {
	Run(ctx context.Context, argv []string, env []string) error
}
