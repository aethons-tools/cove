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
	seed        map[string]string   // cursors returned by Seed
	seeded      bool                // set true once Seed is called
}

// Seed records that it was called and returns a copy of the configured seed
// cursors (mirrors RESTClient.Seed's cold-start behavior).
func (f *fakeDiscord) Seed(_ context.Context) (map[string]string, error) {
	f.seeded = true
	seed := map[string]string{}
	for k, v := range f.seed {
		seed[k] = v
	}
	return seed, nil
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
		{{ID: "10", Channel: "cx", Author: "brent", Content: "hi"}}, // first `get` poll (post-seed)
		{}, // second `get` poll
	}}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionGet}, // first turn: empty inbox (seeded, no history) -> get
		{Messages: []Outbound{{Channel: "cx", Content: "hello"}}, Action: ActionGet},
		{Action: ActionExit},
	}}
	if err := Run(context.Background(), Config{Sleep: noSleep}, d, a); err != nil {
		t.Fatal(err)
	}
	if len(d.posts) != 1 || d.posts[0].Content != "hello" {
		t.Fatalf("posts = %+v", d.posts)
	}
	// first turn is empty (cold start); second turn saw the tagged message;
	// third turn saw the empty sentinel again.
	if a.inputs[0] != "There are no messages." {
		t.Fatalf("turn 0 input = %q", a.inputs[0])
	}
	if a.inputs[1] != "New Discord messages:\n[#cx] brent: hi\n" {
		t.Fatalf("turn 1 input = %q", a.inputs[1])
	}
	if a.inputs[2] != "There are no messages." {
		t.Fatalf("turn 2 input = %q", a.inputs[2])
	}
}

func TestRun_WaitBlocksUntilMessage(t *testing.T) {
	d := &fakeDiscord{polls: [][]Message{
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
		{{ID: "42", Channel: "cx", Author: "brent", Content: "hi"}}, // first `get` poll (post-seed)
		{}, // second `get` poll
	}}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionGet}, // first turn: empty inbox (seeded) -> get
		{Action: ActionGet}, // second turn: sees the message -> get again
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
	// No polls needed: the first turn starts empty (seeded, cold start), and the
	// agent's Post fails before any Poll call would happen.
	d := &fakeDiscord{postErr: wantErr}
	a := &fakeAgent{results: []TurnResult{
		{Messages: []Outbound{{Channel: "cx", Content: "hello"}}, Action: ActionGet}, // first turn (empty inbox) posts anyway
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
		{}, // wait poll 1: empty
	}}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionWait}, // first turn (empty inbox, seeded) -> wait
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

// TestRun_SeedsCursorsAndDoesNotDeliverHistory guards the cold-start behavior:
// a fresh conductor must not replay existing channel history as "new" on its
// first turn. It asserts Seed is called, the first turn's inbox is empty, and
// the first post-seed poll carries the seeded cursor (not a reset/empty map).
func TestRun_SeedsCursorsAndDoesNotDeliverHistory(t *testing.T) {
	d := &fakeDiscord{
		seed: map[string]string{"cx": "100"}, // channel already has history up to id 100
		polls: [][]Message{
			{}, // first post-seed poll (inside wait): nothing new yet
			{{ID: "101", Channel: "cx", Author: "sam", Content: "yo"}}, // then a new message arrives, unblocking wait
		},
	}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionWait}, // first turn MUST see empty inbox (no history)
		{Action: ActionExit}, // runs once the new message unblocks wait
	}}
	if err := Run(context.Background(), Config{Sleep: noSleep}, d, a); err != nil {
		t.Fatal(err)
	}
	if a.inputs[0] != "There are no messages." {
		t.Fatalf("first turn should be empty (no history flood); got %q", a.inputs[0])
	}
	if !d.seeded {
		t.Fatal("Seed was not called")
	}
	// the first post-seed poll must carry the seeded cursor, not an empty map
	if len(d.seenCursors) == 0 || d.seenCursors[0]["cx"] != "100" {
		t.Fatalf("post-seed poll did not use seeded cursor: %+v", d.seenCursors)
	}
}

func noSleep(context.Context, time.Duration) error { return nil }
