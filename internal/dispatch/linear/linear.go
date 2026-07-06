// Package linear is at-dispatch's real scheduler.Tracker: a small GraphQL client
// over the Linear API. Live calls are exercised by the integration-tagged test.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aethons-tools/cove/internal/dispatch/config"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
)

const endpoint = "https://api.linear.app/graphql"

// Client talks to one Linear team. It satisfies scheduler.Tracker.
type Client struct {
	http    *http.Client
	token   string
	team    string
	prefix  string          // class label prefix
	states  config.StateMap // role → configured state name
	stateID map[scheduler.Role]string
}

// New constructs a Client and resolves the team's state names to ids up front.
func New(cfg config.Config, token string, httpc *http.Client) (*Client, error) {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	c := &Client{
		http: httpc, token: token,
		team:   cfg.Tracker.Team,
		prefix: cfg.Tracker.ClassLabelPrefix,
		states: cfg.Tracker.States,
	}
	if err := c.loadStates(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) loadStates(ctx context.Context) error {
	const q = `query($key:String!){workflowStates(filter:{team:{key:{eq:$key}}}){nodes{id name type}}}`
	var out struct {
		WorkflowStates struct {
			Nodes []struct{ ID, Name, Type string } `json:"nodes"`
		} `json:"workflowStates"`
	}
	if err := c.do(ctx, q, map[string]any{"key": c.team}, &out); err != nil {
		return err
	}
	byName := map[string]string{}
	for _, n := range out.WorkflowStates.Nodes {
		byName[n.Name] = n.ID
	}
	c.stateID = map[scheduler.Role]string{}
	roles := map[scheduler.Role]string{
		scheduler.RoleReady: c.states.Ready, scheduler.RoleInProgress: c.states.InProgress,
		scheduler.RoleInReview: c.states.InReview, scheduler.RoleDone: c.states.Done,
		scheduler.RoleNeedsInput: c.states.NeedsInput, scheduler.RoleBlocked: c.states.Blocked,
	}
	for role, name := range roles {
		id, ok := byName[name]
		if !ok {
			return fmt.Errorf("linear: team %s has no workflow state named %q (for role %d)", c.team, name, role)
		}
		c.stateID[role] = id
	}
	return nil
}

// do posts a GraphQL query and unmarshals data into out.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: http %d", resp.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage            `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("linear: %s", envelope.Errors[0].Message)
	}
	if out != nil {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

// Transition moves an issue to the state configured for role.
func (c *Client) Transition(ctx context.Context, issueID string, role scheduler.Role) error {
	id, ok := c.stateID[role]
	if !ok {
		return fmt.Errorf("linear: no state id for role %d", role)
	}
	const m = `mutation($id:String!,$stateId:String!){issueUpdate(id:$id,input:{stateId:$stateId}){success}}`
	var out struct{ IssueUpdate struct{ Success bool } }
	if err := c.do(ctx, m, map[string]any{"id": issueID, "stateId": id}, &out); err != nil {
		return err
	}
	if !out.IssueUpdate.Success {
		return fmt.Errorf("linear: issueUpdate reported failure for %s", issueID)
	}
	return nil
}

// PostComment adds a comment to an issue.
func (c *Client) PostComment(ctx context.Context, issueID, body string) error {
	const m = `mutation($issueId:String!,$body:String!){commentCreate(input:{issueId:$issueId,body:$body}){success}}`
	var out struct{ CommentCreate struct{ Success bool } }
	if err := c.do(ctx, m, map[string]any{"issueId": issueID, "body": body}, &out); err != nil {
		return err
	}
	if !out.CommentCreate.Success {
		return fmt.Errorf("linear: commentCreate reported failure for %s", issueID)
	}
	return nil
}
