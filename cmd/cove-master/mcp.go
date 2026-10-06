// The mcp subcommand ("cove-master mcp") runs a stdio Model Context Protocol
// server that gives the cove's claude agent tools — "read" and "send" brokered
// through Jam's /squawks endpoint on the cove's own ticket (or, via an
// optional "to" target, another authorized user/channel), "commit" brokered
// through Jam's POST /squawks/commit endpoint to advance the durable read
// cursor, and "list_targets" brokered through Jam's GET /squawks/targets
// endpoint.
//
// Security notes (see AGENTS.md / the Jam messaging MCP plan):
//   - The identity token is read from AT_JAM_IDENTITY_TOKEN (or its deprecated
//     name AT_HARBOR_IDENTITY_TOKEN) only — never
//     accepted as a flag/argv, and never logged.
//   - Requests to Jam go over TLS (https://<host>) through the default
//     transport, which honors ProxyFromEnvironment (the squid CONNECT proxy)
//     and the system trust store.
//   - Non-2xx responses from Jam are turned into a generic tool error that
//     never echoes the token or the raw response body.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxJamResponseBytes bounds how much of a Jam response we ever read
// into memory, whether success or failure.
const maxJamResponseBytes = 1 << 20 // 1 MiB

// squawkOut mirrors one entry of Jam's GET /squawks response.
type squawkOut struct {
	ID     string `json:"id,omitempty"`
	Author string `json:"author,omitempty"`
	Body   string `json:"body,omitempty"`
	At     string `json:"at,omitempty"`
	// ContentType: text/markdown, or text/plain (the sender opted out of
	// markdown; read it literally).
	ContentType string `json:"content_type,omitempty"`
	// Channel is the conversation it's in (absent on one from before Jam's
	// channel cutover); From who sent it.
	Channel *partyOut `json:"channel,omitempty"`
	From    *partyOut `json:"from,omitempty"`
}

// partyOut mirrors a channel or participant in Jam's responses: an id, a kind
// (ticket/chat/room; session/user/account) and a label.
type partyOut struct {
	ID    string `json:"id,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Label string `json:"label,omitempty"`
}

// sendOut is the "send" tool's typed output: the squawk's id and the channel
// it went to (both empty from a Jam that predates them).
type sendOut struct {
	ID      string   `json:"id,omitempty"`
	Channel partyOut `json:"channel,omitempty"`
}

// sendIn is the "send" tool's typed input.
type sendIn struct {
	Text string `json:"text" jsonschema:"the message body to post"`
	To   string `json:"to,omitempty" jsonschema:"optional target: user:<name>, chat:user:<a>,user:<b>, channel:<room>, or ticket:<key>; omit to post in your default channel — your ticket's, or a chat with whoever started your session"`
	// ContentType opts out of the markdown default.
	ContentType string `json:"content_type,omitempty" jsonschema:"optional: text/markdown (the default; the body is rendered as markdown) or text/plain (shown literally — use it for text that would render badly as markdown, e.g. logs, ASCII art, or stray * and _)"`
}

// readIn is the "read" tool's typed input: optional seek parameters.
type readIn struct {
	Anchor string `json:"anchor,omitempty" jsonschema:"where to read from: cursor (default; your commit position), start, end, or id"`
	ID     string `json:"id,omitempty" jsonschema:"message id anchor, required when anchor=id"`
	Dir    string `json:"dir,omitempty" jsonschema:"direction from the anchor: forward (default, oldest-first) or backward"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max messages to return (default 50)"`
}

// readOut is the "read" tool's typed output: the cove's inbox.
type readOut struct {
	Squawks         []squawkOut `json:"squawks"`
	CommittedCursor string      `json:"committed_cursor,omitempty"`
	PageFirst       string      `json:"page_first,omitempty"`
	PageLast        string      `json:"page_last,omitempty"`
}

// listTargetsIn is the "list_targets" tool's (empty) typed input.
type listTargetsIn struct{}

// commitIn is the "commit" tool's typed input.
type commitIn struct {
	UpTo string `json:"up_to" jsonschema:"the message id you have processed up to; advances your durable read cursor so these messages are not handed to you again"`
}

// commitOut is the "commit" tool's typed output.
type commitOut struct {
	CommittedCursor string `json:"committed_cursor,omitempty"`
}

// escalateIn is the "escalate" tool's typed input.
type escalateIn struct {
	Category string `json:"category" jsonschema:"the block category to route escalation by, e.g. infra / ticket-blocked / code-architecture (free-form; unknown falls back to the default tier chain)"`
}

// endIn is the "end" tool's typed input.
type endIn struct {
	Reason string `json:"reason" jsonschema:"why the session is ending (shown to its owner); the session is torn down after this turn ends and is never woken again"`
}

// idleTimeoutIn is the "idle_timeout" tool's typed input.
type idleTimeoutIn struct {
	Duration string `json:"duration" jsonschema:"how long after this turn ends to apply the role's on-idle action if nothing else wakes you, e.g. 45m or 2h; or off"`
	Scope    string `json:"scope" jsonschema:"next (only the next turn end) or always (until the session ends)"`
}

// reportIn is the "report" tool's typed input.
type reportIn struct {
	State   string `json:"state" jsonschema:"in-progress, in-review (needs pr), needs-input (put the question in summary), blocked, or done"`
	Summary string `json:"summary" jsonschema:"what changed, for the ticket's comment"`
	PR      string `json:"pr,omitempty" jsonschema:"the pull request URL, when there is one"`
}

// alarmSetIn is the "alarm_set" tool's typed input.
type alarmSetIn struct {
	Name     string `json:"name" jsonschema:"the alarm's name: lowercase letters, digits, - and _ (setting an existing name replaces it)"`
	Schedule string `json:"schedule" jsonschema:"an RFC 3339 time (fires once, e.g. 2026-10-05T14:30:00Z) or a 5-field cron expression in the role's time zone (recurring, at most once a minute, e.g. */5 * * * *)"`
	Note     string `json:"note,omitempty" jsonschema:"what the wake tells you to do when the alarm fires"`
	Gate     string `json:"gate,omitempty" jsonschema:"optional shell command run in your workspace when the alarm comes due (60s limit): exit 0 wakes you with its output; any other exit keeps sleeping; a timeout or a missing command wakes you with the error"`
}

// alarmNameIn is the "alarm_clear" tool's typed input.
type alarmNameIn struct {
	Name string `json:"name" jsonschema:"the alarm to remove"`
}

// alarmListIn is the "alarm_list" tool's (empty) input.
type alarmListIn struct{}

// alarmItem mirrors one entry of Jam's GET /alarms response.
type alarmItem struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Note     string `json:"note,omitempty"`
	NextAt   string `json:"next_at,omitempty"`
	Fired    bool   `json:"fired,omitempty"`
	Gate     string `json:"gate,omitempty"`
	LastGate *struct {
		At      string `json:"at"`
		Verdict string `json:"verdict"`
		Exit    int    `json:"exit"`
		Output  string `json:"output,omitempty"`
	} `json:"last_gate,omitempty"`
}

// alarmsOut is the "alarm_list" tool's typed output.
type alarmsOut struct {
	Alarms []alarmItem `json:"alarms"`
}

// targetItem mirrors one entry of Jam's GET /squawks/targets response.
type targetItem struct {
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
}

// targetsOut is the "list_targets" tool's typed output.
type targetsOut struct {
	Targets []targetItem `json:"targets"`
}

// messagingClient forwards read/send calls to Jam's /squawks endpoint.
type messagingClient struct {
	http    *http.Client
	baseURL string // e.g. "https://jam.example.com", no trailing slash
	token   string
}

// jamBaseURL derives the https base URL Jam's /squawks endpoint is
// served from, given AT_JAM_RUNTIME_ADDR.
//
// In production that variable is "host:443" (per cove-master's Attach dial
// config) — the port is stripped since :443 is implied by https. Tests may
// instead pass a full URL (e.g. an httptest server's http://127.0.0.1:PORT),
// which is used as-is: the scheme is only defaulted to https when the value
// doesn't already carry one.
func jamBaseURL(addr string) (string, error) {
	if addr == "" {
		return "", fmt.Errorf("AT_JAM_RUNTIME_ADDR is required")
	}
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", fmt.Errorf("invalid AT_JAM_RUNTIME_ADDR: %w", err)
		}
		return strings.TrimSuffix(u.String(), "/"), nil
	}
	host := addr
	if h, port, err := net.SplitHostPort(addr); err == nil {
		if port == "" || port == "443" {
			host = h
		} else {
			host = net.JoinHostPort(h, port)
		}
	}
	return "https://" + host, nil
}

// newMessagingClient builds a messagingClient from the environment. It never
// places the token on argv or in any error/log message.
func newMessagingClient(getenv func(string) string) (*messagingClient, error) {
	base, err := jamBaseURL(jamEnv(getenv, "RUNTIME_ADDR"))
	if err != nil {
		return nil, err
	}
	token := jamEnv(getenv, "IDENTITY_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("AT_JAM_IDENTITY_TOKEN is required")
	}
	return &messagingClient{
		// nil Transport falls back to http.DefaultTransport: system TLS trust
		// plus ProxyFromEnvironment, so the squid CONNECT proxy is honored.
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: base,
		token:   token,
	}, nil
}

// do issues an authenticated request to Jam at c.baseURL+pathSuffix and
// returns the response body (capped) on a 2xx status. Any error returned is
// generic: it never contains the bearer token, and never echoes the raw
// response body from Jam.
func (c *messagingClient) do(ctx context.Context, method, pathSuffix string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathSuffix, reqBody)
	if err != nil {
		return nil, fmt.Errorf("building Jam messages request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Jam messages request failed")
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxJamResponseBytes))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Jam messages: unexpected status %d", resp.StatusCode)
	}
	return respBody, nil
}

// send posts a message via Jam: to the cove's default channel, or, when to
// is non-empty, to the authorized target it names.
func (c *messagingClient) send(ctx context.Context, text, to, contentType string) (sendOut, error) {
	payload, err := json.Marshal(struct {
		Body        string `json:"body"`
		To          string `json:"to,omitempty"`
		ContentType string `json:"content_type,omitempty"`
	}{Body: text, To: to, ContentType: contentType})
	if err != nil {
		return sendOut{}, fmt.Errorf("encoding send payload: %w", err)
	}
	body, err := c.do(ctx, http.MethodPost, "/squawks", payload)
	if err != nil {
		return sendOut{}, err
	}
	var out sendOut
	if len(body) > 0 { // an older Jam answers 204, no body
		if err := json.Unmarshal(body, &out); err != nil {
			return sendOut{}, fmt.Errorf("decoding send response: %w", err)
		}
	}
	return out, nil
}

// read fetches the cove's inbox via Jam, optionally seeking via in's
// anchor/id/dir/limit (each forwarded as a query param only when non-empty).
func (c *messagingClient) read(ctx context.Context, in readIn) (readOut, error) {
	q := url.Values{}
	if in.Anchor != "" {
		q.Set("anchor", in.Anchor)
	}
	if in.ID != "" {
		q.Set("id", in.ID)
	}
	if in.Dir != "" {
		q.Set("dir", in.Dir)
	}
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	path := "/squawks"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return readOut{}, err
	}
	var out readOut
	if err := json.Unmarshal(body, &out); err != nil {
		return readOut{}, fmt.Errorf("decoding Jam messages response")
	}
	return out, nil
}

// commit advances the actor's durable read cursor via Jam, marking
// messages up to and including upTo as processed.
func (c *messagingClient) commit(ctx context.Context, upTo string) (commitOut, error) {
	payload, err := json.Marshal(struct {
		UpTo string `json:"up_to"`
	}{UpTo: upTo})
	if err != nil {
		return commitOut{}, err
	}
	body, err := c.do(ctx, http.MethodPost, "/squawks/commit", payload)
	if err != nil {
		return commitOut{}, err
	}
	var out commitOut
	if err := json.Unmarshal(body, &out); err != nil {
		return commitOut{}, fmt.Errorf("decoding Jam commit response")
	}
	return out, nil
}

// listTargets fetches the actor's addressable send targets via Jam.
func (c *messagingClient) listTargets(ctx context.Context) (targetsOut, error) {
	body, err := c.do(ctx, http.MethodGet, "/squawks/targets", nil)
	if err != nil {
		return targetsOut{}, err
	}
	var out targetsOut
	if err := json.Unmarshal(body, &out); err != nil {
		return targetsOut{}, fmt.Errorf("decoding Jam targets response")
	}
	return out, nil
}

// escalate declares the cove's current block category via Jam's
// POST /escalate. This only categorizes the block for the escalation
// engine's tier routing — it does not itself trigger a page.
func (c *messagingClient) escalate(ctx context.Context, category string) error {
	payload, err := json.Marshal(struct {
		Category string `json:"category"`
	}{Category: category})
	if err != nil {
		return fmt.Errorf("encoding escalate payload: %w", err)
	}
	_, err = c.do(ctx, http.MethodPost, "/escalate", payload)
	return err
}

// end asks Jam (POST /end) to tear the cove down once this turn ends.
func (c *messagingClient) end(ctx context.Context, reason string) error {
	payload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	if err != nil {
		return fmt.Errorf("encoding end payload: %w", err)
	}
	_, err = c.do(ctx, http.MethodPost, "/end", payload)
	return err
}

// idleTimeout overrides the cove's idle timeout (PUT /idle).
func (c *messagingClient) idleTimeout(ctx context.Context, duration, scope string) error {
	payload, err := json.Marshal(struct {
		Duration string `json:"duration"`
		Scope    string `json:"scope"`
	}{Duration: duration, Scope: scope})
	if err != nil {
		return fmt.Errorf("encoding idle payload: %w", err)
	}
	_, err = c.do(ctx, http.MethodPut, "/idle", payload)
	return err
}

// report updates the cove's ticket (POST /report).
func (c *messagingClient) report(ctx context.Context, state, summary, pr string) error {
	payload, err := json.Marshal(struct {
		State   string `json:"state"`
		Summary string `json:"summary"`
		PR      string `json:"pr,omitempty"`
	}{State: state, Summary: summary, PR: pr})
	if err != nil {
		return fmt.Errorf("encoding report payload: %w", err)
	}
	_, err = c.do(ctx, http.MethodPost, "/report", payload)
	return err
}

// setAlarm sets (or replaces) a named alarm (PUT /alarms/{name}).
func (c *messagingClient) setAlarm(ctx context.Context, name, schedule, note, gate string) error {
	payload, err := json.Marshal(struct {
		Schedule string `json:"schedule"`
		Note     string `json:"note,omitempty"`
		Gate     string `json:"gate,omitempty"`
	}{Schedule: schedule, Note: note, Gate: gate})
	if err != nil {
		return fmt.Errorf("encoding alarm payload: %w", err)
	}
	_, err = c.do(ctx, http.MethodPut, "/alarms/"+url.PathEscape(name), payload)
	return err
}

// clearAlarm removes a named alarm (DELETE /alarms/{name}).
func (c *messagingClient) clearAlarm(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodDelete, "/alarms/"+url.PathEscape(name), nil)
	return err
}

// listAlarms lists the cove's alarms (GET /alarms).
func (c *messagingClient) listAlarms(ctx context.Context) (alarmsOut, error) {
	body, err := c.do(ctx, http.MethodGet, "/alarms", nil)
	if err != nil {
		return alarmsOut{}, err
	}
	var out alarmsOut
	if err := json.Unmarshal(body, &out); err != nil {
		return alarmsOut{}, fmt.Errorf("decoding Jam alarms response")
	}
	return out, nil
}

// newMessagingServer builds the "intercom" MCP server exposing read/send.
//
// Configuration errors (missing env vars, a malformed address) are captured
// once at construction time and surfaced by every tool call as a clear,
// token-free error — this keeps the constructor signature simple (mirrors
// the go-sdk's own examples of "*Server, no error") while still failing
// loudly the first time a tool is actually invoked. runMCP additionally
// validates the environment up front, before ever starting the transport, so
// a misconfigured cove fails fast with a readable stderr message rather than
// waiting for claude to attempt its first tool call.
func newMessagingServer(getenv func(string) string) *mcp.Server {
	client, cfgErr := newMessagingClient(getenv)

	s := mcp.NewServer(&mcp.Implementation{Name: "intercom", Version: "0.1.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "send",
		Description: "Post a message. Omit 'to' to post in your default channel — your ticket's, or a chat with whoever started your session; set to=user:<name> to talk with a person (their reply reaches you), chat:user:<a>,user:<b> for a group, channel:<room> for a room, or ticket:<key> for a ticket's conversation. Returns the message id and the channel it went to. The body is markdown by default; set content_type=text/plain to have it shown literally.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sendIn) (*mcp.CallToolResult, sendOut, error) {
		if cfgErr != nil {
			return nil, sendOut{}, cfgErr
		}
		out, err := client.send(ctx, in.Text, in.To, in.ContentType)
		if err != nil {
			return nil, sendOut{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read",
		Description: "Read your inbox as a queue: messages to you, each with the channel it's in and who sent it. Default: the next unprocessed messages after your commit cursor (oldest first). Use anchor/dir/limit to seek (start/end/id, forward/backward). Reading does NOT mark anything processed — call `commit` for that.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, readOut, error) {
		if cfgErr != nil {
			return nil, readOut{}, cfgErr
		}
		out, err := client.read(ctx, in)
		if err != nil {
			return nil, readOut{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "commit",
		Description: "Confirm you've processed your inbox up to this message id; advances your durable read cursor so you won't be handed those messages again. Call it after you've durably handled them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commitIn) (*mcp.CallToolResult, commitOut, error) {
		if cfgErr != nil {
			return nil, commitOut{}, cfgErr
		}
		out, err := client.commit(ctx, in.UpTo)
		if err != nil {
			return nil, commitOut{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_targets",
		Description: "List the targets this cove may send to (ticket:<key> / user:<name> / channel:<room>).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listTargetsIn) (*mcp.CallToolResult, targetsOut, error) {
		if cfgErr != nil {
			return nil, targetsOut{}, cfgErr
		}
		out, err := client.listTargets(ctx)
		if err != nil {
			return nil, targetsOut{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "escalate",
		Description: "Declare the category of your current block so Jam routes the escalation to the right on-call tier. Call this before you finish a turn needing input; it categorizes, it does not itself page anyone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in escalateIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		if err := client.escalate(ctx, in.Category); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "end",
		Description: "End this session for good once the current turn finishes: do any wrap-up first, then call this as your last action. Irrevocable; the session is never woken again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in endIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		return nil, nil, client.end(ctx, in.Reason)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "idle_timeout",
		Description: "Change how long after this turn ends Jam waits before applying the role's on-idle action (wake you, or end the session) if nothing else wakes you. scope next = only the next turn end; always = until the session ends. duration off disables it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idleTimeoutIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		return nil, nil, client.idleTimeout(ctx, in.Duration, in.Scope)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "report",
		Description: "Update your ticket's state (ticket sessions only): in-progress, in-review (with pr), needs-input (put the question in summary), blocked, or done. Jam moves the ticket and comments. Call it as often as the state changes; it does not end the session — end does.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in reportIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		return nil, nil, client.report(ctx, in.State, in.Summary, in.PR)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "alarm_set",
		Description: "Set (or replace) a named alarm that wakes you after your turn ends: schedule is an RFC 3339 time (once) or a 5-field cron expression in the role's time zone (recurring, at most once a minute); note is what the wake tells you to do; gate optionally decides whether a due alarm wakes you. Up to 20 alarms. Alarms due while you are working fire when your turn ends.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in alarmSetIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		return nil, nil, client.setAlarm(ctx, in.Name, in.Schedule, in.Note, in.Gate)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "alarm_clear",
		Description: "Remove a named alarm.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in alarmNameIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		return nil, nil, client.clearAlarm(ctx, in.Name)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "alarm_list",
		Description: "List your alarms with their next fire time.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ alarmListIn) (*mcp.CallToolResult, alarmsOut, error) {
		if cfgErr != nil {
			return nil, alarmsOut{}, cfgErr
		}
		out, err := client.listAlarms(ctx)
		if err != nil {
			return nil, alarmsOut{}, err
		}
		return nil, out, nil
	})

	return s
}

// runMCP runs the stdio MCP server until the client disconnects or ctx (here,
// process signals are not wired — the transport's own stdin EOF ends the
// run) completes. It validates the environment up front so a misconfigured
// cove fails fast with a clear, token-free message on stderr rather than
// waiting for claude's first tool call.
func runMCP(getenv func(string) string, stderr *os.File) int {
	if _, err := newMessagingClient(getenv); err != nil {
		fmt.Fprintln(stderr, "cove-master mcp:", err)
		return 2
	}
	s := newMessagingServer(getenv)
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(stderr, "cove-master mcp:", err)
		return 1
	}
	return 0
}
