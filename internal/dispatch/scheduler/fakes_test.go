package scheduler

import (
	"context"
	"os"
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
	inProgress  []InProgressIssue
	comments    map[string][]Comment
	transitions []transition
	posts       []post
	failClaim   bool // Transition to RoleInProgress returns an error

	lastRole    Role   // most recent role transitioned to (any issue)
	lastComment string // most recent comment body posted (any issue)
}

// newFakeTracker returns an empty fakeTracker ready to drive a single-issue test.
func newFakeTracker() *fakeTracker {
	return &fakeTracker{comments: map[string][]Comment{}}
}

func (f *fakeTracker) ListReady(context.Context) ([]Issue, error) { return f.ready, nil }
func (f *fakeTracker) ListInProgress(context.Context) ([]InProgressIssue, error) {
	return f.inProgress, nil
}
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
	f.lastRole = r
	return nil
}
func (f *fakeTracker) PostComment(_ context.Context, id, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, post{id, body})
	f.lastComment = body
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

// fakeExecutor simulates `at-cove work`: it reads the --in task file and
// writes OutJSON to the --out path. RunErr (if set) is returned after writing.
type fakeExecutor struct {
	OutJSON  string // what to write to the --out path ("" => write nothing)
	RunErr   error
	GotInput string // captured contents of the --in file
	GotArgv  []string
	GotEnv   []string // captured env passed to the work subprocess

	panicMsg string        // if non-empty, Run panics (to test recovery)
	started  chan struct{} // if non-nil, closed when Run starts
	release  chan struct{} // if non-nil, Run blocks until this is closed
}

func (f *fakeExecutor) Run(ctx context.Context, argv []string, env []string) error {
	f.GotArgv = argv
	f.GotEnv = env
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
	var inPath, outPath string
	for i := 0; i < len(argv)-1; i++ {
		switch argv[i] {
		case "--in":
			inPath = argv[i+1]
		case "--out":
			outPath = argv[i+1]
		}
	}
	if b, err := os.ReadFile(inPath); err == nil {
		f.GotInput = string(b)
	}
	if f.OutJSON != "" && outPath != "" {
		_ = os.WriteFile(outPath, []byte(f.OutJSON), 0o600)
	}
	return f.RunErr
}
