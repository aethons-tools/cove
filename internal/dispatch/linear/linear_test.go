package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/kit"
)

// rtFunc is a fake http.RoundTripper returning canned responses per request.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func testCfg() kit.Config {
	return kit.Config{
		Tracker: &kit.Tracker{Linear: &kit.LinearTracker{
			Team:             "AET",
			ClassLabelPrefix: "class:",
			States: kit.StateMap{
				Ready: "Todo", InProgress: "In Progress", InReview: "In Review",
				Done: "Done", NeedsInput: "Needs Input", Blocked: "Backlog",
			},
		}},
	}
}

// statesResponse is the workflowStates payload New fetches at construction.
const statesResponse = `{"data":{"workflowStates":{"nodes":[
 {"id":"s-todo","name":"Todo","type":"unstarted"},
 {"id":"s-prog","name":"In Progress","type":"started"},
 {"id":"s-rev","name":"In Review","type":"started"},
 {"id":"s-done","name":"Done","type":"completed"},
 {"id":"s-ni","name":"Needs Input","type":"unstarted"},
 {"id":"s-block","name":"Backlog","type":"backlog"}]}}}`

func newTestClient(t *testing.T, rt rtFunc) *Client {
	t.Helper()
	c, err := New(testCfg(), "tok", &http.Client{Transport: rt})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewFetchesStateMapAndAuthHeader(t *testing.T) {
	var sawAuth string
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		return jsonResp(statesResponse), nil
	})
	if sawAuth != "tok" {
		t.Fatalf("Authorization = %q; want tok", sawAuth)
	}
	if c.stateID[scheduler.RoleInReview] != "s-rev" {
		t.Fatalf("RoleInReview id = %q; want s-rev", c.stateID[scheduler.RoleInReview])
	}
}

func TestTransitionSendsIssueUpdate(t *testing.T) {
	var body map[string]any
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil // New's fetch
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		return jsonResp(`{"data":{"issueUpdate":{"success":true}}}`), nil
	})
	if err := c.Transition(context.Background(), "i1", scheduler.RoleInReview); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	q, _ := body["query"].(string)
	vars, _ := body["variables"].(map[string]any)
	if !strings.Contains(q, "issueUpdate") {
		t.Fatalf("query missing issueUpdate: %s", q)
	}
	if vars["stateId"] != "s-rev" || vars["id"] != "i1" {
		t.Fatalf("variables = %v; want id=i1 stateId=s-rev", vars)
	}
}

func TestPostCommentSendsCommentCreate(t *testing.T) {
	var body map[string]any
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		return jsonResp(`{"data":{"commentCreate":{"success":true}}}`), nil
	})
	if err := c.PostComment(context.Background(), "i9", "hello"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	vars, _ := body["variables"].(map[string]any)
	if vars["issueId"] != "i9" || vars["body"] != "hello" {
		t.Fatalf("variables = %v; want issueId=i9 body=hello", vars)
	}
}

func TestListReadyParsesIssuesAndClass(t *testing.T) {
	const resp = `{"data":{"issues":{"nodes":[
	 {"id":"i1","identifier":"AET-1","title":"T1","description":"D1","labels":{"nodes":[{"name":"class:implement"},{"name":"p1"}]}}
	]}}}`
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil
		}
		return jsonResp(resp), nil
	})
	got, err := c.ListReady(context.Background())
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if len(got) != 1 || got[0].Identifier != "AET-1" || got[0].Class != "implement" {
		t.Fatalf("ListReady = %+v; want one AET-1 with class implement", got)
	}
}

// runReadyGated drives ListReady with canned responses: the state map (at New),
// then the READY-issues query, then the authoritative blocker-state lookup.
func runReadyGated(t *testing.T, ready, blockerStates string) []scheduler.Issue {
	t.Helper()
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResp(statesResponse), nil
		case 2:
			return jsonResp(ready), nil
		default:
			return jsonResp(blockerStates), nil
		}
	})
	got, err := c.ListReady(context.Background())
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	return got
}

func TestListReadyDispatchesOnlyWhenBlockersDone(t *testing.T) {
	// Both are in READY. b1's sole blocker is Done → dispatchable. b2's blocker is
	// still In Review (started) → held: a dependency is satisfied ONLY at Done
	// (COV-56/COV-65). Doneness comes from the authoritative id→state lookup.
	const ready = `{"data":{"issues":{"nodes":[
	 {"id":"b1","identifier":"AET-B1","title":"","description":"","labels":{"nodes":[]},
	  "inverseRelations":{"nodes":[{"type":"blocks","issue":{"id":"blk1"},"relatedIssue":{"id":"b1"}}]}},
	 {"id":"b2","identifier":"AET-B2","title":"","description":"","labels":{"nodes":[]},
	  "inverseRelations":{"nodes":[{"type":"blocks","issue":{"id":"blk2"},"relatedIssue":{"id":"b2"}}]}}
	]}}}`
	const blockerStates = `{"data":{"issues":{"nodes":[
	 {"id":"blk1","state":{"type":"completed"}},
	 {"id":"blk2","state":{"type":"started"}}
	]}}}`
	got := runReadyGated(t, ready, blockerStates)
	if len(got) != 1 || got[0].ID != "b1" {
		t.Fatalf("ListReady = %+v; want only b1 (its blocker is Done)", got)
	}
}

// A READY issue with NO blockers is dispatchable — and the blocker lookup is not
// even needed. (No third request is made when there are no blocker ids.)
func TestListReadyDispatchesUnblockedIssue(t *testing.T) {
	const ready = `{"data":{"issues":{"nodes":[
	 {"id":"free","identifier":"AET-FREE","title":"","description":"","labels":{"nodes":[{"name":"class:implement"}]},
	  "inverseRelations":{"nodes":[]}}
	]}}}`
	// blockerStates would only be used if there were blocker ids; there are none.
	got := runReadyGated(t, ready, `{"data":{"issues":{"nodes":[]}}}`)
	if len(got) != 1 || got[0].ID != "free" || got[0].Class != "implement" {
		t.Fatalf("ListReady = %+v; want the unblocked free issue", got)
	}
}

// A mid-chain completion must not cascade: with a grandparent Done but the direct
// blocker not, only the direct dependent is dispatchable (COV-56).
func TestListReadyDoesNotCascadeMultiLevel(t *testing.T) {
	// c2 blocked by c1 (Done); c3 blocked by c2 (not Done). Only c2 dispatchable.
	// c2 is both a READY issue and c3's blocker; the authoritative lookup reports
	// c2's real state (not completed), so its done-ness can't leak to c3.
	const ready = `{"data":{"issues":{"nodes":[
	 {"id":"c2","identifier":"AET-C2","title":"","description":"","labels":{"nodes":[]},
	  "inverseRelations":{"nodes":[{"type":"blocks","issue":{"id":"c1"},"relatedIssue":{"id":"c2"}}]}},
	 {"id":"c3","identifier":"AET-C3","title":"","description":"","labels":{"nodes":[]},
	  "inverseRelations":{"nodes":[{"type":"blocks","issue":{"id":"c2"},"relatedIssue":{"id":"c3"}}]}}
	]}}}`
	const blockerStates = `{"data":{"issues":{"nodes":[
	 {"id":"c1","state":{"type":"completed"}},
	 {"id":"c2","state":{"type":"started"}}
	]}}}`
	got := runReadyGated(t, ready, blockerStates)
	if len(got) != 1 || got[0].ID != "c2" {
		t.Fatalf("ListReady = %+v; want only c2 (c3 held — its blocker c2 is not Done)", got)
	}
}

// A READY issue whose blocker id resolves to nothing (deleted/missing) is held,
// not dispatched on a phantom satisfied dependency.
func TestListReadyHoldsOnMissingBlocker(t *testing.T) {
	const ready = `{"data":{"issues":{"nodes":[
	 {"id":"d1","identifier":"AET-D1","title":"","description":"","labels":{"nodes":[]},
	  "inverseRelations":{"nodes":[{"type":"blocks","issue":{"id":"gone"},"relatedIssue":{"id":"d1"}}]}}
	]}}}`
	const blockerStates = `{"data":{"issues":{"nodes":[]}}}`
	if got := runReadyGated(t, ready, blockerStates); len(got) != 0 {
		t.Fatalf("ListReady = %+v; want none (blocker missing → not satisfied)", got)
	}
}

func TestListInProgressParsesStartedAtAndSkipsUnparseable(t *testing.T) {
	// i1 has a parseable startedAt; i2's is absent → skipped (can't establish staleness).
	const resp = `{"data":{"issues":{"nodes":[
	 {"id":"i1","identifier":"AET-1","title":"T1","description":"","startedAt":"2026-07-15T09:00:00.000Z","labels":{"nodes":[{"name":"class:implement"}]}},
	 {"id":"i2","identifier":"AET-2","title":"T2","description":"","startedAt":"","labels":{"nodes":[]}}
	]}}}`
	var vars map[string]any
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		vars, _ = body["variables"].(map[string]any)
		return jsonResp(resp), nil
	})
	got, err := c.ListInProgress(context.Background())
	if err != nil {
		t.Fatalf("ListInProgress: %v", err)
	}
	if vars["state"] != "In Progress" {
		t.Fatalf("query state = %v; want In Progress", vars["state"])
	}
	if len(got) != 1 || got[0].ID != "i1" || got[0].Class != "implement" {
		t.Fatalf("ListInProgress = %+v; want only i1 with class implement", got)
	}
	if got[0].StartedAt.IsZero() {
		t.Fatalf("StartedAt not parsed for i1")
	}
}

func TestCommentsParsesThread(t *testing.T) {
	const resp = `{"data":{"issue":{"comments":{"nodes":[
	 {"body":"hi","user":{"displayName":"brent"}},
	 {"body":"yo","user":{"displayName":"agent"}}]}}}}`
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil
		}
		return jsonResp(resp), nil
	})
	got, err := c.Comments(context.Background(), "i1")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(got) != 2 || got[0].Author != "brent" || got[1].Body != "yo" {
		t.Fatalf("Comments = %+v", got)
	}
}
