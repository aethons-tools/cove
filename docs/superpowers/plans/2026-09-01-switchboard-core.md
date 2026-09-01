# Switchboard Core (Component A1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the hermetic core of the Discord "conductor" — a new `at-switchboard` binary whose supervisor loop polls Discord, hands a batched channel/author-tagged inbox to a headless `claude` turn, posts the agent's replies, and obeys an agent-returned `exit`/`wait`/`get` action.

**Architecture:** A new `internal/switchboard` package with two injected interfaces — `Discord` (poll/post) and `Agent` (run one turn) — plus a pure supervisor loop and pure render/parse helpers, all hermetically testable with fakes. Two real adapters live behind those interfaces: a Discord REST client (tested with `httptest`) and a `claude`-shelling agent (tested with `runner.Fake` + a pre-staged result file). `cmd/at-switchboard/main.go` wires them from env. This plan is **A1**; product integration (`at-cove teammate` launch, teammate config class, `discord.com` egress, image embed, user docs) is **A2**, a separate PR.

**Tech Stack:** Go stdlib (`net/http`, `net/http/httptest`, `encoding/json`, `context`, `time`), the repo's `internal/cli` registry and `internal/runner.Runner`.

## Global Constraints

- Module `github.com/aethons-tools/cove`. New binary `cmd/at-switchboard`; new package `internal/switchboard`. Do NOT touch `cmd/at-task`, the image embed pipeline, `internal/kit/config.go`, or egress — those are A2.
- Tests are **hermetic**: the loop is tested with fake `Discord`/`Agent`; the REST adapter with `httptest`; the claude adapter with `runner.Fake` + a pre-written result file. No real network, no real `claude`, no Docker/VM. Any real Discord or real-`claude` round-trip goes behind the `//go:build integration` tag.
- **The bot token is never logged and never placed on argv.** The REST adapter sends it only as the `Authorization: Bot <token>` HTTP header; diagnostics must never include it.
- **Agent-returns-JSON-file contract** (mirrors the existing `worker-result.json` protocol in `internal/dispatchrun`): the agent writes its `{messages, action}` to a known result file; the conductor reads and parses it. The conductor never parses `claude` stdout for structure.
- **Session continuity risk (design-isolated):** there is no in-repo precedent for headless `claude -p` session continuity (`--resume` is unused; `--continue` is used only interactively). The real agent adapter uses `claude -p --continue`; this is the ONE externally-dependent assumption. It is isolated behind the `Agent` interface, the exact argv is asserted by a unit test, and real behavior is verified only under the `integration` tag. If `-p --continue` proves not to carry session state, the fallback (documented in Task 4) is the conductor threading a rolling transcript into each turn's prompt — a change confined to the adapter, not the loop.
- Follow TDD (failing test first), the plan/execute split (pure loop + render/parse; execution behind interfaces), DRY, YAGNI, frequent commits.
- Per AGENTS.md: docs updated in the same change (A1's doc footprint is the binaries list + an architecture note; the user-facing `at-cove teammate` guide is A2).

---

### Task 1: Types + pure render/parse (`message.go`, `render.go`)

**Files:**
- Create: `internal/switchboard/message.go`
- Create: `internal/switchboard/render.go`
- Test: `internal/switchboard/render_test.go`

**Interfaces:**
- Consumes: nothing (leaf).
- Produces:
  - `type Action string` with `ActionExit`/`ActionWait`/`ActionGet` = `"exit"`/`"wait"`/`"get"`.
  - `type Message struct { ID, Channel, Author, Content string }`.
  - `type Outbound struct { Channel string \`json:"channel"\`; Content string \`json:"content"\` }`.
  - `type TurnResult struct { Messages []Outbound \`json:"messages"\`; Action Action \`json:"action"\` }`.
  - `func RenderInbox(batch []Message) string`.
  - `func ParseTurnResult(data []byte) (TurnResult, error)`.

- [ ] **Step 1: Write the failing test**

```go
package switchboard

import "testing"

func TestRenderInbox(t *testing.T) {
	if got := RenderInbox(nil); got != "There are no messages." {
		t.Fatalf("empty batch = %q", got)
	}
	got := RenderInbox([]Message{
		{ID: "1", Channel: "project-x", Author: "brent", Content: "fix the flaky test"},
		{ID: "2", Channel: "project-x", Author: "sam", Content: "which one?"},
	})
	want := "New Discord messages:\n[#project-x] brent: fix the flaky test\n[#project-x] sam: which one?\n"
	if got != want {
		t.Fatalf("RenderInbox:\n got: %q\nwant: %q", got, want)
	}
}

func TestParseTurnResult(t *testing.T) {
	r, err := ParseTurnResult([]byte(`{"messages":[{"channel":"project-x","content":"on it"}],"action":"wait"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != ActionWait || len(r.Messages) != 1 || r.Messages[0].Content != "on it" {
		t.Fatalf("parsed = %+v", r)
	}
	if _, err := ParseTurnResult([]byte(`{"action":"frobnicate"}`)); err == nil {
		t.Fatal("expected error for invalid action")
	}
	if _, err := ParseTurnResult([]byte(`not json`)); err == nil {
		t.Fatal("expected error for bad json")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/switchboard/`
Expected: FAIL — undefined `RenderInbox`/`ParseTurnResult`/types.

- [ ] **Step 3: Write minimal implementation**

`internal/switchboard/message.go`:
```go
// Package switchboard is the in-sandbox Discord conductor: it polls Discord,
// hands a batched, channel/author-tagged inbox to a headless claude turn, posts
// the agent's replies, and obeys an agent-returned exit/wait/get action.
package switchboard

// Action is the agent's per-turn instruction to the conductor loop.
type Action string

const (
	ActionExit Action = "exit" // stop the loop
	ActionWait Action = "wait" // poll until a message arrives, then re-enter
	ActionGet  Action = "get"  // re-enter immediately (empty inbox is fine)
)

// Message is one inbound Discord message. ID is the Discord snowflake, used as
// the per-channel poll cursor.
type Message struct {
	ID      string
	Channel string
	Author  string
	Content string
}

// Outbound is one message the agent asked the conductor to post.
type Outbound struct {
	Channel string `json:"channel"`
	Content string `json:"content"`
}

// TurnResult is the agent's per-turn output, parsed from its JSON result file.
type TurnResult struct {
	Messages []Outbound `json:"messages"`
	Action   Action     `json:"action"`
}
```

`internal/switchboard/render.go`:
```go
package switchboard

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenderInbox renders a batch into the agent's turn input, each message tagged
// with its channel and author so the agent can disambiguate speakers. An empty
// batch renders the sentinel the agent sees after a `get` with nothing waiting.
func RenderInbox(batch []Message) string {
	if len(batch) == 0 {
		return "There are no messages."
	}
	var b strings.Builder
	b.WriteString("New Discord messages:\n")
	for _, m := range batch {
		fmt.Fprintf(&b, "[#%s] %s: %s\n", m.Channel, m.Author, m.Content)
	}
	return b.String()
}

// ParseTurnResult parses the agent's JSON result-file contents and validates the
// action. Unknown/missing actions are an error (the loop must never guess).
func ParseTurnResult(data []byte) (TurnResult, error) {
	var r TurnResult
	if err := json.Unmarshal(data, &r); err != nil {
		return TurnResult{}, fmt.Errorf("switchboard: bad turn result: %w", err)
	}
	switch r.Action {
	case ActionExit, ActionWait, ActionGet:
		return r, nil
	default:
		return TurnResult{}, fmt.Errorf("switchboard: invalid action %q (want exit|wait|get)", r.Action)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/switchboard/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/switchboard/message.go internal/switchboard/render.go internal/switchboard/render_test.go
git commit -m "feat(switchboard): message types + pure inbox render / result parse"
```

---

### Task 2: The supervisor loop (`loop.go`)

**Files:**
- Create: `internal/switchboard/loop.go`
- Test: `internal/switchboard/loop_test.go`

**Interfaces:**
- Consumes: `Message`, `TurnResult`, `Action*`, `RenderInbox` (Task 1).
- Produces:
  - `type Discord interface { Poll(ctx context.Context, cursors map[string]string) (msgs []Message, newCursors map[string]string, err error); Post(ctx context.Context, channel, content string) error }`.
  - `type Agent interface { RunTurn(ctx context.Context, input string) (TurnResult, error) }`.
  - `type Config struct { PollInterval time.Duration; Sleep func(context.Context, time.Duration) error }`.
  - `func Run(ctx context.Context, cfg Config, d Discord, a Agent) error`.

**Design notes for the implementer:** `Run` computes the batch for each turn from the *previous* action — `get` polls once (may be empty), `wait` blocks (poll with `cfg.Sleep` backoff) until non-empty, `exit` returns. The first turn polls once for anything already waiting. `cfg.Sleep` defaults to a ctx-aware sleep when nil, and is stubbed in tests so `wait` never really sleeps.

- [ ] **Step 1: Write the failing test**

```go
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
		{},                                                          // after `get`
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
		{},                                                          // first-turn poll: empty
		{},                                                          // wait poll 1: empty
		{{ID: "5", Channel: "cx", Author: "sam", Content: "yo"}},    // wait poll 2: arrives
	}}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionWait},  // first turn (empty inbox) → wait
		{Action: ActionExit},  // after a message arrives
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/switchboard/ -run TestRun`
Expected: FAIL — undefined `Run`/`Discord`/`Agent`/`Config`.

- [ ] **Step 3: Write minimal implementation**

```go
package switchboard

import (
	"context"
	"fmt"
	"time"
)

// Discord is the inbound/outbound Discord transport (REST adapter or a fake).
type Discord interface {
	// Poll returns new messages across the watched channels since the per-channel
	// cursors, plus the advanced cursors (last message id seen per channel).
	Poll(ctx context.Context, cursors map[string]string) (msgs []Message, newCursors map[string]string, err error)
	// Post sends content to a channel.
	Post(ctx context.Context, channel, content string) error
}

// Agent runs exactly one turn: given the rendered inbox, returns the agent's result.
type Agent interface {
	RunTurn(ctx context.Context, input string) (TurnResult, error)
}

// Config tunes the loop. Sleep defaults to a ctx-aware sleep when nil; tests stub it.
type Config struct {
	PollInterval time.Duration
	Sleep        func(context.Context, time.Duration) error
}

func (c Config) sleep(ctx context.Context) error {
	d := c.PollInterval
	if d <= 0 {
		d = 2 * time.Second
	}
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run drives the supervisor loop until the agent returns ActionExit, ctx is
// cancelled, or an adapter errors. The batch for each turn is produced by the
// PREVIOUS action: the first turn polls once; `get` polls once (empty ok);
// `wait` blocks until a poll returns something.
func Run(ctx context.Context, cfg Config, d Discord, a Agent) error {
	cursors := map[string]string{}
	batch, cursors, err := d.Poll(ctx, cursors)
	if err != nil {
		return fmt.Errorf("switchboard: initial poll: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := a.RunTurn(ctx, RenderInbox(batch))
		if err != nil {
			return fmt.Errorf("switchboard: run turn: %w", err)
		}
		for _, m := range res.Messages {
			if err := d.Post(ctx, m.Channel, m.Content); err != nil {
				return fmt.Errorf("switchboard: post to %s: %w", m.Channel, err)
			}
		}
		switch res.Action {
		case ActionExit:
			return nil
		case ActionGet:
			if batch, cursors, err = d.Poll(ctx, cursors); err != nil {
				return fmt.Errorf("switchboard: get poll: %w", err)
			}
		case ActionWait:
			if batch, cursors, err = waitForMessages(ctx, cfg, d, cursors); err != nil {
				return err
			}
		default:
			return fmt.Errorf("switchboard: unhandled action %q", res.Action)
		}
	}
}

// waitForMessages polls until a non-empty batch arrives, sleeping cfg between
// empty polls.
func waitForMessages(ctx context.Context, cfg Config, d Discord, cursors map[string]string) ([]Message, map[string]string, error) {
	for {
		batch, nc, err := d.Poll(ctx, cursors)
		if err != nil {
			return nil, nil, fmt.Errorf("switchboard: wait poll: %w", err)
		}
		cursors = nc
		if len(batch) > 0 {
			return batch, cursors, nil
		}
		if err := cfg.sleep(ctx); err != nil {
			return nil, nil, err
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/switchboard/`
Expected: PASS (Task 1 + Task 2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/switchboard/loop.go internal/switchboard/loop_test.go
git commit -m "feat(switchboard): supervisor loop with exit/wait/get action handling"
```

---

### Task 3: Discord REST adapter (`discord.go`)

**Files:**
- Create: `internal/switchboard/discord.go`
- Test: `internal/switchboard/discord_test.go`

**Interfaces:**
- Consumes: `Message`, the `Discord` interface (Task 2).
- Produces: `func NewRESTClient(token string, channels []string, opts ...RESTOption) *RESTClient` implementing `Discord`; `func WithBaseURL(u string) RESTOption`; `func WithHTTPClient(c *http.Client) RESTOption`.

**Design notes:** Discord's `GET /channels/{id}/messages?after=<cursor>&limit=100` returns newest-first; the adapter reverses to chronological and advances the cursor to the newest id. The author display name is `global_name` if set, else `username`. Auth is `Authorization: Bot <token>` — never logged, never on argv. Base URL defaults to `https://discord.com/api/v10`, overridable for `httptest`.

- [ ] **Step 1: Write the failing test**

```go
package switchboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRESTClient_PollReversesAndAdvancesCursor(t *testing.T) {
	var gotAuth, gotAfter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAfter = r.URL.Query().Get("after")
		// Discord returns newest-first
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "20", "content": "second", "author": map[string]any{"username": "u", "global_name": "Sam"}},
			{"id": "10", "content": "first", "author": map[string]any{"username": "u", "global_name": "Sam"}},
		})
	}))
	defer srv.Close()

	c := NewRESTClient("secrettoken", []string{"chan1"}, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	msgs, cursors, err := c.Poll(context.Background(), map[string]string{"chan1": "5"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bot secrettoken" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotAfter != "5" {
		t.Fatalf("after param = %q", gotAfter)
	}
	if len(msgs) != 2 || msgs[0].Content != "first" || msgs[1].Content != "second" {
		t.Fatalf("messages not chronological: %+v", msgs)
	}
	if msgs[0].Author != "Sam" || msgs[0].Channel != "chan1" {
		t.Fatalf("msg fields = %+v", msgs[0])
	}
	if cursors["chan1"] != "20" {
		t.Fatalf("cursor = %q, want 20", cursors["chan1"])
	}
}

func TestRESTClient_Post(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/channels/chan1/messages") || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewRESTClient("tok", []string{"chan1"}, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	if err := c.Post(context.Background(), "chan1", "hello world"); err != nil {
		t.Fatal(err)
	}
	if body["content"] != "hello world" {
		t.Fatalf("posted body = %+v", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/switchboard/ -run TestRESTClient`
Expected: FAIL — undefined `NewRESTClient`/`WithBaseURL`/`WithHTTPClient`.

- [ ] **Step 3: Write minimal implementation**

```go
package switchboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// RESTClient is the real Discord REST adapter. The bot token is sent only as the
// Authorization header — never logged, never on argv.
type RESTClient struct {
	token    string
	channels []string
	baseURL  string
	http     *http.Client
}

// RESTOption configures a RESTClient.
type RESTOption func(*RESTClient)

// WithBaseURL overrides the Discord API base (for tests). Default: v10 API.
func WithBaseURL(u string) RESTOption { return func(c *RESTClient) { c.baseURL = u } }

// WithHTTPClient overrides the HTTP client (for tests).
func WithHTTPClient(h *http.Client) RESTOption { return func(c *RESTClient) { c.http = h } }

// NewRESTClient builds a Discord REST adapter for the given watched channels.
func NewRESTClient(token string, channels []string, opts ...RESTOption) *RESTClient {
	c := &RESTClient{token: token, channels: channels, baseURL: "https://discord.com/api/v10", http: http.DefaultClient}
	for _, o := range opts {
		o(c)
	}
	return c
}

type discordMessage struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Author  struct {
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
	} `json:"author"`
}

func (c *RESTClient) Poll(ctx context.Context, cursors map[string]string) ([]Message, map[string]string, error) {
	nc := map[string]string{}
	for k, v := range cursors {
		nc[k] = v
	}
	var out []Message
	for _, ch := range c.channels {
		u := fmt.Sprintf("%s/channels/%s/messages?limit=100", c.baseURL, ch)
		if after := cursors[ch]; after != "" {
			u += "&after=" + url.QueryEscape(after)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bot "+c.token)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("switchboard: poll %s: %w", ch, err)
		}
		var page []discordMessage
		derr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, nil, fmt.Errorf("switchboard: poll %s: status %d", ch, resp.StatusCode)
		}
		if derr != nil {
			return nil, nil, fmt.Errorf("switchboard: poll %s decode: %w", ch, derr)
		}
		// Discord returns newest-first; walk in reverse for chronological order.
		for i := len(page) - 1; i >= 0; i-- {
			m := page[i]
			author := m.Author.GlobalName
			if author == "" {
				author = m.Author.Username
			}
			out = append(out, Message{ID: m.ID, Channel: ch, Author: author, Content: m.Content})
			nc[ch] = m.ID
		}
	}
	return out, nc, nil
}

func (c *RESTClient) Post(ctx context.Context, channel, content string) error {
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/channels/%s/messages", c.baseURL, channel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("switchboard: post %s: %w", channel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("switchboard: post %s: status %d", channel, resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/switchboard/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/switchboard/discord.go internal/switchboard/discord_test.go
git commit -m "feat(switchboard): Discord REST poll/post adapter (httptest-covered)"
```

---

### Task 4: Claude agent adapter (`agent.go`)

**Files:**
- Create: `internal/switchboard/agent.go`
- Test: `internal/switchboard/agent_test.go`

**Interfaces:**
- Consumes: `TurnResult`, `ParseTurnResult` (Task 1), `Agent` (Task 2), `internal/runner.Runner`.
- Produces: `func NewClaudeAgent(r runner.Runner, workDir string) *ClaudeAgent` implementing `Agent`.

**Design notes:** `RunTurn` (1) writes the turn input to `<workDir>/.switchboard/turn-input.txt`, (2) truncates `<workDir>/.switchboard/turn-result.json`, (3) runs the claude command, (4) reads and `ParseTurnResult`s the result file. The command wraps the protocol instruction around the input and is `claude -p --continue` (the session-continuity assumption from Global Constraints — asserted here by argv, verified for real under the `integration` tag). The bot token is NOT involved here (it lives in the conductor/adapter, never the agent). Use `runner.Runner` (not `os/exec`) so it's hermetic. Check `internal/runner` for the exact method names/signatures (`Run`/`Output`/`RunStdin`) and the `Fake` API before writing — mirror how `internal/dispatchrun` calls the runner.

- [ ] **Step 1: Write the failing test**

```go
package switchboard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestClaudeAgent_RunTurn_WritesInput_RunsClaude_ParsesResult(t *testing.T) {
	dir := t.TempDir()
	// The fake "claude" run writes the result file the agent will read back,
	// standing in for what the real agent process does.
	resultPath := filepath.Join(dir, ".switchboard", "turn-result.json")
	fake := &runner.Fake{}
	// Configure the fake so the claude invocation is a no-op success AND writes
	// the canned result (see internal/runner Fake API for the exact hook; if the
	// Fake cannot run side effects, pre-write the file here before RunTurn and
	// make the claude command a plain success).
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(resultPath, []byte(`{"messages":[{"channel":"cx","content":"done"}],"action":"wait"}`), 0o644)

	a := NewClaudeAgent(fake, dir)
	res, err := a.RunTurn(context.Background(), "New Discord messages:\n[#cx] brent: hi\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionWait || len(res.Messages) != 1 || res.Messages[0].Content != "done" {
		t.Fatalf("res = %+v", res)
	}
	// input file was written
	in, _ := os.ReadFile(filepath.Join(dir, ".switchboard", "turn-input.txt"))
	if string(in) == "" {
		t.Fatal("turn input not written")
	}
	// claude was invoked with -p --continue
	cmds := fake.Commands() // adjust to the real Fake accessor
	if len(cmds) == 0 || !containsArgs(cmds, "claude", "-p", "--continue") {
		t.Fatalf("claude not invoked with -p --continue: %+v", cmds)
	}
}
```

> **Implementer note:** the exact `runner.Fake` accessor for recorded commands and the way to make a fake command write a file may differ — inspect `internal/runner/fake.go` and adjust `fake.Commands()`/`containsArgs` and the side-effect setup to the real API. Keep the assertion's intent: input file written, `claude -p --continue` invoked, result file parsed into a `TurnResult`. Add a small `containsArgs` helper in the test if none exists.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/switchboard/ -run TestClaudeAgent`
Expected: FAIL — undefined `NewClaudeAgent`.

- [ ] **Step 3: Write minimal implementation**

```go
package switchboard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/runner"
)

// ClaudeAgent runs one turn by shelling a headless `claude` with the turn input
// and reading back the agent's JSON result file (mirrors the worker-result.json
// contract in internal/dispatchrun).
type ClaudeAgent struct {
	r       runner.Runner
	workDir string
}

// NewClaudeAgent builds an Agent backed by the local `claude` CLI.
func NewClaudeAgent(r runner.Runner, workDir string) *ClaudeAgent {
	return &ClaudeAgent{r: r, workDir: workDir}
}

const turnProtocol = `You are a Discord teammate. Below are new messages (each tagged [#channel] author). ` +
	`Do the work, then write your reply to .switchboard/turn-result.json as EXACTLY: ` +
	`{"messages":[{"channel":"<id>","content":"<text>"}],"action":"exit|wait|get"}. ` +
	`Use "wait" to sleep until someone writes, "get" to check again immediately, "exit" to stop.`

func (a *ClaudeAgent) RunTurn(ctx context.Context, input string) (TurnResult, error) {
	dir := filepath.Join(a.workDir, ".switchboard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return TurnResult{}, err
	}
	inputPath := filepath.Join(dir, "turn-input.txt")
	resultPath := filepath.Join(dir, "turn-result.json")
	if err := os.WriteFile(inputPath, []byte(turnProtocol+"\n\n"+input), 0o644); err != nil {
		return TurnResult{}, err
	}
	_ = os.Remove(resultPath) // clear any prior turn's result

	// claude -p --continue "$(cat <inputPath>)" — run in workDir so --continue
	// resumes this sandbox's rolling session. (See session-continuity risk in the
	// plan's Global Constraints.)
	cmd := fmt.Sprintf(`cd %s && claude -p --continue "$(cat %s)"`, shellQuote(a.workDir), shellQuote(inputPath))
	if err := a.r.Run("sh", "-c", cmd); err != nil {
		return TurnResult{}, fmt.Errorf("switchboard: claude turn: %w", err)
	}

	data, err := os.ReadFile(resultPath)
	if err != nil {
		return TurnResult{}, fmt.Errorf("switchboard: read turn result: %w", err)
	}
	return ParseTurnResult(data)
}

// shellQuote POSIX single-quotes s for use in a /bin/sh -c command.
func shellQuote(s string) string { return "'" + replaceAll(s, "'", `'\''`) + "'" }
```

> **Implementer note:** reuse an existing `shellQuote` if `internal/switchboard` can import one without a cycle; otherwise the tiny local helper above is fine (use `strings.ReplaceAll` directly — the `replaceAll` alias is illustrative). Confirm `runner.Runner` has a `Run(name string, args ...string) error` method with this shape (mirror `internal/dispatchrun`); adjust the call if the signature differs.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/switchboard/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/switchboard/agent.go internal/switchboard/agent_test.go
git commit -m "feat(switchboard): claude turn adapter (result-file protocol)"
```

---

### Task 5: `cmd/at-switchboard` binary + docs

**Files:**
- Create: `cmd/at-switchboard/main.go`
- Test: `cmd/at-switchboard/main_test.go`
- Modify: `AGENTS.md` (binaries line), `docs/OVERVIEW.md` (architecture: name the new binary + one line, linking the design spec §A)

**Interfaces:**
- Consumes: `switchboard.NewRESTClient`, `switchboard.NewClaudeAgent`, `switchboard.Run`, `switchboard.Config`; `internal/runner`, `internal/cli`.
- Produces: the `at-switchboard` binary. It reads config from env: `DISCORD_BOT_TOKEN` (required), `SWITCHBOARD_CHANNELS` (required, comma-separated channel ids), `SWITCHBOARD_POLL_INTERVAL` (optional Go duration, default `3s`), and `SWITCHBOARD_WORKDIR` (optional, default `/home/agent/workspace`).

**Design notes:** mirror `cmd/at-task/main.go`'s `run(argv, stdout, stderr) int` + `main()` skeleton. A missing required env var is a usage error (exit 2) written to stderr — but never echo the token. The binary is invoked by A2's `at-cove teammate` launch; it is not run directly by users.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"strings"
	"testing"
)

func TestRun_MissingToken_IsUsageError(t *testing.T) {
	var out, errb strings.Builder
	env := func(k string) string { return "" } // nothing set
	code := run([]string{"run"}, env, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "DISCORD_BOT_TOKEN") {
		t.Fatalf("stderr = %q", errb.String())
	}
	if strings.Contains(errb.String(), "secret") { // never leak values
		t.Fatalf("stderr should not mention secret values: %q", errb.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-switchboard/`
Expected: FAIL — no such package / undefined `run`.

- [ ] **Step 3: Write minimal implementation**

```go
// Command at-switchboard is the in-sandbox Discord conductor (Component A). It
// polls Discord, drives a headless claude turn loop, and posts replies. It is
// launched by `at-cove teammate` over SSH with the bot token injected in-session;
// it is not run directly by users. See docs/superpowers/specs/2026-08-26-remote-teammate-design.md §A.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/switchboard"
)

func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	token := getenv("DISCORD_BOT_TOKEN")
	channels := splitNonEmpty(getenv("SWITCHBOARD_CHANNELS"))
	if token == "" {
		fmt.Fprintln(stderr, "at-switchboard: DISCORD_BOT_TOKEN is required")
		return 2
	}
	if len(channels) == 0 {
		fmt.Fprintln(stderr, "at-switchboard: SWITCHBOARD_CHANNELS is required (comma-separated channel ids)")
		return 2
	}
	interval := 3 * time.Second
	if v := getenv("SWITCHBOARD_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			fmt.Fprintf(stderr, "at-switchboard: bad SWITCHBOARD_POLL_INTERVAL %q: %v\n", v, err)
			return 2
		}
		interval = d
	}
	workDir := getenv("SWITCHBOARD_WORKDIR")
	if workDir == "" {
		workDir = "/home/agent/workspace"
	}

	d := switchboard.NewRESTClient(token, channels)
	a := switchboard.NewClaudeAgent(runner.Real{}, workDir)
	cfg := switchboard.Config{PollInterval: interval}
	if err := switchboard.Run(context.Background(), cfg, d, a); err != nil {
		fmt.Fprintln(stderr, "at-switchboard:", err)
		return 1
	}
	return 0
}

func splitNonEmpty(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }
```

> **Implementer note:** confirm the concrete `runner.Real` constructor (it may be `runner.New()` or a struct — mirror `cmd/at-task/main.go`'s real runner). Adjust the `at-switchboard` argv handling if you prefer a `cli.App` with a `run` subcommand like at-task; the test calls `run([]string{"run"}, ...)`, so keep a `run` verb or make argv ignored — match whichever you implement.

- [ ] **Step 4: Run test + build**

Run: `go test ./cmd/at-switchboard/` then `go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Update docs**

- `AGENTS.md`: add `at-switchboard` to the binaries line (`**Binaries:** at-cove, at-task` → include `at-switchboard`), one clause noting it's the in-sandbox Discord conductor.
- `docs/OVERVIEW.md`: in the architecture/entry-points area, add one line: `cmd/at-switchboard` — the in-sandbox Discord conductor (Component A), launched by `at-cove teammate`; link `docs/superpowers/specs/2026-08-26-remote-teammate-design.md` §A. Do not duplicate the spec's design — one line + link.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-switchboard/main.go cmd/at-switchboard/main_test.go AGENTS.md docs/OVERVIEW.md
git commit -m "feat(at-switchboard): binary wiring for the Discord conductor + docs"
```

---

## Self-Review

**Spec coverage (§A of `2026-08-26-remote-teammate-design.md`):**
- Supervisor loop, batch-on-entry, channel/author-tagged inbox → Tasks 1–2. ✅
- Agent-returned `exit`/`wait`/`get` → Task 2. ✅
- Poll Discord REST (not gateway), per-channel `after` cursor → Task 3. ✅
- Agent returns `{messages, action}` via a JSON result file (worker-result.json-style) → Tasks 1 (parse) + 4 (adapter). ✅
- Token held by the conductor/adapter, never in the agent env, never logged → Task 3 (header-only) + Task 5 (no token echo). ✅
- `at-switchboard` binary → Task 5. ✅
- **Deferred to A2 (out of scope here, intentionally):** `at-cove teammate` detached launch + token injection, `Teammate` config class + `discord` block, `discord.com` egress via `ResolvedTeammateDomains`/`ApplySessionEgress`, image embed (stage script + `//go:embed` + assemble + Dockerfile + install-currency hash + BINARIES), the user-facing `at-cove teammate` usage doc, and the `--output-format json`/progress "working…" heartbeat. Listed here so A2 has the checklist.

**Placeholder scan:** no TBD/TODO. Two implementer-notes intentionally point at real code to confirm (the `runner.Fake` command-accessor API in Task 4, and the `runner.Real` constructor in Task 5) rather than guessing an API this plan can't see — resolve by reading `internal/runner` during those tasks.

**Type consistency:** `Message`/`Outbound`/`TurnResult`/`Action*` (Task 1) are used unchanged by the loop (Task 2), adapters (Tasks 3–4), and binary (Task 5). `Discord`/`Agent`/`Config`/`Run` signatures (Task 2) match their consumers. `NewRESTClient`/`NewClaudeAgent` match their Task 5 call sites.

**Session-continuity risk** is documented in Global Constraints and isolated behind `Agent` (Task 4) — the loop and all hermetic tests are independent of whether `claude -p --continue` carries session state; only the integration-tagged real run depends on it.
