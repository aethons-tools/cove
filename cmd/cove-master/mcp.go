// The mcp subcommand ("cove-master mcp") runs a stdio Model Context Protocol
// server that gives the cove's claude agent tools — "read" and "send" brokered
// through harbor's /messages endpoint on the cove's own ticket (or, via an
// optional "to" target, another authorized human/channel), and "list_targets"
// brokered through harbor's GET /messages/targets endpoint.
//
// Security notes (see AGENTS.md / the harbor messaging MCP plan):
//   - The identity token is read from AT_HARBOR_IDENTITY_TOKEN only — never
//     accepted as a flag/argv, and never logged.
//   - Requests to harbor go over TLS (https://<host>) through the default
//     transport, which honors ProxyFromEnvironment (the squid CONNECT proxy)
//     and the system trust store.
//   - Non-2xx responses from harbor are turned into a generic tool error that
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
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxHarborResponseBytes bounds how much of a harbor response we ever read
// into memory, whether success or failure.
const maxHarborResponseBytes = 1 << 20 // 1 MiB

// messageOut mirrors one entry of harbor's GET /messages response.
type messageOut struct {
	ID     string `json:"id,omitempty"`
	Author string `json:"author,omitempty"`
	Body   string `json:"body,omitempty"`
	At     string `json:"at,omitempty"`
}

// sendIn is the "send" tool's typed input.
type sendIn struct {
	Text string `json:"text" jsonschema:"the message body to post to the cove's ticket"`
	To   string `json:"to,omitempty" jsonschema:"optional target: human:<name> or channel:<name>; omit to post to this cove's own ticket"`
}

// readIn is the "read" tool's (empty) typed input.
type readIn struct{}

// readOut is the "read" tool's typed output: the cove's inbox.
type readOut struct {
	Messages []messageOut `json:"messages"`
}

// listTargetsIn is the "list_targets" tool's (empty) typed input.
type listTargetsIn struct{}

// escalateIn is the "escalate" tool's typed input.
type escalateIn struct {
	Category string `json:"category" jsonschema:"the block category to route escalation by, e.g. infra / ticket-blocked / code-architecture (free-form; unknown falls back to the default tier chain)"`
}

// targetItem mirrors one entry of harbor's GET /messages/targets response.
type targetItem struct {
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
}

// targetsOut is the "list_targets" tool's typed output.
type targetsOut struct {
	Targets []targetItem `json:"targets"`
}

// messagingClient forwards read/send calls to harbor's /messages endpoint.
type messagingClient struct {
	http    *http.Client
	baseURL string // e.g. "https://harbor.example.com", no trailing slash
	token   string
}

// harborBaseURL derives the https base URL harbor's /messages endpoint is
// served from, given AT_HARBOR_RUNTIME_ADDR.
//
// In production that variable is "host:443" (per cove-master's Attach dial
// config) — the port is stripped since :443 is implied by https. Tests may
// instead pass a full URL (e.g. an httptest server's http://127.0.0.1:PORT),
// which is used as-is: the scheme is only defaulted to https when the value
// doesn't already carry one.
func harborBaseURL(addr string) (string, error) {
	if addr == "" {
		return "", fmt.Errorf("AT_HARBOR_RUNTIME_ADDR is required")
	}
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", fmt.Errorf("invalid AT_HARBOR_RUNTIME_ADDR: %w", err)
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
	base, err := harborBaseURL(getenv("AT_HARBOR_RUNTIME_ADDR"))
	if err != nil {
		return nil, err
	}
	token := getenv("AT_HARBOR_IDENTITY_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("AT_HARBOR_IDENTITY_TOKEN is required")
	}
	return &messagingClient{
		// nil Transport falls back to http.DefaultTransport: system TLS trust
		// plus ProxyFromEnvironment, so the squid CONNECT proxy is honored.
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: base,
		token:   token,
	}, nil
}

// do issues an authenticated request to harbor at c.baseURL+pathSuffix and
// returns the response body (capped) on a 2xx status. Any error returned is
// generic: it never contains the bearer token, and never echoes the raw
// response body from harbor.
func (c *messagingClient) do(ctx context.Context, method, pathSuffix string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathSuffix, reqBody)
	if err != nil {
		return nil, fmt.Errorf("building harbor messages request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("harbor messages request failed")
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxHarborResponseBytes))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("harbor messages: unexpected status %d", resp.StatusCode)
	}
	return respBody, nil
}

// send posts a message to the cove's own ticket via harbor, or, when to is
// non-empty, to the authorized human/channel target it names.
func (c *messagingClient) send(ctx context.Context, text, to string) error {
	payload, err := json.Marshal(struct {
		Body string `json:"body"`
		To   string `json:"to,omitempty"`
	}{Body: text, To: to})
	if err != nil {
		return fmt.Errorf("encoding send payload: %w", err)
	}
	_, err = c.do(ctx, http.MethodPost, "/messages", payload)
	return err
}

// read fetches the cove's inbox via harbor.
func (c *messagingClient) read(ctx context.Context) (readOut, error) {
	body, err := c.do(ctx, http.MethodGet, "/messages", nil)
	if err != nil {
		return readOut{}, err
	}
	var out readOut
	if err := json.Unmarshal(body, &out); err != nil {
		return readOut{}, fmt.Errorf("decoding harbor messages response")
	}
	return out, nil
}

// listTargets fetches the actor's addressable send targets via harbor.
func (c *messagingClient) listTargets(ctx context.Context) (targetsOut, error) {
	body, err := c.do(ctx, http.MethodGet, "/messages/targets", nil)
	if err != nil {
		return targetsOut{}, err
	}
	var out targetsOut
	if err := json.Unmarshal(body, &out); err != nil {
		return targetsOut{}, fmt.Errorf("decoding harbor targets response")
	}
	return out, nil
}

// escalate declares the cove's current block category via harbor's
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

// newMessagingServer builds the "messaging" MCP server exposing read/send.
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

	s := mcp.NewServer(&mcp.Implementation{Name: "messaging", Version: "0.1.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "send",
		Description: "Post a message. Omit 'to' for this cove's own ticket; set to=human:<name> to @-mention a person (their reply reaches you), or to=channel:<name> to post to a channel.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sendIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		if err := client.send(ctx, in.Text, in.To); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read",
		Description: "Read this cove's inbox: comments on its own Linear ticket.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ readIn) (*mcp.CallToolResult, readOut, error) {
		if cfgErr != nil {
			return nil, readOut{}, cfgErr
		}
		out, err := client.read(ctx)
		if err != nil {
			return nil, readOut{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_targets",
		Description: "List the targets this cove may send to (human:<name> / channel:<name>).",
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
		Description: "Declare the category of your current block so harbor routes the escalation to the right on-call tier. Call this before you finish a turn needing input; it categorizes, it does not itself page anyone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in escalateIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		if err := client.escalate(ctx, in.Category); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
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
