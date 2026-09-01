# Switchboard Hardening (Component A2, part 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the switchboard core production-viable by fixing the two blockers the A1 final review flagged — the cold-start flood (first poll delivers up to 100 historical messages as "new") and the fail-fast loop (any poll/post/agent/parse error kills the conductor) — plus adapter-level rate-limit/5xx resilience.

**Architecture:** Three changes inside the existing `internal/switchboard` package: (1) a `Seed` step on the `Discord` interface that positions per-channel cursors at "now" without delivering history, so `Run` starts from an empty inbox; (2) a fail-soft `Run` loop that recovers from turn/poll/post errors — posting a turn-failure notice to a configured channel, backing off, and continuing — instead of returning; (3) 429/5xx retry-with-backoff inside the Discord REST adapter. All hermetic (fakes / httptest / injected sleep). Builds on the merged-into-this-branch A1 core; product wiring (`at-cove teammate`, config class, embed, egress, docs) is the separate A2 part 2.

**Tech Stack:** Go stdlib (`context`, `time`, `net/http`, `net/http/httptest`).

## Global Constraints

- Package `internal/switchboard` only; stdlib only; no new dependencies. Do NOT touch `cmd/`, `internal/kit`, `internal/backend`, or the image pipeline — those are A2 part 2.
- Tests hermetic: fakes for the loop, `httptest` for the adapter, injected/stubbed sleep so backoff never really sleeps. `go test ./internal/switchboard/`.
- The bot token stays confined to the `Authorization: Bot <token>` header (unchanged from A1) — never add it to a log, error, or argv.
- **Fail-soft means the loop survives everything except `ActionExit` and context cancellation.** Context cancellation still returns promptly (unchanged). A recovered error is logged via the injected `Config.Log` sink (nil → discarded) and, for a turn failure, announced to `Config.ErrorChannel` (best-effort). Backoff is capped but retries do not give up — a standing teammate must survive a transient Discord outage.
- TDD (failing test first), plan/execute split, DRY, YAGNI, frequent commits. Docs footprint is a code comment only here (the user-facing teammate guide is A2 part 2).

---

### Task 1: Cold-start cursor seeding

**Files:**
- Modify: `internal/switchboard/loop.go` (add `Seed` to the `Discord` interface; seed at the top of `Run`)
- Modify: `internal/switchboard/discord.go` (implement `RESTClient.Seed`)
- Modify: `internal/switchboard/loop_test.go` (add `Seed` to the fake; new seed test; fix existing tests whose first turn now starts empty)
- Modify: `internal/switchboard/discord_test.go` (httptest for `Seed`)

**Interfaces:**
- Consumes: existing `Message`, `Discord`, `Run`.
- Produces: `Discord` gains `Seed(ctx context.Context) (map[string]string, error)`; `RESTClient` implements it.

**Design notes:** `Seed` returns per-channel cursors positioned at the newest existing message id (via `GET .../messages?limit=1`) WITHOUT returning those messages. `Run` calls `Seed` first and starts with an **empty** batch, so the first turn's inbox is "There are no messages." — no history flood. A channel with zero messages simply gets no cursor entry (a later poll with no `after` returns its ≤100 messages, which for an empty channel is none). Existing loop tests that fed a message on the "first-turn poll" must be updated: the first turn is now empty, so those messages arrive on a subsequent `get`/`wait` poll instead.

- [ ] **Step 1: Write the failing test** (add to `loop_test.go`)

```go
func TestRun_SeedsCursorsAndDoesNotDeliverHistory(t *testing.T) {
	d := &fakeDiscord{
		seed:  map[string]string{"cx": "100"}, // channel already has history up to id 100
		polls: [][]Message{ {} , {} },         // after seed: nothing new yet, then still nothing
	}
	a := &fakeAgent{results: []TurnResult{
		{Action: ActionWait}, // first turn MUST see empty inbox (no history)
		{Action: ActionExit},
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
```

> Extend the existing `fakeDiscord` (in `loop_test.go`) with: a `seed map[string]string` field, a `seeded bool` flag, and a `Seed(ctx)` method that sets `seeded=true` and returns a copy of `seed`. If `fakeDiscord` doesn't already record the cursors it receives per `Poll` (from Task 2 of A1 it should have `seenCursors`), keep that. Then FIX the pre-existing `TestRun_*` tests that assumed the first turn polls a message: move that message into the post-seed poll sequence so the first turn is empty. Re-run all loop tests after.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/switchboard/ -run TestRun_Seeds`
Expected: FAIL — `Seed` not in the interface / fake; and (before the Run change) the first turn isn't empty.

- [ ] **Step 3: Implement**

In `loop.go`, add to the `Discord` interface (after `Post`):
```go
	// Seed positions per-channel cursors at the newest existing message WITHOUT
	// returning those messages, so a fresh conductor doesn't replay channel history
	// on its first turn.
	Seed(ctx context.Context) (cursors map[string]string, err error)
```

Change the top of `Run` from:
```go
	cursors := map[string]string{}
	batch, cursors, err := d.Poll(ctx, cursors)
	if err != nil {
		return fmt.Errorf("switchboard: initial poll: %w", err)
	}
```
to:
```go
	// Cold start: seed cursors at "now" so the first turn sees an empty inbox
	// rather than replaying up to 100 lines of channel history as "new".
	cursors, err := d.Seed(ctx)
	if err != nil {
		return fmt.Errorf("switchboard: seed: %w", err)
	}
	var batch []Message
```

In `discord.go`, implement `Seed` (mirrors `Poll` but `limit=1`, no `after`, discards content):
```go
// Seed returns per-channel cursors at the newest existing message id, without
// returning the messages — so Run starts from an empty inbox. A channel with no
// messages gets no cursor entry.
func (c *RESTClient) Seed(ctx context.Context) (map[string]string, error) {
	cursors := map[string]string{}
	for _, ch := range c.channels {
		u := fmt.Sprintf("%s/channels/%s/messages?limit=1", c.baseURL, ch)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bot "+c.token)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("switchboard: seed %s: %w", ch, err)
		}
		var page []discordMessage
		derr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("switchboard: seed %s: status %d", ch, resp.StatusCode)
		}
		if derr != nil {
			return nil, fmt.Errorf("switchboard: seed %s decode: %w", ch, derr)
		}
		if len(page) > 0 {
			cursors[ch] = page[0].ID // newest-first, so [0] is the latest
		}
	}
	return cursors, nil
}
```

Add an httptest to `discord_test.go` asserting `Seed` issues `limit=1`, sends the auth header, and returns the newest id as the cursor without delivering messages.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/switchboard/`
Expected: PASS (new seed tests + all updated existing tests).

- [ ] **Step 5: Commit**

```bash
git add internal/switchboard/loop.go internal/switchboard/discord.go internal/switchboard/loop_test.go internal/switchboard/discord_test.go
git commit -m "feat(switchboard): seed cursors at boot so the first turn skips channel history"
```

---

### Task 2: Fail-soft loop (turn/poll/post error recovery)

**Files:**
- Modify: `internal/switchboard/loop.go` (`Config` fields + recovery in `Run`/`waitForMessages`)
- Modify: `internal/switchboard/loop_test.go` (recovery tests)

**Interfaces:**
- Consumes: `Discord`, `Agent`, `RenderInbox`, `Seed` (Task 1).
- Produces: `Config` gains `ErrorChannel string` and `Log func(string)`; `Run`'s error semantics change from fail-fast to fail-soft (returns only on `ActionExit` or ctx cancellation).

**Design notes:** wrap each failure mode:
- **Turn failure** (`a.RunTurn` errors — includes a missing/malformed result file): post `"⚠️ I hit an error and skipped a turn: <err>"` to `Config.ErrorChannel` (best-effort; skip if empty), `Log` it, back off, then continue with an empty batch (as if `get` returned nothing). Do NOT return.
- **Post failure** (posting an agent reply): `Log` it and continue with the remaining messages; do NOT return (one failed post must not kill the teammate).
- **Poll failure** (in `get`/`wait`/seed-less batch fetch): `Log` it, back off, and retry — never return. `waitForMessages` already loops; make the `get` path and the poll error path retry instead of returning.
- **Context cancellation** still returns `ctx.Err()` promptly (unchanged) — it is the one non-recoverable signal besides `ActionExit`. Distinguish it from other errors with `errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)` (or check `ctx.Err() != nil`) so a cancel isn't swallowed by the retry loop.

Introduce one helper, `pollWithRetry(ctx, cfg, d, cursors)`, used by both the `get` path and `waitForMessages`, that retries on error with backoff and returns only on success or ctx cancellation.

- [ ] **Step 1: Write the failing test** (add to `loop_test.go`)

```go
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
		seed:      map[string]string{},
		pollErrs:  []error{nil, errorString("discord 503"), nil}, // seed-batch ok, next poll errors, then ok
		polls:     [][]Message{{}, {}, {{ID: "9", Channel: "cx", Author: "sam", Content: "hi"}}},
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
```

> Add the small test doubles this needs and don't assume they exist: `fakeAgentFunc{fn func(i int, input string) (TurnResult, error)}` (a call-indexed agent), an `errorString` type (or reuse `errors.New`), a `contains` helper (or `strings.Contains`), and `errorsIsCanceled` (`errors.Is(err, context.Canceled)`). Extend `fakeDiscord` with `pollErrs []error` (indexed per Poll call; nil = ok) alongside `polls`. Keep the additions minimal and local to the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/switchboard/ -run 'TestRun_TurnError|TestRun_PollError|TestRun_CtxCancelStill'`
Expected: FAIL — `Run` currently returns on the first agent/poll error; `Config.ErrorChannel`/`Log` don't exist.

- [ ] **Step 3: Implement**

Add to `Config`:
```go
	// ErrorChannel receives a best-effort notice when a turn fails; empty disables it.
	ErrorChannel string
	// Log receives one-line notices about recovered errors; nil discards them.
	Log func(string)
```
Add a helper on `Config`:
```go
func (c Config) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(fmt.Sprintf(format, args...))
	}
}
```

Rewrite `Run`'s body (after the Task 1 seed) to be fail-soft:
```go
	var batch []Message
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := a.RunTurn(ctx, RenderInbox(batch))
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			cfg.logf("switchboard: turn failed, recovering: %v", err)
			if cfg.ErrorChannel != "" {
				_ = d.Post(ctx, cfg.ErrorChannel, "⚠️ I hit an error and skipped a turn: "+err.Error())
			}
			if err := cfg.sleep(ctx); err != nil {
				return err
			}
			batch = nil // re-enter with an empty inbox
			continue
		}
		posted := true
		for _, m := range res.Messages {
			if err := d.Post(ctx, m.Channel, m.Content); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				// A failed delivery must NOT let the loop proceed as if the reply
				// landed (the A1 review's "don't act as delivered" property). Recover
				// by re-entering with an empty inbox rather than acting on the action.
				cfg.logf("switchboard: post to %s failed, recovering: %v", m.Channel, err)
				if cfg.ErrorChannel != "" && cfg.ErrorChannel != m.Channel {
					_ = d.Post(ctx, cfg.ErrorChannel, "⚠️ failed to deliver a reply to "+m.Channel)
				}
				posted = false
				break
			}
		}
		if !posted {
			if err := cfg.sleep(ctx); err != nil {
				return err
			}
			batch = nil
			continue
		}
		switch res.Action {
		case ActionExit:
			return nil
		case ActionGet:
			batch, cursors, err = pollWithRetry(ctx, cfg, d, cursors)
			if err != nil {
				return err // only ctx cancellation reaches here
			}
		case ActionWait:
			batch, cursors, err = waitForMessages(ctx, cfg, d, cursors)
			if err != nil {
				return err
			}
		default:
			cfg.logf("switchboard: unhandled action %q, waiting", res.Action)
			batch, cursors, err = waitForMessages(ctx, cfg, d, cursors)
			if err != nil {
				return err
			}
		}
	}
```

Add `pollWithRetry` and make `waitForMessages` use it:
```go
// pollWithRetry polls once, retrying with backoff on error until it succeeds or
// ctx is cancelled. Poll errors are recovered (a transient Discord outage must
// not kill a standing teammate); only ctx cancellation is returned.
func pollWithRetry(ctx context.Context, cfg Config, d Discord, cursors map[string]string) ([]Message, map[string]string, error) {
	for {
		batch, nc, err := d.Poll(ctx, cursors)
		if err == nil {
			return batch, nc, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		cfg.logf("switchboard: poll failed, retrying: %v", err)
		if serr := cfg.sleep(ctx); serr != nil {
			return nil, nil, serr
		}
	}
}

// waitForMessages polls (with retry) until a non-empty batch arrives.
func waitForMessages(ctx context.Context, cfg Config, d Discord, cursors map[string]string) ([]Message, map[string]string, error) {
	for {
		batch, nc, err := pollWithRetry(ctx, cfg, d, cursors)
		if err != nil {
			return nil, nil, err
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
Add `"errors"` to imports if the ctx checks use `errors.Is` (or use `ctx.Err() != nil` as shown). Remove the now-unused `fmt.Errorf` wrapping paths that returned on poll/turn errors.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/switchboard/`
Expected: PASS (recovery tests + all prior tests; note some prior tests that asserted a returned error on a fake Post error must be updated — a Post error is now recovered, not returned).

- [ ] **Step 5: Commit**

```bash
git add internal/switchboard/loop.go internal/switchboard/loop_test.go
git commit -m "feat(switchboard): fail-soft loop — recover from turn/poll/post errors"
```

---

### Task 3: Adapter 429/5xx retry-with-backoff + pagination comment

**Files:**
- Modify: `internal/switchboard/discord.go` (retry on 429/5xx in `Poll`/`Post`/`Seed`; pagination comment; injectable sleep)
- Modify: `internal/switchboard/discord_test.go` (httptest 429-then-200)

**Interfaces:**
- Consumes: existing `RESTClient`.
- Produces: `WithSleep(func(time.Duration)) RESTOption` (test seam for backoff); no signature changes to `Poll`/`Post`/`Seed`.

**Design notes:** wrap each request in a small retry loop: on HTTP 429 or any 5xx, sleep and retry up to a fixed cap (e.g. 4 attempts); honor the `Retry-After` header (seconds) for 429 when present, else exponential backoff. A non-2xx that isn't 429/5xx (e.g. 403) is returned immediately. Add `WithSleep` so tests inject a no-op/counting sleep. Add the pagination clarifying comment to `Poll`.

- [ ] **Step 1: Write the failing test** (add to `discord_test.go`)

```go
func TestRESTClient_PollRetriesOn429(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "7", "content": "ok", "author": map[string]any{"username": "u"}},
		})
	}))
	defer srv.Close()
	var slept int
	c := NewRESTClient("tok", []string{"cx"},
		WithBaseURL(srv.URL), WithHTTPClient(srv.Client()),
		WithSleep(func(time.Duration) { slept++ }))
	msgs, _, err := c.Poll(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || slept != 1 {
		t.Fatalf("expected 1 retry after 429: calls=%d slept=%d", calls, slept)
	}
	if len(msgs) != 1 || msgs[0].Content != "ok" {
		t.Fatalf("did not recover after retry: %+v", msgs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/switchboard/ -run TestRESTClient_PollRetries`
Expected: FAIL — `WithSleep` undefined; `Poll` returns an error on 429 instead of retrying.

- [ ] **Step 3: Implement**

Add a `sleep func(time.Duration)` field to `RESTClient` (default `time.Sleep`) and the option:
```go
// WithSleep overrides the backoff sleep (for tests).
func WithSleep(s func(time.Duration)) RESTOption { return func(c *RESTClient) { c.sleep = s } }
```
Set the default in `NewRESTClient` (`sleep: time.Sleep`). Add a `doWithRetry` helper that performs the request, and on 429/5xx sleeps (Retry-After seconds if parseable, else `1<<attempt` seconds capped) and retries up to 4 attempts, returning the final `*http.Response` (caller still checks status). Route `Poll`, `Post`, and `Seed`'s `c.http.Do(req)` through it. Because a request body (Post) must be re-readable across retries, build the request inside the retry loop from the marshalled bytes (or use `req.GetBody`). Add the clarifying comment above the `Poll` per-channel loop:
```go
		// Single page of up to 100 messages per channel per poll. Discord's `after`
		// is forward pagination (oldest-after-cursor), so a >100 backlog drains 100
		// per poll across successive polls — bounded catch-up latency, never message loss.
```

> Keep the retry cap and backoff simple and hermetic (inject `c.sleep`); honor `ctx` cancellation between attempts (return `ctx.Err()` if `ctx.Done()`). Confirm the token is still only in the header on every retried request.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/switchboard/` then `just test` and `just lint`
Expected: PASS; whole suite + lint green.

- [ ] **Step 5: Commit**

```bash
git add internal/switchboard/discord.go internal/switchboard/discord_test.go
git commit -m "feat(switchboard): retry Discord 429/5xx with backoff; document poll pagination"
```

---

## Self-Review

**Coverage of the A1 final-review carryovers:**
- Cold-start flood → Task 1 (seed at boot, empty first inbox). ✅
- Fail-fast loop → Task 2 (recover from turn/poll/post errors; only exit/ctx-cancel return). ✅
- Discord 429/5xx backoff → Task 3. ✅
- Pagination "not loss" clarifying comment → Task 3. ✅
- **Still deferred to A2 part 2:** `at-cove teammate` launch, `Teammate` config class, `discord.com` egress, image embed/currency, `//go:build integration` real-`claude` continuity test, `shellQuote` consolidation. (The loop nil-`Sleep`/ctx-guard coverage Minor is left as-is — trivial defensive code.)

**Placeholder scan:** no TBD/TODO; every code step shows the concrete change against the actual A1 `loop.go`/`discord.go` (read at plan time). Test doubles that don't yet exist (`fakeAgentFunc`, `pollErrs`, `WithSleep`) are named explicitly with instructions to add them.

**Type consistency:** `Seed(ctx) (map[string]string, error)` added to `Discord` in Task 1 is implemented by `RESTClient` (Task 1) and the fake (Tasks 1–2). `Config.ErrorChannel`/`Log` (Task 2) and `WithSleep` (Task 3) are additive; `Poll`/`Post`/`Seed` signatures are unchanged, so the A1 binary (`cmd/at-switchboard`) and A2 part 2 keep compiling. `Run`'s signature is unchanged.

**Interaction risk:** Task 2 changes the semantics of a Post error from "return" to "recover" — any A1 test asserting a returned error on Post failure (e.g. `TestRun_PostErrorAborts`) must be updated to assert recovery instead. Called out in Task 2 Step 4.
