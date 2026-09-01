package switchboard

import (
	"context"
	"errors"
	"strings"
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
	pollErrs    []error // indexed per Poll call; nil (or short) entries mean no error
	posts       []Outbound
	postErr     error               // if set, Post returns this instead of recording
	seenCursors []map[string]string // copy of cursors received on each Poll call, in order
	seed        map[string]string   // cursors returned by Seed
	seeded      bool                // set true once Seed is called
}

// Seed records that it was called and returns a copy of the configured seed
// cursors (mirrors RESTClient.Seed's cold-start behavior). When a test wires
// up pollErrs (to exercise poll-retry behavior), Seed reserves index 0 of
// the shared pollErrs/polls sequence for itself, so pollErrs/polls entries
// can be authored in call order starting with the seed step ("seed-batch
// ok, next poll errors, then ok") without every other, pollErrs-less test
// having to pad its polls slice with a throwaway leading entry.
func (f *fakeDiscord) Seed(_ context.Context) (map[string]string, error) {
	f.seeded = true
	if len(f.pollErrs) > 0 {
		f.pollAt++
	}
	seed := map[string]string{}
	for k, v := range f.seed {
		seed[k] = v
	}
	return seed, nil
}

func (f *fakeDiscord) Poll(_ context.Context, cursors map[string]string) ([]Message, map[string]string, error) {
	idx := f.pollAt
	f.pollAt++

	seen := map[string]string{}
	for k, v := range cursors {
		seen[k] = v
	}
	f.seenCursors = append(f.seenCursors, seen)

	if idx < len(f.pollErrs) && f.pollErrs[idx] != nil {
		return nil, nil, f.pollErrs[idx]
	}

	var out []Message
	if idx < len(f.polls) {
		out = f.polls[idx]
	}
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

// fakeAgentFunc is a call-indexed agent: fn is given the zero-based turn
// number and the rendered input, and decides the result (including whether
// to error), letting a test script a specific turn (e.g. "the first turn
// errors, every later turn exits").
type fakeAgentFunc struct {
	fn     func(i int, input string) (TurnResult, error)
	at     int
	inputs []string
}

func (a *fakeAgentFunc) RunTurn(_ context.Context, input string) (TurnResult, error) {
	i := a.at
	a.at++
	a.inputs = append(a.inputs, input)
	return a.fn(i, input)
}

// errorString is a minimal error type for tests that need a plain,
// comparable-by-message error without pulling in errors.New's identity
// semantics.
type errorString string

func (e errorString) Error() string { return string(e) }

// contains reports whether s contains substr (a thin, test-local alias for
// strings.Contains so assertions read plainly).
func contains(s, substr string) bool { return strings.Contains(s, substr) }

// errorsIsCanceled reports whether err is (or wraps) a context cancellation
// or deadline signal — the one non-recoverable case the fail-soft loop must
// still surface promptly.
func errorsIsCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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

// TestRun_PostErrorRecovers supersedes the old (fail-fast) A1 expectation
// that a Post error aborts the loop: fail-soft means Run must now recover
// from a Post error instead of returning it. It still guards the property
// the fail-fast test protected — a failed delivery must never be treated as
// delivered — by asserting the loop does NOT act on the failed turn's
// action (it never consumes the poll batch tied to that action) and instead
// re-enters with an empty inbox on the next turn.
func TestRun_PostErrorRecovers(t *testing.T) {
	postErr := errors.New("discord: 429 rate limited")
	d := &fakeDiscord{
		postErr: postErr,
		polls: [][]Message{
			{{ID: "1", Channel: "cx", Author: "sam", Content: "hi"}}, // must NOT be consumed: acting on ActionGet would poll this
		},
	}
	a := &fakeAgentFunc{fn: func(i int, _ string) (TurnResult, error) {
		if i == 0 {
			// First turn (empty inbox, cold start): posts a reply and asks to
			// `get`. The post fails.
			return TurnResult{Messages: []Outbound{{Channel: "cx", Content: "hello"}}, Action: ActionGet}, nil
		}
		return TurnResult{Action: ActionExit}, nil
	}}
	var logged []string
	cfg := Config{Sleep: noSleep, Log: func(s string) { logged = append(logged, s) }}
	if err := Run(context.Background(), cfg, d, a); err != nil {
		t.Fatalf("loop should recover from a post error, not return: %v", err)
	}
	if len(a.inputs) != 2 {
		t.Fatalf("agent saw %d inputs, want 2 (loop must recover and re-enter, not abort)", len(a.inputs))
	}
	if a.inputs[1] != "There are no messages." {
		t.Fatalf("failed post must not be treated as delivered: turn 1 input = %q, want the empty inbox, not the get poll's message", a.inputs[1])
	}
	if len(logged) == 0 {
		t.Fatal("post failure not logged")
	}
}

func TestRun_TurnErrorPostsNoticeAndContinues(t *testing.T) {
	d := &fakeDiscord{seed: map[string]string{}, polls: [][]Message{{}, {}}}
	a := &fakeAgentFunc{fn: func(i int, _ string) (TurnResult, error) {
		switch i {
		case 0:
			return TurnResult{}, errorString("agent blew up")
		default:
			return TurnResult{Action: ActionExit}, nil
		}
	}}
	var logged []string
	cfg := Config{Sleep: noSleep, ErrorChannel: "ops", Log: func(s string) { logged = append(logged, s) }}
	if err := Run(context.Background(), cfg, d, a); err != nil {
		t.Fatalf("loop should recover, not return: %v", err)
	}
	if len(d.posts) != 1 || d.posts[0].Channel != "ops" || !contains(d.posts[0].Content, "error") {
		t.Fatalf("turn failure not announced to ErrorChannel: %+v", d.posts)
	}
	if len(logged) == 0 {
		t.Fatal("turn failure not logged")
	}
}

func TestRun_PollErrorRetriesNotDies(t *testing.T) {
	d := &fakeDiscord{
		seed:     map[string]string{},
		pollErrs: []error{nil, errorString("discord 503"), nil}, // seed-batch ok, next poll errors, then ok
		polls:    [][]Message{{}, {}, {{ID: "9", Channel: "cx", Author: "sam", Content: "hi"}}},
	}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionGet}, // triggers a poll that will error once then recover
		{Action: ActionExit},
	}}
	cfg := Config{Sleep: noSleep}
	if err := Run(context.Background(), cfg, d, a); err != nil {
		t.Fatalf("poll error should be retried, not fatal: %v", err)
	}
	if a.inputs[1] != "New Discord messages:\n[#cx] sam: hi\n" {
		t.Fatalf("did not recover the message after retry: %q", a.inputs[1])
	}
}

func TestRun_CtxCancelStillReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &fakeDiscord{seed: map[string]string{}, polls: [][]Message{{}}}
	a := &fakeAgent{results: []TurnResult{{Action: ActionWait}}}
	cfg := Config{Sleep: func(context.Context, time.Duration) error { cancel(); return ctx.Err() }}
	err := Run(ctx, cfg, d, a)
	if !errorsIsCanceled(err) {
		t.Fatalf("ctx cancel should return a cancellation error, got %v", err)
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
