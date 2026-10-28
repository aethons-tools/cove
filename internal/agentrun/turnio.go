package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TurnIO is what the structured turn end needs from Jam: the session's
// pending messages (to deliver in a wake) and the calls a turn end makes on
// the agent's behalf. Every call is self-scoped by the cove's identity token.
type TurnIO interface {
	// Inbox returns up to limit entries past the session's commit cursor,
	// oldest first; more reports whether others wait beyond them.
	Inbox(ctx context.Context, limit int) (es []InboxEntry, more bool, err error)
	// Post posts text as the session, marked as a turn end's (auto): to its
	// home channel, or to the target to names.
	Post(ctx context.Context, text, to string) error
	// Commit advances the session's commit cursor to upTo.
	Commit(ctx context.Context, upTo string) error
	End(ctx context.Context, reason string) error
	// SetAlarm sets a one-shot alarm at at (RFC 3339).
	SetAlarm(ctx context.Context, name, at, note string) error
	// SetIdle overrides the idle timeout for the next turn end only.
	SetIdle(ctx context.Context, duration string) error
	// Mute sets whether new messages stop waking the session until it next
	// runs; ErrUnsupported from a Jam without it.
	Mute(ctx context.Context, on bool) error
	// SetKept tells Jam how many processes the cove keeps running, so it
	// doesn't pause the studio while any run; ErrUnsupported from an older Jam.
	SetKept(ctx context.Context, n int) error
}

// ErrUnsupported is a TurnIO call this Jam doesn't serve (an older Jam).
var ErrUnsupported = errors.New("agentrun: not supported by this Jam")

// InboxEntry is one inbox entry as GET /squawks returns it.
type InboxEntry struct {
	ID          string      `json:"id"`
	Author      string      `json:"author"`
	Body        string      `json:"body"`
	At          string      `json:"at"`
	ContentType string      `json:"content_type"`
	Channel     InboxParty  `json:"channel"`
	From        InboxParty  `json:"from"`
	Parts       []InboxPart `json:"parts"`
}

// InboxParty is a channel or author of an InboxEntry.
type InboxParty struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// InboxPart is a typed part of an InboxEntry: an attachment (Ref) or inline.
type InboxPart struct {
	Type string `json:"type"`
	Ref  string `json:"ref"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// turnIOTimeout bounds every TurnIO call: a turn end never stalls on Jam.
const turnIOTimeout = 10 * time.Second

// HTTPTurnIO is the TurnIO over Jam's cove-facing endpoints at baseURL
// (https://<jam host>), authenticated with the cove's identity token.
func HTTPTurnIO(baseURL, token string) TurnIO {
	return httpTurnIO{hc: &http.Client{Timeout: turnIOTimeout}, base: strings.TrimRight(baseURL, "/"), token: token}
}

type httpTurnIO struct {
	hc          *http.Client
	base, token string
}

// do sends one request; the error never carries the token or Jam's body.
func (c httpTurnIO) do(ctx context.Context, method, path string, in any) ([]byte, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: request failed", method, path)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		if path == "/mute" || path == "/kept" {
			return nil, ErrUnsupported
		}
		return nil, fmt.Errorf("%s %s: %s", method, path, resp.Status)
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return out, nil
}

func (c httpTurnIO) Inbox(ctx context.Context, limit int) ([]InboxEntry, bool, error) {
	q := url.Values{"limit": {strconv.Itoa(limit + 1)}}
	b, err := c.do(ctx, http.MethodGet, "/squawks?"+q.Encode(), nil)
	if err != nil {
		return nil, false, err
	}
	var page struct {
		Squawks []InboxEntry `json:"squawks"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		return nil, false, fmt.Errorf("GET /squawks: %w", err)
	}
	if len(page.Squawks) > limit {
		return page.Squawks[:limit], true, nil
	}
	return page.Squawks, false, nil
}

func (c httpTurnIO) Post(ctx context.Context, text, to string) error {
	_, err := c.do(ctx, http.MethodPost, "/squawks", struct {
		Body string `json:"body"`
		To   string `json:"to,omitempty"`
		Auto bool   `json:"auto"`
	}{text, to, true})
	return err
}

func (c httpTurnIO) Commit(ctx context.Context, upTo string) error {
	_, err := c.do(ctx, http.MethodPost, "/squawks/commit", struct {
		UpTo string `json:"up_to"`
	}{upTo})
	return err
}

func (c httpTurnIO) End(ctx context.Context, reason string) error {
	_, err := c.do(ctx, http.MethodPost, "/end", struct {
		Reason string `json:"reason"`
	}{reason})
	return err
}

func (c httpTurnIO) SetAlarm(ctx context.Context, name, at, note string) error {
	_, err := c.do(ctx, http.MethodPut, "/alarms/"+url.PathEscape(name), struct {
		Schedule string `json:"schedule"`
		Note     string `json:"note,omitempty"`
	}{at, note})
	return err
}

func (c httpTurnIO) SetIdle(ctx context.Context, duration string) error {
	_, err := c.do(ctx, http.MethodPut, "/idle", struct {
		Duration string `json:"duration"`
		Scope    string `json:"scope"`
	}{duration, "next"})
	return err
}

func (c httpTurnIO) SetKept(ctx context.Context, n int) error {
	_, err := c.do(ctx, http.MethodPut, "/kept", struct {
		Count int `json:"count"`
	}{n})
	return err
}

func (c httpTurnIO) Mute(ctx context.Context, on bool) error {
	_, err := c.do(ctx, http.MethodPut, "/mute", struct {
		On bool `json:"on"`
	}{on})
	return err
}
