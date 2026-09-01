package switchboard

import (
	"context"
	"testing"
	"time"
)

// fakeDiscord returns a queued sequence of poll results and records posts.
type fakeDiscord struct {
	polls  [][]Message // one entry consumed per Poll call
	pollAt int
	posts  []Outbound
}

func (f *fakeDiscord) Poll(_ context.Context, cursors map[string]string) ([]Message, map[string]string, error) {
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

func noSleep(context.Context, time.Duration) error { return nil }
