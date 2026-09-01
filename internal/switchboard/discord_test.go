package switchboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestRESTClient_SeedReturnsNewestIDWithoutDeliveringMessages(t *testing.T) {
	var gotAuth, gotLimit string
	var afterVals []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotLimit = r.URL.Query().Get("limit")
		afterVals = r.URL.Query()["after"]
		// Discord returns newest-first; limit=1 means only one entry.
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "99", "content": "latest", "author": map[string]any{"username": "u", "global_name": "Sam"}},
		})
	}))
	defer srv.Close()

	c := NewRESTClient("secrettoken", []string{"chan1"}, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	cursors, err := c.Seed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bot secrettoken" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotLimit != "1" {
		t.Fatalf("limit param = %q, want 1", gotLimit)
	}
	if len(afterVals) != 0 {
		t.Fatalf("seed must not send an after param; got %q", afterVals)
	}
	if cursors["chan1"] != "99" {
		t.Fatalf("cursor = %q, want 99 (newest id)", cursors["chan1"])
	}
}

func TestRESTClient_SeedEmptyChannelGetsNoCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{}) // no messages yet
	}))
	defer srv.Close()

	c := NewRESTClient("secrettoken", []string{"chan1"}, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	cursors, err := c.Seed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cursors["chan1"]; ok {
		t.Fatalf("empty channel should get no cursor entry; got %+v", cursors)
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

// TestRESTClient_PostDoesNotRetryOn5xx guards idempotency: retrying a Post
// after a 5xx risks Discord having already created the message, so a second
// attempt would duplicate the reply. Post must give up after exactly one
// attempt on 5xx (unlike Poll/Seed, which keep retrying 5xx).
func TestRESTClient_PostDoesNotRetryOn5xx(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewRESTClient("tok", []string{"chan1"}, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	err := c.Post(context.Background(), "chan1", "hello")
	if err == nil {
		t.Fatal("expected an error from a 500 response")
	}
	if calls != 1 {
		t.Fatalf("HTTP attempts = %d, want 1 (Post must not retry on 5xx)", calls)
	}
}

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
	var gotWait time.Duration
	c := NewRESTClient("tok", []string{"cx"},
		WithBaseURL(srv.URL), WithHTTPClient(srv.Client()),
		WithSleep(func(ctx context.Context, d time.Duration) error { slept++; gotWait = d; return nil }))
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
	// Retry-After: "0" must be treated as "no override" (the secs > 0 guard),
	// falling back to the exponential default — NOT to a zero wait. A zero
	// wait would mean the guard was dropped and Discord could stall retries
	// at zero cadence.
	if gotWait <= 0 {
		t.Fatalf("wait = %v, want > 0 (Retry-After: 0 must fall back to exponential backoff, not a zero wait)", gotWait)
	}
}

// TestRESTClient_PollRetriesOn5xx guards that Poll (an idempotent GET) keeps
// retrying on a 5xx response, recovering once the server starts returning
// 200s — unlike Post, which must not retry 5xx (see
// TestRESTClient_PostDoesNotRetryOn5xx).
func TestRESTClient_PollRetriesOn5xx(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
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
		WithSleep(func(ctx context.Context, d time.Duration) error { slept++; return nil }))
	msgs, _, err := c.Poll(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || slept != 1 {
		t.Fatalf("expected 1 retry after 500: calls=%d slept=%d", calls, slept)
	}
	if len(msgs) != 1 || msgs[0].Content != "ok" {
		t.Fatalf("did not recover after retry: %+v", msgs)
	}
}

// TestRESTClient_PollCtxCancelDuringBackoffReturnsPromptly is a REAL timing
// test of the production backoff path: it deliberately does NOT override
// WithSleep, so the default ctxSleep (discord.go) is what actually runs —
// that's the code the Important-#1 fix lives in, and a test that stubs
// WithSleep (as an earlier version of this test did) exercises nothing about
// ctxSleep's own interruptibility; it would pass even against the old,
// non-interruptible `time.Sleep(d)` implementation.
//
// The server returns 429 with Retry-After: 2 (a real ~2s wait if honored to
// completion). The ctx is cancelled 50ms after Poll starts. Against the
// ctx-aware ctxSleep, Poll must return a cancellation error in well under
// 1s; against a plain time.Sleep(d), it would block for the full ~2s. This
// test is designed to FAIL (elapsed ~2s) if ctxSleep regresses to a
// non-interruptible sleep — see the "Final-review fixes v2" report section
// for the RED (plain time.Sleep) → GREEN (ctx-aware) evidence.
func TestRESTClient_PollCtxCancelDuringBackoffReturnsPromptly(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel)

	c := NewRESTClient("tok", []string{"cx"}, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	// No WithSleep: the production ctxSleep runs for real.

	start := time.Now()
	_, _, err := c.Poll(ctx, nil)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed >= 1*time.Second {
		t.Fatalf("elapsed = %v, want well under 1s (the ~2s Retry-After backoff must be interrupted by ctx cancellation, not waited out)", elapsed)
	}
	if calls != 1 {
		t.Fatalf("HTTP attempts after cancel = %d, want 1 (no further attempt after cancel during backoff)", calls)
	}
}

// TestRESTClient_PollCapsRetryAfter guards against honoring an unbounded
// Discord Retry-After (e.g. 3600s) uninterruptibly: doWithRetry must cap the
// wait it hands to the sleep seam at maxRetryAfter.
func TestRESTClient_PollCapsRetryAfter(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	var gotWait time.Duration
	c := NewRESTClient("tok", []string{"cx"},
		WithBaseURL(srv.URL), WithHTTPClient(srv.Client()),
		WithSleep(func(ctx context.Context, d time.Duration) error { gotWait = d; return nil }))
	if _, _, err := c.Poll(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if gotWait != maxRetryAfter {
		t.Fatalf("wait = %v, want exactly %v (a miscalculated cap must be caught, not just any value under it)", gotWait, maxRetryAfter)
	}
}
