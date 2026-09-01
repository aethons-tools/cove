package switchboard

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeDiscord returns a queued sequence of poll results and records posts.
// It also records the cursors map it received on each Poll call (a copy, so
// later in-place mutation by the caller can't retroactively change what was
// observed) and can be told to fail Post with postErr.
type fakeDiscord struct {
	polls       [][]Message // one entry consumed per Poll call
	pollAt      int
	posts       []Outbound
	postErr     error               // if set, Post returns this instead of recording
	seenCursors []map[string]string // copy of cursors received on each Poll call, in order
}

func (f *fakeDiscord) Poll(_ context.Context, cursors map[string]string) ([]Message, map[string]string, error) {
	seen := map[string]string{}
	for k, v := range cursors {
		seen[k] = v
	}
	f.seenCursors = append(f.seenCursors, seen)

	var out []Message
	if f.pollAt < len(f.polls) {
		out = f.polls[f.pollAt]
	}
	f.pollAt++
	nc := map[string]string{}
	for k, v := range cursors {
		nc[k] = v
	}
	for _, m := range out {
		nc[m.Channel] = m.ID
	}
	return out, nc, nil
}
func (f *fakeDiscord) Post(_ context.Context, ch, content string) error {
	if f.postErr != nil {
		return f.postErr
	}
	f.posts = append(f.posts, Outbound{Channel: ch, Content: content})
	return nil
}

// fakeAgent returns a queued sequence of results and records the inputs it saw.
type fakeAgent struct {
	results []TurnResult
	at      int
	inputs  []string
}

func (a *fakeAgent) RunTurn(_ context.Context, input string) (TurnResult, error) {
	a.inputs = append(a.inputs, input)
	r := a.results[a.at]
	a.at++
	return r, nil
}

func TestRun_GetThenExit_PostsAndAdvancesCursor(t *testing.T) {
	d := &fakeDiscord{polls: [][]Message{
		{{ID: "10", Channel: "cx", Author: "brent", Content: "hi"}}, // first-turn poll
		{}, // after `get`
	}}
	a := &fakeAgent{results: []TurnResult{
		{Messages: []Outbound{{Channel: "cx", Content: "hello"}}, Action: ActionGet},
		{Action: ActionExit},
	}}
	if err := Run(context.Background(), Config{Sleep: noSleep}, d, a); err != nil {
		t.Fatal(err)
	}
	if len(d.posts) != 1 || d.posts[0].Content != "hello" {
		t.Fatalf("posts = %+v", d.posts)
	}
	// first turn saw the tagged message; second turn saw the empty sentinel
	if a.inputs[0] != "New Discord messages:\n[#cx] brent: hi\n" {
		t.Fatalf("turn 0 input = %q", a.inputs[0])
	}
	if a.inputs[1] != "There are no messages." {
		t.Fatalf("turn 1 input = %q", a.inputs[1])
	}
}

func TestRun_WaitBlocksUntilMessage(t *testing.T) {
	d := &fakeDiscord{polls: [][]Message{
		{}, // first-turn poll: empty
		{}, // wait poll 1: empty
		{{ID: "5", Channel: "cx", Author: "sam", Content: "yo"}}, // wait poll 2: arrives
	}}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionWait}, // first turn (empty inbox) → wait
		{Action: ActionExit}, // after a message arrives
	}}
	sleeps := 0
	cfg := Config{Sleep: func(context.Context, time.Duration) error { sleeps++; return nil }}
	if err := Run(context.Background(), cfg, d, a); err != nil {
		t.Fatal(err)
	}
	if sleeps != 1 { // slept once between the two wait-polls
		t.Fatalf("sleeps = %d, want 1", sleeps)
	}
	if a.inputs[1] != "New Discord messages:\n[#cx] sam: yo\n" {
		t.Fatalf("turn after wait = %q", a.inputs[1])
	}
}

// TestRun_CursorsThreadBetweenPolls guards against the loop resetting or
// dropping cursors between polls: a regression here would silently re-deliver
// already-seen Discord messages in production. It asserts the SECOND Poll call
// receives the cursor advanced by the first call's returned message, not an
// empty/reset map.
func TestRun_CursorsThreadBetweenPolls(t *testing.T) {
	d := &fakeDiscord{polls: [][]Message{
		{{ID: "42", Channel: "cx", Author: "brent", Content: "hi"}}, // first-turn poll
		{}, // after `get`
	}}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionGet},
		{Action: ActionExit},
	}}
	if err := Run(context.Background(), Config{Sleep: noSleep}, d, a); err != nil {
		t.Fatal(err)
	}
	if len(d.seenCursors) != 2 {
		t.Fatalf("Poll called %d times, want 2; seen = %+v", len(d.seenCursors), d.seenCursors)
	}
	if got := d.seenCursors[1]["cx"]; got != "42" {
		t.Fatalf("second Poll's cursors[\"cx\"] = %q, want %q (threaded from the first poll's message)", got, "42")
	}
}

// TestRun_PostErrorAborts guards against the loop silently swallowing a Post
// failure and pressing on: a regression here would act on the agent's
// instruction (poll again, run another turn) as if the reply had actually
// been delivered. It asserts Run surfaces the Post error and never calls the
// agent a second time.
func TestRun_PostErrorAborts(t *testing.T) {
	wantErr := errors.New("discord: 429 rate limited")
	d := &fakeDiscord{
		polls:   [][]Message{{{ID: "1", Channel: "cx", Author: "brent", Content: "hi"}}},
		postErr: wantErr,
	}
	a := &fakeAgent{results: []TurnResult{
		{Messages: []Outbound{{Channel: "cx", Content: "hello"}}, Action: ActionGet},
		{Action: ActionExit}, // must never be reached
	}}
	err := Run(context.Background(), Config{Sleep: noSleep}, d, a)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want wrapping %v", err, wantErr)
	}
	if len(a.inputs) != 1 {
		t.Fatalf("agent saw %d inputs, want 1 (loop must abort on Post error before acting on the action)", len(a.inputs))
	}
}

// TestRun_CtxCancelDuringWait guards against the wait path spinning forever
// (or ignoring cancellation) once the caller's context is cancelled. It
// simulates cancellation happening during the wait backoff by having the
// Config.Sleep stub cancel the context and return its error, and asserts Run
// returns promptly with a cancellation error.
func TestRun_CtxCancelDuringWait(t *testing.T) {
	d := &fakeDiscord{polls: [][]Message{
		{}, // first-turn poll: empty -> wait
		{}, // wait poll 1: empty
	}}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionWait}, // first turn (empty inbox) -> wait
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := Config{Sleep: func(sctx context.Context, _ time.Duration) error {
		cancel()
		return sctx.Err()
	}}
	err := Run(ctx, cfg, d, a)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func noSleep(context.Context, time.Duration) error { return nil }
