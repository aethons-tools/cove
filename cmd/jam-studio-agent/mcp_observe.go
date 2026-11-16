package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Observing sessions (docs/modules/ROOT/pages/usage/jam/session-observe.adoc):
// a session whose role has scope.observe reads other sessions, read-only.
// The tools are registered only when the agent's environment says the
// session context grants it (JAM_OBSERVE=1, agentrun.ObserveEnv); Jam
// refuses a role without the grant either way. Each tool answers Jam's JSON
// as text.

// maxObserveBytes bounds an observe response: a page of events can be
// large, but past this it is more than an agent can read in one go.
const maxObserveBytes = 8 << 20

var observeSessionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type observeSessionsIn struct {
	Since string `json:"since,omitempty" jsonschema:"optional RFC 3339 start of the window; default 24 hours before until"`
	Until string `json:"until,omitempty" jsonschema:"optional RFC 3339 end of the window; default now"`
}

type observeActivityIn struct {
	Session string `json:"session" jsonschema:"the session's id, from observe_sessions"`
	Anchor  string `json:"anchor,omitempty" jsonschema:"where to page from: end (default), start, or id"`
	ID      string `json:"id,omitempty" jsonschema:"message id anchor, required when anchor=id"`
	Dir     string `json:"dir,omitempty" jsonschema:"from an id: forward (default) or backward"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max messages (default 50, at most 500)"`
}

type observeStreamsIn struct {
	Session string `json:"session" jsonschema:"the session's id, from observe_sessions"`
}

type observeEventsIn struct {
	Session  string   `json:"session" jsonschema:"the session's id, from observe_sessions"`
	Stream   string   `json:"stream,omitempty" jsonschema:"the stream, from observe_streams; default the newest"`
	AfterSeq uint64   `json:"after_seq,omitempty" jsonschema:"page after this event number (a page's next_after_seq)"`
	Limit    int      `json:"limit,omitempty" jsonschema:"max events (default 500, at most 2000)"`
	Types    []string `json:"types,omitempty" jsonschema:"only events of these types (the event's type field, e.g. result, assistant)"`
}

// observePath is /squawks/observe/sessions/{session}/{view}, or an error the
// agent reads.
func observePath(session, view string) (string, error) {
	switch {
	case session == "":
		return "", fmt.Errorf("session is required (an id from observe_sessions)")
	case !observeSessionRe.MatchString(session):
		return "", fmt.Errorf("%q is not a session id (an id from observe_sessions)", session)
	}
	return "/squawks/observe/sessions/" + session + "/" + view, nil
}

// observeGet GETs path with q and answers Jam's JSON as text; a refusal
// (400, 403, 404) carries Jam's reason.
func (c *messagingClient) observeGet(ctx context.Context, path string, q url.Values) (*mcp.CallToolResult, any, error) {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	// One byte over the cap tells a whole answer from a cut one.
	body, status, err := c.doStatusCap(ctx, http.MethodGet, path, nil, maxObserveBytes+1)
	switch {
	case err != nil:
		return nil, nil, err
	case status == http.StatusBadRequest || status == http.StatusForbidden || status == http.StatusNotFound:
		return nil, nil, fmt.Errorf("Jam refused: %s", trimReason(body))
	case status/100 != 2:
		return nil, nil, fmt.Errorf("Jam observe: unexpected status %d", status)
	case len(body) > maxObserveBytes:
		return nil, nil, fmt.Errorf("Jam's answer is over %d MiB: ask for fewer (limit) or filter by types", maxObserveBytes>>20)
	}
	return textResult(string(body)), nil, nil
}

func setIf(q url.Values, k, v string) {
	if v != "" {
		q.Set(k, v)
	}
}

func addObserveTools(s *mcp.Server, client *messagingClient, cfgErr error) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "observe_sessions",
		Description: "Your role observes sessions (read-only). List every session active in a window (default: the last 24 hours), most recently active first: id, label, kind, project, role, unit, owner, raised_at, first_at, last_at, live, and lifetime totals (turns, episodes, tool calls, errors, tokens, cost).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in observeSessionsIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		q := url.Values{}
		setIf(q, "since", in.Since)
		setIf(q, "until", in.Until)
		return client.observeGet(ctx, "/squawks/observe/sessions", q)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "observe_activity",
		Description: "Read a session's messages in and out, in every conversation (read-only), paged like read: anchor end (default), start, or id; dir; limit. Each message says whether the session sent it (out).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in observeActivityIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		path, err := observePath(in.Session, "activity")
		if err != nil {
			return nil, nil, err
		}
		q := url.Values{}
		setIf(q, "anchor", in.Anchor)
		setIf(q, "id", in.ID)
		setIf(q, "dir", in.Dir)
		if in.Limit != 0 {
			q.Set("limit", strconv.Itoa(in.Limit))
		}
		return client.observeGet(ctx, path, q)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "observe_streams",
		Description: "List a session's event streams, one per agent process, oldest first: stream_id, first_at, last_at, last_seq, events. Event numbers restart in each stream.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in observeStreamsIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		path, err := observePath(in.Session, "streams")
		if err != nil {
			return nil, nil, err
		}
		return client.observeGet(ctx, path, nil)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "observe_events",
		Description: "Read one stream's captured events (default: the newest stream), paged by after_seq (pass the answer's next_after_seq while more is true); limit defaults to 500, at most 2000. types filters by the event's type, e.g. [result] for the turn results first, then fetch the full events of the turns you want.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in observeEventsIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		path, err := observePath(in.Session, "events")
		if err != nil {
			return nil, nil, err
		}
		q := url.Values{}
		setIf(q, "stream", in.Stream)
		if in.AfterSeq != 0 {
			q.Set("after_seq", strconv.FormatUint(in.AfterSeq, 10))
		}
		if in.Limit != 0 {
			q.Set("limit", strconv.Itoa(in.Limit))
		}
		setIf(q, "types", strings.Join(in.Types, ","))
		return client.observeGet(ctx, path, q)
	})
}
