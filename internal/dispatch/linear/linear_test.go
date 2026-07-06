package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/config"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
)

// rtFunc is a fake http.RoundTripper returning canned responses per request.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func testCfg() config.Config {
	return config.Config{
		Tracker: config.TrackerConfig{
			Team:             "AET",
			ClassLabelPrefix: "class:",
			States: config.StateMap{
				Ready: "Todo", InProgress: "In Progress", InReview: "In Review",
				Done: "Done", NeedsInput: "Needs Input", Blocked: "Backlog",
			},
		},
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
