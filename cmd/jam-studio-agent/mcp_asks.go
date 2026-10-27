package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The ask tools (intercom structured asks): a session asks a one-of-N
// question in a channel; a person answers in the Jam app's Inbox (/app/user), and the answer arrives
// through read as an entry carrying its ask part.

// partOut mirrors a squawk part in Jam's responses; an ask part's data is
// {event, ask_id, version, choice, comment, …}.
type partOut struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
	// An attachment: its ref (fetch it with fetch), file name and size.
	Ref  string `json:"ref,omitempty"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// askOption is one answer an ask offers.
type askOption struct {
	ID     string `json:"id" jsonschema:"short id: lowercase letters, digits, - and _ (not other)"`
	Label  string `json:"label" jsonschema:"what the person clicks"`
	Detail string `json:"detail,omitempty" jsonschema:"optional one-line explanation under the label"`
}

type askIn struct {
	Question string      `json:"question" jsonschema:"the question (markdown)"`
	Options  []askOption `json:"options" jsonschema:"2 to 8 answers; the person can also answer Other with a comment"`
	To       string      `json:"to,omitempty" jsonschema:"optional target, as for send; omit to ask in your own channel"`
	Of       []string    `json:"of,omitempty" jsonschema:"optional: only these people may answer (user:<name>); omit for anyone in the channel"`
}

type askOut struct {
	AskID   string   `json:"ask_id"`
	Version int      `json:"version"`
	Channel partyOut `json:"channel"`
}

type amendIn struct {
	AskID    string      `json:"ask_id" jsonschema:"the ask to reword (yours, still open)"`
	Question string      `json:"question,omitempty" jsonschema:"new question; omit to keep it"`
	Options  []askOption `json:"options,omitempty" jsonschema:"new options; omit to keep them"`
}

type amendOut struct {
	Version int `json:"version"`
}

type withdrawIn struct {
	AskID  string `json:"ask_id" jsonschema:"the ask to close unanswered (yours, still open)"`
	Reason string `json:"reason,omitempty" jsonschema:"optional: why, shown to people"`
}

type asksIn struct {
	State string `json:"state,omitempty" jsonschema:"optional filter: open, answered or withdrawn"`
}

type askItem struct {
	AskID    string      `json:"ask_id"`
	Channel  partyOut    `json:"channel"`
	Question string      `json:"question"`
	Options  []askOption `json:"options"`
	Version  int         `json:"version"`
	State    string      `json:"state"`
	Answer   *struct {
		Version int    `json:"version"`
		Choice  string `json:"choice"`
		Comment string `json:"comment,omitempty"`
		By      string `json:"by"`
		At      string `json:"at"`
	} `json:"answer,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type asksOut struct {
	Asks []askItem `json:"asks"`
}

// postJSON posts in to Jam at path and decodes the reply into out (nil: none).
func (c *messagingClient) postJSON(ctx context.Context, path string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	body, err := c.do(ctx, http.MethodPost, path, payload)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding Jam %s response", path)
	}
	return nil
}

func (c *messagingClient) listAsks(ctx context.Context, state string) (asksOut, error) {
	path := "/squawks/asks"
	if state != "" {
		path += "?" + url.Values{"state": {state}}.Encode()
	}
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return asksOut{}, err
	}
	var out asksOut
	if err := json.Unmarshal(body, &out); err != nil {
		return asksOut{}, fmt.Errorf("decoding Jam asks response")
	}
	if out.Asks == nil {
		out.Asks = []askItem{}
	}
	return out, nil
}

// addAskTools registers ask, amend, withdraw and asks on s.
func addAskTools(s *mcp.Server, client *messagingClient, cfgErr error) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "ask",
		Description: "Ask people a question with 2-8 answers to pick from (plus Other with a comment), in your own channel or `to` (as for send). It returns at once with the ask's id: keep working or end your turn; the answer arrives through read as an entry whose parts carry {event: answer, ask_id, choice, comment}. Open asks put you under Needs you. Several can be open at once.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in askIn) (*mcp.CallToolResult, askOut, error) {
		if cfgErr != nil {
			return nil, askOut{}, cfgErr
		}
		var out askOut
		return nil, out, client.postJSON(ctx, "/squawks/ask", in, &out)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "amend",
		Description: "Reword one of your open asks (question and/or options). An answer to the old wording is refused, so nobody approves something they didn't see.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in amendIn) (*mcp.CallToolResult, amendOut, error) {
		if cfgErr != nil {
			return nil, amendOut{}, cfgErr
		}
		var out amendOut
		return nil, out, client.postJSON(ctx, "/squawks/ask/amend", in, &out)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "withdraw",
		Description: "Close one of your open asks unanswered (e.g. you found the answer), with an optional reason.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in withdrawIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		return nil, nil, client.postJSON(ctx, "/squawks/ask/withdraw", in, nil)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "asks",
		Description: "List your asks and their state (open, answered with the answer, or withdrawn) — e.g. to pick up after a restart.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in asksIn) (*mcp.CallToolResult, asksOut, error) {
		if cfgErr != nil {
			return nil, asksOut{}, cfgErr
		}
		out, err := client.listAsks(ctx, in.State)
		return nil, out, err
	})
}
