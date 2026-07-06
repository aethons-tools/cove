package scheduler

import (
	"context"
	"os"
	"strings"
	"sync"
)

type transition struct {
	IssueID string
	Role    Role
}
type post struct {
	IssueID string
	Body    string
}

type fakeTracker struct {
	mu          sync.Mutex
	ready       []Issue
	unblockable []Issue
	comments    map[string][]Comment
	transitions []transition
	posts       []post
	failClaim   bool // Transition to RoleInProgress returns an error
}

func (f *fakeTracker) ListReady(context.Context) ([]Issue, error)       { return f.ready, nil }
func (f *fakeTracker) ListUnblockable(context.Context) ([]Issue, error) { return f.unblockable, nil }
func (f *fakeTracker) Comments(_ context.Context, id string) ([]Comment, error) {
	return f.comments[id], nil
}
func (f *fakeTracker) Transition(_ context.Context, id string, r Role) error {
	if f.failClaim && r == RoleInProgress {
		return errFake
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions = append(f.transitions, transition{id, r})
	return nil
}
func (f *fakeTracker) PostComment(_ context.Context, id, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, post{id, body})
	return nil
}
func (f *fakeTracker) roles(id string) []Role {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rs []Role
	for _, t := range f.transitions {
		if t.IssueID == id {
			rs = append(rs, t.Role)
		}
	}
	return rs
}
func (f *fakeTracker) lastPost(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.posts) - 1; i >= 0; i-- {
		if f.posts[i].IssueID == id {
			return f.posts[i].Body
		}
	}
	return ""
}

var errFake = fakeErr("fake error")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// fakeExecutor mimics a dispatch command: it optionally writes canned JSON to the
// DISPATCH_RESULT path from env, then returns runErr.
type fakeExecutor struct {
	resultJSON string
	runErr     error
	panicMsg   string        // if non-empty, Run panics (to test recovery)
	started    chan struct{} // if non-nil, closed when Run starts
	release    chan struct{} // if non-nil, Run blocks until this is closed
}

func (f *fakeExecutor) Run(ctx context.Context, _ []string, env []string) error {
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	if f.started != nil {
		select {
		case <-f.started:
		default:
			close(f.started)
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.resultJSON != "" {
		if p := envVal(env, "DISPATCH_RESULT"); p != "" {
			_ = os.WriteFile(p, []byte(f.resultJSON), 0o600)
		}
	}
	return f.runErr
}

func envVal(env []string, key string) string {
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return e[len(key)+1:]
		}
	}
	return ""
}
